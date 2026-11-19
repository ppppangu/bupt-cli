package tui

import (
	"fmt"
	"slices"
	"strings"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
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
			if !matchesGroup && !r.matches(q) {
				continue
			}
			r.group = key
			r.title = "  " + r.title
			children = append(children, r)
		}
		if len(children) == 0 {
			continue
		}
		if q != "" {
			out = append(out, row{key: key, title: g.title, detail: g.summary})
			out = append(out, children...)
			continue
		}
		open := p.expanded[key]
		mark := "▸ "
		if open {
			mark = "▾ "
		}
		header := link(key, mark+g.title, g.summary, "", func() {
			p.expanded[key] = !p.expanded[key]
		})
		header.group = key
		header.verb = "展开"
		if open {
			header.verb = "收起"
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
		w.clearFilter(p)
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
		if p.expanded[r.key] {
			w.move(p.focus + 1)
		} else {
			p.expanded[r.key] = true
		}
		return true
	}
	if r.key != r.group {
		p.key = r.group
		return true
	}
	if !p.expanded[r.group] {
		return false
	}
	delete(p.expanded, r.group)
	return true
}

func (w *workspace) computeHome() {
	p := &page{title: navigation.ComputeTitle, area: "compute", command: "bupt compute", shortcuts: map[rune]string{'1': "instances", '2': "new", '3': "catalog", 'n': "new"}}
	p.key = "instances"
	p.build = func() content {
		if !p.interacted && p.key == "instances" && w.firstInstance() {
			p.key = "new"
		}
		return w.computeMenu()
	}
	w.push(p)
	if !w.Cached && w.statusGeneration != 0 && (!w.hasReport || w.report.Source != "live") {
		w.refreshStatus(false)
	}
}

func (w *workspace) firstInstance() bool {
	return !w.Cached && w.report.Source == "live" && len(w.report.Instances) == 0 && !slices.ContainsFunc(w.report.Runs, awaitingInstance)
}

func awaitingInstance(r domain.Run) bool {
	return r.ID != "" && r.InstanceID == "" && r.State != "" && active(r) && (r.Kind == "allocation" || r.Kind == "session")
}

func (w *workspace) computeMenu() content {
	instanceHint := "状态、节点和连接"
	if w.report.SnapshotAvailable || w.report.Source == "live" {
		instanceHint = fmt.Sprintf("%d 个 · %s", len(w.report.Instances), instanceHint)
	}
	if slices.ContainsFunc(w.report.Runs, awaitingInstance) {
		instanceHint = "有申请待分配 · " + instanceHint
	}
	primary := []row{navLink("instances", "1  我的实例", instanceHint, "bupt compute instance", func() {
		w.push(&page{title: "我的实例", area: "compute", command: "bupt compute instance", build: w.instances, shortcuts: map[rune]string{'n': "new"}})
	})}
	if w.Cached {
		primary = append(primary, row{key: "new", title: "2  创建实例", detail: "在线模式可创建实例"})
	} else {
		primary = append(primary, navLink("new", "2  创建实例", "选用模板镜像，核对规格、价格与期限", "bupt compute catalog", w.chooseTemplate))
	}
	primary = append(primary, navLink("catalog", "3  资源与价格", "查看可用规格、配额与学校估价", "bupt compute catalog", w.catalog))
	return content{rows: primary}
}

func (w *workspace) computeToolsLink() row {
	return navLink("tools", "更多算力工具", "作业、存储、配额、项目和管理", "bupt compute", func() {
		w.push(&page{title: "更多算力工具", command: "bupt compute", area: "compute", expanded: map[string]bool{}, build: w.computeTools})
	})
}
func (w *workspace) computeTools() content {
	section := func(key, title, detail, cmd string, fn func() content) row {
		return navLink(key, title, detail, cmd, func() { w.local(title, cmd, fn) })
	}
	list := func(key, title, detail, cmd string) row {
		return navLink(key, title, detail, "bupt compute "+cmd, func() { w.computeList(key, title, cmd, 1) })
	}
	optional := func(g *navGroup, feature string, item row) {
		enabled, known := w.report.ComputeFeatures[feature]
		if known && !enabled {
			return
		}
		if !known {
			item.detail += " · 开放状态待核验"
		}
		g.items = append(g.items, item)
	}
	work := navGroup{key: "work", title: "我的工作", summary: "查看作业、日志和学校调度"}
	work.items = append(work.items, section("runs", "运行与记录", "查看实验进度、作业输出或操作结果", "bupt compute run", w.work))
	optional(&work, "hpc", section("scheduler", "学校调度", "查询学校集群的队列和作业", "bupt compute job", func() content {
		return content{rows: []row{
			list("job", "调度作业", "状态与日志", "job"), list("job.request", "提交记录", "查看任务请求", "job request"), list("job.template", "作业模板", "查看模板和参数", "job template"), list("cluster", "计算集群", "集群与分区", "cluster"),
		}}
	}))
	resources := navGroup{key: "resources", title: "资源与申请", summary: "选资源、查配额、跟进申请", items: []row{
		navLink("presets", "资源预设", "复用保存的机器配置", "bupt compute preset", w.presets),
		navLink("quota", "我的配额", "看我最多能用多少，申请提高上限", "bupt compute quota", w.quotas),
		list("request", "平台申请", "查看我的申请批到哪一步", "request"),
	}}
	optional(&resources, "remoteApp", section("remote", "远程算力", "按订单报价租用或续期", "bupt compute remote", func() content {
		return content{rows: []row{
			list("remote.catalog", "可租资源", "规格和报价", "remote catalog"), list("remote.instance", "远程实例", "查看详情和续期", "remote instance"), list("remote.order", "我的订单", "查看订单和支付状态", "remote order"),
		}}
	}))
	data := navGroup{key: "data", title: "存储与数据", summary: "持久存储、实例文件和数据集", items: w.files().rows}
	optional(&data, "dataset", list("dataset", "公共数据集", "学校发布的数据", "dataset"))
	org := navGroup{key: "org", title: "项目与费用", summary: "我的项目、个人账单和校园服务"}
	optional(&org, "projects", list("project", "我的项目", "查看我的角色、成员和项目资源", "project"))
	optional(&org, "billing", navLink("billing", "裸算力账单", "在余额与用量查看实例、存储和账户流水", "bupt compute usage", func() { w.usageWorkspace(30, true, "") }))
	org.items = append(org.items, list("notice", "站内通知", "查看通知正文", "notice"))
	optional(&org, "aiModel", section("llm", "算力站模型账目", "算力站内模型单独计量", "bupt compute llm", func() content {
		return content{rows: []row{
			list("llm.account", "账户余额", "", "llm"), list("llm.model", "站内模型", "", "llm model"), list("llm.key", "站内调用密钥", "", "llm key"), list("llm.spend", "消费明细", "", "llm spend"),
		}}
	}))
	if !w.Cached {
		org.items = append(org.items, list("admin", "管理员入口", "核验学校授予的管理权限", "admin"))
	}
	org.items = append(org.items, w.contextAction("服务开放情况", "查看学校发布的功能开关", []string{"compute", "features"}))
	return content{groups: []navGroup{work, resources, data, org}, empty: "未找到匹配项，按退格清除筛选。"}
}
