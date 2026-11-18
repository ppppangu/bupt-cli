package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
)

func TestUpRetainsRecoverableIDWithoutDuplicateSubmission(t *testing.T) {
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "up":
			if p["wait"] != false {
				t.Fatal("allocation must return its receipt before waiting")
			}
			return domain.Run{ID: "durable-1", State: "preparing"}, nil
		case "run.wait":
			return nil, &domain.Error{Code: "CONSOLE_LOGIN_REQUIRED", Message: "password missing"}
		}
		t.Fatal(method)
		return nil, nil
	}}
	opts, out, errout := testOptions(t, b)
	if code := executeTest(t, opts, "up", "gpu", "--adopt", "123", "--max-runtime", "15m", "--json"); code != 1 {
		t.Fatal(code)
	}
	if out.Len() != 0 {
		t.Fatal(out.String())
	}
	var response struct{ Error domain.Error }
	if e := json.Unmarshal(errout.Bytes(), &response); e != nil {
		t.Fatal(e)
	}
	if response.Error.RunID != "durable-1" || !strings.Contains(response.Error.Hint, "bupt auth access") || !strings.Contains(response.Error.Hint, "bupt wait durable-1") {
		t.Fatal(response.Error)
	}
	if len(b.calls) != 2 || b.calls[0].method != "up" || b.calls[1].method != "run.wait" {
		t.Fatal(b.calls)
	}
}

func TestUpNoWaitQuietEmitsOnlyID(t *testing.T) {
	b := &fakeBackend{}
	opts, out, errout := testOptions(t, b)
	if code := executeTest(t, opts, "up", "gpu", "--spec", "Ubuntu@24.04", "--max-runtime", "15m", "--no-wait", "-q"); code != 0 {
		t.Fatal(code, errout.String())
	}
	if out.String() != "receipt-1\n" || errout.Len() != 0 || len(b.calls) != 1 {
		t.Fatal(out.String(), errout.String(), b.calls)
	}
	if b.calls[0].params["spec_id"] != "Ubuntu@24.04" {
		t.Fatal(b.calls)
	}
}

func TestPresetRoundTripAndExplicitTemplateOverride(t *testing.T) {
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method == "preset.resolve" {
			return domain.Preset{SpecID: "99", Constraints: map[string]any{"node": map[string]any{"cpu": "2"}}}, nil
		}
		return domain.Run{ID: "run-1", State: "ready"}, nil
	}}
	opts, out, errout := testOptions(t, b)
	configFile := filepath.Join(opts.ConfigOptions.HomeDir, ".bupt", "config.toml")
	opts.ConfigOptions.ConfigFile = configFile
	if e := os.MkdirAll(filepath.Dir(configFile), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(configFile, []byte("version = 1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if code := executeTest(t, opts, "preset", "set", "small", "--spec", "Ubuntu", "--max-runtime", "15m"); code != 0 {
		t.Fatal(code, errout.String())
	}
	if code := executeTest(t, opts, "preset", "use", "small"); code != 0 {
		t.Fatal(code, errout.String())
	}
	cfg, e := config.Load(opts.ConfigOptions)
	if e != nil {
		t.Fatal(e)
	}
	if cfg.DefaultPreset != "small" || cfg.Presets["small"].Policy.MaxRuntime != "15m" {
		t.Fatal(cfg)
	}
	if code := executeTest(t, opts, "up", "gpu", "--no-wait"); code != 0 {
		t.Fatal(code, errout.String())
	}
	if b.calls[len(b.calls)-1].params["preset"] != "small" {
		t.Fatal(b.calls)
	}
	if code := executeTest(t, opts, "up", "gpu", "--spec", "100", "--max-runtime", "20m", "--no-wait"); code != 0 {
		t.Fatal(code, errout.String())
	}
	if _, exists := b.calls[len(b.calls)-1].params["preset"]; exists {
		t.Fatal("explicit template must not inherit another template's constraints")
	}
	if code := executeTest(t, opts, "preset", "rm", "small"); code != 1 {
		t.Fatal("default removal should fail")
	}
	if code := executeTest(t, opts, "preset", "use", "-"); code != 0 {
		t.Fatal(code, errout.String())
	}
	if code := executeTest(t, opts, "preset", "rm", "small"); code != 0 {
		t.Fatal(code, errout.String())
	}
	st, e := os.Stat(configFile)
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, e)
	}
	_ = out
}

func TestStatusCachedAndGroupedHelp(t *testing.T) {
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "status.get" || p["cached"] != true {
			t.Fatal(method, p)
		}
		return domain.StatusReport{Platform: "not_checked", Source: "cached"}, nil
	}}
	opts, out, errout := testOptions(t, b)
	if code := executeTest(t, opts, "status", "--cached", "--json"); code != 0 {
		t.Fatal(code, errout.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatal(out.String())
	}
	out.Reset()
	if code := executeTest(t, opts, "--help"); code != 0 {
		t.Fatal(code)
	}
	for _, text := range []string{"大模型 API 服务", "裸算力服务", "按用途浏览", "help compute", "help api", "model", "usage", "catalog", "help --all", "login", "status"} {
		if !strings.Contains(out.String(), text) {
			t.Fatal(text, out.String())
		}
	}
}

func TestExistingInstanceJobDoesNotRequireAnotherRuntimeFlag(t *testing.T) {
	b := &fakeBackend{}
	opts, _, errout := testOptions(t, b)
	if code := executeTest(t, opts, "run", "--instance", "gpu", "--detach", "--", "true"); code != 0 {
		t.Fatal(code, errout.String())
	}
	if len(b.calls) != 1 || b.calls[0].params["inherit_policy"] != true {
		t.Fatal(b.calls)
	}
}
