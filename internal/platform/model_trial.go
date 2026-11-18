package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

// The portal advertises the data plane; RPC callers never supply an upstream URL.
func publicModelGateway(base, portal string) (*url.URL, error) {
	u, e := url.Parse(strings.TrimSuffix(base, "/"))
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.Contains(u.Path, "\\") || path.Clean(u.Path) != u.Path {
		return nil, domain.E("MODEL_GATEWAY_UNTRUSTED", "模型网关地址不符合当前配置的 HTTPS 来源。")
	}
	official := strings.EqualFold(u.Hostname(), "myai.bupt.edu.cn") || strings.EqualFold(u.Hostname(), "ai.bupt.edu.cn")
	if official {
		if u.Port() != "" && u.Port() != "443" || u.Path != "/llm-gw/v1" {
			return nil, domain.E("MODEL_GATEWAY_UNTRUSTED", "学校网关的端口或服务路径与已核验的配置不一致。")
		}
	} else {
		configured, err := url.Parse(portal)
		if err != nil || configured.Scheme != "https" || configured.User != nil || !strings.EqualFold(u.Host, configured.Host) {
			return nil, domain.E("MODEL_GATEWAY_UNTRUSTED", "网关不属于学校模型服务或当前配置可信的门户来源。")
		}
	}
	return u, nil
}

func (c *Client) trialTarget(base string, req domain.TrialRequest) (string, error) {
	u, err := publicModelGateway(base, c.profile.PortalURL)
	if err != nil {
		return "", err
	}
	switch req.Path {
	case "/chat/completions", "/embeddings", "/rerank":
	default:
		return "", domain.E("MODEL_TRIAL_UNSUPPORTED", "未知的模型试用接口。")
	}
	u.Path += req.Path
	if c.gateway != nil {
		if c.gateway.Scheme != "https" {
			return "", domain.E("MODEL_GATEWAY_UNTRUSTED", "模型请求需要 HTTPS WebVPN。")
		}
		return ProxyURL(c.gateway.String(), u.String())
	}
	return u.String(), nil
}

func (c *Client) RunModelTrial(ctx context.Context, base string, trial domain.TrialRequest, secret string) (domain.TrialResult, error) {
	var result domain.TrialResult
	c.mu.Lock()
	remaining := time.Until(c.retryTrialAfter)
	c.mu.Unlock()
	if remaining > 0 {
		return result, domain.E("MODEL_RATE_LIMITED", fmt.Sprintf("学校网关限流，请在 %d 秒后再发起试用。", int(remaining.Seconds())+1))
	}
	dest, e := c.trialTarget(base, trial)
	if e != nil {
		return result, e
	}
	if secret == "" || strings.ContainsAny(secret, "\x00\r\n") {
		return result, domain.E("KEY_NOT_STORED", "控制器缺少可用的密钥完整值。")
	}
	body, e := json.Marshal(trial.Body)
	if e != nil {
		return result, e
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, dest, bytes.NewReader(body))
	if e != nil {
		return result, domain.E("MODEL_GATEWAY_UNTRUSTED", "无法构建模型请求。")
	}
	req.GetBody = nil // A lost response must never cause the credentialed POST to be replayed.
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+secret)
	httpc := *c.http
	httpc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if c.gateway == nil {
		httpc.Jar = nil
	} else {
		req.Header.Set("Origin", c.gateway.String())
		req.Header.Set("Referer", c.endpoint("/apikeys/guide"))
	}
	start := time.Now()
	resp, e := httpc.Do(req)
	if e != nil {
		return result, domain.E("MODEL_RESULT_UNKNOWN", "模型网关的请求结果未知。请核对用量后再决定是否重新发送。")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		if next, err := resp.Location(); c.gateway != nil && err == nil && next.Host == c.gateway.Host && strings.HasPrefix(next.Path, "/login") {
			return result, domain.E("GATEWAY_AUTH_EXPIRED", "WebVPN 已过期，请运行 bupt login。")
		}
		return result, domain.E("MODEL_GATEWAY_REDIRECT", "模型网关要求重定向；本次请求已结束。")
	case resp.StatusCode == 401:
		return result, domain.E("MODEL_KEY_REJECTED", "模型网关未接受所选 Key，请检查启用状态。")
	case resp.StatusCode == 403:
		return result, domain.E("MODEL_ACCESS_DENIED", "模型网关拒绝本次调用，请检查 Key 范围和账户额度。")
	case resp.StatusCode == 429:
		c.mu.Lock()
		c.retryTrialAfter = time.Now().Add(retryDelay(resp.Header.Get("Retry-After")))
		c.mu.Unlock()
		return result, domain.E("MODEL_RATE_LIMITED", "模型网关限流或当前额度不足，请查看余额和用量。")
	case resp.StatusCode == 404:
		return result, domain.E("MODEL_ENDPOINT_UNAVAILABLE", "网关未提供所选模型或该类型的调用接口。")
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		return result, domain.E("MODEL_GATEWAY_ERROR", "模型网关未完成调用（"+http.StatusText(resp.StatusCode)+"）。")
	}
	const limit = 4 << 20
	raw, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if e != nil {
		return result, domain.E("MODEL_RESULT_UNKNOWN", "未收到完整模型结果，请核对用量后再决定是否重发。")
	}
	if len(raw) > limit {
		return result, domain.E("MODEL_RESPONSE_LARGE", "试用响应超出 4 MiB 显示上限。")
	}
	if bytes.Contains(raw, []byte(secret)) {
		return result, domain.E("MODEL_PROTOCOL", "网关响应包含认证材料，无法显示。")
	}
	result, e = parseTrialResponse(trial, raw)
	if e != nil {
		return domain.TrialResult{}, e
	}
	result.ElapsedMS = time.Since(start).Milliseconds()
	return result, nil
}
func parseTrialResponse(req domain.TrialRequest, b []byte) (domain.TrialResult, error) {
	res := domain.TrialResult{Kind: req.Kind, Model: req.Model}
	invalid := domain.E("MODEL_PROTOCOL", "网关返回的结果与所选模型的接口格式不符。")
	var doc struct {
		Error   json.RawMessage
		Usage   *domain.TrialUsage
		Choices []struct {
			Finish  string `json:"finish_reason"`
			Message *struct {
				Content json.RawMessage
				Refusal string
			}
		}
		Data []struct {
			Index     *int
			Embedding []float64
		}
		Results []struct {
			Index *int
			Score *float64 `json:"relevance_score"`
		}
	}
	if e := json.Unmarshal(b, &doc); e != nil || len(doc.Error) > 0 && string(doc.Error) != "null" {
		return res, invalid
	}
	res.Usage = doc.Usage
	switch req.Kind {
	case "chat":
		if len(doc.Choices) == 0 || doc.Choices[0].Message == nil {
			return res, invalid
		}
		first := doc.Choices[0]
		res.Finish = first.Finish
		raw := first.Message.Content
		if len(raw) > 0 && json.Unmarshal(raw, &res.Answer) != nil {
			var blocks []struct{ Type, Text string }
			if json.Unmarshal(raw, &blocks) != nil {
				return res, invalid
			}
			for _, part := range blocks {
				if part.Type == "text" || part.Type == "output_text" {
					res.Answer += part.Text
				}
			}
		}
		if res.Answer == "" {
			res.Answer = first.Message.Refusal
		}
		if res.Answer == "" && res.Finish != "length" && res.Finish != "content_filter" {
			return res, invalid
		}
	case "embedding":
		texts, _ := req.Body["input"].([]string)
		if len(texts) == 0 || len(doc.Data) != len(texts) {
			return res, invalid
		}
		res.Vectors = make([][]float64, len(texts))
		dim := len(doc.Data[0].Embedding)
		for _, row := range doc.Data {
			if dim == 0 || len(row.Embedding) != dim || row.Index == nil || *row.Index < 0 || *row.Index >= len(texts) || res.Vectors[*row.Index] != nil {
				return res, invalid
			}
			res.Vectors[*row.Index] = row.Embedding
		}
	case "rerank":
		docs, _ := req.Body["documents"].([]string)
		n, _ := req.Body["top_n"].(int)
		if len(doc.Results) == 0 || len(doc.Results) > n {
			return res, invalid
		}
		used := make(map[int]bool, len(doc.Results))
		for _, item := range doc.Results {
			if item.Index == nil || *item.Index < 0 || *item.Index >= len(docs) || item.Score == nil || used[*item.Index] {
				return res, invalid
			}
			used[*item.Index] = true
			res.Ranked = append(res.Ranked, domain.RankedText{Index: *item.Index, Score: *item.Score, Text: docs[*item.Index]})
		}
	default:
		return res, invalid
	}
	return res, nil
}
