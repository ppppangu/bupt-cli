package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

const SchoolPortal = "http://10.3.68.122:30088"

func (c *Client) PortalClient() (*Client, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, domain.E("CLOSED", "platform client is closed")
	}
	if c.portalPeer != nil {
		return c.portalPeer, nil
	}
	native := c.profile.PortalURL
	if native == "" {
		p, e := c.ssoPolicy()
		if e != nil || !strings.EqualFold(p.native.Hostname(), "gpuhub.bupt.edu.cn") {
			return nil, domain.E("PORTAL_NOT_CONFIGURED", "configure the trusted portal_url for this profile")
		}
		native = SchoolPortal
		if c.gateway == nil {
			native = "https://myai.bupt.edu.cn"
		}
	}
	u, e := url.Parse(native)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || (u.Scheme != "https" && (u.Scheme != "http" || c.gateway == nil || c.gateway.Scheme != "https")) {
		return nil, domain.E("PORTAL_CONFIG", "门户需要 HTTPS，或通过已配置的 HTTPS WebVPN 访问。")
	}
	u.Path = ""
	p := c.profile
	p.PlatformURL = u.String()
	base := u
	if c.gateway != nil {
		v, err := ProxyURL(c.gateway.String(), u.String())
		if err != nil {
			return nil, err
		}
		base, _ = url.Parse(v)
	}
	c.portalPeer = &Client{profile: p, parent: c, vault: c.vault, http: c.http, cookies: c.cookies, base: base, gateway: c.gateway}
	return c.portalPeer, nil
}

// Standard browser JSON requests only. A write is never replayed on a redirect.
func (c *Client) portalRequest(ctx context.Context, method, path string, body any, out any) error {
	c.mu.Lock()
	paused := time.Until(c.retryPortalAfter)
	c.mu.Unlock()
	if paused > 0 {
		return &domain.Error{Code: "RATE_LIMITED", Message: fmt.Sprintf("学校门户限流，请在 %d 秒后重试。", int(paused.Seconds())+1), Retryable: true}
	}
	var in io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		in = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), in)
	if err != nil {
		return domain.E("PORTAL_CONFIG", "invalid portal request")
	}
	req.Header.Set("Accept", "application/json")
	if method != http.MethodGet {
		// Match the portal page origin. WebVPN owns routing to its target.
		req.Header.Set("Origin", req.URL.Scheme+"://"+req.URL.Host)
		req.Header.Set("Referer", c.endpoint("/apikeys"))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return domain.E("PORTAL_NETWORK", "学校门户连接失败。")
	}
	defer resp.Body.Close()
	if err = c.persist(); err != nil {
		return err
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		if u, e := resp.Location(); e == nil && c.gateway != nil && u.Host == c.gateway.Host && strings.HasPrefix(u.Path, "/login") {
			return domain.E("GATEWAY_AUTH_EXPIRED", "WebVPN 需要认证，请运行 bupt login。")
		}
		return domain.E("AUTH_EXPIRED", "门户会话需要统一认证，请运行 bupt login。")
	}
	switch resp.StatusCode {
	case 401:
		return domain.E("AUTH_EXPIRED", "门户会话已过期，请运行 bupt login。")
	case 403:
		var rejection struct{ Error string }
		_ = json.NewDecoder(io.LimitReader(resp.Body, 8192)).Decode(&rejection)
		reason := strings.ToLower(rejection.Error)
		if strings.Contains(reason, "origin") || strings.Contains(reason, "csrf") {
			return domain.E("PORTAL_ORIGIN_REJECTED", "学校门户未接受当前访问来源。")
		}
		if strings.Contains(reason, "consent") || strings.Contains(reason, "agreement") {
			return domain.E("AGREEMENT_REQUIRED", "请用 bupt agreement 查看学校要求的服务协议。")
		}
		return domain.E("FORBIDDEN", "学校未授权当前账号执行此操作。")
	case 404:
		return domain.E("NOT_FOUND", "学校门户没有找到所选对象。")
	case 429:
		delay := retryDelay(resp.Header.Get("Retry-After"))
		c.mu.Lock()
		c.retryPortalAfter = time.Now().Add(delay)
		c.mu.Unlock()
		return &domain.Error{Code: "RATE_LIMITED", Message: "学校门户要求降低访问频率。", Hint: fmt.Sprintf("请在 %d 秒后重试。", int(delay.Seconds())+1), Retryable: true}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.E(fmt.Sprintf("PORTAL_HTTP_%d", resp.StatusCode), "学校门户未完成此次请求。")
	}
	if out == nil || resp.StatusCode == 204 {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil {
		return err
	}
	if len(data) > maxJSON {
		return domain.E("PORTAL_PROTOCOL", "门户响应过大。")
	}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("<")) {
		return domain.E("GATEWAY_AUTH_EXPIRED", "门户返回了登录页，请运行 bupt login。")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err = dec.Decode(out); err != nil {
		return domain.E("PORTAL_PROTOCOL", "门户响应未提供预期的 JSON。")
	}
	return nil
}
func retryDelay(value string) time.Duration {
	if n, err := strconv.ParseInt(value, 10, 32); err == nil && n >= 0 {
		return time.Duration(n) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil && until.After(time.Now()) {
		return time.Until(until)
	}
	return time.Minute
}
func (c *Client) portalCASTarget(ctx context.Context, policy *ssoPolicy) (*url.URL, error) {
	req, e := http.NewRequestWithContext(ctx, "GET", c.endpoint("/api/auth/cas/login"), nil)
	if e != nil {
		return nil, e
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, e := client.Do(req)
	if e != nil {
		return nil, domain.E("PORTAL_NETWORK", "无法取得门户统一认证入口。")
	}
	defer resp.Body.Close()
	if e = c.persist(); e != nil {
		return nil, e
	}
	u, e := resp.Location()
	if e != nil || resp.StatusCode < 300 || resp.StatusCode >= 400 {
		return nil, domain.E("SSO_PROTOCOL", "门户没有返回统一认证入口。")
	}
	u, e = policy.logical(u)
	if e != nil {
		return nil, e
	}
	if !isCASLoginURL(u) {
		return nil, domain.E("SSO_REDIRECT_DENIED", "门户返回的统一认证地址无效。")
	}
	service, e := url.Parse(u.Query().Get("service"))
	if e != nil {
		return nil, domain.E("SSO_PROTOCOL", "门户回调地址无效。")
	}
	dest, e := policy.logical(service)
	if e != nil || !policy.isNative(dest) {
		return nil, domain.E("SSO_REDIRECT_DENIED", "统一认证回调与所选门户不匹配。")
	}
	if c.vault != nil {
		if e = c.vault.Put(c.key("sso_url"), u.String()); e != nil {
			return nil, e
		}
	}
	return u, nil
}
func (c *Client) readPortalAccount(ctx context.Context) (domain.PortalAccount, error) {
	var v struct {
		User struct {
			SID                 domain.ID `json:"sid"`
			Name, College, Role string
			IsAdmin             *bool
			Permissions         []string
		}
		Balance, FreeQuota *float64
		ConsentRequired    *bool
	}
	err := c.portalRequest(ctx, "GET", "/api/me", nil, &v)
	if err != nil {
		return domain.PortalAccount{}, err
	}
	if v.User.SID == "" || v.ConsentRequired == nil {
		return domain.PortalAccount{}, domain.E("PORTAL_PROTOCOL", "门户未返回可验证的用户身份。")
	}
	account := domain.PortalAccount{Role: v.User.Role, Admin: v.User.IsAdmin, Permissions: v.User.Permissions, SID: string(v.User.SID), Name: v.User.Name, College: v.User.College, Balance: v.Balance, FreeQuota: v.FreeQuota, ConsentRequired: *v.ConsentRequired}
	if c.parent != nil {
		c.parent.mu.Lock()
		campus := c.parent.identity.CampusID
		c.parent.mu.Unlock()
		if campus != "" && campusSubject(campus) != campusSubject(account.SID) {
			return domain.PortalAccount{}, domain.E("IDENTITY_MISMATCH", "门户账号与已验证的算力账号不一致。")
		}
	}
	if c.vault != nil {
		var prior string
		if e := c.vault.Get(c.key("subject"), &prior); e == nil && prior != account.SID {
			return domain.PortalAccount{}, domain.E("IDENTITY_MISMATCH", "门户账号与此 profile 绑定的校园身份不一致。请为其他账号选择独立 profile。")
		} else if e != nil && !ssoMissing(e) {
			return domain.PortalAccount{}, e
		}
		if prior == "" {
			if err := c.vault.Put(c.key("subject"), account.SID); err != nil {
				return domain.PortalAccount{}, err
			}
		}
	}
	c.mu.Lock()
	c.portalObserved = account
	c.mu.Unlock()
	return account, nil
}

// Exchange the current CAS cookie once. A query never starts device approval.
func (c *Client) exchangeService(ctx context.Context) error {
	c.serviceMu.Lock()
	defer c.serviceMu.Unlock()
	if time.Since(c.lastServiceAttempt) < time.Minute {
		return domain.E("AUTH_REQUIRED", "学校要求本人认证，请运行 bupt login。")
	}
	c.lastServiceAttempt = time.Now()
	policy, e := c.ssoPolicy()
	if e != nil {
		return e
	}
	target, e := c.ssoLoginTarget(ctx, policy)
	if e != nil {
		return e
	}
	r, e := c.ssoGET(ctx, policy, target.String(), nil)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if isCASLoginURL(r.Request.URL) {
		return domain.E("AUTH_REQUIRED", "学校要求本人确认，请运行 bupt login。")
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return domain.E("SSO_HTTP", "统一认证回调未完成。")
	}
	return nil
}
func (c *Client) PortalAccount(ctx context.Context) (domain.PortalAccount, error) {
	a, e := c.readPortalAccount(ctx)
	if !platformSessionExpired(e) {
		return a, e
	}
	if e = c.exchangeService(ctx); e != nil {
		return a, e
	}
	return c.readPortalAccount(ctx)
}
func portalNumber(v any) *float64 {
	if v == nil {
		return nil
	}
	n, e := strconv.ParseFloat(fmt.Sprint(v), 64)
	if e != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}
func portalValue(m map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := m[k]; ok {
			out[k] = v
		}
	}
	return out
}
func portalID(id string) (string, error) {
	if id == "" || id == "." || id == ".." || len(id) > 256 || strings.ContainsAny(id, "/\\?#\x00\r\n") {
		return "", domain.E("INVALID_ID", "所选对象 ID 无效。")
	}
	return url.PathEscape(id), nil
}
func (c *Client) PortalKeys(ctx context.Context) ([]domain.PortalKey, error) {
	var doc any
	if e := c.portalRequest(ctx, "GET", "/api/keys", nil, &doc); e != nil {
		return nil, e
	}
	if m := object(doc); m != nil {
		doc = m["items"]
		if total, ok := m["total"]; ok && integer(total) > int64(len(array(doc))) {
			return nil, domain.E("PORTAL_PROTOCOL", "门户返回的密钥列表不完整。")
		}
	}
	items, ok := doc.([]any)
	if !ok {
		return nil, domain.E("PORTAL_PROTOCOL", "门户密钥列表结构未知。")
	}
	out := make([]domain.PortalKey, 0, len(items))
	for _, v := range items {
		m := object(v)
		k := domain.PortalKey{ID: stringValue(m["id"]), Name: stringValue(m["name"]), Status: stringValue(m["status"]), Used: portalNumber(m["used"]), CreatedAt: stringValue(m["createdAt"])}
		if _, e := portalID(k.ID); e != nil {
			return nil, domain.E("PORTAL_PROTOCOL", "门户密钥记录缺少有效 ID。")
		}
		if s := stringValue(m["masked"]); strings.ContainsAny(s, "*•…") || strings.Contains(s, "...") {
			k.Masked = s
		}
		for _, m := range array(m["models"]) {
			if s, ok := m.(string); ok {
				k.Models = append(k.Models, s)
			}
		}
		out = append(out, k)
	}
	return out, nil
}
func (c *Client) portalModels(ctx context.Context, page *domain.PortalPage) error {
	var models []map[string]any
	if e := c.portalRequest(ctx, "GET", "/api/models", nil, &models); e != nil {
		return e
	}
	var health map[string]any
	hErr := c.portalRequest(ctx, "GET", "/api/status", nil, &health)
	if hErr != nil {
		page.Warnings = append(page.Warnings, "模型健康数据暂不可用")
	}
	byID := map[string]map[string]any{}
	for _, v := range array(health["targets"]) {
		m := object(v)
		byID[stringValue(m["id"])] = m
	}
	page.Data = portalValue(health, "generatedAt", "staleAfterMs", "coverage", "availability", "counts", "overall")
	if stamp, err := time.Parse(time.RFC3339, stringValue(health["generatedAt"])); err == nil {
		if expiry := integer(health["staleAfterMs"]); expiry > 0 && time.Since(stamp) > time.Duration(expiry)*time.Millisecond {
			page.Warnings = append(page.Warnings, "健康数据已超过学校的更新周期，当前显示最近一次采样。")
		}
	}
	for _, m := range models {
		id := stringValue(m["id"])
		if id == "" {
			return domain.E("PORTAL_PROTOCOL", "模型记录缺少调用 ID。")
		}
		h := byID[id]
		model := domain.PortalModel{ID: id, Name: stringValue(m["name"]), Description: stringValue(m["desc"]), Source: stringValue(m["source"]), Vendor: stringValue(m["vendor"]), Type: stringValue(m["type"]), Kind: stringValue(h["kind"]), Context: stringValue(m["ctx"]), InputPrice: portalNumber(m["inPrice"]), OutputPrice: portalNumber(m["outPrice"]), Health: stringValue(h["state"]), Coverage: stringValue(h["probeCoverage"]), SampledAt: stringValue(h["lastSampledAt"]), LatencyMS: portalNumber(h["lastLatencyMs"])}
		if model.Health == "" {
			model.Health = stringValue(h["status"])
		}
		if model.Health == "" {
			model.Health = "unknown"
		}
		if model.Kind == "" {
			model.Kind = model.Type
		}
		for _, b := range array(h["buckets"]) {
			model.History = append(model.History, portalValue(object(b), "hourStart", "ok", "failed", "missing", "state"))
		}
		page.Models = append(page.Models, model)
	}
	return nil
}
func (c *Client) PortalPage(ctx context.Context, a domain.PortalAccount, q domain.PortalInput) (domain.PortalPage, error) {
	p := domain.PortalPage{Section: q.Section, Source: "live", ObservedAt: time.Now().UTC(), Account: &a}
	if a.ConsentRequired && q.Section != "agreement" && q.Section != "account" {
		return p, domain.E("AGREEMENT_REQUIRED", "门户要求确认当前服务协议。请运行 bupt agreement 阅读。")
	}
	var err error
	switch q.Section {
	case "account":
	case "admin":
		if a.Admin == nil {
			return p, domain.E("ADMIN_SCOPE_UNVERIFIED", "学校门户没有为当前会话发布可核验的管理员身份。")
		}
		if !*a.Admin {
			return p, domain.E("ADMIN_SCOPE_REQUIRED", "学校门户未授予此账号管理员权限。")
		}
		p.Data = map[string]any{"role": a.Role, "permissions": a.Permissions}
	case "model":
		err = c.portalModels(ctx, &p)
	case "key":
		p.Keys, err = c.PortalKeys(ctx)
	case "usage", "overview":
		days := q.Days
		if days == 0 {
			days = 30
		}
		if days < 1 || days > 30 {
			return p, domain.E("INVALID_PERIOD", "请选择 1 到 30 天。")
		}
		var data map[string]any
		err = c.portalRequest(ctx, "GET", "/api/usage?days="+strconv.Itoa(days), nil, &data)
		p.Data = portalValue(data, "usageAvailable", "periodCostPoints", "periodCalls", "periodTokens", "successRate", "inputSeries", "outputSeries", "modelDistribution", "days", "dates")
		if p.Data["usageAvailable"] == false {
			p.Data = map[string]any{"usageAvailable": false}
		}
		p.Data["days"] = days
		if q.Section == "overview" && err == nil {
			p.Keys, err = c.PortalKeys(ctx)
		}
	case "guide":
		var data struct{ PublicBaseURL string }
		err = c.portalRequest(ctx, "GET", "/api/config/gateway", nil, &data)
		if err == nil {
			p.Data, err = ModelGuideData(data.PublicBaseURL, c.profile.PortalURL)
		}
	case "leaderboard", "preference", "agreement", "news", "news.show":
		endpoints := map[string]string{"leaderboard": "/api/leaderboard", "preference": "/api/me/leaderboard-display", "agreement": "/api/agreement"}
		path := endpoints[q.Section]
		if q.Section == "news" {
			v := url.Values{}
			v.Set("limit", strconv.Itoa(q.Limit))
			if q.Limit <= 0 {
				v.Set("limit", "20")
			}
			if q.Cursor != "" {
				v.Set("cursor", q.Cursor)
			}
			if q.Only != "" {
				v.Set("only", q.Only)
			}
			if q.Order != "" {
				v.Set("order", q.Order)
			}
			path = "/api/news?" + v.Encode()
		}
		if q.Section == "news.show" {
			s, e := portalID(q.Target)
			if e != nil {
				return p, e
			}
			path = "/api/news/" + s
		}
		var data map[string]any
		err = c.portalRequest(ctx, "GET", path, nil, &data)
		p.Data, _ = publicPortalValue(data).(map[string]any)
	default:
		return p, domain.E("UNKNOWN_SECTION", "未知门户栏目。")
	}
	if q.Section != "account" && q.Section != "usage" && q.Section != "overview" && q.Section != "agreement" {
		p.Account = nil
	}
	return p, err
}

func (c *Client) PortalChange(ctx context.Context, m domain.PortalChange) (domain.PortalKey, string, error) {
	var k domain.PortalKey
	if m.Action == "key.create" {
		if strings.TrimSpace(m.Name) == "" || len(m.Models) == 0 {
			return k, "", domain.E("INVALID_KEY", "请填写 Key 名称并至少选择一个模型。")
		}
		var v struct {
			ID                domain.ID
			Name, Key, Status string
		}
		e := c.portalRequest(ctx, "POST", "/api/keys", map[string]any{"name": m.Name, "modelIds": m.Models}, &v)
		if e != nil {
			return k, "", e
		}
		if _, e = portalID(string(v.ID)); e != nil || v.Key == "" {
			return k, "", domain.E("KEY_CREATION_UNCERTAIN", "学校可能已生成 Key，但未返回完整凭据。请先刷新密钥列表。")
		}
		return domain.PortalKey{ID: string(v.ID), Name: m.Name, Status: v.Status, Models: m.Models}, v.Key, nil
	}
	var err error
	switch m.Action {
	case "key.enable", "key.disable", "key.delete":
		id, e := portalID(m.Target)
		if e != nil {
			return k, "", e
		}
		keys, e := c.PortalKeys(ctx)
		if e != nil {
			return k, "", e
		}
		var found bool
		for _, item := range keys {
			if item.ID == m.Target {
				k = item
				found = true
				break
			}
		}
		if !found {
			return k, "", domain.E("NOT_FOUND", "当前账号的密钥列表没有这个 ID。")
		}
		if m.Action == "key.delete" {
			err = c.portalRequest(ctx, "DELETE", "/api/keys/"+id, nil, nil)
			break
		}
		desired := "active"
		if m.Action == "key.disable" {
			desired = "disabled"
		}
		if k.Status == desired {
			return k, "", nil
		}
		if k.Status != "active" && k.Status != "disabled" {
			return k, "", domain.E("KEY_STATE", "当前密钥推送状态不确定，请刷新后核对。")
		}
		err = c.portalRequest(ctx, "POST", "/api/keys/"+id+"/toggle", nil, nil)
		if err == nil {
			updated, e := c.PortalKeys(ctx)
			if e != nil {
				return k, "", e
			}
			for _, item := range updated {
				if item.ID == m.Target && item.Status == desired {
					return item, "", nil
				}
			}
			err = domain.E("KEY_STATE", "学校尚未确认目标密钥状态。")
		}
	case "preference.set":
		if m.Mode != "anonymous" && m.Mode != "partial" && m.Mode != "real" {
			return k, "", domain.E("INVALID_MODE", "请选择 anonymous、partial 或 real。")
		}
		err = c.portalRequest(ctx, "PUT", "/api/me/leaderboard-display", map[string]string{"mode": m.Mode}, nil)
	case "agreement.accept":
		if m.Target == "" {
			return k, "", domain.E("INVALID_ID", "请指定已阅读的协议版本 ID。")
		}
		err = c.portalRequest(ctx, "POST", "/api/agreement/accept", map[string]any{"agreementId": m.Target, "accepted": true}, nil)
	case "news.read":
		body := map[string]any{"ids": []string{m.Target}}
		if m.Target == "all" {
			body = map[string]any{"all": true}
		} else if _, e := portalID(m.Target); e != nil {
			return k, "", e
		}
		err = c.portalRequest(ctx, "POST", "/api/news/read", body, nil)
	default:
		return k, "", domain.E("UNKNOWN_ACTION", "未知门户操作。")
	}
	return k, "", err
}
func isCode(e error, code string) bool {
	var d *domain.Error
	return errors.As(e, &d) && d.Code == code
}

func (c *Client) VerifiedPortalAccount() (domain.PortalAccount, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.portalObserved, c.parent != nil && c.portalObserved.SID != ""
}

// The portal namespaces CAS accounts as cas_<student number>. Temp accounts
// and nonnumeric identifiers keep their distinct identities.
func campusSubject(s string) string {
	if number := domain.CampusNumber(s); number != "" {
		return number
	}
	return s
}

func (c *Client) PortalLogin() (domain.PlatformAPI, error) { return c.PortalClient() }

func publicPortalValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range x {
			if sensitive(k) {
				switch strings.ToLower(k) {
				case "tokens", "totaltokens", "periodtokens", "inputtokens", "outputtokens", "total_tokens", "input_tokens", "output_tokens":
					if _, ok := v.(json.Number); ok {
						out[k] = v
					}
				}
				continue
			}
			out[k] = publicPortalValue(v)
		}
		return out
	case []any:
		out := make([]any, 0, len(x))
		for _, v := range x {
			out = append(out, publicPortalValue(v))
		}
		return out
	default:
		return x
	}
}

// ModelGuideData normalizes live and cached guides using the same trusted
// public destination as a managed trial, before applying any WebVPN route.
func ModelGuideData(base, portal string) (map[string]any, error) {
	u, err := publicModelGateway(base, portal)
	if err != nil {
		return nil, err
	}
	req, err := (domain.PortalModel{ID: "MODEL_ID", Kind: "chat"}).TrialRequest(domain.TrialInput{Texts: []string{"你好"}})
	if err != nil {
		return nil, err
	}
	data := map[string]any{"base_url": u.String(), "protocol": "OpenAI compatible", "network": "使用门户公布的公网接入地址"}
	for _, format := range []string{"curl", "python"} {
		code, err := req.Code(format, u.String())
		if err != nil {
			return nil, err
		}
		if format == "python" {
			code = strings.Replace(code, `"model": "MODEL_ID"`, `"model": os.environ["BUPT_MODEL"]`, 1)
		}
		data[format] = code
	}
	return data, nil
}
