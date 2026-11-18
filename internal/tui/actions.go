package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

type change struct {
	title, method string
	args          []string
	params        map[string]any
	facts         []string
	resources     []string
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
	params := maps.Clone(op.params)
	params["idempotency_key"] = uuid.NewString()
	params["wait"] = false
	test := maps.Clone(params)
	test["dry_run"] = true
	command := cmdline(op.args...)
	type preview struct {
		DryRun      bool                                      `json:"dry_run"`
		Run         *domain.Run                               `json:"run"`
		Constraints map[string][]struct{ Type, Value string } `json:"resource_constraints"`
	}
	var p *page
	p = queryPage(w, "确认"+op.title, command, op.method, test, true, func(plan preview) content {
		lines := slices.Clone(op.facts)
		if !plan.DryRun {
			return w.textContent("控制器未返回有效预览")
		}
		switch op.method {
		case "up", "instance.create", "instance.restore":
			if plan.Run == nil || plan.Run.Deadline.IsZero() {
				return w.textContent("尚未取得托管截止时间，请返回核对时限。")
			}
		}
		if name, ok := params["name"].(string); ok {
			lines = append(lines, "实例名称  "+name)
		}
		if len(plan.Constraints) == 0 {
			lines = append(lines, op.resources...)
			if params["resources"] != nil {
				lines = append(lines, "完整资源组合以学校核定结果为准。")
			}
		} else {
			for _, group := range orderedKeys(plan.Constraints) {
				var hardware []string
				for _, field := range plan.Constraints[group] {
					switch field.Type {
					case "GPU":
						hardware = append(hardware, gpuConstraint(field.Value))
					case "CPU":
						hardware = append(hardware, field.Value+" 核")
					case "Memory":
						hardware = append(hardware, field.Value+" MiB 内存")
					case "AccelDisk":
						hardware = append(hardware, field.Value+" MiB 加速磁盘")
					}
				}
				if len(hardware) != 0 {
					label := "本次配置"
					if len(plan.Constraints) > 1 {
						label = "节点组 " + group
					}
					lines = append(lines, label+"  "+strings.Join(hardware, " · "))
				}
			}
		}
		if plan.Run != nil {
			limit := "截止"
			if _, ok := params["max_runtime"].(string); ok {
				limit = "预计截止"
			}
			lines = append(lines, limit+"  "+moment(plan.Run.Deadline), "到期处理  "+label(plan.Run.Policy.Cleanup)+" · 自动续期 "+yes(plan.Run.Policy.AutoRenew))
		}
		v := w.textContent(strings.Join(lines, "\n"))
		v.rows = append(v.rows, navLink("back", "返回修改", "保留本次填写的内容", "", w.back))
		submit := link("submit", "确认"+op.title, "执行已核对的操作", command, func() {
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
		})
		submit.verb = "确认" + op.title
		v.rows = append(v.rows, submit)
		return v
	})
	w.push(p)
}
func gpuConstraint(raw string) string {
	parts := strings.Split(raw, ":")
	if len(parts) == 2 || len(parts) == 3 {
		fraction, err := strconv.ParseFloat(parts[0], 64)
		if err == nil && fraction > 0 && parts[1] != "" {
			name := parts[0] + " 张 " + parts[1]
			if len(parts) == 2 {
				return name
			}
			if _, err := strconv.ParseUint(parts[2], 10, 64); err == nil {
				return name + " · 显存 " + parts[2] + " MiB"
			}
		}
	}
	return "GPU " + raw
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
				var detail *domain.Error
				if errors.As(err, &detail) {
					switch {
					case detail.RunID != "":
						w.runPage(detail.RunID)
					case detail.TaskID != "":
						w.taskPage(detail.TaskID)
					}
				}
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

func (w *workspace) field(key, title string, dest *string, placeholder string) row {
	detail := *dest
	if detail == "" {
		detail = placeholder
	}
	r := link(key, title, detail, "", func() { w.edit(title, *dest, func(s string) error { *dest = strings.TrimSpace(s); return nil }) })
	r.verb = "编辑"
	return r
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
