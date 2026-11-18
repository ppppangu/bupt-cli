package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"bupt.local/cli/internal/domain"
	"golang.org/x/net/html"
)

// CASPasswordForm stays in the controller's encrypted login flow. Only the
// CAPTCHA image, if required, is presented to the person entering credentials.
type CASPasswordForm struct {
	Action       string `json:"action"`
	Execution    string `json:"execution"`
	Submit       string `json:"submit,omitempty"`
	CaptchaID    string `json:"captcha_id,omitempty"`
	CaptchaImage string `json:"captcha_image,omitempty"`
}

func isCASLoginURL(u *url.URL) bool {
	return u != nil && u.User == nil && u.Scheme == "https" && u.Host == "auth.bupt.edu.cn" && u.EscapedPath() == "/authserver/login"
}

func campusExtraStep() error {
	return domain.E("SSO_INTERACTIVE_REQUIRED", "学校要求额外验证或当前页面未提供可用的密码表单。可运行 bupt login 扫码完成统一认证。")
}

func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

var captchaConfig = regexp.MustCompile(`\bconfig\.captcha\s*=\s*([^;\r\n]*)`)
var captchaIDConfig = regexp.MustCompile(`(?s)\bconfig\.captcha\s*=\s*\{[^}]*?["']?\bid["']?\s*:\s*["']([A-Za-z0-9_-]{1,128})["']`)

func parseCASPasswordForm(body []byte, page *url.URL) (*CASPasswordForm, error) {
	if !isCASLoginURL(page) || page.Query().Get("service") == "" {
		return nil, campusExtraStep()
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, campusExtraStep()
	}
	var form *html.Node
	var captcha bool
	f := &CASPasswordForm{}
	for n := range doc.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		switch n.Data {
		case "form":
			if htmlAttr(n, "id") == "loginForm" {
				if form != nil || !strings.EqualFold(htmlAttr(n, "method"), "post") {
					return nil, campusExtraStep()
				}
				form = n
			}
		case "base":
			if u, err := page.Parse(htmlAttr(n, "href")); err != nil || u.String() != page.String() {
				return nil, campusExtraStep()
			}
		case "script":
			if n.FirstChild != nil && n.FirstChild.Type == html.TextNode {
				config := captchaConfig.FindStringSubmatch(n.FirstChild.Data)
				if config == nil || strings.TrimSpace(config[1]) == "null" || strings.TrimSpace(config[1]) == "false" {
					continue
				}
				captcha = true
				if m := captchaIDConfig.FindStringSubmatch(n.FirstChild.Data); m != nil {
					f.CaptchaID = m[1]
				}
			}
		}
	}
	if form == nil {
		return nil, campusExtraStep()
	}
	action, err := page.Parse(htmlAttr(form, "action"))
	if err != nil || !isCASLoginURL(action) || action.Query().Get("service") != page.Query().Get("service") {
		return nil, domain.E("SSO_REDIRECT_DENIED", "university password form changed its destination or platform service")
	}
	f.Action = action.String()
	fields := map[string]string{}
	for n := range form.Descendants() {
		if n.Type != html.ElementNode {
			continue
		}
		if n.Data == "img" {
			u, err := page.Parse(htmlAttr(n, "src"))
			if err == nil && u.Scheme == "https" && u.Host == "auth.bupt.edu.cn" && u.Path == "/authserver/captcha" {
				captcha = true
				if id := u.Query().Get("captchaId"); ssoOpaqueRE.MatchString(id) {
					f.CaptchaID = id
				}
			}
		}
		if htmlAttr(n, "formaction") != "" {
			return nil, campusExtraStep()
		}
		if n.Data != "input" {
			continue
		}
		name := htmlAttr(n, "name")
		switch name {
		case "":
			continue
		case "captcha":
			captcha = true
		case "username", "password", "type", "execution", "_eventId", "submit":
		default:
			// A changed form may require an additional factor.
			return nil, campusExtraStep()
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, campusExtraStep()
		}
		fields[name] = htmlAttr(n, "value")
	}
	_, username := fields["username"]
	_, password := fields["password"]
	f.Execution, f.Submit = fields["execution"], fields["submit"]
	if !username || !password || fields["type"] != "username_password" || fields["_eventId"] != "submit" || f.Execution == "" || len(f.Execution) > 128<<10 || captcha && f.CaptchaID == "" {
		return nil, campusExtraStep()
	}
	return f, nil
}

func (c *Client) campusCaptcha(ctx context.Context, policy *ssoPolicy, id string) (string, error) {
	target := casOrigin + "/authserver/captcha?" + url.Values{"captchaId": {id}}.Encode()
	r, err := c.ssoGET(ctx, policy, target, nil)
	if err != nil {
		return "", err
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusOK || r.Request.URL.String() != target {
		return "", campusExtraStep()
	}
	image, err := io.ReadAll(io.LimitReader(r.Body, (512<<10)+1))
	if err != nil || len(image) > 512<<10 {
		return "", campusExtraStep()
	}
	kind := http.DetectContentType(image)
	switch kind {
	case "image/jpeg", "image/png", "image/gif":
		return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(image), nil
	default:
		return "", campusExtraStep()
	}
}

// A nil form means the CAS cookie already completed the platform callback.
func (c *Client) BeginSSOPassword(ctx context.Context) (*CASPasswordForm, error) {
	if err := c.CancelSSO(); err != nil {
		return nil, err
	}
	policy, err := c.ssoPolicy()
	if err != nil {
		return nil, err
	}
	target, err := c.ssoLoginTarget(ctx, policy)
	if err != nil {
		return nil, err
	}
	resp, err := c.ssoGET(ctx, policy, target.String(), nil)
	if err != nil {
		return nil, err
	}
	body, err := ssoBody(resp)
	if err != nil {
		return nil, err
	}
	if _, err := c.verifySSO(ctx); err == nil {
		return nil, nil
	} else if !platformSessionExpired(err) {
		return nil, err
	}
	page := resp.Request.URL
	if !isCASLoginURL(page) || page.Query().Get("service") != target.Query().Get("service") {
		return nil, campusExtraStep()
	}
	f, err := parseCASPasswordForm(body, page)
	if err == nil && f.CaptchaID != "" {
		f.CaptchaImage, err = c.campusCaptcha(ctx, policy, f.CaptchaID)
	}
	return f, err
}

func platformSessionExpired(err error) bool {
	var de *domain.Error
	return errors.As(err, &de) && (de.Code == "AUTH_EXPIRED" || de.Code == "AUTH_1032")
}

func (c *Client) SubmitSSOPassword(ctx context.Context, f *CASPasswordForm, username, password, captcha string) error {
	policy, err := c.ssoPolicy()
	if err != nil {
		return err
	}
	// Recheck the configured service before sending a saved form after a restart.
	var target *url.URL
	if c.parent != nil {
		var login string
		if err = c.vault.Get(c.key("sso_url"), &login); err == nil {
			target, err = url.Parse(login)
		}
	} else {
		target, err = c.ssoLoginTarget(ctx, policy)
	}
	if err != nil {
		return err
	}
	action, err := url.Parse(f.Action)
	if err != nil || !isCASLoginURL(action) || action.Query().Get("service") != target.Query().Get("service") {
		return domain.E("SSO_REDIRECT_DENIED", "saved university login form no longer matches the configured service")
	}
	if _, err = c.verifySSO(ctx); err == nil || !platformSessionExpired(err) {
		return err
	}
	fields := url.Values{"username": {username}, "password": {password}, "type": {"username_password"}, "_eventId": {"submit"}, "execution": {f.Execution}}
	if f.Submit != "" {
		fields.Set("submit", f.Submit)
	}
	if f.CaptchaID != "" {
		if strings.TrimSpace(captcha) == "" {
			return domain.E("SSO_CAPTCHA_REQUIRED", "请填写学校统一认证页面的图形验证码。")
		}
		fields.Set("captcha", strings.ToLower(strings.TrimSpace(captcha)))
	}
	headers := http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Origin": {casOrigin}, "Referer": {f.Action}}
	resp, err := c.ssoRequest(ctx, policy, f.Action, headers, fields)
	if err != nil {
		return err
	}
	final := resp.Request.URL
	// CAS also returns the login form with HTTP 401 for rejected credentials.
	if isCASLoginURL(final) && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return campusPasswordRejected()
	}
	if final.Host == "auth.bupt.edu.cn" {
		switch resp.StatusCode {
		case http.StatusForbidden, http.StatusPreconditionFailed:
			resp.Body.Close()
			return campusExtraStep()
		case http.StatusTooManyRequests:
			resp.Body.Close()
			return domain.E("SSO_RATE_LIMITED", "统一认证暂时限制了登录次数，请稍后重试。")
		}
	}
	body, err := ssoBody(resp)
	if err != nil {
		return err
	}
	if _, err = c.verifySSO(ctx); err == nil || !platformSessionExpired(err) {
		return err
	}
	if isCASLoginURL(final) {
		if _, err := parseCASPasswordForm(body, final); err == nil {
			return campusPasswordRejected()
		}
	}
	if u, err := policy.logical(final); err == nil && policy.isNative(u) {
		return domain.E("SSO_VERIFICATION_PENDING", "统一认证已返回算力平台，平台尚未确认当前身份。可用 bupt auth status 检查。")
	}
	return campusExtraStep()
}

func campusPasswordRejected() error {
	return domain.E("SSO_PASSWORD_REJECTED", "统一认证未接受本次提交。请核对账号、密码和图形验证码（如有），再运行 bupt login --sso-password，或用 bupt login 扫码。")
}
