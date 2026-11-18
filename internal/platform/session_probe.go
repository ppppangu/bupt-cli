package platform

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"bupt.local/cli/internal/domain"
)

// ProbeSession never mutates/persists the adapter's jar, credentials, identity,
// or login flows. Ordinary GETs can still count as activity at the remote server;
// therefore repeated probes are not an idle-session experiment.
type SessionProbe struct {
	Time          time.Time               `json:"time"`
	ProxyScope    string                  `json:"proxy_scope"`
	Gateway       SessionLayerProbe       `json:"gateway"`
	Platform      SessionLayerProbe       `json:"platform"`
	CAS           SessionLayerProbe       `json:"cas"`
	Cookies       []SessionCookieMetadata `json:"session_attributes"`
	CookieChanged bool                    `json:"received_session_update"`
}
type SessionLayerProbe struct {
	State            string                  `json:"state"`
	HTTPStatus       int                     `json:"http_status,omitempty"`
	SafeRedirectPath string                  `json:"safe_redirect_path,omitempty"`
	Diagnostics      SessionProbeDiagnostics `json:"diagnostics"`
	Reason           string                  `json:"reason,omitempty"`
	ResponseShape    *SessionResponseShape   `json:"response_shape,omitempty"`
}
type SessionResponseShape struct {
	Type          string                 `json:"type"`
	Fields        []SessionResponseField `json:"fields,omitempty"`
	OmittedFields int                    `json:"omitted_fields,omitempty"`
}
type SessionResponseField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type SessionProbeDiagnostics struct {
	Parsed           bool `json:"parsed"`
	LogoutByOther    bool `json:"logout_by_other"`
	LogoutByIPChange bool `json:"logout_by_ip_change"`
	NeedTwoStep      bool `json:"need_two_step"`
}
type SessionCookieMetadata struct {
	Name      string     `json:"name"`
	Host      string     `json:"host"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	Session   bool       `json:"session"`
	Secure    bool       `json:"secure"`
	HTTPOnly  bool       `json:"http_only"`
}

type sessionProbeObservation struct {
	status       int
	path         string
	body         []byte
	gatewayLogin bool
	casRedirect  bool
	reason       string
}

var probeScripts = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script\s*>`)
var probeHTMLComments = regexp.MustCompile(`(?s)<!--.*?-->`)
var probeLoginInput = regexp.MustCompile(`(?is)<input\b[^>]*\bname\s*=\s*["']auth_type["'][^>]*>`)
var probeLoginQR = regexp.MustCompile(`(?is)<[^>]+\bid\s*=\s*["'](?:wrd-qrcode|wrd_qrcode)["'][^>]*>`)
var probeCookieName = regexp.MustCompile("^[!#$%&'*+.^_`|~A-Za-z0-9-]{1,256}$")

func safeProbePath(u *url.URL) string {
	switch u.Path {
	case "/", "/login", "/auth/user", "/authserver/login", "/user/portal/collections":
		return u.Path
	}
	if strings.HasSuffix(u.Path, "/auth/user") && proxyPath.MatchString(u.EscapedPath()) {
		return "/<platform>/auth/user"
	}
	return "[other_path]"
}
func sameProbeOrigin(a, b *url.URL) bool {
	return a != nil && b != nil && a.User == nil && b.User == nil && strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Host, b.Host)
}

func validateProbeProxy(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "socks5") || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, domain.E("INVALID_PROBE_PROXY", "probe proxy must be a loopback http or socks5 URL without credentials, query or path")
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1 || port > 65535 {
		return nil, domain.E("INVALID_PROBE_PROXY", "probe proxy requires an explicit valid port")
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, domain.E("INVALID_PROBE_PROXY", "probe proxy must use a loopback address")
	}
	u.Host = net.JoinHostPort(ip.String(), strconv.Itoa(port))
	u.Path = ""
	return u, nil
}
func cookieProbeMetadata(snapshot sessionData) []SessionCookieMetadata {
	out := []SessionCookieMetadata{}
	for _, record := range snapshot.Cookies {
		cookie := record.Cookie
		if !probeCookieName.MatchString(cookie.Name) {
			continue
		}
		host := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if host == "" {
			if origin, e := url.Parse(record.URL); e == nil {
				host = strings.ToLower(origin.Hostname())
			}
		}
		if host == "" {
			continue
		}
		entry := SessionCookieMetadata{Name: cookie.Name, Host: host, Session: cookie.Expires.IsZero(), Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly}
		if !cookie.Expires.IsZero() {
			expiry := cookie.Expires.UTC()
			entry.ExpiresAt = &expiry
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].Name < out[j].Name
	})
	return out
}
func clonedProbeJar(snapshot sessionData) *cookieStore {
	jar := newCookies()
	for _, record := range snapshot.Cookies {
		u, e := url.Parse(record.URL)
		if e != nil || u.Host == "" {
			continue
		}
		cookie := record.Cookie
		jar.SetCookies(u, []*http.Cookie{&cookie})
	}
	return jar
}

func (c *Client) ProbeSession(ctx context.Context, proxyURL string) (result SessionProbe, err error) {
	result = SessionProbe{Time: time.Now().UTC(), ProxyScope: "current", Gateway: SessionLayerProbe{State: "not_configured"}, Platform: SessionLayerProbe{State: "unknown"}, CAS: SessionLayerProbe{State: "unknown", Reason: "not_probed_no_side_effect_free_endpoint"}}
	// CAS /login?service=... may issue a service ticket and refresh SSO activity.
	// A cached cookie expiry is not a safe replacement for a server-side probe.
	snapshot := c.cookies.snapshot()
	result.Cookies = cookieProbeMetadata(snapshot)
	jar := clonedProbeJar(snapshot)
	cookieChanged := false
	defer func() { result.CookieChanged = cookieChanged }()
	client := *c.http
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Timeout = 15 * time.Second
	if proxyURL != "" {
		proxy, e := validateProbeProxy(proxyURL)
		if e != nil {
			return result, e
		}
		transport, ok := c.http.Transport.(*http.Transport)
		if !ok {
			return result, domain.E("PROBE_PROXY_UNSUPPORTED", "the configured HTTP transport cannot be safely cloned for a proxy override")
		}
		copy := transport.Clone()
		copy.Proxy = http.ProxyURL(proxy)
		client.Transport = copy
		defer copy.CloseIdleConnections()
		result.ProxyScope = "loopback_override"
	}
	c.mu.Lock()
	expectedUID := c.profile.ExpectedUID
	if expectedUID == "" {
		expectedUID = c.identity.UID
	}
	expectedUsername := c.profile.Username
	if expectedUsername == "" {
		expectedUsername = c.identity.Username
	}
	c.mu.Unlock()
	observation := probeSessionGET(ctx, &client, c.endpoint("/auth/user"), c.base, c.gateway, &cookieChanged)
	if observation.gatewayLogin {
		result.Gateway = classifyGatewayProbe(observation)
		result.Platform = SessionLayerProbe{State: "blocked_by_gateway", HTTPStatus: observation.status, SafeRedirectPath: observation.path, Reason: "gateway_login_required"}
		return result, nil
	}
	result.Platform = classifyPlatformProbe(observation, expectedUID, expectedUsername)
	if c.gateway != nil {
		result.Gateway = classifyGatewayProbe(probeSessionGET(ctx, &client, strings.TrimRight(c.gateway.String(), "/")+"/user/portal/collections", c.base, c.gateway, &cookieChanged))
		if result.Gateway.State == "expired" && result.Platform.State == "unknown" {
			result.Platform.State = "blocked_by_gateway"
			result.Platform.Reason = "gateway_login_required"
		}
	}
	return result, nil
}

func probeSessionGET(ctx context.Context, client *http.Client, target string, platform, gateway *url.URL, changed *bool) sessionProbeObservation {
	u, e := url.Parse(target)
	if e != nil {
		return sessionProbeObservation{reason: "invalid_probe_target"}
	}
	observation := sessionProbeObservation{}
	for hop := 0; hop < 6; hop++ {
		if !sameProbeOrigin(u, platform) && !sameProbeOrigin(u, gateway) {
			return sessionProbeObservation{reason: "redirect_outside_probe_scope", path: safeProbePath(u)}
		}
		req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if e != nil {
			return sessionProbeObservation{reason: "invalid_probe_target"}
		}
		req.Header.Set("Accept", "application/json,text/html;q=0.8")
		req.Header.Set("User-Agent", "bupt-cli-session-probe/1")
		before := probeJarValues(client.Jar)
		response, e := client.Do(req)
		if e != nil {
			return sessionProbeObservation{reason: "network_error"}
		}
		if len(response.Cookies()) > 0 && !sameProbeJarValues(before, probeJarValues(client.Jar)) {
			*changed = true
		}
		observation = sessionProbeObservation{status: response.StatusCode, path: safeProbePath(u), gatewayLogin: sameProbeOrigin(u, gateway) && u.Path == "/login"}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
		response.Body.Close()
		if readErr != nil {
			observation.reason = "response_read_failed"
			return observation
		}
		if len(body) > 2<<20 {
			observation.reason = "response_too_large"
			return observation
		}
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			location := response.Header.Get("Location")
			if location == "" {
				observation.reason = "redirect_target_missing"
				return observation
			}
			next, e := url.Parse(location)
			if e != nil {
				observation.reason = "invalid_redirect"
				return observation
			}
			next = u.ResolveReference(next)
			if next.User != nil {
				observation.reason = "redirect_outside_probe_scope"
				return observation
			}
			if gateway != nil {
				upgradeGatewayHTTP(next, gateway)
			}
			observation.path = safeProbePath(next)
			if next.Scheme == "https" && strings.EqualFold(next.Hostname(), "auth.bupt.edu.cn") && defaultSSOPort(next) && next.Path == "/authserver/login" {
				observation.casRedirect = true
				observation.reason = "university_login_required"
				return observation
			}
			if !sameProbeOrigin(next, platform) && !sameProbeOrigin(next, gateway) {
				observation.reason = "redirect_outside_probe_scope"
				return observation
			}
			u = next
			continue
		}
		observation.body = body
		return observation
	}
	observation.reason = "redirect_limit"
	return observation
}
func classifyGatewayProbe(o sessionProbeObservation) SessionLayerProbe {
	result := SessionLayerProbe{State: "unknown", HTTPStatus: o.status, SafeRedirectPath: o.path, Reason: o.reason}
	if o.reason != "" {
		return result
	}
	diagnostics, reason := parseProbeLoginFlags(o.body)
	result.Diagnostics = diagnostics
	clean := probeScripts.ReplaceAll(o.body, nil)
	clean = probeHTMLComments.ReplaceAll(clean, nil)
	loginDocument := probeLoginInput.Match(clean) && probeLoginQR.Match(clean)
	if o.gatewayLogin || loginDocument {
		result.State = "expired"
		result.Reason = reason
		if result.Reason == "" {
			result.Reason = "login_required"
		}
		return result
	}
	if o.status == 200 && o.path == "/user/portal/collections" {
		var body struct {
			Data    json.RawMessage `json:"data"`
			Success *bool           `json:"success"`
			Error   string          `json:"error"`
		}
		if json.Unmarshal(o.body, &body) == nil {
			if body.Success != nil && !*body.Success || body.Error != "" {
				result.Reason = probeKnownReason(body.Error)
				if result.Reason == "" {
					result.Reason = "gateway_denied"
				}
				return result
			}
			// The live protected endpoint returns {data: []} without csrf.
			// A null/object value or a 200 response from another path is not proof.
			var items []json.RawMessage
			if json.Unmarshal(body.Data, &items) == nil && items != nil {
				result.State = "authenticated"
				result.Reason = "protected_gateway_get"
				return result
			}
		}
	}
	result.Reason = "unrecognized_gateway_response"
	result.ResponseShape = probeResponseShape(o.body)
	return result
}

// Expose schema only, and only recognized API field names. Dynamic keys might
// themselves be account identifiers; count those without returning their text.
func probeResponseShape(body []byte) *SessionResponseShape {
	if !json.Valid(body) {
		kind := "non_json"
		if len(strings.TrimSpace(string(body))) == 0 {
			kind = "empty"
		}
		return &SessionResponseShape{Type: kind}
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return &SessionResponseShape{Type: "non_json"}
	}
	shape := &SessionResponseShape{Type: probeJSONType(value)}
	object, ok := value.(map[string]any)
	if !ok {
		return shape
	}
	known := map[string]bool{}
	for _, name := range strings.Fields("code data csrf _csrf csrfToken success error errors message msg retCode errCode errMsg status result results item items total rows records collections collection list lists resources user username userName uid count") {
		known[name] = true
	}
	for name, field := range object {
		if !known[name] {
			shape.OmittedFields++
			continue
		}
		shape.Fields = append(shape.Fields, SessionResponseField{Name: name, Type: probeJSONType(field)})
	}
	sort.Slice(shape.Fields, func(i, j int) bool { return shape.Fields[i].Name < shape.Fields[j].Name })
	return shape
}
func probeJSONType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	}
	return "unknown"
}

type probePlatformIdentity struct {
	UID      domain.ID `json:"uid"`
	Username string    `json:"username"`
	Uname    string    `json:"uname"`
}

func classifyPlatformProbe(o sessionProbeObservation, expectedUID domain.ID, expectedUsername string) SessionLayerProbe {
	result := SessionLayerProbe{State: "unknown", HTTPStatus: o.status, SafeRedirectPath: o.path, Reason: o.reason}
	if o.casRedirect {
		result.State = "expired"
		return result
	}
	if o.reason != "" {
		return result
	}
	var body struct {
		Code      *int64 `json:"code"`
		RetCode   *int64 `json:"retCode"`
		ErrCode   *int64 `json:"errCode"`
		LoginFrom string `json:"loginFrom"`
		probePlatformIdentity
		Data *probePlatformIdentity `json:"data"`
	}
	if json.Unmarshal(o.body, &body) != nil {
		result.Reason = "unrecognized_platform_response"
		return result
	}
	failed := body.Code != nil && *body.Code != 0 && *body.Code != 1 || body.RetCode != nil && *body.RetCode != 0 || body.ErrCode != nil && *body.ErrCode != 0
	if failed {
		if o.status == 401 || o.status == 200 {
			result.State = "expired"
			result.Reason = "platform_auth_required"
			return result
		}
		result.Reason = "platform_request_denied"
		return result
	}
	identity := body.probePlatformIdentity
	if identity.UID == "" && body.Data != nil {
		identity = *body.Data
	}
	if o.status != 200 || !validIdentityID(identity.UID) {
		result.Reason = "unrecognized_platform_response"
		return result
	}
	if expectedUID != "" && identity.UID != expectedUID {
		result.Reason = "identity_mismatch"
		return result
	}
	name := identity.Username
	if name == "" {
		name = identity.Uname
	}
	if expectedUID == "" && expectedUsername != "" && (name == "" || name != expectedUsername) {
		result.Reason = "identity_unverified"
		return result
	}
	result.State = "authenticated"
	result.Reason = "identity_verified"
	if expectedUID == "" && expectedUsername == "" {
		result.State = "unknown"
		result.Reason = "identity_unpinned"
	}
	return result
}
func probeKnownReason(raw string) string {
	switch raw {
	case "IP_FORBIDDEN":
		return "ip_forbidden"
	case "TOO_MANY_ATTEMPTS":
		return "login_rate_limited"
	case "NEED_CONFIRM":
		return "concurrent_login_confirmation"
	case "INVALID_FAILURETIME":
		return "account_validity_expired"
	case "CAPTCHA_FAILED":
		return "captcha_required"
	case "INVALID_ACCOUNT":
		return "invalid_account"
	}
	return ""
}

type probeJSToken struct {
	kind byte
	text string
}

func probeJSQuoted(s string, start int) (string, int, bool) {
	quote := s[start]
	at := start + 1
	var value strings.Builder
	for at < len(s) {
		if s[at] == quote {
			return value.String(), at + 1, true
		}
		if s[at] == '\n' || s[at] == '\r' {
			return "", at, false
		}
		r, _, tail, e := strconv.UnquoteChar(s[at:], quote)
		if e != nil {
			return "", at, false
		}
		used := len(s[at:]) - len(tail)
		if used <= 0 {
			return "", at, false
		}
		value.WriteRune(r)
		if value.Len() > 4096 {
			return "", at, false
		}
		at += used
	}
	return "", at, false
}
func probeJSTokens(source string) []probeJSToken {
	tokens := []probeJSToken{}
	for at := 0; at < len(source); {
		ch := source[at]
		if ch == '\r' || ch == '\n' {
			tokens = append(tokens, probeJSToken{'n', "\n"})
			at++
			continue
		}
		if ch == ' ' || ch == '\t' {
			at++
			continue
		}
		if ch == '/' && at+1 < len(source) && source[at+1] == '/' {
			for at < len(source) && source[at] != '\n' {
				at++
			}
			continue
		}
		if ch == '/' && at+1 < len(source) && source[at+1] == '*' {
			at += 2
			for at+1 < len(source) && !(source[at] == '*' && source[at+1] == '/') {
				if source[at] == '\n' {
					tokens = append(tokens, probeJSToken{'n', "\n"})
				}
				at++
			}
			at = min(at+2, len(source))
			continue
		}
		if ch == '\'' || ch == '"' {
			value, end, ok := probeJSQuoted(source, at)
			if !ok {
				tokens = append(tokens, probeJSToken{'x', ""})
				at++
				continue
			}
			tokens = append(tokens, probeJSToken{'s', value})
			at = end
			continue
		}
		if ch == '`' {
			at++
			for at < len(source) {
				if source[at] == '\\' {
					at = min(at+2, len(source))
					continue
				}
				if source[at] == '`' {
					at++
					break
				}
				at++
			}
			tokens = append(tokens, probeJSToken{'x', ""})
			continue
		}
		r, size := utf8.DecodeRuneInString(source[at:])
		if unicode.IsLetter(r) || r == '_' || r == '$' {
			start := at
			at += size
			for at < len(source) {
				r, size = utf8.DecodeRuneInString(source[at:])
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '$' {
					break
				}
				at += size
			}
			tokens = append(tokens, probeJSToken{'i', source[start:at]})
			continue
		}
		tokens = append(tokens, probeJSToken{'p', string(ch)})
		at++
	}
	return tokens
}
func parseProbeLoginFlags(body []byte) (SessionProbeDiagnostics, string) {
	values := map[string]string{}
	counts := map[string]int{}
	allowed := map[string]bool{"logoutByOther": true, "logoutByIpChange": true, "needTwoStep": true, "errorMessage": true}
	for _, script := range probeScripts.FindAllSubmatch(body, -1) {
		tokens := probeJSTokens(string(script[1]))
		for i := 0; i+3 < len(tokens); i++ {
			if tokens[i].kind != 'i' || tokens[i].text != "var" && tokens[i].text != "let" && tokens[i].text != "const" {
				continue
			}
			j := i + 1
			for j < len(tokens) && tokens[j].kind == 'n' {
				j++
			}
			if j >= len(tokens) || tokens[j].kind != 'i' || !allowed[tokens[j].text] {
				continue
			}
			name := tokens[j].text
			j++
			for j < len(tokens) && tokens[j].kind == 'n' {
				j++
			}
			if j >= len(tokens) || tokens[j].text != "=" {
				continue
			}
			j++
			for j < len(tokens) && tokens[j].kind == 'n' {
				j++
			}
			if j >= len(tokens) || tokens[j].kind != 's' {
				continue
			}
			value := tokens[j].text
			j++
			literal := j >= len(tokens) || tokens[j].text == ";" || tokens[j].text == "}"
			if !literal && tokens[j].kind == 'n' {
				for j < len(tokens) && tokens[j].kind == 'n' {
					j++
				}
				literal = j >= len(tokens) || !strings.Contains("+-.([?/*%&|^=<>:", tokens[j].text)
			}
			if literal {
				counts[name]++
				values[name] = value
			}
		}
	}
	diagnostics := SessionProbeDiagnostics{Parsed: counts["logoutByOther"] == 1 && counts["logoutByIpChange"] == 1 && counts["needTwoStep"] == 1}
	diagnostics.LogoutByOther = counts["logoutByOther"] == 1 && values["logoutByOther"] != ""
	diagnostics.LogoutByIPChange = counts["logoutByIpChange"] == 1 && values["logoutByIpChange"] != ""
	diagnostics.NeedTwoStep = counts["needTwoStep"] == 1 && values["needTwoStep"] != ""
	reason := ""
	n := 0
	for _, set := range []bool{diagnostics.LogoutByOther, diagnostics.LogoutByIPChange, diagnostics.NeedTwoStep} {
		if set {
			n++
		}
	}
	if n > 1 {
		return diagnostics, "multiple_gateway_reasons"
	}
	switch {
	case diagnostics.LogoutByOther:
		reason = "concurrent_login"
	case diagnostics.LogoutByIPChange:
		reason = "ip_changed"
	case diagnostics.NeedTwoStep:
		reason = "two_factor_required"
	case counts["errorMessage"] == 1:
		reason = probeKnownReason(values["errorMessage"])
	}
	return diagnostics, reason
}

func probeJarValues(jar http.CookieJar) map[string]string {
	values := map[string]string{}
	j, ok := jar.(*cookieStore)
	if !ok {
		return values
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for key, record := range j.records {
		values[key] = record.Cookie.Value
	}
	return values
}
func sameProbeJarValues(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if other, ok := b[key]; !ok || other != value {
			return false
		}
	}
	return true
}
