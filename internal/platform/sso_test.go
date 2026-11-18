package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/coder/websocket"
)

type ssoFixture struct {
	t                 *testing.T
	server            *httptest.Server
	mu                sync.Mutex
	confirm           chan struct{}
	providerClosed    chan struct{}
	closeOnce         sync.Once
	redirects         int
	stateGets         int
	callbackGets      int
	foreignRequests   int
	foreignCallback   bool
	blockCallback     bool
	callbackEntered   chan struct{}
	uid               string
	service           string
	login             string
	proxyPrefix       string
	preApprovalEvents [][]byte
	passwordCAS       http.HandlerFunc
}

func newSSOFixture(t *testing.T) *ssoFixture {
	t.Helper()
	f := &ssoFixture{t: t, confirm: make(chan struct{}), providerClosed: make(chan struct{}), uid: "7", service: "http://gpuhub.bupt.edu.cn/auth/cas/callback?application=gpu"}
	f.login = casOrigin + "/authserver/login?" + url.Values{"service": []string{f.service}}.Encode()
	mapped, _ := ProxyURL("https://webvpn.bupt.edu.cn", "https://gpuhub.bupt.edu.cn")
	u, _ := url.Parse(mapped)
	f.proxyPrefix = u.Path
	f.server = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}
func (f *ssoFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil {
		f.t.Error("SSO transport was not TLS")
	}
	switch r.Host {
	case "auth.bupt.edu.cn":
		if _, e := r.Cookie("VPN_SESSION"); e == nil {
			f.t.Error("gateway cookie leaked to CAS")
		}
		if f.passwordCAS != nil {
			f.passwordCAS(w, r)
			return
		}
		if r.URL.Path == "/authserver/clientredirect" {
			f.mu.Lock()
			f.stateGets++
			f.mu.Unlock()
			if r.URL.Query().Get("service") != f.service || r.URL.Query().Get("client_name") != "mc-qr" || r.URL.Query().Get("javax.faces.partial.ajax") != "true" {
				f.t.Error("wrong CAS state request")
			}
			if _, e := r.Cookie("CAS_SESSION"); e != nil {
				f.t.Error("CAS state request lost its cookie")
			}
			w.Header().Set("Content-Type", "text/xml")
			_, _ = io.WriteString(w, `<partial-response><redirect url="https://app.bupt.edu.cn/site/oauth/authorize?state=private-cas-state&amp;client=public-app"/></partial-response>`)
			return
		}
		if r.URL.Path != "/authserver/login" {
			f.t.Error("unexpected CAS path", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		if r.URL.Query().Get("client_name") == "mc-qr" {
			f.mu.Lock()
			f.callbackGets++
			foreign := f.foreignCallback
			block := f.blockCallback
			entered := f.callbackEntered
			f.mu.Unlock()
			if block {
				close(entered)
				<-r.Context().Done()
				return
			}
			if r.URL.Query().Get("code") != "private-approved-code" || r.URL.Query().Get("state") != "private-cas-state" {
				f.t.Error("callback lost approval fields")
			}
			if _, e := r.Cookie("CAS_SESSION"); e != nil {
				f.t.Error("callback lost CAS session")
			}
			if foreign {
				http.Redirect(w, r, "https://outside.example/steal?ticket=private-st", 302)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "TGC", Value: "private-cas-tgc", Path: "/authserver", Secure: true, HttpOnly: true})
			http.Redirect(w, r, f.service+"&ticket=private-st", 302)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "CAS_SESSION", Value: "private-cas-session", Path: "/authserver", Secure: true, HttpOnly: true})
		_, _ = io.WriteString(w, `<html><script>config.mobileCampus={baseHost:'app.bupt.edu.cn',security:true,baseUrl:'https://app.bupt.edu.cn',appid:'public-app',title:'移动校园'};</script></html>`)
	case "app.bupt.edu.cn":
		if r.URL.Path != "/sock/qrcode" || r.URL.Query().Get("appid") != "public-app" {
			f.t.Error("wrong QR provider endpoint")
		}
		if r.Header.Get("Origin") != casOrigin {
			f.t.Error("wrong QR websocket Origin")
		}
		if _, e := r.Cookie("CAS_SESSION"); e == nil {
			f.t.Error("CAS cookie leaked to QR provider")
		}
		if _, e := r.Cookie("VPN_SESSION"); e == nil {
			f.t.Error("gateway cookie leaked to QR provider")
		}
		conn, e := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"auth.bupt.edu.cn"}})
		if e != nil {
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		closed := make(chan struct{})
		go func() {
			defer close(closed)
			for {
				if _, _, e := conn.Read(ctx); e != nil {
					f.closeOnce.Do(func() { close(f.providerClosed) })
					return
				}
			}
		}()
		b, _ := json.Marshal(map[string]any{"event": "ticket", "data": map[string]any{"ticket": "private-ticket", "expire": "120"}})
		_ = conn.Write(ctx, websocket.MessageText, b)
		for _, event := range f.preApprovalEvents {
			_ = conn.Write(ctx, websocket.MessageText, event)
		}
		select {
		case <-f.confirm:
			b, _ := json.Marshal(map[string]any{"event": "confirm", "data": map[string]any{"code": "private-approved-code"}})
			_ = conn.Write(ctx, websocket.MessageText, b)
			<-closed
		case <-closed:
		}
	case "webvpn.bupt.edu.cn":
		if _, e := r.Cookie("CAS_SESSION"); e == nil {
			f.t.Error("CAS cookie copied into rewritten platform callback")
		}
		if _, e := r.Cookie("TGC"); e == nil {
			f.t.Error("CAS TGC copied into platform callback")
		}
		if _, e := r.Cookie("VPN_SESSION"); e != nil {
			f.t.Error("rewritten request did not attach gateway cookie")
		}
		m := proxyPath.FindStringSubmatch(r.URL.Path)
		if len(m) == 0 {
			f.t.Error("platform request not rewritten through WebVPN")
			w.WriteHeader(404)
			return
		}
		switch m[4] {
		case "/auth/user":
			if _, e := r.Cookie("PLATFORM_SESSION"); e != nil {
				w.WriteHeader(401)
				writeJSON(w, map[string]any{"code": 1032, "loginFrom": "sso", "ssoUrl": f.login})
				return
			}
			f.mu.Lock()
			uid := f.uid
			f.mu.Unlock()
			writeJSON(w, map[string]any{"code": 1, "uid": uid, "uname": "student", "eid": 100, "userState": "active"})
		case "/auth/cas/callback":
			if m[1] != "http" || r.URL.Query().Get("ticket") != "private-st" {
				f.t.Error("rewriting changed native service scheme or ticket")
			}
			f.mu.Lock()
			f.redirects++
			f.mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "PLATFORM_SESSION", Value: "private-platform-session", Path: f.proxyPrefix + "/", Secure: true, HttpOnly: true})
			http.Redirect(w, r, f.proxyPrefix+"/", 302)
		case "/":
			_, _ = io.WriteString(w, "platform home")
		default:
			f.t.Error("unexpected proxied platform path", m[4])
			w.WriteHeader(404)
		}
	default:
		f.mu.Lock()
		f.foreignRequests++
		f.mu.Unlock()
		f.t.Error("SSO reached an unapproved host")
		w.WriteHeader(403)
	}
}
func (f *ssoFixture) client(t *testing.T, v *memoryVault, expected string) *Client {
	t.Helper()
	p := domain.Profile{Name: "sso", PlatformURL: "https://gpuhub.bupt.edu.cn", GatewayURL: "https://webvpn.bupt.edu.cn", ExpectedUID: domain.ID(expected)}
	c, e := New(p, v)
	if e != nil {
		t.Fatal(e)
	}
	transport := f.server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, f.server.Listener.Addr().String())
	}
	c.http.Transport = transport
	g, _ := url.Parse("https://webvpn.bupt.edu.cn/")
	if len(c.cookies.Cookies(g)) == 0 {
		c.cookies.SetCookies(g, []*http.Cookie{{Name: "VPN_SESSION", Value: "private-gateway-session", Path: "/", Secure: true, HttpOnly: true}})
	}
	_ = v.Put(c.key("sso_url"), f.login)
	t.Cleanup(func() { c.Close() })
	return c
}
func waitSSOCode(t *testing.T, c *Client) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		s := c.sso
		c.mu.Unlock()
		if s != nil && s.snapshot().Code != "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("approval code was not persisted")
}
func waitSSOPoll(t *testing.T, c *Client) (domain.AuthChallenge, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for {
		challenge, e := c.SSOPoll(ctx)
		if e != nil || challenge.State == "authenticated" || challenge.State == "refresh_required" {
			return challenge, e
		}
		select {
		case <-ctx.Done():
			return challenge, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestSSOQRSurvivesRPCContextAndRewritesCookiesBeforeCallback(t *testing.T) {
	f := newSSOFixture(t)
	v := &memoryVault{}
	c := f.client(t, v, "7")
	startCtx, cancel := context.WithCancel(context.Background())
	challenge, e := c.SSOStart(startCtx)
	if e != nil {
		t.Fatal(e)
	}
	if challenge.Kind != "bupt_sso_qr" || challenge.Stage != "university_sso" || challenge.State != "waiting" || challenge.ID != "" || challenge.URL != campusOrigin+"/site/smlogin/index?ticket=private-ticket" {
		t.Fatalf("unexpected QR challenge %+v", challenge)
	}
	cancel()
	select {
	case <-f.providerClosed:
		t.Fatal("RPC cancellation closed provider socket")
	case <-time.After(30 * time.Millisecond):
	}
	close(f.confirm)
	challenge, e = waitSSOPoll(t, c)
	if e != nil || challenge.State != "authenticated" || challenge.URL != "" {
		t.Fatal(challenge, e)
	}
	i, e := c.WhoAmI(context.Background())
	if e != nil || i.UID != "7" {
		t.Fatal(i, e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redirects != 1 || f.stateGets != 1 || f.callbackGets != 1 || f.foreignRequests != 0 {
		t.Fatal("incorrect CAS handshake counts")
	}
	var saved savedSSO
	if e = v.Get(c.key(ssoStateKey), &saved); e != nil || saved.Code != "" || saved.CASState != "" {
		t.Fatal("completed SSO retained approval secrets")
	}
}

func TestSSOScanEventsAcceptDifferentPayloadShapesAndKeepSocketThroughApproval(t *testing.T) {
	f := newSSOFixture(t)
	f.preApprovalEvents = [][]byte{
		[]byte(`{"event":"scan","data":"phone opened the QR"}`),
		[]byte(`{"event":"notification","data":["scanned",1,true]}`),
		[]byte(`{"event":"keepalive","data":7}`),
		[]byte(`{"event":"notification","data":null}`),
	}
	c := f.client(t, &memoryVault{}, "7")
	ctx, cancel := context.WithCancel(context.Background())
	if _, e := c.SSOStart(ctx); e != nil {
		t.Fatal(e)
	}
	cancel()
	select {
	case <-f.providerClosed:
		t.Fatal("scan notification closed the QR connection")
	case <-time.After(50 * time.Millisecond):
	}
	challenge, e := c.SSOPoll(context.Background())
	if e != nil || challenge.State != "waiting" {
		t.Fatalf("scan changed login state: %+v %v", challenge, e)
	}
	close(f.confirm)
	waitSSOCode(t, c)
	select {
	case <-f.providerClosed:
		t.Fatal("approval closed the QR connection before CAS completed")
	case <-time.After(50 * time.Millisecond):
	}
	challenge, e = waitSSOPoll(t, c)
	if e != nil || challenge.State != "authenticated" {
		t.Fatal(challenge, e)
	}
	select {
	case <-f.providerClosed:
	case <-time.After(time.Second):
		t.Fatal("completed login retained provider connection")
	}
}

func TestSSOConfirmedApprovalResumesAfterControllerRestart(t *testing.T) {
	f := newSSOFixture(t)
	v := &memoryVault{}
	first := f.client(t, v, "7")
	if _, e := first.SSOStart(context.Background()); e != nil {
		t.Fatal(e)
	}
	close(f.confirm)
	waitSSOCode(t, first)
	first.Close()
	restored := f.client(t, v, "7")
	challenge, e := waitSSOPoll(t, restored)
	if e != nil || challenge.State != "authenticated" {
		t.Fatal(challenge, e)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stateGets != 1 || f.callbackGets != 1 {
		t.Fatal("restart duplicated the CAS exchange")
	}
}

func TestSSOPendingSocketDoesNotPretendToResumeAfterRestart(t *testing.T) {
	f := newSSOFixture(t)
	v := &memoryVault{}
	first := f.client(t, v, "7")
	if _, e := first.SSOStart(context.Background()); e != nil {
		t.Fatal(e)
	}
	first.Close()
	restored := f.client(t, v, "7")
	challenge, e := restored.SSOPoll(context.Background())
	if e != nil || challenge.State != "refresh_required" || challenge.URL != "" {
		t.Fatal("pending socket was falsely restored", challenge, e)
	}
}

func TestSSORejectsForeignCallbacksAndIdentityMismatch(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(fmt.Sprint(mismatch), func(t *testing.T) {
			f := newSSOFixture(t)
			f.foreignCallback = !mismatch
			v := &memoryVault{}
			expected := "7"
			if mismatch {
				expected = "8"
			}
			c := f.client(t, v, expected)
			if _, e := c.SSOStart(context.Background()); e != nil {
				t.Fatal(e)
			}
			close(f.confirm)
			_, e := waitSSOPoll(t, c)
			want := "SSO_REDIRECT_DENIED"
			if mismatch {
				want = "IDENTITY_MISMATCH"
			}
			assertCode(t, e, want)
			if strings.Contains(e.Error(), "private-") {
				t.Fatal("SSO error exposed authorization material")
			}
			var saved savedSSO
			_ = v.Get(c.key(ssoStateKey), &saved)
			if saved.Challenge.State == "authenticated" {
				t.Fatal("unverified identity marked authenticated")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.foreignRequests != 0 {
				t.Fatal("foreign callback was contacted")
			}
		})
	}
}

func TestSSOProviderAndStateValidation(t *testing.T) {
	for _, config := range []string{`config.mobileCampus={baseHost:'evil.example',security:true,baseUrl:'https://evil.example',appid:'app'};`, `config.mobileCampus={baseHost:'app.bupt.edu.cn',security:false,baseUrl:'http://app.bupt.edu.cn',appid:'app'};`} {
		if _, e := campusAppID([]byte(config)); managedSSOCode(e) != "SSO_PROVIDER_UNSUPPORTED" {
			t.Fatal(e)
		}
	}
	p := &ssoPolicy{native: &url.URL{Scheme: "https", Host: "gpuhub.bupt.edu.cn"}, gateway: &url.URL{Scheme: "https", Host: "webvpn.bupt.edu.cn"}}
	for _, body := range []string{`<partial-response><redirect url="https://outside.example/?state=secret"/></partial-response>`, `<partial-response><redirect url="https://app.bupt.edu.cn/site/oauth/authorize"/></partial-response>`, `<!DOCTYPE x [<!ENTITY a SYSTEM "file:///etc/passwd">]><redirect url="&a;"/>`} {
		if _, e := stateFromCASXML([]byte(body), p); e == nil {
			t.Fatal("unsafe CAS XML accepted")
		}
	}
	u, _ := url.Parse("https://outside.example/?secret=private")
	if _, _, e := p.route(u); managedSSOCode(e) != "SSO_REDIRECT_DENIED" {
		t.Fatal(e)
	}
	insecure := &ssoPolicy{native: &url.URL{Scheme: "https", Host: "gpuhub.bupt.edu.cn"}}
	u, _ = url.Parse("http://gpuhub.bupt.edu.cn/callback?ticket=secret")
	if _, _, e := insecure.route(u); managedSSOCode(e) != "SSO_TLS_REQUIRED" {
		t.Fatal(e)
	}
}
func managedSSOCode(e error) string {
	if d, ok := e.(*domain.Error); ok {
		return d.Code
	}
	return ""
}

func TestSSOLogoutClearsPersistedApproval(t *testing.T) {
	f := newSSOFixture(t)
	v := &memoryVault{}
	c := f.client(t, v, "7")
	if _, e := c.SSOStart(context.Background()); e != nil {
		t.Fatal(e)
	}
	close(f.confirm)
	waitSSOCode(t, c)
	if e := c.CancelSSO(); e != nil {
		t.Fatal(e)
	}
	if _, e := c.SSOPoll(context.Background()); managedSSOCode(e) != "NO_SSO_CHALLENGE" {
		t.Fatal("logout left usable SSO approval", e)
	}
}

func TestSSOCancelJoinsInFlightCallbackBeforeReturning(t *testing.T) {
	f := newSSOFixture(t)
	f.mu.Lock()
	f.blockCallback = true
	f.callbackEntered = make(chan struct{})
	entered := f.callbackEntered
	f.mu.Unlock()
	v := &memoryVault{}
	c := f.client(t, v, "7")
	if _, e := c.SSOStart(context.Background()); e != nil {
		t.Fatal(e)
	}
	close(f.confirm)
	waitSSOCode(t, c)
	pollDone := make(chan error, 1)
	go func() { _, e := c.SSOPoll(context.Background()); pollDone <- e }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("CAS callback did not start")
	}
	cancelDone := make(chan error, 1)
	go func() { cancelDone <- c.CancelSSO() }()
	select {
	case e := <-cancelDone:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSO cancellation did not join callback")
	}
	select {
	case e := <-pollDone:
		if e == nil {
			t.Fatal("cancelled callback reported success")
		}
	case <-time.After(time.Second):
		t.Fatal("callback still running after cancellation returned")
	}
	var saved savedSSO
	if e := v.Get(c.key(ssoStateKey), &saved); e == nil {
		t.Fatal("logout retained usable approval")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.redirects != 0 {
		t.Fatal("cancelled callback completed after logout")
	}
}

func TestSSONormalExpiryReturnsChallengeStateInsteadOfCancellation(t *testing.T) {
	f := newSSOFixture(t)
	c := f.client(t, &memoryVault{}, "7")
	if _, e := c.SSOStart(context.Background()); e != nil {
		t.Fatal(e)
	}
	c.mu.Lock()
	session := c.sso
	c.mu.Unlock()
	session.mu.Lock()
	session.saved.Challenge.ExpiresAt = time.Now().Add(-time.Second)
	_ = session.persistLocked()
	session.mu.Unlock()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		challenge, e := c.SSOPoll(context.Background())
		if e != nil {
			t.Fatalf("normal expiry became an error: %v", e)
		}
		if challenge.State == "expired" || challenge.State == "refresh_required" {
			if challenge.URL != "" || challenge.RequiredAction != "scan_new_university_qr" {
				t.Fatal("expired challenge remained usable", challenge)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("provider challenge did not expire")
}
