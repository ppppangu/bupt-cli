package platform

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

// A successful interactive login belongs to the portal. Compute obtains its
// service ticket through the same CAS cookie only when compute is requested.
func (c *Client) PrepareCompute(ctx context.Context) error {
	if _, err := c.PortalClient(); err != nil {
		if isCode(err, "PORTAL_NOT_CONFIGURED") {
			return nil
		}
		return err
	}
	c.mu.Lock()
	fresh := !c.computeVerified.IsZero() && time.Since(c.computeVerified) < time.Minute
	c.mu.Unlock()
	if fresh {
		return nil
	}
	_, err := c.verifySSO(ctx)
	if platformSessionExpired(err) {
		if err = c.exchangeService(ctx); err == nil {
			_, err = c.verifySSO(ctx)
		}
	}
	if err == nil {
		c.mu.Lock()
		c.computeVerified = time.Now()
		c.mu.Unlock()
	} else if c.vault != nil && (platformSessionExpired(err) || isCode(err, "AUTH_REQUIRED") || isCode(err, "GATEWAY_AUTH_EXPIRED")) {
		_ = c.vault.Put(c.key("pending-service"), "compute")
		return domain.E("AUTH_REQUIRED", "算力服务需要学校确认，请运行 bupt login 后继续。")
	}
	return err
}

type ComputePage struct {
	Section string           `json:"section"`
	Page    int              `json:"page"`
	Limit   int              `json:"limit"`
	Total   any              `json:"total,omitempty"`
	Items   []map[string]any `json:"items"`
}

func (c *Client) VerifyComputeAdmin(ctx context.Context, i domain.Identity) error {
	return c.verifyAdmin(ctx, i)
}
func (c *Client) verifyAdmin(ctx context.Context, identity domain.Identity) error {
	var doc struct {
		RetCode *int
		Data    struct {
			UserID   domain.ID
			Username string
		}
	}
	if err := c.jsonRequest(ctx, "GET", "/auth/admin/verify", nil, &doc); err != nil {
		if platformSessionExpired(err) || isCode(err, "FORBIDDEN") {
			return domain.E("ADMIN_SCOPE_REQUIRED", "学校已识别普通账号，当前会话没有平台管理员授权。")
		}
		return err
	}
	if doc.RetCode == nil || *doc.RetCode != 0 || doc.Data.UserID == "" || doc.Data.UserID != identity.UID {
		return domain.E("FORBIDDEN", "学校未授予当前身份平台管理员权限。")
	}
	return nil
}
func (c *Client) ComputePage(ctx context.Context, section string, page, limit int, group string) (ComputePage, error) {
	result := ComputePage{Section: section, Page: page, Limit: limit, Items: []map[string]any{}}
	if page < 1 || limit < 1 || limit > 100 {
		return result, domain.E("INVALID_PAGE", "页码从 1 开始，每页可取 1 到 100 条。")
	}
	me, e := c.WhoAmI(ctx)
	if e != nil {
		return result, e
	}
	var path, uidField string
	method := "GET"
	var body any
	var fields []string
	switch section {
	case "cluster":
		path = "/api/hpc/clusters"
		fields = []string{"id", "eid", "clusterEid", "name", "scheduler", "state", "description"}
	case "job":
		path = "/api/hpc/historyJobs"
		uidField = "idUser"
		fields = []string{"idJob", "idUser", "jobDbInx", "clusterId", "clusterName", "jobName", "state", "partition", "timeSubmit", "timeStart", "timeEnd", "elapsed", "account"}
	case "job.request":
		path = "/api/hpc/jobRequestList"
		uidField = "uid"
		fields = []string{"id", "uid", "gid", "name", "jobID", "clusterId", "state", "submittedAt", "updatedAt", "createdAt"}
	case "job.template":
		path = "/api/hpc/jobTemplates"
		fields = []string{"id", "name", "description", "scheduler", "clusterId", "params", "cliParams", "state"}
	case "remote.catalog", "remote.instance", "remote.order":
		path = "/api/computehub/" + map[string]string{"remote.catalog": "app-specs", "remote.instance": "apps", "remote.order": "orders"}[section]
		method = "POST"
		body = map[string]any{"page": page, "limit": limit, "order": "desc", "orderBy": "createdAt"}
		fields = []string{"id", "no", "name", "appId", "appName", "appSpecId", "specName", "description", "amount", "price", "priceUnit", "durationUnit", "durationAmount", "status", "state", "type", "gpuModel", "gpuCount", "cpu", "memory", "createdAt", "updatedAt", "expiredAt", "endAt"}
	case "llm.model", "llm.key", "llm.spend", "llm.account":
		path = "/api/meter/llm/" + map[string]string{"llm.model": "models", "llm.key": "keys", "llm.spend": "spend/logs", "llm.account": "user/info"}[section]
		if section == "llm.key" || section == "llm.spend" {
			path += "?" + url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}}.Encode()
		}
		fields = []string{"id", "name", "modelName", "model", "displayName", "type", "provider", "budgetDuration", "maxBudget", "spend", "keyAlias", "key_alias", "models", "status", "createdAt", "expiresAt", "updatedAt", "amount", "promptTokens", "completionTokens", "totalTokens", "startTime", "endTime"}
	case "notice":
		path = "/api/meter/platform/notifications?" + url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}}.Encode()
		fields = []string{"id", "eid", "title", "summary", "content", "status", "createdAt", "updatedAt", "publishedAt"}
	case "dataset":
		path = "/api/storage/public/datasets?" + url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(limit)}, "orderBy": {"subject"}, "order": {"asc"}}.Encode()
		fields = []string{"id", "eid", "subject", "name", "description", "size", "path", "storageName", "createdAt", "updatedAt"}
	case "billing", "billing.storage", "billing.hpc", "billing.account", "billing.history", "billing.prepayment":
		path = map[string]string{"billing": "/api/meter/billing/apps", "billing.storage": "/api/meter/billing/storages", "billing.hpc": "/api/hpc/billing/jobs", "billing.account": "/api/meter/billing/account-mgmt-bill", "billing.history": "/api/meter/billing/user-account-history", "billing.prepayment": "/api/meter/billing/prepayments"}[section]
		uidField = "uid"
		fields = []string{"id", "uid", "userId", "gid", "appName", "name", "appId", "amount", "balance", "balanceAfter", "accountType", "type", "reason", "appSpecName", "nodeName", "poolName", "usagePoints", "cpuUsage", "gpuUsage", "memoryUsage", "updatedAt", "cost", "sum", "state", "duration", "startAt", "endAt", "createdAt"}
	case "admin", "admin.users", "admin.cluster", "admin.billing":
		if e = c.verifyAdmin(ctx, me); e != nil {
			return result, e
		}
		if section == "admin" {
			result.Items = append(result.Items, map[string]any{"uid": me.UID, "role": "platform_admin", "views": []string{"user", "cluster", "usage", "quota", "request"}})
			return result, nil
		}
		if section == "admin.cluster" {
			path = "/api/hpc/clusters"
			fields = []string{"id", "eid", "clusterEid", "name", "scheduler", "state", "description"}
			break
		}
		if section == "admin.billing" {
			path = "/api/meter/billing/apps"
			method = "POST"
			body = map[string]any{"page": page, "limit": limit, "order": "desc", "orderBy": "startAt", "columns": []any{}}
			fields = []string{"id", "uid", "userId", "gid", "name", "appName", "appId", "amount", "cost", "state", "startAt", "endAt"}
			break
		}
		path = "/api/meter/get-users-by-filter"
		method = "POST"
		body = map[string]any{"page": page, "limit": limit, "columns": []any{}}
		fields = []string{"uid", "eid", "uname", "displayName", "userState", "state", "groups"}
	default:
		return result, domain.E("UNKNOWN_SECTION", "未知裸算力栏目。")
	}
	if uidField != "" {
		method = "POST"
		order := "timeSubmit"
		if section == "job.request" {
			order = "submittedAt"
		} else if strings.HasPrefix(section, "billing") {
			order = "id"
		}
		cols := []map[string]any{{"name": uidField, "value": string(me.UID), "regex": false}}
		if group != "" {
			cols = append(cols, map[string]any{"name": "gid", "value": group, "regex": false})
		}
		table := map[string]any{"page": page, "limit": limit, "order": "desc", "orderBy": order, "columns": cols}
		if strings.HasPrefix(section, "billing") {
			table["startAt"] = 0
			table["endAt"] = 0
			if section == "billing.account" {
				table["orderBy"] = "dueDate"
			}
		}
		body = table
	}
	var doc any
	if e = c.jsonRequest(ctx, method, path, body, &doc); e != nil {
		if strings.HasPrefix(section, "remote.") && isCode(e, "HTTP_404") {
			return result, domain.E("SERVICE_UNAVAILABLE", "学校的远程算力入口当前没有提供订单或资源服务。")
		}
		return result, e
	}
	if m := object(doc); m != nil {
		for _, key := range []string{"errCode", "retCode", "code"} {
			if v, ok := m[key]; ok && integer(v) != 0 {
				return result, domain.E(fmt.Sprintf("COMPUTE_%d", integer(v)), "学校未返回该栏目的数据。")
			}
		}
		if v, ok := m["data"]; ok {
			doc = v
		}
	}
	if section == "llm.account" {
		if m := object(doc); m != nil {
			result.Items = append(result.Items, portalValue(m, fields...))
			return result, nil
		}
	}
	if m := object(doc); m != nil {
		result.Total = m["filteredTotal"]
		if result.Total == nil {
			result.Total = m["total"]
		}
		for _, key := range []string{"records", "items", "clusters", "templates", "datasets", "list", "users", "keys", "logs", "models", "notifications"} {
			if v, ok := m[key]; ok {
				doc = v
				break
			}
		}
	}
	rows, ok := doc.([]any)
	if !ok {
		return result, domain.E("COMPUTE_PROTOCOL", "学校返回的列表格式与已核对的网页不同。")
	}
	for _, item := range rows {
		m := object(item)
		if m == nil {
			continue
		}
		if uidField != "" {
			owner := stringValue(m[uidField])
			if owner == "" {
				owner = stringValue(m["userId"])
			}
			if owner == "" {
				return result, domain.E("OWNERSHIP_UNVERIFIED", "学校未返回此记录的账号归属。")
			}
			if owner != string(me.UID) {
				continue
			}
		}
		result.Items = append(result.Items, portalValue(m, fields...))
	}
	return result, nil
}
