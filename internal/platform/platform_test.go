package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/coder/websocket"
)

type memoryVault struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (v *memoryVault) Get(k string, out any) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.m[k]
	if !ok {
		return os.ErrNotExist
	}
	return json.Unmarshal(b, out)
}
func (v *memoryVault) Put(k string, in any) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil {
		v.m = map[string][]byte{}
	}
	b, e := json.Marshal(in)
	v.m[k] = b
	return e
}
func (v *memoryVault) Delete(k string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, k)
	return nil
}
func testClient(t *testing.T, s *httptest.Server) *Client {
	t.Helper()
	c, e := New(domain.Profile{Name: "test", PlatformURL: s.URL}, &memoryVault{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func assertCode(t *testing.T, e error, code string) {
	t.Helper()
	var d *domain.Error
	if !errors.As(e, &d) || d.Code != code {
		t.Fatalf("want code %s; got %v", code, e)
	}
}

func TestPasswordBrowserCodecKnownVector(t *testing.T) {
	salt := make([]byte, 32)
	for i := range salt {
		salt[i] = byte(i)
	}
	got, e := encodePassword("测试-password", bytes.NewReader(salt))
	if e != nil {
		t.Fatal(e)
	}
	want := "UXYwb/6TAQBlCnlJhrXyCQ==000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	if got != want {
		t.Fatalf("codec mismatch: %q", got)
	}
	if _, e = encodePassword("x", strings.NewReader("")); e == nil {
		t.Fatal("accepted short randomness")
	}
}

func TestWebVPNProxyKnownHostnameAndWebSocketProtocol(t *testing.T) {
	const gateway = "https://webvpn.bupt.edu.cn"
	const encrypted = "77726476706e69737468656265737421f7e75494323226526b189de29d51367b1232"
	got, e := ProxyURL(gateway, "https://gpuhub.bupt.edu.cn/auth/user?x=1")
	want := gateway + "/https/" + encrypted + "/auth/user?x=1"
	if e != nil || got != want {
		t.Fatalf("proxy URL: %s %v", got, e)
	}
	again, e := ProxyURL(gateway, got)
	if e != nil || again != got {
		t.Fatal("double-proxied an existing URL", again, e)
	}
	ws, e := ProxyURL(gateway, "wss://gpuhub.bupt.edu.cn/socket.io/?EIO=3&transport=websocket")
	if e != nil || ws != "wss://webvpn.bupt.edu.cn/wss/"+encrypted+"/socket.io/?EIO=3&transport=websocket" {
		t.Fatal(ws, e)
	}
	c, e := New(domain.Profile{Name: "vpn", PlatformURL: "https://gpuhub.bupt.edu.cn", GatewayURL: gateway}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	u, e := c.websocketEndpoint("/proxy/service/ff/term")
	if e != nil || u.String() != "wss://webvpn.bupt.edu.cn/wss/"+encrypted+"/proxy/service/ff/term" {
		t.Fatal(u, e)
	}
	pre, e := New(domain.Profile{Name: "pre", PlatformURL: gateway + "/https/" + encrypted, GatewayURL: gateway}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer pre.Close()
	u, e = pre.websocketEndpoint("/socket.io/")
	if e != nil || !strings.Contains(u.String(), "/wss/"+encrypted+"/socket.io/") {
		t.Fatal(u, e)
	}
	port, e := ProxyURL(gateway, "http://gpuhub.bupt.edu.cn:8080/a%20b")
	if e != nil || port != gateway+"/http-8080/"+encrypted+"/a%20b" {
		t.Fatal(port, e)
	}
}

func TestAuthCookiesPersistenceIdentityAndPasswordSetup(t *testing.T) {
	var mu sync.Mutex
	var reset map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login":
			var p map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p["username"] != "student" || p["password"] == "new-secret" || len(p["password"]) < 65 {
				t.Error("login codec was not used")
			}
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "test-session", Path: "/", HttpOnly: true})
			writeJSON(w, map[string]any{"retCode": 0})
		case "/auth/user":
			if _, e := r.Cookie("session"); e != nil {
				w.WriteHeader(401)
				return
			}
			writeJSON(w, map[string]any{"uid": json.Number("9007199254740993"), "eid": 101, "uname": "student", "displayName": "Student", "passwordSha256": "must-not-escape"})
		case "/auth/reset-user-password":
			mu.Lock()
			_ = json.NewDecoder(r.Body).Decode(&reset)
			mu.Unlock()
			writeJSON(w, map[string]any{"retCode": 0})
		case "/auth/logout":
			writeJSON(w, map[string]any{"retCode": 0})
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	v := &memoryVault{}
	profile := domain.Profile{Name: "test", PlatformURL: s.URL, ExpectedUID: "9007199254740993"}
	c, _ := New(profile, v)
	i, e := c.Login(context.Background(), "student", "new-secret")
	if e != nil || i.UID != "9007199254740993" {
		t.Fatalf("login %v %v", i, e)
	}
	_ = c.Close()
	c2, _ := New(profile, v)
	defer c2.Close()
	if _, e = c2.WhoAmI(context.Background()); e != nil {
		t.Fatal("session cookie not restored", e)
	}
	if e = c2.SetupPassword(context.Background(), " changed ", " old "); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	if reset["password"] != "changed" || reset["oldPassword"] != "old" || len(reset) != 2 {
		t.Error("reset payload must be plain trimmed values without initialize", reset)
	}
	mu.Unlock()
	profile.ExpectedUID = "7"
	wrong, _ := New(profile, v)
	_, e = wrong.WhoAmI(context.Background())
	assertCode(t, e, "IDENTITY_MISMATCH")
	wrong.Close()
	if e = c2.Logout(context.Background()); e != nil {
		t.Fatal(e)
	}
	c3, _ := New(profile, v)
	defer c3.Close()
	_, e = c3.WhoAmI(context.Background())
	assertCode(t, e, "AUTH_EXPIRED")
}

func TestGatewayQRSessionAndRedirectAllowlist(t *testing.T) {
	var target string
	var completions int
	var mu sync.Mutex
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "vpn-pre", Value: "same-jar", Path: "/"})
			_, _ = io.WriteString(w, `<div data-type="wrd_qrcode"></div><script>url=location.origin+'/login?qrcode=true&vpn_sid=' + '0123456789abcdef';</script>`)
		case "/qrcode-check":
			if _, e := r.Cookie("vpn-pre"); e != nil {
				t.Error("lost QR cookie")
			}
			mu.Lock()
			u := target
			mu.Unlock()
			writeJSON(w, map[string]any{"state": 2, "url": u})
		case "/complete":
			mu.Lock()
			completions++
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "vpn", Value: "good", Path: "/"})
			http.Redirect(w, r, "/", 302)
		case "/":
			_, _ = io.WriteString(w, "portal")
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c, e := New(domain.Profile{Name: "qr", PlatformURL: s.URL + "/https/encoded", GatewayURL: s.URL}, &memoryVault{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	q, e := c.GatewayStart(context.Background())
	if e != nil || q.State != "waiting" {
		t.Fatalf("start %v %v", q, e)
	}
	u, _ := url.Parse(q.URL)
	if u.Query().Get("vpn_sid") != "0123456789abcdef" {
		t.Fatal("QR SID parsing failed")
	}
	mu.Lock()
	target = "https://untrusted.example/complete"
	mu.Unlock()
	_, e = c.GatewayPoll(context.Background())
	assertCode(t, e, "GATEWAY_PROTOCOL")
	mu.Lock()
	target = "/complete"
	mu.Unlock()
	q, e = c.GatewayPoll(context.Background())
	if e != nil || q.State != "authenticated" || q.URL != "" {
		t.Fatalf("poll %v %v", q, e)
	}
	mu.Lock()
	defer mu.Unlock()
	if completions != 1 {
		t.Fatal("unexpected completion requests", completions)
	}
}

func TestGatewayExpiryIsDistinctFromPlatformExpiry(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/user" {
			http.Redirect(w, r, "/login", 302)
			return
		}
		_, _ = io.WriteString(w, "<html>login</html>")
	}))
	defer s.Close()
	c, _ := New(domain.Profile{Name: "x", PlatformURL: s.URL, GatewayURL: s.URL}, nil)
	defer c.Close()
	status := c.AuthStatus(context.Background())
	if status.Gateway != "expired" || status.RequiresAction != "gateway_qr_login" {
		t.Fatalf("wrong auth state: %+v", status)
	}
}

func TestGatewayChallengeSurvivesRestartAndOriginErrorRedactsURL(t *testing.T) {
	var polls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "vpn-pre", Value: "same-jar", Path: "/"})
			_, _ = io.WriteString(w, `<script>var u='/login?qrcode=true&vpn_sid='+'0123456789abcdef';</script>`)
		case "/qrcode-check":
			polls++
			if _, e := r.Cookie("vpn-pre"); e != nil {
				t.Error("lost persisted QR cookies")
			}
			writeJSON(w, map[string]any{"state": 2, "url": "https://untrusted.example/private/completion?ticket=TOP_SECRET"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	v := &memoryVault{}
	p := domain.Profile{Name: "durable", PlatformURL: s.URL, GatewayURL: s.URL}
	first, _ := New(p, v)
	challenge, e := first.GatewayStart(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	first.Close()
	second, _ := New(p, v)
	defer second.Close()
	restored, e := second.GatewayStart(context.Background())
	if e != nil || restored.URL != challenge.URL {
		t.Fatal("challenge was not restored", restored, e)
	}
	_, e = second.GatewayPoll(context.Background())
	assertCode(t, e, "GATEWAY_PROTOCOL")
	if !strings.Contains(e.Error(), "https://untrusted.example") || strings.Contains(e.Error(), "TOP_SECRET") || strings.Contains(e.Error(), "private/completion") {
		t.Fatal("origin error leaked completion URL", e)
	}
	if polls != 1 {
		t.Fatal("unexpected challenge polling", polls)
	}
}

type gatewayRoundTripFunc func(*http.Request) (*http.Response, error)

func (f gatewayRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGatewayUpgradesDefaultPortSelfURLBeforeTransport(t *testing.T) {
	c, e := New(domain.Profile{Name: "https-qr", PlatformURL: "https://webvpn.example/https/encoded", GatewayURL: "https://webvpn.example"}, &memoryVault{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	var destinations []string
	c.http.Transport = gatewayRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "webvpn.example" {
			t.Errorf("ticket would be sent to an unapproved transport origin: %s://%s", r.URL.Scheme, r.URL.Host)
		}
		destinations = append(destinations, r.URL.Path)
		h := http.Header{}
		body := ""
		status := 200
		switch r.URL.Path {
		case "/login":
			body = `<script>var q='/login?qrcode=true&vpn_sid='+'0123456789abcdef';</script>`
		case "/qrcode-check":
			body = `{"state":2,"url":"http://webvpn.example:80/complete?opaque=DO_NOT_LOG%2B%2F"}`
		case "/complete":
			if r.URL.RawQuery != "opaque=DO_NOT_LOG%2B%2F" {
				t.Error("opaque query was changed")
			}
			status = 302
			h.Set("Location", "http://webvpn.example/landing?next=ALSO_PRIVATE")
		case "/landing":
			body = "portal"
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	if _, e = c.GatewayStart(context.Background()); e != nil {
		t.Fatal(e)
	}
	challenge, e := c.GatewayPoll(context.Background())
	if e != nil || challenge.State != "authenticated" {
		t.Fatal(challenge, e)
	}
	if len(destinations) != 4 {
		t.Fatal(destinations)
	}
	for _, raw := range []string{"http://foreign.example/complete", "http://webvpn.example:8080/complete", "http://user@webvpn.example/complete"} {
		u, _ := url.Parse(raw)
		g, _ := url.Parse("https://webvpn.example")
		upgradeGatewayHTTP(u, g)
		if u.Scheme != "http" {
			t.Fatal("upgraded an unapproved URL origin")
		}
	}
}

func TestGatewayConfirmationRequiresStructuredProofNotLoginScript(t *testing.T) {
	var mu sync.Mutex
	structured := false
	posts := 0
	page := `<title>北京邮电大学WebVPN系统</title><script>var qr='/login?qrcode=true&vpn_sid='+'0123456789abcdef';switch(res.error){case "NEED_CONFIRM":fetch('/do-confirm-login');}</script>`
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			mu.Lock()
			confirm := structured && r.URL.Query().Has("ticket")
			mu.Unlock()
			if confirm {
				writeJSON(w, map[string]any{"success": false, "error": "NEED_CONFIRM"})
			} else {
				_, _ = io.WriteString(w, page)
			}
		case "/qrcode-check":
			writeJSON(w, map[string]any{"state": 2, "url": "/login?ticket=SHOULD_NEVER_APPEAR&auth_type=wrd_qrcode"})
		case "/do-confirm-login":
			if r.Method != "POST" {
				t.Error("confirmation method")
			}
			body, _ := io.ReadAll(r.Body)
			if len(body) != 0 {
				t.Error("unexpected confirmation payload")
			}
			mu.Lock()
			posts++
			mu.Unlock()
			writeJSON(w, map[string]any{"success": true, "url": "/"})
		case "/":
			_, _ = io.WriteString(w, "portal")
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	v := &memoryVault{}
	p := domain.Profile{Name: "proof", PlatformURL: s.URL, GatewayURL: s.URL}
	c, _ := New(p, v)
	if _, e := c.GatewayStart(context.Background()); e != nil {
		t.Fatal(e)
	}
	challenge, e := c.GatewayPoll(context.Background())
	assertCode(t, e, "GATEWAY_AUTH_EXPIRED")
	if challenge.State == "confirmation_required" || !strings.Contains(e.Error(), "query_keys=[auth_type,ticket]") || strings.Contains(e.Error(), "SHOULD_NEVER_APPEAR") {
		t.Fatal("login script incorrectly treated as confirmation", challenge, e)
	}
	_, e = c.GatewayConfirm(context.Background())
	assertCode(t, e, "CONFIRMATION_NOT_REQUIRED")
	mu.Lock()
	if posts != 0 {
		t.Error("posted without proof")
	}
	structured = true
	mu.Unlock()
	challenge, e = c.GatewayPoll(context.Background())
	if e != nil || challenge.State != "confirmation_required" {
		t.Fatal(challenge, e)
	}
	c.Close()
	restored, e := New(p, v)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	challenge, e = restored.GatewayConfirm(context.Background())
	if e != nil || challenge.State != "authenticated" {
		t.Fatal(challenge, e)
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 1 {
		t.Fatal("wrong confirmation count", posts)
	}
}

func TestCatalogEstimateAndEncodedGatewayPrefix(t *testing.T) {
	prefix := "/https/7777"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case prefix + "/api/core/app-spec-refs":
			writeJSON(w, map[string]any{"errCode": 0, "data": []any{map[string]any{"specEid": 19, "name": "GPU", "nodeRoleConstraints": []any{}, "password": "hidden"}}})
		case prefix + "/api/core/app-spec-refs/19":
			writeJSON(w, map[string]any{"errCode": 0, "data": map[string]any{"specEid": 19, "name": "GPU"}})
		case prefix + "/api/meter/billing/app-estimated-cost":
			if r.URL.Query().Get("appSpecEid") != "19" || r.URL.Query().Get("resourceConstraints") == "" {
				t.Error("invalid estimate query")
			}
			writeJSON(w, map[string]any{"errCode": 0, "data": 12.5})
		case prefix + "/data/config.json":
			writeJSON(w, map[string]any{"app": map[string]any{"maxReserveTime": 100, "secret": "hidden"}})
		case prefix + "/api/meter/config":
			writeJSON(w, map[string]any{"errCode": 0, "data": map[string]any{"enabled": true}})
		default:
			t.Error("unexpected path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c, _ := New(domain.Profile{Name: "p", PlatformURL: s.URL + prefix}, nil)
	defer c.Close()
	specs, e := c.Specs(context.Background())
	if e != nil || len(specs) != 1 || specs[0].ID != "19" {
		t.Fatal(specs, e)
	}
	b, _ := json.Marshal(specs)
	if strings.Contains(string(b), "hidden") {
		t.Fatal("secret exposed")
	}
	if _, e = c.Spec(context.Background(), "19"); e != nil {
		t.Fatal(e)
	}
	estimate, e := c.Estimate(context.Background(), "19", map[string]any{"1": []any{}})
	if e != nil || estimate["points_per_day"] != json.Number("12.5") {
		t.Fatal(estimate, e)
	}
	cfg, e := c.Config(context.Background())
	if e != nil || cfg.App["secret"] != nil {
		t.Fatal(cfg, e)
	}
}

func TestSpecRefMetadataUsesActualFrontendFieldNames(t *testing.T) {
	spec := parseSpec(map[string]any{"specEid": json.Number("90"), "name": "GPU", "specVersion": "2026.11", "description": "Template description", "scope": "group", "domain": "campus", "poolName": "gpu", "canCustom": false, "resourceConstraints": "{}", "resourceSummary": "{}", "passwordSha256": "never expose"})
	if spec.Version != "2026.11" || spec.Description != "Template description" || spec.Options["scope"] != "group" || spec.Options["poolName"] != "gpu" {
		t.Fatalf("dropped catalog metadata: %+v", spec)
	}
	data, _ := json.Marshal(spec)
	if strings.Contains(string(data), "never expose") {
		t.Fatal("spec metadata leaked credentials")
	}
}

func TestStorageTUSResumeStreamingAndTraversal(t *testing.T) {
	var mu sync.Mutex
	data := []byte{}
	creates := 0
	failSecond := true
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/storage/config":
			writeJSON(w, map[string]any{"stores": []any{map[string]any{"name": "Home", "urlPath": "/work", "isWorkDir": "true"}}})
		case "/api/storage/work/folders/dir":
			writeJSON(w, map[string]any{"items": []any{map[string]any{"name": "file.bin", "isDir": false, "size": 10, "token": "hidden"}}})
		case "/api/storage/work/download/dir/file.bin":
			_, _ = w.Write([]byte("0123456789"))
		case "/api/storage/work/files/dir/":
			if r.Method != "POST" || r.Header.Get("Tus-Resumable") != "1.0.0" || r.Header.Get("Upload-Length") != "10" || !strings.Contains(r.Header.Get("Upload-Metadata"), "filename ZmlsZS5iaW4=") {
				t.Error("invalid TUS create")
			}
			mu.Lock()
			creates++
			mu.Unlock()
			w.Header().Set("Location", "/api/storage/work/files/upload-id")
			w.WriteHeader(201)
		case "/api/storage/work/files/upload-id":
			mu.Lock()
			defer mu.Unlock()
			if r.Method == "HEAD" {
				w.Header().Set("Upload-Offset", fmt.Sprint(len(data)))
				w.Header().Set("Upload-Length", "10")
				return
			}
			if r.Method != "PATCH" || r.Header.Get("Content-Type") != "application/offset+octet-stream" || r.Header.Get("Upload-Offset") != fmt.Sprint(len(data)) {
				t.Error("invalid TUS PATCH")
			}
			if len(data) == 4 && failSecond {
				failSecond = false
				w.WriteHeader(503)
				return
			}
			chunk, _ := io.ReadAll(r.Body)
			data = append(data, chunk...)
			w.Header().Set("Upload-Offset", fmt.Sprint(len(data)))
			w.WriteHeader(204)
		default:
			t.Error("unexpected storage path", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := testClient(t, s)
	stores, e := c.StorageConfig(context.Background())
	if e != nil || len(stores) != 1 || !stores[0].IsWorkDir {
		t.Fatal(stores, e)
	}
	p := StoragePath{Store: "/work", Path: "dir/file.bin"}
	files, e := c.ListFiles(context.Background(), StoragePath{Store: "/work", Path: "dir"})
	if e != nil || len(files) != 1 {
		t.Fatal(files, e)
	}
	var dst bytes.Buffer
	if e = c.Download(context.Background(), p, &dst); e != nil || dst.String() != "0123456789" {
		t.Fatal(dst.String(), e)
	}
	result, e := c.Upload(context.Background(), p, strings.NewReader("0123456789"), 10, UploadOptions{ChunkSize: 4})
	if e == nil || result.Offset != 4 || result.URL == "" {
		t.Fatal(result, e)
	}
	result, e = c.Upload(context.Background(), p, strings.NewReader("0123456789"), 10, UploadOptions{ResumeURL: result.URL, ChunkSize: 4})
	if e != nil || !result.Complete || result.Offset != 10 {
		t.Fatal(result, e)
	}
	mu.Lock()
	if creates != 1 || string(data) != "0123456789" {
		t.Error("resume duplicated content", creates, string(data))
	}
	mu.Unlock()
	_, e = c.ListFiles(context.Background(), StoragePath{Path: "../escape"})
	assertCode(t, e, "INVALID_PATH")
	_, e = c.Upload(context.Background(), p, strings.NewReader("x"), 1, UploadOptions{ResumeURL: "https://evil.example/upload"})
	assertCode(t, e, "TUS_PROTOCOL")
}

func dumpFixture(index int) map[string]any {
	return map[string]any{"minIndex": index, "views": []any{map[string]any{"name": "ResourceActor", "view": map[string]any{"index": index, "memViewDump": map[string]any{"entities": map[string]any{
		"App":      []any{map[string]any{"eid": 10, "name": "test", "userId": 7, "groupId": 0, "specEid": 11, "eaasIndex": 55, "state": "running", "hasReserved": true}},
		"Executor": []any{map[string]any{"eid": 20, "appEid": 10}}, "Cluster": []any{map[string]any{"eid": 30, "executorEid": 20}},
		"Node":            []any{map[string]any{"eid": 40, "clusterEid": 30, "uname": "student", "gid": 9, "eaasIndex": 56}},
		"ResourceAlloc":   []any{map[string]any{"eid": 41, "cpu": 4, "memory": 8192}},
		"ExecutorService": []any{map[string]any{"eid": 50, "name": "ssh", "type": "tcp", "nodeEid": 40}},
		"PortMap":         []any{map[string]any{"eid": 60, "serviceEid": 50, "gwIp": "10.0.0.1", "gwPort": 2222, "port": 22}},
		"User":            []any{map[string]any{"eid": 70, "uid": 7, "uname": "student", "passwordSha256": "DO-NOT-LEAK"}},
	}, "relations": []any{map[string]any{"label": "executors", "srcEid": 10, "tgtEid": 20}, map[string]any{"label": "clusters", "srcEid": 20, "tgtEid": 30}, map[string]any{"label": "nodes", "srcEid": 30, "tgtEid": 40}, map[string]any{"label": "resourceAlloc", "srcEid": 40, "tgtEid": 41}}}}}}}
}
func sendEvent(ctx context.Context, ws *websocket.Conn, name string, p any) error {
	b, _ := json.Marshal([]any{name, p})
	return ws.Write(ctx, websocket.MessageText, append([]byte("42"), b...))
}
func readEvent(ctx context.Context, ws *websocket.Conn) (string, map[string]any, error) {
	for {
		_, b, e := ws.Read(ctx)
		if e != nil {
			return "", nil, e
		}
		if string(b) == "2" {
			_ = ws.Write(ctx, websocket.MessageText, []byte("3"))
			continue
		}
		if !bytes.HasPrefix(b, []byte("42")) {
			return "", nil, fmt.Errorf("unexpected packet %q", b)
		}
		var a []json.RawMessage
		if e = json.Unmarshal(b[2:], &a); e != nil {
			return "", nil, e
		}
		var name string
		_ = json.Unmarshal(a[0], &name)
		var p map[string]any
		if len(a) > 1 {
			_ = decodeInto(json.RawMessage(a[1]), &p)
		}
		return name, p, nil
	}
}

func TestSocketEIO3GraphIndexGapAndTaskCorrelation(t *testing.T) {
	taskID := "c5b62d2e-33cf-4f2d-9ef6-5a466a86287b"
	done := make(chan struct{})
	serverErrors := make(chan error, 8)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/user" {
			writeJSON(w, map[string]any{"uid": 7, "uname": "student"})
			return
		}
		if r.URL.Path == "/api/meter/user/70" {
			writeJSON(w, map[string]any{"errCode": 0, "data": map[string]any{"user": map[string]any{"uid": 7, "email": "student@example.edu", "passwordSha256": "CONTACT-HASH-MUST-NOT-LEAK"}}})
			return
		}
		if r.URL.Query().Get("EIO") != "3" || r.URL.Query().Get("transport") != "websocket" {
			t.Error("wrong websocket version")
		}
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer ws.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		_ = ws.Write(ctx, websocket.MessageText, []byte(`0{"sid":"test","pingInterval":100,"pingTimeout":2000}`))
		_ = ws.Write(ctx, websocket.MessageText, []byte("40"))
		index := 5
		for {
			name, p, e := readEvent(ctx, ws)
			if e != nil {
				return
			}
			switch name {
			case "SYNC":
				if integer(p["index"]) == -1 {
					_ = sendEvent(ctx, ws, "VIEW_DUMP", dumpFixture(index))
				} else {
					_ = sendEvent(ctx, ws, "SYNCED", nil)
				}
			case "READY_RECEIVE_EVENT":
			case "TASK":
				if stringValue(p["id"]) != taskID || integer(p["userId"]) != 7 || integer(p["adminUserId"]) != 0 {
					serverErrors <- fmt.Errorf("invalid task envelope")
				}
				if _, ok := p["payload"].(string); !ok {
					serverErrors <- fmt.Errorf("payload is not a JSON string")
				}
				_ = sendEvent(ctx, ws, "TASK_RET", map[string]any{"id": taskID, "state": "running"})
				// A gap forces a fresh dump before accepting later changes.
				index = 9
				_ = sendEvent(ctx, ws, "EVENT", map[string]any{"type": "AppUpdated", "index": 9, "previousIndex": 8})
				name, p, e = readEvent(ctx, ws)
				if e != nil || name != "SYNC" || integer(p["index"]) != -1 {
					serverErrors <- fmt.Errorf("gap failed to request full sync: %s %v %v", name, p, e)
					return
				}
				_ = sendEvent(ctx, ws, "VIEW_DUMP", dumpFixture(9))
				name, _, e = readEvent(ctx, ws)
				if e != nil || name != "READY_RECEIVE_EVENT" {
					serverErrors <- fmt.Errorf("missing ready")
					return
				}
				vu := map[string]any{"viewName": "ResourceActor", "operations": []any{map[string]any{"operName": "entityAdd", "typeName": "App", "eid": 101, "fields": []any{map[string]any{"name": "name", "value": "created"}, map[string]any{"name": "userId", "value": 7}}}}}
				vub, _ := json.Marshal(vu)
				_ = sendEvent(ctx, ws, "EVENT", map[string]any{"type": "AppCreated", "index": 10, "previousIndex": 9, "taskId": taskID, "viewUpdate": string(vub), "payload": "{}"})
				_ = sendEvent(ctx, ws, "EVENT", map[string]any{"type": "TaskCompleted", "index": 11, "previousIndex": 10, "taskId": taskID})
				close(done)
			}
		}
	}))
	defer s.Close()
	c := testClient(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	snap, e := c.Sync(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if len(snap.Instances) != 1 {
		t.Fatal(snap)
	}
	if snap.Identity.Email != "student@example.edu" {
		t.Fatal("snapshot did not load current-user contact information", snap.Identity)
	}
	app := snap.Instances[0]
	if app.OwnerID != "7" || app.GroupID != "0" || app.SpecID != "11" || app.Index != "55" || len(app.Nodes) != 1 {
		t.Fatalf("bad app graph %+v", app)
	}
	node := app.Nodes[0]
	if node.Username != "student" || node.GroupID != "9" || node.Index != "56" || integer(node.Resources["cpu"]) != 4 || len(node.Services) != 1 || node.Services[0].Port != 2222 {
		t.Fatalf("bad node %+v", node)
	}
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), "DO-NOT-LEAK") {
		t.Fatal("snapshot leaked credentials")
	}
	result, e := c.Submit(ctx, domain.TaskRequest{ID: taskID, Type: "CreateAppTask", Payload: map[string]any{"appSpecEid": 11}})
	if e != nil || result.InstanceID != "101" || result.State != "complete" {
		t.Fatal(result, e)
	}
	<-done
	select {
	case e := <-serverErrors:
		t.Fatal(e)
	default:
	}
	_, e = c.Submit(ctx, domain.TaskRequest{ID: taskID, Type: "CreateAppTask", Payload: map[string]any{"appSpecEid": 12}})
	assertCode(t, e, "TASK_ID_REUSED")
}

func TestTaskStaleCodeAndAmbiguousTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		respond bool
	}{{"stale", true}, {"timeout", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/user" {
					writeJSON(w, map[string]any{"uid": 7})
					return
				}
				ws, e := websocket.Accept(w, r, nil)
				if e != nil {
					return
				}
				defer ws.CloseNow()
				_ = ws.Write(r.Context(), websocket.MessageText, []byte(`0{"pingInterval":1000,"pingTimeout":2000}`))
				_ = ws.Write(r.Context(), websocket.MessageText, []byte("40"))
				for {
					name, p, e := readEvent(r.Context(), ws)
					if e != nil {
						return
					}
					switch name {
					case "SYNC":
						_ = sendEvent(r.Context(), ws, "VIEW_DUMP", dumpFixture(5))
					case "TASK":
						if tc.respond {
							_ = sendEvent(r.Context(), ws, "TASK_RET", map[string]any{"id": p["id"], "state": "rejected", "code": 1165, "message": "server text must not be logged"})
						}
					}
				}
			}))
			defer s.Close()
			c := testClient(t, s)
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			r, e := c.Submit(ctx, domain.TaskRequest{ID: "c5b62d2e-33cf-4f2d-9ef6-5a466a86287b", Type: "StopAppTask", Payload: map[string]any{"appEid": 10}})
			if tc.respond {
				if r.Code != 1165 {
					t.Fatal(r, e)
				}
				assertCode(t, e, "TASK_1165")
			} else {
				assertCode(t, e, "TASK_PENDING")
				if r.ID == "" {
					t.Fatal("lost task UUID")
				}
			}
		})
	}
}

func TestConsoleBinaryFramesAndFragmentedReads(t *testing.T) {
	received := make(chan []byte, 2)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/proxy/service/ff/term" {
			t.Error("wrong console URL", r.URL.Path)
		}
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer ws.CloseNow()
		for range 2 {
			typ, b, e := ws.Read(r.Context())
			if e != nil {
				return
			}
			if typ != websocket.MessageBinary {
				t.Error("console input must be binary")
			}
			received <- b
		}
		_ = ws.Write(r.Context(), websocket.MessageBinary, append([]byte{0}, []byte("hello")...))
		_, _, _ = ws.Read(r.Context())
	}))
	defer s.Close()
	c := testClient(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rw, e := c.Console(ctx, "255")
	if e != nil {
		t.Fatal(e)
	}
	defer rw.Close()
	_, _ = rw.Write([]byte("ls\n"))
	if e = rw.(*ConsoleStream).Resize(80, 24); e != nil {
		t.Fatal(e)
	}
	first := <-received
	second := <-received
	if !bytes.Equal(first, []byte{0, 'l', 's', '\n'}) || second[0] != 1 {
		t.Fatalf("bad frames %v %v", first, second)
	}
	out := make([]byte, 5)
	if _, e = io.ReadFull(rw, out); e != nil || string(out) != "hello" {
		t.Fatal(string(out), e)
	}
}

func TestCaptchaOptionsAndSavedCredentialRetry(t *testing.T) {
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/captcha":
			writeJSON(w, map[string]any{"captchaUuid": "challenge-id", "captchaImage": "data:image/svg+xml;base64,PHN2Zy8+"})
		case "/auth/login":
			calls++
			var p map[string]string
			_ = json.NewDecoder(r.Body).Decode(&p)
			if p["captchaUuid"] != "challenge-id" || p["captchaCode"] != "1234" {
				writeJSON(w, map[string]any{"retCode": 1054})
				return
			}
			if p["username"] != "student" || p["password"] == "password" {
				t.Error("invalid saved login payload")
			}
			writeJSON(w, map[string]any{"retCode": 0})
		case "/auth/user":
			writeJSON(w, map[string]any{"uid": 7, "uname": "student"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := testClient(t, s)
	challenge, e := c.Captcha(context.Background())
	if e != nil || challenge.UUID != "challenge-id" || challenge.Image == "" {
		t.Fatal(challenge, e)
	}
	_, e = c.Login(context.Background(), "student", "password")
	assertCode(t, e, "CAPTCHA_REQUIRED")
	options := LoginOptions{Username: "student", Password: "password", CaptchaUUID: challenge.UUID, CaptchaCode: "1234"}
	if _, e = c.LoginWithOptions(context.Background(), options); e != nil {
		t.Fatal(e)
	}
	if _, e = c.LoginSaved(context.Background(), LoginOptions{CaptchaUUID: challenge.UUID, CaptchaCode: "1234"}); e != nil {
		t.Fatal(e)
	}
	if calls != 3 {
		t.Fatal("unexpected login attempts", calls)
	}
}

func TestTaskRetryReusesExactUUIDAndPayload(t *testing.T) {
	requests := make(chan string, 2)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/user" {
			writeJSON(w, map[string]any{"uid": 7})
			return
		}
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer ws.CloseNow()
		_ = ws.Write(r.Context(), websocket.MessageText, []byte(`0{"pingInterval":100,"pingTimeout":2000}`))
		_ = ws.Write(r.Context(), websocket.MessageText, []byte("40"))
		count := 0
		for {
			name, p, e := readEvent(r.Context(), ws)
			if e != nil {
				return
			}
			switch name {
			case "SYNC":
				_ = sendEvent(r.Context(), ws, "VIEW_DUMP", dumpFixture(5))
			case "TASK":
				b, _ := json.Marshal(p)
				requests <- string(b)
				count++
				if count == 2 {
					_ = sendEvent(r.Context(), ws, "TASK_RET", map[string]any{"id": p["id"], "state": "complete", "code": 0})
				}
			}
		}
	}))
	defer s.Close()
	c := testClient(t, s)
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	_, e := c.Submit(ctx, domain.TaskRequest{ID: "c5b62d2e-33cf-4f2d-9ef6-5a466a86287b", Type: "StopAppTask", Payload: map[string]any{"appEid": 10, "eaasIndex": 55}})
	if e != nil {
		t.Fatal(e)
	}
	a, b := <-requests, <-requests
	if a != b {
		t.Fatal("retry changed task envelope")
	}
}

type failingVault struct{}

func (failingVault) Get(string, any) error { return domain.E("SECRET_DECRYPTION_FAILED", "test") }
func (failingVault) Put(string, any) error { return nil }
func (failingVault) Delete(string) error   { return nil }
func TestUnreadableSessionFailsClosed(t *testing.T) {
	_, e := New(domain.Profile{Name: "x", PlatformURL: "https://example.test"}, failingVault{})
	assertCode(t, e, "SECRET_STORE")
}
