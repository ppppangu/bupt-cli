package platform

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

const campusFormHTML = `<form method='POST' id="loginForm" %s>
<input name="username"><input type="password" name="password">
<input name="submit" type="submit" value="登录"><input name="type" value="username_password">
<input value="opaque&amp;fresh" name="execution"><input name="_eventId" value="submit">%s</form>`

func TestCampusPasswordCASRoundTrip(t *testing.T) {
	const username = "campus-student"
	const password = "测试 +&=% password"
	for _, tc := range []struct {
		name, result, want string
		captcha            bool
	}{
		{name: "success"}, {name: "human captcha", captcha: true},
		{name: "wrong credentials", result: "rejected", want: "SSO_PASSWORD_REJECTED"},
		{name: "additional verification", result: "further", want: "SSO_INTERACTIVE_REQUIRED"},
		{name: "identity mismatch", result: "mismatch", want: "IDENTITY_MISMATCH"},
		{name: "no replay on 307", result: "307", want: "SSO_REDIRECT_DENIED"},
		{name: "outside redirect", result: "outside", want: "SSO_REDIRECT_DENIED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSSOFixture(t)
			if tc.result == "mismatch" {
				f.uid = "8"
			}
			var submissions, imageReads atomic.Int32
			extra := ""
			if tc.captcha {
				extra = `<input name="captcha"><img src="/authserver/captcha?captchaId=manual-123">`
			}
			f.passwordCAS = func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.String(), "campus-student") || strings.Contains(r.Header.Get("Referer"), "opaque") {
					t.Error("credentials or execution token in URL/header")
				}
				if r.URL.Path == "/authserver/captcha" && tc.captcha {
					imageReads.Add(1)
					if r.URL.Query().Get("captchaId") != "manual-123" {
						t.Error("wrong CAPTCHA challenge")
					}
					if _, err := r.Cookie("CAS_SESSION"); err != nil {
						t.Error("CAPTCHA session lost")
					}
					_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nfixture"))
					return
				}
				if r.URL.Path != "/authserver/login" || r.URL.Query().Get("service") != f.service {
					t.Error("unexpected CAS endpoint")
					w.WriteHeader(400)
					return
				}
				if r.Method == "GET" {
					http.SetCookie(w, &http.Cookie{Name: "CAS_SESSION", Value: "form-session", Secure: true, Path: "/authserver"})
					fmt.Fprintf(w, campusFormHTML, "", extra)
					return
				}
				submissions.Add(1)
				cookie, e := r.Cookie("CAS_SESSION")
				if e != nil || cookie.Value != "form-session" || r.Method != "POST" || r.Header.Get("Origin") != casOrigin || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || r.Header.Get("Referer") != f.login {
					t.Error("password POST lost its origin, headers, or CAS session")
				}
				if r.ParseForm() != nil || r.PostForm.Get("password") != password || r.PostForm.Get("username") != username || r.PostForm.Get("execution") != "opaque&fresh" || r.PostForm.Get("_eventId") != "submit" || r.PostForm.Get("type") != "username_password" || r.PostForm.Get("submit") != "登录" || (tc.captcha && r.PostForm.Get("captcha") != "ab12") {
					t.Error("password form did not match official fields")
				}
				switch tc.result {
				case "rejected":
					fmt.Fprintf(w, campusFormHTML, "", "")
				case "further":
					fmt.Fprint(w, `<form id="mfa"><input name="otp"></form>`)
				case "307":
					http.Redirect(w, r, f.service, http.StatusTemporaryRedirect)
				case "outside":
					http.Redirect(w, r, "https://unrelated.example/callback", http.StatusSeeOther)
				default:
					http.Redirect(w, r, f.service+"&ticket=private-st", http.StatusFound)
				}
			}
			v := &memoryVault{}
			c := f.client(t, v, "7")
			form, e := c.BeginSSOPassword(t.Context())
			if e != nil || form == nil || form.Execution != "opaque&fresh" {
				t.Fatalf("prepare: %v", e)
			}
			if tc.captcha != strings.HasPrefix(form.CaptchaImage, "data:image/png;base64,") || tc.captcha != (imageReads.Load() == 1) {
				t.Fatal("incorrect CAPTCHA presentation")
			}
			if submissions.Load() != 0 {
				t.Fatal("password POST before user input")
			}
			e = c.SubmitSSOPassword(t.Context(), form, username, password, "Ab12")
			if tc.want != "" {
				assertCode(t, e, tc.want)
			} else if e != nil {
				t.Fatal(e)
			}
			if submissions.Load() != 1 {
				t.Fatal("password POST count:", submissions.Load())
			}
			if tc.want == "" {
				restored := f.client(t, v, "7")
				if id, err := restored.WhoAmI(t.Context()); err != nil || id.UID != "7" {
					t.Fatal("verified session did not survive restart", err)
				}
				if err := c.SubmitSSOPassword(t.Context(), form, username, password, "Ab12"); err != nil || submissions.Load() != 1 {
					t.Fatal("fresh session prompted another POST", err)
				}
			}
			if _, e := v.m[c.key("credentials")]; e {
				t.Fatal("campus password entered platform credential store")
			}
			for _, raw := range v.m {
				if bytes.Contains(raw, []byte(password)) || bytes.Contains(raw, []byte(username)) {
					t.Fatal("university credentials persisted")
				}
			}
		})
	}
}

func TestCampusPasswordRequiresOfficialFormAndBoundService(t *testing.T) {
	p, _ := url.Parse(casOrigin + "/authserver/login?service=https%3A%2F%2Fgpuhub.bupt.edu.cn%2Fauth")
	for _, tc := range []struct{ name, markup, code string }{
		{"changed origin", fmt.Sprintf(campusFormHTML, `action="https://outside.example/authserver/login"`, ""), "SSO_REDIRECT_DENIED"},
		{"changed service", fmt.Sprintf(campusFormHTML, `action="?service=https://outside.example/"`, ""), "SSO_REDIRECT_DENIED"},
		{"insecure action", fmt.Sprintf(campusFormHTML, `action="http://auth.bupt.edu.cn/authserver/login"`, ""), "SSO_REDIRECT_DENIED"},
		{"unexpected form", `<form id="unrelated"><input type=password></form>`, "SSO_INTERACTIVE_REQUIRED"},
		{"unrecognized factor", fmt.Sprintf(campusFormHTML, "", `<input name="sms_code">`), "SSO_INTERACTIVE_REQUIRED"},
		{"unavailable captcha", fmt.Sprintf(campusFormHTML, "", `<input name="captcha">`), "SSO_INTERACTIVE_REQUIRED"},
		{"form method", strings.Replace(fmt.Sprintf(campusFormHTML, "", ""), "POST", "GET", 1), "SSO_INTERACTIVE_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) { _, e := parseCASPasswordForm([]byte(tc.markup), p); assertCode(t, e, tc.code) })
	}
	f, e := parseCASPasswordForm([]byte(fmt.Sprintf(campusFormHTML, "", "")+`<script>config.captcha = {id:'captcha-2'}</script>`), p)
	if e != nil || f.CaptchaID != "captcha-2" {
		t.Fatal("script-issued CAS CAPTCHA not recognized", e)
	}
}

func TestPlatformSessionCode1032AllowsCampusStep(t *testing.T) {
	f := newSSOFixture(t)
	f.passwordCAS = func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, campusFormHTML, "", "") }
	c := f.client(t, &memoryVault{}, "7")
	tr := c.http.Transport
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		resp, e := tr.RoundTrip(r)
		if e == nil && strings.HasSuffix(r.URL.Path, "/auth/user") {
			resp.StatusCode = 200
		}
		return resp, e
	})
	s := c.AuthStatus(context.Background())
	if s.Platform != "expired" || s.Gateway != "authenticated" {
		t.Fatal(s.Platform, s.Gateway)
	}
	if form, e := c.BeginSSOPassword(t.Context()); e != nil || form == nil {
		t.Fatal("valid gateway did not reach CAS", e)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
