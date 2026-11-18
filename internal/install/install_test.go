package install

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"github.com/BurntSushi/toml"
	"golang.org/x/crypto/ssh"
)

type commandCall struct {
	name string
	args []string
}

func testSetup(t *testing.T) (*handler, *config.Config, *[]commandCall) {
	t.Helper()
	base := t.TempDir()
	cfg, err := config.Load(config.Options{HomeDir: base, Env: map[string]string{}, NoProject: true})
	if err != nil {
		t.Fatal(err)
	}
	calls := []commandCall{}
	var clock atomic.Int64
	launchdLoaded := true
	h := &handler{goos: "linux", home: base, executable: filepath.Join(base, "bin", "bupt"), uid: 501, userConfigHome: filepath.Join(base, ".config"), now: func() time.Time { return time.Unix(1_800_000_000, clock.Add(1)) }}
	h.run = func(_ context.Context, name string, args []string, _ io.Reader, _, _ io.Writer) error {
		calls = append(calls, commandCall{name, append([]string(nil), args...)})
		if name == "launchctl" {
			switch args[0] {
			case "print":
				if !launchdLoaded {
					return errors.New("service not found")
				}
			case "bootout":
				launchdLoaded = false
			case "bootstrap":
				launchdLoaded = true
			}
		}
		return nil
	}
	h.rpc = func(_ context.Context, _ *config.Config, method string, _, out any) error {
		switch method {
		case "agent.status":
			return setJSON(out, map[string]any{"state": "ready"})
		case "key.import":
			return setJSON(out, map[string]any{"id": "key-1", "installed": []string{"instance-1"}, "pending": []string{}})
		case "agent.stop":
			return setJSON(out, map[string]any{"stopped": true})
		default:
			return domain.E("UNEXPECTED_RPC", method)
		}
	}
	h.start = func(string, []string, string) (int, error) { return 4242, nil }
	h.dial = func(context.Context, domain.RelayConfig) (remote, error) {
		t.Fatal("unexpected remote connection")
		return nil, nil
	}
	return h, cfg, &calls
}
func setJSON(out, value any) error {
	if out == nil {
		return nil
	}
	b, _ := json.Marshal(value)
	return json.Unmarshal(b, out)
}
func keyText(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
}
func requireMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != mode {
		t.Fatalf("%s mode %o, want %o", path, info.Mode().Perm(), mode)
	}
}

func TestInitWritesPrivateSecretFreeConfigAndPreservesExistingKeys(t *testing.T) {
	h, cfg, calls := testSetup(t)
	params := map[string]any{"install": false, "start": false}
	result, err := h.handle(context.Background(), cfg, "init", params, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["mode"] != "local" {
		t.Fatal(result)
	}
	for _, dir := range []string{cfg.Paths.ConfigDir, cfg.Paths.DataDir, cfg.Paths.StateDir, filepath.Join(cfg.Paths.DataDir, "keys")} {
		requireMode(t, dir, 0700)
	}
	master := filepath.Join(cfg.Paths.DataDir, "master.key")
	identity := filepath.Join(cfg.Paths.DataDir, "keys", "device_ed25519")
	for _, path := range []string{master, identity, cfg.Paths.ConfigFile} {
		requireMode(t, path, 0600)
	}
	masterBefore, _ := os.ReadFile(master)
	keyBefore, _ := os.ReadFile(identity)
	if len(masterBefore) != 32 {
		t.Fatal("master key is not 256 bits")
	}
	data, _ := os.ReadFile(cfg.Paths.ConfigFile)
	if bytes.Contains(data, masterBefore) || bytes.Contains(data, keyBefore) || strings.Contains(string(data), "PRIVATE KEY") {
		t.Fatal("configuration contains private material")
	}
	loaded, err := config.Load(config.Options{ConfigFile: cfg.Paths.ConfigFile, HomeDir: h.home, Env: map[string]string{}, NoProject: true})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Profiles[cfg.Selected].Controller != "local" || loaded.Policy.Cleanup != "release" || !loaded.Policy.ForceStop {
		t.Fatalf("wrong defaults: %#v", loaded)
	}
	if _, err = h.handle(context.Background(), loaded, "init", params, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	masterAfter, _ := os.ReadFile(master)
	keyAfter, _ := os.ReadFile(identity)
	if !bytes.Equal(masterBefore, masterAfter) || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("repeated init rotated a private key")
	}
	if len(*calls) != 0 {
		t.Fatal("no-start/no-install invoked service commands")
	}
}

func TestInitBacksUpConfigurationAndPreservesUnrelatedProfiles(t *testing.T) {
	h, cfg, _ := testSetup(t)
	if err := os.MkdirAll(cfg.Paths.ConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	old := []byte("version = 1\ndefault_profile = 'bupt'\n[profiles.other]\nplatform_url = 'https://other.example'\ncontroller = 'local'\n[policy]\ncleanup = 'retain'\nstop_grace = '90s'\nforce_stop = false\n")
	if err := os.WriteFile(cfg.Paths.ConfigFile, old, 0600); err != nil {
		t.Fatal(err)
	}
	value, err := h.handle(context.Background(), cfg, "init", map[string]any{"install": false, "start": false}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	backups := value.(map[string]any)["backups"].([]string)
	if len(backups) != 1 {
		t.Fatal(backups)
	}
	data, _ := os.ReadFile(backups[0])
	if !bytes.Equal(data, old) {
		t.Fatal("backup differs from original")
	}
	var doc map[string]any
	if _, err = toml.DecodeFile(cfg.Paths.ConfigFile, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["profiles"].(map[string]any)["other"]; !ok {
		t.Fatal("removed unrelated profile")
	}
	if doc["policy"].(map[string]any)["cleanup"] != "retain" {
		t.Fatal("overrode existing policy")
	}
}

func TestInitDryRunHasNoWritesOrCommands(t *testing.T) {
	h, cfg, calls := testSetup(t)
	if _, err := h.handle(context.Background(), cfg, "init", map[string]any{"dry_run": true}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Paths.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run created config")
	}
	if len(*calls) != 0 {
		t.Fatal("dry-run executed a service command")
	}
}

func TestUserServiceTemplatesAndStop(t *testing.T) {
	for _, goos := range []string{"linux", "darwin"} {
		t.Run(goos, func(t *testing.T) {
			h, cfg, calls := testSetup(t)
			h.goos = goos
			h.executable = filepath.Join(h.home, "bin dir", "bupt&test")
			result, err := h.handle(context.Background(), cfg, "init", map[string]any{}, nil, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			m := result.(map[string]any)
			if m["started"] != true || m["installed"] != true {
				t.Fatal(m)
			}
			data, err := os.ReadFile(m["service_file"].(string))
			if err != nil {
				t.Fatal(err)
			}
			if goos == "darwin" {
				d := xml.NewDecoder(bytes.NewReader(data))
				for {
					_, e := d.Token()
					if e == io.EOF {
						break
					}
					if e != nil {
						t.Fatal(e)
					}
				}
				if !bytes.Contains(data, []byte("bupt&amp;test")) {
					t.Fatal("plist did not escape executable")
				}
				if (*calls)[len(*calls)-1].args[0] != "print" || (*calls)[len(*calls)-2].args[0] != "bootstrap" {
					t.Fatal(*calls)
				}
			} else if !bytes.Contains(data, []byte("UMask=0077")) || !bytes.Contains(data, []byte("--master-key-file")) {
				t.Fatal(string(data))
			}
			if _, err = h.handle(context.Background(), cfg, "agent.stop", nil, nil, nil, nil); err != nil {
				t.Fatal(err)
			}
			last := (*calls)[len(*calls)-1]
			if goos == "linux" && !reflect.DeepEqual(last.args, []string{"--user", "stop", "bupt-agent.service"}) {
				t.Fatal(last)
			}
			if goos == "darwin" && last.args[0] != "bootout" {
				t.Fatal(last)
			}
		})
	}
}

type launchctlExit int

func (e launchctlExit) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e launchctlExit) ExitCode() int { return int(e) }

func TestLaunchdReloadWaitsForRemovalAndRetriesTransientBootstrap(t *testing.T) {
	h, cfg, _ := testSetup(t)
	h.goos = "darwin"
	phase := "loaded"
	removalPolls, registrationPolls, attempts, waits := 0, 0, 0, 0
	h.wait = func(ctx context.Context, delay time.Duration) error {
		waits++
		if delay != launchdRetryInterval {
			t.Fatal(delay)
		}
		return ctx.Err()
	}
	h.run = func(ctx context.Context, name string, args []string, _ io.Reader, _, stderr io.Writer) error {
		if name != "launchctl" {
			t.Fatal(name)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > launchdReloadTimeout {
			t.Fatal("launchd command has no bounded deadline")
		}
		switch args[0] {
		case "print":
			switch phase {
			case "loaded":
				return nil
			case "removing":
				removalPolls++
				if removalPolls < 3 {
					return nil
				}
				phase = "absent"
			case "registering":
				registrationPolls++
				if registrationPolls > 1 {
					phase = "loaded"
					return nil
				}
			}
			return errors.New("service not found")
		case "bootout":
			phase = "removing"
			return nil
		case "bootstrap":
			if phase != "absent" || removalPolls != 3 {
				t.Fatal("bootstrap ran before bootout finished")
			}
			attempts++
			if attempts == 1 {
				fmt.Fprintln(stderr, "Bootstrap failed: 5: Input/output error\nTry re-running the command as root for richer errors.")
				return launchctlExit(5)
			}
			phase = "registering"
			return nil
		default:
			t.Fatal(args)
			return nil
		}
	}
	var diagnostics bytes.Buffer
	record, _, err := h.installUserService(context.Background(), cfg, "/private/master.key", "bupt-agent", true, &diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || waits < 4 || phase != "loaded" {
		t.Fatalf("attempts=%d waits=%d phase=%s", attempts, waits, phase)
	}
	if diagnostics.Len() != 0 {
		t.Fatal("transient errors were printed despite successful recovery")
	}
	if _, err = os.Stat(record.Path); err != nil {
		t.Fatal(err)
	}
}

func TestLaunchdFailureRetainsPlistBackupAndUserRecovery(t *testing.T) {
	h, cfg, _ := testSetup(t)
	h.goos = "darwin"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.wait = func(ctx context.Context, _ time.Duration) error { cancel(); return ctx.Err() }
	h.run = func(_ context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
		if args[0] == "print" {
			return errors.New("service not found")
		}
		fmt.Fprintln(stderr, "Bootstrap failed: 5: Input/output error\nTry re-running the command as root for richer errors.")
		return launchctlExit(5)
	}
	plist := filepath.Join(h.home, "Library", "LaunchAgents", "cn.edu.bupt.cli.bupt-agent.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plist, []byte("previous plist"), 0600); err != nil {
		t.Fatal(err)
	}
	record, backup, err := h.installUserService(ctx, cfg, "/private/master.key", "bupt-agent", true, io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost context cancellation: %v", err)
	}
	for _, text := range []string{"Input/output error", "plist retained at " + plist, "launchctl print 'gui/501/", "launchctl bootstrap 'gui/501'", backup} {
		if !strings.Contains(err.Error(), text) {
			t.Fatalf("missing %q in %v", text, err)
		}
	}
	if strings.Contains(err.Error(), "as root") {
		t.Fatal("suggested privilege escalation for a user agent")
	}
	old, _ := os.ReadFile(backup)
	if string(old) != "previous plist" {
		t.Fatal("missing original plist backup")
	}
	newFile, _ := os.ReadFile(record.Path)
	if !bytes.Contains(newFile, []byte("<plist")) {
		t.Fatal("removed generated recovery plist")
	}
}

func TestLaunchdRetryHonorsShorterCallerDeadline(t *testing.T) {
	h, _, _ := testSetup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	attempts := 0
	h.run = func(_ context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
		if args[0] == "print" {
			return errors.New("service not found")
		}
		attempts++
		fmt.Fprintln(stderr, "Bootstrap failed: 5: Input/output error")
		return launchctlExit(5)
	}
	start := time.Now()
	err := h.reloadLaunchd(ctx, serviceRecord{Name: "test", Path: "/private/agent.plist"}, "")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second || attempts != 1 {
		t.Fatalf("did not honor caller deadline: elapsed=%s attempts=%d", time.Since(start), attempts)
	}
}

func TestLaunchdDoesNotRetryPermanentFailureOrAlreadyRegisteredService(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint(registered), func(t *testing.T) {
			h, _, _ := testSetup(t)
			attempts := 0
			h.wait = func(context.Context, time.Duration) error { t.Fatal("unexpected retry"); return nil }
			h.run = func(_ context.Context, _ string, args []string, _ io.Reader, _, stderr io.Writer) error {
				if args[0] == "print" {
					if registered && attempts > 0 {
						return nil
					}
					return errors.New("service not found")
				}
				attempts++
				if registered {
					return launchctlExit(5)
				}
				fmt.Fprintln(stderr, "Invalid domain")
				return launchctlExit(64)
			}
			err := h.reloadLaunchd(context.Background(), serviceRecord{Name: "test", Path: "/private/agent.plist"}, "")
			if registered && err != nil {
				t.Fatal(err)
			}
			if !registered && (err == nil || !strings.Contains(err.Error(), "Invalid domain")) {
				t.Fatal(err)
			}
			if attempts != 1 {
				t.Fatal(attempts)
			}
		})
	}
}

func TestForegroundUsesAttachedRunnerAndDetachedUsesInjectedStarter(t *testing.T) {
	h, cfg, calls := testSetup(t)
	if _, err := h.handle(context.Background(), cfg, "init", map[string]any{"foreground": true}, strings.NewReader(""), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].name != h.executable || !reflect.DeepEqual((*calls)[0].args[:2], []string{"agent", "run"}) {
		t.Fatal(*calls)
	}
	started := false
	h.start = func(_ string, _ []string, log string) (int, error) {
		started = true
		if !strings.HasSuffix(log, "agent.log") {
			t.Fatal(log)
		}
		return 123, nil
	}
	statusCalls := 0
	h.rpc = func(_ context.Context, _ *config.Config, method string, _, out any) error {
		if method == "agent.status" {
			statusCalls++
			if statusCalls == 1 {
				return domain.E("AGENT_NOT_RUNNING", "test")
			}
		}
		return setJSON(out, map[string]any{"ready": true})
	}
	result, err := h.handle(context.Background(), cfg, "init", map[string]any{"install": false}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !started || result.(map[string]any)["pid"] != 123 {
		t.Fatal(result)
	}
}

func TestPrivatePathsAndSymlinkTargetsAreRejected(t *testing.T) {
	h, cfg, _ := testSetup(t)
	if err := os.MkdirAll(cfg.Paths.DataDir, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := h.handle(context.Background(), cfg, "init", map[string]any{"start": false, "install": false}, nil, nil, nil); err == nil {
		t.Fatal("accepted public data directory")
	}
	if err := os.Chmod(cfg.Paths.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(h.home, "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, cfg.Paths.ConfigFile); err != nil {
		t.Fatal(err)
	}
	if _, err := h.handle(context.Background(), cfg, "init", map[string]any{"start": false, "install": false}, nil, nil, nil); err == nil {
		t.Fatal("accepted config symlink")
	}
	data, _ := os.ReadFile(outside)
	if string(data) != "unchanged" {
		t.Fatal("followed symlink")
	}
}

func TestKeyImportValidatesPairAndNeverSendsPrivateMaterial(t *testing.T) {
	h, cfg, _ := testSetup(t)
	var sent map[string]any
	h.rpc = func(_ context.Context, _ *config.Config, method string, p, out any) error {
		if method != "key.import" {
			t.Fatal(method)
		}
		sent = p.(map[string]any)
		return setJSON(out, map[string]any{"id": "key-1"})
	}
	result, err := h.handle(context.Background(), cfg, "key.generate", map[string]any{"name": "work"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity := result.(map[string]any)["identity_file"].(string)
	data, _ := json.Marshal(sent)
	private, _ := os.ReadFile(identity)
	if bytes.Contains(data, private) || bytes.Contains(data, []byte("PRIVATE KEY")) || sent["identity_file"] != nil {
		t.Fatal("sent private material or a client path to remote controller")
	}
	wrong := filepath.Join(h.home, "wrong.pub")
	if err = os.WriteFile(wrong, []byte(keyText(t)), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = h.handle(context.Background(), cfg, "key.import", map[string]any{"name": "wrong", "identity_file": identity, "public_key_file": wrong}, nil, nil, nil); err == nil {
		t.Fatal("accepted mismatched public key")
	}
	if _, err = h.handle(context.Background(), cfg, "key.import", map[string]any{"name": "imported", "identity_file": identity, "public_key_file": identity + ".pub"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSSHConfigEnrollsDeviceAndPreservesUserConfiguration(t *testing.T) {
	h, cfg, _ := testSetup(t)
	sshDir := filepath.Join(h.home, ".ssh")
	if err := os.Mkdir(sshDir, 0700); err != nil {
		t.Fatal(err)
	}
	old := "Host personal\n  HostName personal.example\n  User me\n"
	if err := os.WriteFile(filepath.Join(sshDir, "config"), []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	route := domain.Route{Profile: cfg.Selected, InstanceID: "123", NodeID: "node1", Username: "ubuntu", HostKey: keyText(t)}
	var methods []string
	h.rpc = func(_ context.Context, _ *config.Config, method string, p, out any) error {
		methods = append(methods, method)
		switch method {
		case "route.get":
			if p.(map[string]any)["repair"] != false {
				t.Fatal("repair would require school auth")
			}
			return setJSON(out, route)
		case "key.import":
			if p.(map[string]any)["target"] != "123" {
				t.Fatal(p)
			}
			return setJSON(out, map[string]any{"installed": []string{"123/node1"}, "pending": []string{}})
		default:
			t.Fatal(method)
			return nil
		}
	}
	result, err := h.handle(context.Background(), cfg, "ssh-config.install", map[string]any{"target": "123"}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	m := result.(map[string]any)
	data, _ := os.ReadFile(m["path"].(string))
	text := string(data)
	for _, expected := range []string{"Host bupt-bupt-123-node1", "StrictHostKeyChecking yes", "'proxy' '123' '--node' 'node1'", "device_ed25519"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("missing %q in %s", expected, text)
		}
	}
	userConfig, _ := os.ReadFile(filepath.Join(sshDir, "config"))
	if !strings.HasPrefix(string(userConfig), "Include ") || !strings.HasSuffix(string(userConfig), old) {
		t.Fatal(string(userConfig))
	}
	known, _ := os.ReadFile(m["known_hosts"].(string))
	if !strings.Contains(string(known), route.HostKey) {
		t.Fatal("missing host key pin")
	}
	if !reflect.DeepEqual(methods, []string{"route.get", "key.import"}) {
		t.Fatal(methods)
	}
	if _, err = h.handle(context.Background(), cfg, "ssh-config.install", map[string]any{"target": "123"}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	userConfig, _ = os.ReadFile(filepath.Join(sshDir, "config"))
	if strings.Count(string(userConfig), "Include ") != 1 {
		t.Fatal("duplicated Include")
	}
}

func TestSSHConfigExplicitShortAlias(t *testing.T) {
	h, cfg, _ := testSetup(t)
	route := domain.Route{Profile: cfg.Selected, InstanceID: "123", NodeID: "node1", Username: "ubuntu", HostKey: keyText(t)}
	h.rpc = func(_ context.Context, _ *config.Config, method string, p, out any) error {
		switch method {
		case "route.get":
			return setJSON(out, route)
		case "key.import":
			return setJSON(out, map[string]any{"installed": []string{"123/node1"}})
		}
		return domain.E("UNEXPECTED_RPC", method)
	}
	var out bytes.Buffer
	if _, err := h.handle(context.Background(), cfg, "ssh-config.export", map[string]any{"target": "123", "alias": "bupt-gpu"}, nil, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Host bupt-gpu\n") || !strings.Contains(out.String(), "HostKeyAlias bupt-gpu\n") {
		t.Fatal(out.String())
	}
	for _, alias := range []string{"*", "gpu\nHost *", "-bad"} {
		if _, err := h.handle(context.Background(), cfg, "ssh-config.export", map[string]any{"target": "123", "alias": alias}, nil, &out, nil); err == nil {
			t.Fatal(alias)
		}
	}
}

func TestSSHExportAllNodesAndEnrollmentFailure(t *testing.T) {
	h, cfg, _ := testSetup(t)
	routes := []domain.Route{{InstanceID: "123", NodeID: "a", Username: "ubuntu", HostKey: keyText(t)}, {InstanceID: "123", NodeID: "b", Username: "ubuntu", HostKey: keyText(t)}}
	pending := false
	h.rpc = func(_ context.Context, _ *config.Config, method string, _, out any) error {
		if method == "route.list" {
			return setJSON(out, routes)
		}
		if method == "key.import" {
			if pending {
				return setJSON(out, map[string]any{"installed": []string{}, "pending": []string{"123"}})
			}
			return setJSON(out, map[string]any{"installed": []string{"123"}, "pending": []string{}})
		}
		t.Fatal(method)
		return nil
	}
	var out bytes.Buffer
	result, err := h.handle(context.Background(), cfg, "ssh-config.export", nil, nil, &out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil || !strings.Contains(out.String(), "Host bupt-bupt-123-a") || !strings.Contains(out.String(), "Host bupt-bupt-123-b") {
		t.Fatal(out.String())
	}
	pending = true
	out.Reset()
	if _, err = h.handle(context.Background(), cfg, "ssh-config.export", nil, nil, &out, nil); err == nil {
		t.Fatal("published config before enrollment succeeded")
	}
	if out.Len() != 0 {
		t.Fatal("wrote unusable SSH config")
	}
}

func TestMergeSSHBlocksKeepsOtherTargetsAndRejectsUnmanagedData(t *testing.T) {
	h, _, _ := testSetup(t)
	path := filepath.Join(h.home, "managed")
	old := renderBlocks(map[string]string{"p:1:a": "# bupt BEGIN p:1:a\nHost first\n# bupt END p:1:a\n"})
	if err := os.WriteFile(path, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	merged, err := mergeSSHBlocks(path, map[string]string{"p:2:b": "# bupt BEGIN p:2:b\nHost second\n# bupt END p:2:b\n"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged, "Host first") || !strings.Contains(merged, "Host second") {
		t.Fatal(merged)
	}
	if err = os.WriteFile(path, []byte("Host personal\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = mergeSSHBlocks(path, map[string]string{}); err == nil {
		t.Fatal("overwrote unmanaged output")
	}
}

type fakeRemote struct {
	commands []string
	uploads  map[string][]byte
	arch     string
	closed   bool
}

func (r *fakeRemote) Run(_ context.Context, command string, in io.Reader) ([]byte, error) {
	var data []byte
	if in != nil {
		data, _ = io.ReadAll(in)
	}
	r.commands = append(r.commands, command+"\n"+string(data))
	if bytes.Equal(data, []byte(remotePreflight)) {
		return []byte(r.arch + "\n"), nil
	}
	if strings.Contains(string(data), "mktemp -d") {
		return []byte("/tmp/bupt-install.abc123\n"), nil
	}
	return nil, nil
}
func (r *fakeRemote) Upload(_ context.Context, path string, in io.Reader, mode os.FileMode) error {
	if mode != 0600 {
		return errors.New("insecure upload")
	}
	data, err := io.ReadAll(in)
	if r.uploads == nil {
		r.uploads = map[string][]byte{}
	}
	r.uploads[path] = data
	return err
}
func (r *fakeRemote) Close() error { r.closed = true; return nil }
func fakeLinuxBinary(t *testing.T, path string) {
	t.Helper()
	b := make([]byte, 64)
	copy(b, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(b[16:], 2)
	binary.LittleEndian.PutUint16(b[18:], 62)
	binary.LittleEndian.PutUint32(b[20:], 1)
	binary.LittleEndian.PutUint16(b[52:], 64)
	binary.LittleEndian.PutUint16(b[54:], 56)
	binary.LittleEndian.PutUint16(b[58:], 64)
	if err := os.WriteFile(path, b, 0700); err != nil {
		t.Fatal(err)
	}
}

func TestRemoteInstallUsesSFTPAndRestrictedUnprivilegedAccount(t *testing.T) {
	h, cfg, _ := testSetup(t)
	binaryPath := filepath.Join(h.home, "bupt-linux")
	fakeLinuxBinary(t, binaryPath)
	r := &fakeRemote{arch: "x86_64"}
	hostKey := keyText(t)
	h.dial = func(_ context.Context, relay domain.RelayConfig) (remote, error) {
		if relay.User != "ubuntu" || relay.Address != "relay.example:2222" || relay.HostKey != hostKey {
			t.Fatal(relay)
		}
		return r, nil
	}
	p := map[string]any{"mode": "remote", "address": "relay.example:2222", "user": "ubuntu", "identity_file": filepath.Join(h.home, "admin-key"), "host_key": hostKey, "binary": binaryPath, "start": false}
	value, err := h.handle(context.Background(), cfg, "agent.install", p, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.closed || len(r.uploads) != 5 {
		t.Fatalf("closed=%v uploads=%v", r.closed, r.uploads)
	}
	operator := string(r.uploads["/tmp/bupt-install.abc123/operator-key"])
	if !strings.HasPrefix(operator, "restrict,command=\"/usr/local/bin/bupt agent gateway --client device-") || !strings.Contains(operator, "--socket /var/lib/bupt/state/agent.sock\"") {
		t.Fatal(operator)
	}
	unit := string(r.uploads["/tmp/bupt-install.abc123/bupt-agent.service"])
	for _, expected := range []string{"User=bupt", "Group=bupt", "--authorized-keys", "--bootstrap-client", "ProtectSystem=strict"} {
		if !strings.Contains(unit, expected) {
			t.Fatal(unit)
		}
	}
	all := strings.Join(r.commands, "\n")
	for _, bad := range []string{"sshd_config", "ufw ", "iptables ", "firewall-cmd", "usermod -aG sudo"} {
		if strings.Contains(all, bad) {
			t.Fatalf("global mutation: %s", bad)
		}
	}
	for _, expected := range []string{"sudo -n sh -s", "refusing an existing, unmanaged bupt account", "sha256sum -c", "dd if=/dev/urandom", "/etc/bupt/backups/", "# BEGIN BUPT MANAGED", "# END BUPT MANAGED"} {
		if !strings.Contains(all, expected) {
			t.Fatalf("missing %q", expected)
		}
	}
	if strings.Contains(all, "systemctl restart") {
		t.Fatal("--start=false started a service")
	}
	loaded, err := config.Load(config.Options{ConfigFile: cfg.Paths.ConfigFile, HomeDir: h.home, Env: map[string]string{}, NoProject: true})
	if err != nil {
		t.Fatal(err)
	}
	profile := loaded.Profiles[cfg.Selected]
	if profile.Controller != "ssh://relay.example:2222" || profile.ControllerUser != "bupt" || !strings.HasSuffix(profile.ControllerIdentityFile, "device_ed25519") {
		t.Fatal(profile)
	}
	remoteConfig := string(r.uploads["/tmp/bupt-install.abc123/config.toml"])
	if strings.Contains(remoteConfig, h.home) || strings.Contains(remoteConfig, "PRIVATE KEY") {
		t.Fatal("uploaded local path or secret")
	}
	if value.(map[string]any)["remote_backup"] == nil {
		t.Fatal("missing recovery location")
	}
}

func TestRemoteRejectsWrongOperatingSystemAndArchitecture(t *testing.T) {
	h, cfg, _ := testSetup(t)
	binaryPath := filepath.Join(h.home, "agent")
	if err := os.WriteFile(binaryPath, []byte{0xcf, 0xfa, 0xed, 0xfe}, 0700); err != nil {
		t.Fatal(err)
	}
	p := map[string]any{"mode": "remote", "address": "host", "user": "admin", "identity_file": "admin-key", "host_key": keyText(t), "binary": binaryPath}
	if _, err := h.handle(context.Background(), cfg, "agent.install", p, nil, nil, nil); err == nil {
		t.Fatal("accepted Darwin binary")
	}
	fakeLinuxBinary(t, binaryPath)
	r := &fakeRemote{arch: "aarch64"}
	h.dial = func(context.Context, domain.RelayConfig) (remote, error) { return r, nil }
	if _, err := h.handle(context.Background(), cfg, "agent.install", p, nil, nil, nil); err == nil {
		t.Fatal("accepted mismatched remote architecture")
	}
	if len(r.uploads) != 0 || len(r.commands) != 1 {
		t.Fatal("mutated remote before architecture validation")
	}
}

func TestRemoteDryRunDoesNotConnectOrWriteKeys(t *testing.T) {
	h, cfg, _ := testSetup(t)
	binaryPath := filepath.Join(h.home, "agent")
	fakeLinuxBinary(t, binaryPath)
	p := map[string]any{"mode": "remote", "address": "host", "user": "admin", "identity_file": "admin-key", "host_key": keyText(t), "binary": binaryPath, "dry_run": true}
	if _, err := h.handle(context.Background(), cfg, "agent.install", p, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cfg.Paths.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("dry-run created private data")
	}
}

func TestLocalWithRelayInstallRetainsDeviceCredentials(t *testing.T) {
	h, cfg, _ := testSetup(t)
	binaryPath := filepath.Join(h.home, "agent")
	fakeLinuxBinary(t, binaryPath)
	r := &fakeRemote{arch: "x86_64"}
	h.dial = func(context.Context, domain.RelayConfig) (remote, error) { return r, nil }
	p := map[string]any{"mode": "local", "relay_install": true, "relay_address": "relay.example:2222", "relay_user": "admin", "relay_identity_file": "/private/admin/key", "relay_host_key": keyText(t), "agent_binary": binaryPath, "start": false, "install": false}
	result, err := h.handle(context.Background(), cfg, "init", p, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["mode"] != "local" || result.(map[string]any)["relay_install"] == nil {
		t.Fatal(result)
	}
	loaded, err := config.Load(config.Options{ConfigFile: cfg.Paths.ConfigFile, HomeDir: h.home, Env: map[string]string{}, NoProject: true})
	if err != nil {
		t.Fatal(err)
	}
	profile := loaded.Profiles[cfg.Selected]
	if profile.Controller != "local" || profile.Relay.User != "bupt" || !strings.HasSuffix(profile.Relay.IdentityFile, "device_ed25519") || profile.Relay.IdentityFile == "/private/admin/key" {
		t.Fatal(profile)
	}
}

func TestRemoteScriptParsesWithoutExecuting(t *testing.T) {
	for _, start := range []bool{false, true} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(remoteInstallScript("/tmp/bupt-install.abc123", "20261117T120000", "012345abcdef", "bupt-agent", start))
		if data, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("shell syntax: %v: %s", err, data)
		}
	}
}

func TestSetupRejectsUnitLineInjectionBeforeWriting(t *testing.T) {
	h, cfg, calls := testSetup(t)
	if _, err := h.handle(context.Background(), cfg, "init", map[string]any{"master_key_file": "/tmp/key\nExecStart=/bad"}, nil, nil, nil); err == nil {
		t.Fatal("accepted service line injection")
	}
	if len(*calls) != 0 {
		t.Fatal("executed commands after invalid argument")
	}
	if _, err := os.Stat(cfg.Paths.DataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote data before validation")
	}
}

func TestInitRejectsMalformedExistingMasterWithoutStarting(t *testing.T) {
	h, cfg, calls := testSetup(t)
	keyPath := filepath.Join(h.home, "master.key")
	if err := os.WriteFile(keyPath, []byte("not a master key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := h.handle(context.Background(), cfg, "init", map[string]any{"master_key_file": keyPath}, nil, nil, nil); err == nil {
		t.Fatal("accepted malformed master key")
	}
	if len(*calls) != 0 {
		t.Fatal("started a service with an invalid master key")
	}
	data, _ := os.ReadFile(keyPath)
	if string(data) != "not a master key" {
		t.Fatal("overwrote supplied master key")
	}
}

func TestServiceArgumentEscaping(t *testing.T) {
	arg := unitArg(`/tmp/a "$HOME" %n`)
	if !strings.Contains(arg, "$$HOME") || !strings.Contains(arg, "%%n") || !strings.Contains(arg, `\"`) {
		t.Fatal(arg)
	}
	if got := sshQuote(`/tmp/"%h`); !strings.Contains(got, "%%h") || !strings.Contains(got, `\"`) {
		t.Fatal(got)
	}
}
