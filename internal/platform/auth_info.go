package platform

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"sort"
	"strings"

	"bupt.local/cli/internal/domain"
)

// AuthInfo exposes public login capabilities, never session values or the full
// authentication redirect URL. It is useful when a school disables local login.
func (c *Client) AuthInfo(ctx context.Context) (map[string]any, error) {
	var ui map[string]any
	if e := c.jsonRequest(ctx, "GET", "/data/config.json", nil, &ui); e != nil {
		return nil, e
	}
	out := map[string]any{"login_features": map[string]any{}}
	features := out["login_features"].(map[string]any)
	roots := []map[string]any{ui, object(ui["features"])}
	for _, root := range roots {
		for _, k := range []string{"authBase", "authSso", "authQrcode", "authLdap", "auth", "login"} {
			if v, ok := root[k]; ok {
				features[k] = cleanValue(v)
			}
		}
	}
	keys := []string{}
	for k := range ui {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out["configuration_keys"] = keys
	response, e := c.do(ctx, "GET", c.endpoint("/auth/user"), nil, nil)
	if e != nil {
		return out, e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 401 {
		return out, c.responseError(response)
	}
	out["http_status"] = response.StatusCode
	var user map[string]any
	d := json.NewDecoder(io.LimitReader(response.Body, maxJSON))
	d.UseNumber()
	if e = d.Decode(&user); e != nil {
		return out, domain.E("PROTOCOL", "authentication metadata was not JSON")
	}
	out["code"] = user["code"]
	for _, key := range []string{"loginFrom", "userState", "username", "uid"} {
		if v, ok := user[key]; ok {
			out[key] = v
		}
	}
	if target := stringValue(user["ssoUrl"]); target != "" {
		u, e := url.Parse(target)
		if e == nil && u.User == nil {
			names := []string{}
			for k := range u.Query() {
				names = append(names, k)
			}
			sort.Strings(names)
			out["sso"] = map[string]any{"origin": safeOrigin(u), "path": u.EscapedPath(), "query_keys": names}
			if c.vault != nil {
				_ = c.vault.Put(c.key("sso_url"), target)
			}
		}
	}
	// URLs in public configuration are described without embedded query values.
	return stripInfoQueries(out).(map[string]any), nil
}
func stripInfoQueries(v any) any {
	switch value := v.(type) {
	case map[string]any:
		for k, x := range value {
			value[k] = stripInfoQueries(x)
		}
	case []any:
		for i, x := range value {
			value[i] = stripInfoQueries(x)
		}
	case string:
		if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
			if u, e := url.Parse(value); e == nil {
				u.RawQuery = ""
				u.Fragment = ""
				u.User = nil
				return u.String()
			}
		}
	}
	return v
}
