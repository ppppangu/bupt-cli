package tuiprefs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// Draft holds only form controls. It deliberately has no free text, result,
// key identifier, command, credentials, or previously granted authorization.
type Draft struct {
	Version      int       `json:"version"`
	Updated      time.Time `json:"updated"`
	Runtime      string    `json:"runtime,omitempty"`
	AllocateOnly bool      `json:"allocate_only,omitempty"`
	MaxTokens    int       `json:"max_tokens,omitempty"`
	TopN         int       `json:"top_n,omitempty"`
}

const draftVersion = 1
const draftLifetime = 14 * 24 * time.Hour

func draftPath(stateDir, scope string) string {
	digest := sha256.Sum256([]byte(scope))
	return filepath.Join(stateDir, "tui-drafts", hex.EncodeToString(digest[:])+".json")
}

func ReadDraft(stateDir, scope string) (Draft, error) {
	name := draftPath(stateDir, scope)
	if err := draftDirectory(filepath.Dir(name), false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Draft{}, nil
		}
		return Draft{}, err
	}
	f, err := os.OpenFile(name, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return Draft{}, nil
	}
	if err != nil {
		return Draft{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Draft{}, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 4096 {
		return Draft{}, fmt.Errorf("草稿须为权限 0600 的小型普通文件")
	}
	body, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return Draft{}, err
	}
	var d Draft
	if len(body) > 4096 || json.Unmarshal(body, &d) != nil {
		return Draft{}, fmt.Errorf("本地草稿无法读取")
	}
	if d.Version != 0 && d.Version != draftVersion {
		return Draft{}, fmt.Errorf("本地草稿版本不受支持")
	}
	if d.Version == 0 && d.Updated.IsZero() {
		d.Updated = st.ModTime()
	}
	if age := time.Since(d.Updated); d.Updated.IsZero() || age > draftLifetime || age < -time.Minute {
		return Draft{}, nil
	}
	if !validDraft(d) {
		return Draft{}, fmt.Errorf("本地草稿的参数无效")
	}
	return d, nil
}

func validDraft(d Draft) bool {
	runtime, err := time.ParseDuration(d.Runtime)
	return (d.Runtime == "" || err == nil && runtime > 0) && d.MaxTokens >= 0 && d.MaxTokens <= 4096 && d.TopN >= 0 && d.TopN <= 16
}

func draftDirectory(dir string, create bool) error {
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("草稿目录须为权限 0700 的真实目录")
	}
	return nil
}

func WriteDraft(stateDir, scope string, d Draft) error {
	if !validDraft(d) {
		return fmt.Errorf("草稿参数无效")
	}
	name := draftPath(stateDir, scope)
	dir := filepath.Dir(name)
	if err := draftDirectory(dir, true); err != nil {
		return err
	}
	d.Version, d.Updated = draftVersion, time.Now().UTC()
	f, err := os.CreateTemp(dir, ".draft-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(d)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), name); err != nil {
		return err
	}
	return syncDraftDirectory(dir)
}

func ClearDraft(stateDir, scope string) error {
	name := draftPath(stateDir, scope)
	dir := filepath.Dir(name)
	if err := draftDirectory(dir, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(name); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncDraftDirectory(dir)
}

func syncDraftDirectory(name string) error {
	dir, err := os.Open(name)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
