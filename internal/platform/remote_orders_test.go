package platform

import (
	"bupt.local/cli/internal/domain"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteCheckoutReadsQuoteAndReturnsOnlyOrderReceipt(t *testing.T) {
	posts := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/user":
			writeJSON(w, map[string]any{"code": 1, "uid": 7, "eid": 17})
		case "/api/computehub/app-specs/23":
			writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"id": 23, "name": "test", "price": 1.25, "currency": "CNY", "durationUnit": "hour"}})
		case "/api/computehub/apps/new":
			posts++
			writeJSON(w, map[string]any{"code": 0, "data": map[string]any{"codeURL": "private-checkout", "createdOrder": map[string]any{"id": 16, "amount": 3.75, "status": "pending"}}})
		default:
			t.Fatal("unexpected route", r.URL.Path)
		}
	}))
	defer server.Close()
	c, _ := New(domain.Profile{Name: "p", PlatformURL: server.URL}, nil)
	defer c.Close()
	c.http.Transport = server.Client().Transport
	q, e := c.QuoteRemote(t.Context(), RemoteOrderIntent{Action: "create", Target: "23", Name: "test", Units: 3})
	if e != nil || q.Total.String() != "3.75" || posts != 0 {
		t.Fatal(q, e)
	}
	r, e := c.PlaceRemoteOrder(t.Context(), q)
	if e != nil || stringValue(r["id"]) != "16" || r["codeURL"] != nil || posts != 1 {
		t.Fatal(r, e)
	}
}
