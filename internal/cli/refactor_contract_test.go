package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
)

func TestRemovedPathsAndUnapprovedCreateDoNotSubmit(t *testing.T) {
	cases := [][]string{
		{"up", "lab"}, {"down", "lab"}, {"job"}, {"compute", "run", "run", "--", "true"},
		{"compute", "ssh", "lab"}, {"api", "model", "try", "model"}, {"compute", "instance", "-q"},
		{"compute", "instance", "create", "lab", "--spec", "9", "--max-runtime", "20m", "--non-interactive"},
		{"compute", "instance", "delete", "lab"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			b := &fakeBackend{}
			opts, out, diag := testOptions(t, b)
			if code := executeTest(t, opts, args...); code != 2 || len(b.calls) != 0 || out.Len() != 0 {
				t.Fatal(code, b.calls, out.String(), diag.String())
			}
		})
	}
}

func TestExecSeparatesRemoteStatusFromTransportFailure(t *testing.T) {
	for _, status := range []int{0, 1, 2, 124, 130, 255} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			b := &fakeBackend{session: func(_ context.Context, _ string, _ connect.SessionOptions, _ io.Reader, out, errOut io.Writer) (int, error) {
				fmt.Fprint(out, "remote\x00output")
				fmt.Fprint(errOut, "remote stderr")
				return status, nil
			}}
			opts, out, diag := testOptions(t, b)
			if code := executeTest(t, opts, "exec", "lab", "--", "fixture", "--output=json"); code != status || out.String() != "remote\x00output" || diag.String() != "remote stderr" {
				t.Fatal(code, out.String(), diag.String())
			}
		})
	}
	b := &fakeBackend{session: func(context.Context, string, connect.SessionOptions, io.Reader, io.Writer, io.Writer) (int, error) {
		return 255, io.ErrUnexpectedEOF
	}}
	opts, out, diag := testOptions(t, b)
	if code := executeTest(t, opts, "exec", "lab", "--", "true"); code != 1 || out.Len() != 0 || !strings.HasPrefix(diag.String(), "BUPT[SSH_TRANSPORT] ") {
		t.Fatal(code, out.String(), diag.String())
	}
}

func TestBalanceFieldAndPartialUsage(t *testing.T) {
	balance := 0.0
	b := &fakeBackend{call: func(_ context.Context, method string, _ map[string]any) (any, error) {
		if method == "portal.read" {
			return domain.PortalPage{Section: "usage", Account: &domain.PortalAccount{Balance: &balance}}, nil
		}
		return nil, domain.E("AUTH_REQUIRED", "expired")
	}}
	opts, out, diag := testOptions(t, b)
	if code := executeTest(t, opts, "api", "usage", "--field", "balance"); code != 0 || out.String() != "0\n" || diag.Len() != 0 {
		t.Fatal(code, out.String(), diag.String())
	}
	out.Reset()
	if code := executeTest(t, opts, "usage", "--output=jsonl"); code != 0 || !strings.Contains(out.String(), `"state":"unavailable"`) || !strings.Contains(out.String(), `"code":"AUTH_REQUIRED"`) || strings.Count(out.String(), "\n") != 2 {
		t.Fatal(code, out.String(), diag.String())
	}
}
