package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

func fixture(t *testing.T) Options {
	t.Helper()
	root := t.TempDir()
	return Options{HomeDir: root, ConfigDir: filepath.Join(root, "config"), DataDir: filepath.Join(root, "data"), StateDir: filepath.Join(root, "state"), WorkDir: root, Env: map[string]string{}}
}
func write(t *testing.T, path, s string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte(s), 0o600); e != nil {
		t.Fatal(e)
	}
}
func code(err error) string {
	var e *domain.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func TestForProfileUsesFrozenIndependentLayers(t *testing.T) {
	o := fixture(t)
	o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
	o.Profile = "a"
	o.NoProject = true
	write(t, o.ConfigFile, `default_profile = "a"
[policy]
max_runtime = "10h"
[presets.shared]
spec_id = "1"
[profiles.a]
platform_url = "https://a.example"
[profiles.a.policy]
max_runtime = "1h"
auto_renew = true
[profiles.a.presets.aonly]
spec_id = "2"
[profiles.b]
platform_url = "https://b.example"
[profiles.b.policy]
max_runtime = "3h"
auto_renew = false
[profiles.b.presets.shared]
spec_id = "3"
`)
	c, err := Load(o)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(o.WorkDir, ".bupt.toml"), "[policy]\nmax_runtime='99h'\n")
	write(t, o.ConfigFile, "not valid TOML")
	t.Setenv("BUPT_MAX_RUNTIME", "88h")
	c.Policy.MaxRuntime = "mutated"
	c.Profiles["b"] = domain.Profile{PlatformURL: "https://changed.example"}
	b, err := c.ForProfile("b")
	if err != nil {
		t.Fatal(err)
	}
	if b.Policy.MaxRuntime != "3h" || b.Policy.AutoRenew || b.Presets["shared"].SpecID != "3" || b.Presets["shared"].Policy.AutoRenew {
		t.Fatal(b.Policy, b.Presets)
	}
	if _, ok := b.Presets["aonly"]; ok {
		t.Fatal("selected profile preset leaked into another profile")
	}
	if b.Profiles["b"].PlatformURL != "https://b.example" || b.Paths.ProjectFile != "" {
		t.Fatal("snapshot read external changes")
	}
	a, err := b.ForProfile("a")
	if err != nil {
		t.Fatal(err)
	}
	if a.Policy.MaxRuntime != "1h" || !a.Policy.AutoRenew || a.Presets["shared"].SpecID != "1" || !a.Presets["aonly"].Policy.AutoRenew {
		t.Fatal(a.Policy, a.Presets)
	}
	if _, err = c.ForProfile("missing"); code(err) != "PROFILE_NOT_FOUND" {
		t.Fatal(err)
	}
}

func TestForProfilePreservesCapturedProjectEnvironmentAndFlags(t *testing.T) {
	o := fixture(t)
	o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
	o.ProjectFile = filepath.Join(o.WorkDir, ".bupt.toml")
	o.Profile = "a"
	write(t, o.ConfigFile, "[profiles.a.policy]\nmax_runtime='1h'\nauto_renew=true\n[profiles.b.policy]\nmax_runtime='3h'\nauto_renew=true\n")
	write(t, o.ProjectFile, "[policy]\ncleanup='stop'\n")
	o.Env["BUPT_AUTO_RENEW"] = "false"
	o.Env["BUPT_PLATFORM_URL"] = "https://env.example"
	o.Env["BUPT_MASTER_KEY"] = "must-not-be-retained"
	o.Overrides = map[string]any{"policy.max_runtime": "7h", "policy.force_stop": false, "presets.shared": map[string]any{"spec_id": "4", "constraints": map[string]any{"list": []any{"original"}}}}
	c, err := Load(o)
	if err != nil {
		t.Fatal(err)
	}
	o.Env["BUPT_AUTO_RENEW"] = "true"
	o.Overrides["policy.max_runtime"] = "99h"
	o.Overrides["presets.shared"].(map[string]any)["constraints"].(map[string]any)["list"].([]any)[0] = "changed"
	write(t, o.ProjectFile, "[policy]\ncleanup='delete'\n")
	b, err := c.ForProfile("b")
	if err != nil {
		t.Fatal(err)
	}
	if b.Policy.MaxRuntime != "7h" || b.Policy.AutoRenew || b.Policy.ForceStop || b.Policy.Cleanup != "stop" {
		t.Fatal(b.Policy)
	}
	if b.Sources["policy.auto_renew"] != "env:BUPT_AUTO_RENEW" || b.Sources["policy.max_runtime"] != "command-line" {
		t.Fatal(b.Sources)
	}
	if b.Profiles["a"].PlatformURL != "https://env.example" || b.Profiles["b"].PlatformURL == "https://env.example" {
		t.Fatal("endpoint override was rebound to another account")
	}
	if b.Presets["shared"].Constraints["list"].([]any)[0] != "original" {
		t.Fatal("snapshot aliases caller inputs")
	}
	b.Presets["shared"].Constraints["list"].([]any)[0] = "returned mutation"
	again, err := b.ForProfile("b")
	if err != nil {
		t.Fatal(err)
	}
	if again.Presets["shared"].Constraints["list"].([]any)[0] != "original" {
		t.Fatal("returned config mutated frozen input")
	}
	serialized, _ := json.Marshal(c)
	if strings.Contains(string(serialized), "must-not-be-retained") {
		t.Fatal("secret environment escaped config")
	}
}

func TestExplicitInitMayCreateMissingConfigButStillRejectsInvalidFile(t *testing.T) {
	o := fixture(t)
	o.ConfigFile = filepath.Join(o.ConfigDir, "new.toml")
	if _, err := Load(o); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("explicit missing config should fail outside init: %v", err)
	}
	o.AllowMissingConfig = true
	if _, err := Load(o); err != nil {
		t.Fatal(err)
	}
	write(t, o.ConfigFile, "not = [valid TOML")
	if _, err := Load(o); code(err) != "INVALID_TOML" {
		t.Fatalf("init must still reject invalid config: %v", err)
	}
}

func TestDefaultsAreLocalAndFiniteLimitsAreRuntimeRequirements(t *testing.T) {
	o := fixture(t)
	c, e := Load(o)
	if e != nil {
		t.Fatal(e)
	}
	p, e := c.SelectedProfile()
	if e != nil {
		t.Fatal(e)
	}
	if p.Controller != "local" || p.PlatformURL != "https://gpuhub.bupt.edu.cn" {
		t.Fatalf("wrong defaults %#v", p)
	}
	if c.Policy.Cleanup != "release" || c.Policy.StopGrace != "60s" || c.Policy.ForceStop {
		t.Fatalf("wrong policy %#v", c.Policy)
	}
	if e = ValidatePolicy(c.Policy, true); code(e) != "LIMIT_REQUIRED" {
		t.Fatalf("want runtime limit error, got %v", e)
	}
	c.Policy.AutoRenew = true
	c.Policy.MaxRuntime = "12h"
	if e = ValidatePolicy(c.Policy, true); e != nil {
		t.Fatal(e)
	}
	if p.ControllerSocket != filepath.Join(o.StateDir, "agent.sock") {
		t.Fatal(p.ControllerSocket)
	}
}

func TestLayerPrecedenceAndProvenance(t *testing.T) {
	o := fixture(t)
	o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
	o.ProjectFile = filepath.Join(o.WorkDir, ".bupt.toml")
	write(t, o.ConfigFile, `version=1
default_profile="personal"
[policy]
cleanup="stop"
stop_grace="90s"
[profiles.personal]
username="u123"
expected_uid=123
[profiles.personal.policy]
cleanup="none"
force_stop=false
[profiles.personal.presets.small]
spec_id=100
[profiles.personal.presets.small.policy]
max_runtime="4h"
`)
	write(t, o.ProjectFile, `[policy]
cleanup="release"
stop_grace="30s"
[presets.small]
usage="project training"
`)
	o.Env["BUPT_CLEANUP"] = "stop"
	o.Env["BUPT_MAX_RUNTIME"] = "8h"
	o.Overrides = map[string]any{"policy.cleanup": "release", "policy.force_stop": true}
	c, e := Load(o)
	if e != nil {
		t.Fatal(e)
	}
	if c.Selected != "personal" || c.Policy.Cleanup != "release" || c.Policy.StopGrace != "30s" || !c.Policy.ForceStop || c.Policy.MaxRuntime != "8h" {
		t.Fatalf("bad merged config %+v", c)
	}
	p, e := c.Profile("")
	if e != nil {
		t.Fatal(e)
	}
	if p.Username != "u123" || p.ExpectedUID != "123" || p.Controller != "local" {
		t.Fatalf("bad profile %+v", p)
	}
	pr, e := c.Preset("small")
	if e != nil {
		t.Fatal(e)
	}
	if pr.SpecID != "100" || pr.Usage != "project training" || pr.Policy.MaxRuntime != "4h" || pr.Policy.Cleanup != "release" {
		t.Fatalf("bad preset %+v", pr)
	}
	ex := c.Explain()
	for k, want := range map[string]string{"policy.cleanup": "command-line", "policy.force_stop": "command-line", "policy.stop_grace": "project:" + o.ProjectFile, "policy.max_runtime": "env:BUPT_MAX_RUNTIME", "profiles.personal.controller": "builtin"} {
		if ex[k].Source != want {
			t.Fatalf("%s source=%q want=%q", k, ex[k].Source, want)
		}
	}
}

func TestProjectCannotReplaceTrustedEndpointsOrCredentials(t *testing.T) {
	for _, body := range []string{`[profiles.bupt]
platform_url="https://attacker.example"`, `[profiles.bupt.relay]
address="attacker.example:22"`, `controller="ssh://attacker.example"`} {
		t.Run(body, func(t *testing.T) {
			o := fixture(t)
			o.ProjectFile = filepath.Join(o.WorkDir, ".bupt.toml")
			write(t, o.ProjectFile, body)
			if _, e := Load(o); e == nil {
				t.Fatal("accepted project endpoint override")
			}
		})
	}
	o := fixture(t)
	o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
	write(t, o.ConfigFile, `[profiles.bupt]
password="sensitive-secret-value"`)
	_, e := Load(o)
	if code(e) != "SECRET_IN_CONFIG" || strings.Contains(e.Error(), "sensitive-secret-value") {
		t.Fatalf("unsafe error %v", e)
	}
	o.Env["BUPT_MASTER_KEY"] = "must-not-leak"
	o.ConfigFile = ""
	os.Remove(filepath.Join(o.ConfigDir, "config.toml"))
	c, e := Load(o)
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(c.Explain())
	if strings.Contains(string(b), "must-not-leak") {
		t.Fatal("master key leaked")
	}
}

func TestSelectionAndControllerOverrides(t *testing.T) {
	o := fixture(t)
	o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
	write(t, o.ConfigFile, `[profiles.ci]
controller="ssh://runner@relay.example:22"
controller_identity_file="~/.ssh/bupt-ci"
`)
	o.Env["BUPT_PROFILE"] = "ci"
	o.Env["BUPT_CONTROLLER"] = "local"
	c, e := Load(o)
	if e != nil {
		t.Fatal(e)
	}
	p, e := c.SelectedProfile()
	if e != nil {
		t.Fatal(e)
	}
	if c.Selected != "ci" || p.Controller != "local" || p.ControllerIdentityFile != filepath.Join(o.HomeDir, ".ssh/bupt-ci") {
		t.Fatalf("bad profile %+v", p)
	}
	o.Profile = "bupt"
	c, e = Load(o)
	if e != nil {
		t.Fatal(e)
	}
	if c.Selected != "bupt" {
		t.Fatal(c.Selected)
	}
	o.Profile = "missing"
	if _, e = Load(o); code(e) != "PROFILE_NOT_FOUND" {
		t.Fatal(e)
	}
}

func TestInvalidOriginsPolicyAndTypos(t *testing.T) {
	for _, origin := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com/?token=secret", "https://example.com/path"} {
		o := fixture(t)
		o.Overrides = map[string]any{"profiles.bupt.platform_url": origin}
		if _, e := Load(o); e == nil {
			t.Fatalf("accepted %s", origin)
		}
	}
	o := fixture(t)
	o.Overrides = map[string]any{"profiles.bupt.platform_url": "http://127.0.0.1:18233"}
	if _, e := Load(o); e != nil {
		t.Fatal(e)
	}
	o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
	write(t, o.ConfigFile, "[policy]\nauto_renwe=true\n")
	if _, e := Load(o); code(e) != "UNKNOWN_CONFIG_KEY" {
		t.Fatal(e)
	}
	for _, p := range []domain.RunPolicy{{Cleanup: "explode"}, {Cleanup: "release", MaxRuntime: "-1h"}, {Cleanup: "release", StopGrace: "bad"}, {Cleanup: "release", MaxRuntime: "1h", Until: time.Now().Add(time.Hour)}} {
		if e := ValidatePolicy(p, false); e == nil {
			t.Fatal("accepted invalid policy", p)
		}
	}
}

func TestSetFileIsAtomicPrivateAndRejectsSecret(t *testing.T) {
	o := fixture(t)
	p := filepath.Join(o.ConfigDir, "config.toml")
	if e := SetFile(p, "profiles.bupt.username", "u1"); e != nil {
		t.Fatal(e)
	}
	if e := SetFile(p, "policy.cleanup", "none"); e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(p)
	if e != nil {
		t.Fatal(e)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatal(st.Mode())
	}
	c, e := Load(o)
	if e != nil {
		t.Fatal(e)
	}
	pr, _ := c.SelectedProfile()
	if pr.Username != "u1" || c.Policy.Cleanup != "none" {
		t.Fatal(c)
	}
	before, _ := os.ReadFile(p)
	if e = SetFile(p, "profiles.bupt.password", "do-not-store"); code(e) != "SECRET_IN_CONFIG" {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("failed update changed the file")
	}
	link := filepath.Join(o.ConfigDir, "linked.toml")
	if e = os.Symlink(p, link); e != nil {
		t.Fatal(e)
	}
	if e = SetFile(link, "policy.cleanup", "stop"); code(e) != "CONFIG_SYMLINK" {
		t.Fatal(e)
	}
}

func TestRedactionRecurses(t *testing.T) {
	v := map[string]any{"nested": []any{map[string]any{"passwordSha256": "never-log", "name": "safe"}}, "url": "https://alice:secret@example.com/path"}
	b, e := json.Marshal(Redact(v))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "never-log") || strings.Contains(string(b), "alice") || strings.Contains(string(b), "secret") {
		t.Fatal(string(b))
	}
}

func TestProjectDiscoveryUsesNearestFile(t *testing.T) {
	o := fixture(t)
	write(t, filepath.Join(o.WorkDir, ".bupt.toml"), "[policy]\ncleanup=\"stop\"\n")
	nested := filepath.Join(o.WorkDir, "project", "sub")
	if e := os.MkdirAll(nested, 0o700); e != nil {
		t.Fatal(e)
	}
	write(t, filepath.Join(o.WorkDir, "project", ".bupt.toml"), "[policy]\ncleanup=\"none\"\n")
	o.WorkDir = nested
	c, e := Load(o)
	if e != nil {
		t.Fatal(e)
	}
	if c.Policy.Cleanup != "none" {
		t.Fatal(c.Policy)
	}
	o.NoProject = true
	c, e = Load(o)
	if e != nil {
		t.Fatal(e)
	}
	if c.Policy.Cleanup != "release" {
		t.Fatal(c.Policy)
	}
}

func TestCLIMapMergeAndUnknownKeys(t *testing.T) {
	o := fixture(t)
	o.Overrides = map[string]any{"policy": map[string]any{"cleanup": "none"}}
	c, e := Load(o)
	if e != nil {
		t.Fatal(e)
	}
	if c.Policy.Cleanup != "none" || c.Policy.StopGrace != "60s" || c.Policy.ForceStop {
		t.Fatal(c.Policy)
	}
	o.Overrides = map[string]any{"policy.auto_renwe": true}
	if _, e = Load(o); code(e) != "UNKNOWN_CONFIG_KEY" {
		t.Fatal(e)
	}
}

func TestMalformedSecretDoesNotLeakInParseError(t *testing.T) {
	o := fixture(t)
	o.ConfigFile = filepath.Join(o.ConfigDir, "config.toml")
	write(t, o.ConfigFile, "[profiles.bupt]\npassword=\"do-not-leak\n")
	_, e := Load(o)
	if code(e) != "INVALID_TOML" || strings.Contains(e.Error(), "do-not-leak") {
		t.Fatal(e)
	}
}

func TestPersonalPathsAndIndependentOverrides(t *testing.T) {
	base := t.TempDir()
	personal := filepath.Join(base, ".bupt")
	xdgConfig := filepath.Join(base, "settings")
	xdgState := filepath.Join(base, "runtime")
	xdgData := filepath.Join(base, "storage")
	xdg := map[string]string{"XDG_CONFIG_HOME": xdgConfig, "XDG_STATE_HOME": xdgState, "XDG_DATA_HOME": xdgData}
	for _, tc := range []struct {
		name                         string
		opts                         Options
		configDir, dataDir, stateDir string
	}{
		{"personal", Options{Env: map[string]string{}}, personal, filepath.Join(personal, "data"), filepath.Join(personal, "state")},
		{"all XDG", Options{Env: xdg}, filepath.Join(xdgConfig, "bupt"), filepath.Join(xdgData, "bupt"), filepath.Join(xdgState, "bupt")},
		{"state XDG only", Options{Env: map[string]string{"XDG_STATE_HOME": xdgState}}, personal, filepath.Join(personal, "data"), filepath.Join(xdgState, "bupt")},
		{"explicit paths", Options{Env: xdg, ConfigDir: "~/cfg", DataDir: "~/data", StateDir: "~/state"}, filepath.Join(base, "cfg"), filepath.Join(base, "data"), filepath.Join(base, "state")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := tc.opts
			opts.HomeDir, opts.NoProject = base, true
			cfg, e := Load(opts)
			if e != nil {
				t.Fatal(e)
			}
			want := Paths{ConfigDir: tc.configDir, DataDir: tc.dataDir, StateDir: tc.stateDir, ConfigFile: filepath.Join(tc.configDir, "config.toml"), Socket: filepath.Join(tc.stateDir, "agent.sock")}
			if cfg.Paths != want {
				t.Fatalf("paths = %+v, want %+v", cfg.Paths, want)
			}
			p, e := cfg.SelectedProfile()
			if e != nil || p.ControllerSocket != want.Socket {
				t.Fatalf("profile socket = %q (%v), want %q", p.ControllerSocket, e, want.Socket)
			}
		})
	}
}
