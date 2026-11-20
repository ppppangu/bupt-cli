package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"bupt.local/cli/internal/domain"
)

// This is the separate school's compute marketplace. The checkout code is
// useful only when the user explicitly asks to open an existing order.
func (c *Client) RemoteOrder(ctx context.Context, id, action, confirm string, dry bool) (map[string]any, error) {
	if !numericObjectID(id) {
		return nil, domain.E("INVALID_ID", "请选择学校返回的订单 ID。")
	}
	me, e := c.WhoAmI(ctx)
	if e != nil {
		return nil, e
	}
	data, e := c.computeJSON(ctx, "GET", "/api/computehub/orders/"+id, nil)
	if e != nil {
		return nil, e
	}
	order := object(data)
	if stringValue(order["id"]) != id {
		return nil, domain.E("NOT_FOUND", "学校未返回所选订单。")
	}
	for _, key := range []string{"uid", "userId"} {
		if v := stringValue(order[key]); v != "" && v != string(me.UID) {
			return nil, domain.E("FORBIDDEN", "订单归属与当前账号不符。")
		}
	}
	out := portalValue(order, "id", "no", "amount", "status", "appId", "appName", "name", "createdAt")
	out["dry_run"] = dry
	out["operation"] = action
	switch action {
	case "cancel":
		if !dry && confirm != id {
			return nil, domain.E("CONFIRM_REQUIRED", "请核对订单后指定 --confirm ORDER_ID。")
		}
		if !dry {
			_, e = c.computeJSON(ctx, "POST", "/api/computehub/orders/"+id+"/cancel", map[string]any{})
			if e != nil {
				return nil, e
			}
			out["result"] = "accepted"
		}
	case "payment-code":
		if !dry {
			v, err := c.computeJSON(ctx, "GET", "/api/computehub/orders/"+id+"/wcpay-code", nil)
			if err != nil {
				return nil, err
			}
			code := stringValue(object(v)["codeUrl"])
			if code == "" || strings.ContainsAny(code, "\r\n\x00") {
				return nil, domain.E("PAYMENT_CODE_UNAVAILABLE", "学校未为该订单返回付款码。")
			}
			out["payment_url"] = code
		}
	default:
		return nil, domain.E("UNKNOWN_ACTION", fmt.Sprintf("未知订单操作 %q", action))
	}
	return out, nil
}

type RemoteOrderIntent struct {
	Action string `json:"action"`
	Target string `json:"target"`
	Name   string `json:"name"`
	Units  int64  `json:"units"`
}
type RemoteQuote struct {
	Intent       RemoteOrderIntent `json:"intent"`
	UID          domain.ID         `json:"uid"`
	SpecID       string            `json:"spec_id"`
	SpecName     string            `json:"spec_name"`
	UnitPrice    json.Number       `json:"unit_price"`
	Total        json.Number       `json:"total"`
	Unit         string            `json:"unit,omitempty"`
	DurationUnit string            `json:"duration_unit,omitempty"`
}

func (c *Client) QuoteRemote(ctx context.Context, v RemoteOrderIntent) (RemoteQuote, error) {
	q := RemoteQuote{Intent: v}
	if !numericObjectID(v.Target) || v.Units <= 0 {
		return q, domain.E("INVALID_ORDER", "请选择资源 ID 和正整数周期数量。")
	}
	who, e := c.WhoAmI(ctx)
	if e != nil {
		return q, e
	}
	q.UID = who.UID
	spec := v.Target
	switch v.Action {
	case "create":
		if strings.TrimSpace(v.Name) == "" {
			return q, domain.E("INPUT_REQUIRED", "请填写实例名称。")
		}
	case "renew":
		raw, e := c.computeJSON(ctx, "GET", "/api/computehub/apps/"+v.Target, nil)
		if e != nil {
			return q, e
		}
		m := object(raw)
		if stringValue(m["id"]) != v.Target {
			return q, domain.E("NOT_FOUND", "学校未返回所选远程实例。")
		}
		spec = stringValue(m["specId"])
		if spec == "" {
			spec = stringValue(m["appSpecId"])
		}
		for _, key := range []string{"uid", "userId"} {
			if uid := stringValue(m[key]); uid != "" && uid != string(who.UID) {
				return q, domain.E("FORBIDDEN", "远程实例不属于当前账号。")
			}
		}
	default:
		return q, domain.E("INVALID_ORDER", "请选择创建或续期。")
	}
	if !numericObjectID(spec) {
		return q, domain.E("INVALID_ID", "无法确定此实例的资源规格。")
	}
	raw, e := c.computeJSON(ctx, "GET", "/api/computehub/app-specs/"+spec, nil)
	if e != nil {
		return q, e
	}
	m := object(raw)
	if stringValue(m["id"]) != spec {
		return q, domain.E("NOT_FOUND", "学校没有返回该资源规格。")
	}
	price := stringValue(m["price"])
	amt, ok := new(big.Rat).SetString(price)
	if !ok || amt.Sign() < 0 || len(price) > 30 {
		return q, domain.E("QUOTE_UNAVAILABLE", "学校未返回明确单价。")
	}
	total := new(big.Rat).Mul(amt, big.NewRat(v.Units, 1))
	scale := 0
	if at := strings.IndexByte(price, '.'); at >= 0 {
		scale = len(price) - at - 1
	}
	if scale > 12 || strings.ContainsAny(price, "eE/") {
		return q, domain.E("QUOTE_UNAVAILABLE", "学校报价精度无法核对。")
	}
	q.SpecID = spec
	q.SpecName = stringValue(m["name"])
	q.UnitPrice = json.Number(amt.FloatString(scale))
	q.Total = json.Number(total.FloatString(scale))
	q.Unit = stringValue(m["currency"])
	if q.Unit == "" {
		q.Unit = stringValue(m["priceUnit"])
	}
	q.DurationUnit = stringValue(m["durationUnit"])
	return q, nil
}
func (c *Client) PlaceRemoteOrder(ctx context.Context, q RemoteQuote) (map[string]any, error) {
	path := "/api/computehub/apps/new"
	body := map[string]any{"name": q.Intent.Name, "appSpecId": domain.ID(q.SpecID).Number(), "durationAmount": q.Intent.Units}
	if q.Intent.Action == "renew" {
		path = "/api/computehub/apps/" + q.Intent.Target + "/operate"
		body = map[string]any{"type": "renew", "appId": domain.ID(q.Intent.Target).Number(), "payloadJson": string(domain.Result(map[string]any{"durationAmount": q.Intent.Units}))}
	}
	raw, e := c.computeJSON(ctx, "POST", path, body)
	if e != nil {
		return nil, e
	}
	order := object(object(raw)["createdOrder"])
	id := stringValue(order["id"])
	if !numericObjectID(id) {
		return nil, domain.E("ORDER_RESULT_UNCERTAIN", "学校未返回可核对的订单 ID。请先查 bupt remote order。")
	}
	return portalValue(order, "id", "no", "name", "appId", "appName", "amount", "status", "createdAt"), nil
}
