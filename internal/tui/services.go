package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

func (w *workspace) serviceHome() content {
	api := "模型、余额与密钥"
	if w.report.API.State == "authenticated" {
		if w.report.API.Balance != nil {
			api = fmt.Sprintf("余额 %g 点", *w.report.API.Balance)
		}
		if w.report.API.Keys != nil {
			api += fmt.Sprintf(" · %d 把 Key", *w.report.API.Keys)
		}
	}
	compute := "资源目录、实例与作业"
	if w.hasReport && w.report.SnapshotAvailable {
		running, reserved := 0, 0
		for _, i := range w.report.Instances {
			if i.State == "running" {
				running++
			}
			if i.Reserved {
				reserved++
			}
		}
		compute = fmt.Sprintf("%d 个实例 · %d 运行 · %d 预留", len(w.report.Instances), running, reserved)
	}
	return content{rows: []row{
		link("api", "大模型 API 服务", api, "bupt api", w.apiHome),
		link("compute", "裸算力服务", compute, "bupt compute", w.computeHome),
		link("access", "账号与平台", "学校登录、公告、协议和连接", "bupt status", func() { w.local("账号与平台", "bupt status", w.access) }),
	}}
}
func (w *workspace) computeHome() {
	w.local("裸算力服务", "bupt compute", func() content {
		v := content{rows: []row{
			link("instances", "我的实例", "运行、预留、期限和 SSH", "bupt compute instance", func() { w.local("我的实例", "bupt compute instance", w.instances) }),
			link("resources", "资源目录与配额", "资源池、规格和预设", "bupt compute catalog", func() { w.local("资源与模板", "bupt compute catalog", w.resources) }),
			link("work", "作业与记录", "学校调度、托管运行和回执", "bupt compute job", func() { w.local("作业与记录", "bupt compute job", w.computeWork) }),
			link("files", "存储与文件", "持久存储与实例文件", "bupt compute storage", func() { w.local("存储与文件", "bupt compute storage", w.files) }),
		}}
		for _, f := range []struct{ id, title, cmd string }{{"project", "项目与角色", "project"}, {"request", "平台申请", "request"}, {"cluster", "计算集群", "cluster"}, {"dataset", "公共数据集", "dataset"}, {"billing", "用量与账单", "usage"}, {"remote.order", "远程算力订单", "remote order"}, {"notice", "裸算力站内通知", "notice"}, {"admin", "平台管理权限", "admin"}} {
			feature := map[string]string{"cluster": "hpc", "dataset": "dataset", "billing": "billing", "remote.order": "remoteApp"}[f.id]
			if enabled, known := w.report.ComputeFeatures[feature]; known && !enabled {
				continue
			}
			v.rows = append(v.rows, link(f.id, f.title, "", cmdline("compute", f.cmd), func() { w.computeList(f.id, f.title, f.cmd, 1) }))
		}
		v.rows = append(v.rows, w.moreCommands("compute"))
		return v
	})
	w.current().area = "compute"
	if !w.Cached && w.statusGeneration != 0 && (!w.hasReport || w.report.Source != "live") {
		w.refreshStatus(false)
	}
}
func (w *workspace) computeWork() content {
	v := w.work()
	if enabled, known := w.report.ComputeFeatures["hpc"]; known && !enabled {
		return v
	}
	return content{rows: append([]row{
		link("scheduler", "学校调度作业", "调度器记录的执行历史", "bupt compute job", func() { w.computeList("job", "学校调度作业", "job", 1) }),
		link("submission", "调度提交记录", "", "bupt compute job request", func() { w.computeList("job.request", "调度提交记录", "job request", 1) }),
		link("templates", "调度作业模板", "", "bupt compute job template", func() { w.computeList("job.template", "调度作业模板", "job template", 1) }),
	}, v.rows...)}
}
func (w *workspace) computeList(section, title, command string, num int) {
	cmd := "bupt compute " + command
	accountOnly := section == "project" || section == "request"
	w.push(queryPage(w, title, cmd, "compute.read", map[string]any{"section": section, "page": num, "limit": 25, "cached": w.Cached}, !accountOnly, func(result any) content {
		v := content{empty: "当前账号没有可显示的记录。"}
		items, _ := result.([]any)
		total := 0
		if m, ok := result.(map[string]any); ok {
			items, _ = m["items"].([]any)
			total, _ = strconv.Atoi(fmt.Sprint(m["total"]))
		}
		for n, item := range items {
			m := object(item)
			name := ""
			for _, k := range []string{"name", "subject", "jobName", "group_id", "idJob", "id", "uid", "type"} {
				if x := value(m[k]); x != "" {
					name = x
					break
				}
			}
			v.rows = append(v.rows, link(fmt.Sprint(n), name, strings.TrimSpace(value(m["role"])+" "+value(m["state"])), cmd, func() {
				var target, sub, cluster string
				switch section {
				case "project":
					target, sub = value(m["group_id"]), "project.show"
				case "job":
					target, sub, cluster = value(m["idJob"]), "job.show", value(m["clusterId"])
				case "job.request", "job.template", "cluster":
					target, sub = value(m["id"]), section+".show"
				}
				if target == "" || sub == "" || sub == "job.show" && cluster == "" {
					w.objectPage(name, cmd, item)
					return
				}
				args := append([]string{"compute"}, strings.Fields(command)...)
				args = append(args, "show", target)
				if cluster != "" {
					args = append(args, "--cluster", cluster)
				}
				show := cmdline(args...)
				w.push(queryPage(w, name, show, "compute.read", map[string]any{"section": sub, "target": target, "cluster": cluster, "cached": w.Cached}, sub != "project.show", func(obj any) content { raw, _ := json.MarshalIndent(obj, "", "  "); return w.textContent(string(raw)) }))
			}))
		}
		if num*25 < total || total == 0 && len(items) == 25 {
			v.rows = append(v.rows, link("next", "下一页", "", fmt.Sprintf("%s --page %d", cmd, num+1), func() { w.computeList(section, title, command, num+1) }))
		}
		if section == "admin" && len(items) > 0 {
			v.rows = append(v.rows, link("users", "平台账户", "", "bupt compute admin user", func() { w.computeList("admin.users", "平台账户", "admin user", 1) }))
		}
		return v
	}))
}
func (w *workspace) objectPage(title, cmd string, obj any) {
	raw, _ := json.MarshalIndent(obj, "", "  ")
	w.textPage(title, cmd, string(raw))
}
func ntext(n *float64) string {
	if n == nil {
		return "—"
	}
	return strconv.FormatFloat(*n, 'f', -1, 64)
}
func (w *workspace) apiHome() {
	w.portalPage("overview", "大模型 API 服务", "bupt api", nil)
	w.current().area = "api"
}
func (w *workspace) portalPage(section, title, cmd string, extra map[string]any) {
	params := map[string]any{"section": section, "cached": w.Cached}
	for k, v := range extra {
		params[k] = v
	}
	p := queryPage(w, title, cmd, "portal.read", params, false, func(v domain.PortalPage) content { return w.portalContent(section, cmd, v) })
	if section == "overview" {
		p.build = func() content { return w.portalContent(section, cmd, domain.PortalPage{}) }
	}
	w.push(p)
	if section == "overview" {
		w.loadPage(p)
	}
}
func (w *workspace) portalContent(section, cmd string, page domain.PortalPage) content {
	v := content{empty: "当前账号没有可显示的记录。"}
	if !page.ObservedAt.IsZero() {
		v.facts = append(v.facts, label(page.Source)+" · "+moment(page.ObservedAt))
	}
	v.facts = append(v.facts, page.Warnings...)
	switch section {
	case "overview":
		if page.Account != nil {
			v.facts = append(v.facts, "余额 "+ntext(page.Account.Balance)+" 点 · "+strconv.Itoa(len(page.Keys))+" 把 API Key")
		}
		for _, x := range []struct{ id, title, detail string }{{"model", "模型", "ID、能力、上下文、价格和健康状态"}, {"usage", "余额与用量", "按时间和模型查看消耗"}, {"key", "API Key", "模型范围、状态和密钥保管"}, {"guide", "接入指南", "Base URL、curl 与 SDK 示例"}, {"leaderboard", "排行榜", "平台榜单与本人展示方式"}} {
			sub := "bupt api " + x.id
			v.rows = append(v.rows, link(x.id, x.title, x.detail, sub, func() { w.portalPage(x.id, x.title, sub, nil) }))
		}
		v.rows = append([]row{link("trial", "试用模型", "输入自己的问题或文本，查看实际返回", "bupt api model", w.chooseTrialModel)}, v.rows...)
		v.rows = append(v.rows, w.moreCommands("api"))
		if !w.Cached {
			v.rows = append(v.rows, link("login", "平台登录", "", "bupt login", w.loginMethods))
		}
	case "model":
		for _, m := range page.Models {
			detail := strings.TrimSpace(m.Kind + " · " + m.Context + " · " + ntext(m.InputPrice) + "/" + ntext(m.OutputPrice) + " 点/M · " + m.Health)
			show := cmdline("api", "model", "show", m.ID)
			v.rows = append(v.rows, link(m.ID, m.ID, detail, show, func() {
				w.local(m.Name, show, func() content {
					cv := content{facts: []string{m.ID, m.Description}, rows: []row{info("能力", m.Kind), info("上下文", m.Context), info("输入 / 输出价", ntext(m.InputPrice)+" / "+ntext(m.OutputPrice)+" 点/百万 Token"), info("健康状态", m.Health+" · "+m.Coverage), info("最近采样", m.SampledAt), info("延迟", ntext(m.LatencyMS)+" ms")}}
					if t, e := domain.TrialFor(m); e == nil {
						cv.rows = append([]row{link("try", "试用模型", "输入自己的内容并发送", cmdline("api", "model", "example", m.ID), func() { w.modelPlayground(m, t) })}, cv.rows...)
					} else {
						cv.rows = append(cv.rows, info("模型试用", e.Error()))
					}
					if len(m.History) > 0 {
						cv.rows = append(cv.rows, link("history", "历史采样", "", show, func() { w.objectPage("历史采样", show, m.History) }))
					}
					if !w.Cached {
						cv.rows = append(cv.rows, link("create", "为此模型创建 Key", "", cmdline("api", "key", "create", "NAME", "--model", m.ID), func() { w.keyForm([]string{m.ID}) }))
					}
					return cv
				})
			}))
		}
	case "key":
		if !w.Cached {
			v.rows = append(v.rows, link("new", "+ 创建 API Key", "选择模型范围", "bupt api key create NAME --model ID", func() { w.keyForm(nil) }))
		}
		for _, k := range page.Keys {
			show := cmdline("api", "key", "show", k.ID)
			v.rows = append(v.rows, link(k.ID, k.Name, k.Status+" · "+strings.Join(k.Models, ", "), show, func() { w.portalKey(k) }))
		}
	case "usage":
		if page.Account != nil {
			v.facts = append(v.facts, "账户余额 "+ntext(page.Account.Balance)+" 点")
		}
		for _, n := range []struct{ k, title string }{{"days", "统计天数"}, {"periodCalls", "调用次数"}, {"periodTokens", "Token 用量"}, {"successRate", "成功率"}, {"modelDistribution", "按模型消耗"}, {"inputSeries", "每日输入 Token"}, {"outputSeries", "每日输出 Token"}} {
			if data, ok := page.Data[n.k]; ok {
				v.rows = append(v.rows, link(n.k, n.title, value(data), cmd, func() { w.objectPage(n.title, cmd, data) }))
			}
		}
		if page.Data["usageAvailable"] == false {
			v.facts = append(v.facts, "用量数据暂不可用")
		}
		for _, days := range []int{7, 30} {
			v.rows = append(v.rows, link(fmt.Sprint(days), fmt.Sprintf("查看近 %d 天", days), "", fmt.Sprintf("bupt api usage --days %d", days), func() {
				w.portalPage("usage", "余额与用量", fmt.Sprintf("bupt api usage --days %d", days), map[string]any{"days": days})
			}))
		}
	case "news":
		w.portalNews(&v, page)
	case "news.show":
		w.article(&v, cmd, page)
	case "agreement":
		w.agreement(&v, page)
	case "guide":
		for _, k := range []string{"base_url", "curl", "python"} {
			v.rows = append(v.rows, link(k, k, value(page.Data[k]), cmd, func() { w.textPage(k, cmd, value(page.Data[k])) }))
		}
	default:
		if section == "account" {
			v.rows = append(v.rows, info("学工号", page.Account.SID), info("姓名", page.Account.Name), info("余额（点）", ntext(page.Account.Balance)))
		}
		for _, k := range orderedKeys(page.Data) {
			data := page.Data[k]
			v.rows = append(v.rows, link(k, k, value(data), cmd, func() { w.objectPage(k, cmd, data) }))
		}
		if section == "leaderboard" {
			v.rows = append(v.rows, link("display", "我的展示方式", "", "bupt api leaderboard display", func() { w.portalPage("preference", "展示方式", "bupt api leaderboard display", nil) }))
		}
		if section == "preference" && !w.Cached {
			for _, m := range []string{"anonymous", "partial", "real"} {
				v.rows = append(v.rows, link(m, map[string]string{"anonymous": "匿名", "partial": "脱敏", "real": "实名"}[m], "", cmdline("api", "leaderboard", "display", "set", m), func() {
					w.portalConfirm("更新排行榜展示方式", []string{"api", "leaderboard", "display", "set", m}, map[string]any{"action": "preference.set", "mode": m}, func() { w.portalPage("preference", "展示方式", "bupt api leaderboard display", nil) })
				}))
			}
		}
	}
	return v
}
func (w *workspace) portalKey(k domain.PortalKey) {
	show := cmdline("api", "key", "show", k.ID)
	w.local(k.Name, show, func() content {
		v := content{rows: []row{info("ID", k.ID), info("状态", k.Status), info("允许模型", strings.Join(k.Models, ", ")), info("累计用量（点）", ntext(k.Used)), info("创建时间", k.CreatedAt), info("控制器保管", yes(k.Stored))}}
		if len(k.Models) > 0 {
			v.rows = append(v.rows, link("models", "查看授权模型的价格与状态", strings.Join(k.Models, ", "), "bupt api model", func() {
				w.push(queryPage(w, "授权模型", "bupt api model", "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(page domain.PortalPage) content {
					page.Models = slices.DeleteFunc(slices.Clone(page.Models), func(m domain.PortalModel) bool { return !slices.Contains(k.Models, m.ID) })
					return w.portalContent("model", "bupt api model", page)
				}))
			}))
		}
		if w.Cached {
			return v
		}
		for _, op := range []struct{ id, title string }{{"enable", "启用"}, {"disable", "停用"}, {"delete", "删除"}} {
			if op.id == "enable" && k.Status == "active" || op.id == "disable" && k.Status == "disabled" {
				continue
			}
			args := []string{"api", "key", op.id, k.ID}
			p := map[string]any{"action": "key." + op.id, "target": k.ID}
			if op.id == "delete" {
				args = append(args, "--confirm", k.ID)
				p["confirm"] = k.ID
			}
			v.rows = append(v.rows, link(op.id, op.title, k.Name, cmdline(args...), func() {
				w.portalConfirm(op.title+" "+k.Name, args, p, func() { w.portalPage("key", "API Key", "bupt api key", nil) })
			}))
		}
		if k.Stored {
			v.rows = append(v.rows, link("export", "导出到新私有文件", "", cmdline("api", "key", "export", k.ID, "--output", "FILE"), func() {
				w.edit("新文件路径", "", func(path string) error {
					if strings.TrimSpace(path) == "" || path == "-" {
						return fmt.Errorf("请输入新文件路径")
					}
					w.terminalReview("导出 API Key", path, []string{"api", "key", "export", k.ID, "--output", path})
					return nil
				})
			}))
		}
		return v
	})
}
func (w *workspace) keyForm(initial []string) { w.keyFormReturning(initial, nil, nil) }
func (w *workspace) keyFormReturning(initial []string, returnTo *page, done func(map[string]any)) {
	selected := slices.Clone(initial)
	name := ""
	w.push(queryPage(w, "创建 API Key", "bupt api key create NAME --model ID", "portal.read", map[string]any{"section": "model"}, true, func(p domain.PortalPage) content {
		v := content{rows: []row{w.field("name", "用途名称", &name, "例如 科研脚本")}}
		for _, m := range p.Models {
			mark := "□ "
			if slices.Contains(selected, m.ID) {
				mark = "■ "
			}
			v.rows = append(v.rows, link(m.ID, mark+m.ID, m.Name, "", func() {
				if n := slices.Index(selected, m.ID); n >= 0 {
					selected = slices.Delete(selected, n, n+1)
				} else {
					selected = append(selected, m.ID)
				}
			}))
		}
		args := []string{"api", "key", "create", name}
		for _, id := range selected {
			args = append(args, "--model", id)
		}
		v.rows = append(v.rows, link("preview", "确认模型范围", strings.Join(selected, ", "), cmdline(args...), func() {
			if strings.TrimSpace(name) == "" || len(selected) == 0 {
				w.notice = "请填写名称并选择模型"
				return
			}
			after := done
			if after == nil {
				after = func(map[string]any) { w.portalPage("key", "API Key", "bupt api key", nil) }
			}
			w.confirmPortalChange("创建 API Key", args, map[string]any{"action": "key.create", "name": name, "models": selected}, returnTo, after)
		}))
		return v
	}))
}
func (w *workspace) portalConfirm(title string, args []string, params map[string]any, done func()) {
	w.confirmPortalChange(title, args, params, nil, func(map[string]any) { done() })
}
func (w *workspace) confirmPortalChange(title string, args []string, params map[string]any, returnTo *page, done func(map[string]any)) {
	if w.Cached {
		return
	}
	key := uuid.NewString()
	cmd := cmdline(args...)
	w.local(title, cmd, func() content {
		return content{facts: []string{cmd}, rows: []row{link("back", "返回", "", "", w.back), link("submit", "确认"+title, "", cmd, func() {
			p := w.current()
			if p.submitting {
				return
			}
			p.submitting = true
			params["idempotency_key"] = key
			go func() {
				ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
				defer cancel()
				var result map[string]any
				e := w.Backend.Call(ctx, "portal.change", params, &result)
				w.post(w.ctx, func() {
					p.submitting = false
					if e != nil {
						w.notice = whenError(e)
						return
					}
					for len(w.pages) > 1 && w.current() != returnTo && w.current().title != "大模型 API 服务" && w.current().title != "账号与平台" {
						w.back()
					}
					done(result)
					w.notice = "学校已确认操作"
				})
			}()
		})}}
	})
}
