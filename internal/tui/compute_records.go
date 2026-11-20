package tui

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

func computeValue(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v := value(m[k]); v != "" {
			return v
		}
	}
	return ""
}

// Compute list payloads include both typed slices and paginated envelopes.
// Preserve native IDs while projecting numeric JSON fields for navigation.
func readComputeJSON(raw json.RawMessage) (any, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var obj any
	e := d.Decode(&obj)
	return obj, e
}

func computeRecordID(section string, m map[string]any) string {
	switch section {
	case "project":
		return computeValue(m, "group_id")
	case "job":
		if c, j := computeValue(m, "clusterId"), computeValue(m, "idJob"); c != "" && j != "" {
			return c + "/" + j
		}
	case "admin.users":
		return computeValue(m, "uid", "id", "eid")
	}
	return computeValue(m, "id", "eid", "clusterEid")
}

func computeTime(raw string) string {
	n, err := strconv.ParseInt(raw, 10, 64)
	if err == nil {
		switch {
		case n == 0:
			return "—"
		case len(raw) == 13:
			return time.UnixMilli(n).Local().Format("2006-01-02 15:04")
		case len(raw) == 10:
			return time.Unix(n, 0).Local().Format("2006-01-02 15:04")
		}
	}
	return portalTime(raw)
}

func projectRole(role string) string {
	switch role {
	case "member":
		return "成员"
	case "admin":
		return "管理员"
	case "manager":
		return "项目负责人"
	default:
		return label(role)
	}
}

func recordSummary(m map[string]any) []string {
	var lines []string
	for _, f := range []struct{ k, n string }{
		{"amount", "金额"}, {"cost", "费用"}, {"sum", "合计"}, {"usagePoints", "消耗积分"},
		{"duration", "计费时长"}, {"runningSeconds", "运行秒数"}, {"balance", "余额"}, {"balanceAfter", "变更后余额"}, {"price", "单价"}, {"durationUnit", "计费周期单位"},
		{"state", "状态"}, {"status", "状态"}, {"startAt", "开始"}, {"endAt", "结束"}, {"createdAt", "创建"}, {"expiredAt", "到期"},
		{"reason", "事由"}, {"description", "说明"}, {"summary", "摘要"}, {"role", "角色"},
		{"group_id", "项目 ID"}, {"clusterId", "集群"}, {"id", "ID"},
	} {
		if s := value(m[f.k]); s != "" {
			switch f.k {
			case "startAt", "endAt", "createdAt", "expiredAt":
				s = computeTime(s)
			case "state", "status":
				s = label(s)
			case "role":
				s = projectRole(s)
			case "duration":
				unit := computeValue(m, "durationUnit")
				if unit == "" {
					unit = "（单位未公布）"
				}
				s += " " + unit
			case "price":
				s = strings.TrimSpace(s + " " + computeValue(m, "currency", "priceUnit"))
			case "amount", "cost", "balance", "sum", "balanceAfter":
				s = strings.TrimSpace(s + " " + computeValue(m, "currency"))
			}
			lines = append(lines, f.n+"  "+s)
		}
	}
	return lines
}

func computeCommandPath(path string) string {
	head, rest, _ := strings.Cut(path, " ")
	switch head {
	case "admin", "cluster", "remote", "project":
		return path
	case "job":
		if strings.HasPrefix(rest, "request") || strings.HasPrefix(rest, "template") {
			return "compute scheduler " + rest
		}
		return "compute scheduler " + path
	default:
		return "compute " + path
	}
}

func (w *workspace) computeList(section, title, command string, num int) {
	cmd := "bupt " + computeCommandPath(command)
	accountOnly := section == "project" || section == "request"
	show := func(doc json.RawMessage) content {
		result, e := readComputeJSON(doc)
		if e != nil {
			return content{empty: "无法解析该列表。"}
		}
		v := content{split: true, empty: "当前账号没有可显示的记录。"}
		items, _ := result.([]any)
		total := -1
		if m := object(result); m != nil {
			items, _ = m["items"].([]any)
			if n, e := strconv.Atoi(value(m["total"])); e == nil {
				total = n
			}
		}
		if section == "admin" {
			for _, it := range items {
				m := object(it)
				if value(m["role"]) == "platform_admin" {
					return w.adminMenu()
				}
			}
			return content{empty: "当前会话没有已核验的平台管理员角色。"}
		}
		projectRows := map[string]int{}
		for _, it := range items {
			m := object(it)
			if m == nil {
				continue
			}
			id := computeRecordID(section, m)
			key := id
			if key == "" {
				raw, _ := json.Marshal(m)
				key = fmt.Sprintf("item-%x", sha256.Sum256(raw))
			}
			name := computeValue(m, "title", "subject", "name", "jobName", "appName", "specName", "displayName", "modelName", "keyAlias", "no", "group_id", "idJob", "id", "uid", "type")
			if name == "" {
				name = "记录"
			}
			detail := computeValue(m, "state", "status", "role", "description", "summary", "amount", "cost", "createdAt")
			summary := recordSummary(m)
			if strings.HasPrefix(section, "billing") || section == "admin.billing" {
				if len(summary) > 0 {
					detail = summary[0]
				}
			}
			r := navLink(section+"/"+key, name, label(detail), cmd, func() { w.computeRecord(section, command, name, id, m) })
			r.preview = summary
			r.aliases = computeValue(m, "appId") + " " + strings.Join(summary, " ")
			if section == "project" {
				membership := projectRole(value(m["role"]))
				if state := value(m["state"]); state != "" {
					membership += " · " + label(state)
				}
				if previous, ok := projectRows[id]; ok {
					v.rows[previous].preview = append(v.rows[previous].preview, membership)
					continue
				}
				r.detail, r.preview = "角色与成员", []string{membership}
				projectRows[id] = len(v.rows)
			}
			v.rows = append(v.rows, r)
		}
		turn := func(n int) { w.back(); w.computeList(section, title, command, n) }
		if num > 1 {
			v.rows = append(v.rows, navLink("prev", "上一页", "", fmt.Sprintf("%s --page %d", cmd, num-1), func() { turn(num - 1) }))
		}
		// These school endpoints return the whole collection and ignore page.
		unpaged := accountOnly || section == "admin.request" || section == "cluster" || section == "admin.cluster" || section == "job.template" || section == "llm.model" || section == "llm.account"
		if !unpaged && (num*25 < total || total < 0 && len(items) == 25) {
			v.rows = append(v.rows, navLink("next", "下一页", "", fmt.Sprintf("%s --page %d", cmd, num+1), func() { turn(num + 1) }))
		}
		return v
	}
	w.push(queryPage(w, title, cmd, "compute.read", map[string]any{"section": section, "page": num, "limit": 25, "cached": w.Cached}, !accountOnly, show))
}
func (w *workspace) adminMenu() content {
	v := content{}
	for _, a := range []struct{ section, title, cmd string }{{"admin.users", "平台账户", "admin user"}, {"admin.cluster", "计算集群与限额", "admin cluster"}, {"admin.billing", "平台实例计量", "admin usage"}, {"admin.request", "需处理的申请", "admin request"}} {
		v.rows = append(v.rows, navLink(a.section, a.title, "", "bupt compute "+a.cmd, func() { w.computeList(a.section, a.title, a.cmd, 1) }))
	}
	return v
}

func (w *workspace) computeRecord(section, command, name, id string, m map[string]any) {
	path := append([]string{"compute"}, strings.Fields(command)...)
	base := cmdline(path...)
	if id == "" {
		w.local(name, base, func() content {
			return content{facts: recordSummary(m), rows: []row{navLink("detail", "完整记录", "", base, func() { w.objectPage(name, base, m) })}}
		})
		return
	}
	params := map[string]any{"target": id, "cached": w.Cached}
	var sub string
	target := id
	switch section {
	case "notice", "dataset", "remote.catalog", "remote.instance", "remote.order", "project", "job.request", "job.template", "cluster":
		sub = section + ".show"
	case "job":
		target = computeValue(m, "idJob")
		params["cluster"] = computeValue(m, "clusterId")
		params["target"] = target
		if target != "" && params["cluster"] != "" {
			sub = "job.show"
		}
	}
	build := func(data any) content {
		detail := object(data)
		if detail == nil {
			return content{empty: "学校未返回该对象的详情。"}
		}
		if sub != "" && section != "project" {
			got := computeRecordID(section, detail)
			if section == "job" {
				got = value(detail["idJob"])
				if cluster := computeValue(detail, "clusterId", "clusterID"); cluster != "" && cluster != params["cluster"] {
					return content{empty: "学校返回的作业不属于所选集群。"}
				}
			}
			if got == "" && strings.HasPrefix(section, "remote.") {
				return content{facts: recordSummary(detail), rows: []row{navLink("detail", "完整记录", "学校未返回资源 ID", base, func() { w.objectPage(name, base, detail) })}}
			}
			if got != "" && got != target {
				return content{empty: "学校返回的对象与所选 ID 不一致。"}
			}
		}
		if section == "project" && value(object(detail["membership"])["group_id"]) != id {
			return content{empty: "学校返回的项目与所选 ID 不一致。"}
		}
		v := content{facts: recordSummary(detail)}
		if section == "job" {
			detail["clusterId"] = params["cluster"]
		}
		v.rows = append(v.rows, w.recordActions(section, target, detail)...)
		v.rows = append(v.rows, navLink("detail", "完整记录", "", base, func() { w.objectPage(name, base, data) }))
		if section == "project" {
			return w.projectRecord(base, data)
		}
		if section == "notice" {
			if body := computeValue(object(data), "content", "summary"); body != "" {
				return w.textContent(body)
			}
		}
		return v
	}
	// Cached school lists may expose values but never authorize a remote write.
	if sub == "" {
		w.local(name, base, func() content { return build(m) })
		return
	}
	params["section"] = sub
	args := append(path, "show", target)
	if c, ok := params["cluster"].(string); ok {
		args = append(args, "--cluster", c)
	}
	base = cmdline(args...)
	w.push(queryPage(w, name, base, "compute.read", params, sub != "project.show", func(raw json.RawMessage) content {
		data, e := readComputeJSON(raw)
		if e != nil {
			return content{empty: "无法解析详情。"}
		}
		return build(data)
	}))
}
func (w *workspace) recordActions(section, id string, m map[string]any) []row {
	if w.Cached {
		return nil
	}
	action := func(n, d, cmd string, args ...string) row {
		return w.contextAction(n, d, strings.Fields(cmd), args...)
	}
	switch section {
	case "job":
		if id == "" || computeValue(m, "clusterId", "clusterID") == "" {
			return nil
		}
		return []row{action("作业日志", "stdout 或 stderr", "compute scheduler job logs", "--cluster="+computeValue(m, "clusterId", "clusterID"), id)}
	case "cluster":
		return []row{action("集群分区", "", "cluster partition", id)}
	case "remote.catalog", "remote.instance":
		renew := section == "remote.instance"
		op, title, bound := "create", "按此规格创建订单", "--spec="+id
		if renew {
			op, title, bound = "renew", "按此实例续期", id
		}
		cmd := cmdline("remote", "order", op, bound)
		return []row{navLink("action/remote/order/"+op, title, "选择周期并核对学校报价", cmd, func() {
			w.remoteOrderForm(renew, id, computeValue(m, "name", "specName", "appName"), computeValue(m, "durationUnit"))
		})}
	case "remote.order":
		if !strings.EqualFold(computeValue(m, "state", "status"), "pending") {
			return nil
		}
		return []row{action("查询付款码", "查看本订单的学校付款码", "remote order payment-code", id), action("取消订单", "核对订单 ID 后确认", "remote order cancel", "--confirm="+id, id)}
	case "admin.cluster":
		return []row{action("账户限额", "查看学校发布的限额", "admin quota show", id), action("更改账户限额", "按当前限额预览版本提交", "admin quota set", id)}
	case "admin.request":
		if state := strings.ToLower(computeValue(m, "state", "status")); state != "pending" && state != "created" {
			return nil
		}
		return []row{action("批准申请", "核对当前申请 ID", "admin approve", "--confirm="+id, id), action("拒绝申请", "填写学校审批意见", "admin reject", "--confirm="+id, id)}
	}
	return nil
}

func (w *workspace) projectRecord(cmd string, data any) content {
	obj := object(data)
	membership := object(obj["membership"])
	role := value(membership["role"])
	v := content{facts: []string{"项目 " + value(membership["group_id"]) + " · 我的角色 " + projectRole(role)}}
	state := value(membership["state"])
	active := state == "" || state == "active"
	if group := domain.ID(value(membership["group_id"])); group != "" && active && !w.Cached {
		v.rows = append(v.rows, navLink("storage", "项目存储", "浏览当前项目的文件和目录", "bupt compute storage stores", func() { w.projectStores(group) }))
	}
	instances, _ := obj["instances"].([]any)
	v.rows = append(v.rows, navLink("instances", "项目实例", fmt.Sprintf("%d 个", len(instances)), cmd, func() {
		w.local("项目实例", cmd, func() content {
			page := content{empty: "当前项目没有可见实例。"}
			for _, item := range instances {
				m := object(item)
				id := domain.ID(value(m["id"]))
				if id == "" {
					continue
				}
				name := value(m["name"])
				page.rows = append(page.rows, navLink(string(id), name, label(value(m["state"])), cmdline("compute", "instance", "show", string(id)), func() {
					if _, ok := w.instance(id); ok {
						w.instancePage(id)
					} else {
						w.objectPage(name, cmd, m)
					}
				}))
			}
			return page
		})
	}))
	if active && (role == "admin" || role == "manager") {
		members, _ := obj["members"].([]any)
		v.rows = append(v.rows, navLink("members", "可见成员", fmt.Sprintf("%d 人", len(members)), cmd, func() {
			w.local("项目成员", cmd, func() content {
				page := content{empty: "该项目没有已发布的成员记录。"}
				for _, item := range members {
					m := object(item)
					uid := value(m["uid"])
					page.rows = append(page.rows, navLink(strings.Join([]string{uid, value(m["role"]), value(m["state"])}, "/"), computeValue(m, "name", "uid"), projectRole(value(m["role"]))+" · "+label(value(m["state"])), cmd, func() { w.objectPage(uid, cmd, item) }))
				}
				return page
			})
		}))
	}
	return v
}
