package platform

import (
	"bupt.local/cli/internal/domain"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIdentityCodeOneStillRequiresValidPinnedUser(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     map[string]any
		expected string
		errCode  string
	}{
		{"authenticated identity", map[string]any{"code": 1, "uid": 700001, "userState": "active"}, "700001", ""},
		{"missing identity", map[string]any{"code": 1}, "700001", "AUTH_EXPIRED"},
		{"zero identity", map[string]any{"code": 1, "uid": 0}, "700001", "AUTH_EXPIRED"},
		{"different account", map[string]any{"code": 1, "uid": 700002}, "700001", "IDENTITY_MISMATCH"},
		{"explicit auth error", map[string]any{"code": 1032, "uid": 700001}, "700001", "AUTH_1032"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, tc.body) }))
			defer s.Close()
			c, e := New(domain.Profile{Name: "identity", PlatformURL: s.URL, ExpectedUID: domain.ID(tc.expected)}, &memoryVault{})
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			identity, e := c.WhoAmI(context.Background())
			if tc.errCode != "" {
				assertCode(t, e, tc.errCode)
				return
			}
			if e != nil || identity.UID != "700001" {
				t.Fatal(identity, e)
			}
		})
	}
}
