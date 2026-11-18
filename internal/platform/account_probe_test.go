package platform

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

func newBarProbe(t *testing.T, gateway bool, rt probeRoundTripFunc) (*Client, *probeVault) {
	t.Helper()
	p := domain.Profile{Name: "qa", PlatformURL: "https://gpu.example", PortalURL: "https://portal.example"}
	if gateway {
		p.GatewayURL = "https://vpn.example"
	}
	v := &probeVault{}
	c, err := New(p, v)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	c.http.Transport = rt
	return c, v
}
func accountResponse(r *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}
}
func TestAccountProbeVerifiesLayersWithoutMutatingJarOrVault(t *testing.T) {
	calls := 0
	c, v := newBarProbe(t, true, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Host != "vpn.example" {
			t.Fatal("unexpected request", r.Method, r.URL.Host)
		}
		switch r.URL.Path {
		case "/user/portal/collections":
			cookie, _ := r.Cookie("vpn")
			if cookie == nil || cookie.Value != "original" {
				t.Fatal("missing cookie")
			}
			resp := accountResponse(r, 200, `{"data":[]}`)
			resp.Header.Add("Set-Cookie", "vpn=probe-only; Path=/; Secure")
			return resp, nil
		default:
			if !strings.HasSuffix(r.URL.Path, "/api/me") {
				t.Fatal(r.URL.Path)
			}
			cookie, _ := r.Cookie("vpn")
			if cookie == nil || cookie.Value != "probe-only" {
				t.Fatal("probe cookie rotation lost")
			}
			return accountResponse(r, 200, `{"user":{"sid":"cas_20260001","name":"Student"},"balance":0,"consentRequired":true}`), nil
		}
	})
	u, _ := url.Parse("https://vpn.example")
	c.cookies.SetCookies(u, []*http.Cookie{{Name: "vpn", Value: "original", Path: "/", Secure: true}})
	peer, err := c.PortalClient()
	if err != nil {
		t.Fatal(err)
	}
	if err = v.Put(peer.key("subject"), "cas_20260001"); err != nil {
		t.Fatal(err)
	}
	c.identity = domain.Identity{UID: "7", CampusID: "20260001"}
	before := string(probeMainJarBytes(c))
	writes := v.writes.Load()
	identity := c.identity
	result, err := c.ProbeAccountStatus(t.Context())
	if err != nil || result.VPN != "authenticated" || result.Platform != "authenticated" || !result.ConsentRequired || result.Name != "Student" || result.CampusID != "20260001" || result.Balance == nil || *result.Balance != 0 {
		t.Fatal(result, err)
	}
	if calls != 2 || string(probeMainJarBytes(c)) != before || v.writes.Load() != writes || !reflect.DeepEqual(c.identity, identity) || peer.portalObserved.SID != "" {
		t.Fatal("poll changed live session or made extra requests")
	}
}

func TestAccountProbeStopsAtExpiredOrUnavailableGateway(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		status                          int
		loc, body, vpn, website, reason string
		delay                           time.Duration
	}{
		{"IP change", 200, "", `<input name="auth_type"><div id="wrd-qrcode"></div><script>var logoutByOther='';var logoutByIpChange='1';var needTwoStep='';</script>`, "expired", "blocked_by_gateway", "ip_changed", 0},
		{"gateway login redirect", 302, "/login?ticket=do-not-forward", "", "expired", "blocked_by_gateway", "login_required", 0},
		{"other origin redirect", 302, "https://unrelated.example/redirect", "", "unknown", "unknown", "redirect_outside_probe_scope", 0},
		{"same origin logout redirect", 302, "/logout", "", "unknown", "unknown", "redirect_outside_probe_scope", 0},
		{"limited", 429, "", "", "unknown", "unknown", "rate_limited", 17 * time.Second},
		{"unavailable", 503, "", "", "unknown", "unknown", "service_unavailable", 0},
		{"forbidden", 403, "", "", "unknown", "unknown", "access_denied", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			c, v := newBarProbe(t, true, func(r *http.Request) (*http.Response, error) {
				count++
				if r.URL.Path != "/user/portal/collections" {
					t.Fatal("unexpected redirect", r.URL.Path)
				}
				resp := accountResponse(r, tc.status, tc.body)
				if tc.loc != "" {
					resp.Header.Set("Location", tc.loc)
				}
				if tc.delay != 0 {
					resp.Header.Set("Retry-After", "17")
				}
				return resp, nil
			})
			o, e := c.ProbeAccountStatus(t.Context())
			if e != nil || o.VPN != tc.vpn || o.Platform != tc.website || o.Reason != tc.reason || o.RetryAfter != tc.delay || o.Name != "" || o.CampusID != "" || o.Balance != nil || count != 1 || v.writes.Load() != 0 {
				t.Fatal(o, e, count)
			}
		})
	}
}
func TestAccountProbePortalRequiresBoundSubjectAndDoesNotPerformCAS(t *testing.T) {
	const user = `{"user":{"sid":"cas_20260001","name":"Student"},"balance":4,"consentRequired":false}`
	for _, tc := range []struct {
		name, pin, compute string
		status             int
		loc                string
		body               string
		state, reason      string
	}{
		{"unbound", "", "", 200, "", user, "unknown", "identity_unpinned"},
		{"bound elsewhere", "cas_20269999", "", 200, "", user, "unknown", "identity_mismatch"},
		{"GPU mismatch", "cas_20260001", "20269999", 200, "", user, "unknown", "identity_mismatch"},
		{"GPU bound", "", "20260001", 200, "", user, "authenticated", "identity_verified"},
		{"CAS redirect", "", "", 302, "https://auth.bupt.edu.cn/authserver/login?ticket=opaque", "", "expired", "portal_login_required"},
		{"portal login redirect", "", "", 302, "/api/auth/cas/login", "", "expired", "portal_login_required"},
		{"foreign redirected portal", "", "", 302, "https://other.example/login", "", "unknown", "redirect_outside_probe_scope"},
		{"portal unauthorized", "", "", 401, "", "", "expired", "portal_login_required"},
		{"portal forbidden", "", "", 403, "", "", "unknown", "access_denied"},
		{"bad shape", "", "", 200, "", `{"user":{"sid":"1"}}`, "unknown", "unrecognized_portal_response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			c, v := newBarProbe(t, false, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Host != "portal.example" || r.URL.Path != "/api/me" {
					t.Fatal("unexpected identity, CAS or GPU request", r.URL)
				}
				resp := accountResponse(r, tc.status, tc.body)
				if tc.loc != "" {
					resp.Header.Set("Location", tc.loc)
				}
				return resp, nil
			})
			peer, e := c.PortalClient()
			if e != nil {
				t.Fatal(e)
			}
			if tc.pin != "" {
				if e = v.Put(peer.key("subject"), tc.pin); e != nil {
					t.Fatal(e)
				}
			}
			c.identity.CampusID = tc.compute
			writes := v.writes.Load()
			o, e := c.ProbeAccountStatus(context.Background())
			if e != nil || o.VPN != "not_configured" || o.Platform != tc.state || o.Reason != tc.reason || calls != 1 || v.writes.Load() != writes {
				t.Fatal(o, e, calls)
			}
			if tc.state != "authenticated" && (o.Name != "" || o.CampusID != "" || o.Balance != nil) {
				t.Fatal("unverified private fields were retained")
			}
		})
	}
}

func TestAccountProbeHonorsExistingPortalCooldown(t *testing.T) {
	c, _ := newBarProbe(t, false, func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected school request during cooldown")
		return nil, nil
	})
	portal, err := c.PortalClient()
	if err != nil {
		t.Fatal(err)
	}
	portal.retryPortalAfter = time.Now().Add(time.Minute)
	o, err := c.ProbeAccountStatus(t.Context())
	if err != nil || o.Platform != "unknown" || o.Reason != "rate_limited" || o.RetryAfter <= 0 || o.Balance != nil {
		t.Fatal(o, err)
	}
}

func TestAccountProbeRecognizesOnlyTrustedWrappedCASLogin(t *testing.T) {
	const login = "https://auth.bupt.edu.cn/authserver/login?service=test"
	for _, tc := range []struct{ outer, inner, state string }{
		{"https://vpn.example", login, "expired"},
		{"https://other.example", login, "unknown"},
		{"https://vpn.example", "https://other.example/authserver/login", "unknown"},
		{"https://vpn.example", "https://auth.bupt.edu.cn/authserver/logout", "unknown"},
	} {
		location, err := ProxyURL(tc.outer, tc.inner)
		if err != nil {
			t.Fatal(err)
		}
		calls := 0
		c, vault := newBarProbe(t, true, func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.Path == "/user/portal/collections" {
				return accountResponse(r, 200, `{"data":[]}`), nil
			}
			if !strings.HasSuffix(r.URL.Path, "/api/me") {
				t.Fatalf("unexpected request to %s", r.URL.Path)
			}
			resp := accountResponse(r, http.StatusFound, "")
			resp.Header.Set("Location", location)
			return resp, nil
		})
		got, err := c.ProbeAccountStatus(t.Context())
		if err != nil || got.VPN != "authenticated" || got.Platform != tc.state || calls != 2 || vault.writes.Load() != 0 {
			t.Fatalf("%s via %s: %+v, calls=%d, err=%v", tc.inner, tc.outer, got, calls, err)
		}
	}
}
