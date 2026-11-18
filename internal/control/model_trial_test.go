package control

import (
	"bupt.local/cli/internal/domain"
	"context"
	"strings"
	"testing"
	"time"
)

type trialPortal struct {
	testPortal
	key   domain.PortalKey
	calls int
}

func (b *trialPortal) PortalPage(_ context.Context, a domain.PortalAccount, q domain.PortalInput) (domain.PortalPage, error) {
	p := domain.PortalPage{Section: q.Section, Source: "live", ObservedAt: time.Now(), Account: &a}
	switch q.Section {
	case "model":
		p.Models = []domain.PortalModel{{ID: "model-x", Kind: "chat"}}
	case "key":
		p.Keys = []domain.PortalKey{b.key}
	case "guide":
		p.Data = map[string]any{"base_url": "https://myai.bupt.edu.cn/llm-gw/v1"}
	}
	return p, nil
}
func (b *trialPortal) RunModelTrial(_ context.Context, base string, r domain.TrialRequest, secret string) (domain.TrialResult, error) {
	if base != "https://myai.bupt.edu.cn/llm-gw/v1" || r.Model != "model-x" || secret != "fixture-vault-secret" {
		return domain.TrialResult{}, domain.E("FIXTURE", "wrong decision")
	}
	b.calls++
	return domain.TrialResult{Model: r.Model, Kind: r.Kind, Answer: "mock-output"}, nil
}
func TestTrialChecksOwnerCurrentKeyAndScopeAndDoesNotReplay(t *testing.T) {
	c, _ := coreController(t)
	b := &trialPortal{key: domain.PortalKey{ID: "k1", Status: "active", Models: []string{"model-x"}}}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return b, nil }
	ns := c.portalNamespace("bupt")
	if e := c.Vault.Put(portalCacheKey(ns, "campus-001", "secret/k1"), "fixture-vault-secret"); e != nil {
		t.Fatal(e)
	}
	rpc := domain.RPCRequest{Profile: "bupt", Method: "portal.trial"}
	p := params(map[string]any{"model": "model-x", "key_id": "k1", "idempotency_key": "attempt-1", "input": domain.TrialInput{Texts: []string{"my original text"}}})
	p.Set("dry_run", true)
	if _, e := c.Handle(t.Context(), rpc, p); e != nil || b.calls != 0 {
		t.Fatal("dry run submitted", e)
	}
	delete(p, "dry_run")
	if e := c.Store.Put("clients", "peer", ClientIdentity{ID: "peer", Profiles: []string{"bupt"}}); e != nil {
		t.Fatal(e)
	}
	rpc.ClientID = "peer"
	if _, e := c.Handle(t.Context(), rpc, p); managedCode(e) != "OWNER_REQUIRED" || b.calls != 0 {
		t.Fatal("non-owner spent school credit", e)
	}
	rpc.ClientID = ""
	if _, e := c.Handle(t.Context(), rpc, p); e != nil || b.calls != 1 {
		t.Fatal("send failed", e)
	}
	if _, e := c.Handle(t.Context(), rpc, p); managedCode(e) != "TRIAL_ALREADY_SUBMITTED" || b.calls != 1 {
		t.Fatal("replayed", e)
	}
	p.Set("input", domain.TrialInput{Texts: []string{"different"}})
	if _, e := c.Handle(t.Context(), rpc, p); managedCode(e) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(e)
	}
	p.Set("idempotency_key", "attempt-2")
	b.key.Status = "disabled"
	if _, e := c.Handle(t.Context(), rpc, p); managedCode(e) != "MODEL_KEY_SCOPE" {
		t.Fatal(e)
	}
	b.key.Status = "active"
	b.key.Models = []string{"other"}
	if _, e := c.Handle(t.Context(), rpc, p); managedCode(e) != "MODEL_KEY_SCOPE" {
		t.Fatal(e)
	}
	b.key.Models = []string{"model-x"}
	_ = c.Vault.Delete(portalCacheKey(ns, "campus-001", "secret/k1"))
	if _, e := c.Handle(t.Context(), rpc, p); managedCode(e) != "KEY_NOT_STORED" || b.calls != 1 {
		t.Fatal(e, b.calls)
	}
	for _, v := range c.Vault.(*coreVault).values {
		if strings.Contains(string(v), "my original text") || strings.Contains(string(v), "mock-output") {
			t.Fatal("trial transcript persisted")
		}
	}
}
