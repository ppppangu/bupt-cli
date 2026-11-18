package connect

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"bupt.local/cli/internal/domain"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type SFTPConnection struct {
	Client *sftp.Client
	SSH    *ssh.Client
	stop   func() bool
}

func (c *SFTPConnection) Close() error {
	if c.stop != nil {
		c.stop()
	}
	e := c.Client.Close()
	e2 := c.SSH.Close()
	if e != nil {
		return e
	}
	return e2
}
func OpenSFTP(ctx context.Context, route domain.Route) (*SFTPConnection, error) {
	client, err := DialSSH(ctx, route)
	if err != nil {
		return nil, err
	}
	files, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, err
	}
	value := &SFTPConnection{Client: files, SSH: client}
	value.stop = context.AfterFunc(ctx, func() { files.Close(); client.Close() })
	return value, nil
}

type FileOptions struct {
	Overwrite bool
	Mkdir     bool
	Mode      fs.FileMode
}
type FileInfo struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Mode      uint32 `json:"mode"`
	Modified  string `json:"modified"`
	IsDir     bool   `json:"is_dir"`
	IsSymlink bool   `json:"is_symlink,omitempty"`
}

func RemotePath(home, requested string) (string, error) {
	if requested == "" || strings.ContainsRune(requested, 0) {
		return "", domain.E("INVALID_PATH", "a non-empty remote path is required")
	}
	if requested == "~" || strings.HasPrefix(requested, "~/") {
		if !path.IsAbs(home) {
			return "", domain.E("REMOTE_HOME_UNKNOWN", "an authenticated remote home directory is required")
		}
		if requested == "~" {
			return path.Clean(home), nil
		}
		return path.Clean(path.Join(home, strings.TrimPrefix(requested, "~/"))), nil
	}
	if strings.HasPrefix(requested, "~") {
		return "", domain.E("INVALID_PATH", "another user's home expansion is not supported")
	}
	if !path.IsAbs(requested) && home != "" {
		requested = path.Join(home, requested)
	}
	return path.Clean(requested), nil
}
func resolvePath(client *sftp.Client, route domain.Route, requested string) (string, error) {
	home := route.Home
	if home == "" {
		var err error
		home, err = client.Getwd()
		if err != nil {
			return "", err
		}
	}
	return RemotePath(home, requested)
}
func Stat(ctx context.Context, route domain.Route, requested string) (FileInfo, error) {
	files, err := OpenSFTP(ctx, route)
	if err != nil {
		return FileInfo{}, err
	}
	defer files.Close()
	name, err := resolvePath(files.Client, route, requested)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := files.Client.Lstat(name)
	if err != nil {
		return FileInfo{}, err
	}
	return fileInfo(info, name), nil
}
func List(ctx context.Context, route domain.Route, requested string) ([]FileInfo, error) {
	files, err := OpenSFTP(ctx, route)
	if err != nil {
		return nil, err
	}
	defer files.Close()
	name, err := resolvePath(files.Client, route, requested)
	if err != nil {
		return nil, err
	}
	entries, err := files.Client.ReadDir(name)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, info := range entries {
		out = append(out, fileInfo(info, path.Join(name, info.Name())))
	}
	return out, nil
}
func fileInfo(info fs.FileInfo, name string) FileInfo {
	return FileInfo{Name: info.Name(), Path: name, Size: info.Size(), Mode: uint32(info.Mode()), Modified: info.ModTime().UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), IsDir: info.IsDir(), IsSymlink: info.Mode()&os.ModeSymlink != 0}
}

// Upload writes a sibling temporary file then publishes it atomically. The
// no-overwrite path uses the OpenSSH hardlink extension to avoid a stat/rename
// race with another writer.
func Upload(ctx context.Context, route domain.Route, requested string, source io.Reader, options FileOptions) (int64, error) {
	files, err := OpenSFTP(ctx, route)
	if err != nil {
		return 0, err
	}
	defer files.Close()
	name, err := resolvePath(files.Client, route, requested)
	if err != nil {
		return 0, err
	}
	if name == "/" || path.Base(name) == "." {
		return 0, domain.E("INVALID_PATH", "destination must name a file")
	}
	if options.Mkdir {
		if err = files.Client.MkdirAll(path.Dir(name)); err != nil {
			return 0, err
		}
	}
	exists := false
	if info, statErr := files.Client.Lstat(name); statErr == nil {
		exists = true
		if info.IsDir() {
			return 0, domain.E("IS_DIRECTORY", "destination is a directory")
		}
		if !options.Overwrite {
			return 0, domain.E("ALREADY_EXISTS", "destination already exists; explicitly request overwrite")
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return 0, statErr
	}
	token := make([]byte, 12)
	if _, err = rand.Read(token); err != nil {
		return 0, err
	}
	temp := path.Join(path.Dir(name), ".bupt-upload-"+hex.EncodeToString(token))
	f, err := files.Client.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return 0, err
	}
	published := false
	defer func() {
		_ = f.Close()
		if !published {
			_ = files.Client.Remove(temp)
		}
	}()
	mode := options.Mode.Perm()
	if mode == 0 {
		mode = 0600
	}
	if err = f.Chmod(mode); err != nil {
		return 0, err
	}
	written, err := io.Copy(f, source)
	if err != nil {
		if ctx.Err() != nil {
			return written, ctx.Err()
		}
		return written, err
	}
	if ctx.Err() != nil {
		return written, ctx.Err()
	}
	if err = f.Close(); err != nil {
		return written, err
	}
	if !options.Overwrite {
		if _, supported := files.Client.HasExtension("hardlink@openssh.com"); !supported {
			return written, domain.E("ATOMIC_PUBLISH_UNSUPPORTED", "server cannot atomically publish without replacing an existing destination")
		}
		err = files.Client.Link(temp, name)
		if err == nil {
			published = true
			_ = files.Client.Remove(temp)
			return written, nil
		}
		if _, statErr := files.Client.Lstat(name); statErr == nil {
			return written, domain.E("ALREADY_EXISTS", "destination appeared during upload; it was not overwritten")
		}
	} else {
		if _, supported := files.Client.HasExtension("posix-rename@openssh.com"); supported {
			err = files.Client.PosixRename(temp, name)
		} else if exists {
			return written, domain.E("ATOMIC_RENAME_UNSUPPORTED", "server cannot atomically replace the existing destination")
		} else {
			err = files.Client.Rename(temp, name)
		}
	}
	if err != nil {
		return written, err
	}
	published = true
	return written, nil
}

func Mkdir(ctx context.Context, route domain.Route, requested string, recursive bool, mode fs.FileMode) error {
	files, err := OpenSFTP(ctx, route)
	if err != nil {
		return err
	}
	defer files.Close()
	name, err := resolvePath(files.Client, route, requested)
	if err != nil {
		return err
	}
	if info, err := files.Client.Lstat(name); err == nil {
		if recursive && info.IsDir() {
			return nil
		}
		return domain.E("ALREADY_EXISTS", "destination already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if recursive {
		err = files.Client.MkdirAll(name)
	} else {
		err = files.Client.Mkdir(name)
	}
	if err != nil {
		return err
	}
	mode = mode.Perm()
	if mode == 0 {
		mode = 0700
	}
	return files.Client.Chmod(name, mode)
}

func protectedPath(client *sftp.Client, route domain.Route, name string) error {
	home := route.Home
	if home == "" {
		var err error
		home, err = client.Getwd()
		if err != nil {
			return err
		}
	}
	if name == "/" || name == path.Clean(home) {
		return domain.E("PROTECTED_PATH", "refusing to remove or rename the filesystem root or instance home")
	}
	return nil
}

// Rename moves the exact path; it does not silently append a source basename
// when the destination is a directory. No-overwrite regular files use hardlink.
func Rename(ctx context.Context, route domain.Route, source, destination string, overwrite bool) error {
	files, err := OpenSFTP(ctx, route)
	if err != nil {
		return err
	}
	defer files.Close()
	old, err := resolvePath(files.Client, route, source)
	if err != nil {
		return err
	}
	name, err := resolvePath(files.Client, route, destination)
	if err != nil {
		return err
	}
	if err = protectedPath(files.Client, route, old); err != nil {
		return err
	}
	if err = protectedPath(files.Client, route, name); err != nil {
		return err
	}
	if old == name {
		return nil
	}
	info, err := files.Client.Lstat(old)
	if err != nil {
		return err
	}
	if _, err = files.Client.Lstat(name); err == nil && !overwrite {
		return domain.E("ALREADY_EXISTS", "destination already exists")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if overwrite {
		if _, ok := files.Client.HasExtension("posix-rename@openssh.com"); !ok {
			return domain.E("ATOMIC_RENAME_UNSUPPORTED", "server cannot atomically replace a destination")
		}
		return files.Client.PosixRename(old, name)
	}
	if info.Mode().IsRegular() {
		if _, ok := files.Client.HasExtension("hardlink@openssh.com"); !ok {
			return domain.E("ATOMIC_PUBLISH_UNSUPPORTED", "server cannot atomically move a file without replacing its destination")
		}
		if err = files.Client.Link(old, name); err != nil {
			return err
		}
		return files.Client.Remove(old)
	}
	// SFTP v3 RENAME refuses existing destinations. Its directory guarantees are
	// those provided by the SFTP server; do not emulate overwrite with deletion.
	return files.Client.Rename(old, name)
}

func Remove(ctx context.Context, route domain.Route, requested string, recursive bool) error {
	files, err := OpenSFTP(ctx, route)
	if err != nil {
		return err
	}
	defer files.Close()
	name, err := resolvePath(files.Client, route, requested)
	if err != nil {
		return err
	}
	if err = protectedPath(files.Client, route, name); err != nil {
		return err
	}
	var remove func(string, int) error
	remove = func(p string, depth int) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if depth > 256 {
			return domain.E("PATH_TOO_DEEP", "directory nesting exceeds the removal limit")
		}
		info, err := files.Client.Lstat(p)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return files.Client.Remove(p)
		}
		if !recursive {
			return files.Client.RemoveDirectory(p)
		}
		entries, err := files.Client.ReadDir(p)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() == "." || entry.Name() == ".." {
				continue
			}
			if strings.Contains(entry.Name(), "/") {
				return domain.E("INVALID_REMOTE_ENTRY", "server returned an invalid directory entry")
			}
			if err = remove(path.Join(p, entry.Name()), depth+1); err != nil {
				return err
			}
		}
		return files.Client.RemoveDirectory(p)
	}
	return remove(name, 0)
}

func Download(ctx context.Context, route domain.Route, requested string, destination io.Writer) (int64, error) {
	files, err := OpenSFTP(ctx, route)
	if err != nil {
		return 0, err
	}
	defer files.Close()
	name, err := resolvePath(files.Client, route, requested)
	if err != nil {
		return 0, err
	}
	f, err := files.Client.Open(name)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, domain.E("NOT_REGULAR_FILE", "source is not a regular file")
	}
	n, err := io.Copy(destination, f)
	if ctx.Err() != nil {
		return n, ctx.Err()
	}
	return n, err
}
