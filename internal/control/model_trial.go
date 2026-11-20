package control

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
)

// Only the controller resolves the credential. The client supplies a model ID,
// its editable inputs and a key ID, never an arbitrary HTTP request.
type modelRunner interface {
	RunModelTrial(context.Context, string, domain.TrialRequest, string) (domain.TrialResult, error)
}
type preparedTrial struct {
	gateway      modelRunner
	base, secret string
	request      domain.TrialRequest
	effect       string
	id           string
}

func (c *Controller) modelTrial(ctx context.Context, profile string, p Params) (domain.TrialResult, error) {
	call, e := c.prepareTrial(ctx, profile, p)
	if e != nil {
		return domain.TrialResult{}, e
	}
	// Do not hold the profile login lock while the model is generating output.
	result, err := call.gateway.RunModelTrial(ctx, call.base, call.request, call.secret)
	defer c.modelCalls.CompareAndDelete(call.effect, call.id)
	if definiteModelResult(err) {
		// The next request can reuse the input after a definite result. The
		// original idempotency key is still consumed even after success.
		defer c.lockLogin(profile)()
		_ = c.Vault.Delete(call.effect)
	}
	return result, err
}

func definiteModelResult(err error) bool {
	if err == nil {
		return true
	}
	switch managedCode(err) {
	case "MODEL_BALANCE_INSUFFICIENT", "MODEL_KEY_REJECTED", "MODEL_ACCESS_DENIED",
		"MODEL_RATE_LIMITED", "MODEL_ENDPOINT_UNAVAILABLE", "MODEL_GATEWAY_UNTRUSTED",
		"MODEL_TRIAL_UNSUPPORTED", "KEY_NOT_STORED", "GATEWAY_AUTH_EXPIRED":
		return true
	}
	return false
}
func (c *Controller) prepareTrial(ctx context.Context, profile string, p Params) (preparedTrial, error) {
	var call preparedTrial
	if p.Bool("cached") || p.Bool("dry_run") {
		return call, domain.E("TRIAL_PREVIEW", "离线浏览和预览只展示请求。")
	}
	var invocation domain.ModelInvocation
	if err := json.Unmarshal(domain.Result(p), &invocation); err != nil || !p.Has("input") {
		return call, domain.E("INVALID_TRIAL_INPUT", "模型输入格式有误。")
	}
	modelID, keyID := invocation.Model, invocation.KeyID
	if modelID == "" || keyID == "" {
		return call, domain.E("INPUT_REQUIRED", "请选择模型和已启用的 API Key。")
	}
	if id := invocation.IdempotencyKey; strings.TrimSpace(id) == "" || len(id) > 256 {
		return call, domain.E("IDEMPOTENCY_REQUIRED", "计费调用必须提供有效的请求编号")
	}
	in := invocation.Input
	if c.Vault == nil {
		return call, domain.E("SECRET_STORE", "模型调用需要控制器的加密凭据库。")
	}
	defer c.lockLogin(profile)()
	b, e := c.portalBackend(profile)
	if e != nil {
		return call, e
	}
	runner, ok := b.(modelRunner)
	if !ok {
		return call, domain.E("MODEL_TRIAL_UNSUPPORTED", "控制器未提供模型调用。")
	}
	ns := c.portalNamespace(profile)
	idx, e := c.portalIndex(ns)
	if e != nil {
		return call, e
	}
	account, e := c.portalAccount(ctx, profile, ns, b, &idx)
	if e != nil {
		return call, e
	}
	if account.ConsentRequired {
		return call, domain.E("AGREEMENT_REQUIRED", "请先通过 bupt agreement 核对学校服务协议。")
	}
	models, e := c.portalReadLocked(ctx, profile, Params{"section": domain.Result("model")})
	if e != nil {
		return call, e
	}
	m, e := models.TrialModel(modelID)
	if e != nil {
		return call, e
	}
	call.request, e = m.TrialRequest(in)
	if e != nil {
		return call, e
	}
	// A cached menu is useful while editing. Always check the current school key
	// and its exact model scope again at the moment of sending.
	keys, e := b.PortalPage(ctx, account, domain.PortalInput{Section: "key"})
	if e != nil {
		return call, e
	}
	at := slices.IndexFunc(keys.Keys, func(k domain.PortalKey) bool { return k.ID == keyID })
	if at < 0 || keys.Keys[at].Status != "active" || !slices.Contains(keys.Keys[at].Models, modelID) {
		return call, domain.E("MODEL_KEY_SCOPE", "所选 Key 已停用、已删除或未授权这个模型。")
	}
	guide, e := c.portalReadLocked(ctx, profile, Params{"section": domain.Result("guide")})
	if e != nil {
		return call, e
	}
	call.base, _ = guide.Data["base_url"].(string)
	if call.base == "" {
		return call, domain.E("PORTAL_PROTOCOL", "门户未提供模型接入地址。")
	}
	if e = c.Vault.Get(portalCacheKey(ns, account.SID, "secret/"+keyID), &call.secret); e != nil {
		if state.IsNotFound(e) || errors.Is(e, os.ErrNotExist) {
			return call, domain.E("KEY_NOT_STORED", "此控制器未保管该 Key 的完整值，请选择或创建另一把 Key。")
		}
		return call, e
	}
	if call.secret == "" {
		return call, domain.E("KEY_NOT_STORED", "此控制器缺少完整 Key。")
	}
	id := invocation.IdempotencyKey
	marker := portalCacheKey(ns, account.SID, "trial/"+id)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(domain.Result([]any{modelID, keyID, in})))
	var previous string
	if e = c.Vault.Get(marker, &previous); e == nil {
		if previous != fingerprint {
			return call, domain.E("IDEMPOTENCY_CONFLICT", "同一调用标识已用于其他输入。")
		}
		return call, &domain.Error{Code: "TRIAL_ALREADY_SUBMITTED", Message: "该次调用已提交。请先核对用量，再决定是否发起新调用。", RequestID: id}
	} else if !state.IsNotFound(e) && !errors.Is(e, os.ErrNotExist) {
		return call, e
	}
	effect := portalCacheKey(ns, account.SID, "trial-effect/"+fingerprint)
	var pending string
	if e = c.Vault.Get(effect, &pending); e == nil {
		_, running := c.modelCalls.Load(effect)
		if running || invocation.ResendAfter != pending {
			return call, &domain.Error{Code: "MODEL_RECONCILIATION_REQUIRED", Message: "同一请求仍在执行或结果待核查，请先核对用量。", RequestID: pending}
		}
	} else if !state.IsNotFound(e) && !errors.Is(e, os.ErrNotExist) {
		return call, e
	}
	if e = ctx.Err(); e != nil {
		return call, e
	}
	if e = c.Vault.Put(marker, fingerprint); e != nil {
		return call, e
	}
	if e = c.Vault.Put(effect, id); e != nil {
		return call, e
	}
	c.modelCalls.Store(effect, id)
	call.gateway, call.effect, call.id = runner, effect, id
	return call, nil
}
