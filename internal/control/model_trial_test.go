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
	key       domain.PortalKey
	calls     int
	lostReply bool
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
	if b.lostReply {
		return domain.TrialResult{}, domain.E("MODEL_RESULT_UNKNOWN", "fixture consumed the request and dropped the response")
	}
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

func TestModelUnknownEffectRequiresReconciliationAcrossControllerRestart(t *testing.T) {
	c, _ := coreController(t)
	p := &trialPortal{key: domain.PortalKey{ID: "k1", Status: "active", Models: []string{"model-x"}}, lostReply: true}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return p, nil }
	ns := c.portalNamespace("bupt")
	if err := c.Vault.Put(portalCacheKey(ns, "campus-001", "secret/k1"), "fixture-vault-secret"); err != nil {
		t.Fatal(err)
	}
	request := domain.ModelInvocation{Model: "model-x", KeyID: "k1", Input: domain.TrialInput{Texts: []string{"charged once"}}, IdempotencyKey: "first"}
	rpc := domain.RPCRequest{Profile: "bupt", Method: "portal.trial"}
	run := func(c *Controller) (any, error) {
		p, err := Decode(domain.Result(request))
		if err != nil {
			t.Fatal(err)
		}
		return c.Handle(t.Context(), rpc, p)
	}
	if _, err := run(c); managedCode(err) != "MODEL_RESULT_UNKNOWN" || p.calls != 1 {
		t.Fatal(err, p.calls)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(c.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	request.IdempotencyKey = "second"
	if _, err := run(next); managedCode(err) != "MODEL_RECONCILIATION_REQUIRED" || p.calls != 1 {
		t.Fatal(err, p.calls)
	}
	p.lostReply = false
	request.ResendAfter = "first"
	out, err := run(next)
	if err != nil || out.(domain.TrialResult).Answer != "mock-output" || p.calls != 2 {
		t.Fatal(out, err, p.calls)
	}
	if _, err := run(next); managedCode(err) != "TRIAL_ALREADY_SUBMITTED" || p.calls != 2 {
		t.Fatal(err, p.calls)
	}
	request.IdempotencyKey, request.ResendAfter = "third", ""
	if _, err := run(next); err != nil || p.calls != 3 {
		t.Fatal(err, p.calls)
	}
}
