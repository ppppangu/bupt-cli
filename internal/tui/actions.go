package tui

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

type change struct {
	title, method string
	args          []string
	params        map[string]any
	facts         []string
	deleteName    string
	task          bool
}

func (w *workspace) terminalReview(title, detail string, args []string) {
	display := cmdline(args...)
	w.local(title, display, func() content {
		return content{facts: []string{detail}, rows: []row{
			link("back", "返回", "取消操作", "", w.back),
			link("apply", title, "在当前终端执行", display, func() { w.foreground(args...) }),
		}}
	})
}
func (w *workspace) review(op change) {
	if w.Cached {
		return
	}
	needsSchool := op.method == "up" || op.method == "instance.create" || op.method == "instance.delete" || op.method == "lease.renew"
	if needsSchool && w.needsLogin() {
		w.loginMethods()
		return
	}
	params := maps.Clone(op.params)
	params["idempotency_key"] = uuid.NewString()
	params["wait"] = false
	test := maps.Clone(params)
	test["dry_run"] = true
	command := cmdline(op.args...)
	type preview struct {
		DryRun  bool        `json:"dry_run"`
		Run     *domain.Run `json:"run"`
		Cleanup string      `json:"cleanup"`
	}
	var p *page
	p = queryPage(w, "确认"+op.title, command, op.method, test, true, func(plan preview) content {
		v := content{facts: append([]string{"预览 · " + op.title}, op.facts...)}
		if !plan.DryRun {
			v.facts = append(v.facts, "控制器未返回有效预览")
			return v
		}
		if plan.Run != nil {
			v.facts = append(v.facts, "截止 "+moment(plan.Run.Deadline)+" · "+label(plan.Run.Policy.Cleanup))
		}
		if name, ok := params["name"].(string); ok {
			v.rows = append(v.rows, info("实例名称", name))
		}
		if sel, ok := params["resources"].(map[string]string); ok {
			for _, key := range orderedKeys(sel) {
				v.rows = append(v.rows, info(strings.ToUpper(key), sel[key]))
			}
		}
		if plan.Run != nil {
			v.rows = append(v.rows, info("自动续期", yes(plan.Run.Policy.AutoRenew)))
		}
		v.rows = append(v.rows, link("command", "等效命令", "查看完整参数", command, func() { w.textPage("等效命令", command, command) }))
		v.rows = append(v.rows, link("back", "返回", "继续浏览或修改", command, w.back))
		v.rows = append(v.rows, link("submit", "确认"+op.title, "提交给当前账号的控制器", command, func() {
			if op.deleteName != "" {
				w.edit("输入实例名称 "+op.deleteName+" 确认永久删除", "", func(input string) error {
					if input != op.deleteName {
						return fmt.Errorf("名称不匹配")
					}
					w.submit(p, op, params)
					return nil
				})
			} else {
				w.submit(p, op, params)
			}
		}))
		return v
	})
	w.push(p)
}
func (w *workspace) submit(p *page, op change, params map[string]any) {
	if p.submitting || w.current() != p {
		return
	}
	p.submitting = true
	w.notice = ""
	go func() {
		ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
		defer cancel()
		var receipt struct {
			ID string `json:"id"`
		}
		err := w.Backend.Call(ctx, op.method, params, &receipt)
		w.post(w.ctx, func() {
			p.submitting = false
			if err != nil {
				w.notice = whenError(err)
				return
			}
			if receipt.ID == "" {
				w.notice = "控制器未返回回执，请查看作业与记录"
				return
			}
			w.pages = w.pages[:len(w.pages)-1]
			if op.task {
				w.taskPage(receipt.ID)
			} else {
				w.runPage(receipt.ID)
			}
			w.notice = "已提交 · " + receipt.ID
			w.refreshStatus(false)
		})
	}()
}
func (w *workspace) endReview(i domain.Instance) {
	id := string(i.ID)
	w.review(change{title: "停止并释放", method: "down", args: []string{"down", id, "--cleanup", "release", "--no-wait"}, params: map[string]any{"target": id, "cleanup": "release"}, facts: []string{i.Name + " · " + id, "停止计算并释放预留资源，保留实例磁盘。"}})
}
func (w *workspace) deleteReview(i domain.Instance) {
	id := string(i.ID)
	w.review(change{title: "永久删除", method: "instance.delete", args: []string{"instance", "delete", id, "--no-wait"}, params: map[string]any{"target": id}, facts: []string{i.Name + " · " + id, "平台将永久删除实例及其实例磁盘。"}, deleteName: i.Name, task: true})
}
func (w *workspace) cancelReview(r domain.Run) {
	w.review(change{title: "取消作业", method: "job.cancel", args: []string{"job", "cancel", r.ID}, params: map[string]any{"target": r.ID}, facts: []string{r.Name + " · " + r.ID, "作业终止后按周期策略清理 · " + label(r.Policy.Cleanup)}})
}

type draft struct {
	restore     bool
	instance    *domain.Instance
	spec        domain.Spec
	preset      *domain.Preset
	name, limit string
	policy      domain.RunPolicy
	resources   map[string]string
	group       string
}

func (w *workspace) startForm(i domain.Instance) {
	policy := w.Config.Policy
	if p, ok := w.Config.Presets[w.Config.DefaultPreset]; ok {
		policy = p.Policy
	}
	d := &draft{instance: &i, name: w.instanceName(i), policy: policy, limit: policy.MaxRuntime}
	w.resourceForm("启动实例", d)
}
func (w *workspace) createForm(s domain.Spec, preset *domain.Preset) {
	policy := w.Config.Policy
	if preset != nil {
		policy = preset.Policy
	}
	d := &draft{spec: s, preset: preset, policy: policy, limit: policy.MaxRuntime, resources: map[string]string{}}
	w.resourceForm("创建实例", d)
}
func (w *workspace) field(key, title string, dest *string, placeholder string) row {
	detail := *dest
	if detail == "" {
		detail = placeholder
	}
	return link(key, title, detail, "", func() { w.edit(title, *dest, func(s string) error { *dest = strings.TrimSpace(s); return nil }) })
}
func (w *workspace) resourceForm(title string, d *draft) {
	w.local(title, "", func() content {
		op := d.change(title)
		v := content{facts: op.facts}
		v.rows = []row{w.field("name", "实例名称", &d.name, "输入名称…"), w.field("limit", "运行上限", &d.limit, "例如 30m 或 2h")}
		if d.instance == nil && d.preset == nil {
			for _, k := range []string{"gpu", "cpu", "memory"} {
				value := d.resources[k]
				detail := value
				if detail == "" {
					detail = "模板默认"
				}
				v.rows = append(v.rows, link(k, strings.ToUpper(k), detail, "", func() {
					w.edit(strings.ToUpper(k), value, func(s string) error { d.resources[k] = strings.TrimSpace(s); return nil })
				}))
			}
			v.rows = append(v.rows, w.field("group", "项目 ID", &d.group, "个人 / 模板默认"))
		}
		v.rows = append(v.rows, link("preview", "预览"+title, "查看目标、期限和清理策略", cmdline(op.args...), func() {
			if d.name == "" {
				w.notice = "请先填写实例名称"
				return
			}
			if d.limit != "" {
				n, e := time.ParseDuration(d.limit)
				if e != nil || n <= 0 {
					w.notice = "请输入正的运行时长，例如 30m"
					return
				}
			} else if d.policy.Until.IsZero() {
				w.notice = "请先填写运行上限"
				return
			}
			w.review(op)
		}))
		return v
	})
}
func (d *draft) change(title string) change {
	op := change{title: title, method: "instance.create", params: map[string]any{"name": d.name, "policy": d.policy}, args: []string{"instance", "create", d.name}}
	if d.instance != nil {
		id := string(d.instance.ID)
		op.method = "up"
		op.params["target"] = id
		op.params["adopt"] = true
		op.args = []string{"up", d.name, "--adopt", id}
		op.facts = []string{"原实例 " + d.instance.Name + " · " + id + "，启动后准备 SSH。"}
		if d.restore {
			op.method = "instance.restore"
			delete(op.params, "adopt")
			delete(op.params, "name")
			op.args = []string{"compute", "instance", "restore", id}
			op.facts = []string{"恢复原实例的预留并设置清理期限。原磁盘保留。"}
		}
	} else if d.preset != nil {
		op.params["preset"] = d.preset.Name
		op.params["preset_snapshot"] = *d.preset
		op.args = append(op.args, "--preset", d.preset.Name)
		op.facts = []string{"预设 " + d.preset.Name + " · 使用其模板和资源配置"}
	} else {
		op.params["spec_id"] = d.spec.ID
		op.args = append(op.args, "--spec-id", string(d.spec.ID))
		op.facts = []string{"模板 " + strings.TrimSpace(d.spec.Name+" "+d.spec.Version), "新建实例，完成后可从实例页启动。"}
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
			op.params["group_id"] = d.group
			op.args = append(op.args, "--group-id", d.group)
		}
	}
	if d.limit != "" {
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
	op.args = append(op.args, "--no-wait")
	return op
}
func (w *workspace) jobForm(i domain.Instance) {
	id := string(i.ID)
	var argv = []string{""}
	w.local("提交后台作业", "bupt run --help", func() content {
		args := append([]string{"run", "--detach", "--instance", id, "--"}, argv...)
		facts := []string{"实例 " + i.Name + " · 沿用当前运行期限和清理策略"}
		v := content{facts: facts}
		for j, s := range argv {
			title := "可执行文件"
			if j > 0 {
				title = fmt.Sprintf("参数 %d", j)
			}
			v.rows = append(v.rows, link(fmt.Sprint(j), title, s, "", func() { w.edit(title, s, func(val string) error { argv[j] = val; return nil }) }))
		}
		v.rows = append(v.rows, link("add", "+ 添加参数", "每项作为一个参数传递", "", func() { w.edit("新参数", "", func(val string) error { argv = append(argv, val); return nil }) }))
		if len(argv) > 1 {
			v.rows = append(v.rows, link("remove", "移除末尾参数", argv[len(argv)-1], "", func() { argv = argv[:len(argv)-1] }))
		}
		v.rows = append(v.rows, link("preview", "预览作业", "提交后可从作业记录查看日志", cmdline(args...), func() {
			if strings.TrimSpace(argv[0]) == "" {
				w.notice = "请先填写可执行文件"
				return
			}
			w.review(change{title: "提交作业", method: "job.run", args: args, params: map[string]any{"target": id, "command": append([]string{}, argv...), "detach": true, "inherit_policy": true}, facts: facts})
		}))
		return v
	})
}
