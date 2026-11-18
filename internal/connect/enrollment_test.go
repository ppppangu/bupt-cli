package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"bupt.local/cli/internal/domain"
	"golang.org/x/crypto/ssh"
)

func enrollmentTestKey(t *testing.T) string {
	t.Helper()
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signerForTest(t).PublicKey())))
}
func enrollmentTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, ".ssh"), 0700); err != nil {
		t.Fatal(err)
	}
	return home
}
func enrollmentTestPrefix(home, user string) string {
	h, _ := json.Marshal(home)
	u, _ := json.Marshal(user)
	return "import pwd,types\npwd.getpwuid=lambda uid:types.SimpleNamespace(pw_name=" + string(u) + ",pw_dir=" + string(h) + ")\n"
}
func enrollmentTestRun(t *testing.T, home string, add, remove []string, source string) (AuthorizedKeysResult, error) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	if source == "" {
		source = authorizedKeysProgram
	}
	if add == nil {
		add = []string{}
	}
	if remove == nil {
		remove = []string{}
	}
	input, _ := json.Marshal(map[string]any{"add": add, "remove": remove, "expected_username": "tester", "expected_home": home})
	cmd := exec.Command(python, "-c", enrollmentTestPrefix(home, "tester")+source)
	cmd.Stdin = bytes.NewReader(input)
	output, runErr := cmd.CombinedOutput()
	var response struct {
		OK     bool                 `json:"ok"`
		Result AuthorizedKeysResult `json:"result"`
		Error  string               `json:"error"`
	}
	if err = json.Unmarshal(output, &response); err != nil {
		t.Fatalf("invalid enrollment output: %s (%v, %v)", output, err, runErr)
	}
	if !response.OK {
		return response.Result, errors.New(response.Error)
	}
	if runErr != nil {
		return response.Result, runErr
	}
	return response.Result, nil
}

func TestEnrollmentPreservesPlatformAndUnknownLines(t *testing.T) {
	home := enrollmentTestHome(t)
	revoked, active := enrollmentTestKey(t), enrollmentTestKey(t)
	fingerprint, _ := Fingerprint(revoked)
	legacy := strings.TrimPrefix(fingerprint, "SHA256:")[:20]
	platform := revoked + " platform-owned\r\n"
	options := "restrict,command=\"echo platform session\" " + revoked + " bupt-device\n"
	unknown := revoked + " bupt-client:unrecognized-fingerprint\n"
	managed := []string{revoked + " bupt-device\n", revoked + " bupt-client:" + legacy + "\n", revoked + " bupt-client:" + fingerprint + "\n"}
	old := []byte("# platform header\n" + platform + options + unknown + strings.Join(managed, "") + "# non-UTF8 comment \xff")
	name := filepath.Join(home, ".ssh/authorized_keys")
	if err := os.WriteFile(name, old, 0644); err != nil {
		t.Fatal(err)
	}
	result, err := enrollmentTestRun(t, home, []string{active}, []string{revoked}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.AddedKeys != 1 || result.RemovedKeys != 3 || !result.Changed || len(result.PreservedUnmanagedBlobs) != 1 || result.PreservedUnmanagedBlobs[0] != strings.Fields(revoked)[1] {
		t.Fatalf("%+v", result)
	}
	fp, _ := Fingerprint(active)
	want := []byte("# platform header\n" + platform + options + unknown + "# non-UTF8 comment \xff\n" + active + " bupt-client:" + fp + "\n")
	actual, _ := os.ReadFile(name)
	if !bytes.Equal(actual, want) {
		t.Fatalf("unmanaged lines changed:\n%q\nwant:\n%q", actual, want)
	}
	backup, err := os.ReadFile(result.BackupPath)
	if err != nil || !bytes.Equal(backup, old) {
		t.Fatalf("backup %v %q", err, backup)
	}
	for _, file := range []string{name, result.BackupPath} {
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe permissions %s: %v %v", file, info, err)
		}
	}
	again, err := enrollmentTestRun(t, home, []string{active}, []string{revoked}, "")
	if err != nil || again.Changed || again.AddedKeys != 0 || again.RemovedKeys != 0 || len(again.PreservedUnmanagedBlobs) != 1 {
		t.Fatalf("not idempotent: %+v %v", again, err)
	}
	backups, _ := filepath.Glob(filepath.Join(home, ".ssh/authorized_keys.before-*"))
	if len(backups) != 1 {
		t.Fatal(backups)
	}
}

func TestImportAndRevokeExistingPlatformKeyNeverTakesOwnership(t *testing.T) {
	home := enrollmentTestHome(t)
	key := enrollmentTestKey(t)
	name := filepath.Join(home, ".ssh/authorized_keys")
	old := []byte("from=\"10.0.0.0/8\",command=\"platform login\" " + key + " platform key\n")
	os.WriteFile(name, old, 0600)
	added, err := enrollmentTestRun(t, home, []string{key}, nil, "")
	if err != nil || added.Changed || added.AddedKeys != 0 {
		t.Fatalf("%+v %v", added, err)
	}
	removed, err := enrollmentTestRun(t, home, nil, []string{key}, "")
	if err != nil || removed.Changed || removed.RemovedKeys != 0 || len(removed.PreservedUnmanagedBlobs) != 1 {
		t.Fatalf("%+v %v", removed, err)
	}
	actual, _ := os.ReadFile(name)
	if !bytes.Equal(actual, old) {
		t.Fatal("platform authorization was changed")
	}
}

func TestEnrollmentRejectsUnsafeFilesystemObjects(t *testing.T) {
	for _, kind := range []string{"ssh-symlink", "auth-symlink", "auth-hardlink", "lock-symlink", "writable-directory", "writable-file"} {
		t.Run(kind, func(t *testing.T) {
			home := enrollmentTestHome(t)
			key := enrollmentTestKey(t)
			auth := filepath.Join(home, ".ssh/authorized_keys")
			original := []byte(key + " platform\n")
			outside := filepath.Join(t.TempDir(), "preserve")
			os.WriteFile(outside, original, 0600)
			switch kind {
			case "ssh-symlink":
				os.Remove(filepath.Join(home, ".ssh"))
				os.Symlink(filepath.Dir(outside), filepath.Join(home, ".ssh"))
			case "auth-symlink":
				os.Symlink(outside, auth)
			case "auth-hardlink":
				os.Link(outside, auth)
			case "lock-symlink":
				os.Symlink(outside, filepath.Join(home, ".ssh/.bupt-enrollment.lock"))
			case "writable-directory":
				os.Chmod(filepath.Join(home, ".ssh"), 0777)
			case "writable-file":
				os.WriteFile(auth, original, 0666)
				os.Chmod(auth, 0666)
			}
			if _, err := enrollmentTestRun(t, home, []string{enrollmentTestKey(t)}, nil, ""); err == nil {
				t.Fatal("unsafe object accepted")
			}
			actual, _ := os.ReadFile(outside)
			if !bytes.Equal(actual, original) {
				t.Fatal("modified a linked file")
			}
		})
	}
}

func TestEnrollmentDoesNotOverwriteAnExternalWriter(t *testing.T) {
	home := enrollmentTestHome(t)
	original := enrollmentTestKey(t) + " platform\n"
	concurrent := original + enrollmentTestKey(t) + " concurrent-platform\n"
	name := filepath.Join(home, ".ssh/authorized_keys")
	os.WriteFile(name, []byte(original), 0600)
	// Inject a platform writer between constructing the replacement and commit.
	value, _ := json.Marshal(concurrent)
	hook := `original_new_file=ak_new_file
def ak_new_file(directory,name,value):
    original_new_file(directory,name,value)
    if name.startswith('.bupt-authorized_keys-'):
        fd=os.open('authorized_keys',os.O_WRONLY|os.O_TRUNC,dir_fd=directory)
        with os.fdopen(fd,'w') as external:external.write(` + string(value) + `)
`
	program := strings.Replace(authorizedKeysProgram, "import json,sys\n", hook+"\nimport json,sys\n", 1)
	if _, err := enrollmentTestRun(t, home, []string{enrollmentTestKey(t)}, nil, program); err == nil || !strings.Contains(err.Error(), "outside the enrollment lock") {
		t.Fatalf("%v", err)
	}
	actual, _ := os.ReadFile(name)
	if string(actual) != concurrent {
		t.Fatal("external writer was overwritten")
	}
	temps, _ := filepath.Glob(filepath.Join(home, ".ssh/.bupt-authorized_keys-*"))
	if len(temps) != 0 {
		t.Fatal(temps)
	}
}

func TestConcurrentEnrollmentPreservesAllKeys(t *testing.T) {
	home := enrollmentTestHome(t)
	original := enrollmentTestKey(t) + " platform\n"
	name := filepath.Join(home, ".ssh/authorized_keys")
	os.WriteFile(name, []byte(original), 0600)
	keys := make([]string, 8)
	for i := range keys {
		keys[i] = enrollmentTestKey(t)
	}
	var workers sync.WaitGroup
	for _, key := range keys {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := enrollmentTestRun(t, home, []string{key}, nil, ""); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	actual, _ := os.ReadFile(name)
	if !bytes.HasPrefix(actual, []byte(original)) {
		t.Fatal("platform key lost")
	}
	for _, key := range keys {
		if bytes.Count(actual, []byte(strings.Fields(key)[1])) != 1 {
			t.Fatal("concurrent enrollment lost or duplicated a key")
		}
	}
}

func TestBootstrapReconcilesPendingRevocation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	home := enrollmentTestHome(t)
	old, active, host := enrollmentTestKey(t), enrollmentTestKey(t), enrollmentTestKey(t)
	name := filepath.Join(home, ".ssh/authorized_keys")
	os.WriteFile(name, []byte(old+" platform\n"+old+" bupt-device\n"), 0600)
	program, err := PrepareProgram(BootstrapRequest{Profile: "p", InstanceID: "i", NodeID: "n", ExpectedUsername: "tester", PublicKeys: []string{active}, RevokedKeys: []string{old}, SkipTunnelKey: true})
	if err != nil {
		t.Fatal(err)
	}
	hostJSON, _ := json.Marshal(host)
	program = strings.TrimSuffix(program, "\nresult=prepare(spec)\n") + "\ndiscover_host_key=lambda port:" + string(hostJSON) + "\nprint(json.dumps(prepare(spec)))\n"
	output, err := exec.Command(python, "-c", enrollmentTestPrefix(home, "tester")+program).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, output)
	}
	var result BootstrapResult
	if err = json.Unmarshal(output, &result); err != nil {
		t.Fatal(err)
	}
	if result.AddedKeys != 1 || result.RemovedKeys != 1 || len(result.PreservedUnmanagedBlobs) != 1 {
		t.Fatalf("%+v", result)
	}
	contents, _ := os.ReadFile(name)
	if !bytes.HasPrefix(contents, []byte(old+" platform\n")) || bytes.Contains(contents, []byte(old+" bupt-device")) {
		t.Fatal("pending revocation affected platform ownership")
	}
}

func TestEnrollmentValidationRejectsAmbiguousKeyState(t *testing.T) {
	key := enrollmentTestKey(t)
	_, err := ApplyAuthorizedKeys(context.Background(), domain.Route{}, []string{key}, []string{key})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "KEY_STATE_CONFLICT" {
		t.Fatal(err)
	}
	_, err = PrepareProgram(BootstrapRequest{InstanceID: "i", NodeID: "n", PublicKeys: []string{key}, RevokedKeys: []string{key}})
	if !errors.As(err, &de) || de.Code != "KEY_STATE_CONFLICT" {
		t.Fatal(err)
	}
}

func testEnrollmentCommand() string {
	command, _ := ShellCommand([]string{"python3", "-c", authorizedKeysProgram})
	return command
}
func runTestEnrollment(ch ssh.Channel, home string) {
	cmd := exec.Command("python3", "-c", enrollmentTestPrefix(home, "student")+authorizedKeysProgram)
	cmd.Stdin = ch
	cmd.Stdout = ch
	cmd.Stderr = ch.Stderr()
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		} else {
			code = 125
		}
	}
	sendExit(ch, uint32(code))
}
func TestApplyAuthorizedKeysUsesNativeSSHAndFixedCommand(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	fixture := newSSHFixture(t)
	key := enrollmentTestKey(t)
	result, err := ApplyAuthorizedKeys(context.Background(), fixture.route(), []string{key}, nil)
	if err != nil || result.AddedKeys != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	if command := <-fixture.execs; command != testEnrollmentCommand() || strings.Contains(command, strings.Fields(key)[1]) {
		t.Fatal("enrollment data appeared in command arguments")
	}
	result, err = ApplyAuthorizedKeys(context.Background(), fixture.route(), nil, []string{key})
	if err != nil || result.RemovedKeys != 1 {
		t.Fatalf("%+v %v", result, err)
	}
	contents, _ := os.ReadFile(filepath.Join(fixture.home, ".ssh/authorized_keys"))
	if len(contents) != 0 {
		t.Fatal("managed key still present")
	}
}
