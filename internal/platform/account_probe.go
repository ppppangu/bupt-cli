package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

// ProbeAccountStatus reads the two protected endpoints using an isolated
// cookie jar. In particular it must never exchange a CAS ticket just to
// refresh the status bar, or visit a login page which can mint a QR challenge.
func (c *Client) ProbeAccountStatus(ctx context.Context) (domain.AccountObservation, error) {
	result := domain.AccountObservation{VPN: "not_configured", Platform: "unknown"}
	client := *c.http
	client.Jar = clonedProbeJar(c.cookies.snapshot())
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	probeCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()

	if c.gateway != nil {
		obs, delay := accountProbeGET(probeCtx, &client, c.gateway.JoinPath("/user/portal/collections").String(), c.gateway)
		gateway := classifyGatewayProbe(obs)
		result.VPN, result.Reason, result.RetryAfter = gateway.State, gateway.Reason, delay
		if obs.status == http.StatusUnauthorized {
			result.VPN, result.Reason = "expired", "gateway_login_required"
		}
		if result.VPN != "authenticated" {
			if result.VPN == "expired" {
				result.Platform = "blocked_by_gateway"
			}
			return result, ctx.Err()
		}
	}
	portal, err := c.PortalClient()
	if err != nil {
		if isCode(err, "PORTAL_NOT_CONFIGURED") {
			result.Platform, result.Reason = "not_configured", "portal_not_configured"
			return result, nil
		}
		return result, err
	}
	portal.mu.Lock()
	remaining := time.Until(portal.retryPortalAfter)
	rememberedSID := portal.portalObserved.SID
	portal.mu.Unlock()
	if remaining > 0 {
		result.Reason, result.RetryAfter = "rate_limited", remaining
		return result, nil
	}
	obs, delay := accountProbeGET(probeCtx, &client, portal.endpoint("/api/me"), c.gateway)
	result.Reason, result.RetryAfter = obs.reason, delay
	if obs.reason != "" {
		return result, ctx.Err()
	}
	if c.gateway != nil && (obs.gatewayLogin || bytes.HasPrefix(bytes.TrimSpace(obs.body), []byte("<"))) {
		if gateway := classifyGatewayProbe(obs); gateway.State == "expired" {
			result.VPN, result.Platform, result.Reason = "expired", "blocked_by_gateway", gateway.Reason
			return result, nil
		}
	}
	if obs.casRedirect || obs.status == http.StatusUnauthorized {
		result.Platform, result.Reason = "expired", "portal_login_required"
		return result, nil
	}
	var doc struct {
		User struct {
			SID  domain.ID
			Name string
		}
		Balance         *float64
		ConsentRequired *bool
	}
	if obs.status != http.StatusOK || json.Unmarshal(obs.body, &doc) != nil || doc.User.SID == "" || doc.ConsentRequired == nil {
		result.Reason = "unrecognized_portal_response"
		return result, nil
	}

	var pinnedSID string
	if c.vault != nil {
		if err = c.vault.Get(portal.key("subject"), &pinnedSID); err != nil && !ssoMissing(err) {
			return result, err
		}
	}
	c.mu.Lock()
	campus := c.identity.CampusID
	c.mu.Unlock()
	sid := string(doc.User.SID)
	if pinnedSID == "" && rememberedSID == "" && campus == "" {
		result.Reason = "identity_unpinned"
		return result, nil
	}
	if pinnedSID != "" && sid != pinnedSID || rememberedSID != "" && sid != rememberedSID || campus != "" && campusSubject(campus) != campusSubject(sid) {
		result.Reason = "identity_mismatch"
		return result, nil
	}
	result.Platform, result.Reason = "authenticated", "identity_verified"
	result.CampusID, result.Name, result.Balance = campusSubject(sid), doc.User.Name, doc.Balance
	return result, nil
}

// Unlike the diagnostic probe this does not follow even same-origin redirects.
// Both endpoints have fixed final paths; a redirect is itself the observation.
func accountProbeGET(ctx context.Context, client *http.Client, target string, gateway *url.URL) (sessionProbeObservation, time.Duration) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return sessionProbeObservation{reason: "invalid_probe_target"}, 0
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return sessionProbeObservation{reason: "network_error"}, 0
	}
	defer resp.Body.Close()
	o := sessionProbeObservation{status: resp.StatusCode, path: safeProbePath(req.URL)}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		o.reason = "rate_limited"
		return o, retryDelay(resp.Header.Get("Retry-After"))
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		location, err := resp.Location()
		if err == nil {
			upgradeGatewayHTTP(location, gateway)
		}
		switch {
		case err != nil:
			o.reason = "invalid_redirect"
		case sameProbeOrigin(location, gateway) && location.Path == "/login":
			o.gatewayLogin = true
		case accountCASRedirect(location, gateway):
			o.casRedirect = true
		case sameProbeOrigin(req.URL, location) && strings.HasSuffix(req.URL.Path, "/api/me"):
			base := strings.TrimSuffix(req.URL.Path, "/api/me")
			if location.Path == base+"/login" || location.Path == base+"/api/auth/cas/login" {
				o.casRedirect = true
			} else {
				o.reason = "redirect_outside_probe_scope"
			}
		default:
			o.reason = "redirect_outside_probe_scope"
		}
	case resp.StatusCode == http.StatusForbidden:
		o.reason = "access_denied"
	case resp.StatusCode >= 500:
		o.reason = "service_unavailable"
	case resp.StatusCode == http.StatusOK:
		const maxBody = 2 << 20
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
		switch {
		case err != nil:
			o.reason = "response_read_failed"
		case len(body) > maxBody:
			o.reason = "response_too_large"
		default:
			o.body = body
		}
	}
	return o, 0
}

// Decode a wrapped destination only when the outer URL is our HTTPS gateway.
func accountCASRedirect(location, gateway *url.URL) bool {
	if sameProbeOrigin(location, gateway) && location.Scheme == "https" {
		inner, err := decodeProxyTarget(location)
		return err == nil && isCASLoginURL(inner)
	}
	return isCASLoginURL(location)
}
