package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
)

func TestPortalCommandsExposeCatalogMeasurementsAndProtectKeyExport(t *testing.T) {
	b := &fakeBackend{call: func(ctx context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "portal.read":
			if p["section"] == "model" {
				price := 1.5
				return domain.PortalPage{Section: "model", Source: "live", Models: []domain.PortalModel{{ID: "m-1", Name: "模型", Kind: "chat", Health: "ok", InputPrice: &price}}}, nil
			}
			return domain.PortalPage{Section: "usage", Data: map[string]any{"periodTokens": 331, "token": "secret-fixture"}}, nil
		case "portal.export":
			return map[string]string{"secret": "private-fixture-credential"}, nil
		}
		t.Fatal("unexpected method", method)
		return nil, nil
	}}
	opts, out, diag := testOptions(t, b)
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"api", "model", "--json"}, "1.5"}, {[]string{"api", "model", "show", "m-1", "--json"}, "m-1"}, {[]string{"api", "model", "--output=id"}, "m-1"}, {[]string{"api", "usage", "--days", "7", "--json"}, "331"}} {
		out.Reset()
		if n := executeTest(t, opts, tc.args...); n != 0 || !strings.Contains(out.String(), tc.want) || strings.Contains(out.String(), "secret-fixture") {
			t.Fatal(n, out.String(), diag.String())
		}
	}
	filename := filepath.Join(t.TempDir(), "private")
	out.Reset()
	if n := executeTest(t, opts, "api", "key", "export", "id", "--file", filename, "--json"); n != 0 || strings.Contains(out.String()+diag.String(), "private-fixture-credential") {
		t.Fatal(n, out.String(), diag.String())
	}
	fi, e := os.Stat(filename)
	if e != nil || fi.Mode().Perm() != 0600 {
		t.Fatal("export mode", e)
	}
	before := len(b.calls)
	if executeTest(t, opts, "api", "key", "export", "id", "--file", filename) == 0 || len(b.calls) != before {
		t.Fatal("overwrote private export")
	}
	out.Reset()
	if executeTest(t, opts, "api", "key", "export", "id", "--file", "-", "--json") == 0 || len(b.calls) != before {
		t.Fatal("secret mixed with machine output")
	}
}
func TestTwoWorkspaceContractsAndCompatibility(t *testing.T) {
	b := &fakeBackend{call: func(ctx context.Context, m string, p map[string]any) (any, error) {
		if m == "instance.list" {
			return []domain.Instance{}, nil
		}
		return map[string]any{}, nil
	}}
	o, out, diag := testOptions(t, b)
	for _, test := range []struct {
		args   []string
		method string
	}{{[]string{"compute", "instance", "--cached", "--json"}, "instance.list"}, {[]string{"compute", "instance", "ls", "--json"}, "instance.list"}, {[]string{"compute", "scheduler", "job", "--json"}, "compute.read"}, {[]string{"compute", "job", "--json"}, "job.list"}, {[]string{"compute", "catalog", "show", "12", "--json"}, "spec.show"}} {
		out.Reset()
		if code := executeTest(t, o, test.args...); code != 0 || b.calls[len(b.calls)-1].method != test.method || !json.Valid(out.Bytes()) {
			t.Fatal(test, diag.String())
		}
	}
	if b.calls[0].params["cached"] != true {
		t.Fatal("cached flag was not forwarded")
	}
	out.Reset()
	if executeTest(t, o) != 0 || !strings.Contains(out.String(), "bupt api") || !strings.Contains(out.String(), "bupt compute") {
		t.Fatal("missing entry points")
	}
}
