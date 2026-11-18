package platform

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
)

func (c *Client) ComputeCapabilities(ctx context.Context) (map[string]bool, error) {
	cfg, err := c.Config(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range []struct {
		name         string
		source       map[string]any
		section, key string
	}{
		{"hpc", cfg.Meter, "hpc", "enable"}, {"billing", cfg.Meter, "billing", "enable"}, {"prepayment", cfg.Meter, "billing", "prepayment"},
		{"dataset", cfg.App, "dataset", "showInUserMenu"}, {"projects", cfg.Meter, "project", "enable"},
	} {
		if enabled, ok := object(f.source[f.section])[f.key].(bool); ok {
			out[f.name] = enabled
		}
	}
	for k, v := range cfg.Features {
		if k == "dataset" {
			out["public_datasets"] = v
			v = v || out["dataset"]
		}
		out[k] = v
	}
	return out, nil
}

func (c *Client) computeJSON(ctx context.Context, method, path string, in any) (any, error) {
	// Never follow a redirect carrying an administrative or scheduler payload.
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	tmp := &Client{profile: c.profile, vault: c.vault, http: &client, cookies: c.cookies, base: c.base, gateway: c.gateway}
	var doc any
	if err := tmp.jsonRequest(ctx, method, path, in, &doc); err != nil {
		return nil, err
	}
	if m := object(doc); m != nil {
		for _, key := range []string{"errCode", "retCode", "code"} {
			if val, ok := m[key]; ok && integer(val) != 0 {
				return nil, domain.E(fmt.Sprintf("COMPUTE_%d", integer(val)), "学校未完成该操作。")
			}
		}
		if data, ok := m["data"]; ok {
			return data, nil
		}
	}
	return doc, nil
}
func numericObjectID(s string) bool {
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && n != 0 && strconv.FormatUint(n, 10) == s
}
func (c *Client) AdminLimits(ctx context.Context, cluster string) (map[string]any, error) {
	if !numericObjectID(cluster) {
		return nil, domain.E("INVALID_ID", "请选择数值集群 ID。")
	}
	user, e := c.WhoAmI(ctx)
	if e != nil {
		return nil, e
	}
	if e = c.verifyAdmin(ctx, user); e != nil {
		return nil, e
	}
	data, e := c.computeJSON(ctx, "GET", "/api/hpc/cluster/"+cluster+"/resourceLimits", nil)
	if e != nil {
		return nil, e
	}
	return map[string]any{"cluster_id": cluster, "limits": cleanValue(data), "revision": fmt.Sprintf("%x", sha256.Sum256(domain.Result(data)))}, nil
}

// Change one field of an existing account quota, preserving all other native
// settings. A reviewed revision binds the write to the inspected document.
func (c *Client) UpdateAdminLimit(ctx context.Context, cluster, account, field string, value json.Number, revision string, dry bool) (map[string]any, error) {
	info, e := c.AdminLimits(ctx, cluster)
	if e != nil {
		return nil, e
	}
	name, e := portalID(account)
	if e != nil {
		return nil, e
	}
	if strings.TrimSpace(field) == "" {
		return nil, domain.E("INPUT_REQUIRED", "请选择一项已发布的数值限额。")
	}
	n, e := value.Float64()
	if e != nil || n < 0 {
		return nil, domain.E("INVALID_LIMIT", "限额必须为非负数值。")
	}
	data := info["limits"]
	rows := array(data)
	if m := object(data); m != nil {
		if items := array(m["items"]); items != nil {
			rows = items
		} else if items := array(m["records"]); items != nil {
			rows = items
		}
	}
	var limits map[string]any
	for _, r := range rows {
		m := object(r)
		if stringValue(m["account"]) == account {
			limits = object(m["limit"])
			break
		}
	}
	if limits == nil {
		if m := object(data); m != nil {
			limits = object(m[account])
		}
	}
	before, ok := limits[field]
	if !ok || portalNumber(before) == nil {
		return nil, domain.E("UNSUPPORTED_LIMIT", "该账户未发布此数值限额。")
	}
	preview := map[string]any{"dry_run": dry, "cluster_id": cluster, "account": account, "field": field, "before": before, "after": value, "revision": info["revision"]}
	if dry {
		return preview, nil
	}
	if revision == "" || info["revision"] != revision {
		return nil, domain.E("STALE_QUOTA", "限额已变化或缺少预览版本，请重新预览。")
	}
	limits[field] = value
	_, e = c.computeJSON(ctx, "PUT", "/api/hpc/cluster/"+cluster+"/resourceLimits/"+name, limits)
	return preview, e
}

func (c *Client) SchedulerDetail(ctx context.Context, section, cluster, id string) (map[string]any, error) {
	if !numericObjectID(id) {
		return nil, domain.E("INVALID_ID", "请选择数值 ID。")
	}
	who, e := c.WhoAmI(ctx)
	if e != nil {
		return nil, e
	}
	var path string
	switch section {
	case "remote.catalog.show", "remote.instance.show", "remote.order.show":
		path = "/api/computehub/" + map[string]string{"remote.catalog.show": "app-specs", "remote.instance.show": "apps", "remote.order.show": "orders"}[section] + "/" + id
	case "notice.show":
		path = "/api/meter/platform/notifications/" + id
	case "dataset.show":
		path = "/api/storage/public/datasets/" + id
	case "job.show":
		if !numericObjectID(cluster) {
			return nil, domain.E("INVALID_ID", "请选择集群 ID。")
		}
		path = "/api/hpc/cluster/" + cluster + "/historyJob/byJobID/" + id
	case "job.request.show":
		path = "/api/hpc/jobRequest/" + id
	case "job.template.show":
		path = "/api/hpc/jobTemplate/" + id
	case "cluster.show":
		path = "/api/hpc/cluster/" + id
	case "partition":
		path = "/api/meter/clusters/" + id + "/partitions"
	default:
		return nil, domain.E("INVALID_SECTION", "未知调度栏目。")
	}
	doc, e := c.computeJSON(ctx, "GET", path, nil)
	if e != nil {
		return nil, e
	}
	m := object(doc)
	if section == "partition" {
		return map[string]any{"cluster_id": id, "partitions": cleanValue(doc)}, nil
	}
	if m == nil {
		return nil, domain.E("COMPUTE_PROTOCOL", "学校没有返回对象详情。")
	}
	for _, k := range []string{"job", "jobRequest", "jobTemplate", "cluster"} {
		if item := object(m[k]); item != nil {
			m = item
			break
		}
	}
	if section == "job.show" || section == "job.request.show" {
		owner := stringValue(m["idUser"])
		if owner == "" {
			owner = stringValue(m["uid"])
		}
		if owner != string(who.UID) {
			return nil, domain.E("FORBIDDEN", "无法核验此调度作业归属当前账号。")
		}
	}
	return portalValue(m, "id", "eid", "idJob", "idUser", "jobDbInx", "uid", "gid", "clusterID", "clusterId", "clusterEid", "clusterName", "jobName", "name", "description", "scheduler", "state", "partition", "timeSubmit", "timeStart", "timeEnd", "submittedAt", "createdAt", "elapsed", "account", "cliParameterMetaSets", "envMetaSets", "cliJobParams", "envJobParams", "exitCode", "nodes", "jobID", "limit", "title", "subject", "content", "summary", "appId", "appSpecId", "appName", "no", "amount", "price", "priceUnit", "durationUnit", "durationAmount", "status", "gpuModel", "gpuCount", "startAt", "endAt", "expiredAt", "path", "storageName", "size"), nil
}
func (c *Client) SchedulerLog(ctx context.Context, cluster, id, stream string) (any, error) {
	if stream != "stdout" && stream != "stderr" {
		return nil, domain.E("INVALID_STREAM", "请选择 stdout 或 stderr。")
	}
	if _, e := c.SchedulerDetail(ctx, "job.show", cluster, id); e != nil {
		return nil, e
	}
	return c.computeJSON(ctx, "GET", "/api/hpc/cluster/"+url.PathEscape(cluster)+"/historyJob/byJobID/"+url.PathEscape(id)+"/stdio/"+stream, nil)
}
