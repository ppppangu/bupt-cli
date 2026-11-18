package control

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
)

type pendingKey struct {
	RequestID string
	At        time.Time
}

// A new invocation has a new request ID. An unresolved creation is also bound
// to its name so a command retry cannot silently issue a second credential.
func (c *Controller) reserveKeyCreation(ctx context.Context, storeKey, requestID string, b portalBackend, a domain.PortalAccount, name string) error {
	var previous pendingKey
	e := c.Vault.Get(storeKey, &previous)
	if e != nil {
		if !state.IsNotFound(e) && !errors.Is(e, os.ErrNotExist) {
			return e
		}
		return c.Vault.Put(storeKey, pendingKey{requestID, time.Now().UTC()})
	}
	if time.Since(previous.At) < time.Minute {
		return domain.E("KEY_CREATION_UNCERTAIN", "已有同名称的创建请求等待核对。请先查看 bupt api key --refresh。")
	}
	page, e := b.PortalPage(ctx, a, domain.PortalInput{Section: "key"})
	if e != nil {
		return e
	}
	for _, k := range page.Keys {
		if k.Name == name {
			return &domain.Error{Code: "KEY_CREATION_UNCERTAIN", Message: "学校已有同名 Key，请先核对其状态和控制器保管标记。", Hint: "bupt api key show " + k.ID}
		}
	}
	if e = c.Vault.Delete(storeKey); e != nil {
		return e
	}
	return domain.E("KEY_CREATION_RECONCILED", "学校当前列表没有这个名称。再次执行创建命令将发起一笔新请求。")
}
func definitivePortalRejection(e error) bool {
	switch managedCode(e) {
	case "FORBIDDEN", "PORTAL_ORIGIN_REJECTED", "AGREEMENT_REQUIRED", "AUTH_EXPIRED", "GATEWAY_AUTH_EXPIRED", "RATE_LIMITED", "INVALID_KEY", "NOT_FOUND", "INVALID_ID", "INVALID_MODE", "KEY_STATE":
		return true
	}
	return strings.HasPrefix(managedCode(e), "PORTAL_HTTP_4")
}
