// Package platform implements the BUPT WebVPN and EaaS wire protocols. It has
// no dependency on SSH routes: losing these sessions never closes an SSH tunnel.
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/domain"
)

const maxJSON = 32 << 20

type gatewayRedirectScope struct{}

type savedCookie struct {
	URL    string      `json:"url"`
	Cookie http.Cookie `json:"cookie"`
}
type sessionData struct {
	Cookies []savedCookie `json:"cookies"`
}
type cookieStore struct {
	persistMu sync.Mutex
	mu        sync.Mutex
	jar       *cookiejar.Jar
	records   map[string]savedCookie
}

func newCookies() *cookieStore {
	j, _ := cookiejar.New(nil)
	return &cookieStore{jar: j, records: map[string]savedCookie{}}
}
func (j *cookieStore) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.jar.Cookies(u)
}
func (j *cookieStore) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.jar.SetCookies(u, cs)
	for _, c := range cs {
		cp := *c
		if cp.Path == "" {
			p := u.Path
			i := strings.LastIndex(p, "/")
			if i <= 0 {
				cp.Path = "/"
			} else {
				cp.Path = p[:i]
			}
		}
		host := strings.ToLower(cp.Domain)
		if host == "" {
			host = strings.ToLower(u.Hostname())
		}
		key := host + "|" + cp.Path + "|" + cp.Name
		if cp.MaxAge < 0 || (!cp.Expires.IsZero() && cp.Expires.Before(time.Now())) {
			delete(j.records, key)
			continue
		}
		if cp.MaxAge > 0 {
			cp.Expires = time.Now().Add(time.Duration(cp.MaxAge) * time.Second)
			cp.MaxAge = 0
		}
		origin := url.URL{Scheme: u.Scheme, Host: u.Host, Path: cp.Path}
		j.records[key] = savedCookie{origin.String(), cp}
	}
}
func (j *cookieStore) snapshot() sessionData {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := sessionData{}
	for _, c := range j.records {
		if c.Cookie.Expires.IsZero() || c.Cookie.Expires.After(time.Now()) {
			s.Cookies = append(s.Cookies, c)
		}
	}
	return s
}

type Client struct {
	portalObserved     domain.PortalAccount
	retryPortalAfter   time.Time
	portalPeer         *Client
	parent             *Client
	serviceMu          sync.Mutex
	lastServiceAttempt time.Time
	computeVerified    time.Time
	profile            domain.Profile
	vault              domain.SecretStore
	http               *http.Client
	cookies            *cookieStore
	base               *url.URL
	gateway            *url.URL
	mu                 sync.Mutex
	identity           domain.Identity
	qr                 domain.AuthChallenge
	qrCompletion       string
	qrConfirmVerified  bool
	socket             *eventSocket
	sso                *ssoSession
	ssoStartMu         sync.Mutex
	ssoStartCancel     context.CancelFunc
	closed             bool
	contact            *identityContactCache
}

var _ domain.PlatformAPI = (*Client)(nil)

func New(profile domain.Profile, vault domain.SecretStore) (*Client, error) {
	base, e := url.Parse(profile.PlatformURL)
	if e != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return nil, domain.E("CONFIG", "platform_url must be an HTTP(S) URL without credentials")
	}
	base.RawQuery = ""
	base.Fragment = ""
	c := &Client{profile: profile, vault: vault, base: base, cookies: newCookies(), contact: &identityContactCache{records: map[string]identityContactRecord{}}}
	if profile.GatewayURL != "" {
		g, e := url.Parse(profile.GatewayURL)
		if e != nil || g.Host == "" || (g.Scheme != "http" && g.Scheme != "https") || g.User != nil {
			return nil, domain.E("CONFIG", "invalid gateway_url")
		}
		g.Path = ""
		g.RawPath = ""
		g.RawQuery = ""
		g.Fragment = ""
		c.gateway = g
		if !strings.EqualFold(base.Host, g.Host) {
			proxy, err := ProxyURL(g.String(), base.String())
			if err != nil {
				return nil, err
			}
			c.base, _ = url.Parse(proxy)
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 45 * time.Second
	if profile.HTTPProxy != "" {
		p, e := url.Parse(profile.HTTPProxy)
		if e != nil || p.Host == "" {
			return nil, domain.E("CONFIG", "invalid http_proxy")
		}
		tr.Proxy = http.ProxyURL(p)
	}
	c.http = &http.Client{Transport: tr, Jar: c.cookies, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return domain.E("REDIRECT", "too many redirects")
		}
		if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
			return domain.E("REDIRECT", "invalid redirect scheme")
		}
		if len(via) > 0 {
			if origin, ok := via[0].Context().Value(gatewayRedirectScope{}).(*url.URL); ok {
				upgradeGatewayHTTP(req.URL, origin)
				if req.URL.Host != origin.Host || req.URL.Scheme != origin.Scheme {
					return domain.E("GATEWAY_PROTOCOL", "gateway completion redirected outside its origin: "+safeOrigin(req.URL))
				}
			}
		}
		if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
			return domain.E("REDIRECT", "refused HTTPS downgrade")
		}
		return nil
	}}
	if vault != nil {
		var s sessionData
		if e := vault.Get(c.key("session"), &s); e == nil {
			for _, r := range s.Cookies {
				u, e := url.Parse(r.URL)
				if e == nil && u.Host != "" {
					c.cookies.SetCookies(u, []*http.Cookie{&r.Cookie})
				}
			}
		} else {
			var de *domain.Error
			if !errors.Is(e, os.ErrNotExist) && !(errors.As(e, &de) && de.Code == "NOT_FOUND") {
				return nil, domain.E("SECRET_STORE", "could not read the saved authentication session")
			}
		}
		var challenge savedQRChallenge
		if e := vault.Get(c.key("qr_challenge"), &challenge); e == nil {
			if c.gateway != nil {
				c.qr = challenge.Challenge
				c.qrCompletion = challenge.CompletionURL
				c.qrConfirmVerified = challenge.ConfirmationVerified
			}
		} else {
			var de *domain.Error
			if !errors.Is(e, os.ErrNotExist) && !(errors.As(e, &de) && de.Code == "NOT_FOUND") {
				return nil, domain.E("SECRET_STORE", "could not read saved QR login challenge")
			}
		}
	}
	return c, nil
}
func (c *Client) key(s string) string {
	if c.parent != nil && s != "session" {
		s = "portal/" + s
	}
	return "platform/" + c.profile.Name + "/" + s
}
func (c *Client) persist() error {
	if c.vault == nil {
		return nil
	}
	c.cookies.persistMu.Lock()
	defer c.cookies.persistMu.Unlock()
	if e := c.vault.Put(c.key("session"), c.cookies.snapshot()); e != nil {
		return domain.E("SECRET_STORE", "could not persist authentication session")
	}
	return nil
}
func (c *Client) endpoint(path string) string {
	return strings.TrimRight(c.base.String(), "/") + "/" + strings.TrimLeft(path, "/")
}
func (c *Client) do(ctx context.Context, method, target string, body io.Reader, h http.Header) (*http.Response, error) {
	req, e := http.NewRequestWithContext(ctx, method, target, body)
	if e != nil {
		return nil, domain.E("REQUEST", "invalid request URL")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "bupt-cli/1")
	for k, vs := range h {
		req.Header[k] = vs
	}
	r, e := c.http.Do(req)
	pe := c.persist()
	if e != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var de *domain.Error
		if errors.As(e, &de) {
			return nil, de
		}
		return nil, &domain.Error{Code: "NETWORK", Message: "platform request failed", Retryable: true}
	}
	if pe != nil {
		r.Body.Close()
		return nil, pe
	}
	return r, nil
}
func (c *Client) responseError(r *http.Response) error {
	if c.gateway != nil && r.Request.URL.Host == c.gateway.Host && strings.HasPrefix(r.Request.URL.Path, "/login") {
		return domain.E("GATEWAY_AUTH_EXPIRED", "school WebVPN login is required")
	}
	if r.StatusCode == http.StatusBadRequest && strings.Contains(r.Request.URL.Path, "/api/meter/billing/") {
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		s := strings.ToLower(string(raw))
		fields := []string{}
		for _, f := range []string{"columns", "value", "orderby", "limit", "page", "startat", "endat", "string", "number", "integer", "missing", "required", "timestamp", "unmarshal"} {
			if strings.Contains(s, f) {
				fields = append(fields, f)
			}
		}
		msg := "学校未接受账单查询参数"
		if len(fields) > 0 {
			msg += "（" + strings.Join(fields, ", ") + "）"
		}
		return domain.E("BILLING_QUERY_REJECTED", msg)
	}
	if r.StatusCode == 401 {
		return domain.E("AUTH_EXPIRED", "platform login is required")
	}
	if r.StatusCode == 403 {
		return domain.E("FORBIDDEN", "platform refused this operation")
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return &domain.Error{Code: fmt.Sprintf("HTTP_%d", r.StatusCode), Message: "platform request was unsuccessful", Retryable: r.StatusCode == 429 || r.StatusCode >= 500}
	}
	return nil
}
func (c *Client) jsonRequest(ctx context.Context, method, path string, in any, out any) error {
	return c.jsonURL(ctx, method, c.endpoint(path), in, out)
}
func (c *Client) jsonURL(ctx context.Context, method, target string, in any, out any) error {
	var body io.Reader
	h := http.Header{}
	if in != nil {
		b, e := json.Marshal(in)
		if e != nil {
			return e
		}
		body = bytes.NewReader(b)
		h.Set("Content-Type", "application/json;charset=utf-8")
	}
	r, e := c.do(ctx, method, target, body, h)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if e = c.responseError(r); e != nil {
		return e
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, maxJSON+1))
	if e != nil {
		return e
	}
	if len(b) > maxJSON {
		return domain.E("RESPONSE_TOO_LARGE", "JSON response exceeds limit")
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(b), []byte("<")) {
		if c.gateway != nil {
			return domain.E("GATEWAY_AUTH_EXPIRED", "gateway returned a login page")
		}
		return domain.E("AUTH_EXPIRED", "platform returned a login page")
	}
	if out == nil {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if e = d.Decode(out); e != nil {
		return domain.E("PROTOCOL", "invalid JSON response")
	}
	return nil
}
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	peer := c.portalPeer
	s := c.socket
	c.socket = nil
	sso := c.sso
	c.sso = nil
	startCancel := c.ssoStartCancel
	c.mu.Unlock()
	if peer != nil {
		_ = peer.Close()
	}
	if startCancel != nil {
		startCancel()
	}
	if s != nil {
		s.close()
	}
	if sso != nil {
		sso.shutdown()
	}
	c.ssoStartMu.Lock()
	c.ssoStartMu.Unlock()
	c.http.CloseIdleConnections()
	return c.persist()
}
func unwrap(m map[string]any) (any, error) {
	if n, ok := m["errCode"]; ok && integer(n) != 0 {
		return nil, &domain.Error{Code: fmt.Sprintf("PLATFORM_%d", integer(n)), Message: "platform rejected request"}
	}
	if v, ok := m["data"]; ok {
		return v, nil
	}
	return m, nil
}
func decodeInto(v any, out any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	return d.Decode(out)
}
func stringValue(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return fmt.Sprintf("%.0f", x)
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}
func integer(v any) int64 {
	var n json.Number
	switch x := v.(type) {
	case json.Number:
		n = x
	case string:
		n = json.Number(x)
	case float64:
		return int64(x)
	case int:
		return int64(x)
	case int64:
		return x
	}
	i, _ := n.Int64()
	return i
}
func id(v any) domain.ID { return domain.ID(stringValue(v)) }
func object(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	if s, ok := v.(string); ok {
		var m map[string]any
		d := json.NewDecoder(strings.NewReader(s))
		d.UseNumber()
		_ = d.Decode(&m)
		return m
	}
	return nil
}
func array(v any) []any    { a, _ := v.([]any); return a }
func boolValue(v any) bool { return v == true || v == "true" }
func timestamp(v any) time.Time {
	if s, ok := v.(string); ok {
		if t, e := time.Parse(time.RFC3339, s); e == nil {
			return t
		}
	}
	n := integer(v)
	if n == 0 {
		return time.Time{}
	}
	if n > 1e12 {
		return time.UnixMilli(n)
	}
	return time.Unix(n, 0)
}
func authError(code int64) error {
	names := map[int64]string{1010: "USER_NOT_FOUND", 1023: "INVALID_CREDENTIALS", 1089: "ACCOUNT_LOCKED", 1054: "CAPTCHA_REQUIRED", 1058: "INITIALIZATION_REQUIRED", 1059: "TOKEN_REQUIRED", 1060: "ACCOUNT_PENDING", 1068: "ACCOUNT_DISABLED", 1009: "PASSWORD_POLICY"}
	s := names[code]
	if s == "" {
		s = fmt.Sprintf("AUTH_%d", code)
	}
	return domain.E(s, fmt.Sprintf("platform authentication requires action (server code %d)", code))
}
func isAuthError(e error) bool {
	var d *domain.Error
	return errors.As(e, &d) && (d.Code == "AUTH_EXPIRED" || d.Code == "GATEWAY_AUTH_EXPIRED")
}
