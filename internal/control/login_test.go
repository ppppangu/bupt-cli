package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
)

type flowPlatform struct {
	corePlatform
	gatewayReady, ssoReady   bool
	passwordCalls, ssoStarts int
	rejectIdentity           bool
}

func (a *flowPlatform) AuthStatus(context.Context) domain.AuthStatus {
	g := "expired"
	p := "expired"
	if a.gatewayReady {
		g = "authenticated"
	}
	if a.ssoReady {
		p = "authenticated"
	}
	return domain.AuthStatus{Gateway: g, Platform: p}
}
func (a *flowPlatform) GatewayStart(context.Context) (domain.AuthChallenge, error) {
	return domain.AuthChallenge{Kind: "webvpn_qr", URL: "https://webvpn.bupt.edu.cn/fixture", State: "waiting"}, nil
}
func (a *flowPlatform) GatewayPoll(context.Context) (domain.AuthChallenge, error) {
	a.gatewayReady = true
	return domain.AuthChallenge{Kind: "webvpn_qr", State: "authenticated"}, nil
}
func (a *flowPlatform) SSOStart(context.Context) (domain.AuthChallenge, error) {
	a.ssoStarts++
	return domain.AuthChallenge{Kind: "bupt_sso_qr", URL: "https://app.bupt.edu.cn/fixture", State: "waiting"}, nil
}
func (a *flowPlatform) SSOPoll(context.Context) (domain.AuthChallenge, error) {
	a.ssoReady = true
	return domain.AuthChallenge{Kind: "bupt_sso_qr", State: "authenticated"}, nil
}
func (a *flowPlatform) WhoAmI(ctx context.Context) (domain.Identity, error) {
	if a.rejectIdentity {
		return domain.Identity{}, domain.E("IDENTITY_MISMATCH", "fixture mismatch")
	}
	return a.corePlatform.WhoAmI(ctx)
}
func (a *flowPlatform) Login(context.Context, string, string) (domain.Identity, error) {
	a.passwordCalls++
	return domain.Identity{}, domain.E("TEST", "default flow must not use a password")
}
func TestLoginFlowSeparatesGatewayAndSSOAndVerifiesIdentity(t *testing.T) {
	c, _ := coreController(t)
	provider := &flowPlatform{}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return provider, nil }
	first, e := c.StartLogin(context.Background(), "bupt", Params{})
	if e != nil {
		t.Fatal(e)
	}
	if first.Stage != "webvpn" || first.RequiredAction != "scan_and_confirm" {
		t.Fatalf("first QR missing gateway stage: %+v", first)
	}
	second, e := c.PollLogin(context.Background(), "bupt")
	if e != nil {
		t.Fatal(e)
	}
	if second.Stage != "university_sso" || second.URL == first.URL || second.State == "authenticated" {
		t.Fatalf("gateway was mistaken for platform login: %+v", second)
	}
	final, e := c.PollLogin(context.Background(), "bupt")
	if e != nil {
		t.Fatal(e)
	}
	if final.Stage != "complete" || final.State != "authenticated" || provider.passwordCalls != 0 {
		t.Fatalf("wrong terminal auth flow: %+v", final)
	}
	if _, e = c.StartLogin(context.Background(), "bupt", Params{}); e != nil {
		t.Fatal(e)
	}
	if provider.ssoStarts != 1 {
		t.Fatal("authenticated session prompted again")
	}
}
func TestLoginFlowResumesFromDurableStageWithoutPassword(t *testing.T) {
	c, _ := coreController(t)
	provider := &flowPlatform{gatewayReady: true}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return provider, nil }
	first, e := c.StartLogin(context.Background(), "bupt", Params{})
	if e != nil || first.Stage != "university_sso" {
		t.Fatalf("valid gateway was not reused: %+v %v", first, e)
	}
	resumed, e := New(Options{Config: c.Configuration(), Store: c.Store, Vault: c.Vault, Factory: func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return provider, nil }})
	if e != nil {
		t.Fatal(e)
	}
	defer resumed.Close()
	provider.rejectIdentity = true
	if _, e = resumed.PollLogin(context.Background(), "bupt"); e == nil {
		t.Fatal("provider signal bypassed platform identity verification")
	}
}

type campusFlowPlatform struct {
	flowPlatform
	submissions int
}

func (a *campusFlowPlatform) BeginSSOPassword(context.Context) (*platform.CASPasswordForm, error) {
	if !a.gatewayReady {
		return nil, domain.E("TEST", "CAS was contacted before WebVPN approval")
	}
	return &platform.CASPasswordForm{Execution: "private-cas-form"}, nil
}
func (a *campusFlowPlatform) SubmitSSOPassword(_ context.Context, form *platform.CASPasswordForm, username, password, captcha string) error {
	if form.Execution != "private-cas-form" || username != "campus" || password != "campus-secret" || captcha != "" {
		return domain.E("TEST", "incorrect credential routing")
	}
	a.submissions++
	a.ssoReady = true
	return nil
}

func TestCampusPasswordFlowSurvivesGatewayAndRestartAndConsumesOnce(t *testing.T) {
	c, _ := coreController(t)
	provider := &campusFlowPlatform{}
	factory := func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return provider, nil }
	c.opts.Factory = factory
	ch, e := c.StartLogin(t.Context(), "bupt", params(map[string]string{"sso_method": "password"}))
	if e != nil || ch.Stage != "webvpn" {
		t.Fatal(ch.Stage, e)
	}
	submit := params(map[string]string{"challenge_id": "stale", "username": "campus", "password": "campus-secret"})
	if _, e = c.SubmitLoginPassword(t.Context(), "bupt", submit); e == nil {
		t.Fatal("submitted before gateway approval")
	}
	resumed, e := New(Options{Config: c.Configuration(), Store: c.Store, Vault: c.Vault, Factory: factory})
	if e != nil {
		t.Fatal(e)
	}
	defer resumed.Close()
	ch, e = resumed.PollLogin(t.Context(), "bupt")
	if e != nil || ch.Kind != domain.CampusPasswordKind || ch.RequiredAction != domain.EnterCampusPassword || ch.Stage != "university_sso" || ch.URL != "" || ch.ID == "" || strings.Contains(string(domain.Result(ch)), "private-cas-form") {
		t.Fatal("incorrect public password challenge", e)
	}
	pending, e := resumed.PollLogin(t.Context(), "bupt")
	if e != nil || pending.ID != ch.ID {
		t.Fatal("poll replaced the open form", e)
	}
	if _, e = resumed.SubmitLoginPassword(t.Context(), "bupt", submit); e == nil {
		t.Fatal("unrelated form accepted")
	}
	submit.Set("challenge_id", ch.ID)
	replies := make(chan error, 2)
	for range 2 {
		go func() {
			v, err := resumed.Handle(t.Context(), domain.RPCRequest{Profile: "bupt", Method: "auth.sso.submit"}, submit)
			if err == nil && v.(domain.AuthChallenge).Stage != "complete" {
				err = errors.New("submission omitted platform verification")
			}
			replies <- err
		}()
	}
	var completed, stale int
	for range 2 {
		if e := <-replies; e == nil {
			completed++
		} else {
			var de *domain.Error
			if errors.As(e, &de) && de.Code == "SSO_CHALLENGE_EXPIRED" {
				stale++
			} else {
				t.Fatal(e)
			}
		}
	}
	if completed != 1 || stale != 1 || provider.submissions != 1 || provider.passwordCalls != 0 || provider.ssoStarts != 0 {
		t.Fatal("credential routing or single-use failure")
	}
	var flow loginFlow
	if err := resumed.Vault.Get(loginFlowKey("bupt"), &flow); err != nil || flow.Stage != "complete" || flow.PasswordForm != nil {
		t.Fatal("form was not retired", err)
	}
	for _, raw := range c.Vault.(*coreVault).values {
		if strings.Contains(string(raw), "campus-secret") {
			t.Fatal("campus password was saved")
		}
	}
	ch, e = resumed.StartLogin(t.Context(), "bupt", params(map[string]string{"sso_method": "password"}))
	if e != nil || ch.Stage != "complete" || ch.ID != "" {
		t.Fatal("valid session asked for credentials", e)
	}
}

func TestCampusPasswordExpiryAndOwnerRestriction(t *testing.T) {
	c, _ := coreController(t)
	provider := &campusFlowPlatform{flowPlatform: flowPlatform{gatewayReady: true}}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return provider, nil }
	ch, e := c.StartLogin(t.Context(), "bupt", params(map[string]string{"sso_method": "password"}))
	if e != nil || ch.ID == "" {
		t.Fatal("missing password challenge", e)
	}
	if e = c.Store.Put("clients", "peer", ClientIdentity{ID: "peer", Profiles: []string{"bupt"}}); e != nil {
		t.Fatal(e)
	}
	submit := params(map[string]string{"challenge_id": ch.ID, "username": "campus", "password": "campus-secret"})
	_, e = c.Handle(t.Context(), domain.RPCRequest{Profile: "bupt", Method: "auth.sso.submit", ClientID: "peer"}, submit)
	var de *domain.Error
	if !errors.As(e, &de) || de.Code != "OWNER_REQUIRED" {
		t.Fatal("non-owner submitted a password", e)
	}
	var flow loginFlow
	if err := c.Vault.Get(loginFlowKey("bupt"), &flow); err != nil {
		t.Fatal(err)
	}
	flow.ExpiresAt = time.Now().Add(-time.Second)
	if err := c.saveLoginFlow("bupt", flow.Stage, flow); err != nil {
		t.Fatal(err)
	}
	ch, e = c.PollLogin(t.Context(), "bupt")
	if e != nil || ch.State != "expired" {
		t.Fatal("expired form still pending", e)
	}
	if _, e = c.SubmitLoginPassword(t.Context(), "bupt", submit); !errors.As(e, &de) || de.Code != "SSO_CHALLENGE_EXPIRED" || provider.submissions != 0 {
		t.Fatal("expired form submitted", e)
	}
}

type loginPortalFixture struct {
	flowPlatform
	portal    *flowPlatform
	exchanges int
}

func (f *loginPortalFixture) PortalLogin() (domain.PlatformAPI, error) { return f.portal, nil }
func (f *loginPortalFixture) PrepareCompute(context.Context) error {
	f.exchanges++
	f.ssoReady = true
	return nil
}
func TestUnifiedLoginVerifiesPortalThenPendingCompute(t *testing.T) {
	c, _ := coreController(t)
	portal := &flowPlatform{gatewayReady: true}
	compute := &loginPortalFixture{portal: portal}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return compute, nil }
	if e := c.Vault.Put("platform/bupt/pending-service", "compute"); e != nil {
		t.Fatal(e)
	}
	ch, e := c.StartLogin(t.Context(), "bupt", Params{})
	if e != nil || ch.Stage != "university_sso" || portal.ssoStarts != 1 || compute.ssoStarts != 0 {
		t.Fatal(ch.Stage, e)
	}
	ch, e = c.PollLogin(t.Context(), "bupt")
	if e != nil || ch.Stage != "complete" || compute.exchanges != 1 {
		t.Fatal(ch.Stage, e)
	}
	if _, e = c.StartLogin(t.Context(), "bupt", Params{}); e != nil || compute.exchanges != 1 || portal.ssoStarts != 1 {
		t.Fatal("already verified layers were restarted", e)
	}
}
