//go:build integration

package platform

import (
	"context"
	"net/url"
	"os"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

// Read the public form with a fresh in-memory cookie jar. No account, saved
// session, or credential submission is involved in this compatibility check.
func TestLiveUniversityPasswordForm(t *testing.T) {
	if os.Getenv("BUPT_LIVE_SSO_FORM_TEST") != "1" {
		t.Skip("set BUPT_LIVE_SSO_FORM_TEST=1 to read the public CAS page")
	}
	const platformURL = "https://gpuhub.bupt.edu.cn"
	c, err := New(domain.Profile{Name: "form-test", PlatformURL: platformURL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	policy, err := c.ssoPolicy()
	if err != nil {
		t.Fatal(err)
	}
	service := platformURL + "/auth"
	target := casOrigin + "/authserver/login?" + url.Values{"service": {service}}.Encode()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	r, err := c.ssoGET(ctx, policy, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	body, err := ssoBody(r)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parseCASPasswordForm(body, r.Request.URL)
	if err != nil {
		t.Fatal(err)
	}
	if f.Execution == "" || r.Request.URL.Query().Get("service") != service {
		t.Fatal("public CAS form lost its platform binding")
	}
	if f.CaptchaID != "" {
		if _, err := c.campusCaptcha(ctx, policy, f.CaptchaID); err != nil {
			t.Fatal(err)
		}
	}
}
