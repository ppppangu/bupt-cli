package tui

import (
	"bupt.local/cli/internal/domain"
	"slices"
	"strconv"
	"strings"
	"time"
)

type draft struct {
	restore                 bool
	pool                    string
	catalog                 map[string]any
	quote                   *instanceQuote
	instance                *domain.Instance
	spec                    domain.Spec
	preset                  *domain.Preset
	name, limit             string
	allocateOnly, forceStop bool
	policy                  domain.RunPolicy
	resources               map[string]string
	group, groupName        string
	node                    domain.Node
}

func (w *workspace) startForm(i domain.Instance) {
	if r := w.instanceRun(i); r != nil && r.Kind == "allocation" && r.State == "allocated" {
		w.startOnNode(i, r)
		return
	}
	w.startOnNode(i, nil)
}

func (w *workspace) startOnNode(i domain.Instance, allocation *domain.Run) {
	open := func(n domain.Node) {
		if allocation != nil {
			w.startAllocation(*allocation, n)
			return
		}
		policy := w.Config.Policy
		if p, ok := w.Config.Presets[w.Config.DefaultPreset]; ok {
			policy = p.Policy
		}
		w.resourceForm("启动实例", &draft{instance: &i, name: w.instanceName(i), node: n, policy: policy, limit: policy.MaxRuntime})
	}
	if len(i.Nodes) <= 1 {
		var n domain.Node
		if len(i.Nodes) == 1 {
			n = i.Nodes[0]
		}
		open(n)
		return
	}
	w.picker("选择 SSH 节点", func() content {
		v := content{facts: []string{"选择需要为其准备 SSH 的节点"}}
		for _, n := range i.Nodes {
			if n.ID != "" {
				v.rows = append(v.rows, link(string(n.ID), n.Name, n.Role, "", func() { w.back(); open(n) }))
			}
		}
		return v
	})
}

// Starting an allocated instance keeps its original managed deadline. The
// explicit absolute limit also bounds a new cycle if the old one just closed.
func (w *workspace) startAllocation(r domain.Run, n domain.Node) {
	if r.InstanceID == "" || !r.Deadline.After(time.Now()) {
		w.notice = "当前托管期限已结束，请刷新实例状态"
		return
	}
	policy := r.Policy
	policy.ForceStop = false
	policy.Until, policy.MaxRuntime = r.Deadline, ""
	id := string(r.InstanceID)
	args := []string{"compute", "instance", "start", "--until", r.Deadline.Format(time.RFC3339Nano)}
	params := map[string]any{"target": id, "name": r.Name, "adopt": true, "until": r.Deadline, "policy": policy}
	facts := []string{"启动已分配的实例 " + r.Name, "沿用当前托管截止时间，启动后验证 SSH 连接。"}
	if n.ID != "" {
		params["node"] = n.ID
		args = append(args, "--node", string(n.ID))
		facts = append(facts, "SSH 节点  "+n.Name)
	}
	w.review(change{title: "启动并准备 SSH", method: "instance.start", args: append(args, "--", id), params: params, facts: facts})
}
func (w *workspace) createForm(s domain.Spec, preset *domain.Preset) {
	policy := w.Config.Policy
	if preset != nil {
		policy = preset.Policy
	}
	d := &draft{spec: s, preset: preset, policy: policy, limit: policy.MaxRuntime, resources: map[string]string{}}
	w.restoreCreateDraft(d)
	w.resourceForm("创建实例", d)
}

// A chosen template opens directly on the editable application. Refreshing
// its school metadata leaves the user's name, time limit and overrides intact.
func (w *workspace) createFromTemplate(t domain.TemplateSummary, pool string) {
	policy := w.Config.Policy
	d := &draft{spec: domain.Spec{ID: t.ID}, policy: policy, limit: policy.MaxRuntime, pool: pool, catalog: t.Resources, resources: map[string]string{}}
	w.restoreCreateDraft(d)
	p := queryPage(w, "创建实例", cmdline("compute", "catalog", "show", string(t.ID)), "spec.show", map[string]any{"target": t.ID}, true, func(spec domain.Spec) content {
		if spec.ID == "" || spec.ID != t.ID {
			return content{empty: "模板已变更，请返回资源目录重新选择。"}
		}
		d.spec = spec
		return w.resourceFields("创建实例", d)
	})
	p.createStep = true
	w.push(p)
	w.attachDraftQuote(p, d)
}

func (w *workspace) resourceForm(title string, d *draft) {
	w.local(title, "", func() content { return w.resourceFields(title, d) })
	w.current().createStep = true
	w.attachDraftQuote(w.current(), d)
}

func (w *workspace) resourceFields(title string, d *draft) content {
	op := d.change(title)
	if slot := d.slot(); slot != "" {
		op.afterSubmit = func() { w.clearDraft(slot) }
	}
	limitLabel, until := "运行上限", "必填，例如 30m 或 2h"
	previewTitle, previewDetail := "核对"+title, "查看截止时间、资源和到期处理"
	if d.restore {
		limitLabel, until = "分配与运行总时长", "从提交分配开始算，例如 30m"
		previewTitle, previewDetail = "核对价格与时限", "查看学校日价和到期处理"
	}
	if !d.policy.Until.IsZero() {
		until = "截止 " + moment(d.policy.Until)
	}
	v := content{facts: append(slices.Clone(op.facts), op.resources...)}
	if d.quote != nil {
		v.facts = append([]string{d.quote.label()}, v.facts...)
	}
	if !d.restore {
		v.rows = append(v.rows, w.field("name", "实例名称", &d.name, "必填，给这次实验起名"))
	}
	v.rows = append(v.rows, w.field("limit", limitLabel, &d.limit, until, func() { w.saveCreateDraft(d) }))
	needsProject := d.needsProject()
	if needsProject {
		v.rows = append(v.rows, w.projectField(d))
	}
	preview := link("preview", previewTitle, previewDetail, cmdline(op.args...), func() {
		target := ""
		if !d.restore && d.name == "" {
			target = "name"
			w.notice = "请先填写实例名称"
		} else if d.limit == "" && d.policy.Until.IsZero() {
			target = "limit"
			w.notice = "请先填写" + limitLabel + "，例如 30m"
		} else if d.limit != "" {
			if n, err := time.ParseDuration(d.limit); err != nil || n <= 0 {
				target = "limit"
				w.notice = "请输入大于零的运行时长，例如 30m"
			}
		}
		if target == "" && needsProject && (d.group == "" || d.group == "0") {
			target = "group"
			w.notice = "请选择有权使用此模板的所属项目"
		}
		if target != "" {
			w.current().key = target
			return
		}
		w.review(op)
	})
	preview.verb = "核对"
	if d.instance == nil {
		mode := "创建并启动"
		if d.allocateOnly {
			mode = "仅申请或分配"
		}
		v.rows = append(v.rows, link("allocate", "创建方式", mode, "", func() { d.allocateOnly = !d.allocateOnly; w.saveCreateDraft(d) }))
		price := link("estimate", "刷新学校估价", "按当前配置查询每日积分", "bupt compute catalog estimate", func() { w.loadDraftQuote(d) })
		price.verb = "查询估价"
		v.rows = append(v.rows, price)
	}
	v.rows = append(v.rows, preview)
	if d.slot() != "" && !w.Cached {
		v.rows = append(v.rows, link("clear-draft", "清除本机草稿", "恢复默认时限和创建方式", "", func() {
			if w.clearDraft(d.slot()) {
				d.limit, d.allocateOnly = d.policy.MaxRuntime, false
				w.notice = "已清除本机草稿"
			}
		}))
	}
	force := "关闭"
	if d.forceStop {
		force = "已授权"
	}
	v.rows = append(v.rows, link("force-stop", "宽限期后强制停机", force, "", func() { d.forceStop = !d.forceStop }))
	if d.instance == nil {
		if d.preset == nil {
			v.rows = append(v.rows, navLink("options", "调整资源和项目", "GPU、CPU、内存、磁盘和项目归属", "", func() { w.draftOptions(d) }),
				navLink("spec", "查看模板说明", "学校公布的规格和可选值", cmdline("spec", "show", string(d.spec.ID)), func() {
					lines := []string{strings.TrimSpace(d.spec.Name + " " + d.spec.Version), d.spec.Description}
					for _, r := range constraintRows(d.spec.Options["resourceConstraints"]) {
						lines = append(lines, r.title+"  "+r.detail)
					}
					lines = append(lines, "模板 ID  "+string(d.spec.ID))
					w.textPage("模板说明", cmdline("spec", "show", string(d.spec.ID)), strings.Join(lines, "\n"))
				}))
		}
	}
	return v
}

func (w *workspace) draftOptions(d *draft) {
	w.picker("调整资源和项目", func() content {
		v := content{facts: d.resourceFacts()}
		for _, field := range []struct{ key, label string }{{"gpu", "GPU（卡或模板规格）"}, {"cpu", "CPU（核）"}, {"memory", "内存（MiB 或 GiB）"}, {"disk", "磁盘（MiB 或 GiB）"}} {
			current := d.resources[field.key]
			detail := current
			if detail == "" {
				detail = "沿用模板"
			}
			r := link(field.key, field.label, detail, "", func() {
				set := func(s string) error {
					if value := strings.TrimSpace(s); value != "" {
						d.resources[field.key] = value
					} else {
						delete(d.resources, field.key)
					}
					w.loadDraftQuote(d)
					return nil
				}
				groups := constraintGroups(d.spec.Options["resourceConstraints"])
				if len(groups) == 1 {
					typ := map[string]string{"cpu": "CPU", "gpu": "GPU", "memory": "Memory", "disk": "AccelDisk"}[field.key]
					for _, group := range groups {
						for _, c := range group.Constraints {
							if c.Type == typ && c.Options != "" {
								choices := []exampleChoice{{"", "模板默认 · " + c.Value}}
								for _, o := range strings.Split(c.Options, ",") {
									o = strings.TrimSpace(o)
									if o != "" {
										choices = append(choices, exampleChoice{o, o})
									}
								}
								w.chooseExample(field.label, choices, current, func(o exampleChoice) { set(o.id) })
								return
							}
						}
					}
				}
				w.edit(field.label, current, set)
			})
			r.verb = "设置"
			v.rows = append(v.rows, r)
		}
		if !d.needsProject() {
			v.rows = append(v.rows, w.projectField(d))
		}
		v.rows = append(v.rows, navLink("done", "返回申请", "保留当前选择", "", w.back))
		return v
	})
}

func (w *workspace) projectField(d *draft) row {
	detail := d.groupName
	if detail == "" {
		switch {
		case d.group != "":
			detail = "项目 " + d.group
		case d.needsProject():
			detail = "必选，从我的项目中选择"
		default:
			detail = "个人 / 模板默认"
		}
	}
	return navLink("group", "所属项目", detail, "bupt project", func() {
		w.popup(queryPage(w, "选择所属项目", "bupt project", "compute.read", map[string]any{"section": "project", "cached": w.Cached}, true, func(members []domain.ProjectMembership) content {
			v := content{empty: "当前账号没有已核验的可用项目。可返回选择个人模板。"}
			if !d.needsProject() {
				v.rows = append(v.rows, link("personal", "个人 / 模板默认", "", "", func() { d.group, d.groupName = "", ""; w.back() }))
			}
			seen := map[domain.ID]bool{}
			for _, m := range members {
				if m.GroupID == "" || m.GroupID == "0" || seen[m.GroupID] || (m.State != "" && m.State != "active") || !slices.Contains([]string{"member", "manager", "admin"}, m.Role) {
					continue
				}
				seen[m.GroupID] = true
				name := m.Name
				if name == "" {
					name = "项目 " + string(m.GroupID)
				}
				v.rows = append(v.rows, link("project/"+string(m.GroupID), name, projectRole(m.Role)+" · ID "+string(m.GroupID), "", func() {
					d.group, d.groupName = string(m.GroupID), m.Name
					w.back()
				}))
			}
			return v
		}))
	})
}

func (d *draft) needsProject() bool {
	return d.instance == nil && d.preset == nil && strings.EqualFold(value(d.spec.Options["scope"]), "group")
}

func (d *draft) resourceFacts() []string {
	var facts []string
	resources := resourceSummary(object(d.spec.Options["resourceSummary"]))
	if resources == "" {
		resources = resourceSummary(d.catalog)
	}
	if resources != "" {
		facts = append(facts, "模板默认  "+resources)
	} else if d.preset == nil {
		for _, r := range constraintRows(d.spec.Options["resourceConstraints"]) {
			val, _, _ := strings.Cut(r.detail, " · 可选")
			facts = append(facts, r.title+"  "+val)
		}
		if len(facts) == 0 {
			facts = append(facts, "默认资源数待学校确认")
		}
	}
	if storage := value(d.spec.Options["storageName"]); storage != "" {
		facts = append(facts, "存储  "+storage)
	}
	images := templateImages(d.spec.Options)
	image := "随所选模板"
	if len(images) > 0 {
		image = strings.Join(images, "、")
	}
	facts = append(facts, "镜像  "+image)
	var selected []string
	for _, k := range orderedKeys(d.resources) {
		switch k {
		case "gpu":
			selected = append(selected, gpuConstraint(d.resources[k]))
		case "cpu":
			selected = append(selected, d.resources[k]+" 核")
		case "memory", "disk":
			name := "内存"
			if k == "disk" {
				name = "磁盘"
			}
			memory := d.resources[k]
			if _, err := strconv.ParseUint(memory, 10, 64); err == nil {
				memory += " MiB"
			}
			selected = append(selected, memory+" "+name)
		}
	}
	if len(selected) > 0 {
		facts = append(facts, "本次指定  "+strings.Join(selected, " · "))
	}
	return facts
}

func (d *draft) change(title string) change {
	policy := d.policy
	policy.ForceStop = d.forceStop
	op := change{title: title, method: "instance.create", params: map[string]any{"name": d.name, "policy": policy, "allocate_only": d.allocateOnly}, args: []string{"compute", "instance", "create"}}
	if d.allocateOnly {
		op.args = append(op.args, "--allocate-only")
	}
	if d.forceStop {
		op.args = append(op.args, "--force-stop")
	}
	if d.instance != nil {
		id := string(d.instance.ID)
		op.method = "instance.start"
		op.params["target"] = id
		op.params["adopt"] = true
		op.args = []string{"compute", "instance", "start"}
		op.facts = []string{"原实例 " + d.instance.Name + " · " + id + "，启动后准备 SSH。"}
		if d.node.ID != "" {
			op.params["node"] = d.node.ID
			op.args = append(op.args, "--node", string(d.node.ID))
			op.facts = append(op.facts, "SSH 节点  "+d.node.Name)
		}
		if d.restore {
			op.method = "instance.restore"
			delete(op.params, "adopt")
			delete(op.params, "name")
			op.args = []string{"compute", "instance", "restore"}
			op.facts = []string{"实例  " + d.name + " · " + id, "原磁盘保留。", "分配完成后，选择「启动并准备 SSH」。"}
		}
	} else if d.preset != nil {
		op.params["preset"] = d.preset.Name
		op.params["preset_snapshot"] = *d.preset
		op.args = append(op.args, "--preset", d.preset.Name)
		op.facts = []string{"预设 " + d.preset.Name + " · 模板 " + string(d.preset.SpecID)}
		op.resources = d.resourceFacts()
	} else {
		op.params["spec_id"] = d.spec.ID
		op.args = append(op.args, "--spec-id", string(d.spec.ID))
		name := strings.TrimSpace(d.spec.Name + " " + d.spec.Version)
		pool := d.pool
		if pool == "" {
			pool = value(d.spec.Options["poolDisplayName"])
		}
		if pool != "" {
			name += " · " + pool
		}
		op.facts = []string{"模板  " + name}
		op.resources = d.resourceFacts()
		resources := map[string]string{}
		for _, k := range orderedKeys(d.resources) {
			if v := d.resources[k]; v != "" {
				resources[k] = v
				op.args = append(op.args, "--"+k, v)
			}
		}
		if len(resources) > 0 {
			op.params["resources"] = resources
		}
		if d.group != "" {
			fact := "项目 ID  " + d.group
			if d.groupName != "" {
				fact = "项目  " + d.groupName + " · ID " + d.group
			}
			op.facts = append(op.facts, fact)
			op.params["group_id"] = d.group
			op.args = append(op.args, "--group-id", d.group)
		}
	}
	if d.instance == nil {
		op.facts = append(op.facts, "连接方式  默认启动并验证 SSH。Web IDE 按节点提供的服务显示。", "获得计算预留或保留存储可能产生费用。实际计费起点与金额以学校账单为准。")
	}
	if d.limit != "" {
		policy.Until = time.Time{}
		policy.MaxRuntime = d.limit
		op.params["policy"] = policy
		op.params["max_runtime"] = d.limit
		op.args = append(op.args, "--max-runtime", d.limit)
	} else if !d.policy.Until.IsZero() {
		op.params["until"] = d.policy.Until
		op.args = append(op.args, "--until", d.policy.Until.Format(time.RFC3339))
	}
	if d.policy.Cleanup != "" {
		op.args = append(op.args, "--cleanup", d.policy.Cleanup)
	}
	if d.policy.AutoRenew {
		op.args = append(op.args, "--renew")
	} else {
		op.args = append(op.args, "--no-renew")
	}
	target := d.name
	if d.instance != nil {
		target = string(d.instance.ID)
	}
	op.args = append(op.args, "--", target)
	return op
}

func templateImages(options map[string]any) []string {
	var images []string
	var visit func(any)
	visit = func(node any) {
		switch v := node.(type) {
		case []any:
			for _, child := range v {
				visit(child)
			}
		case map[string]any:
			if name, ok := v["imageName"].(string); ok && name != "" && !slices.Contains(images, name) {
				images = append(images, name)
			}
			for _, key := range orderedKeys(v) {
				visit(v[key])
			}
		}
	}
	for _, key := range []string{"nodeSpecs", "executorSpecs", "executors", "spec"} {
		visit(options[key])
	}
	return images
}
