package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRemoteCheckoutOutputPreservesSchoolQuotePrecision(t *testing.T) {
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "compute.remote.checkout" || p["dry_run"] != true || p["target"] != "80" || p["name"] != "--quote=literal" {
			t.Fatal(method, p)
		}
		return json.RawMessage(`{"quote":"review-id","details":{"total":9007199254740993.10,"currency":"CNY"}}`), nil
	}}
	opts, out, diag := testOptions(t, b)
	if rc := executeTest(t, opts, "compute", "remote", "order", "create", "--dry-run", "--json", "--spec=80", "--", "--quote=literal"); rc != 0 || !strings.Contains(out.String(), "9007199254740993.10") {
		t.Fatal(rc, out.String(), diag.String())
	}
}
