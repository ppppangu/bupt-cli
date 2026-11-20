package domain

import (
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"bupt.local/cli/internal/shellquote"
)

const TrialItemLimit = 16
const TrialTextLimit = 16384 // UTF-8 bytes per input field

type TrialInput struct {
	Texts     []string `json:"texts,omitempty"`
	Query     string   `json:"query,omitempty"`
	Documents []string `json:"documents,omitempty"`
	MaxTokens int      `json:"max_tokens,omitempty"`
	TopN      int      `json:"top_n,omitempty"`
}
type ModelInvocation struct {
	Model          string     `json:"model"`
	KeyID          string     `json:"key_id"`
	Input          TrialInput `json:"input"`
	IdempotencyKey string     `json:"idempotency_key"`
	ResendAfter    string     `json:"resend_after,omitempty"`
}

type ModelTrial struct {
	Kind   string
	Title  string
	Sample TrialInput
}

func trialKind(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "chat", "openai-chat/completions":
		return "chat"
	case "embedding", "embeddings", "openai-embeddings":
		return "embedding"
	case "rerank", "openai-rerank":
		return "rerank"
	}
	return ""
}
func TrialFor(m PortalModel) (ModelTrial, error) {
	kind, protocol := trialKind(m.Kind), trialKind(m.Type)
	if kind == "" {
		kind = protocol
	}
	if m.ID == "" || kind == "" || protocol != "" && protocol != kind {
		return ModelTrial{}, E("MODEL_TRIAL_UNSUPPORTED", "目录尚未给出可识别且一致的对话、向量或重排接口。")
	}
	t := ModelTrial{Kind: kind}
	switch kind {
	case "chat":
		t.Title = "短问答"
		question := "用一句话解释什么是语义检索。"
		switch m.ID {
		case "qwen3.8-27b":
			t.Title = "提炼课程通知"
			question = "把通知缩成一句话：周五下午的实验课改在教三楼上课，请带电脑。"
		case "deepseek-v4-flash":
			t.Title = "解释网络概念"
			question = "用一句话解释 TCP 为什么需要确认。"
		}
		t.Sample = TrialInput{Texts: []string{question}, MaxTokens: 128}
	case "embedding":
		t.Title = "比较两句话的语义"
		texts := []string{"图书馆何时闭馆？", "我想知道图书馆的开放时间。"}
		switch m.ID {
		case "bge-m3":
			t.Title = "比较中英文表述"
			texts = []string{"请问怎样借阅图书？", "How can I borrow a book?"}
		case "jina-embeddings-v3":
			t.Title = "比较检索词与段落"
			texts = []string{"报名科研讲座", "科研讲座的报名入口已开放。"}
		}
		t.Sample = TrialInput{Texts: texts}
	case "rerank":
		t.Title = "为校园问答排列候选段落"
		t.Sample = TrialInput{Query: "在哪里借书？", Documents: []string{"食堂早上七点供应早餐。", "图书馆服务台办理图书借阅。", "操场周末开放。"}}
	}
	return t, nil
}

// ModelArgs represents the editable input as argv, including empty drafts.
func (in TrialInput) ModelArgs(model, key string, send bool, options ...string) []string {
	verb := "example"
	if send {
		verb = "invoke"
	}
	args := []string{"api", "model", verb}
	args = append(args, options...)
	if send && key != "" {
		args = append(args, "--key", key)
	}
	return append(in.appendFlags(args), "--", model)
}

func (in TrialInput) appendFlags(args []string) []string {
	for _, s := range in.Texts {
		args = append(args, "--text", s)
	}
	if in.Query != "" {
		args = append(args, "--query", in.Query)
	}
	for _, s := range in.Documents {
		args = append(args, "--document", s)
	}
	if in.MaxTokens > 0 {
		args = append(args, "--max-tokens", strconv.Itoa(in.MaxTokens))
	}
	if in.TopN > 0 {
		args = append(args, "--top", strconv.Itoa(in.TopN))
	}
	return args
}

// ExampleCode builds the same validated request used by a live trial.
func (m PortalModel) ExampleCode(in TrialInput, format, base string) (string, error) {
	request, err := m.TrialRequest(in)
	if err != nil {
		return "", err
	}
	if format != "bupt" {
		return request.Code(format, base)
	}
	argv := in.appendFlags([]string{"api", "model", "invoke"})
	return shellquote.Command("bupt", argv...) + ` --key "${BUPT_KEY_ID:?请设置选中模型的 Key ID}" -- ` + shellquote.Arg(m.ID), nil
}

type TrialRequest struct {
	Model string         `json:"model"`
	Kind  string         `json:"kind"`
	Path  string         `json:"path"`
	Body  map[string]any `json:"body"`
}

func (m PortalModel) TrialRequest(in TrialInput) (TrialRequest, error) {
	t, e := TrialFor(m)
	if e != nil {
		return TrialRequest{}, e
	}
	r := TrialRequest{Model: m.ID, Kind: t.Kind, Body: map[string]any{"model": m.ID}}
	check := func(items []string) bool {
		if len(items) == 0 || len(items) > TrialItemLimit {
			return false
		}
		for _, s := range items {
			if strings.TrimSpace(s) == "" || len(s) > TrialTextLimit {
				return false
			}
		}
		return true
	}
	switch t.Kind {
	case "chat":
		if len(in.Texts) != 1 || !check(in.Texts) || in.Query != "" || len(in.Documents) > 0 || in.TopN != 0 {
			return r, E("INVALID_TRIAL_INPUT", "请输入一段对话内容。")
		}
		n := in.MaxTokens
		if n == 0 {
			n = 128
		}
		if n < 1 || n > 4096 {
			return r, E("INVALID_TRIAL_INPUT", "试用的输出上限为 1 至 4096 Token。")
		}
		r.Path = "/chat/completions"
		r.Body["messages"] = []map[string]string{{"role": "user", "content": in.Texts[0]}}
		r.Body["max_tokens"] = n
	case "embedding":
		if !check(in.Texts) || in.Query != "" || len(in.Documents) > 0 || in.MaxTokens != 0 || in.TopN != 0 {
			return r, E("INVALID_TRIAL_INPUT", "请输入 1 至 16 段非空向量文本。")
		}
		r.Path = "/embeddings"
		r.Body["input"] = slices.Clone(in.Texts)
		r.Body["encoding_format"] = "float"
	case "rerank":
		if !check([]string{in.Query}) || !check(in.Documents) || len(in.Texts) > 0 || in.MaxTokens != 0 {
			return r, E("INVALID_TRIAL_INPUT", "请填写问题和 1 至 16 段非空候选文本。")
		}
		n := in.TopN
		if n == 0 {
			n = len(in.Documents)
		}
		if n < 1 || n > len(in.Documents) {
			return r, E("INVALID_TRIAL_INPUT", "返回条数应在 1 到候选段落数之间。")
		}
		r.Path = "/rerank"
		r.Body["query"] = in.Query
		r.Body["documents"] = slices.Clone(in.Documents)
		r.Body["top_n"] = n
	}
	return r, nil
}

// Snippets and the actual POST share exactly the same serialized request body.
// Secrets are supplied by the caller's environment in the exported examples.
// SimilarityExample uses the embedding endpoint and computes the same local
// cosine score as the workbench. JSON remains the exact gateway request.
func (m PortalModel) SimilarityExample(in TrialInput, format, base string) (string, error) {
	r, err := m.TrialRequest(in)
	if err != nil {
		return "", err
	}
	if r.Kind != "embedding" || len(in.Texts) != 2 {
		return "", E("INVALID_TRIAL_INPUT", "语义相似度需要两段向量文本。")
	}
	if format == "bupt" {
		return m.ExampleCode(in, format, base)
	}
	return r.code(format, base, true)
}

const similarityPython = `import math
items = sorted(data["data"], key=lambda item: item["index"])
a, b = (item["embedding"] for item in items)
if len(a) != len(b) or not a or not any(a) or not any(b):
    raise ValueError("两个向量须维度相同且非零")
score = sum(x * y for x, y in zip(a, b)) / (math.hypot(*a) * math.hypot(*b))
print(f"余弦相似度  {score:.4f}")`

func (r TrialRequest) Code(format, base string) (string, error) { return r.code(format, base, false) }
func (r TrialRequest) code(format, base string, similarity bool) (string, error) {
	body, e := json.MarshalIndent(r.Body, "", "  ")
	if e != nil {
		return "", e
	}
	literal := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	endpoint := strings.TrimRight(base, "/") + r.Path
	switch format {
	case "json":
		return string(body), nil
	case "curl":
		prefix := ""
		target := shellquote.Literal(endpoint)
		if base == "" {
			prefix = ": \"${BUPT_BASE_URL:?请设置 BUPT_BASE_URL}\"\n"
			target = "\"${BUPT_BASE_URL%/}" + r.Path + "\""
		}
		code := prefix + "curl -q --silent --show-error --globoff --proto '=https' --url " + target + " \\\n  -H \"Authorization: Bearer ${BUPT_API_KEY:?请设置 BUPT_API_KEY}\" \\\n  -H 'Content-Type: application/json' \\\n  --data-binary " + shellquote.Literal(string(body))
		if similarity {
			code += " | python3 -c " + shellquote.Literal("import json, sys\ndata = json.load(sys.stdin)\n"+similarityPython)
		}
		return code, nil
	case "python":
		target := literal(endpoint)
		if base == "" {
			target = "os.environ['BUPT_BASE_URL'].rstrip('/') + " + literal(r.Path)
		}
		output := "    print(json.dumps(json.load(response), ensure_ascii=False, indent=2))"
		if similarity {
			output = "    data = json.load(response)\n\n" + similarityPython
		}
		return `import json
import os
from urllib.request import HTTPRedirectHandler, Request, build_opener

class NoRedirect(HTTPRedirectHandler):
    def redirect_request(self, *args):
        return None

` + "payload = " + string(body) + "\nurl = " + target + `
if not url.startswith('https://'):
    raise ValueError('Base URL 需要 HTTPS')
req = Request(url, data=json.dumps(payload).encode(), headers={
    'Content-Type': 'application/json',
    'Authorization': 'Bearer ' + os.environ['BUPT_API_KEY'],
})
with build_opener(NoRedirect).open(req, timeout=90) as response:
` + output, nil
	default:
		return "", E("INVALID_FORMAT", "示例格式可选 curl、python 或 json。")
	}
}

type TrialUsage struct {
	Prompt     *int `json:"prompt_tokens,omitempty"`
	Completion *int `json:"completion_tokens,omitempty"`
	Total      *int `json:"total_tokens,omitempty"`
}
type RankedText struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
	Text  string  `json:"text"`
}
type TrialResult struct {
	ResponseID string       `json:"response_id,omitempty"`
	RequestID  string       `json:"request_id,omitempty"`
	CostPoints *float64     `json:"cost_points,omitempty"`
	Model      string       `json:"model"`
	Kind       string       `json:"kind"`
	ElapsedMS  int64        `json:"elapsed_ms"`
	Answer     string       `json:"answer,omitempty"`
	Finish     string       `json:"finish_reason,omitempty"`
	Vectors    [][]float64  `json:"embeddings,omitempty"`
	Ranked     []RankedText `json:"results,omitempty"`
	Usage      *TrialUsage  `json:"usage,omitempty"`
}

func (r TrialResult) Summary() string {
	var parts []string
	switch r.Kind {
	case "chat":
		if r.Answer != "" {
			parts = append(parts, r.Answer)
		} else {
			parts = append(parts, "模型返回了空文本。")
		}
		if r.Finish != "" && r.Finish != "stop" {
			parts = append(parts, "结束原因  "+r.Finish)
		}
	case "embedding":
		for i, v := range r.Vectors {
			head := make([]string, 0, 4)
			for _, n := range v[:min(4, len(v))] {
				head = append(head, fmt.Sprintf("%.5f", n))
			}
			parts = append(parts, fmt.Sprintf("文本 %d · %d 维\n[%s …]", i+1, len(v), strings.Join(head, ", ")))
		}
		if len(r.Vectors) == 2 && len(r.Vectors[0]) == len(r.Vectors[1]) {
			var dot, a, b float64
			for i, x := range r.Vectors[0] {
				y := r.Vectors[1][i]
				dot += x * y
				a += x * x
				b += y * y
			}
			if score := dot / (math.Sqrt(a) * math.Sqrt(b)); !math.IsNaN(score) && !math.IsInf(score, 0) {
				parts = append(parts, fmt.Sprintf("两段文本的余弦相似度  %.4f", score))
			}
		}
	case "rerank":
		for rank, d := range r.Ranked {
			parts = append(parts, fmt.Sprintf("%d. 候选 %d · %.4f\n%s", rank+1, d.Index+1, d.Score, d.Text))
		}
	}
	return strings.Join(append(parts, r.Metrics()), "\n\n")
}
func (r TrialResult) Metrics() string {
	var stats []string
	if r.Usage != nil {
		if n := r.Usage.Prompt; n != nil {
			stats = append(stats, fmt.Sprintf("输入 %d Token", *n))
		}
		if n := r.Usage.Completion; n != nil {
			stats = append(stats, fmt.Sprintf("输出 %d Token", *n))
		}
		if n := r.Usage.Total; n != nil {
			stats = append(stats, fmt.Sprintf("合计 %d Token", *n))
		}
	}
	if r.CostPoints != nil {
		stats = append(stats, fmt.Sprintf("费用 %g 积分", *r.CostPoints))
	}
	if r.RequestID != "" {
		stats = append(stats, "网关请求编号 "+r.RequestID)
	}
	stats = append(stats, fmt.Sprintf("耗时 %.2f 秒", float64(r.ElapsedMS)/1000))
	return strings.Join(stats, " · ")
}

func (p PortalPage) TrialModel(id string) (PortalModel, error) {
	for _, m := range p.Models {
		if m.ID == id {
			return m, nil
		}
	}
	return PortalModel{}, E("MODEL_NOT_FOUND", "当前账号的模型目录没有这个 ID。")
}
