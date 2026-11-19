package platform

import (
	"bupt.local/cli/internal/domain"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestModelGatewayPostsOnlyTheEditedRequestAndParsesEachType(t *testing.T) {
	const secret = "fixture-secret-never-display"
	for _, tc := range []struct {
		kind, text, reply string
		in                domain.TrialInput
	}{
		{"chat", "我输入的课题", `{"id":"reply-42","cost_points":0,"choices":[{"finish_reason":"stop","message":{"content":"模型的回复"}}],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`, domain.TrialInput{Texts: []string{"我输入的课题"}}},
		{"embedding", "向量文本", `{"data":[{"index":1,"embedding":[0,1]},{"index":0,"embedding":[1,0]}]}`, domain.TrialInput{Texts: []string{"向量文本", "另一段"}}},
		{"rerank", "想借书", `{"results":[{"index":1,"relevance_score":0.94},{"index":0,"relevance_score":0.1}]}`, domain.TrialInput{Query: "想借书", Documents: []string{"食堂", "图书馆"}}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			m := domain.PortalModel{ID: "current-id", Kind: tc.kind}
			req, e := m.TrialRequest(tc.in)
			if e != nil {
				t.Fatal(e)
			}
			calls := 0
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				b, _ := io.ReadAll(r.Body)
				if r.Method != "POST" || r.URL.Path != "/v1"+req.Path || r.Header.Get("Authorization") != "Bearer "+secret || !strings.Contains(string(b), tc.text) || strings.Contains(string(b), secret) || r.Header.Get("Cookie") != "" {
					t.Error("unexpected inference request")
				}
				w.Header().Set("X-Request-Id", "gateway-call-7")
				fmt.Fprint(w, tc.reply)
			}))
			defer s.Close()
			c, _ := New(domain.Profile{Name: "f", PlatformURL: s.URL, PortalURL: s.URL}, &memoryVault{})
			defer c.Close()
			c.http.Transport = s.Client().Transport
			url, _ := url.Parse(s.URL)
			c.cookies.SetCookies(url, []*http.Cookie{{Name: "portal", Value: "management-cookie", Path: "/"}})
			r, e := c.RunModelTrial(t.Context(), s.URL+"/v1", req, secret)
			if e != nil || calls != 1 || r.Kind != tc.kind || r.Model != m.ID || r.RequestID != "gateway-call-7" {
				t.Fatal(r, e, calls)
			}
			switch tc.kind {
			case "chat":
				if r.ResponseID != "reply-42" || r.CostPoints == nil || *r.CostPoints != 0 || r.Answer != "模型的回复" || r.Usage == nil || *r.Usage.Total != 11 {
					t.Fatal(r)
				}
			case "embedding":
				if r.Vectors[0][0] != 1 || !strings.Contains(r.Summary(), "余弦相似度  0.0000") {
					t.Fatal(r)
				}
			case "rerank":
				if r.Ranked[0].Text != "图书馆" || r.Ranked[0].Index != 1 {
					t.Fatal(r)
				}
			}
		})
	}
}
func TestTrialRejectsRedirectsAndForeignDestinations(t *testing.T) {
	calls := 0
	const secret = "private-fixture-token"
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Redirect(w, r, "/other", 307) }))
	defer s.Close()
	c, _ := New(domain.Profile{Name: "f", PlatformURL: s.URL, PortalURL: s.URL}, nil)
	defer c.Close()
	c.http.Transport = s.Client().Transport
	req, _ := (domain.PortalModel{ID: "m", Kind: "chat"}).TrialRequest(domain.TrialInput{Texts: []string{"hi"}})
	for _, base := range []string{"http://myai.bupt.edu.cn/llm-gw/v1", "https://unrelated.example/v1", "https://myai.bupt.edu.cn/untrusted", "https://myai.bupt.edu.cn/llm-gw/v1?q=1", "https://user@myai.bupt.edu.cn/llm-gw/v1"} {
		_, e := c.RunModelTrial(t.Context(), base, req, secret)
		assertCode(t, e, "MODEL_GATEWAY_UNTRUSTED")
	}
	_, e := c.RunModelTrial(t.Context(), s.URL+"/v1", req, secret)
	assertCode(t, e, "MODEL_GATEWAY_REDIRECT")
	if calls != 1 || strings.Contains(e.Error(), secret) {
		t.Fatal("replayed redirect or leaked secret", calls)
	}
}
func TestTrialRoutesBearerThroughSelectedWebVPN(t *testing.T) {
	c, _ := New(domain.Profile{Name: "p", GatewayURL: "https://webvpn.bupt.edu.cn", PlatformURL: "https://gpuhub.bupt.edu.cn"}, nil)
	defer c.Close()
	c.cookies.SetCookies(c.gateway, []*http.Cookie{{Name: "vpn", Value: "fixture", Path: "/"}})
	dest, _ := ProxyURL(c.gateway.String(), "https://myai.bupt.edu.cn/llm-gw/v1/embeddings")
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != dest || !strings.Contains(r.Header.Get("Cookie"), "vpn=fixture") || r.Header.Get("Authorization") != "Bearer fixture-trial-secret" {
			t.Fatal("wrong transport or credential")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"index":0,"embedding":[1]}]}`)), Header: http.Header{}, Request: r}, nil
	})
	req, _ := (domain.PortalModel{ID: "m", Kind: "embedding"}).TrialRequest(domain.TrialInput{Texts: []string{"x"}})
	if _, e := c.RunModelTrial(context.Background(), "https://myai.bupt.edu.cn/llm-gw/v1", req, "fixture-trial-secret"); e != nil {
		t.Fatal(e)
	}
}
func TestTrialRejectsMalformedResults(t *testing.T) {
	req, _ := (domain.PortalModel{ID: "m", Kind: "rerank"}).TrialRequest(domain.TrialInput{Query: "q", Documents: []string{"a"}})
	for _, doc := range []string{`{"results":[{"index":9,"relevance_score":1}]}`, `{"results":[{"index":0}]}`, `{"error":{"message":"upstream error"}}`} {
		if !json.Valid([]byte(doc)) {
			t.Fatal("bad fixture")
		}
		_, e := parseTrialResponse(req, []byte(doc))
		assertCode(t, e, "MODEL_PROTOCOL")
	}
}

func TestModelRateLimitDoesNotResend(t *testing.T) {
	n := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		w.Header().Set("Retry-After", "15")
		w.WriteHeader(429)
	}))
	defer s.Close()
	c, _ := New(domain.Profile{Name: "f", PlatformURL: s.URL, PortalURL: s.URL}, nil)
	defer c.Close()
	c.http.Transport = s.Client().Transport
	req, _ := (domain.PortalModel{ID: "m", Kind: "chat"}).TrialRequest(domain.TrialInput{Texts: []string{"test"}})
	for range 2 {
		_, e := c.RunModelTrial(t.Context(), s.URL+"/v1", req, "fixture-secret")
		assertCode(t, e, "MODEL_RATE_LIMITED")
	}
	if n != 1 {
		t.Fatal("ignored inference cooldown", n)
	}
}
