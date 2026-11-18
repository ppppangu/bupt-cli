package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"bupt.local/cli/internal/state"
	"github.com/google/uuid"
)

type portalBackend interface {
	PortalAccount(context.Context) (domain.PortalAccount, error)
	PortalPage(context.Context, domain.PortalAccount, domain.PortalInput) (domain.PortalPage, error)
	PortalChange(context.Context, domain.PortalChange) (domain.PortalKey, string, error)
}

func (c *Controller) portalBackend(profile string) (portalBackend, error) {
	a, e := c.api(profile)
	if e != nil {
		return nil, e
	}
	if p, ok := a.(*platform.Client); ok {
		return p.PortalClient()
	}
	if p, ok := a.(portalBackend); ok {
		return p, nil
	}
	return nil, domain.E("PORTAL_UNAVAILABLE", "此控制器未配置大模型 API 门户。")
}
func (c *Controller) portalNamespace(profile string) string {
	p := c.Configuration().Profiles[profile]
	return fmt.Sprintf("portal/%s/%x/", profile, sha256.Sum256(domain.Result([]string{p.PortalURL, p.PlatformURL, p.GatewayURL, string(p.ExpectedUID)})))
}

type portalIndex struct {
	Account   *domain.PortalAccount
	SID       string
	Summary   domain.PortalSummary
	ChangedAt time.Time
}

func (c *Controller) portalIndex(ns string) (portalIndex, error) {
	var i portalIndex
	e := c.Vault.Get(ns+"index", &i)
	if state.IsNotFound(e) || errors.Is(e, os.ErrNotExist) {
		e = nil
	}
	return i, e
}
func portalCacheKey(ns, sid string, v any) string {
	return fmt.Sprintf("%s%x", ns, sha256.Sum256(domain.Result([]any{sid, v})))
}
func (c *Controller) portalAccount(ctx context.Context, profile, ns string, b portalBackend, i *portalIndex) (domain.PortalAccount, error) {
	ticket := c.accountTicket(profile)
	a, e := b.PortalAccount(ctx)
	if e != nil {
		ticket.failure(e, true)
		code := managedCode(e)
		if code == "AUTH_REQUIRED" || code == "AUTH_EXPIRED" || code == "GATEWAY_AUTH_EXPIRED" {
			_ = c.Vault.Put("platform/"+profile+"/pending-service", "portal")
			i.Summary.State = "expired"
		} else {
			i.Summary.State = "unavailable"
		}
		i.Summary.CheckedAt = time.Now().UTC()
		i.ChangedAt = i.Summary.CheckedAt
		_ = c.Vault.Put(ns+"index", *i)
		return a, e
	}
	if a.SID == "" {
		return a, domain.E("PORTAL_PROTOCOL", "当前账号缺少校园身份。")
	}
	if i.SID != a.SID {
		*i = portalIndex{SID: a.SID}
	}
	i.Account = &a
	i.Summary.State = "authenticated"
	i.Summary.CheckedAt = time.Now().UTC()
	i.Summary.Balance = a.Balance
	e = c.Vault.Put(ns+"index", *i)
	if e == nil {
		ticket.observePortal(a, false)
	}
	return a, e
}
func (c *Controller) PortalRead(ctx context.Context, profile string, p Params) (domain.PortalPage, error) {
	defer c.lockLogin(profile)()
	return c.portalReadLocked(ctx, profile, p)
}
func (c *Controller) portalReadLocked(ctx context.Context, profile string, p Params) (domain.PortalPage, error) {
	var q domain.PortalInput
	raw, _ := json.Marshal(p)
	if e := json.Unmarshal(raw, &q); e != nil {
		return domain.PortalPage{}, domain.E("INVALID_INPUT", "门户查询参数无效。")
	}
	if q.Section == "" {
		q.Section = "overview"
	}
	if q.Days == 0 && (q.Section == "usage" || q.Section == "overview") {
		q.Days = 30
	}
	if q.Section == "news" && q.Limit <= 0 {
		q.Limit = 20
	}
	if c.Vault == nil {
		return domain.PortalPage{}, domain.E("SECRET_STORE", "门户需要控制器的私有存储。")
	}
	ns := c.portalNamespace(profile)
	idx, e := c.portalIndex(ns)
	if e != nil {
		return domain.PortalPage{}, e
	}
	var page domain.PortalPage
	if idx.SID != "" {
		e = c.Vault.Get(portalCacheKey(ns, idx.SID, q), &page)
		if q.Section == "news" && q.Limit == 20 && (state.IsNotFound(e) || errors.Is(e, os.ErrNotExist)) {
			legacy := q
			legacy.Limit = 0
			e = c.Vault.Get(portalCacheKey(ns, idx.SID, legacy), &page)
		}
		if e == nil && (p.Bool("cached") || !p.Bool("refresh") && idx.Summary.State == "authenticated" && page.ObservedAt.After(idx.ChangedAt) && time.Since(page.ObservedAt) < time.Minute) {
			if q.Section == "guide" {
				base, _ := page.Data["base_url"].(string)
				page.Data, e = platform.ModelGuideData(base, c.Configuration().Profiles[profile].PortalURL)
				if e != nil {
					return page, e
				}
			}
			page.Source = "cached"
			if !idx.ChangedAt.IsZero() && page.ObservedAt.Before(idx.ChangedAt) {
				page.Warnings = append(page.Warnings, "此快照早于最近一次会话或对象变更。可用 --refresh 更新。")
			}
			return page, nil
		}
		if e != nil && !state.IsNotFound(e) && !errors.Is(e, os.ErrNotExist) {
			return page, e
		}
	}
	if p.Bool("cached") {
		return domain.PortalPage{}, domain.E("NO_CACHE", "此账号尚无该栏目的缓存。")
	}
	b, e := c.portalBackend(profile)
	if e != nil {
		return domain.PortalPage{}, e
	}
	var account domain.PortalAccount
	if !p.Bool("refresh") && idx.Account != nil && idx.Summary.State == "authenticated" && time.Since(idx.Summary.CheckedAt) < time.Minute {
		account = *idx.Account
	} else {
		account, e = c.portalAccount(ctx, profile, ns, b, &idx)
		if e != nil {
			return domain.PortalPage{}, e
		}
	}
	ticket := c.accountTicket(profile)
	page, e = b.PortalPage(ctx, account, q)
	if e != nil {
		ticket.failure(e, true)
		return page, e
	}
	if err := page.ValidateContent(); err != nil {
		return page, err
	}
	for n, k := range page.Keys {
		var marker bool
		page.Keys[n].Stored = c.Vault.Get(portalCacheKey(ns, account.SID, "stored/"+k.ID), &marker) == nil && marker
	}
	if q.Section == "key" || q.Section == "overview" {
		n := len(page.Keys)
		idx.Summary.Keys = &n
	}
	if q.Section == "model" {
		n := len(page.Models)
		idx.Summary.Models = &n
	}
	if e = c.Vault.Put(portalCacheKey(ns, account.SID, q), page); e == nil {
		e = c.Vault.Put(ns+"index", idx)
	}
	return page, e
}

type portalReceipt struct {
	Fingerprint string
	State       string
	Key         domain.PortalKey
}

func (c *Controller) PortalWrite(ctx context.Context, profile string, p Params) (any, error) {
	if c.Vault == nil {
		return nil, domain.E("SECRET_STORE", "门户操作需要加密存储。")
	}
	var op domain.PortalChange
	raw, _ := json.Marshal(p)
	if err := json.Unmarshal(raw, &op); err != nil {
		return nil, domain.E("INVALID_INPUT", "门户操作参数无效。")
	}
	switch op.Action {
	case "key.create":
		if op.Name == "" || len(op.Models) == 0 {
			return nil, domain.E("INVALID_KEY", "请指定 Key 名称和至少一个模型。")
		}
	case "key.delete", "agreement.accept":
		if (!p.Bool("dry_run") && p.String("confirm") != op.Target) || op.Target == "" {
			return nil, domain.E("CONFIRM_REQUIRED", "请明确确认所选对象 ID。")
		}
	case "key.enable", "key.disable", "preference.set", "news.read":
	default:
		return nil, domain.E("UNKNOWN_ACTION", "未知门户操作。")
	}
	if p.Bool("dry_run") {
		return map[string]any{"dry_run": true, "operation": op.Action, "target": op.Target, "name": op.Name, "models": op.Models, "mode": op.Mode}, nil
	}
	defer c.lockLogin(profile)()
	b, e := c.portalBackend(profile)
	if e != nil {
		return nil, e
	}
	ns := c.portalNamespace(profile)
	idx, e := c.portalIndex(ns)
	if e != nil {
		return nil, e
	}
	a, e := c.portalAccount(ctx, profile, ns, b, &idx)
	if e != nil {
		return nil, e
	}
	if a.ConsentRequired && op.Action != "agreement.accept" {
		return nil, domain.E("AGREEMENT_REQUIRED", "请先阅读 bupt agreement。")
	}
	requestID := p.String("idempotency_key")
	if requestID == "" {
		requestID = uuid.NewString()
	}
	id := portalCacheKey(ns, a.SID, "request/"+requestID)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(domain.Result(op)))
	receipt := portalReceipt{Fingerprint: fingerprint, State: "pending"}
	var previous portalReceipt
	if e = c.Vault.Get(id, &previous); e == nil {
		if previous.Fingerprint != fingerprint {
			return nil, domain.E("IDEMPOTENCY_CONFLICT", "该请求标识已用于其他操作。")
		}
		if previous.State == "complete" {
			return portalChangeResult(op, previous.Key, requestID), nil
		}
		return nil, domain.E("PORTAL_RESULT_UNCERTAIN", "请刷新对象列表核对上一次提交结果。")
	} else if !state.IsNotFound(e) && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	creationMarker := ""
	if op.Action == "key.create" {
		creationMarker = portalCacheKey(ns, a.SID, "unresolved/"+op.Name)
		if e = c.reserveKeyCreation(ctx, creationMarker, requestID, b, a, op.Name); e != nil {
			return nil, e
		}
	}
	idx.ChangedAt = time.Now().UTC()
	if op.Action == "agreement.accept" {
		// The next reader must verify the school's current terms, including
		// when the outcome of the following write is uncertain.
		idx.Account = nil
	}
	if e = c.Vault.Put(ns+"index", idx); e != nil {
		return nil, e
	}
	if e = c.Vault.Put(id, receipt); e != nil {
		return nil, e
	}
	ticket := c.accountTicket(profile)
	key, secret, e := b.PortalChange(ctx, op)
	if e != nil {
		ticket.failure(e, true)
	}
	if e != nil {
		if definitivePortalRejection(e) {
			_ = c.Vault.Delete(id)
			if creationMarker != "" {
				_ = c.Vault.Delete(creationMarker)
			}
		}
		return nil, e
	}
	if op.Action == "agreement.accept" {
		a.ConsentRequired = false
		ticket.observePortal(a, false)
	}
	secretID := portalCacheKey(ns, a.SID, "secret/"+key.ID)
	stored := portalCacheKey(ns, a.SID, "stored/"+key.ID)
	if op.Action == "key.create" {
		if key.ID == "" || secret == "" {
			return nil, domain.E("KEY_CREATION_UNCERTAIN", "学校未返回可保管的完整密钥，请核对 Key 列表。")
		}
		if e = c.Vault.Put(secretID, secret); e != nil {
			return nil, &domain.Error{Code: "KEY_STORAGE_FAILED", Message: "学校已创建 Key " + key.ID + "，控制器保存完整值失败。请核对并停用该 Key。"}
		}
		if e = c.Vault.Put(stored, true); e != nil {
			return nil, e
		}
		key.Stored = true
	}
	if op.Action == "key.delete" {
		if e = c.Vault.Delete(secretID); e != nil {
			return nil, e
		}
		if e = c.Vault.Delete(stored); e != nil {
			return nil, e
		}
	}
	receipt.State = "complete"
	receipt.Key = key
	if e = c.Vault.Put(id, receipt); e != nil {
		return nil, e
	}
	if creationMarker != "" {
		if e = c.Vault.Delete(creationMarker); e != nil {
			return nil, e
		}
	}
	c.Audit("portal."+op.Action, profile, op.Target, "", "", "学校门户操作已完成")
	return portalChangeResult(op, key, requestID), nil
}
func portalChangeResult(op domain.PortalChange, k domain.PortalKey, requestID string) map[string]any {
	out := map[string]any{"operation": op.Action, "state": "complete", "request_id": requestID}
	if k.ID != "" {
		out["id"] = k.ID
		out["item"] = k
	} else {
		out["id"] = op.Target
	}
	return out
}
func (c *Controller) PortalExport(ctx context.Context, profile, id string) (any, error) {
	if id == "" || c.Vault == nil {
		return nil, domain.E("INPUT_REQUIRED", "请指定要导出的 Key ID。")
	}
	defer c.lockLogin(profile)()
	ns := c.portalNamespace(profile)
	idx, e := c.portalIndex(ns)
	if e != nil {
		return nil, e
	}
	b, e := c.portalBackend(profile)
	if e != nil {
		return nil, e
	}
	a, e := c.portalAccount(ctx, profile, ns, b, &idx)
	if e != nil {
		return nil, e
	}
	var secret string
	e = c.Vault.Get(portalCacheKey(ns, a.SID, "secret/"+id), &secret)
	if state.IsNotFound(e) || errors.Is(e, os.ErrNotExist) {
		return nil, domain.E("KEY_NOT_STORED", "此控制器未保管该 Key 的完整值。")
	}
	if e != nil {
		return nil, e
	}
	return map[string]string{"secret": secret}, nil
}
