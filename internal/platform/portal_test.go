package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
)

func TestPortalPagesMergeNativeIDsAndRespectCookiesAndPermissions(t *testing.T) {
	requests := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests[r.URL.Path]++
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/me":
			writeJSON(w, map[string]any{"user": map[string]any{"sid": "campus-1", "name": "student"}, "balance": 24, "consentRequired": false})
		case "/api/models":
			writeJSON(w, []map[string]any{{"id": "model-xyz", "name": "显示名", "inPrice": 0, "outPrice": 3, "ctx": "128K"}, {"id": "missing"}})
		case "/api/status":
			writeJSON(w, map[string]any{"targets": []map[string]any{{"id": "model-xyz", "kind": "chat", "state": "ok", "lastLatencyMs": 20}}})
		case "/api/keys":
			if r.Method == "POST" {
				if r.Header.Get("Origin") != "https://"+r.Host || r.Header.Get("Referer") != "https://"+r.Host+"/apikeys" {
					t.Error("missing native portal origin")
				}
				var p map[string]any
				if json.NewDecoder(r.Body).Decode(&p) != nil || p["name"] != "project" || len(array(p["modelIds"])) != 1 {
					t.Error("invalid create body")
				}
				writeJSON(w, map[string]any{"id": 19, "name": "project", "key": "fixture-one-time-secret"})
				return
			}
			writeJSON(w, map[string]any{"items": []map[string]any{{"id": 19, "name": "project", "models": []string{"model-xyz"}, "key": "fixture-one-time-secret", "masked": "****xyz", "status": "active"}}, "total": 1})
		case "/api/usage":
			if r.URL.Query().Get("days") != "7" {
				t.Error("wrong period")
			}
			writeJSON(w, map[string]any{"periodTokens": 10, "usageAvailable": true})
		case "/api/config/gateway":
			writeJSON(w, map[string]any{"publicBaseUrl": "https://ai.bupt.edu.cn/llm-gw/v1"})
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, e := New(domain.Profile{Name: "test", PlatformURL: server.URL, PortalURL: server.URL}, &memoryVault{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.http.Transport = server.Client().Transport
	p, e := c.PortalClient()
	if e != nil {
		t.Fatal(e)
	}
	account, e := p.PortalAccount(t.Context())
	if e != nil || account.SID != "campus-1" {
		t.Fatal(account, e)
	}
	models, e := p.PortalPage(t.Context(), account, domain.PortalInput{Section: "model"})
	if e != nil || len(models.Models) != 2 {
		t.Fatal(e)
	}
	if m := models.Models[0]; m.ID != "model-xyz" || m.Kind != "chat" || m.Health != "ok" || m.InputPrice == nil || *m.InputPrice != 0 || m.Context != "128K" {
		t.Fatal(m)
	}
	if models.Models[1].Health != "unknown" || models.Models[1].InputPrice != nil {
		t.Fatal("missing status lost a model or invented a price")
	}
	k, secret, e := p.PortalChange(t.Context(), domain.PortalChange{Action: "key.create", Name: "project", Models: []string{"model-xyz"}})
	if e != nil || k.ID != "19" || secret != "fixture-one-time-secret" {
		t.Fatal("creation contract", e)
	}
	keys, e := p.PortalKeys(t.Context())
	if e != nil || len(keys) != 1 || strings.Contains(string(domain.Result(keys)), secret) {
		t.Fatal("keys response leaked a secret", e)
	}
	usage, e := p.PortalPage(t.Context(), account, domain.PortalInput{Section: "usage", Days: 7})
	if e != nil || integer(usage.Data["periodTokens"]) != 10 {
		t.Fatal(e, usage.Data)
	}
	guide, e := p.PortalPage(t.Context(), account, domain.PortalInput{Section: "guide"})
	if e != nil || !strings.Contains(stringValue(guide.Data["curl"]), "/v1/chat/completions") {
		t.Fatal(e)
	}
	account.ConsentRequired = true
	_, e = p.PortalPage(t.Context(), account, domain.PortalInput{Section: "key"})
	assertCode(t, e, "AGREEMENT_REQUIRED")
	if requests["/api/status"] != 1 || requests["/api/models"] != 1 {
		t.Fatal(requests)
	}
}
func TestPortalWriteNeverFollowsRedirectAndHonorsRateLimit(t *testing.T) {
	var calls int
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/api/me/leaderboard-display" {
			http.Redirect(w, r, "/other", http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path == "/other" {
			t.Error("replayed a write")
		}
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer s.Close()
	c, _ := New(domain.Profile{Name: "p", PlatformURL: s.URL, PortalURL: s.URL}, nil)
	defer c.Close()
	c.http.Transport = s.Client().Transport
	p, _ := c.PortalClient()
	_, _, e := p.PortalChange(context.Background(), domain.PortalChange{Action: "preference.set", Mode: "anonymous"})
	assertCode(t, e, "AUTH_EXPIRED")
	for range 2 {
		_, e = p.PortalKeys(t.Context())
		assertCode(t, e, "RATE_LIMITED")
	}
	if calls != 2 {
		t.Fatal("redirect or retry during rate limit", calls)
	}
}

func TestCampusNamespaceOnlyAppliesToNumericCASSubjects(t *testing.T) {
	for in, want := range map[string]string{"cas_20260001": "20260001", "20260001": "20260001", "temp_20260001": "temp_20260001", "cas_x": "cas_x", "cas_": "cas_"} {
		if campusSubject(in) != want {
			t.Fatal(in)
		}
	}
}
