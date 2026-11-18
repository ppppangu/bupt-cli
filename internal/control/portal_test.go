package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

type testPortal struct {
	corePlatform
	reads, changes int
}

func (f *testPortal) PortalAccount(context.Context) (domain.PortalAccount, error) {
	return domain.PortalAccount{SID: "campus-001"}, nil
}
func (f *testPortal) PortalPage(_ context.Context, a domain.PortalAccount, q domain.PortalInput) (domain.PortalPage, error) {
	f.reads++
	return domain.PortalPage{Account: &a, Section: q.Section, Source: "live", ObservedAt: time.Now().UTC(), Keys: []domain.PortalKey{{ID: "id-1", Name: "work", Status: "active"}}}, nil
}
func (f *testPortal) PortalChange(_ context.Context, m domain.PortalChange) (domain.PortalKey, string, error) {
	f.changes++
	return domain.PortalKey{ID: "id-1", Name: m.Name, Status: "active"}, "one-time-fixture-key", nil
}

func TestPortalKeyCustodyReceiptsCacheAndAuthorization(t *testing.T) {
	c, _ := coreController(t)
	f := &testPortal{}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return f, nil }
	query := params(map[string]any{"section": "key"})
	for _, src := range []string{"live", "cached"} {
		v, e := c.PortalRead(t.Context(), "bupt", query)
		if e != nil || v.Source != src || len(v.Keys) != 1 || v.Keys[0].Stored {
			t.Fatal(v, e)
		}
	}
	if f.reads != 1 {
		t.Fatal("repeated school read")
	}
	op := params(map[string]any{"action": "key.create", "models": []string{"m"}, "name": "work", "idempotency_key": "unique"})
	for range 2 {
		v, e := c.PortalWrite(t.Context(), "bupt", op)
		if e != nil || strings.Contains(string(domain.Result(v)), "one-time-fixture-key") {
			t.Fatal("receipt contained a credential or failed", e)
		}
	}
	if f.changes != 1 {
		t.Fatal("duplicate remote create")
	}
	page, e := c.PortalRead(t.Context(), "bupt", query)
	if e != nil || !page.Keys[0].Stored || page.Source != "live" {
		t.Fatal("stale custody after create", e)
	}
	for _, method := range []string{"portal.change", "portal.export", "compute.admin"} {
		_ = c.Store.Put("clients", "peer", ClientIdentity{ID: "peer", Profiles: []string{"bupt"}})
		if e = c.Authorize("peer", method, "bupt"); e == nil {
			t.Fatal("peer reached protected operation", method)
		}
	}
	exported, e := c.PortalExport(t.Context(), "bupt", "id-1")
	if e != nil || exported.(map[string]string)["secret"] != "one-time-fixture-key" {
		t.Fatal("owner export failed", e)
	}
	op.Set("name", "different")
	if _, e = c.PortalWrite(t.Context(), "bupt", op); e == nil {
		t.Fatal("changed request reused receipt")
	}
	op = params(map[string]any{"action": "key.delete", "target": "id-1"})
	if _, e = c.PortalWrite(t.Context(), "bupt", op); e == nil {
		t.Fatal("delete without confirmation")
	}
	op.Set("confirm", "id-1")
	if _, e = c.PortalWrite(t.Context(), "bupt", op); e != nil {
		t.Fatal(e)
	}
	if _, e = c.PortalExport(t.Context(), "bupt", "id-1"); e == nil {
		t.Fatal("exported deleted secret")
	}
	var audits []domain.AuditEvent
	audits, e = c.Store.ListAudits(time.Time{}, 20)
	b, _ := json.Marshal(audits)
	if e != nil || strings.Contains(string(b), "one-time-fixture-key") {
		t.Fatal("audit leak", e)
	}
}
