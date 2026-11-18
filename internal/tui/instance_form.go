package tui

import (
	"bupt.local/cli/internal/domain"
	"slices"
	"strconv"
	"strings"
	"time"
)

type draft struct {
	restore     bool
	pool        string
	catalog     map[string]any
	price       string
	instance    *domain.Instance
	spec        domain.Spec
	preset      *domain.Preset
	name, limit string
	policy      domain.RunPolicy
	resources   map[string]string
	group       string
	node        domain.Node
}

func (w *workspace) startForm(i domain.Instance) {
	for _, r := range w.report.Runs {
		if r.InstanceID == i.ID && r.Kind == "allocation" && r.State == "allocated" {
			w.startOnNode(i, &r)
			return
		}
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
	w.local("选择 SSH 节点", "", func() content {
		v := content{facts: []string{"选择需要为其准备 SSH 的节点"}}
		for _, n := range i.Nodes {
			if n.ID != "" {
				v.rows = append(v.rows, link(string(n.ID), n.Name, n.Role, "", func() { open(n) }))
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
	policy.Until, policy.MaxRuntime = r.Deadline, ""
	id := string(r.InstanceID)
	args := []string{"up", "--adopt", id, "--until", r.Deadline.Format(time.RFC3339Nano), "--no-wait"}
	params := map[string]any{"target": id, "name": r.Name, "adopt": true, "until": r.Deadline, "policy": policy}
	facts := []string{"启动已分配的实例 " + r.Name, "沿用当前托管截止时间，启动后验证 SSH 连接。"}
	if n.ID != "" {
		params["node"] = n.ID
		args = append(args, "--node", string(n.ID))
		facts = append(facts, "SSH 节点  "+n.Name)
	}
	w.review(change{title: "启动并准备 SSH", method: "up", args: append(args, "--", r.Name), params: params, facts: facts})
}
func (w *workspace) createForm(s domain.Spec, preset *domain.Preset) {
	policy := w.Config.Policy
	if preset != nil {
		policy = preset.Policy
	}
	d := &draft{spec: s, preset: preset, policy: policy, limit: policy.MaxRuntime, resources: map[string]string{}}
	w.resourceForm("创建实例", d)
}

// A chosen template opens directly on the editable application. Refreshing
// its school metadata leaves the user's name, time limit and overrides intact.
func (w *workspace) createFromTemplate(t domain.TemplateSummary, pool string) {
	policy := w.Config.Policy
	d := &draft{policy: policy, limit: policy.MaxRuntime, pool: pool, catalog: t.Resources, resources: map[string]string{}}
	w.push(queryPage(w, "创建实例", cmdline("compute", "catalog", "show", string(t.ID)), "spec.show", map[string]any{"target": t.ID}, true, func(spec domain.Spec) content {
		if spec.ID == "" || spec.ID != t.ID {
			return content{empty: "模板已变更，请返回资源目录重新选择。"}
		}
		d.spec = spec
		return w.resourceFields("创建实例", d)
	}))
}

func (w *workspace) resourceForm(title string, d *draft) {
	w.local(title, "", func() content { return w.resourceFields(title, d) })
}

func (w *workspace) resourceFields(title string, d *draft) content {
	op := d.change(title)
	until := "必填，例如 30m 或 2h"
	if !d.policy.Until.IsZero() {
		until = "截止 " + moment(d.policy.Until)
	}
	v := content{facts: append(slices.Clone(op.facts), op.resources...)}
	if !d.restore {
		v.rows = append(v.rows, w.field("name", "实例名称", &d.name, "必填，给这次实验起名"))
	}
	v.rows = append(v.rows, w.field("limit", "运行上限", &d.limit, until))
	needsProject := d.needsProject()
	if needsProject {
		v.rows = append(v.rows, w.field("group", "项目 ID", &d.group, "此模板仅供项目使用，必填"))
	}
	preview := link("preview", "核对"+title, "查看截止时间、资源和到期处理", cmdline(op.args...), func() {
		target := ""
		if !d.restore && d.name == "" {
			target = "name"
			w.notice = "请先填写实例名称"
		} else if d.limit == "" && d.policy.Until.IsZero() {
			target = "limit"
			w.notice = "请先填写运行上限，例如 30m"
		} else if d.limit != "" {
			if n, err := time.ParseDuration(d.limit); err != nil || n <= 0 {
				target = "limit"
				w.notice = "请输入大于零的运行时长，例如 30m"
			}
		}
		if target == "" && needsProject && (d.group == "" || d.group == "0") {
			target = "group"
			w.notice = "此模板需要填写所属项目 ID"
		}
		if target != "" {
			w.current().key = target
			return
		}
		w.review(op)
	})
	preview.verb = "核对"
	v.rows = append(v.rows, preview)
	if d.instance == nil {
		price := link("estimate", "查看学校估价", "按当前配置查询", "bupt estimate", func() { w.estimate(d) })
		price.verb = "查询估价"
		v.rows = append(v.rows, price)
		if d.preset == nil {
			v.rows = append(v.rows, navLink("options", "调整资源和项目", "GPU、CPU、内存和项目归属", "", func() { w.draftOptions(d) }),
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
	w.local("调整资源和项目", "", func() content {
		v := content{facts: d.resourceFacts()}
		for _, field := range []struct{ key, label string }{{"gpu", "GPU（卡或模板规格）"}, {"cpu", "CPU（核）"}, {"memory", "内存（MiB 或 GiB）"}} {
			current := d.resources[field.key]
			detail := current
			if detail == "" {
				detail = "沿用模板"
			}
			r := link(field.key, field.label, detail, "", func() {
				w.edit(field.label, current, func(s string) error {
					d.price = ""
					if value := strings.TrimSpace(s); value != "" {
						d.resources[field.key] = value
					} else {
						delete(d.resources, field.key)
					}
					return nil
				})
			})
			r.verb = "编辑"
			v.rows = append(v.rows, r)
		}
		if !d.needsProject() {
			v.rows = append(v.rows, w.field("group", "项目 ID", &d.group, "个人 / 模板默认"))
		}
		v.rows = append(v.rows, navLink("done", "返回申请", "保留当前选择", "", w.back))
		return v
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
	var selected []string
	for _, k := range orderedKeys(d.resources) {
		switch k {
		case "gpu":
			selected = append(selected, gpuConstraint(d.resources[k]))
		case "cpu":
			selected = append(selected, d.resources[k]+" 核")
		case "memory":
			memory := d.resources[k]
			if _, err := strconv.ParseUint(memory, 10, 64); err == nil {
				memory += " MiB"
			}
			selected = append(selected, memory+" 内存")
		}
	}
	if len(selected) > 0 {
		facts = append(facts, "本次指定  "+strings.Join(selected, " · "))
	}
	return facts
}

func (d *draft) change(title string) change {
	op := change{title: title, method: "instance.create", params: map[string]any{"name": d.name, "policy": d.policy}, args: []string{"instance", "create"}}
	if d.instance != nil {
		id := string(d.instance.ID)
		op.method = "up"
		op.params["target"] = id
		op.params["adopt"] = true
		op.args = []string{"up", "--adopt", id}
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
			op.facts = []string{"实例  " + d.instance.Name + " · " + id, "恢复此实例的计算预留，保留原磁盘。"}
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
			op.facts = append(op.facts, "项目  "+d.group)
			op.params["group_id"] = d.group
			op.args = append(op.args, "--group-id", d.group)
		}
	}
	if d.price != "" {
		op.facts = append(op.facts, "学校估价  "+d.price+" 积分/天")
	}
	if d.limit != "" {
		policy := d.policy
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
	if d.restore {
		target = string(d.instance.ID)
	}
	op.args = append(op.args, "--no-wait", "--", target)
	return op
}
