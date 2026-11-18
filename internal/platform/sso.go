package platform

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/coder/websocket"
)

const casOrigin = "https://auth.bupt.edu.cn"
const campusOrigin = "https://app.bupt.edu.cn"
const ssoStateKey = "sso_challenge"

// These endpoints and messages are from the current official CAS desktop page
// and https://auth.bupt.edu.cn/js/mxsocket.js. This is a separate QR protocol
// from WebVPN: the provider pushes ticket/confirm events over a plain WebSocket.
type savedSSO struct {
	Challenge        domain.AuthChallenge `json:"challenge"`
	LoginURL         string               `json:"login_url"`
	Service          string               `json:"service"`
	AppID            string               `json:"app_id"`
	Code             string               `json:"code,omitempty"`
	CASState         string               `json:"cas_state,omitempty"`
	ConfirmedAt      time.Time            `json:"confirmed_at,omitempty"`
	CallbackAttempts int                  `json:"callback_attempts,omitempty"`
}
type ssoSession struct {
	c                *Client
	mu               sync.Mutex
	completeMu       sync.Mutex
	saved            savedSSO
	conn             *websocket.Conn
	ctx              context.Context
	cancel           context.CancelFunc
	notify           chan struct{}
	lastErr          error
	readerDone       chan struct{}
	closed           bool
	explicitCancel   bool
	completionCancel context.CancelFunc
}

func (s *ssoSession) changed()           { close(s.notify); s.notify = make(chan struct{}) }
func (s *ssoSession) snapshot() savedSSO { s.mu.Lock(); defer s.mu.Unlock(); return s.saved }
func (s *ssoSession) persistLocked() error {
	if s.c.vault == nil {
		return domain.E("SECRET_STORE", "SSO requires encrypted session storage")
	}
	if e := s.c.vault.Put(s.c.key(ssoStateKey), s.saved); e != nil {
		return domain.E("SECRET_STORE", "could not persist the university login challenge")
	}
	return nil
}
func (s *ssoSession) shutdown() {
	s.mu.Lock()
	s.closed = true
	completionCancel := s.completionCancel
	conn := s.conn
	s.conn = nil
	if s.cancel != nil {
		s.cancel()
	}
	if s.saved.Code == "" && (s.saved.Challenge.State == "waiting" || s.saved.Challenge.State == "connecting") {
		s.saved.Challenge.State = "refresh_required"
		s.saved.Challenge.URL = ""
		s.saved.Challenge.RequiredAction = "scan_new_university_qr"
		_ = s.persistLocked()
	}
	s.changed()
	s.mu.Unlock()
	if completionCancel != nil {
		completionCancel()
	}
	if conn != nil {
		_ = conn.CloseNow()
	}
	if s.readerDone != nil {
		select {
		case <-s.readerDone:
		case <-time.After(time.Second):
		}
	}
	s.completeMu.Lock()
	s.completeMu.Unlock()
}

type ssoPolicy struct {
	native  *url.URL
	gateway *url.URL
}

func decodeProxyTarget(u *url.URL) (*url.URL, error) {
	m := proxyPath.FindStringSubmatch(u.EscapedPath())
	if len(m) == 0 {
		return nil, domain.E("SSO_CONFIG", "gateway URL has no recognized platform target")
	}
	raw, e := hex.DecodeString(m[3])
	key := []byte("wrdvpnisthebest!")
	if e != nil || len(raw) <= 16 || !bytes.Equal(raw[:16], key) {
		return nil, domain.E("SSO_CONFIG", "unsupported WebVPN hostname encoding")
	}
	block, _ := aes.NewCipher(key)
	host := make([]byte, len(raw)-16)
	cipher.NewCFBDecrypter(block, raw[:16]).XORKeyStream(host, raw[16:])
	if !regexp.MustCompile(`^[A-Za-z0-9.-]+$`).Match(host) {
		return nil, domain.E("SSO_CONFIG", "invalid proxy target hostname")
	}
	authority := string(host)
	if m[2] != "" {
		authority += ":" + strings.TrimPrefix(m[2], "-")
	}
	target, e := url.Parse(m[1] + "://" + authority + m[4])
	if e != nil {
		return nil, domain.E("SSO_CONFIG", "invalid proxy target")
	}
	target.RawQuery = u.RawQuery
	return target, nil
}
func (c *Client) ssoPolicy() (*ssoPolicy, error) {
	native, e := url.Parse(c.profile.PlatformURL)
	if e != nil || native.Host == "" {
		return nil, domain.E("SSO_CONFIG", "invalid platform URL")
	}
	if c.gateway != nil {
		if c.gateway.Scheme != "https" {
			return nil, domain.E("SSO_TLS_REQUIRED", "university SSO requires an HTTPS gateway")
		}
		if strings.EqualFold(native.Host, c.gateway.Host) {
			native, e = decodeProxyTarget(native)
			if e != nil {
				return nil, e
			}
		}
	}
	return &ssoPolicy{native: native, gateway: c.gateway}, nil
}
func defaultSSOPort(u *url.URL) bool {
	return u.Port() == "" || u.Scheme == "https" && u.Port() == "443" || u.Scheme == "http" && u.Port() == "80"
}
func (p *ssoPolicy) isNative(u *url.URL) bool {
	if !strings.EqualFold(u.Hostname(), p.native.Hostname()) {
		return false
	}
	if defaultSSOPort(u) && defaultSSOPort(p.native) {
		return true
	}
	return u.Port() == p.native.Port() && u.Port() != ""
}
func (p *ssoPolicy) logical(u *url.URL) (*url.URL, error) {
	if u == nil || u.User != nil || u.Host == "" {
		return nil, domain.E("SSO_REDIRECT_DENIED", "university login URL is invalid")
	}
	copy := *u
	copy.Fragment = ""
	if p.gateway != nil && strings.EqualFold(copy.Host, p.gateway.Host) {
		if copy.Scheme != "https" {
			return nil, domain.E("SSO_TLS_REQUIRED", "university gateway transport must use HTTPS")
		}
		if strings.HasPrefix(copy.Path, "/login") {
			return nil, domain.E("GATEWAY_AUTH_EXPIRED", "school WebVPN login is required before university SSO")
		}
		inner, e := decodeProxyTarget(&copy)
		if e != nil {
			return nil, e
		}
		if strings.EqualFold(inner.Host, p.gateway.Host) {
			return nil, domain.E("SSO_REDIRECT_DENIED", "nested gateway redirects are not supported")
		}
		return p.logical(inner)
	}
	if strings.EqualFold(copy.Hostname(), "auth.bupt.edu.cn") && defaultSSOPort(&copy) && (copy.Scheme == "http" || copy.Scheme == "https") {
		copy.Scheme = "https"
		copy.Host = "auth.bupt.edu.cn"
		return &copy, nil
	}
	if strings.EqualFold(copy.Hostname(), "app.bupt.edu.cn") && copy.Scheme == "https" && defaultSSOPort(&copy) {
		copy.Host = "app.bupt.edu.cn"
		return &copy, nil
	}
	if p.isNative(&copy) && (copy.Scheme == "https" || copy.Scheme == "http") {
		if copy.Scheme == "http" && p.gateway == nil {
			return nil, domain.E("SSO_TLS_REQUIRED", "platform callback would require an insecure direct connection")
		}
		return &copy, nil
	}
	return nil, domain.E("SSO_REDIRECT_DENIED", "university login redirected outside the configured university origins")
}
func (p *ssoPolicy) route(u *url.URL) (*url.URL, *url.URL, error) {
	logical, e := p.logical(u)
	if e != nil {
		return nil, nil, e
	}
	effective := logical
	if p.isNative(logical) && p.gateway != nil {
		mapped, e := ProxyURL(p.gateway.String(), logical.String())
		if e != nil {
			return nil, nil, e
		}
		effective, _ = url.Parse(mapped)
	}
	return logical, effective, nil
}

// Redirects are followed manually. Rewriting happens BEFORE each new request,
// so net/http attaches cookies for the actual gateway URL, never CAS cookies
// copied from the previous hop. No arbitrary redirect host can be contacted.
func (c *Client) ssoGET(ctx context.Context, p *ssoPolicy, target string, headers http.Header) (*http.Response, error) {
	return c.ssoRequest(ctx, p, target, headers, nil)
}

// A credential POST is sent once to the exact CAS login endpoint. Subsequent
// requests carry only the cookies selected by the jar for each redirect.
func (c *Client) ssoRequest(ctx context.Context, p *ssoPolicy, target string, headers http.Header, form url.Values) (*http.Response, error) {
	u, e := url.Parse(target)
	if e != nil {
		return nil, domain.E("SSO_PROTOCOL", "invalid university login URL")
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for hop := 0; hop < 10; hop++ {
		logical, effective, e := p.route(u)
		if e != nil {
			return nil, e
		}
		method := http.MethodGet
		var body io.Reader
		if form != nil {
			if !isCASLoginURL(u) || !isCASLoginURL(effective) {
				return nil, domain.E("SSO_REDIRECT_DENIED", "password submission requires the university HTTPS login endpoint")
			}
			method, body = http.MethodPost, strings.NewReader(form.Encode())
		}
		req, e := http.NewRequestWithContext(ctx, method, effective.String(), body)
		if e != nil {
			return nil, domain.E("SSO_PROTOCOL", "invalid university login request")
		}
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		req.Header.Set("User-Agent", "bupt-cli/1")
		if hop == 0 {
			for key, values := range headers {
				if !strings.EqualFold(key, "Cookie") && !strings.EqualFold(key, "Authorization") {
					req.Header[key] = values
				}
			}
		}
		r, e := client.Do(req)
		pe := c.persist()
		if e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, &domain.Error{Code: "SSO_NETWORK", Message: "university login request failed", Retryable: true}
		}
		if pe != nil {
			r.Body.Close()
			return nil, pe
		}
		if r.StatusCode < 300 || r.StatusCode >= 400 {
			return r, nil
		}
		if form != nil {
			switch r.StatusCode {
			case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther:
				form = nil
			default:
				r.Body.Close()
				return nil, domain.E("SSO_REDIRECT_DENIED", "CAS requested a redirect that would repeat password submission")
			}
		}
		location := r.Header.Get("Location")
		if location == "" {
			r.Body.Close()
			return nil, domain.E("SSO_PROTOCOL", "university redirect omitted its target")
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, 4096))
		r.Body.Close()
		next, e := url.Parse(location)
		if e != nil {
			return nil, domain.E("SSO_PROTOCOL", "invalid university redirect")
		}
		if next.Host == "" {
			if p.gateway != nil && effective.Host == p.gateway.Host && (strings.HasPrefix(next.Path, "/login") || proxyPath.MatchString(next.EscapedPath())) {
				next = effective.ResolveReference(next)
			} else {
				next = logical.ResolveReference(next)
			}
		}
		u = next
	}
	return nil, domain.E("SSO_REDIRECT_LOOP", "university login exceeded its redirect limit")
}
func ssoBody(r *http.Response) ([]byte, error) {
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return nil, domain.E("SSO_HTTP", "university login endpoint was unsuccessful")
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, 2<<20+1))
	if e != nil {
		return nil, e
	}
	if len(b) > 2<<20 {
		return nil, domain.E("SSO_PROTOCOL", "university login page exceeds the parsing limit")
	}
	return b, nil
}

var campusConfigRE = regexp.MustCompile(`(?s)config\.mobileCampus\s*=\s*\{(.*?)\}`)
var campusPropertyRE = regexp.MustCompile(`([A-Za-z][A-Za-z0-9_]*)\s*:\s*['"]([^'"\r\n]*)['"]`)
var campusTLSRE = regexp.MustCompile(`\bsecurity\s*:\s*true\b`)
var ssoOpaqueRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func campusAppID(body []byte) (string, error) {
	m := campusConfigRE.FindSubmatch(body)
	if len(m) != 2 {
		return "", domain.E("SSO_INTERACTIVE_REQUIRED", "CAS did not expose its supported university QR configuration")
	}
	properties := map[string]string{}
	for _, pair := range campusPropertyRE.FindAllSubmatch(m[1], -1) {
		properties[string(pair[1])] = html.UnescapeString(string(pair[2]))
	}
	if properties["baseHost"] != "app.bupt.edu.cn" || properties["baseUrl"] != campusOrigin || !campusTLSRE.Match(m[1]) || !ssoOpaqueRE.MatchString(properties["appid"]) {
		return "", domain.E("SSO_PROVIDER_UNSUPPORTED", "CAS QR provider is not the verified HTTPS university provider")
	}
	return properties["appid"], nil
}
func ssoMissing(e error) bool {
	var d *domain.Error
	return errors.Is(e, os.ErrNotExist) || errors.As(e, &d) && d.Code == "NOT_FOUND"
}
func (c *Client) loadSSO() (*ssoSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, domain.E("CLOSED", "platform client is closed")
	}
	if c.sso != nil {
		return c.sso, nil
	}
	if c.vault == nil {
		return nil, domain.E("SECRET_STORE", "university SSO requires encrypted session storage")
	}
	var saved savedSSO
	if e := c.vault.Get(c.key(ssoStateKey), &saved); e != nil {
		if ssoMissing(e) {
			return nil, nil
		}
		return nil, domain.E("SECRET_STORE", "could not restore university login state")
	}
	s := &ssoSession{c: c, saved: saved, notify: make(chan struct{})}
	if s.saved.Code == "" && s.saved.Challenge.State != "authenticated" {
		s.saved.Challenge.State = "refresh_required"
		s.saved.Challenge.URL = ""
		s.saved.Challenge.RequiredAction = "scan_new_university_qr"
	}
	c.sso = s
	return s, nil
}
func ssoChallenge(state string) domain.AuthChallenge {
	return domain.AuthChallenge{Stage: "university_sso", Kind: "bupt_sso_qr", State: state, PollAfterMS: 1000, RequiredAction: "scan_and_confirm_university_qr"}
}

// verifySSO uses the existing WhoAmI parser/UID pin, with redirects disabled.
// A successful callback alone is never treated as an authenticated platform.
func (c *Client) verifySSO(ctx context.Context) (domain.Identity, error) {
	if c.parent != nil {
		return c.WhoAmI(ctx)
	}
	httpClient := *c.http
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	shadow := &Client{profile: c.profile, vault: c.vault, http: &httpClient, cookies: c.cookies, base: c.base, gateway: c.gateway, contact: c.contact}
	identity, e := shadow.WhoAmI(ctx)
	if e != nil {
		return identity, e
	}
	c.mu.Lock()
	old := c.socket
	if c.identity.UID == "" || c.identity.UID == identity.UID {
		old = nil
	}
	if old != nil {
		c.socket = nil
	}
	c.identity = identity
	c.mu.Unlock()
	if old != nil {
		old.close()
	}
	return identity, nil
}
func (c *Client) ssoLoginTarget(ctx context.Context, policy *ssoPolicy) (*url.URL, error) {
	if c.parent != nil {
		return c.portalCASTarget(ctx, policy)
	}
	if c.vault == nil {
		return nil, domain.E("SECRET_STORE", "university login requires protected session storage")
	}
	var login string
	if e := c.vault.Get(c.key("sso_url"), &login); e != nil || login == "" {
		if _, e = c.AuthInfo(ctx); e != nil {
			return nil, e
		}
		if e = c.vault.Get(c.key("sso_url"), &login); e != nil || login == "" {
			return nil, domain.E("SSO_UNAVAILABLE", "platform did not provide a university SSO login URL")
		}
	}
	target, e := url.Parse(login)
	if e != nil {
		return nil, domain.E("SSO_PROTOCOL", "invalid CAS URL")
	}
	target, e = policy.logical(target)
	if e != nil {
		return nil, e
	}
	if !isCASLoginURL(target) {
		return nil, domain.E("SSO_REDIRECT_DENIED", "platform SSO URL is not the verified CAS login endpoint")
	}
	service := target.Query().Get("service")
	serviceURL, e := url.Parse(service)
	if e != nil || service == "" {
		return nil, domain.E("SSO_PROTOCOL", "CAS login URL omitted its platform service")
	}
	logicalService, e := policy.logical(serviceURL)
	if e != nil || !policy.isNative(logicalService) {
		return nil, domain.E("SSO_REDIRECT_DENIED", "CAS service does not belong to the configured platform")
	}
	return target, nil
}

func (c *Client) SSOStart(ctx context.Context) (domain.AuthChallenge, error) {
	c.ssoStartMu.Lock()
	defer c.ssoStartMu.Unlock()
	ctx, startCancel := context.WithCancel(ctx)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		startCancel()
		return domain.AuthChallenge{}, domain.E("CLOSED", "platform client is closed")
	}
	c.ssoStartCancel = startCancel
	c.mu.Unlock()
	defer func() { startCancel(); c.mu.Lock(); c.ssoStartCancel = nil; c.mu.Unlock() }()
	if _, e := c.verifySSO(ctx); e == nil {
		return c.rememberSSOAuthenticated(ctx)
	} else {
		var d *domain.Error
		if errors.As(e, &d) && (d.Code == "IDENTITY_MISMATCH" || d.Code == "GATEWAY_AUTH_EXPIRED") {
			return domain.AuthChallenge{}, e
		}
	}
	existing, e := c.loadSSO()
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	if existing != nil {
		saved := existing.snapshot()
		if saved.Code != "" {
			return c.SSOPoll(ctx)
		}
		existing.mu.Lock()
		active := existing.conn != nil
		existing.mu.Unlock()
		if active && saved.Challenge.State == "waiting" && saved.Challenge.ExpiresAt.After(time.Now()) {
			return saved.Challenge, nil
		}
		existing.shutdown()
	}
	policy, e := c.ssoPolicy()
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	target, e := c.ssoLoginTarget(ctx, policy)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	service := target.Query().Get("service")
	r, e := c.ssoGET(ctx, policy, target.String(), nil)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	body, e := ssoBody(r)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	if _, e = c.verifySSO(ctx); e == nil {
		return c.rememberSSOAuthenticated(ctx)
	}
	appid, e := campusAppID(body)
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	bg, cancel := context.WithCancel(context.Background())
	s := &ssoSession{c: c, saved: savedSSO{Challenge: ssoChallenge("connecting"), LoginURL: target.String(), Service: service, AppID: appid}, ctx: bg, cancel: cancel, notify: make(chan struct{}), readerDone: make(chan struct{})}
	s.saved.Challenge.ExpiresAt = time.Now().Add(5 * time.Minute)
	s.mu.Lock()
	e = s.persistLocked()
	s.mu.Unlock()
	if e != nil {
		cancel()
		return domain.AuthChallenge{}, e
	}
	providerURL := "wss://app.bupt.edu.cn/sock/qrcode?" + url.Values{"appid": []string{appid}}.Encode()
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	stopRequestCancel := context.AfterFunc(ctx, cancel)
	handshakeTimer := time.AfterFunc(45*time.Second, cancel)
	conn, _, e := websocket.Dial(bg, providerURL, &websocket.DialOptions{HTTPClient: &client, HTTPHeader: http.Header{"Origin": []string{casOrigin}}})
	stopRequestCancel()
	handshakeTimer.Stop()
	if e != nil {
		cancel()
		return domain.AuthChallenge{}, domain.E("SSO_QR_CONNECT", "could not connect to the university QR provider")
	}
	if ctx.Err() != nil {
		conn.CloseNow()
		cancel()
		return domain.AuthChallenge{}, ctx.Err()
	}
	conn.SetReadLimit(64 << 10)
	if e = c.persist(); e != nil {
		conn.CloseNow()
		cancel()
		return domain.AuthChallenge{}, e
	}
	s.conn = conn
	go s.read(conn)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		s.shutdown()
		return domain.AuthChallenge{}, domain.E("CLOSED", "platform client is closed")
	}
	c.sso = s
	c.mu.Unlock()
	go s.expire()
	return s.waitTicket(ctx)
}
func (s *ssoSession) waitTicket(ctx context.Context) (domain.AuthChallenge, error) {
	for {
		s.mu.Lock()
		challenge := s.saved.Challenge
		e := s.lastErr
		ch := s.notify
		s.mu.Unlock()
		if e != nil {
			return challenge, e
		}
		if challenge.URL != "" || challenge.State != "connecting" {
			return challenge, nil
		}
		select {
		case <-ctx.Done():
			return challenge, ctx.Err()
		case <-ch:
		}
	}
}
func (s *ssoSession) expire() {
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
			s.mu.Lock()
			expired := s.saved.Code == "" && !s.saved.Challenge.ExpiresAt.IsZero() && time.Now().After(s.saved.Challenge.ExpiresAt)
			if expired {
				s.saved.Challenge.State = "expired"
				s.saved.Challenge.URL = ""
				s.saved.Challenge.RequiredAction = "scan_new_university_qr"
				_ = s.persistLocked()
				s.changed()
			}
			s.mu.Unlock()
			if expired {
				s.shutdown()
				return
			}
		}
	}
}
func (s *ssoSession) read(reader *websocket.Conn) {
	defer func() {
		_ = reader.CloseNow()
		if s.cancel != nil {
			s.cancel()
		}
		s.mu.Lock()
		if s.saved.Code == "" && (s.saved.Challenge.State == "waiting" || s.saved.Challenge.State == "connecting") {
			s.saved.Challenge.State = "refresh_required"
			s.saved.Challenge.URL = ""
			s.saved.Challenge.RequiredAction = "scan_new_university_qr"
			_ = s.persistLocked()
		}
		s.conn = nil
		s.changed()
		s.mu.Unlock()
		close(s.readerDone)
	}()
	for {
		_, body, e := reader.Read(s.ctx)
		if e != nil {
			return
		}
		var message struct {
			Event string          `json:"event"`
			Data  json.RawMessage `json:"data"`
		}
		if json.Unmarshal(body, &message) != nil {
			s.mu.Lock()
			s.lastErr = domain.E("SSO_PROTOCOL", "university QR provider sent an invalid event")
			s.changed()
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		if s.closed || s.saved.Challenge.State == "authenticated" {
			s.mu.Unlock()
			return
		}
		// mxsocket dispatches by event before inspecting its payload. Scan,
		// keepalive and extension messages need not carry an object; treating
		// them as ticket/confirm payloads closes an otherwise valid QR socket.
		var data map[string]json.RawMessage
		if message.Event == "ticket" || message.Event == "confirm" {
			if json.Unmarshal(message.Data, &data) != nil || data == nil {
				s.lastErr = domain.E("SSO_PROTOCOL", "university QR approval message has an invalid payload")
				s.changed()
				s.mu.Unlock()
				return
			}
		}
		switch message.Event {
		case "ticket":
			if s.saved.Code != "" {
				break
			}
			var ticket string
			var expiry any
			_ = json.Unmarshal(data["ticket"], &ticket)
			_ = decodeInto(data["expire"], &expiry)
			expire := integer(expiry)
			if ticket == "" || len(ticket) > 4096 || strings.ContainsAny(ticket, "\x00\r\n") {
				s.lastErr = domain.E("SSO_PROTOCOL", "university QR ticket is invalid")
				break
			}
			if expire <= 0 || expire > 3600 {
				expire = 300
			}
			s.saved.Challenge = ssoChallenge("waiting")
			s.saved.Challenge.URL = campusOrigin + "/site/smlogin/index?" + url.Values{"ticket": []string{ticket}}.Encode()
			s.saved.Challenge.ExpiresAt = time.Now().Add(time.Duration(expire) * time.Second)
		case "confirm":
			if s.saved.Code != "" {
				break
			}
			var code string
			_ = json.Unmarshal(data["code"], &code)
			if code == "" || len(code) > 8192 || strings.ContainsAny(code, "\x00\r\n") {
				s.lastErr = domain.E("SSO_PROTOCOL", "university QR approval omitted its authorization code")
				break
			}
			s.saved.Code = code
			s.saved.ConfirmedAt = time.Now().UTC()
			s.saved.Challenge.State = "confirmed"
			s.saved.Challenge.URL = ""
			s.saved.Challenge.RequiredAction = ""
			s.saved.Challenge.ExpiresAt = time.Now().Add(2 * time.Minute)
		case "reject":
			s.saved.Challenge.State = "rejected"
			s.saved.Challenge.URL = ""
			s.saved.Challenge.RequiredAction = "scan_new_university_qr"
		default:
			s.mu.Unlock()
			continue
		}
		if e = s.persistLocked(); e != nil {
			s.lastErr = e
		}
		s.changed()
		// Keep the provider connection alive while CAS completes the approved
		// login. The browser closes it on navigation, not upon receiving code.
		stop := s.saved.Challenge.State == "rejected" || s.lastErr != nil
		s.mu.Unlock()
		if stop {
			_ = reader.CloseNow()
			return
		}
	}
}

func stateFromCASXML(body []byte, p *ssoPolicy) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", domain.E("SSO_PROTOCOL", "CAS state response is not valid XML")
		}
		if element, ok := token.(xml.StartElement); ok && element.Name.Local == "redirect" {
			for _, attr := range element.Attr {
				if attr.Name.Local != "url" {
					continue
				}
				u, e := url.Parse(attr.Value)
				if e != nil {
					return "", domain.E("SSO_PROTOCOL", "CAS state redirect is invalid")
				}
				u, e = p.logical(u)
				if e != nil {
					return "", e
				}
				if u.Host != "app.bupt.edu.cn" || u.Scheme != "https" {
					return "", domain.E("SSO_REDIRECT_DENIED", "CAS authorization state is outside the university provider")
				}
				state := u.Query().Get("state")
				if state == "" || len(state) > 8192 || strings.ContainsAny(state, "\x00\r\n") {
					return "", domain.E("SSO_PROTOCOL", "CAS state response omitted its state")
				}
				return state, nil
			}
		}
	}
	return "", domain.E("SSO_PROTOCOL", "CAS state response omitted its redirect")
}
func (c *Client) SSOPoll(ctx context.Context) (domain.AuthChallenge, error) {
	s, e := c.loadSSO()
	if e != nil {
		return domain.AuthChallenge{}, e
	}
	if s == nil {
		return domain.AuthChallenge{}, domain.E("NO_SSO_CHALLENGE", "start university QR login first")
	}
	s.completeMu.Lock()
	s.mu.Lock()
	if s.closed {
		challenge := s.saved.Challenge
		explicit := s.explicitCancel
		s.mu.Unlock()
		s.completeMu.Unlock()
		if !explicit && (challenge.State == "expired" || challenge.State == "refresh_required" || challenge.State == "rejected") {
			return challenge, nil
		}
		return domain.AuthChallenge{}, domain.E("SSO_CANCELLED", "university login was cancelled")
	}
	completionCtx, completionCancel := context.WithCancel(ctx)
	s.completionCancel = completionCancel
	s.mu.Unlock()
	ctx = completionCtx
	defer func() {
		completionCancel()
		s.mu.Lock()
		s.completionCancel = nil
		s.mu.Unlock()
		s.completeMu.Unlock()
	}()
	s.mu.Lock()
	providerErr := s.lastErr
	s.mu.Unlock()
	if providerErr != nil {
		return s.snapshot().Challenge, providerErr
	}
	saved := s.snapshot()
	if saved.Challenge.State == "authenticated" {
		if _, e = c.verifySSO(ctx); e == nil {
			return saved.Challenge, nil
		} else {
			return saved.Challenge, e
		}
	}
	if saved.Code == "" {
		s.mu.Lock()
		challenge, e := s.saved.Challenge, s.lastErr
		s.mu.Unlock()
		return challenge, e
	}
	if _, e = c.verifySSO(ctx); e == nil {
		return s.authenticated()
	} else {
		var de *domain.Error
		if errors.As(e, &de) && de.Code == "IDENTITY_MISMATCH" {
			return saved.Challenge, e
		}
	}
	if time.Since(saved.ConfirmedAt) > 2*time.Minute || saved.CallbackAttempts >= 2 {
		s.mu.Lock()
		s.saved.Challenge.State = "refresh_required"
		s.saved.Challenge.RequiredAction = "scan_new_university_qr"
		s.saved.Code = ""
		s.saved.CASState = ""
		e = s.persistLocked()
		challenge := s.saved.Challenge
		s.mu.Unlock()
		return challenge, e
	}
	policy, e := c.ssoPolicy()
	if e != nil {
		return saved.Challenge, e
	}
	serviceURL, parseErr := url.Parse(saved.Service)
	if parseErr != nil {
		return saved.Challenge, domain.E("SSO_PROTOCOL", "saved CAS service is invalid")
	}
	logicalService, parseErr := policy.logical(serviceURL)
	if parseErr != nil || !policy.isNative(logicalService) {
		return saved.Challenge, domain.E("SSO_REDIRECT_DENIED", "saved CAS service no longer matches the configured platform")
	}
	if saved.CASState == "" {
		q := url.Values{"client_name": []string{"mc-qr"}, "javax.faces.partial.ajax": []string{"true"}, "service": []string{saved.Service}}
		r, e := c.ssoGET(ctx, policy, casOrigin+"/authserver/clientredirect?"+q.Encode(), http.Header{"X-Requested-With": []string{"XMLHttpRequest"}, "Accept": []string{"application/xml,text/xml,*/*"}, "Referer": []string{casOrigin + "/authserver/cas/login-normal.html"}})
		if e != nil {
			return saved.Challenge, e
		}
		body, e := ssoBody(r)
		if e != nil {
			return saved.Challenge, e
		}
		state, e := stateFromCASXML(body, policy)
		if e != nil {
			return saved.Challenge, e
		}
		s.mu.Lock()
		s.saved.CASState = state
		e = s.persistLocked()
		saved = s.saved
		s.mu.Unlock()
		if e != nil {
			return saved.Challenge, e
		}
	}
	q := url.Values{"client_name": []string{"mc-qr"}, "code": []string{saved.Code}, "state": []string{saved.CASState}}
	s.mu.Lock()
	s.saved.CallbackAttempts++
	e = s.persistLocked()
	s.mu.Unlock()
	if e != nil {
		return saved.Challenge, e
	}
	response, e := c.ssoGET(ctx, policy, casOrigin+"/authserver/login?"+q.Encode(), nil)
	if e != nil {
		return saved.Challenge, e
	}
	if _, e = ssoBody(response); e != nil {
		return saved.Challenge, e
	}
	if _, e = c.verifySSO(ctx); e != nil {
		var de *domain.Error
		if errors.As(e, &de) && de.Code == "IDENTITY_MISMATCH" {
			return saved.Challenge, e
		}
		return saved.Challenge, domain.E("SSO_VERIFICATION_PENDING", "CAS completed but the configured platform session is not authenticated yet")
	}
	return s.authenticated()
}
func (s *ssoSession) authenticated() (domain.AuthChallenge, error) {
	s.mu.Lock()
	s.saved.Challenge = ssoChallenge("authenticated")
	s.saved.Challenge.RequiredAction = ""
	s.saved.Challenge.PollAfterMS = 0
	s.saved.Code = ""
	s.saved.CASState = ""
	s.saved.Service = ""
	s.saved.LoginURL = ""
	e := s.persistLocked()
	challenge := s.saved.Challenge
	conn := s.conn
	s.mu.Unlock()
	if conn != nil {
		_ = conn.CloseNow()
	}
	return challenge, e
}

func (c *Client) rememberSSOAuthenticated(ctx context.Context) (domain.AuthChallenge, error) {
	c.mu.Lock()
	old := c.sso
	c.sso = nil
	c.mu.Unlock()
	if old != nil {
		old.shutdown()
	}
	if _, e := c.verifySSO(ctx); e != nil {
		return domain.AuthChallenge{}, e
	}
	s := &ssoSession{c: c, notify: make(chan struct{})}
	challenge, e := s.authenticated()
	if e != nil {
		return challenge, e
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return domain.AuthChallenge{}, domain.E("CLOSED", "platform client is closed")
	}
	c.sso = s
	c.mu.Unlock()
	return challenge, nil
}

// CancelSSO is for explicit logout. Close instead preserves a confirmed code so
// restarting the controller can finish the already approved login.
func (c *Client) CancelSSO() error {
	c.mu.Lock()
	s := c.sso
	c.sso = nil
	startCancel := c.ssoStartCancel
	c.mu.Unlock()
	if startCancel != nil {
		startCancel()
	}
	if s != nil {
		s.mu.Lock()
		s.explicitCancel = true
		s.mu.Unlock()
		s.shutdown()
	}
	c.ssoStartMu.Lock()
	defer c.ssoStartMu.Unlock()
	c.mu.Lock()
	late := c.sso
	c.sso = nil
	c.mu.Unlock()
	if late != nil && late != s {
		late.mu.Lock()
		late.explicitCancel = true
		late.mu.Unlock()
		late.shutdown()
	}
	if c.vault != nil {
		return c.vault.Delete(c.key(ssoStateKey))
	}
	return nil
}
