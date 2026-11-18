package platform

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"golang.org/x/crypto/pbkdf2"
)

// encodePassword reproduces the existing browser login codec; it is not a
// storage encryption scheme. The credential vault provides storage encryption.
func encodePassword(password string, random io.Reader) (string, error) {
	salt := make([]byte, 32)
	if _, e := io.ReadFull(random, salt); e != nil {
		return "", e
	}
	saltHex := hex.EncodeToString(salt)
	key := pbkdf2.Key([]byte("fe838583"), []byte(saltHex), 100, 16, sha256.New)
	block, e := aes.NewCipher(key)
	if e != nil {
		return "", e
	}
	b := []byte(password)
	pad := aes.BlockSize - len(b)%aes.BlockSize
	for range pad {
		b = append(b, byte(pad))
	}
	cipher.NewCBCEncrypter(block, key).CryptBlocks(b, b)
	return base64.StdEncoding.EncodeToString(b) + saltHex, nil
}

func (c *Client) WhoAmI(ctx context.Context) (domain.Identity, error) {
	if c.parent != nil {
		a, e := c.readPortalAccount(ctx)
		if e != nil {
			return domain.Identity{}, e
		}
		return domain.Identity{UID: domain.ID(a.SID), CampusID: a.SID, Username: a.SID, DisplayName: a.Name}, nil
	}
	var m map[string]any
	if e := c.jsonRequest(ctx, http.MethodGet, "/auth/user", nil, &m); e != nil {
		return domain.Identity{}, e
	}
	// /auth/user is an identity document, not the retCode mutation envelope.
	// This deployment returns code=1 with a valid UID after successful CAS.
	// Both accepted values still require the numeric UID and expected_uid checks.
	if n := integer(m["code"]); n != 0 && n != 1 {
		return domain.Identity{}, authError(n)
	}
	if n := integer(m["retCode"]); n != 0 {
		return domain.Identity{}, authError(n)
	}
	if _, ok := m["uid"]; !ok {
		if inner := object(m["data"]); inner != nil {
			m = inner
		}
	}
	i := domain.Identity{CampusID: stringValue(m["identity"]), UID: id(m["uid"]), EID: id(m["eid"]), Username: stringValue(m["username"]), DisplayName: stringValue(m["displayName"]), Email: stringValue(m["email"])}
	if i.EID == "0" {
		i.EID = ""
	}
	if i.Username == "" {
		i.Username = stringValue(m["uname"])
	}
	if i.Username == "" {
		i.Username = c.profile.Username
	}
	uid, uidErr := strconv.ParseUint(string(i.UID), 10, 64)
	if uidErr != nil || uid == 0 {
		return domain.Identity{}, domain.E("AUTH_EXPIRED", "no authenticated platform identity")
	}
	if c.profile.ExpectedUID != "" && c.profile.ExpectedUID != i.UID {
		return domain.Identity{}, domain.E("IDENTITY_MISMATCH", "platform user differs from expected_uid")
	}
	if i.CampusID != "" && c.vault != nil {
		var pinned string
		if e := c.vault.Get(c.key("portal/subject"), &pinned); e == nil && pinned != "" && campusSubject(pinned) != campusSubject(i.CampusID) {
			return domain.Identity{}, domain.E("IDENTITY_MISMATCH", "算力服务与门户绑定的校园身份不一致。")
		}
	}
	i = c.cachedIdentityContact(i)
	c.mu.Lock()
	var stale *eventSocket
	if c.identity.UID != "" && c.identity.UID != i.UID {
		stale = c.socket
		c.socket = nil
	}
	c.identity = i
	c.mu.Unlock()
	if stale != nil {
		stale.close()
	}
	return i, nil
}

type LoginOptions struct {
	Username    string
	Password    string
	Token       string
	CaptchaUUID string
	CaptchaCode string
}
type Captcha struct {
	UUID  string `json:"uuid"`
	Image string `json:"image"`
}

func (c *Client) Captcha(ctx context.Context) (Captcha, error) {
	var m map[string]any
	if e := c.jsonRequest(ctx, "GET", "/auth/captcha", nil, &m); e != nil {
		return Captcha{}, e
	}
	if n := integer(m["retCode"]); n != 0 {
		return Captcha{}, authError(n)
	}
	if inner := object(m["data"]); inner != nil {
		m = inner
	}
	result := Captcha{UUID: stringValue(m["captchaUuid"]), Image: stringValue(m["captchaImage"])}
	if result.UUID == "" || result.Image == "" {
		return result, domain.E("PROTOCOL", "captcha response omitted its challenge or image")
	}
	return result, nil
}

func (c *Client) LoginSaved(ctx context.Context, challenge ...LoginOptions) (domain.Identity, error) {
	if c.vault == nil {
		return domain.Identity{}, domain.E("CREDENTIALS_NOT_FOUND", "no credential store is configured")
	}
	var saved struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if e := c.vault.Get(c.key("credentials"), &saved); e != nil {
		return domain.Identity{}, domain.E("CREDENTIALS_NOT_FOUND", "saved platform credentials are unavailable")
	}
	options := LoginOptions{Username: saved.Username, Password: saved.Password}
	if len(challenge) > 0 {
		options.Token = challenge[0].Token
		options.CaptchaUUID = challenge[0].CaptchaUUID
		options.CaptchaCode = challenge[0].CaptchaCode
	}
	return c.LoginWithOptions(ctx, options)
}

func (c *Client) Login(ctx context.Context, username, password string) (domain.Identity, error) {
	return c.LoginWithOptions(ctx, LoginOptions{Username: username, Password: password})
}
func (c *Client) LoginWithOptions(ctx context.Context, options LoginOptions) (domain.Identity, error) {
	username, password := options.Username, options.Password
	var enc string
	var e error
	if password != "" {
		enc, e = encodePassword(password, rand.Reader)
		if e != nil {
			return domain.Identity{}, e
		}
	}
	var m map[string]any
	payload := map[string]any{"username": username, "password": enc}
	if options.Token != "" {
		payload["token"] = options.Token
	}
	if options.CaptchaUUID != "" {
		payload["captchaUuid"] = options.CaptchaUUID
	}
	if options.CaptchaCode != "" {
		payload["captchaCode"] = options.CaptchaCode
	}
	if e = c.jsonRequest(ctx, http.MethodPost, "/auth/login", payload, &m); e != nil {
		return domain.Identity{}, e
	}
	if _, ok := m["retCode"]; !ok {
		return domain.Identity{}, domain.E("PROTOCOL", "login response omitted its result code")
	}
	if n := integer(m["retCode"]); n != 0 {
		return domain.Identity{}, authError(n)
	}
	i, e := c.WhoAmI(ctx)
	if e != nil {
		return i, e
	}
	if c.vault != nil && password != "" {
		if e = c.vault.Put(c.key("credentials"), map[string]string{"username": username, "password": password}); e != nil {
			return i, domain.E("SECRET_STORE", "could not save platform credentials")
		}
	}
	return i, nil
}
func (c *Client) SetupPassword(ctx context.Context, newPassword, oldPassword string) error {
	if _, e := c.WhoAmI(ctx); e != nil {
		return e
	}
	p := map[string]string{"password": strings.TrimSpace(newPassword)}
	if p["password"] == "" {
		return domain.E("PASSWORD_POLICY", "password must not be empty")
	}
	if oldPassword != "" {
		p["oldPassword"] = strings.TrimSpace(oldPassword)
	}
	var m map[string]any
	if e := c.jsonRequest(ctx, http.MethodPost, "/auth/reset-user-password", p, &m); e != nil {
		return e
	}
	if _, ok := m["retCode"]; !ok {
		return domain.E("PROTOCOL", "password response omitted its result code")
	}
	if n := integer(m["retCode"]); n != 0 {
		return authError(n)
	}
	c.mu.Lock()
	username := c.identity.Username
	c.mu.Unlock()
	if c.vault != nil {
		return c.vault.Put(c.key("credentials"), map[string]string{"username": username, "password": p["password"]})
	}
	return nil
}
func (c *Client) AuthStatus(ctx context.Context) domain.AuthStatus {
	s := domain.AuthStatus{Gateway: "not_configured", Platform: "unknown", UpdatedAt: time.Now()}
	if c.gateway != nil {
		s.Gateway = "unknown"
	}
	i, e := c.WhoAmI(ctx)
	if e == nil {
		s.Platform = "authenticated"
		s.Identity = &i
		if c.gateway != nil {
			s.Gateway = "authenticated"
		}
		return s
	}
	if d, ok := e.(*domain.Error); ok {
		switch d.Code {
		case "GATEWAY_AUTH_EXPIRED":
			s.Gateway = "expired"
			s.RequiresAction = "gateway_qr_login"
		case "AUTH_EXPIRED", "AUTH_1032":
			s.Platform = "expired"
			s.RequiresAction = "platform_login"
			if c.gateway != nil {
				s.Gateway = "authenticated"
			}
		default:
			s.RequiresAction = d.Code
		}
	}
	return s
}
func (c *Client) Logout(ctx context.Context) error {
	stopErr := c.CancelSSO()
	if peer, err := c.PortalClient(); err == nil {
		_ = peer.CancelSSO()
		pErr := peer.portalRequest(ctx, "POST", "/api/auth/logout", nil, nil)
		if pErr != nil && !isAuthError(pErr) && stopErr == nil {
			stopErr = pErr
		}
	}
	var m map[string]any
	e := c.jsonRequest(ctx, http.MethodPost, "/auth/logout", nil, &m)
	if e == nil && integer(m["retCode"]) != 0 {
		e = authError(integer(m["retCode"]))
	}
	c.mu.Lock()
	s := c.socket
	c.socket = nil
	c.identity = domain.Identity{}
	c.computeVerified = time.Time{}
	c.qr = domain.AuthChallenge{}
	c.qrCompletion = ""
	c.qrConfirmVerified = false
	c.mu.Unlock()
	if s != nil {
		s.close()
	}
	// Clearing auth sessions deliberately has no access to connection-route state.
	c.cookies.mu.Lock()
	c.cookies.jar = newCookies().jar
	c.cookies.records = map[string]savedCookie{}
	c.cookies.mu.Unlock()
	if c.vault != nil {
		_ = c.vault.Delete(c.key("credentials"))
		_ = c.vault.Delete(c.key("instance_credentials"))
		_ = c.vault.Delete(c.key("sso_url"))
		_ = c.vault.Delete(c.key("qr_challenge"))
		if pe := c.persist(); e == nil {
			e = pe
		}
	}
	if isAuthError(e) {
		return stopErr
	}
	if e == nil {
		e = stopErr
	}
	return e
}

var qrSID = regexp.MustCompile(`(?s)vpn_sid=.{0,40}?([0-9a-fA-F]{16})`)

type savedQRChallenge struct {
	Challenge            domain.AuthChallenge `json:"challenge"`
	CompletionURL        string               `json:"completion_url,omitempty"`
	ConfirmationVerified bool                 `json:"confirmation_verified,omitempty"`
}

func safeOrigin(u *url.URL) string {
	origin := u.Scheme + "://" + u.Host
	if len(origin) > 300 {
		return "[invalid origin]"
	}
	return origin
}

// The verified WebVPN QR endpoint returns an HTTP self-URL even when reached
// over HTTPS. Upgrade only this exact host's default HTTP port to the configured
// default HTTPS origin. There is never an HTTP request carrying its ticket.
func upgradeGatewayHTTP(target, gateway *url.URL) {
	if target != nil && gateway != nil && target.User == nil && gateway.User == nil && gateway.Scheme == "https" && target.Scheme == "http" && strings.EqualFold(target.Hostname(), gateway.Hostname()) && (target.Port() == "" || target.Port() == "80") && (gateway.Port() == "" || gateway.Port() == "443") {
		target.Scheme = "https"
		target.Host = gateway.Host
	}
}
func (c *Client) persistQR() error {
	if c.vault == nil {
		return nil
	}
	c.mu.Lock()
	saved := savedQRChallenge{Challenge: c.qr, CompletionURL: c.qrCompletion, ConfirmationVerified: c.qrConfirmVerified}
	c.mu.Unlock()
	if e := c.vault.Put(c.key("qr_challenge"), saved); e != nil {
		return domain.E("SECRET_STORE", "could not persist QR login challenge")
	}
	return nil
}

func (c *Client) GatewayStart(ctx context.Context) (domain.AuthChallenge, error) {
	if c.parent != nil {
		return c.parent.GatewayStart(ctx)
	}
	if c.gateway == nil {
		return domain.AuthChallenge{}, domain.E("CONFIG", "gateway_url is not configured")
	}
	c.mu.Lock()
	previous := c.qr
	c.mu.Unlock()
	if (previous.State == "waiting" || previous.State == "confirmation_required") && previous.ExpiresAt.After(time.Now()) {
		return previous, nil
	}
	r, e := c.do(ctx, "GET", c.gateway.String()+"/login", nil, nil)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	defer r.Body.Close()
	b, e := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	if r.StatusCode != 200 {
		return domain.AuthChallenge{}, domain.E("GATEWAY_UNAVAILABLE", "gateway login request failed")
	}
	if !strings.HasPrefix(r.Request.URL.Path, "/login") {
		return domain.AuthChallenge{Kind: "webvpn_qr", State: "authenticated"}, nil
	}
	match := qrSID.FindSubmatch(b)
	if len(match) != 2 {
		return domain.AuthChallenge{}, domain.E("GATEWAY_PROTOCOL", "gateway QR challenge was not found")
	}
	u := *c.gateway
	u.Path = "/login"
	q := u.Query()
	q.Set("qrcode", "true")
	q.Set("vpn_sid", string(match[1]))
	u.RawQuery = q.Encode()
	challenge := domain.AuthChallenge{Kind: "webvpn_qr", URL: u.String(), State: "waiting", ExpiresAt: time.Now().Add(5 * time.Minute)}
	c.mu.Lock()
	c.qr = challenge
	c.qrCompletion = ""
	c.qrConfirmVerified = false
	c.mu.Unlock()
	return challenge, c.persistQR()
}
func (c *Client) GatewayPoll(ctx context.Context) (domain.AuthChallenge, error) {
	if c.parent != nil {
		return c.parent.GatewayPoll(ctx)
	}
	c.mu.Lock()
	challenge := c.qr
	completion := c.qrCompletion
	c.mu.Unlock()
	if c.gateway == nil || challenge.State == "" {
		return challenge, domain.E("NO_CHALLENGE", "start gateway QR login first")
	}
	if challenge.State == "authenticated" {
		return challenge, nil
	}
	if time.Now().After(challenge.ExpiresAt) {
		challenge.State = "expired"
		challenge.URL = ""
		c.mu.Lock()
		c.qr = challenge
		c.qrCompletion = ""
		c.mu.Unlock()
		return challenge, c.persistQR()
	}
	{
		var m map[string]any
		if e := c.jsonURL(ctx, "GET", c.gateway.String()+"/qrcode-check", nil, &m); e != nil {
			if completion == "" {
				return challenge, e
			}
		} else if integer(m["state"]) == 2 {
			completion = stringValue(m["url"])
		}
		if completion == "" {
			return challenge, nil
		}
		c.mu.Lock()
		c.qrCompletion = completion
		c.mu.Unlock()
		if e := c.persistQR(); e != nil {
			return challenge, e
		}
	}
	target, e := url.Parse(completion)
	if e != nil {
		return challenge, domain.E("GATEWAY_PROTOCOL", "invalid QR completion URL")
	}
	target = c.gateway.ResolveReference(target)
	upgradeGatewayHTTP(target, c.gateway)
	if target.Host != c.gateway.Host || target.Scheme != c.gateway.Scheme || target.User != nil {
		c.mu.Lock()
		c.qrCompletion = ""
		c.mu.Unlock()
		if err := c.persistQR(); err != nil {
			return challenge, err
		}
		return challenge, domain.E("GATEWAY_PROTOCOL", "QR completion URL is outside the gateway: "+safeOrigin(target))
	}
	return c.followGatewayCompletion(ctx, challenge, target)
}

var gatewayTitleRE = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
var gatewayQueryKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,47}$`)

func gatewayURLDescription(u *url.URL) string {
	p := "[other path]"
	switch u.Path {
	case "/", "/login", "/do-login", "/do-confirm-login", "/qrcode-check":
		p = u.Path
	}
	keys := []string{}
	for key := range u.Query() {
		if gatewayQueryKeyRE.MatchString(key) {
			keys = append(keys, key)
		} else {
			keys = append(keys, "[opaque key]")
		}
	}
	sort.Strings(keys)
	if len(keys) > 16 {
		keys = keys[:16]
	}
	description := fmt.Sprintf("path=%s query_keys=[%s]", p, strings.Join(keys, ","))
	switch value := u.Query().Get("auth_type"); value {
	case "local", "ldap", "cas", "sso", "oauth", "oauth2", "wrd_qrcode", "qrcode":
		description += " auth_type=" + value
	}
	return description
}
func gatewayDiagnostic(r *http.Response, target *url.URL, b []byte) string {
	title := "[no recognized title]"
	if matches := gatewayTitleRE.FindSubmatch(b); len(matches) == 2 {
		value := strings.TrimSpace(html.UnescapeString(string(matches[1])))
		switch value {
		case "北京邮电大学WebVPN系统", "WebVPN", "WebVPN 登录":
			title = value
		}
	}
	return fmt.Sprintf("HTTP %d; final %s; title=%s; completion %s", r.StatusCode, gatewayURLDescription(r.Request.URL), title, gatewayURLDescription(target))
}
func gatewayNeedsConfirmation(b []byte) bool {
	var response map[string]any
	return json.Unmarshal(b, &response) == nil && response["error"] == "NEED_CONFIRM"
}

func (c *Client) followGatewayCompletion(ctx context.Context, challenge domain.AuthChallenge, target *url.URL) (domain.AuthChallenge, error) {
	r, e := c.do(context.WithValue(ctx, gatewayRedirectScope{}, c.gateway), "GET", target.String(), nil, nil)
	if e != nil {
		return challenge, e
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	diagnostic := gatewayDiagnostic(r, target, b)
	verified := gatewayNeedsConfirmation(b)
	if verified {
		challenge.State = "confirmation_required"
		challenge.Message = "gateway explicitly requires confirmation; " + diagnostic
	} else if r.StatusCode >= 200 && r.StatusCode < 400 && !strings.HasPrefix(r.Request.URL.Path, "/login") {
		challenge.State = "authenticated"
		challenge.URL = ""
		challenge.Message = diagnostic
	} else {
		challenge.State = "waiting"
		challenge.Message = diagnostic
		c.mu.Lock()
		c.qr = challenge
		c.qrConfirmVerified = false
		c.mu.Unlock()
		if e := c.persistQR(); e != nil {
			return challenge, e
		}
		return challenge, domain.E("GATEWAY_AUTH_EXPIRED", "QR completion did not establish a gateway session; "+diagnostic)
	}
	c.mu.Lock()
	c.qr = challenge
	c.qrConfirmVerified = verified
	if challenge.State == "authenticated" {
		c.qrCompletion = ""
	}
	c.mu.Unlock()
	return challenge, c.persistQR()
}

// GatewayConfirm must be invoked explicitly by an authorized caller. A saved
// proof from the actual JSON NEED_CONFIRM response is required; text in a login
// page's JavaScript cannot authorize a single-session takeover.
func (c *Client) GatewayConfirm(ctx context.Context) (domain.AuthChallenge, error) {
	c.mu.Lock()
	challenge := c.qr
	verified := c.qrConfirmVerified
	c.mu.Unlock()
	if c.gateway == nil || !verified || challenge.State != "confirmation_required" {
		return challenge, domain.E("CONFIRMATION_NOT_REQUIRED", "gateway has not issued a verified confirmation challenge")
	}
	headers := http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}, "X-Requested-With": []string{"XMLHttpRequest"}, "Origin": []string{c.gateway.String()}, "Referer": []string{c.gateway.String() + "/login"}}
	response, e := c.do(context.WithValue(ctx, gatewayRedirectScope{}, c.gateway), "POST", c.gateway.String()+"/do-confirm-login", nil, headers)
	if e != nil {
		return challenge, e
	}
	defer response.Body.Close()
	if e = c.responseError(response); e != nil {
		return challenge, e
	}
	body, e := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if e != nil {
		return challenge, e
	}
	var result map[string]any
	if json.Unmarshal(body, &result) != nil {
		return challenge, domain.E("GATEWAY_PROTOCOL", "invalid gateway confirmation response")
	}
	if result["success"] != true {
		return challenge, domain.E("GATEWAY_CONFIRM_FAILED", "gateway did not confirm the login")
	}
	location := stringValue(result["url"])
	if location == "" {
		location = stringValue(result["data"])
	}
	if location == "" {
		location = "/"
	}
	target, e := url.Parse(location)
	if e != nil {
		return challenge, domain.E("GATEWAY_PROTOCOL", "invalid confirmation redirect")
	}
	target = c.gateway.ResolveReference(target)
	upgradeGatewayHTTP(target, c.gateway)
	if target.User != nil || target.Host != c.gateway.Host || target.Scheme != c.gateway.Scheme {
		return challenge, domain.E("GATEWAY_PROTOCOL", "confirmation redirect is outside gateway: "+safeOrigin(target))
	}
	return c.followGatewayCompletion(ctx, challenge, target)
}
