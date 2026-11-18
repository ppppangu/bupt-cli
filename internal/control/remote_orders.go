package control

import (
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"context"
	"github.com/google/uuid"
	"reflect"
	"time"
)

type orderReview struct {
	Quote     platform.RemoteQuote
	ExpiresAt time.Time
	State     string
	Result    map[string]any
}

func (c *Controller) remoteCheckout(ctx context.Context, profile string, p Params) (any, error) {
	if c.Vault == nil {
		return nil, domain.E("SECRET_STORE", "订单核对需要控制器的持久存储。")
	}
	defer c.lockLogin(profile)()
	api, e := c.api(profile)
	if e != nil {
		return nil, e
	}
	client, ok := api.(*platform.Client)
	if !ok {
		return nil, domain.E("UNSUPPORTED", "此 profile 没有远程算力商城适配器。")
	}
	v := platform.RemoteOrderIntent{Action: p.String("action"), Target: p.String("target"), Name: p.String("name"), Units: p.Int("units")}
	ns := c.portalNamespace(profile) + "remote-quote/"
	if p.Bool("dry_run") {
		q, e := client.QuoteRemote(ctx, v)
		if e != nil {
			return nil, e
		}
		id := uuid.NewString()
		r := orderReview{Quote: q, ExpiresAt: time.Now().Add(5 * time.Minute), State: "reviewed"}
		if e = c.Vault.Put(ns+id, r); e != nil {
			return nil, e
		}
		return map[string]any{"dry_run": true, "quote": id, "expires_at": r.ExpiresAt, "details": q}, nil
	}
	id := p.String("quote")
	if _, e = uuid.Parse(id); e != nil {
		return nil, domain.E("QUOTE_REQUIRED", "先加 --dry-run 查看学校单价与总额，再用 --quote 确认该报价。")
	}
	var r orderReview
	if e = c.Vault.Get(ns+id, &r); e != nil {
		return nil, domain.E("QUOTE_REQUIRED", "没有找到此 profile 的报价。")
	}
	if r.Quote.Intent != v {
		return nil, domain.E("QUOTE_CHANGED", "所选对象、名称或周期数量与预览不同。")
	}
	if r.State == "complete" {
		return r.Result, nil
	}
	if r.State == "submitted" {
		return nil, domain.E("ORDER_RESULT_UNCERTAIN", "该报价已经提交，请核对学校订单列表。")
	}
	if !r.ExpiresAt.After(time.Now()) {
		return nil, domain.E("QUOTE_EXPIRED", "报价已过期，请重新预览。")
	}
	q, e := client.QuoteRemote(ctx, v)
	if e != nil {
		return nil, e
	}
	if q.Unit == "" || q.DurationUnit == "" {
		return nil, domain.E("QUOTE_UNAVAILABLE", "学校未公布货币或计费周期单位，无法核对实际订单。")
	}
	if !reflect.DeepEqual(q, r.Quote) {
		return nil, domain.E("QUOTE_CHANGED", "学校报价或账号已变化，请重新预览。")
	}
	r.State = "submitted"
	if e = c.Vault.Put(ns+id, r); e != nil {
		return nil, e
	}
	result, e := client.PlaceRemoteOrder(ctx, q)
	if e != nil {
		return nil, e
	}
	r.State = "complete"
	r.Result = result
	if e = c.Vault.Put(ns+id, r); e != nil {
		return nil, e
	}
	c.Audit("compute.order."+v.Action, profile, v.Target, "", "", "远程算力订单已由学校确认。")
	return result, nil
}
