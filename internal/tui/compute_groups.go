package tui

import (
	"fmt"
	"strings"
)

type navGroup struct {
	key, title, summary string
	items               []row
}

// Filtering reveals matches without writing to the user's expansion state.
func (w *workspace) groupRows(p *page, groups []navGroup) []row {
	var out []row
	q := strings.ToLower(p.filter)
	for _, g := range groups {
		key := "group/" + g.key
		matchesGroup := strings.Contains(strings.ToLower(g.title+" "+g.key), q)
		var children []row
		for _, r := range g.items {
			if !matchesGroup && !strings.Contains(strings.ToLower(r.title+" "+r.detail+" "+r.key), q) {
				continue
			}
			r.group = key
			r.title = "  " + r.title
			children = append(children, r)
		}
		if len(children) == 0 {
			continue
		}
		open := p.expanded[key] || q != ""
		mark := "▸ "
		if open {
			mark = "▾ "
		}
		header := link(key, mark+g.title, g.summary, "", func() {
			if p.filter != "" {
				return
			}
			p.expanded[key] = !p.expanded[key]
		})
		header.group = key
		for _, child := range children {
			header.preview = append(header.preview, strings.TrimSpace(child.title)+"  "+child.detail)
		}
		out = append(out, header)
		if open {
			out = append(out, children...)
		}
	}
	return out
}

func (w *workspace) disclose(right bool) bool {
	p := w.current()
	if p.expanded == nil {
		return false
	}
	v := w.view(p)
	if !right && p.filter != "" {
		p.key = p.filterAnchor
		p.filter = ""
		p.filterAnchor = ""
		return true
	}
	if len(v.rows) == 0 {
		return false
	}
	r := v.rows[p.focus]
	if r.group == "" {
		return false
	}
	if right {
		if r.key != r.group {
			return false
		}
		if p.expanded[r.key] || p.filter != "" {
			w.move(p.focus + 1)
		} else {
			p.expanded[r.key] = true
		}
		return true
	}
	if !p.expanded[r.group] {
		return false
	}
	delete(p.expanded, r.group)
	p.key = r.group
	return true
}

func (w *workspace) computeHome() {
	p := &page{title: "裸算力服务", area: "compute", command: "bupt compute", expanded: map[string]bool{"group/work": true}}
	p.build = w.computeGroups
	w.push(p)
	if !w.Cached && w.statusGeneration != 0 && (!w.hasReport || w.report.Source != "live") {
		w.refreshStatus(false)
	}
}

func (w *workspace) computeGroups() content {
	section := func(key, title, detail, cmd string, fn func() content) row {
		return link(key, title, detail, cmd, func() { w.local(title, cmd, fn) })
	}
	list := func(key, title, detail, cmd string) row {
		return link(key, title, detail, "bupt compute "+cmd, func() { w.computeList(key, title, cmd, 1) })
	}
	optional := func(g *navGroup, feature string, item row) {
		enabled, known := w.report.ComputeFeatures[feature]
		if known && !enabled {
			return
		}
		if !known {
			item.detail = "开放状态待核验 · " + item.detail
		}
		g.items = append(g.items, item)
	}
	instanceHint := "状态、节点和连接"
	if w.report.SnapshotAvailable || w.report.Source == "live" {
		instanceHint = fmt.Sprintf("%d 个 · %s", len(w.report.Instances), instanceHint)
	}
	work := navGroup{key: "work", title: "我的工作", summary: "实例、托管运行和作业"}
	work.items = []row{
		section("instances", "我的实例", instanceHint, "bupt compute instance", w.instances),
		section("runs", "运行与记录", "后台作业、托管期限、回执和控制器事件", "bupt compute run", w.work),
	}
	optional(&work, "hpc", section("scheduler", "学校调度", "作业历史、队列和模板", "bupt compute job", func() content {
		return content{rows: []row{
			list("job", "调度作业", "状态与日志", "job"), list("job.request", "提交记录", "查看任务请求", "job request"), list("job.template", "作业模板", "查看模板和参数", "job template"), list("cluster", "计算集群", "集群与分区", "cluster"),
		}}
	}))
	resources := navGroup{key: "resources", title: "资源与申请", summary: "选资源、查配额、跟进申请", items: []row{
		link("catalog", "资源目录", "比较模板、估价和创建实例", "bupt compute catalog", w.catalog),
		link("presets", "资源预设", "复用常用资源组合", "bupt compute preset", w.presets),
		link("quota", "我的配额", "查看用量和上限、申请变更", "bupt compute quota", w.quotas),
		list("request", "平台申请", "申请与审批状态", "request"),
	}}
	optional(&resources, "remoteApp", section("remote", "远程算力", "商城资源、实例和订单", "bupt compute remote", func() content {
		return content{rows: []row{
			list("remote.catalog", "可租资源", "规格和报价", "remote catalog"), list("remote.instance", "远程实例", "查看详情和续期", "remote instance"), list("remote.order", "我的订单", "查看订单和支付状态", "remote order"),
		}}
	}))
	data := navGroup{key: "data", title: "存储与数据", summary: "持久存储、实例文件和数据集", items: w.files().rows}
	optional(&data, "dataset", list("dataset", "公共数据集", "学校发布的数据", "dataset"))
	org := navGroup{key: "org", title: "项目与费用", summary: "项目、账目、通知与管理"}
	optional(&org, "projects", list("project", "项目与角色", "查看项目成员、存储和实例", "project"))
	org.items = append(org.items, list("notice", "站内通知", "查看通知正文", "notice"))
	optional(&org, "billing", section("billing", "用量与账单", "按资源和账户查看账目", "bupt compute usage", w.billingMenu))
	optional(&org, "aiModel", section("llm", "站内模型账户", "裸算力站的附加计量", "bupt compute llm", func() content {
		return content{rows: []row{
			list("llm.account", "账户余额", "", "llm"), list("llm.model", "站内模型", "", "llm model"), list("llm.key", "密钥元数据", "", "llm key"), list("llm.spend", "消费明细", "", "llm spend"),
		}}
	}))
	if !w.Cached {
		org.items = append(org.items, list("admin", "管理身份", "核验学校授权后显示后台操作", "admin"))
	}
	org.items = append(org.items, w.contextAction("服务开放情况", "查看学校发布的功能开关", []string{"compute", "features"}))
	return content{groups: []navGroup{work, resources, data, org}, empty: "未找到匹配项，按 Esc 清除筛选。"}
}

func (w *workspace) billingMenu() content {
	v := content{}
	for _, item := range []struct{ section, title, command string }{{"billing", "实例账单", "usage"}, {"billing.storage", "存储账单", "usage storage"}, {"billing.account", "管理账单", "usage account"}, {"billing.history", "账户流水", "usage history"}, {"billing.prepayment", "预付费记录", "usage prepayment"}, {"billing.hpc", "调度账单", "usage hpc"}} {
		if item.section == "billing.hpc" {
			if enabled, known := w.report.ComputeFeatures["hpc"]; known && !enabled {
				continue
			}
		}
		if item.section == "billing.prepayment" {
			if enabled, known := w.report.ComputeFeatures["prepayment"]; known && !enabled {
				continue
			}
		}
		v.rows = append(v.rows, link(item.section, item.title, "", "bupt compute "+item.command, func() { w.computeList(item.section, item.title, item.command, 1) }))
	}
	return v
}
