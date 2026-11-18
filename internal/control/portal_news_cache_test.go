package control

import (
	"bupt.local/cli/internal/domain"
	"context"
	"testing"
	"time"
)

func TestNewsDefaultSharesLegacyCursorCache(t *testing.T) {
	c, _ := coreController(t)
	ns := c.portalNamespace("bupt")
	if e := c.Vault.Put(ns+"index", portalIndex{SID: "fixture"}); e != nil {
		t.Fatal(e)
	}
	for _, cursor := range []string{"", "next"} {
		old := domain.PortalInput{Section: "news", Cursor: cursor}
		doc := domain.PortalPage{Section: "news", Data: map[string]any{"items": []any{}, "test_cursor": cursor}}
		if e := c.Vault.Put(portalCacheKey(ns, "fixture", old), doc); e != nil {
			t.Fatal(e)
		}
		for _, limit := range []int{0, 20} {
			r, e := c.PortalRead(t.Context(), "bupt", params(map[string]any{"section": "news", "cursor": cursor, "cached": true, "limit": limit}))
			if e != nil || r.Source != "cached" || r.Data["test_cursor"] != cursor {
				t.Fatal(r, e)
			}
		}
	}
}

type newsFixture struct {
	testPortal
	malformed bool
}

func (f *newsFixture) PortalPage(ctx context.Context, a domain.PortalAccount, q domain.PortalInput) (domain.PortalPage, error) {
	data := map[string]any{"items": []map[string]string{{"id": "saved", "title": "原记录"}}}
	if f.malformed {
		data = map[string]any{"unexpected": []any{}}
	}
	return domain.PortalPage{Section: q.Section, Source: "live", ObservedAt: time.Now().UTC(), Data: data}, nil
}
func TestMalformedPortalNewsPreservesValidSnapshot(t *testing.T) {
	c, _ := coreController(t)
	f := &newsFixture{}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return f, nil }
	q := params(map[string]any{"section": "news", "refresh": true})
	if _, e := c.PortalRead(t.Context(), "bupt", q); e != nil {
		t.Fatal(e)
	}
	f.malformed = true
	if _, e := c.PortalRead(t.Context(), "bupt", q); managedCode(e) != "PORTAL_PROTOCOL" {
		t.Fatal(e)
	}
	delete(q, "refresh")
	q.Set("cached", true)
	page, e := c.PortalRead(t.Context(), "bupt", q)
	if e != nil || page.Data["items"] == nil || page.Source != "cached" {
		t.Fatal(e, page)
	}
}
