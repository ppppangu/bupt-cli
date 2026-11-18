package tuiprefs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsDefaultsAndConcurrentEdits(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if got, err := Read(dir); err != nil || got != Defaults() {
		t.Fatalf("missing settings: %+v, %v", got, err)
	}
	if _, err := Reset(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	for name, mode := range map[string]os.FileMode{dir: 0700, path: 0600} {
		st, err := os.Stat(name)
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s mode: %v, %v", name, st, err)
		}
	}
	if err := os.WriteFile(path, []byte("show_name = true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	before, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	goEdit := make(chan struct{})
	results := make(chan error, 2)
	for _, edit := range []func(*Settings){
		func(p *Settings) { p.ShowCampusID = true },
		func(p *Settings) { p.AutoVerify = false },
	} {
		go func() {
			<-goEdit
			_, err := Update(dir, edit)
			results <- err
		}()
	}
	close(goEdit)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	after, err := os.Stat(path)
	if err != nil || os.SameFile(before, after) {
		t.Fatalf("expected atomic file replacement: %v", err)
	}
	want := Defaults()
	want.ShowName, want.ShowCampusID, want.AutoVerify = true, true, false
	if got, err := Read(dir); err != nil || got != want {
		t.Fatalf("concurrent edits: %+v, %v", got, err)
	}
}

func TestExplicitResetHandlesCorruptionButRejectsNonprivateFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filename)
	for _, bad := range []string{"show_name = [", "unrecognized_option = false", "show_name = 3"} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Update(dir, func(s *Settings) { s.ShowName = true }); err == nil {
			t.Fatal("toggle overwrote a broken document")
		}
		if got, err := Reset(dir); err != nil || got != Defaults() {
			t.Fatalf("reset: %+v, %v", got, err)
		}
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("read a public settings file")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := Reset(dir); err == nil {
		t.Fatal("reset followed a symlink")
	}
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "private" {
		t.Fatalf("outside file changed: %q, %v", got, err)
	}
}

func TestWatcherTracksRenameAndRemoval(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		Watch(ctx, dir, func() {
			select {
			case events <- struct{}{}:
			default:
			}
		})
	}()
	observe := func(want Settings) {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			select {
			case <-events:
				if v, err := Read(dir); err == nil && v == want {
					return
				}
			case <-timeout:
				t.Fatalf("watcher did not report %+v", want)
			}
		}
	}
	observe(Defaults())
	v, err := Update(dir, func(p *Settings) { p.ShowName = true })
	if err != nil {
		t.Fatal(err)
	}
	observe(v)
	if err := os.Remove(filepath.Join(dir, filename)); err != nil {
		t.Fatal(err)
	}
	observe(Defaults())
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("settings watcher did not stop")
	}
}
