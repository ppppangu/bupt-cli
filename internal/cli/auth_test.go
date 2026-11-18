package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
)

func TestInteractiveLoginContinuesCaptchaWithOnePasswordPrompt(t *testing.T) {
	image := []byte("\x89PNG\r\n\x1a\nCAPTCHA image fixture")
	for _, dataURI := range []bool{false, true} {
		name := "raw-base64"
		if dataURI {
			name = "data-uri"
		}
		t.Run(name, func(t *testing.T) {
			const password = "secret-retained-only-for-this-login"
			const uuid = "opaque-challenge-do-not-display"
			const answer = "user-entered-code"
			encoded := base64.StdEncoding.EncodeToString(image)
			if dataURI {
				encoded = "data:image/png;base64," + encoded
			}
			loginCalls := 0
			b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
				switch method {
				case "auth.status":
					return domain.AuthStatus{Gateway: "authenticated"}, nil
				case "auth.login":
					loginCalls++
					if p["username"] != "student" || p["password"] != password {
						t.Fatal("credentials changed during CAPTCHA continuation")
					}
					if loginCalls == 1 {
						return nil, domain.E("CAPTCHA_REQUIRED", "platform requires CAPTCHA")
					}
					if p["captcha_uuid"] != uuid || p["captcha_code"] != answer {
						t.Fatal("the user's CAPTCHA response was not sent with its challenge")
					}
					return map[string]any{"authenticated": true}, nil
				case "auth.captcha":
					return map[string]any{"uuid": uuid, "image": encoded}, nil
				default:
					t.Fatalf("unexpected RPC %s", method)
					return nil, nil
				}
			}}
			opts, out, errOut := testOptions(t, b)
			opts.In = strings.NewReader(answer + "\n")
			opts.Interactive = func() bool { return true }
			passwordPrompts := 0
			viewerCalls := 0
			opts.ReadSecret = func(string) (string, error) { passwordPrompts++; return password, nil }
			opts.OpenImage = func(_ context.Context, filename string) error {
				viewerCalls++
				wantDir := filepath.Join(opts.ConfigOptions.HomeDir, ".bupt", "state", "captchas")
				if filepath.Dir(filename) != wantDir {
					t.Fatalf("CAPTCHA saved outside state directory: %s", filename)
				}
				for _, entry := range []struct {
					path string
					mode os.FileMode
				}{{filename, 0600}, {wantDir, 0700}} {
					info, e := os.Stat(entry.path)
					if e != nil || info.Mode().Perm() != entry.mode {
						t.Fatalf("unsafe CAPTCHA permissions: %s %v %v", entry.path, info, e)
					}
				}
				data, e := os.ReadFile(filename)
				if e != nil || !bytes.Equal(data, image) {
					t.Fatalf("image was not decoded intact: %v", e)
				}
				if !dataURI {
					return errors.New("no desktop viewer")
				}
				return nil
			}
			if code := executeTest(t, opts, "auth", "login", "--local", "--username", "student", "--json"); code != 0 {
				t.Fatalf("exit=%d stderr=%q", code, errOut)
			}
			if loginCalls != 2 || passwordPrompts != 1 || viewerCalls != 1 {
				t.Fatalf("login=%d password prompts=%d viewer=%d", loginCalls, passwordPrompts, viewerCalls)
			}
			if !json.Valid(out.Bytes()) || !strings.Contains(errOut.String(), "CAPTCHA code:") {
				t.Fatalf("missing interaction or invalid machine result: out=%q err=%q", out, errOut)
			}
			for _, secret := range []string{password, uuid, answer} {
				if strings.Contains(out.String()+errOut.String(), secret) {
					t.Fatal("credentials or CAPTCHA response/identifier leaked to output")
				}
			}
			if !dataURI && !strings.Contains(errOut.String(), "manually") {
				t.Fatal("viewer failure did not preserve manual image access")
			}
		})
	}
}

func TestNoninteractiveCaptchaNeverPromptsOrOpensViewer(t *testing.T) {
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "auth.status":
			return domain.AuthStatus{Gateway: "authenticated"}, nil
		case "auth.login":
			if p["captcha_uuid"] != "provided-challenge" || p["captcha_code"] != "provided-code" {
				t.Fatal("stdin CAPTCHA fields were lost")
			}
			return nil, domain.E("CAPTCHA_REQUIRED", "new CAPTCHA required")
		default:
			t.Fatalf("noninteractive login initiated %s", method)
			return nil, nil
		}
	}}
	opts, out, errOut := testOptions(t, b)
	opts.In = strings.NewReader(`{"username":"student","password":"stdin-secret","captcha_uuid":"provided-challenge","captcha_code":"provided-code"}`)
	opts.Interactive = func() bool { return true }
	opts.ReadSecret = func(string) (string, error) { t.Fatal("noninteractive login prompted"); return "", nil }
	opts.OpenImage = func(context.Context, string) error { t.Fatal("noninteractive login opened a viewer"); return nil }
	if code := executeTest(t, opts, "auth", "login", "--credentials-stdin", "--non-interactive", "--json"); code != 1 || out.Len() != 0 {
		t.Fatalf("exit=%d stdout=%q stderr=%q", code, out, errOut)
	}
	if len(b.calls) != 2 || strings.Contains(errOut.String(), "stdin-secret") {
		t.Fatalf("unexpected calls or leaked password: %+v %q", b.calls, errOut)
	}
}

func TestInteractiveCaptchaHasThreeChallengeLimitAndDoesNotReadAhead(t *testing.T) {
	loginCalls, captchaCalls, viewerCalls, passwordPrompts := 0, 0, 0, 0
	b := &fakeBackend{call: func(_ context.Context, method string, _ map[string]any) (any, error) {
		switch method {
		case "auth.status":
			return domain.AuthStatus{Gateway: "authenticated"}, nil
		case "auth.login":
			loginCalls++
			return nil, domain.E("CAPTCHA_REQUIRED", "CAPTCHA did not match")
		case "auth.captcha":
			captchaCalls++
			return map[string]any{"uuid": "challenge", "image": base64.StdEncoding.EncodeToString([]byte("image"))}, nil
		default:
			t.Fatalf("unexpected RPC %s", method)
			return nil, nil
		}
	}}
	opts, out, errOut := testOptions(t, b)
	input := strings.NewReader("student\nwrong-one\nwrong-two\nwrong-three\nnot-consumed\n")
	opts.In = input
	opts.Interactive = func() bool { return true }
	opts.ReadSecret = func(string) (string, error) { passwordPrompts++; return "secret", nil }
	opts.OpenImage = func(context.Context, string) error { viewerCalls++; return nil }
	if code := executeTest(t, opts, "auth", "login", "--local", "--json"); code != 1 || out.Len() != 0 {
		t.Fatalf("exit=%d out=%q err=%q", code, out, errOut)
	}
	if loginCalls != 4 || captchaCalls != 3 || viewerCalls != 3 || passwordPrompts != 1 {
		t.Fatalf("unbounded/restarted login: logins=%d challenges=%d viewers=%d passwords=%d", loginCalls, captchaCalls, viewerCalls, passwordPrompts)
	}
	rest, e := io.ReadAll(input)
	if e != nil || string(rest) != "not-consumed\n" {
		t.Fatalf("read ahead into unused input: %q %v", rest, e)
	}
}

func TestOtherLoginFailuresDoNotTriggerCaptcha(t *testing.T) {
	b := &fakeBackend{call: func(_ context.Context, method string, _ map[string]any) (any, error) {
		if method == "auth.status" {
			return domain.AuthStatus{Gateway: "authenticated"}, nil
		}
		if method != "auth.login" {
			t.Fatalf("unexpected RPC %s", method)
		}
		return nil, domain.E("INVALID_CREDENTIALS", "incorrect credentials")
	}}
	opts, out, errOut := testOptions(t, b)
	opts.Interactive = func() bool { return true }
	opts.ReadSecret = func(string) (string, error) { return "secret", nil }
	opts.OpenImage = func(context.Context, string) error { t.Fatal("unrelated failure opened CAPTCHA viewer"); return nil }
	if code := executeTest(t, opts, "auth", "login", "--local", "--username", "student", "--json"); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "INVALID_CREDENTIALS") {
		t.Fatalf("exit=%d out=%q err=%q", code, out, errOut)
	}
}

func TestWebVPNTakeoverRequiresExplicitFlag(t *testing.T) {
	b := &fakeBackend{}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "auth", "confirm", "--json"); code != 2 || len(b.calls) != 0 || out.Len() != 0 {
		t.Fatalf("implicit takeover: exit=%d calls=%+v stdout=%q stderr=%q", code, b.calls, out, errOut)
	}
	opts, out, errOut = testOptions(t, b)
	if code := executeTest(t, opts, "auth", "confirm", "--take-over", "--json"); code != 0 || len(b.calls) != 1 || b.calls[0].method != "auth.gateway.confirm" || b.calls[0].params["take_over"] != true {
		t.Fatalf("explicit confirmation failed: exit=%d calls=%+v stdout=%q stderr=%q", code, b.calls, out, errOut)
	}
}

func TestDevicePollingStopsAtTakeoverConfirmationWithoutApplyingIt(t *testing.T) {
	b := &fakeBackend{call: func(_ context.Context, method string, _ map[string]any) (any, error) {
		switch method {
		case "auth.gateway.start":
			return domain.AuthChallenge{State: "pending"}, nil
		case "auth.gateway.poll":
			return domain.AuthChallenge{State: "confirmation_required"}, nil
		default:
			t.Fatalf("confirmation was performed without user action: %s", method)
			return nil, nil
		}
	}}
	opts, out, errOut := testOptions(t, b)
	if code := executeTest(t, opts, "auth", "device", "--wait", "--json"); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), "AUTH_CONFIRM_REQUIRED") || !strings.Contains(errOut.String(), "--take-over") {
		t.Fatalf("confirmation was hidden or retried: exit=%d out=%q err=%q", code, out, errOut)
	}
	if len(b.calls) != 2 {
		t.Fatalf("unexpected confirmation requests: %+v", b.calls)
	}
}

func TestLogoutCommandPaths(t *testing.T) {
	for _, path := range [][]string{{"logout"}, {"auth", "logout"}} {
		t.Run(strings.Join(path, "-"), func(t *testing.T) {
			b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
				if method != "auth.logout" {
					t.Fatal("wrong logout RPC", method)
				}
				if p["dry_run"] == true {
					return map[string]any{"operation": method, "dry_run": true}, nil
				}
				return map[string]bool{"logged_out": true, "ssh_routes_preserved": true}, nil
			}}
			opts, out, diag := testOptions(t, b)
			run := func(flags ...string) int {
				out.Reset()
				diag.Reset()
				return executeTest(t, opts, append(append([]string{}, path...), flags...)...)
			}
			if code := run("--json"); code != 0 || len(b.calls) != 1 || len(b.calls[0].params) != 0 {
				t.Fatalf("unexpected default logout: exit=%d calls=%v stderr=%s", code, b.calls, diag)
			}
			var result struct {
				LoggedOut bool `json:"logged_out"`
			}
			if err := json.Unmarshal(out.Bytes(), &result); err != nil || !result.LoggedOut {
				t.Fatalf("unexpected receipt: %s", out)
			}
			if code := run("--dry-run", "--json"); code != 0 || len(b.calls) != 2 || b.calls[1].params["dry_run"] != true || !strings.Contains(out.String(), "auth.logout") {
				t.Fatalf("unexpected preview: exit=%d calls=%v out=%s", code, b.calls, out)
			}
			if code := run("--force", "-q"); code != 0 || len(b.calls) != 3 || b.calls[2].params["force"] != true || out.Len() != 0 {
				t.Fatalf("unexpected quiet logout: exit=%d calls=%v out=%s", code, b.calls, out)
			}
			if code := run("extra"); code != 2 || len(b.calls) != 3 {
				t.Fatal("invalid arguments reached controller", code, b.calls)
			}
			b.call = func(context.Context, string, map[string]any) (any, error) {
				return nil, domain.E("ACTIVE_RUNS", "active managed runs")
			}
			if code := run("-q"); code == 0 || out.Len() != 0 || !strings.Contains(diag.String(), "ACTIVE_RUNS") || len(b.calls) != 4 {
				t.Fatal("logout error was suppressed or retried", code, b.calls)
			}
		})
	}
}
