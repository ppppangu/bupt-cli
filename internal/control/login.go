package control

import (
	"context"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"github.com/google/uuid"
)

type loginFlow struct {
	account      accountTicket             // bound before opening the configured login adapter; never persisted
	NextService  string                    `json:"next_service,omitempty"`
	Service      string                    `json:"service,omitempty"`
	SSOMethod    string                    `json:"sso_method,omitempty"`
	ChallengeID  string                    `json:"challenge_id,omitempty"`
	ExpiresAt    time.Time                 `json:"expires_at,omitzero"`
	PasswordForm *platform.CASPasswordForm `json:"password_form,omitempty"`
	Stage        string                    `json:"stage"`
	StartedAt    time.Time                 `json:"started_at"`
	UpdatedAt    time.Time                 `json:"updated_at"`
}
type ssoProvider interface {
	SSOStart(context.Context) (domain.AuthChallenge, error)
	SSOPoll(context.Context) (domain.AuthChallenge, error)
}

type passwordSSOProvider interface {
	BeginSSOPassword(context.Context) (*platform.CASPasswordForm, error)
	SubmitSSOPassword(context.Context, *platform.CASPasswordForm, string, string, string) error
}

// Serialize transitions for a single profile, including consuming a form before
// submitting it. A dropped RPC or a second terminal cannot replay a password.
func (c *Controller) lockLogin(profile string) func() {
	value, _ := c.loginLocks.LoadOrStore(profile, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func loginFlowKey(profile string) string { return "login-flow/" + profile }

func (c *Controller) saveLoginFlow(profile, stage string, flow loginFlow) error {
	flow.Stage = stage
	flow.UpdatedAt = time.Now().UTC()
	return c.Vault.Put(loginFlowKey(profile), flow)
}

func (f loginFlow) passwordChallenge() domain.AuthChallenge {
	ch := domain.AuthChallenge{Kind: domain.CampusPasswordKind, Stage: "university_sso", State: "input_required", RequiredAction: domain.EnterCampusPassword, ID: f.ChallengeID, ExpiresAt: f.ExpiresAt}
	if !f.ExpiresAt.After(time.Now()) || f.PasswordForm == nil {
		ch.State, ch.RequiredAction = "expired", "restart_login"
	} else {
		ch.CaptchaImage = f.PasswordForm.CaptchaImage
	}
	return ch
}

func annotateChallenge(v domain.AuthChallenge, stage string) domain.AuthChallenge {
	v.Stage = stage
	if v.PollAfterMS <= 0 {
		v.PollAfterMS = 1000
	}
	if v.RequiredAction != "" {
		return v
	}
	switch v.State {
	case "waiting", "pending":
		v.RequiredAction = "scan_and_confirm"
	case "confirmation_required":
		v.RequiredAction = "confirm_takeover"
	case "expired":
		v.RequiredAction = "restart_login"
	case "authenticated", "complete":
		v.RequiredAction = "none"
	}
	return v
}

// StartLogin and PollLogin own all transitions. Frontends render challenges and
// return human input; neither a CLI nor a future TUI implements provider logic.
func (c *Controller) StartLogin(ctx context.Context, profile string, p Params) (domain.AuthChallenge, error) {
	if c.Vault == nil {
		return domain.AuthChallenge{}, domain.E("SECRET_STORE", "authentication requires protected session storage")
	}
	method := p.String("sso_method")
	if method != "" && method != "qr" && method != "password" {
		return domain.AuthChallenge{}, domain.E("INVALID_SSO_METHOD", "sso_method must be qr or password")
	}
	defer c.lockLogin(profile)()
	flow := loginFlow{Service: "portal", SSOMethod: method, StartedAt: time.Now().UTC()}
	var pending string
	if c.Vault.Get("platform/"+profile+"/pending-service", &pending) == nil && pending == "compute" {
		flow.NextService = "compute"
	}
	flow.account = c.accountTicket(profile)
	a, e := c.loginAdapter(profile, flow.Service)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	status := a.AuthStatus(ctx)
	if status.Gateway == "expired" {
		flow.account.observe(domain.AccountObservation{VPN: "expired"}, true)
	} else if status.Gateway == "authenticated" && status.Platform == "expired" && flow.Service == "portal" {
		flow.account.observe(domain.AccountObservation{VPN: "authenticated", Platform: "expired"}, true)
	}
	if status.RequiresAction == "IDENTITY_MISMATCH" {
		return domain.AuthChallenge{}, domain.E("IDENTITY_MISMATCH", "platform user differs from expected_uid")
	}
	if status.Gateway == "unknown" && status.RequiresAction != "" {
		return domain.AuthChallenge{}, domain.E(status.RequiresAction, "当前无法核验学校会话，请稍后重试。")
	}
	gateway := c.Configuration().Profiles[profile].GatewayURL != ""
	if gateway && (p.Bool("force_gateway") || status.Gateway != "authenticated") {
		if e = c.saveLoginFlow(profile, "webvpn", flow); e != nil {
			return domain.AuthChallenge{}, e
		}
		challenge, e := a.GatewayStart(ctx)
		if e != nil {
			return domain.AuthChallenge{}, e
		}
		if challenge.State == "authenticated" {
			return c.startSSO(ctx, profile, a, flow)
		}
		return annotateChallenge(challenge, "webvpn"), nil
	}
	if status.Platform == "authenticated" {
		if err := c.recordPortalLogin(profile, a, flow.account); err != nil {
			return domain.AuthChallenge{}, err
		}
		if flow.NextService == "compute" {
			return c.finishComputeLogin(ctx, profile, flow)
		}
		_ = c.Vault.Delete("platform/" + profile + "/pending-service")
		if e := c.saveLoginFlow(profile, "complete", flow); e != nil {
			return domain.AuthChallenge{}, e
		}
		flow.account.loginCompleted()
		return annotateChallenge(domain.AuthChallenge{Kind: "platform", State: "authenticated"}, "complete"), nil
	}
	return c.startSSO(ctx, profile, a, flow)
}
func (c *Controller) startSSO(ctx context.Context, profile string, a domain.PlatformAPI, flow loginFlow) (domain.AuthChallenge, error) {
	if c.Configuration().Profiles[profile].GatewayURL != "" {
		flow.account.observe(domain.AccountObservation{VPN: "authenticated"}, true)
	}
	// Invalidate an earlier form before CAS can change its backing session.
	if e := c.saveLoginFlow(profile, "university_sso", flow); e != nil {
		return domain.AuthChallenge{}, e
	}
	if flow.SSOMethod == "password" {
		provider, ok := a.(passwordSSOProvider)
		if !ok {
			return domain.AuthChallenge{}, domain.E("SSO_UNSUPPORTED", "controller does not support university password login")
		}
		form, err := provider.BeginSSOPassword(ctx)
		if err != nil {
			return domain.AuthChallenge{}, err
		}
		if form == nil {
			return c.completeLogin(ctx, profile, a, flow)
		}
		flow.PasswordForm = form
		flow.ChallengeID = uuid.NewString()
		flow.ExpiresAt = time.Now().Add(5 * time.Minute)
		if err := c.saveLoginFlow(profile, "university_sso", flow); err != nil {
			return domain.AuthChallenge{}, err
		}
		return flow.passwordChallenge(), nil
	}
	provider, ok := a.(ssoProvider)
	if !ok {
		return domain.AuthChallenge{}, domain.E("SSO_UNSUPPORTED", "platform adapter does not support unified identity login")
	}
	challenge, e := provider.SSOStart(ctx)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	if challenge.State == "authenticated" || challenge.State == "complete" {
		return c.completeLogin(ctx, profile, a, flow)
	}
	return annotateChallenge(challenge, "university_sso"), nil
}
func (c *Controller) completeLogin(ctx context.Context, profile string, a domain.PlatformAPI, flow loginFlow) (domain.AuthChallenge, error) {
	if _, e := a.WhoAmI(ctx); e != nil {
		return domain.AuthChallenge{}, e
	}
	if err := c.recordPortalLogin(profile, a, flow.account); err != nil {
		return domain.AuthChallenge{}, err
	}
	if flow.NextService == "compute" {
		return c.finishComputeLogin(ctx, profile, flow)
	}
	if e := c.saveLoginFlow(profile, "complete", loginFlow{Service: flow.Service, SSOMethod: flow.SSOMethod, StartedAt: flow.StartedAt}); e != nil {
		return domain.AuthChallenge{}, e
	}
	flow.account.loginCompleted()
	_ = c.Vault.Delete("platform/" + profile + "/pending-service")
	c.Audit("auth.completed", profile, "", "", "", "university login and platform identity verified")
	return annotateChallenge(domain.AuthChallenge{Kind: "platform", State: "authenticated", Message: "platform identity verified"}, "complete"), nil
}
func (c *Controller) PollLogin(ctx context.Context, profile string) (domain.AuthChallenge, error) {
	if c.Vault == nil {
		return domain.AuthChallenge{}, domain.E("SECRET_STORE", "authentication requires protected session storage")
	}
	defer c.lockLogin(profile)()
	var flow loginFlow
	if e := c.Vault.Get(loginFlowKey(profile), &flow); e != nil {
		return domain.AuthChallenge{}, domain.E("NO_LOGIN_FLOW", "start with bupt auth login")
	}
	flow.account = c.accountTicket(profile)
	a, e := c.loginAdapter(profile, flow.Service)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	switch flow.Stage {
	case "webvpn":
		challenge, e := a.GatewayPoll(ctx)
		if e != nil {
			return domain.AuthChallenge{}, e
		}
		if challenge.State == "authenticated" || challenge.State == "complete" {
			return c.startSSO(ctx, profile, a, flow)
		}
		return annotateChallenge(challenge, "webvpn"), nil
	case "university_sso":
		if flow.SSOMethod == "password" {
			return flow.passwordChallenge(), nil
		}
		provider, ok := a.(ssoProvider)
		if !ok {
			return domain.AuthChallenge{}, domain.E("SSO_UNSUPPORTED", "platform adapter does not support unified identity login")
		}
		challenge, e := provider.SSOPoll(ctx)
		if e != nil {
			return domain.AuthChallenge{}, e
		}
		if challenge.State == "authenticated" || challenge.State == "complete" {
			return c.completeLogin(ctx, profile, a, flow)
		}
		return annotateChallenge(challenge, "university_sso"), nil
	case "password_submitted", "complete":
		return c.completeLogin(ctx, profile, a, flow)
	default:
		return domain.AuthChallenge{}, domain.E("LOGIN_STATE", "unknown persisted login stage")
	}
}

func (c *Controller) SubmitLoginPassword(ctx context.Context, profile string, p Params) (domain.AuthChallenge, error) {
	if c.Vault == nil {
		return domain.AuthChallenge{}, domain.E("SECRET_STORE", "authentication requires protected session storage")
	}
	defer c.lockLogin(profile)()
	var flow loginFlow
	if err := c.Vault.Get(loginFlowKey(profile), &flow); err != nil || flow.Stage != "university_sso" || flow.SSOMethod != "password" || flow.PasswordForm == nil || flow.ChallengeID == "" || flow.ChallengeID != p.String("challenge_id") || !flow.ExpiresAt.After(time.Now()) {
		return domain.AuthChallenge{}, domain.E("SSO_CHALLENGE_EXPIRED", "本次密码表单已过期或已提交。请重新运行 bupt login --sso-password。")
	}
	username, password, captcha := strings.TrimSpace(p.String("username")), p.String("password"), strings.TrimSpace(p.String("captcha_code"))
	if username == "" || password == "" {
		return domain.AuthChallenge{}, domain.E("CREDENTIALS_REQUIRED", "请提供统一身份认证账号和密码。")
	}
	form := flow.PasswordForm
	if form.CaptchaID != "" && captcha == "" {
		return domain.AuthChallenge{}, domain.E("SSO_CAPTCHA_REQUIRED", "请填写统一认证图形验证码。")
	}
	flow.account = c.accountTicket(profile)
	api, err := c.loginAdapter(profile, flow.Service)
	if err != nil {
		return domain.AuthChallenge{}, err
	}
	provider, ok := api.(passwordSSOProvider)
	if !ok {
		return domain.AuthChallenge{}, domain.E("SSO_UNSUPPORTED", "controller does not support university password login")
	}
	flow.PasswordForm, flow.ChallengeID, flow.ExpiresAt = nil, "", time.Time{}
	if err := c.saveLoginFlow(profile, "password_submitted", flow); err != nil {
		return domain.AuthChallenge{}, err
	}
	if err := provider.SubmitSSOPassword(ctx, form, username, password, captcha); err != nil {
		return domain.AuthChallenge{}, err
	}
	return c.completeLogin(ctx, profile, api, flow)
}
func (c *Controller) loginAdapter(profile, service string) (domain.PlatformAPI, error) {
	a, e := c.api(profile)
	if e != nil {
		return nil, e
	}
	if service == "portal" {
		if base, ok := a.(interface {
			PortalLogin() (domain.PlatformAPI, error)
		}); ok {
			peer, err := base.PortalLogin()
			if err == nil {
				return peer, nil
			}
			if managedCode(err) != "PORTAL_NOT_CONFIGURED" {
				return nil, err
			}
		}
	}
	return a, nil
}

func (c *Controller) recordPortalLogin(profile string, a domain.PlatformAPI, ticket accountTicket) error {
	peer, ok := a.(interface {
		VerifiedPortalAccount() (domain.PortalAccount, bool)
	})
	if !ok {
		return nil
	}
	account, ok := peer.VerifiedPortalAccount()
	if !ok {
		return nil
	}
	ns := c.portalNamespace(profile)
	i, e := c.portalIndex(ns)
	if e != nil {
		return e
	}
	if i.SID != account.SID {
		i = portalIndex{SID: account.SID}
	}
	i.Summary.State = "authenticated"
	i.Account = &account
	i.Summary.Balance = account.Balance
	i.Summary.CheckedAt = time.Now().UTC()
	i.ChangedAt = i.Summary.CheckedAt
	err := c.Vault.Put(ns+"index", i)
	if err == nil {
		ticket.observePortal(account, true)
	}
	return err
}
func (c *Controller) finishComputeLogin(ctx context.Context, profile string, flow loginFlow) (domain.AuthChallenge, error) {
	flow.Service = "compute"
	flow.NextService = ""
	a, e := c.api(profile)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	if p, ok := a.(interface{ PrepareCompute(context.Context) error }); ok {
		if e = p.PrepareCompute(ctx); e == nil {
			return c.completeLogin(ctx, profile, a, flow)
		} else if managedCode(e) != "AUTH_REQUIRED" {
			return domain.AuthChallenge{}, e
		}
	}
	return c.startSSO(ctx, profile, a, flow)
}
