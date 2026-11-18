package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/rivo/uniseg"
)

func (w *workspace) work() content {
	var jobs int
	for _, r := range w.report.Runs {
		if r.Kind == "job" {
			jobs++
		}
	}
	return content{rows: []row{
		link("jobs", "后台作业", fmt.Sprintf("%d 个 · 命令输出与退出码", jobs), "bupt job ls --filter kind=job", func() { w.runsPage("", true) }),
		link("runs", "全部托管周期", fmt.Sprintf("%d 个 · 实例、作业与清理期限", len(w.report.Runs)), "bupt job ls", func() { w.runsPage("", false) }),
		link("tasks", "平台操作回执", fmt.Sprintf("%d 个待核对 · 申请、启动和释放", w.report.PendingTasks), "bupt task ls", w.tasks),
		link("audit", "控制器事件", "按时间查看操作记录", "bupt audit ls", w.audit),
	}}
}
func (w *workspace) runsPage(instance domain.ID, jobsOnly bool) {
	title, cmd := "托管周期", "bupt job ls"
	if jobsOnly {
		title = "后台作业"
		cmd = "bupt job ls --filter kind=job"
	} else if instance != "" {
		cmd = cmdline("status", string(instance))
	}
	w.local(title, cmd, func() content {
		v := content{empty: "暂无符合条件的运行记录。"}
		for _, r := range newestFirst(w.report.Runs) {
			if instance != "" && r.InstanceID != instance || jobsOnly && r.Kind != "job" {
				continue
			}
			name := r.Name
			if r.JobName != "" {
				name = r.JobName
			}
			detail := moment(r.CreatedAt) + " · " + label(r.Kind) + " · " + label(r.State)
			v.rows = append(v.rows, link(r.ID, name, detail, cmdline("job", "inspect", r.ID), func() { w.runPage(r.ID) }))
		}
		return v
	})
}
func (w *workspace) runPage(id string) {
	cmd := cmdline("job", "inspect", id)
	var page *page
	page = queryPage(w, "运行详情", cmd, "job.get", map[string]any{"target": id}, false, func(r domain.Run) content {
		page.watch = active(r)
		v := content{facts: []string{r.Name + " · " + label(r.Kind) + " · " + label(r.State), "Run " + r.ID}}
		if r.JobName != "" {
			v.rows = append(v.rows, info("作业", r.JobName))
		}
		if r.ExitCode != nil {
			v.rows = append(v.rows, info("退出码", fmt.Sprint(*r.ExitCode)))
		}
		v.rows = append(v.rows, info("截止时间", moment(r.Deadline)), info("清理", label(r.Policy.Cleanup)+" · "+label(r.CleanupState)))
		if active(r) {
			v.rows = append(v.rows, link("lease", "期限与续期", "平台到期时间、托管上限与续期状态", cmdline("lease", "status", r.ID), func() { w.leasePage(r) }))
		}
		if r.LastError != "" {
			v.rows = append(v.rows, info("运行诊断", r.LastError))
		}
		if r.Kind == "job" && !w.Cached {
			for _, stream := range []string{"stdout", "stderr"} {
				v.rows = append(v.rows, link(stream, stream, "查看作业输出", cmdline("job", "logs", id, "--stream", stream), func() { w.logs(id, stream) }))
			}
		}
		if r.InstanceID != "" {
			v.rows = append(v.rows, link("instance", "所属实例", string(r.InstanceID), cmdline("inspect", string(r.InstanceID)), func() { w.instancePage(r.InstanceID) }))
		}
		if r.LastTaskID != "" {
			v.rows = append(v.rows, link("receipt", "最近操作回执", r.LastTaskID, cmdline("task", "show", r.LastTaskID), func() { w.taskPage(r.LastTaskID) }))
		}
		if active(r) && !w.Cached && r.Kind == "job" {
			v.rows = append(v.rows, link("cancel", "取消作业…", "按当前周期策略清理", cmdline("job", "cancel", id), func() { w.cancelReview(r) }))
		}
		return v
	})
	w.push(page)
}
func (w *workspace) tasks() { w.scopedTasks("") }
func (w *workspace) scopedTasks(instance domain.ID) {
	w.push(queryPage(w, "平台操作回执", "bupt task ls", "task.list", nil, false, func(tasks []domain.TaskRecord) content {
		tasks = slices.Clone(tasks)
		slices.SortStableFunc(tasks, func(a, b domain.TaskRecord) int { return b.CreatedAt.Compare(a.CreatedAt) })
		v := content{empty: "暂无平台操作回执。"}
		for _, t := range tasks {
			if instance != "" && t.Request.TargetID != instance && (t.Result == nil || t.Result.InstanceID != instance) {
				continue
			}
			v.rows = append(v.rows, link(t.ID, label(t.Operation), moment(t.CreatedAt)+" · "+label(t.State), cmdline("task", "show", t.ID), func() { w.taskPage(t.ID) }))
		}
		return v
	}))
}
func (w *workspace) taskPage(id string) {
	w.push(queryPage(w, "操作回执", cmdline("task", "show", id), "task.get", map[string]any{"target": id}, false, func(t domain.TaskRecord) content {
		v := content{facts: []string{label(t.Operation) + " · " + label(t.State), "Task " + t.ID}, rows: []row{info("提交", moment(t.CreatedAt)), info("更新", moment(t.UpdatedAt))}}
		if t.Result != nil {
			v.rows = append(v.rows, info("平台返回码", fmt.Sprint(t.Result.Code)), info("结果", t.Result.Message))
		}
		if id := t.Request.TargetID; id != "" {
			v.rows = append(v.rows, link("target", "所属实例", string(id), cmdline("inspect", string(id)), func() { w.instancePage(id) }))
		}
		return v
	}))
}
func (w *workspace) audit() {
	w.push(queryPage(w, "控制器事件", "bupt audit ls", "audit.list", map[string]any{"limit": 100}, false, func(events []domain.AuditEvent) content {
		v := content{empty: "暂无控制器事件。"}
		events = slices.Clone(events)
		slices.SortStableFunc(events, func(a, b domain.AuditEvent) int { return b.Time.Compare(a.Time) })
		for i, e := range events {
			v.rows = append(v.rows, link(fmt.Sprint(i), e.Kind, moment(e.Time)+" · "+e.Message, "bupt audit ls", func() { w.textPage(e.Kind, "bupt audit ls", moment(e.Time)+"\n"+e.Message+"\n"+e.Target) }))
		}
		return v
	}))
}

var errLogLimit = errors.New("log preview limit")

type logPreview struct{ bytes.Buffer }

func (p *logPreview) Write(b []byte) (int, error) {
	const limit = 32 * 1024
	left := limit - p.Len()
	if len(b) > left {
		n, _ := p.Buffer.Write(b[:left])
		return n, errLogLimit
	}
	return p.Buffer.Write(b)
}
func (w *workspace) logs(id, stream string) {
	cmd := cmdline("job", "logs", id, "--stream", stream)
	p := &page{title: stream, command: cmd, load: func(ctx context.Context) (func() content, error) {
		var out logPreview
		_, err := w.Backend.Stream(ctx, "job.logs", map[string]any{"target": id, "stream": stream}, nil, &out)
		if err != nil && !errors.Is(err, errLogLimit) && out.Len() == 0 {
			return nil, err
		}
		text := out.String()
		return func() content {
			v := w.textContent(text)
			if out.Len() >= 32*1024 {
				v.facts = []string{"显示前 32 KiB，完整日志可用下方命令读取。"}
			} else if err != nil {
				v.facts = []string{err.Error()}
			}
			return v
		}, nil
	}}
	w.push(p)
}
func (w *workspace) textPage(title, cmd, text string) {
	w.local(title, cmd, func() content { return w.textContent(text) })
}
func (w *workspace) textContent(text string) content {
	v := content{empty: "无输出。"}
	if text == "" {
		return v
	}
	width, _ := w.screen.Size()
	width = max(8, width-6)
	v.fullWidth = true
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		var part strings.Builder
		cells := 0
		emit := func() {
			v.rows = append(v.rows, row{key: fmt.Sprint(len(v.rows)), title: part.String()})
			part.Reset()
			cells = 0
		}
		g := uniseg.NewGraphemes(clean(line))
		for g.Next() {
			if cells+g.Width() > width {
				emit()
			}
			part.WriteString(g.Str())
			cells += g.Width()
		}
		emit()
	}
	return v
}

var _ io.Writer = (*logPreview)(nil)

func (w *workspace) leasePage(r domain.Run) {
	w.local("期限与续期", cmdline("lease", "status", r.ID), func() content {
		// The status overview contains the last observed platform expiry. Its source
		// is shown in the header independently of the controller's finite deadline.
		current := r
		for _, tracked := range w.report.Runs {
			if tracked.ID == r.ID && tracked.UpdatedAt.After(r.UpdatedAt) {
				current = tracked
				break
			}
		}
		v := content{facts: []string{current.Name + " · " + label(current.State)}, rows: []row{info("托管上限", moment(current.Deadline)), info("自动续期", yes(current.Policy.AutoRenew)), info("续期状态", label(current.RenewalState))}}
		if i, ok := w.instance(current.InstanceID); ok {
			v.rows = append(v.rows, info("平台到期", moment(i.ExpiresAt)))
		}
		if !active(current) || w.Cached {
			return v
		}
		v.rows = append(v.rows, link("set", "调整总运行时长…", "从本周期开始时计算", cmdline("lease", "set", r.ID, "--max-runtime", "DURATION"), func() {
			w.edit("总运行时长，例如 2h", current.Policy.MaxRuntime, func(s string) error {
				d, e := time.ParseDuration(s)
				if e != nil || d <= 0 {
					return fmt.Errorf("请输入正的时长")
				}
				deadline := current.CreatedAt.Add(d)
				if !deadline.After(time.Now()) {
					return fmt.Errorf("该总时长已用完，可从实例页停止并释放")
				}
				w.review(change{title: "调整运行上限", method: "lease.set", params: map[string]any{"target": r.ID, "max_runtime": s, "revision": current.PolicyRevision}, args: []string{"lease", "set", r.ID, "--max-runtime", s}, facts: []string{current.Name + " · " + r.ID, "新的托管截止时间 " + moment(deadline)}})
				return nil
			})
		}), link("renew", "向平台申请续期…", "在托管期限内向学校申请延长使用时间", cmdline("lease", "renew", r.ID, "--duration", "DURATION"), func() {
			w.edit("申请增加的时长，例如 30m", "", func(s string) error {
				d, e := time.ParseDuration(s)
				if e != nil || d < time.Second {
					return fmt.Errorf("请输入至少 1 秒的时长")
				}
				w.review(change{title: "申请续期", method: "lease.renew", params: map[string]any{"target": r.ID, "duration": s}, args: []string{"lease", "renew", r.ID, "--duration", s}, facts: []string{current.Name + " · " + r.ID, "增加 " + s + " · 等待学校审批"}, task: true})
				return nil
			})
		}))
		return v
	})
}
