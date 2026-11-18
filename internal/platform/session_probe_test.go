package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

type probeVault struct {
	memoryVault
	writes atomic.Int64
}

func (v *probeVault) Put(key string, value any) error {
	v.writes.Add(1)
	return v.memoryVault.Put(key, value)
}
func probeMainJarBytes(c *Client) []byte {
	c.cookies.mu.Lock()
	defer c.cookies.mu.Unlock()
	b, _ := json.Marshal(c.cookies.records)
	return b
}
func probeTestClient(t *testing.T, server *httptest.Server, gateway bool) (*Client, *probeVault) {
	t.Helper()
	v := &probeVault{}
	p := domain.Profile{Name: "probe", PlatformURL: server.URL + "/platform", ExpectedUID: "7"}
	if gateway {
		p.GatewayURL = server.URL
	}
	c, e := New(p, v)
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(server.URL)
	c.cookies.SetCookies(u, []*http.Cookie{{Name: "session", Value: "MAIN_SECRET", Path: "/", HttpOnly: true, Expires: time.Now().Add(time.Hour)}})
	t.Cleanup(func() { c.Close() })
	return c, v
}

func TestProbeClonesJarAndReportsIPReasonWithoutSecrets(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/platform/auth/user":
			cookie, _ := r.Cookie("session")
			if cookie == nil || cookie.Value != "MAIN_SECRET" {
				t.Error("probe did not clone current session")
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "ROTATED_SECRET", Path: "/", HttpOnly: true})
			http.Redirect(w, r, "/login?ticket=REDIRECT_SECRET", 302)
		case "/login":
			cookie, _ := r.Cookie("session")
			if cookie == nil || cookie.Value != "ROTATED_SECRET" {
				t.Error("redirect did not use probe-local cookie rotation")
			}
			_, _ = io.WriteString(w, `<html><script>
var logoutByOther = ''
var logoutByIpChange = '1'
var needTwoStep = ''
var errorMessage = 'SECRET_PERSON_ID'
if (logoutByOther != '') alert('NEED_CONFIRM');
</script></html>`)
		default:
			t.Error("unexpected probe request", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c, v := probeTestClient(t, server, true)
	c.mu.Lock()
	c.identity = domain.Identity{UID: "7", EID: "70"}
	c.qr = domain.AuthChallenge{State: "waiting", URL: "private-challenge"}
	identity, qr := c.identity, c.qr
	c.mu.Unlock()
	before := probeMainJarBytes(c)
	result, e := c.ProbeSession(context.Background(), "")
	if e != nil {
		t.Fatal(e)
	}
	if result.Gateway.State != "expired" || result.Platform.State != "blocked_by_gateway" || !result.Gateway.Diagnostics.LogoutByIPChange || !result.Gateway.Diagnostics.Parsed || result.Gateway.Reason != "ip_changed" || result.Gateway.SafeRedirectPath != "/login" || !result.CookieChanged {
		t.Fatalf("wrong classification: %+v", result)
	}
	if string(before) != string(probeMainJarBytes(c)) {
		t.Fatal("probe mutated the main cookie jar")
	}
	if v.writes.Load() != 0 {
		t.Fatal("probe wrote the vault")
	}
	c.mu.Lock()
	if !reflect.DeepEqual(identity, c.identity) || !reflect.DeepEqual(qr, c.qr) {
		t.Error("probe altered identity or login state")
	}
	c.mu.Unlock()
	data, _ := json.Marshal(result)
	for _, secret := range []string{"MAIN_SECRET", "ROTATED_SECRET", "REDIRECT_SECRET", "SECRET_PERSON_ID", "private-challenge", "ticket=", "\"uid\""} {
		if strings.Contains(string(data), secret) {
			t.Error("probe exposed private material")
		}
	}
	if result.Time.Location() != time.UTC || len(result.Cookies) != 1 || result.Cookies[0].ExpiresAt == nil || !result.Cookies[0].HTTPOnly {
		t.Fatal("cookie metadata or UTC timestamp is incomplete")
	}
	if calls != 2 {
		t.Fatal("unexpected extra probe calls", calls)
	}
}

func TestProbePlatformIdentityAcceptsZeroOrOneOnlyWithPin(t *testing.T) {
	for _, tc := range []struct {
		body          string
		state, reason string
	}{{`{"code":1,"uid":7,"loginFrom":"sso","userState":"active"}`, "authenticated", "identity_verified"}, {`{"code":0,"uid":7}`, "authenticated", "identity_verified"}, {`{"code":1,"uid":8}`, "unknown", "identity_mismatch"}, {`{"code":1}`, "unknown", "unrecognized_platform_response"}, {`{"code":1,"uid":0}`, "unknown", "unrecognized_platform_response"}, {`{"code":1032,"uid":7}`, "expired", "platform_auth_required"}, {`{"code":1,"uid":7,"retCode":2}`, "expired", "platform_auth_required"}, {`{"code":1,"uid":7,"errCode":1}`, "expired", "platform_auth_required"}} {
		got := classifyPlatformProbe(sessionProbeObservation{status: 200, body: []byte(tc.body)}, "7", "")
		if got.State != tc.state || got.Reason != tc.reason {
			t.Fatalf("classification %s: %+v", tc.body, got)
		}
	}
}

func TestProbeLoginFlagsRequireDeclarationsAndLiteralValues(t *testing.T) {
	cases := []struct {
		body                   string
		other, ip, two, parsed bool
		reason                 string
	}{{`<script>var logoutByOther='yes';var logoutByIpChange='';var needTwoStep='';</script>`, true, false, false, true, "concurrent_login"}, {`<script>var logoutByOther='';var logoutByIpChange='';var needTwoStep='yes';</script>`, false, false, true, true, "two_factor_required"}, {`<script>var sample="var logoutByOther='yes';";/* var logoutByIpChange='yes'; */
var logoutByOther='';var logoutByIpChange='';var needTwoStep='';if(x) alert('您的账号IP发生变化 NEED_CONFIRM');</script>`, false, false, false, true, ""}, {`<script>var logoutByOther='yes'+getValue();var logoutByIpChange='';var needTwoStep='';</script>`, false, false, false, false, ""}, {`<script>var logoutByOther=someFunction();var logoutByIpChange='';var needTwoStep='';</script>`, false, false, false, false, ""}, {`<script>var logoutByOther='';var logoutByIpChange='';var needTwoStep='';var errorMessage='INVALID_FAILURETIME';</script>`, false, false, false, true, "account_validity_expired"}}
	for _, tc := range cases {
		got, reason := parseProbeLoginFlags([]byte(tc.body))
		if got.LogoutByOther != tc.other || got.LogoutByIPChange != tc.ip || got.NeedTwoStep != tc.two || got.Parsed != tc.parsed || reason != tc.reason {
			t.Fatalf("literal diagnostic mismatch: %+v %s", got, reason)
		}
	}
}

func TestProbeProxyOverrideIsIsolatedAndLoopbackOnly(t *testing.T) {
	var direct, alternate atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		direct.Add(1)
		writeJSON(w, map[string]any{"code": 1, "uid": 7})
	}))
	defer target.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		alternate.Add(1)
		if !r.URL.IsAbs() {
			t.Error("request was not routed through the explicit HTTP proxy")
		}
		writeJSON(w, map[string]any{"code": 1, "uid": 8})
	}))
	defer proxy.Close()
	c, v := probeTestClient(t, target, false)
	originalTransport := c.http.Transport
	before := probeMainJarBytes(c)
	first, e := c.ProbeSession(context.Background(), "")
	if e != nil || first.Platform.State != "authenticated" {
		t.Fatal(first, e)
	}
	second, e := c.ProbeSession(context.Background(), proxy.URL)
	if e != nil || second.ProxyScope != "loopback_override" || second.Platform.Reason != "identity_mismatch" {
		t.Fatal(second, e)
	}
	third, e := c.ProbeSession(context.Background(), "")
	if e != nil || third.Platform.State != "authenticated" || third.ProxyScope != "current" {
		t.Fatal(third, e)
	}
	if c.http.Transport != originalTransport || string(before) != string(probeMainJarBytes(c)) || v.writes.Load() != 0 || direct.Load() != 2 || alternate.Load() != 1 {
		t.Fatal("proxy override affected main state or routing")
	}
	for _, raw := range []string{"http://example.com:1080", "socks5://example.com:1080", "http://127.0.0.1:1080/path", "http://127.0.0.1:1080?token=SECRET", "http://user:SECRET@127.0.0.1:1080", "https://127.0.0.1:1080", "file:///tmp/proxy", "http://127.0.0.1", "socks5://[::1]:99999"} {
		if _, e = c.ProbeSession(context.Background(), raw); e == nil {
			t.Error("unsafe proxy accepted", raw)
		} else if strings.Contains(e.Error(), "SECRET") {
			t.Fatal("proxy error exposed credentials")
		}
	}
	for _, raw := range []string{"http://127.0.0.1:1080", "socks5://[::1]:1080", "socks5://localhost:1080"} {
		if _, e := validateProbeProxy(raw); e != nil {
			t.Fatal("valid loopback proxy rejected", e)
		}
	}
}

type probeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f probeRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestProbeDoesNotFollowCASOrRefreshAuthentication(t *testing.T) {
	c, e := New(domain.Profile{Name: "p", PlatformURL: "https://platform.example", ExpectedUID: "7"}, &probeVault{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	calls := 0
	c.http.Transport = probeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "platform.example" || r.Method != "GET" {
			t.Error("probe contacted authentication service")
		}
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://auth.bupt.edu.cn/authserver/login?service=PRIVATE_SERVICE"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	result, e := c.ProbeSession(context.Background(), "")
	if e != nil || result.Platform.State != "expired" || result.CAS.State != "unknown" || calls != 1 {
		t.Fatal(result, e)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "PRIVATE_SERVICE") {
		t.Fatal("CAS redirect query leaked")
	}
}

func TestProbeUnknownGatewayReturnsSchemaWithoutValuesOrDynamicKeys(t *testing.T) {
	body := []byte(`{"collections":[],"csrf":"PRIVATE_CSRF_VALUE","code":0,"message":"PRIVATE_ACCOUNT_MESSAGE","700001":{"email":"PRIVATE_EMAIL"},"PRIVATE_DYNAMIC_KEY":"PRIVATE_VALUE"}`)
	result := classifyGatewayProbe(sessionProbeObservation{status: 200, path: "/user/portal/collections", body: body})
	if result.State != "unknown" || result.Reason != "unrecognized_gateway_response" || result.ResponseShape == nil || result.ResponseShape.Type != "object" || result.ResponseShape.OmittedFields != 2 {
		t.Fatalf("unexpected schema classification: %+v", result)
	}
	want := []SessionResponseField{{Name: "code", Type: "number"}, {Name: "collections", Type: "array"}, {Name: "csrf", Type: "string"}, {Name: "message", Type: "string"}}
	if !reflect.DeepEqual(result.ResponseShape.Fields, want) {
		t.Fatal("schema fields differ", result.ResponseShape.Fields)
	}
	encoded, _ := json.Marshal(result)
	for _, private := range []string{"PRIVATE_", "700001"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("shape exposed values or dynamic identifiers")
		}
	}
	array := classifyGatewayProbe(sessionProbeObservation{status: 200, path: "/user/portal/collections", body: []byte(`[{"id":"PRIVATE_ID"}]`)})
	if array.State != "unknown" || array.ResponseShape.Type != "array" {
		t.Fatal("unknown array was promoted to authenticated")
	}
}

func TestProbeGatewayCollectionsExactAuthenticatedEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		status           int
		authenticated    bool
	}{
		{"live empty collections", "/user/portal/collections", `{"data":[]}`, 200, true},
		{"nonempty collections", "/user/portal/collections", `{"data":[{"name":"private favorite"}]}`, 200, true},
		{"explicit success", "/user/portal/collections", `{"success":true,"data":[]}`, 200, true},
		{"denied", "/user/portal/collections", `{"success":false,"data":[]}`, 200, false},
		{"error", "/user/portal/collections", `{"error":"NEED_CONFIRM","data":[]}`, 200, false},
		{"structured error", "/user/portal/collections", `{"error":{"message":"denied"},"data":[]}`, 200, false},
		{"null data", "/user/portal/collections", `{"data":null}`, 200, false},
		{"object data", "/user/portal/collections", `{"data":{}}`, 200, false},
		{"string data", "/user/portal/collections", `{"data":"[]"}`, 200, false},
		{"top level array", "/user/portal/collections", `[]`, 200, false},
		{"malformed", "/user/portal/collections", `{"data":[`, 200, false},
		{"other protected path", "/", `{"csrf":"value","data":[]}`, 200, false},
		{"error status", "/user/portal/collections", `{"data":[]}`, 403, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyGatewayProbe(sessionProbeObservation{status: tc.status, path: tc.path, body: []byte(tc.body)})
			if (got.State == "authenticated") != tc.authenticated {
				t.Fatalf("unexpected classification: %+v", got)
			}
		})
	}
}
