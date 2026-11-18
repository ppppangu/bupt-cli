package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
)

func TestDefaultLoginFollowsBothQRStagesWithoutPassword(t *testing.T) {
	const webvpnURL = "https://webvpn.example/approve/first"
	const ssoURL = "https://app.example/approve/second"
	for _, interactive := range []bool{true, false} {
		name := "interactive"
		if !interactive {
			name = "explicit-headless-wait"
		}
		t.Run(name, func(t *testing.T) {
			steps := []domain.AuthChallenge{
				{Stage: "webvpn", State: "waiting", RequiredAction: "scan_qr", URL: webvpnURL, PollAfterMS: 1},
				{Stage: "webvpn", State: "waiting", RequiredAction: "scan_qr", URL: webvpnURL, PollAfterMS: 1},
				{Stage: "university_sso", State: "waiting", RequiredAction: "scan_qr", URL: ssoURL, PollAfterMS: 1},
				{Stage: "university_sso", State: "pending", RequiredAction: "confirm_on_phone", URL: ssoURL, PollAfterMS: 1},
				{Stage: "complete", State: "authenticated"},
			}
			next := 0
			b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
				want := "auth.poll"
				if next == 0 {
					want = "auth.start"
				}
				if method != want {
					t.Fatalf("CLI duplicated authentication business logic: got %s want %s", method, want)
				}
				if _, ok := p["password"]; ok {
					t.Fatal("default QR login sent a password")
				}
				if next >= len(steps) {
					t.Fatal("polling continued after platform authentication")
				}
				step := steps[next]
				next++
				return step, nil
			}}
			opts, out, errOut := testOptions(t, b)
			opts.Interactive = func() bool { return interactive }
			opts.In = failOnRead{t}
			opts.ReadSecret = func(string) (string, error) { t.Fatal("default QR login prompted for a password"); return "", nil }
			opts.OpenImage = func(context.Context, string) error {
				t.Fatal("QR flow launched the local-password CAPTCHA viewer")
				return nil
			}
			args := []string{"auth", "login", "--json"}
			if !interactive {
				args = append(args, "--device", "--wait", "--non-interactive")
			}
			if code := executeTest(t, opts, args...); code != 0 {
				t.Fatalf("exit=%d stderr=%s", code, errOut)
			}
			if next != len(steps) {
				t.Fatalf("flow stopped before platform callback: consumed %d stages", next)
			}
			var result domain.AuthChallenge
			if e := json.Unmarshal(out.Bytes(), &result); e != nil || result.Stage != "complete" || result.State != "authenticated" {
				t.Fatalf("invalid final result: %q %v", out, e)
			}
			if strings.Count(errOut.String(), webvpnURL) != 1 || strings.Count(errOut.String(), ssoURL) != 1 {
				t.Fatal("a repeated challenge URL was displayed more than once")
			}
			if !strings.Contains(errOut.String(), "WebVPN") || !strings.Contains(errOut.String(), "北邮统一身份认证") {
				t.Fatal("the two independent authentication stages were not labeled")
			}
		})
	}
}

type failOnRead struct{ t *testing.T }

func (r failOnRead) Read([]byte) (int, error) { r.t.Fatal("QR login read stdin"); return 0, io.EOF }

func TestLoginNoWaitAndFullFlowPoll(t *testing.T) {
	for _, command := range [][]string{{"auth", "login", "--no-wait", "--json"}, {"auth", "poll", "--json"}} {
		t.Run(command[1], func(t *testing.T) {
			b := &fakeBackend{call: func(_ context.Context, method string, _ map[string]any) (any, error) {
				want := "auth.start"
				if command[1] == "poll" {
					want = "auth.poll"
				}
				if method != want {
					t.Fatalf("got %s want %s", method, want)
				}
				return domain.AuthChallenge{Stage: "university_sso", State: "waiting", URL: "https://app.example/qr"}, nil
			}}
			opts, out, errOut := testOptions(t, b)
			opts.Interactive = func() bool { return true }
			opts.ReadSecret = func(string) (string, error) { t.Fatal("no-wait login requested a password"); return "", nil }
			if code := executeTest(t, opts, command...); code != 0 || len(b.calls) != 1 || !json.Valid(out.Bytes()) {
				t.Fatalf("exit=%d calls=%d out=%q err=%q", code, len(b.calls), out, errOut)
			}
		})
	}
}

func TestRefreshRequiredStopsFullFlowPolling(t *testing.T) {
	b := &fakeBackend{call: func(context.Context, string, map[string]any) (any, error) {
		return domain.AuthChallenge{Stage: "university_sso", State: "refresh_required", RequiredAction: "refresh_qr"}, nil
	}}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "auth", "login", "--device", "--wait", "--json"); code != 1 || out.Len() != 0 || len(b.calls) != 1 || !strings.Contains(errOut.String(), "AUTH_REFRESH_REQUIRED") {
		t.Fatalf("refresh state was hidden or polled: exit=%d calls=%d out=%q err=%q", code, len(b.calls), out, errOut)
	}
}

func TestQRFilesSeparateStagesAndNeverOverwriteExistingFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "login.png")
	if e := os.WriteFile(base, []byte("unrelated existing file"), 0640); e != nil {
		t.Fatal(e)
	}
	var out bytes.Buffer
	r := newAuthChallengeRenderer(&out, false, base, "")
	webvpn := domain.AuthChallenge{Stage: "webvpn", State: "waiting", URL: "https://webvpn.example/one"}
	sso := domain.AuthChallenge{Stage: "university_sso", State: "waiting", URL: "https://app.example/two"}
	for _, c := range []domain.AuthChallenge{webvpn, webvpn, sso, {Stage: "university_sso", State: "waiting", URL: "https://app.example/three"}} {
		if e := r.Show(c); e != nil {
			t.Fatal(e)
		}
	}
	for _, filename := range []string{"login.webvpn.png", "login.sso.png", "login.sso.2.png"} {
		info, e := os.Stat(filepath.Join(dir, filename))
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("QR file permissions: %s %+v %v", filename, info, e)
		}
		data, e := os.ReadFile(filepath.Join(dir, filename))
		if e != nil || !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			t.Fatalf("QR file is not PNG: %s", filename)
		}
	}
	data, e := os.ReadFile(base)
	if e != nil || string(data) != "unrelated existing file" {
		t.Fatal("--qr-file overwrote an existing arbitrary file")
	}
	existing := filepath.Join(dir, "login.webvpn.png")
	before, e := os.ReadFile(existing)
	if e != nil {
		t.Fatal(e)
	}
	r2 := newAuthChallengeRenderer(io.Discard, false, base, "")
	if e = r2.Show(webvpn); !os.IsExist(e) {
		t.Fatalf("new invocation did not reject existing stage file: %v", e)
	}
	after, e := os.ReadFile(existing)
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("existing stage file was replaced")
	}
}

func TestInstanceAccessOnlyStoresExplicitConsoleCredential(t *testing.T) {
	const password = "instance-console-secret"
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "auth.access" || p["password"] != password || len(p) != 1 {
			t.Fatal("console credential triggered a login or wrong operation")
		}
		return map[string]any{"stored": true, "purpose": "instance console SSH bootstrap"}, nil
	}}
	opts, out, errOut := testOptions(t, b)
	opts.In = strings.NewReader(password + "\n")
	if code := executeTest(t, opts, "auth", "access", "--password-stdin", "--non-interactive", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	if len(b.calls) != 1 || strings.Contains(out.String()+errOut.String(), password) || !json.Valid(out.Bytes()) {
		t.Fatal("instance credential leaked or the command performed extra work")
	}
}

func TestSSHEnsureOnlyRequestsRepairOfSelectedExistingNode(t *testing.T) {
	b := &fakeBackend{}
	opts, _, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "ssh", "ensure", "existing-instance", "--node", "worker", "--dry-run", "--json"); code != 0 {
		t.Fatalf("exit=%d err=%s", code, errOut)
	}
	if len(b.calls) != 1 || b.calls[0].method != "route.get" {
		t.Fatalf("ensure allocated or started a session: %+v", b.calls)
	}
	p := b.calls[0].params
	if p["target"] != "existing-instance" || p["node"] != "worker" || p["repair"] != true || p["dry_run"] != true {
		t.Fatalf("repair selection lost: %#v", p)
	}
}

func TestSessionProbeDoesNotLoginAndDryRunDoesNotReachController(t *testing.T) {
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "auth.probe" || p["proxy"] != "socks5://127.0.0.1:12345" {
			t.Fatalf("unexpected probe request: %s %#v", method, p)
		}
		return map[string]any{"gateway": map[string]string{"state": "expired", "reason": "ip_changed"}}, nil
	}}
	opts, _, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "auth", "probe", "--proxy", "socks5://127.0.0.1:12345", "--json"); code != 0 {
		t.Fatal(code, errOut.String())
	}
	if len(b.calls) != 1 {
		t.Fatal("probe performed extra login or mutation calls")
	}
	b2 := &fakeBackend{}
	opts, _, errOut = testOptions(t, b2)
	if code := executeTest(t, opts, "auth", "probe", "--proxy", "socks5://127.0.0.1:12345", "--dry-run", "--json"); code != 0 {
		t.Fatal(code, errOut.String())
	}
	if len(b2.calls) != 0 {
		t.Fatal("probe preview contacted the controller")
	}
}

func TestCampusPasswordLoginWaitsForGatewayAndSubmitsOnce(t *testing.T) {
	const secret = "campus-only-test-secret"
	const captcha = "manual-answer"
	ch := domain.AuthChallenge{Stage: "university_sso", State: "input_required", Kind: domain.CampusPasswordKind, RequiredAction: domain.EnterCampusPassword, ID: "form-1", CaptchaImage: "data:image/png;base64,iVBORw0KGgo="}
	for _, mode := range []string{"terminal", "password-stdin", "credentials-stdin", "resume"} {
		t.Run(mode, func(t *testing.T) {
			issued, submitted := false, 0
			b := &fakeBackend{call: func(_ context.Context, m string, p map[string]any) (any, error) {
				switch m {
				case "auth.start":
					if p["sso_method"] != "password" || len(p) != 1 {
						t.Fatal("credentials sent in start request")
					}
					return domain.AuthChallenge{Stage: "webvpn", Kind: "webvpn_qr", State: "waiting", URL: "https://webvpn.example/device", PollAfterMS: 1}, nil
				case "auth.poll":
					if len(p) != 0 || issued {
						t.Fatal("request while waiting for password")
					}
					issued = true
					step := ch
					if mode == "password-stdin" {
						step.CaptchaImage = ""
					}
					return step, nil
				case "auth.sso.submit":
					submitted++
					if !issued || submitted != 1 || p["challenge_id"] != "form-1" || p["username"] != "my-campus-id" || p["password"] != secret || mode != "password-stdin" && p["captcha_code"] != captcha {
						t.Fatal("incorrect submission")
					}
					return domain.AuthChallenge{Stage: "complete", State: "authenticated", Kind: "platform"}, nil
				default:
					t.Fatalf("campus credentials routed to %s", m)
					return nil, nil
				}
			}}
			opt, out, diag := testOptions(t, b)
			args := []string{"login", "--sso-password", "--json"}
			opt.Interactive = func() bool { return mode == "terminal" }
			if mode == "terminal" {
				opt.In = strings.NewReader("my-campus-id\n" + captcha + "\n")
				opt.ReadSecret = func(prompt string) (string, error) {
					if !issued || !strings.Contains(prompt, "统一身份认证密码") {
						t.Fatal("password prompted outside CAS stage")
					}
					return secret, nil
				}
				opt.OpenImage = func(context.Context, string) error { return nil }
			} else if mode == "password-stdin" {
				args = append(args, "--username", "my-campus-id", "--password-stdin", "--non-interactive")
				opt.In = strings.NewReader(secret + "\n")
			} else {
				if mode == "resume" {
					args = []string{"auth", "poll", "--wait", "--json"}
				} else {
					args = []string{"auth", "login", "--sso-password", "--json"}
				}
				args = append(args, "--non-interactive", "--credentials-stdin")
				opt.In = strings.NewReader(`{"username":"my-campus-id","password":"` + secret + `","captcha_code":"` + captcha + `"}`)
			}
			if code := executeTest(t, opt, args...); code != 0 || submitted != 1 {
				t.Fatal(code, diag.String())
			}
			var final domain.AuthChallenge
			if err := json.Unmarshal(out.Bytes(), &final); err != nil || final.Stage != "complete" {
				t.Fatal("invalid machine output", err)
			}
			if strings.Contains(out.String()+diag.String(), secret) || strings.Contains(out.String()+diag.String(), captcha) {
				t.Fatal("campus credentials leaked")
			}
		})
	}
}

func TestCampusPasswordSkipsInputAndRejectsWrongMethod(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ch    domain.AuthChallenge
		flags []string
		exit  int
	}{
		{"already signed in", domain.AuthChallenge{Stage: "complete", State: "authenticated"}, []string{"--credentials-stdin", "--non-interactive"}, 0},
		{"gateway not ready", domain.AuthChallenge{Stage: "webvpn", State: "waiting", URL: "https://webvpn.example/qr"}, []string{"--no-wait"}, 0},
		{"stdin pending at gateway", domain.AuthChallenge{Stage: "webvpn", State: "waiting", URL: "https://webvpn.example/qr"}, []string{"--no-wait", "--credentials-stdin"}, 0},
		{"no wait at CAS", domain.AuthChallenge{Stage: "university_sso", Kind: domain.CampusPasswordKind, State: "input_required", RequiredAction: domain.EnterCampusPassword, ID: "form"}, []string{"--no-wait"}, 0},
		{"controller uses QR", domain.AuthChallenge{Stage: "university_sso", Kind: "bupt_sso_qr", State: "waiting", URL: "https://sso.example/qr"}, []string{"--wait", "--credentials-stdin"}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &fakeBackend{call: func(_ context.Context, method string, _ map[string]any) (any, error) {
				if method != "auth.start" {
					t.Fatal(method)
				}
				return tc.ch, nil
			}}
			o, _, diag := testOptions(t, b)
			o.In = failOnRead{t}
			o.ReadSecret = func(string) (string, error) { t.Fatal("unexpected credential prompt"); return "", nil }
			if code := executeTest(t, o, append([]string{"login", "--sso-password", "--json"}, tc.flags...)...); code != tc.exit || len(b.calls) != 1 {
				t.Fatal(code, diag.String())
			}
		})
	}
	for _, flags := range [][]string{{"--local"}, {"--gateway-only"}, {"--password-stdin", "--credentials-stdin"}} {
		b := &fakeBackend{}
		o, _, _ := testOptions(t, b)
		o.In = failOnRead{t}
		if code := executeTest(t, o, append([]string{"login", "--sso-password"}, flags...)...); code != 2 || len(b.calls) != 0 {
			t.Fatal("ambiguous flags accepted", flags)
		}
	}
}
