package tuiprefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDraftRecoveryVersionAndPermissions(t *testing.T) {
	dir := t.TempDir()
	scope := "test/create/spec/9"
	d := Draft{Runtime: "37m", AllocateOnly: true}
	if err := WriteDraft(dir, scope, d); err != nil {
		t.Fatal(err)
	}
	file := draftPath(dir, scope)
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, err)
	}
	if st, err := os.Stat(filepath.Dir(file)); err != nil || st.Mode().Perm() != 0700 {
		t.Fatal(st, err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(file), ".draft-interrupted"), []byte(`{"runtime":`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadDraft(dir, scope)
	if err != nil || got.Runtime != "37m" || !got.AllocateOnly || got.Version != 1 {
		t.Fatal(got, err)
	}
	if got, err := ReadDraft(dir, "another-profile/create/spec/9"); err != nil || !got.Updated.IsZero() {
		t.Fatal(got, err)
	}
	if err := os.WriteFile(file, []byte(`{"runtime":"15m","input":"old test payload","api_key":"dummy-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	legacy, err := ReadDraft(dir, scope)
	if err != nil || legacy.Runtime != "15m" {
		t.Fatal(legacy, err)
	}
	if err := WriteDraft(dir, scope, legacy); err != nil {
		t.Fatal(err)
	}
	current, err := os.ReadFile(file)
	if err != nil || strings.Contains(string(current), "dummy-secret") || strings.Contains(string(current), "old test payload") {
		t.Fatal(string(current), err)
	}

	for _, bad := range []string{`{"version":99,"runtime":"30m"}`, `{"runtime":`, `{"runtime":"-30m"}`} {
		if err := os.WriteFile(file, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadDraft(dir, scope); err == nil {
			t.Fatalf("accepted damaged document %s", bad)
		}
		if err := WriteDraft(dir, scope, d); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte(`{"version":1,"updated":"2020-01-01T00:00:00Z","runtime":"37m"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadDraft(dir, scope); err != nil || !got.Updated.Equal(time.Time{}) {
		t.Fatal(got, err)
	}
	if err := ClearDraft(dir, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
