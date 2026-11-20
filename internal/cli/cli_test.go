package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"golang.org/x/crypto/ssh"
)

type recordedCall struct {
	method string
	params map[string]any
}
type fakeBackend struct {
	mu      sync.Mutex
	calls   []recordedCall
	call    func(context.Context, string, map[string]any) (any, error)
	session func(context.Context, string, connect.SessionOptions, io.Reader, io.Writer, io.Writer) (int, error)
	proxy   func(context.Context, string, io.Reader, io.Writer) error
	stream  func(context.Context, string, map[string]any, io.Reader, io.Writer) (map[string]any, error)
}

func (f *fakeBackend) record(method string, p any) map[string]any {
	b, _ := json.Marshal(p)
	params := map[string]any{}
	_ = json.Unmarshal(b, &params)
	f.mu.Lock()
	f.calls = append(f.calls, recordedCall{method, params})
	f.mu.Unlock()
	return params
}
func (f *fakeBackend) Call(ctx context.Context, method string, p, out any) error {
	params := f.record(method, p)
	v := any(map[string]any{"id": "receipt-1"})
	var e error
	if f.call != nil {
		v, e = f.call(ctx, method, params)
	}
	if e != nil {
		return e
	}
	if out == nil {
		return nil
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}
func (f *fakeBackend) Session(ctx context.Context, target string, o connect.SessionOptions, in io.Reader, out, errOut io.Writer, _ <-chan connect.WindowSize, _ <-chan string) (int, error) {
	f.record("connect.session", map[string]any{"target": target, "options": o})
	if f.session != nil {
		return f.session(ctx, target, o, in, out, errOut)
	}
	return 0, nil
}
func (f *fakeBackend) Proxy(ctx context.Context, target string, in io.Reader, out io.Writer) error {
	f.record("connect.proxy", map[string]any{"target": target})
	if f.proxy != nil {
		return f.proxy(ctx, target, in, out)
	}
	return nil
}
func (f *fakeBackend) Stream(ctx context.Context, method string, p any, in io.Reader, out io.Writer) (map[string]any, error) {
	params := f.record(method, p)
	if f.stream != nil {
		return f.stream(ctx, method, params, in, out)
	}
	return map[string]any{"id": "stream-1"}, nil
}
func testOptions(t *testing.T, b *fakeBackend) (Options, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return Options{In: strings.NewReader(""), Out: out, Err: errOut, ConfigOptions: config.Options{HomeDir: home, WorkDir: home, NoProject: true, Env: map[string]string{}}, Hooks: Hooks{NewClient: func(*config.Config, string) (Backend, error) { return b, nil }}}, out, errOut
}
func executeTest(t *testing.T, opts Options, args ...string) int {
	t.Helper()
	return ExecuteOptions(context.Background(), args, opts)
}

func TestNativeExecPreservesArgumentsAndExitWithoutSchoolAuthentication(t *testing.T) {
	b := &fakeBackend{}
	b.session = func(_ context.Context, target string, o connect.SessionOptions, _ io.Reader, out, errOut io.Writer) (int, error) {
		if target != "gpu" || o.PTY || o.Term != "xterm-256color" {
			t.Fatalf("unexpected session: target=%s options=%+v", target, o)
		}
		want := []string{"printf", "%s", "an argument with spaces", "$(no-shell-expansion)"}
		if !reflect.DeepEqual(o.Command, want) {
			t.Fatalf("arguments altered: %#v", o.Command)
		}
		_, _ = out.Write([]byte("remote\x00output\n"))
		_, _ = errOut.Write([]byte("remote stderr\n"))
		return 37, nil
	}
	opts, out, errOut := testOptions(t, b)
	code := executeTest(t, opts, "exec", "gpu", "--", "printf", "%s", "an argument with spaces", "$(no-shell-expansion)")
	if code != 37 || out.String() != "remote\x00output\n" || errOut.String() != "remote stderr\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if len(b.calls) != 1 || b.calls[0].method != "connect.session" {
		t.Fatalf("session consulted school authentication: %+v", b.calls)
	}
}

func TestProxyIsByteCleanAndDoesNotAuthenticate(t *testing.T) {
	b := &fakeBackend{proxy: func(_ context.Context, _ string, in io.Reader, out io.Writer) error {
		_, e := io.Copy(out, in)
		return e
	}}
	opts, out, errOut := testOptions(t, b)
	data := []byte{0, 255, 10, 13, 27, 'S', 'S', 'H'}
	opts.In = bytes.NewReader(data)
	if code := executeTest(t, opts, "proxy", "gpu"); code != 0 || !bytes.Equal(out.Bytes(), data) || errOut.Len() != 0 {
		t.Fatalf("proxy modified stream: exit=%d out=%q err=%q", code, out, errOut)
	}
	if len(b.calls) != 1 || b.calls[0].method != "connect.proxy" {
		t.Fatalf("unexpected calls: %+v", b.calls)
	}
}

func TestInvalidLifecycleRequestsDoNotReachController(t *testing.T) {
	cases := []struct {
		name string
		args []string
		code int
	}{
		{"missing creation cap", []string{"compute", "instance", "create", "demo"}, 1},
		{"missing job cap", []string{"compute", "job", "run", "--", "true"}, 1},
		{"format conflicts with ssh", []string{"up", "demo", "--ssh", "--json"}, 2},
		{"readiness required for ssh", []string{"up", "demo", "--ssh", "--no-wait"}, 2},
		{"ambiguous deadline", []string{"compute", "lease", "set", "demo", "--max-runtime", "1h", "--until", "2099-01-01T00:00:00Z"}, 2},
		{"ambiguous renewal", []string{"compute", "job", "update", "demo", "--renew", "--no-renew"}, 2},
		{"invalid grace", []string{"compute", "lease", "set", "demo", "--stop-grace", "-1s"}, 2},
		{"secret argv is unsupported", []string{"login", "--password", "secret"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeBackend{}
			opts, out, errOut := testOptions(t, b)
			if code := executeTest(t, opts, tc.args...); code != tc.code {
				t.Fatalf("exit=%d want=%d err=%s", code, tc.code, errOut)
			}
			if len(b.calls) > 0 || out.Len() > 0 {
				t.Fatalf("invalid input triggered operation/output: calls=%+v out=%s", b.calls, out)
			}
		})
	}
}

func TestConfiguredCapAndExplicitFalseArePreserved(t *testing.T) {
	b := &fakeBackend{}
	opts, _, errOut := testOptions(t, b)
	opts.ConfigOptions.Overrides = map[string]any{"policy.max_runtime": "2h", "policy.auto_renew": true, "policy.force_stop": true}
	if code := executeTest(t, opts, "compute", "instance", "create", "demo", "--max-points-per-day", "10", "--no-renew", "--force-stop=false", "--wait=false", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	if len(b.calls) != 1 {
		t.Fatalf("calls=%+v", b.calls)
	}
	p := b.calls[0].params
	pol := p["policy"].(map[string]any)
	if pol["max_runtime"] != "2h" || pol["auto_renew"] != false || pol["force_stop"] != false || p["wait"] != false {
		t.Fatalf("explicit policy lost: %#v", p)
	}
	if _, ok := p["spec_id"]; ok {
		t.Fatal("absent flag overwrote layered template")
	}
}

func TestStartDoesNotAllocate(t *testing.T) {
	backend := &fakeBackend{}
	opts, _, diag := testOptions(t, backend)
	if code := executeTest(t, opts, "compute", "instance", "start", "demo", "--json"); code != 0 || len(backend.calls) != 1 || backend.calls[0].method != "instance.start" {
		t.Fatal(code, diag.String(), backend.calls)
	}
}

func TestLeaseUpdateSendsOnlyExplicitPolicyFields(t *testing.T) {
	b := &fakeBackend{}
	opts, _, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "compute", "lease", "set", "demo", "--no-renew", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	want := map[string]any{"auto_renew": false}
	if !reflect.DeepEqual(b.calls[0].params["policy"], want) {
		t.Fatalf("update reset unrelated defaults: %+v", b.calls)
	}
}

func TestPasswordStdinIsNeverRendered(t *testing.T) {
	const secret = "S3cret with spaces!"
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "auth.status":
			return domain.AuthStatus{Gateway: "authenticated"}, nil
		case "auth.login":
			if p["username"] != "student" || p["password"] != secret {
				t.Fatalf("credential input lost: %#v", p)
			}
			return map[string]any{"state": "authenticated", "password": secret, "nested": map[string]any{"access_token": secret}}, nil
		default:
			t.Fatalf("unexpected call %s", method)
			return nil, nil
		}
	}}
	opts, out, errOut := testOptions(t, b)
	opts.In = strings.NewReader(secret + "\n")
	if code := executeTest(t, opts, "login", "--username", "student", "--password-stdin", "--non-interactive", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	if strings.Contains(out.String()+errOut.String(), secret) {
		t.Fatal("credential leaked to CLI output")
	}
	if !json.Valid(out.Bytes()) || errOut.Len() != 0 {
		t.Fatalf("machine output mixed with diagnostics: out=%q err=%q", out, errOut)
	}
}

func TestJSONErrorsStayOnStderr(t *testing.T) {
	b := &fakeBackend{call: func(context.Context, string, map[string]any) (any, error) {
		return nil, &domain.Error{Code: "AUTH_REQUIRED", Message: "school approval expired", Retryable: false}
	}}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "compute", "instance", "ls", "--json"); code != 1 || out.Len() != 0 {
		t.Fatalf("exit=%d out=%q err=%q", code, out, errOut)
	}
	var envelope struct{ Error domain.Error }
	if e := json.Unmarshal(errOut.Bytes(), &envelope); e != nil || envelope.Error.Code != "AUTH_REQUIRED" {
		t.Fatalf("invalid structured error: %q (%v)", errOut, e)
	}
}

func TestJSONLAndQuietListsAreComposable(t *testing.T) {
	for _, format := range []string{"--jsonl", "--output=id"} {
		t.Run(format, func(t *testing.T) {
			b := &fakeBackend{call: func(context.Context, string, map[string]any) (any, error) {
				return []map[string]any{{"id": "a", "name": "first"}, {"id": "b", "name": "second"}}, nil
			}}
			opts, out, errOut := testOptions(t, b)
			if code := executeTest(t, opts, "compute", "instance", "ls", format); code != 0 || errOut.Len() != 0 {
				t.Fatalf("exit=%d err=%q", code, errOut)
			}
			if format == "--output=id" {
				if out.String() != "a\nb\n" {
					t.Fatalf("quiet output=%q", out)
				}
			} else {
				lines := strings.Split(strings.TrimSpace(out.String()), "\n")
				if len(lines) != 2 || !json.Valid([]byte(lines[0])) || !json.Valid([]byte(lines[1])) {
					t.Fatalf("jsonl output=%q", out)
				}
			}
		})
	}
}

func TestDownloadStdoutContainsOnlyFileBytes(t *testing.T) {
	data := []byte{0, 1, 2, 255, '\n'}
	b := &fakeBackend{stream: func(_ context.Context, method string, _ map[string]any, _ io.Reader, out io.Writer) (map[string]any, error) {
		if method != "data.download" {
			t.Fatalf("method=%s", method)
		}
		_, e := out.Write(data)
		return map[string]any{"bytes": len(data)}, e
	}}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "compute", "storage", "pull", "result.bin", "-"); code != 0 || !bytes.Equal(out.Bytes(), data) || errOut.Len() != 0 {
		t.Fatalf("download not byte-clean: exit=%d out=%q err=%q", code, out, errOut)
	}
}

func TestFailedDownloadKeepsExistingFileAndRemovesPartial(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "result.bin")
	if e := os.WriteFile(dest, []byte("original"), 0600); e != nil {
		t.Fatal(e)
	}
	b := &fakeBackend{stream: func(_ context.Context, _ string, _ map[string]any, _ io.Reader, out io.Writer) (map[string]any, error) {
		_, _ = out.Write([]byte("partial"))
		return nil, errors.New("interrupted transport")
	}}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "compute", "storage", "pull", "result.bin", dest, "--overwrite"); code != 1 || out.Len() != 0 {
		t.Fatalf("exit=%d out=%q err=%q", code, out, errOut)
	}
	contents, e := os.ReadFile(dest)
	if e != nil || string(contents) != "original" {
		t.Fatalf("existing data changed: %q %v", contents, e)
	}
	entries, e := os.ReadDir(dir)
	if e != nil || len(entries) != 1 {
		t.Fatalf("partial file left behind: %v %v", entries, e)
	}
}

func TestRunForegroundKeepsJSONSeparateAndReturnsRemoteStatus(t *testing.T) {
	exit := 9
	b := &fakeBackend{call: func(_ context.Context, method string, _ map[string]any) (any, error) {
		switch method {
		case "job.run":
			return map[string]any{"id": "job-1"}, nil
		case "job.get":
			return domain.Run{ID: "job-1", State: "done", CleanupState: "complete", ExitCode: &exit}, nil
		default:
			return nil, errors.New("unexpected RPC " + method)
		}
	}, stream: func(_ context.Context, method string, _ map[string]any, _ io.Reader, out io.Writer) (map[string]any, error) {
		if method != "job.logs" {
			return nil, errors.New("unexpected stream")
		}
		_, e := io.WriteString(out, "program log\n")
		return nil, e
	}}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "compute", "job", "run", "--max-runtime", "1h", "--max-points-per-day", "1.25", "--json", "--", "false"); code != 9 {
		t.Fatalf("exit=%d err=%q", code, errOut)
	}
	if !json.Valid(out.Bytes()) || errOut.String() != "program log\n" {
		t.Fatalf("mixed program/results: stdout=%q stderr=%q", out, errOut)
	}
}

func TestProjectCannotSetTrustedControllerEndpoint(t *testing.T) {
	project := filepath.Join(t.TempDir(), "bupt.toml")
	if e := os.WriteFile(project, []byte("version = 1\n"), 0600); e != nil {
		t.Fatal(e)
	}
	opts, out, errOut := testOptions(t, &fakeBackend{})
	opts.ConfigOptions.NoProject = false
	opts.ConfigOptions.ProjectFile = project
	if code := executeTest(t, opts, "config", "set", "profiles.bupt.controller", "ssh://untrusted.example", "--project"); code != 1 || out.Len() != 0 {
		t.Fatalf("exit=%d out=%s err=%s", code, out, errOut)
	}
	contents, e := os.ReadFile(project)
	if e != nil || string(contents) != "version = 1\n" {
		t.Fatalf("project changed: %q %v", contents, e)
	}
}

func TestInitSupportsOptionalLocalForegroundController(t *testing.T) {
	opts, _, errOut := testOptions(t, &fakeBackend{})
	var got map[string]any
	opts.Hooks.Local = func(_ context.Context, _ *config.Config, method string, p map[string]any, _ io.Reader, _, _ io.Writer) (any, error) {
		if method != "init" {
			t.Fatalf("method=%s", method)
		}
		got = p
		return map[string]any{"mode": "local"}, nil
	}
	if code := executeTest(t, opts, "init", "--mode", "local", "--install=false", "--foreground", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	if got["mode"] != "local" || got["install"] != false || got["foreground"] != true {
		t.Fatalf("local options lost: %#v", got)
	}
}

func TestHelpNeedsNoConfigurationOrController(t *testing.T) {
	opts, out, errOut := testOptions(t, &fakeBackend{})
	opts.LoadConfig = func(config.Options) (*config.Config, error) { t.Fatal("help loaded configuration"); return nil, nil }
	if code := executeTest(t, opts, "help", "--all"); code != 0 || errOut.Len() != 0 {
		t.Fatalf("exit=%d err=%q", code, errOut)
	}
	for _, name := range []string{"auth", "agent", "config", "profile", "compute instance", "compute catalog", "compute lease", "compute job run", "ssh", "exec", "compute storage", "compute snapshot", "compute service", "client key"} {
		if !strings.Contains(out.String(), "  "+name+" ") {
			t.Errorf("help missing %s", name)
		}
	}
}

type fakeNodeBackend struct{ *fakeBackend }

func (f *fakeNodeBackend) ProxyNode(_ context.Context, target, node string, in io.Reader, out io.Writer) error {
	f.record("connect.proxy", map[string]any{"target": target, "node": node})
	_, e := io.Copy(out, in)
	return e
}
func (f *fakeNodeBackend) SessionNode(_ context.Context, target, node string, o connect.SessionOptions, _ io.Reader, out, errOut io.Writer, _ <-chan connect.WindowSize, _ <-chan string) (int, error) {
	f.record("connect.session", map[string]any{"target": target, "node": node, "options": o})
	_, e := io.WriteString(out, "node output\n")
	return 0, e
}

func TestNodeSpecificCachedConnections(t *testing.T) {
	for _, command := range [][]string{{"proxy", "gpu", "--node", "worker-2"}, {"exec", "gpu", "--node", "worker-2", "--", "hostname"}} {
		t.Run(command[0], func(t *testing.T) {
			base := &fakeBackend{}
			b := &fakeNodeBackend{base}
			opts, out, errOut := testOptions(t, base)
			opts.Hooks.NewClient = func(*config.Config, string) (Backend, error) { return b, nil }
			opts.In = strings.NewReader("SSH-2.0-test\r\n")
			if code := executeTest(t, opts, command...); code != 0 || errOut.Len() != 0 {
				t.Fatalf("exit=%d err=%q", code, errOut)
			}
			if len(base.calls) != 1 || base.calls[0].params["node"] != "worker-2" {
				t.Fatalf("node route lost: %+v", base.calls)
			}
			if command[0] == "proxy" && out.String() != "SSH-2.0-test\r\n" {
				t.Fatalf("proxy altered stream: %q", out)
			}
		})
	}
}

func TestNodeCapabilityFailureDoesNotSilentlySelectAnotherNode(t *testing.T) {
	b := &fakeBackend{}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "proxy", "gpu", "--node", "worker-2"); code != 1 || len(b.calls) != 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "TRANSPORT_CAPABILITY") {
		t.Fatalf("unsafe fallback: exit=%d calls=%+v out=%q err=%q", code, b.calls, out, errOut)
	}
}

func TestRecursiveCopyProducesOneJSONResultAndUsesSelectedNode(t *testing.T) {
	source := t.TempDir()
	for _, name := range []string{"one.txt", "two.txt"} {
		if e := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); e != nil {
			t.Fatal(e)
		}
	}
	b := &fakeBackend{stream: func(_ context.Context, method string, p map[string]any, in io.Reader, _ io.Writer) (map[string]any, error) {
		if method != "connect.upload" || p["node"] != "worker-2" {
			t.Fatalf("file route=%s %#v", method, p)
		}
		data, e := io.ReadAll(in)
		return map[string]any{"path": p["path"], "bytes": len(data)}, e
	}}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "cp", source, "gpu:work", "--recursive", "--node", "worker-2", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	var results []map[string]any
	if e := json.Unmarshal(out.Bytes(), &results); e != nil || len(results) != 2 {
		t.Fatalf("not a single result array: %q %v", out, e)
	}
	for _, call := range b.calls {
		if !strings.HasPrefix(call.method, "connect.") || call.params["node"] != "worker-2" {
			t.Fatalf("copy authenticated or omitted node: %+v", call)
		}
	}
}

func TestUploadResumeIdentityHashesContentsAndRewinds(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sample.bin")
	data := []byte("content\x00for\xffresume")
	if e := os.WriteFile(file, data, 0600); e != nil {
		t.Fatal(e)
	}
	digest := sha256.Sum256(data)
	b := &fakeBackend{stream: func(_ context.Context, method string, p map[string]any, in io.Reader, _ io.Writer) (map[string]any, error) {
		if method != "data.upload" || p["source_hash"] != hex.EncodeToString(digest[:]) || p["upload_id"] != "pending-1" {
			t.Fatalf("resume identity=%s %#v", method, p)
		}
		got, e := io.ReadAll(in)
		if !bytes.Equal(got, data) {
			t.Fatalf("hashing did not rewind upload: %q", got)
		}
		return map[string]any{"id": "pending-1"}, e
	}}
	opts, _, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "compute", "storage", "push", file, "sample.bin", "--upload-id", "pending-1", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%q", code, errOut)
	}
}

func TestInitCanCreateAnExplicitMissingConfig(t *testing.T) {
	opts, _, errOut := testOptions(t, &fakeBackend{})
	target := filepath.Join(t.TempDir(), "new-config.toml")
	called := false
	opts.Hooks.Local = func(_ context.Context, c *config.Config, method string, _ map[string]any, _ io.Reader, _, _ io.Writer) (any, error) {
		called = true
		if method != "init" || c.Paths.ConfigFile != target {
			t.Fatalf("init target=%s %+v", method, c.Paths)
		}
		return map[string]any{"configured": true}, nil
	}
	if code := executeTest(t, opts, "init", "--config", target, "--json"); code != 0 || !called {
		t.Fatalf("exit=%d called=%v err=%s", code, called, errOut)
	}
}

func TestJobWaitReturnsForExpiredAndExternallyStoppedRuns(t *testing.T) {
	exit := 42
	for _, tc := range []struct {
		name string
		run  domain.Run
		want int
	}{
		{"deadline", domain.Run{Kind: "job", State: "expired"}, 124},
		{"known remote failure", domain.Run{Kind: "job", State: "expired", ExitCode: &exit}, 42},
		{"external stop", domain.Run{Kind: "job", State: "stopped"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeBackend{call: func(context.Context, string, map[string]any) (any, error) { return tc.run, nil }}
			opts, _, errOut := testOptions(t, b)
			if code := executeTest(t, opts, "compute", "job", "wait", "job-1", "--timeout", "100ms", "--json"); code != tc.want {
				t.Fatalf("exit=%d want=%d err=%s", code, tc.want, errOut)
			}
			if len(b.calls) != 1 {
				t.Fatalf("terminal state was polled repeatedly: %+v", b.calls)
			}
		})
	}
}

func TestCIClientRegistrationUsesOnlyPublicKeyAndSelectedScope(t *testing.T) {
	public, _, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	key, e := ssh.NewPublicKey(public)
	if e != nil {
		t.Fatal(e)
	}
	filename := filepath.Join(t.TempDir(), "ci.pub")
	if e = os.WriteFile(filename, ssh.MarshalAuthorizedKey(key), 0600); e != nil {
		t.Fatal(e)
	}
	b := &fakeBackend{}
	opts, _, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "client", "register", "ci-monitor", "--public-key-file", filename, "--read-only", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	if len(b.calls) != 1 || b.calls[0].method != "client.register" {
		t.Fatalf("calls=%+v", b.calls)
	}
	p := b.calls[0].params
	if p["owner"] != false || p["read_only"] != true || !reflect.DeepEqual(p["profiles"], []any{"bupt"}) || !strings.HasPrefix(p["public_key"].(string), "ssh-ed25519 ") {
		t.Fatalf("CI scope=%#v", p)
	}
	if _, ok := p["identity_file"]; ok {
		t.Fatal("private identity crossed RPC boundary")
	}
}

func TestSSHConfigRawExportRejectsMachineFormattingBeforeLocalWrite(t *testing.T) {
	opts, out, errOut := testOptions(t, &fakeBackend{})
	opts.Hooks.Local = func(context.Context, *config.Config, string, map[string]any, io.Reader, io.Writer, io.Writer) (any, error) {
		t.Fatal("invalid export reached local writer")
		return nil, nil
	}
	if code := executeTest(t, opts, "ssh-config", "export", "--json"); code != 2 || out.Len() != 0 {
		t.Fatalf("exit=%d out=%q err=%q", code, out, errOut)
	}
}
