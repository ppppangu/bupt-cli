package platform

import (
	"context"
	"encoding/json"
	"net/url"

	"bupt.local/cli/internal/domain"
)

func (c *Client) Config(ctx context.Context) (domain.PlatformConfig, error) {
	var ui, meter map[string]any
	if e := c.jsonRequest(ctx, "GET", "/data/config.json", nil, &ui); e != nil {
		return domain.PlatformConfig{}, e
	}
	if e := c.jsonRequest(ctx, "GET", "/api/meter/config", nil, &meter); e != nil {
		return domain.PlatformConfig{}, e
	}
	v, e := unwrap(meter)
	if e != nil {
		return domain.PlatformConfig{}, e
	}
	app := cleanMap(object(ui["app"]))
	if app == nil {
		app = map[string]any{}
	}
	if dataset := object(ui["dataset"]); dataset != nil {
		app["dataset"] = portalValue(dataset, "showInUserMenu")
	}
	var enabled map[string]bool
	if features := object(ui["features"]); features != nil {
		enabled = map[string]bool{}
		for _, key := range []string{"app", "hpc", "billing", "dataset", "pool", "remoteApp", "aiModel", "paper"} {
			enabled[key], _ = object(features[key])["enable"].(bool)
		}
		if v, ok := object(features["billing"])["prepayment"].(bool); ok {
			enabled["prepayment"] = v
		}
	}
	return domain.PlatformConfig{Features: enabled, App: app, Meter: cleanMap(object(v)), Version: stringValue(ui["version"])}, nil
}
func parseSpec(m map[string]any) domain.Spec {
	sid := id(m["specEid"])
	if sid == "" {
		sid = id(m["eid"])
	}
	name := stringValue(m["name"])
	if name == "" {
		name = stringValue(m["specName"])
	}
	version := stringValue(m["specVersion"])
	if version == "" {
		version = stringValue(m["version"])
	}
	description := stringValue(m["description"])
	if description == "" {
		description = stringValue(m["desc"])
	}
	return domain.Spec{ID: sid, Name: name, Version: version, Description: description, Options: pick(m, "eid", "specEid", "nodeRoleConstraints", "resourceConstraints", "executors", "executorSpecs", "spec", "meta", "canCustom", "tags", "expDuration", "state", "nodeSpecs", "domain", "autoApprove", "scope", "visibility", "poolName", "poolDisplayName", "storageName", "billByMonth", "disabled", "resourceSummary")}
}
func (c *Client) Specs(ctx context.Context) ([]domain.Spec, error) {
	var raw any
	if e := c.jsonRequest(ctx, "GET", "/api/core/app-spec-refs", nil, &raw); e != nil {
		return nil, e
	}
	if m := object(raw); m != nil {
		var e error
		raw, e = unwrap(m)
		if e != nil {
			return nil, e
		}
	}
	if m := object(raw); m != nil {
		if v, ok := m["items"]; ok {
			raw = v
		} else if v, ok := m["appSpecRefs"]; ok {
			raw = v
		}
	}
	result := []domain.Spec{}
	for _, v := range array(raw) {
		if m := object(v); m != nil {
			result = append(result, parseSpec(m))
		}
	}
	return result, nil
}
func (c *Client) Spec(ctx context.Context, sid domain.ID) (domain.Spec, error) {
	if sid == "" {
		return domain.Spec{}, domain.E("INVALID_ID", "specification ID is required")
	}
	var m map[string]any
	if e := c.jsonRequest(ctx, "GET", "/api/core/app-spec-refs/"+url.PathEscape(string(sid)), nil, &m); e != nil {
		return domain.Spec{}, e
	}
	v, e := unwrap(m)
	if e != nil {
		return domain.Spec{}, e
	}
	return parseSpec(object(v)), nil
}
func (c *Client) Estimate(ctx context.Context, sid domain.ID, constraints map[string]any) (map[string]any, error) {
	b, e := json.Marshal(constraints)
	if e != nil {
		return nil, e
	}
	q := url.Values{"appSpecEid": {string(sid)}, "resourceConstraints": {string(b)}}
	var m map[string]any
	if e = c.jsonRequest(ctx, "GET", "/api/meter/billing/app-estimated-cost?"+q.Encode(), nil, &m); e != nil {
		return nil, e
	}
	v, e := unwrap(m)
	if e != nil {
		return nil, e
	}
	if obj := object(v); obj != nil {
		return cleanMap(obj), nil
	}
	return map[string]any{"points_per_day": v}, nil
}
