// Package tuiprefs persists the display preferences shared by this user's terminals.
package tuiprefs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/fsnotify/fsnotify"
	"golang.org/x/sys/unix"
)

const filename = "tui.toml"

type Settings struct {
	AutoVerify   bool `toml:"auto_verify"`
	ShowName     bool `toml:"show_name"`
	ShowCampusID bool `toml:"show_campus_id"`
	ShowBalance  bool `toml:"show_balance"`
	ShowRunning  bool `toml:"show_running"`
}

func Defaults() Settings { return Settings{AutoVerify: true, ShowBalance: true, ShowRunning: true} }

func privateDir(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return checkDir(dir)
}

func checkDir(dir string) error {
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("顶栏设置目录须为权限 0700 的真实目录")
	}
	return nil
}

func contents(dir string) ([]byte, error) {
	if err := checkDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(dir, filename), os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const maxSize = 32 << 10
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("tui.toml 须为权限 0600 的普通文件")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxSize {
		return nil, fmt.Errorf("tui.toml 超过 32 KiB")
	}
	return data, nil
}

// Read uses the defaults for an absent file or omitted fields.
func Read(dir string) (Settings, error) {
	data, err := contents(dir)
	if err != nil {
		return Defaults(), err
	}
	return decode(data)
}

func decode(data []byte) (Settings, error) {
	v := Defaults()
	md, err := toml.NewDecoder(bytes.NewReader(data)).Decode(&v)
	if err != nil {
		return Defaults(), err
	}
	if len(md.Undecoded()) != 0 {
		return Defaults(), fmt.Errorf("未知顶栏设置 %s", md.Undecoded()[0])
	}
	return v, nil
}

// Update reads the latest settings under the directory lock so concurrent
// windows editing different fields do not replace one another's changes.
func Update(dir string, edit func(*Settings)) (Settings, error) {
	return write(dir, edit)
}

// Reset also recovers a malformed document when its file and directory are safe.
func Reset(dir string) (Settings, error) {
	return write(dir, nil)
}

func write(dir string, edit func(*Settings)) (Settings, error) {
	if err := privateDir(dir); err != nil {
		return Settings{}, err
	}
	lock, err := os.OpenFile(dir, unix.O_DIRECTORY|unix.O_NOFOLLOW|os.O_RDONLY, 0)
	if err != nil {
		return Settings{}, err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return Settings{}, err
	}
	data, err := contents(dir)
	if err != nil {
		return Settings{}, err
	}
	v := Defaults()
	if edit != nil {
		v, err = decode(data)
		if err != nil {
			return v, err
		}
		edit(&v)
	}
	f, err := os.CreateTemp(dir, ".tui-*")
	if err != nil {
		return v, err
	}
	defer os.Remove(f.Name())
	if err = toml.NewEncoder(f).Encode(v); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, filename))
	}
	return v, err
}

// Watch issues invalidations rather than settings values. Consumers reread
// atomically on their event loop, including after reattaching the directory.
func Watch(ctx context.Context, dir string, changed func()) {
	for ctx.Err() == nil {
		if err := watchDir(ctx, dir, changed); ctx.Err() != nil {
			return
		} else if err != nil {
			changed()
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func watchDir(ctx context.Context, dir string, changed func()) error {
	if err := privateDir(dir); err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err = w.Add(dir); err != nil {
		return err
	}
	changed()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err, ok := <-w.Errors:
			if !ok {
				return fsnotify.ErrClosed
			}
			return err
		case ev, ok := <-w.Events:
			if !ok {
				return fsnotify.ErrClosed
			}
			if ev.Name == dir && ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				return fsnotify.ErrClosed
			}
			if filepath.Base(ev.Name) == filename && ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) != 0 {
				changed()
			}
		}
	}
}
