package cli

import (
	"bupt.local/cli/internal/domain"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestModelExamplesAndExplicitCLITrial(t *testing.T) {
	var submitted int
	b := &fakeBackend{call: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method == "portal.read" {
			page := domain.PortalPage{Section: p["section"].(string)}
			if page.Section == "model" {
				page.Models = []domain.PortalModel{{ID: "user-model", Type: "openai-chat/completions"}}
			} else {
				page.Data = map[string]any{"base_url": "https://myai.bupt.edu.cn/llm-gw/v1"}
			}
			return page, nil
		}
		if method != "portal.trial" || p["key_id"] != "key-id" || p["idempotency_key"] == "" {
			t.Fatal(method, p)
		}
		var in domain.TrialInput
		_ = json.Unmarshal(domain.Result(p["input"]), &in)
		if len(in.Texts) != 1 || in.Texts[0] != "我写的句子" || in.MaxTokens != 24 {
			t.Fatal(in)
		}
		submitted++
		used := 8
		return domain.TrialResult{Model: "user-model", Kind: "chat", Answer: "assistant reply", Usage: &domain.TrialUsage{Prompt: &used}}, nil
	}}
	opts, out, diag := testOptions(t, b)
	invoke := func(args ...string) int {
		out.Reset()
		diag.Reset()
		return executeTest(t, opts, append([]string{"api", "model"}, args...)...)
	}
	if c := invoke("example", "user-model", "--text", "我写的句子", "--format", "curl"); c != 0 || !strings.Contains(out.String(), "我写的句子") || !strings.Contains(out.String(), "/chat/completions") || submitted != 0 {
		t.Fatal(c, out, diag)
	}
	if c := invoke("try", "user-model", "--sample", "--dry-run", "--json"); c != 0 || !strings.Contains(out.String(), `"dry_run":true`) || strings.Contains(out.String(), "[redacted]") || submitted != 0 {
		t.Fatal(c, out, diag)
	}
	if c := invoke("try", "user-model", "--key", "key-id", "--text", "我写的句子", "--max-tokens", "24", "--json"); c != 0 || submitted != 1 || !strings.Contains(out.String(), "assistant reply") || !strings.Contains(out.String(), `"prompt_tokens":8`) {
		t.Fatal(c, out, diag)
	}
	if c := invoke("try", "user-model", "--key", "key-id", "--cached", "--sample"); c == 0 || submitted != 1 {
		t.Fatal("cached execution was accepted")
	}
}
