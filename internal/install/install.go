// Package install owns explicit workstation and controller setup. It does not
// change firewall rules, the system SSH daemon, or platform-owned keys.
package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"bupt.local/cli/internal/client"
	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
)

type rpcFunc func(context.Context, *config.Config, string, any, any) error
type runFunc func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error

// handler keeps operating-system effects injectable. Tests use temp paths and
// fake runners; the exported entry point selects the real local environment.
type handler struct {
	goos, home, executable, userConfigHome string
	uid                                    int
	run                                    runFunc
	rpc                                    rpcFunc
	dial                                   func(context.Context, domain.RelayConfig) (remote, error)
	start                                  func(string, []string, string) (int, error)
	wait                                   func(context.Context, time.Duration) error
	now                                    func() time.Time
}

func newHandler() (*handler, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	return &handler{
		goos: runtime.GOOS, home: home, executable: executable, userConfigHome: first(os.Getenv("XDG_CONFIG_HOME"), filepath.Join(home, ".config")), uid: os.Getuid(), now: time.Now,
		run: func(ctx context.Context, name string, args []string, in io.Reader, out, errOut io.Writer) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = in, out, errOut
			return cmd.Run()
		},
		rpc: func(ctx context.Context, cfg *config.Config, method string, params, out any) error {
			c, err := client.New(cfg, cfg.Selected)
			if err != nil {
				return err
			}
			return c.Call(ctx, method, params, out)
		},
		dial: dialRemote, start: startDetached,
	}, nil
}

// Handle performs only the requested, explicit setup operation. Caller-level
// --dry-run is honored before filesystem writes, connections or service starts.
func Handle(ctx context.Context, cfg *config.Config, method string, params map[string]any, in io.Reader, out, errOut io.Writer) (any, error) {
	h, err := newHandler()
	if err != nil {
		return nil, err
	}
	return h.handle(ctx, cfg, method, params, in, out, errOut)
}

func (h *handler) handle(ctx context.Context, cfg *config.Config, method string, params map[string]any, in io.Reader, out, errOut io.Writer) (any, error) {
	if cfg == nil {
		return nil, domain.E("CONFIG_REQUIRED", "configuration is required")
	}
	// Service managers do not inherit the caller's working directory. Resolve
	// paths once and reject line breaks before generating service definitions.
	resolved := *cfg
	cfg = &resolved
	for _, path := range []*string{&cfg.Paths.ConfigDir, &cfg.Paths.DataDir, &cfg.Paths.StateDir, &cfg.Paths.ConfigFile, &cfg.Paths.Socket} {
		value, err := h.path(*path)
		if err != nil {
			return nil, err
		}
		*path = value
	}
	if !noControls(h.executable) {
		return nil, domain.E("INVALID_PATH", "executable path contains control characters")
	}
	if params == nil {
		params = map[string]any{}
	}
	for _, value := range params {
		if text, ok := value.(string); ok && !noControls(text) {
			return nil, domain.E("INVALID_ARGUMENT", "setup arguments must not contain control characters")
		}
	}
	if out == nil {
		out = io.Discard
	}
	if errOut == nil {
		errOut = io.Discard
	}
	switch method {
	case "workspace.start":
		return h.startWorkspace(ctx, cfg, params, errOut)
	case "init", "agent.install":
		return h.setup(ctx, cfg, method, params, in, out, errOut)
	case "agent.stop":
		return h.stop(ctx, cfg, params, errOut)
	case "key.generate", "key.import":
		return h.key(ctx, cfg, method, params)
	case "ssh-config.export", "ssh-config.install":
		return h.sshConfig(ctx, cfg, method, params, out)
	default:
		return nil, domain.E("UNSUPPORTED_LOCAL_OPERATION", "unsupported setup operation")
	}
}

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,80}$`)

func paramString(p map[string]any, key string) string {
	v, _ := p[key].(string)
	return v
}
func paramBool(p map[string]any, key string, fallback bool) bool {
	v, ok := p[key].(bool)
	if !ok {
		return fallback
	}
	return v
}
func paramInt(p map[string]any, key string, fallback int) int {
	switch v := p[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return int(i)
		}
	}
	return fallback
}
func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func noControls(s string) bool { return !strings.ContainsAny(s, "\r\n\x00") }
func (h *handler) path(value string) (string, error) {
	if value == "~" {
		value = h.home
	} else if strings.HasPrefix(value, "~/") {
		value = filepath.Join(h.home, strings.TrimPrefix(value, "~/"))
	} else if strings.HasPrefix(value, "~") {
		return "", domain.E("INVALID_PATH", "another user's home expansion is not supported")
	}
	if value == "" || !noControls(value) {
		return "", domain.E("INVALID_PATH", "a path without control characters is required")
	}
	return filepath.Abs(value)
}

func ensurePrivateDir(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(path, 0700)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return domain.E("UNSAFE_DIRECTORY", "setup directory must be a real directory")
	}
	if info.Mode().Perm()&0077 != 0 {
		return domain.E("INSECURE_DIRECTORY", fmt.Sprintf("directory %s must have mode 0700", path))
	}
	return nil
}

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, domain.E("UNSAFE_FILE", "refusing a symlink or non-regular file")
	}
	if info.Size() > limit {
		return nil, domain.E("FILE_TOO_LARGE", "setup input exceeds its size limit")
	}
	return os.ReadFile(path)
}

// replaceFile backs up the exact previous contents and replaces atomically.
// Its caller supplies the narrowly scoped target; symlinks are never followed.
func (h *handler) replaceFile(path string, data []byte, mode os.FileMode) (string, error) {
	var backup string
	old, err := readRegular(path, 16<<20)
	if err == nil {
		if string(old) == string(data) {
			return "", os.Chmod(path, mode)
		}
		backup = fmt.Sprintf("%s.bupt-backup-%s", path, h.now().UTC().Format("20060102T150405.000000000"))
		if err = writeNew(backup, old, 0600); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return backup, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".bupt-write-*")
	if err != nil {
		return backup, err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return backup, err
	}
	if err = os.Rename(tmp, path); err != nil {
		return backup, err
	}
	return backup, nil
}

func writeNew(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	good := false
	defer func() {
		f.Close()
		if !good {
			os.Remove(path)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	good = true
	return nil
}

func addBackup(backups *[]string, path string) {
	if path != "" {
		*backups = append(*backups, path)
	}
}
