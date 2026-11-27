package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

func TestAgentLoginDisplaysOnePrivateChallengePerStep(t *testing.T) {
	web := domain.AuthChallenge{Stage: "webvpn", State: "waiting", RequiredAction: "scan_qr", PollAfterMS: 1500, URL: "https://example.invalid/private-vpn", ExpiresAt: time.Now().Add(time.Minute)}
	sso := web
	sso.Stage, sso.URL = "university_sso", "https://example.invalid/private-sso"
	rotated := sso
	rotated.URL += "-new"
	complete := domain.AuthChallenge{Stage: "complete", State: "authenticated", RequiredAction: "none"}
	stages := []domain.AuthChallenge{web, web, sso, rotated, complete, complete}
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		n := len(stages)
		want := "auth.poll"
		if n == 6 || n == 1 {
			want = "auth.start"
		}
		if n == 0 || method != want || len(p) != 0 {
			t.Fatalf("unexpected RPC %s %v", method, p)
		}
		ch := stages[0]
		stages = stages[1:]
		return ch, nil
	}}
	opts, out, diag := testOptions(t, b)
	opts.Interactive = func() bool { return true }
	opts.In = failOnRead{t}
	var opened string
	opts.OpenImage = func(_ context.Context, p string) error { opened = p; return errors.New("no desktop") }
	var paths []string
	for i := 0; i < 6; i++ {
		args := []string{"auth", "poll", "--agent"}
		if i == 0 || i == 5 {
			args = []string{"login", "--agent"}
		}
		if i == 3 {
			args = append(args, "--open")
		}
		out.Reset()
		diag.Reset()
		if code := executeTest(t, opts, args...); code != 0 || diag.Len() != 0 || strings.Contains(out.String(), "example.invalid") {
			t.Fatal(code, diag.String(), out.String())
		}
		var step agentLoginStep
		if err := json.Unmarshal(out.Bytes(), &step); err != nil {
			t.Fatal(err)
		}
		if i < 4 {
			path := step.QRFile
			if !filepath.IsAbs(path) || step.PollAfterMS != 1500 || !step.ExpiresAt.Equal(web.ExpiresAt) {
				t.Fatal(step)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal(info, err)
			}
			dir, err := os.Stat(filepath.Dir(path))
			if err != nil || dir.Mode().Perm() != 0700 {
				t.Fatal(dir, err)
			}
			paths = append(paths, path)
		} else if step.QRFile != "" || step.Stage != "complete" || step.PollAfterMS != 0 {
			t.Fatal(step)
		}
		if i < 3 && opened != "" {
			t.Fatal("implicit viewer", opened)
		}
		if i == 3 {
			if opened != step.QRFile || !strings.Contains(step.Hint, "手动") {
				t.Fatal(step)
			}
			before := time.Now().Add(-time.Minute)
			if err := os.Chtimes(paths[0], before, before); err != nil {
				t.Fatal(err)
			}
		}
	}
	if paths[0] != paths[1] || paths[1] == paths[2] || paths[2] == paths[3] {
		t.Fatal(paths)
	}
	if _, err := os.Stat(paths[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expired QR retained", err)
	}
	if _, err := os.Stat(paths[3]); err != nil {
		t.Fatal("valid QR removed", err)
	}
}

func TestAgentPollRequiresUserForExpiredOrTakeover(t *testing.T) {
	for _, tc := range []struct{ state, code, hint string }{{"expired", "AUTH_REQUIRED", "login --agent"}, {"confirmation_required", "AUTH_CONFIRM_REQUIRED", "--take-over"}} {
		b := &fakeBackend{call: func(context.Context, string, map[string]any) (any, error) {
			return domain.AuthChallenge{Stage: "webvpn", State: tc.state, URL: "https://example.invalid/private"}, nil
		}}
		opts, out, diag := testOptions(t, b)
		if code := executeTest(t, opts, "auth", "poll", "--agent"); code != 1 || out.Len() != 0 || !json.Valid(diag.Bytes()) || !strings.Contains(diag.String(), tc.code) || !strings.Contains(diag.String(), tc.hint) || strings.Contains(diag.String(), "example.invalid") || len(b.calls) != 1 {
			t.Fatal(code, out.String(), diag.String(), b.calls)
		}
	}
}
