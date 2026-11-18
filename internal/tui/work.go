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
		navLink("jobs", "查看作业输出", fmt.Sprintf("%d 个 · 命令输出与退出码", jobs), "bupt job ls --filter kind=job", func() { w.runsPage("", true) }),
		navLink("runs", "查看实验进度", fmt.Sprintf("%d 个 · 实例、作业与清理期限", len(w.report.Runs)), "bupt job ls", func() { w.runsPage("", false) }),
		navLink("tasks", "查看操作结果", fmt.Sprintf("%d 个待核对 · 申请、启动和释放", w.report.PendingTasks), "bupt task ls", w.tasks),
		navLink("audit", "排查控制器事件", "按时间查看操作记录", "bupt audit ls", w.audit),
	}}
}
func (w *workspace) runsPage(instance domain.ID, jobsOnly bool) {
	title, cmd := "实验进度", "bupt job ls"
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
			v.rows = append(v.rows, navLink(r.ID, name, detail, cmdline("job", "inspect", r.ID), func() { w.runPage(r.ID) }))
		}
		return v
	})
}
func (w *workspace) runPage(id string) {
	cmd := cmdline("job", "inspect", id)
	var p *page
	// This one local read associates the receipt, recorded instance and
	// verified per-node SSH routes without starting a school login or probe.
	p = queryPage(w, "运行详情", cmd, "status.get", map[string]any{"cached": true, "target": id}, false, func(snapshot domain.StatusReport) content {
		at := slices.IndexFunc(snapshot.Runs, func(r domain.Run) bool { return r.ID == id })
		if at < 0 {
			p.watch = false
			return content{empty: "当前账号没有这条运行记录。"}
		}
		r := snapshot.Runs[at]
		p.watch = active(r) && !w.Cached
		v := content{facts: []string{r.Name + " · " + label(r.State)}}
		idx := slices.IndexFunc(snapshot.Instances, func(i domain.Instance) bool { return i.ID != "" && i.ID == r.InstanceID })
		if r.Kind == "job" {
			if r.JobName != "" {
				v.facts = append(v.facts, "作业  "+r.JobName)
			}
			if !w.Cached {
				v.rows = append(v.rows, navLink("stdout", "查看作业输出", "程序标准输出", cmdline("job", "logs", id, "--stream", "stdout"), func() { w.logs(id, "stdout") }),
					navLink("stderr", "查看错误输出", "程序标准错误", cmdline("job", "logs", id, "--stream", "stderr"), func() { w.logs(id, "stderr") }))
			}
		} else if active(r) {
			w.runProgress(&v, p, snapshot, r, idx)
		}
		if r.LastError != "" {
			v.facts = append(v.facts, "运行诊断  "+r.LastError)
		}
		if r.ExitCode != nil {
			v.rows = append(v.rows, info("退出码", fmt.Sprint(*r.ExitCode)))
		}
		v.rows = append(v.rows, info("截止时间", moment(r.Deadline)), info("到期处理", label(r.Policy.Cleanup)+" · "+label(r.CleanupState)), info("运行记录 ID", r.ID))
		if r.InstanceID != "" {
			v.rows = append(v.rows, navLink("instance", "查看所属实例", r.Name, cmdline("inspect", string(r.InstanceID)), func() {
				w.instancePage(r.InstanceID)
				w.refreshStatus(false)
			}))
		}
		if active(r) {
			v.rows = append(v.rows, navLink("lease", "查看或调整期限", "时限、清理方式与续期状态", cmdline("lease", "status", r.ID), func() { w.leasePage(r) }))
		}
		if r.LastTaskID != "" {
			v.rows = append(v.rows, navLink("receipt", "查看最近操作结果", r.LastTaskID, cmdline("task", "show", r.LastTaskID), func() { w.taskPage(r.LastTaskID) }))
		}
		if active(r) && !w.Cached {
			if r.Kind == "job" {
				v.rows = append(v.rows,
					w.contextAction("跟随作业输出", "在前台持续查看输出", []string{"compute", "run", "attach"}, id),
					w.contextAction("等待作业结束", "等待并取得作业退出状态", []string{"compute", "run", "wait"}, id),
					w.contextAction("调整作业策略", "设置托管时限、续期与清理方式", []string{"compute", "run", "update"}, id),
					link("cancel", "取消作业…", "按当前周期策略清理", cmdline("job", "cancel", id), func() { w.cancelReview(r) }))
			} else {
				v.rows = append(v.rows, w.contextAction("等待托管状态", "选择完成条件", []string{"compute", "wait"}, id))
			}
		}
		return v
	})
	w.push(p)
}

func (w *workspace) runProgress(v *content, p *page, report domain.StatusReport, r domain.Run, idx int) {
	var i domain.Instance
	if idx >= 0 {
		i = report.Instances[idx]
	}
	switch {
	case r.Phase == "cleanup" || r.Phase == "manual_wait" || r.CancelRequested || r.State == "cleanup_pending":
		v.facts = append(v.facts, "正在核对停止和资源释放结果")
	case r.LastError == "CONSOLE_LOGIN_REQUIRED" || r.LastError == "CONSOLE_LOGIN_FAILED":
		v.facts = append(v.facts, "准备 SSH 需要有效的实例终端密码")
		if !w.Cached {
			password := link("access", "保存实例终端密码", "继续准备 SSH", "bupt auth access", func() { w.foreground("auth", "access") })
			password.verb = "填写密码"
			v.rows = append(v.rows, password)
		}
	case r.AuthState == "action_required" && !(i.State == "running" && (r.State == "ready" || r.State == "running")):
		v.facts = append(v.facts, "学校要求重新登录，登录后控制器继续处理")
		if !w.Cached {
			v.rows = append(v.rows, navLink("login", "登录学校账号", "继续此实例的管理", "bupt login", w.loginMethods))
		}
	case r.InstanceID == "":
		v.facts = append(v.facts, "控制器已接收申请，正在等待学校确认实例")
	case i.ID == "":
		v.facts = append(v.facts, "已取得实例编号，正在等待节点状态")
	case i.State == "pending_approval" || strings.Contains(i.State, "queu"):
		v.facts = append(v.facts, "等待学校处理  "+label(i.State))
	case r.Kind == "allocation" && r.State == "allocated" && (i.State == "approved" || i.State == "stopped" || i.State == "running"):
		if !r.Deadline.After(time.Now()) {
			v.facts = append(v.facts, "托管期限已结束，正在核对资源状态")
			break
		}
		v.facts = append(v.facts, "资源已分配，可以启动并准备 SSH")
		if !w.Cached {
			start := link("start", "启动并准备 SSH", "沿用当前托管截止时间", cmdline("up", "--adopt", string(i.ID), "--", r.Name), func() { w.startOnNode(i, &r) })
			start.verb = "核对启动"
			v.rows = append(v.rows, start)
		}
	case i.State == "running" && (r.State == "ready" || r.State == "running"):
		if !w.Cached {
			v.rows = append(v.rows, w.sshRows(i, report.Routes, r.NodeID)...)
		}
		if slices.ContainsFunc(i.Nodes, func(n domain.Node) bool {
			return (r.NodeID == "" || n.ID == r.NodeID) && n.State != "stopped" && readyRoute(report.Routes, i.ID, n.ID)
		}) {
			v.facts = append(v.facts, "实例已运行，控制器记录了已验证的节点 SSH 路由")
		} else {
			v.facts = append(v.facts, "实例已运行，SSH 路由尚待验证")
		}
		if r.AuthState == "action_required" && !w.Cached {
			v.rows = append(v.rows, navLink("login", "恢复学校管理会话", "资源管理需要重新登录", "bupt login", w.loginMethods))
		}
	case r.State == "restoring":
		v.facts = append(v.facts, "学校正在恢复此实例的资源预留")
	case r.State == "starting" || i.State == "starting":
		v.facts = append(v.facts, "学校正在启动实例，控制器会继续准备 SSH")
	default:
		v.facts = append(v.facts, "控制器正在核对实例和节点状态")
	}
	if !w.Cached && len(v.rows) == 0 {
		v.rows = append(v.rows, link("progress", "刷新准备进度", "此页也会自动更新", p.command, func() { w.fetchPage(p, false) }))
	}
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
			v.rows = append(v.rows, navLink(t.ID, label(t.Operation), moment(t.CreatedAt)+" · "+label(t.State), cmdline("task", "show", t.ID), func() { w.taskPage(t.ID) }))
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
		if !w.Cached && slices.Contains([]string{"prepared", "submitted", "uncertain", "auth_required"}, t.State) {
			v.rows = append(v.rows, w.contextAction("等待操作完成", "等待控制器确认此回执的结果", []string{"compute", "task", "wait"}, id))
		}
		target := t.Request.TargetID
		if t.Result != nil && t.Result.InstanceID != "" {
			target = t.Result.InstanceID
		}
		if _, ok := w.instance(target); ok {
			v.rows = append(v.rows, navLink("target", "所属实例", string(target), cmdline("inspect", string(target)), func() { w.instancePage(target) }))
		}
		return v
	}))
}
func (w *workspace) audit() {
	w.push(queryPage(w, "控制器事件", "bupt audit ls", "audit.list", map[string]any{"limit": 100}, false, func(events []domain.AuditEvent) content {
		v := content{empty: "暂无控制器事件。"}
		events = slices.Clone(events)
		slices.SortStableFunc(events, func(a, b domain.AuditEvent) int { return b.Time.Compare(a.Time) })
		for _, e := range events {
			key := strings.Join([]string{e.Time.Format(time.RFC3339Nano), e.Kind, e.Target, e.RunID, e.TaskID, e.Message}, "\x00")
			v.rows = append(v.rows, navLink(key, e.Kind, moment(e.Time)+" · "+e.Message, "bupt audit ls", func() { w.textPage(e.Kind, "bupt audit ls", moment(e.Time)+"\n"+e.Message+"\n"+e.Target) }))
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
	v := content{empty: "无输出。", document: true}
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
