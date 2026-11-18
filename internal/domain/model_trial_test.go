package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTrialTemplatesAndCustomPayloads(t *testing.T) {
	for _, tc := range []struct {
		id, kind, path string
		in             TrialInput
	}{
		{"deepseek-v4-flash", "chat", "/chat/completions", TrialInput{Texts: []string{"我自己的 '问题'\n第二行"}, MaxTokens: 32}},
		{"qwen3.8-27b", "chat", "/chat/completions", TrialInput{Texts: []string{"我的通知"}}},
		{"bge-m3", "embedding", "/embeddings", TrialInput{Texts: []string{"第一句", "second"}}},
		{"jina-embeddings-v3", "openai-embeddings", "/embeddings", TrialInput{Texts: []string{"只输入一段"}}},
		{"jina-reranker-m0", "rerank", "/rerank", TrialInput{Query: "我的问题", Documents: []string{"甲", "乙"}, TopN: 1}},
	} {
		m := PortalModel{ID: tc.id, Kind: tc.kind}
		template, e := TrialFor(m)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = m.TrialRequest(template.Sample); e != nil {
			t.Fatal(tc.id, e)
		}
		r, e := m.TrialRequest(tc.in)
		if e != nil || r.Path != tc.path || r.Body["model"] != tc.id {
			t.Fatal(r, e)
		}
		raw, e := r.Code("json", "")
		if e != nil || !json.Valid([]byte(raw)) {
			t.Fatal(e)
		}
		for _, format := range []string{"curl", "python"} {
			code, e := r.Code(format, "https://example.edu/v1")
			if e != nil || !strings.Contains(code, tc.id) || !strings.Contains(code, tc.path) || !strings.Contains(code, "BUPT_API_KEY") {
				t.Fatal(format, e)
			}
		}
	}
	m := PortalModel{ID: "new", Type: "openai-rerank"}
	if _, e := m.TrialRequest(TrialInput{Query: "x", Documents: []string{"1"}, TopN: 3}); e == nil {
		t.Fatal("invalid top_n")
	}
	if _, e := TrialFor(PortalModel{ID: "unknown"}); e == nil {
		t.Fatal("guessed unknown modality")
	}
	if _, e := TrialFor(PortalModel{ID: "m", Kind: "chat", Type: "openai-rerank"}); e == nil {
		t.Fatal("conflicting metadata")
	}
}
