package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
	"bupt.local/cli/internal/shellquote"
)

var stateLabels = map[string]string{
	"authenticated": "已登录", "expired": "已过期", "blocked_by_gateway": "等待 WebVPN", "not_checked": "未检查", "unknown": "未知", "action_required": "需要认证",
	"running": "运行中", "starting": "启动中", "stopping": "停止中", "stopped": "已停止", "ready": "就绪", "released": "已释放", "allocated": "已分配", "preparing": "准备中", "pending": "等待中", "failed": "失败", "complete": "完成", "completed": "完成", "cancelled": "已取消", "rejected": "已拒绝", "uncertain": "待核对", "cleanup_pending": "等待清理", "approved": "已批准", "pending_approval": "等待审批", "session": "实例周期", "allocation": "资源申请", "job": "后台作业", "direct": "直连", "relay": "中继", "release": "停止并释放资源", "stop": "只停止", "none": "保留资源", "delete": "永久删除", "ip_changed": "出口 IP 已变化",
}

func label(s string) string {
	if s == "" {
		return "—"
	}
	if v, ok := stateLabels[s]; ok {
		return v
	}
	return s
}
func active(r domain.Run) bool {
	switch r.State {
	case "done", "complete", "completed", "stopped", "released", "deleted", "failed", "expired", "cancelled", "canceled":
		return false
	}
	return true
}
func moment(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Local().Format("01-02 15:04:05")
}
func yes(v bool) string {
	if v {
		return "是"
	}
	return "否"
}
func cmdline(args ...string) string {
	return shellquote.Command("bupt", args...)
}
func info(key, value string) row { return row{key: key, title: key, detail: value} }
func link(key, title, detail, cmd string, open func()) row {
	return row{key: key, title: title, detail: detail, command: cmd, open: open}
}

// navLink can open a read-only destination with either Enter or Right.
// All other callbacks, including toggles, forms and submissions, require Enter.
func navLink(key, title, detail, cmd string, open func()) row {
	r := link(key, title, detail, cmd, open)
	r.forward = true
	return r
}
func (w *workspace) local(title, cmd string, body func() content) {
	w.push(&page{title: title, command: cmd, build: body})
}
func (w *workspace) needsLogin() bool {
	s := w.bar.snapshot.Effective(time.Now())
	return s.VPN == "expired" || s.VPN == "unauthenticated" || s.Platform == "expired" || s.Platform == "blocked_by_gateway" || s.Platform == "unauthenticated"
}
func (w *workspace) home() content { return w.serviceHome() }
func (w *workspace) openArea(a navigation.Area) {
	if a.ID == "api" {
		w.apiHome()
		return
	}
	if a.ID == "compute" {
		w.computeHome()
		return
	}
	sections := map[string]struct {
		cmd  string
		body func() content
	}{
		"compute": {"bupt ls", w.instances}, "resources": {"bupt resources", w.resources},
		"work": {"bupt job ls", w.work}, "files": {"bupt data stores", w.files}, "access": {"bupt status", w.access},
	}
	section := sections[a.ID]
	w.push(&page{title: a.Name, area: a.ID, command: section.cmd, build: section.body})
}
func (w *workspace) instance(id domain.ID) (domain.Instance, bool) {
	for _, i := range w.report.Instances {
		if i.ID == id {
			return i, true
		}
	}
	return domain.Instance{}, false
}
func (w *workspace) instanceName(i domain.Instance) string {
	var name string
	var at time.Time
	for _, r := range w.report.Runs {
		if r.InstanceID == i.ID && r.Name != "" && (name == "" || r.CreatedAt.After(at)) {
			at = r.CreatedAt
			name = r.Name
		}
	}
	if name == "" {
		return i.Name
	}
	return name
}
func (w *workspace) instances() content {
	v := content{split: true, empty: "当前账号暂无实例。"}
	if w.hasReport && !w.report.SnapshotAvailable && w.report.Source != "live" && len(w.report.Instances) == 0 {
		v.empty = "尚无实例快照，可从「" + navigation.AccountTitle + "」登录。"
	}
	now := time.Now()
	for _, i := range w.report.Instances {
		title := i.Name
		if alias := w.instanceName(i); alias != title {
			title += " · " + alias
		}
		res := "未预留"
		if i.Reserved {
			res = "已预留"
		}
		ssh := "SSH " + label(w.instanceSSHState(i))
		run := w.instanceRun(i)
		expiry, next := instanceTiming(i.ExpiresAt, run, now)
		if !next.IsZero() && (v.redrawAt.IsZero() || next.Before(v.redrawAt)) {
			v.redrawAt = next
		}
		r := navLink(string(i.ID), title, strings.Join([]string{ssh, expiry, label(i.State), res}, " · "), cmdline("inspect", string(i.ID)), func() { w.instancePage(i.ID) })
		r.preview = []string{"实例 " + string(i.ID), "状态 " + label(i.State) + " · " + res}
		if len(i.Nodes) > 0 {
			if spec := resourceSummary(i.Nodes[0].Resources); spec != "" {
				r.detail += " · " + spec
			}
		}
		if rate := dailyRate(i.Meta); rate != "" {
			r.detail += " · " + rate + " 积分/天"
			r.preview = append(r.preview, "日价 "+rate+" 积分")
		}
		if !i.ExpiresAt.IsZero() {
			r.preview = append(r.preview, "平台预留到期 "+moment(i.ExpiresAt))
		}
		if run != nil {
			r.preview = append(r.preview, runDeadline(*run))
		}
		for _, n := range i.Nodes {
			node := n.Name
			if resources := resourceSummary(n.Resources); resources != "" {
				node += " · " + resources
			}
			r.preview = append(r.preview, "节点 "+node)
		}
		v.rows = append(v.rows, r)
	}
	targets := w.sshTargets().rows
	if len(targets) == 1 {
		v.rows = append(v.rows, targets[0])
	} else if len(targets) > 1 {
		v.rows = append(v.rows, navLink("ssh/choose", "选择 SSH 连接", fmt.Sprintf("%d 个就绪节点", len(targets)), "bupt compute instance", func() { w.picker("选择 SSH 连接", w.sshTargets) }))
	}
	for _, r := range w.report.Runs {
		if awaitingInstance(r) {
			v.rows = append(v.rows, navLink("pending/"+r.ID, "申请进度 · "+r.Name, label(r.State), cmdline("status", r.ID), func() {
				w.instanceWorkspace("", r.Name, &instanceOperation{id: r.ID, label: "创建实例", method: "instance.create"})
				w.refreshStatus(false)
			}))
		}
	}
	if !w.Cached {
		if len(v.rows) == 0 {
			v.facts = append(v.facts, v.empty)
		}
		v.rows = append(v.rows, navLink("new", "创建实例", "选择模板，核对资源和时限", "bupt compute catalog", w.chooseTemplate))
	}
	return v
}

func (w *workspace) instanceRun(i domain.Instance) *domain.Run {
	var latest *domain.Run
	for n := range w.report.Runs {
		r := &w.report.Runs[n]
		if r.InstanceID != i.ID || r.ID == "" || r.State == "" || !active(*r) {
			continue
		}
		if r.ID == i.RunID {
			return r
		}
		if i.RunID == "" && (latest == nil || r.CreatedAt.After(latest.CreatedAt)) {
			latest = r
		}
	}
	return latest
}

func runDeadline(r domain.Run) string {
	action := "到期处理待确认"
	if r.Policy.Cleanup != "" {
		action = "到期" + label(r.Policy.Cleanup)
	}
	return "本次托管截止 " + moment(r.Deadline) + " · " + action
}

// Display the first relevant boundary. A keep-resources policy does not stop the machine.
func instanceTiming(platform time.Time, r *domain.Run, now time.Time) (string, time.Time) {
	if r != nil && (r.Phase == "cleanup" || r.State == "cleanup_pending" || r.CancelRequested) {
		return "正在核对清理结果", time.Time{}
	}
	if r != nil && r.Phase == "manual_wait" {
		return "等待人工处理", time.Time{}
	}
	action := ""
	if r != nil {
		switch r.Policy.Cleanup {
		case "stop":
			action = "停止运行"
		case "release":
			action = "停止并释放"
		case "delete":
			action = "删除实例及磁盘"
		}
	}
	if action != "" && !r.Deadline.IsZero() && (platform.IsZero() || !r.Deadline.After(platform)) {
		if !r.Deadline.After(now) {
			return "托管已到期 · 清理待核对", time.Time{}
		}
		left, next := timeLeft(r.Deadline, now)
		return left + "后" + action, next
	}
	if platform.IsZero() {
		return "平台到期待确认", time.Time{}
	}
	if !platform.After(now) {
		return "平台预留已到期", time.Time{}
	}
	left, next := timeLeft(platform, now)
	return "平台预留剩余 " + left, next
}

// Also return when this label next changes, so idle lists need no network poll.
func timeLeft(expires, now time.Time) (string, time.Time) {
	left := expires.Sub(now)
	minutes := int64((left-1)/time.Minute) + 1
	next := expires.Add(-time.Duration(minutes-1) * time.Minute)
	if minutes < 60 {
		return fmt.Sprintf("%d 分钟", minutes), next
	}
	if minutes%60 == 0 {
		return fmt.Sprintf("%d 小时", minutes/60), next
	}
	return fmt.Sprintf("%d小时%d分", minutes/60, minutes%60), next
}

func (w *workspace) verifiedNode(i domain.Instance, route domain.Route) (domain.Node, bool) {
	if route.State != "ready" || route.NodeID == "" || route.InstanceID == "" {
		return domain.Node{}, false
	}
	live := w.report.Source == "live"
	if i.State != "running" && (live || i.State != "" && i.State != "unknown") {
		return domain.Node{}, false
	}
	for _, n := range i.Nodes {
		if n.ID == route.NodeID {
			return n, n.State != "stopped"
		}
	}
	// A verified local route can outlive the school session or its snapshot.
	return domain.Node{ID: route.NodeID}, !live && len(i.Nodes) == 0
}

func (w *workspace) instanceSSHState(i domain.Instance) string {
	for _, route := range w.report.Routes {
		if route.InstanceID == i.ID {
			if _, ok := w.verifiedNode(i, route); ok {
				return "ready"
			}
		}
	}
	if i.SSHState == "ready" {
		return "unknown"
	}
	return i.SSHState
}

func (w *workspace) sshTargets() content {
	v := content{empty: "当前没有已准备好的 SSH 连接。"}
	if w.Cached {
		return v
	}
	for _, route := range w.report.Routes {
		i, found := w.instance(route.InstanceID)
		if !found {
			i = domain.Instance{ID: route.InstanceID, Name: route.Name}
		}
		n, ok := w.verifiedNode(i, route)
		if !ok {
			continue
		}
		name := cmp.Or(w.instanceName(i), route.Name, string(i.ID))
		n.Name = cmp.Or(n.Name, string(n.ID))
		r := w.sshRow(route.InstanceID, n, true)
		r.key = "ssh/" + string(route.InstanceID) + "/" + string(n.ID)
		r.title = "连接 " + name
		r.detail = "SSH 就绪 · " + n.Name + " · 实例 " + string(route.InstanceID)
		v.rows = append(v.rows, r)
	}
	return v
}

type instanceOperation struct {
	id, label, method string
	task, observed    bool
	record            domain.TaskRecord
	problem           string
}

func (w *workspace) instancePage(id domain.ID) {
	title := string(id)
	if i, ok := w.instance(id); ok && i.Name != "" {
		title = i.Name
	}
	w.instanceWorkspace(id, title, nil)
}
func (w *workspace) instanceWorkspace(id domain.ID, title string, tracking *instanceOperation) *page {
	p := &page{title: title, area: "compute", bench: "instance", instanceID: id, tracking: tracking, command: cmdline("inspect", string(id))}
	p.build = func() content { return w.instanceContent(p) }
	w.push(p)
	return p
}
func (w *workspace) instanceContent(p *page) content {
	op := p.tracking
	var progress []string
	outstanding := false
	expectInstance := false
	if op != nil {
		state := "请求已提交"
		outstanding = true
		if op.task {
			t := op.record
			if t.ID == op.id && t.State != "" {
				state = label(t.State)
				outstanding = !slices.Contains([]string{"complete", "aborted", "failed", "rejected", "need_repeat"}, t.State)
				if t.Result != nil && t.Result.Message != "" {
					progress = append(progress, t.Result.Message)
				}
			}
			if op.problem != "" {
				progress = append(progress, op.problem)
			}
		} else {
			for _, r := range w.report.Runs {
				if r.ID == op.id && (p.instanceID == "" || r.InstanceID == "" || r.InstanceID == p.instanceID) {
					if r.InstanceID != "" {
						p.instanceID = r.InstanceID
						p.command = cmdline("inspect", string(r.InstanceID))
					}
					state = label(r.State)
					outstanding = active(r) && !slices.Contains([]string{"ready", "running", "allocated"}, r.State)
					expectInstance = active(r)
					if p.instanceID == "" && active(r) {
						outstanding = true
					}
					if r.LastError != "" {
						progress = append(progress, "诊断  "+r.LastError)
					}
					break
				}
			}
		}
		progress = append([]string{op.label + " · " + state + "  /  " + op.id}, progress...)
	}
	id := p.instanceID
	cmd := p.command
	i, ok := w.instance(id)
	if !ok {
		p.watch = !w.Cached && (outstanding || expectInstance)
		if op != nil && op.method == "instance.delete" && op.record.State == "complete" && w.report.Source == "live" {
			progress = append(progress, "学校已确认销毁实例及磁盘。")
		} else if op != nil && (outstanding || expectInstance) {
			progress = append(progress, "正在等待学校更新实例状态。")
		} else {
			progress = append(progress, "当前快照中没有此实例。")
		}
		return content{facts: progress, rows: []row{link("refresh", "刷新实例状态", "", "", w.refresh)}}
	}
	if i.Name != "" {
		p.title = i.Name
	}
	if op != nil && op.task && op.record.State == "complete" && !op.observed {
		switch op.method {
		case "instance.stop":
			outstanding = i.State != "stopped"
		case "instance.release":
			outstanding = i.Reserved
		case "instance.delete":
			outstanding = true
		}
		if outstanding {
			progress = append(progress, "已收到操作回执，正在核对实例状态。")
		} else if w.report.Source == "live" {
			op.observed = true
		}
	}
	if op != nil && op.method == "up" && expectInstance && !op.observed {
		if i.State != "running" {
			outstanding = true
		} else if !outstanding && w.report.Source == "live" {
			op.observed = true
		}
	}
	if op != nil && w.statusArea == "compute" && w.statusErr != "" {
		progress = append(progress, w.statusErr)
	}
	if op != nil && w.report.Source == "cached" {
		progress = append(progress, "实例状态来自控制器快照。")
	}
	p.watch = !w.Cached && (outstanding || slices.Contains([]string{"starting", "stopping", "pending_approval", "queuing", "queued", "restoring"}, i.State))
	sshState := w.instanceSSHState(i)
	v := content{facts: []string{fmt.Sprintf("%s   ·   已预留 %s   ·   SSH %s", label(i.State), yes(i.Reserved), label(sshState)), "实例 " + string(i.ID) + "   ·   逻辑名 " + w.instanceName(i)}}
	v.facts = append(progress, v.facts...)
	if i.Health != "" {
		v.facts = append(v.facts, "健康状态  "+label(i.Health))
	}
	if !i.ExpiresAt.IsZero() {
		v.facts = append(v.facts, "平台预留到期  "+moment(i.ExpiresAt))
	}
	run := w.instanceRun(i)
	if run != nil {
		if !run.CreatedAt.IsZero() {
			v.rows = append(v.rows, info("托管开始", moment(run.CreatedAt)))
		}
		v.facts = append(v.facts, runDeadline(*run))
	}
	if !w.Cached {
		if i.State == "running" || sshState == "ready" {
			v.rows = append(v.rows, w.sshRows(i, w.report.Routes, "")...)
			for _, n := range i.Nodes {
				if i.State != "running" || n.State == "stopped" || !readyRoute(w.report.Routes, i.ID, n.ID) {
					continue
				}
				for _, svc := range n.Services {
					if svc.Healthy && svc.InnerPort > 0 && svc.InnerPort <= 65535 && slices.Contains([]string{"jupyter", "jupyterhub", "code-server", "vscode", "webide"}, strings.ToLower(svc.Name)) {
						args := []string{"forward", "--node", string(n.ID), "--", string(i.ID), strconv.Itoa(svc.InnerPort)}
						v.rows = append(v.rows, link("ide/"+string(n.ID)+"/"+string(svc.ID), "转发 "+svc.Name+" 到本机", n.Name+" · 在浏览器使用终端显示的本机端口", cmdline(args...), func() { w.foreground(args...) }))
					}
				}
			}
			if sshState == "ready" {
				v.rows = append(v.rows, link("upload", "上传代码到此实例", "选择本机文件和实例路径", cmdline("cp", "LOCAL", string(id)+":PATH"), func() { w.uploadToInstance(i) }))
			}
			if i.Managed {
				v.rows = append(v.rows, navLink("job", "提交后台作业", "沿用此周期的截止时间", cmdline("run", "--instance", string(id), "--", "COMMAND"), func() { w.jobForm(i) }))
			}
		}
		if i.State == "stopped" || i.State == "approved" || i.State == "running" && !i.Managed {
			name := "启动并准备 SSH"
			if i.State == "running" {
				name = "设置本次运行期限"
			}
			v.rows = append(v.rows, link("start", name, "核对期限和到期处理，准备运行程序", "bupt up "+quoteArg(w.instanceName(i)), func() { w.startForm(i) }))
		}
	}
	if run != nil {
		v.rows = append(v.rows, navLink("progress", "查看本次运行进度", label(run.State)+" · 下一步操作", cmdline("status", run.ID), func() { w.runPage(run.ID) }))
	}
	for _, n := range i.Nodes {
		v.rows = append(v.rows, navLink("node/"+string(n.ID), "节点 "+n.Name, resourceSummary(n.Resources), cmd, func() { w.nodePage(i, n) }))
	}
	w.lifecycleRows(&v, i)
	v.rows = append(v.rows, navLink("history", "运行与期限", "本实例的作业、清理策略和历史回执", cmdline("status", string(id)), func() { w.runsPage(id, false) }))
	v.rows = append(v.rows, navLink("billing", "查看费用与计量", "查看此实例的计费时长与学校实扣记录", "bupt compute usage", func() { w.instanceBills(i) }))
	if !w.Cached && i.State == "stopped" && !i.Reserved {
		v.rows = append(v.rows, link("delete", "销毁实例及磁盘…", "需要输入实例名称确认", cmdline("instance", "delete", string(id)), func() { w.deleteReview(i) }))
	}
	v.rows = append(v.rows, navLink("maintenance", "维护与记录", "本实例的操作回执和维护选项", "bupt help instance", func() {
		w.local("维护与记录", "bupt help instance", func() content {
			current, ok := w.instance(id)
			if !ok {
				return content{empty: "当前快照中已找不到此实例。"}
			}
			maintenance := content{rows: []row{navLink("receipts", "实例操作回执", "查看与此实例相关的操作结果", "bupt task ls", func() { w.scopedTasks(id) })}}
			if !w.Cached {
				target := string(current.ID)
				maintenance.rows = append(maintenance.rows,
					w.contextAction("实例事件", "查看该实例的操作事件", []string{"compute", "instance", "events"}, target),
					w.contextAction("等待状态", "等待实例进入指定状态", []string{"compute", "instance", "wait"}, target),
				)
				if len(current.Nodes) == 1 {
					maintenance.rows = append(maintenance.rows, w.contextAction("保存为资源预设", "为此实例的配置命名", []string{"compute", "preset", "set"}, "--from="+target))
				}
				if !current.Managed {
					maintenance.rows = append(maintenance.rows, w.contextAction("纳入托管周期", "选择时限和清理策略", []string{"compute", "instance", "adopt"}, target))
				}
				if len(current.Nodes) == 1 && current.Nodes[0].Type == "qemu" && (current.State == "stopped" || current.State == "approved") {
					maintenance.rows = append(maintenance.rows, w.contextAction("克隆实例", "填写新实例名称及其生命周期", []string{"compute", "clone"}, target))
				}
			}
			return maintenance
		})
	}))
	if outstanding {
		v.rows = slices.DeleteFunc(v.rows, func(r row) bool {
			return slices.Contains([]string{"start", "down", "stop", "release", "restore", "delete", "cancel-queue"}, r.key)
		})
	}
	return v
}
func quoteArg(s string) string { return strings.TrimPrefix(cmdline(s), "bupt ") }
func readyRoute(routes []domain.Route, instance, node domain.ID) bool {
	return slices.ContainsFunc(routes, func(r domain.Route) bool {
		return r.InstanceID == instance && r.NodeID == node && r.State == "ready"
	})
}
func (w *workspace) sshRows(i domain.Instance, routes []domain.Route, node domain.ID) []row {
	var ready, pending []row
	if len(i.Nodes) == 0 {
		for _, route := range routes {
			if route.InstanceID == i.ID && (node == "" || node == route.NodeID) {
				if n, ok := w.verifiedNode(i, route); ok {
					n.Name = string(n.ID)
					ready = append(ready, w.sshRow(i.ID, n, true))
				}
			}
		}
		return ready
	}
	for _, n := range i.Nodes {
		if n.ID == "" || n.State == "stopped" || (node != "" && n.ID != node) {
			continue
		}
		verified := readyRoute(routes, i.ID, n.ID)
		r := w.sshRow(i.ID, n, verified)
		if verified {
			ready = append(ready, r)
		} else if i.State == "running" {
			pending = append(pending, r)
		}
	}
	return append(ready, pending...)
}
func (w *workspace) addSSH(v *content, i domain.Instance, n domain.Node) {
	if n.ID != "" && n.State != "stopped" {
		v.rows = append(v.rows, w.sshRow(i.ID, n, readyRoute(w.report.Routes, i.ID, n.ID)))
	}
}
func (w *workspace) sshRow(id domain.ID, n domain.Node, ready bool) row {
	args := []string{"ssh", string(id), "--node", string(n.ID)}
	name, detail := "连接 SSH", n.Name
	if !ready {
		args = []string{"ssh", "ensure", string(id), "--node", string(n.ID)}
		name, detail = "准备 SSH", n.Name+" · 核对后安装并验证公钥"
	}
	r := link("ssh/"+string(n.ID), name, detail, cmdline(args...), func() {
		if ready {
			w.foreground(args...)
		} else {
			w.terminalReview(name, detail, args)
		}
	})
	r.verb = name
	return r
}
func (w *workspace) node(instanceID, nodeID domain.ID) (domain.Instance, domain.Node, bool) {
	i, ok := w.instance(instanceID)
	if ok {
		for _, n := range i.Nodes {
			if n.ID == nodeID {
				return i, n, true
			}
		}
	}
	return i, domain.Node{}, false
}
func (w *workspace) nodePage(i domain.Instance, n domain.Node) {
	w.local(n.Name, cmdline("inspect", string(i.ID)), func() content {
		current, n, ok := w.node(i.ID, n.ID)
		if !ok {
			return content{empty: "当前实例中已找不到此节点。"}
		}
		v := content{facts: []string{n.Type + " · " + n.Role + " · " + label(n.State), resourceSummary(n.Resources)}}
		target, node := string(current.ID), "--node="+string(n.ID)
		v.rows = []row{info("节点 ID", string(n.ID)), info("登录用户", n.Username)}
		if n.Image != "" {
			v.rows = append(v.rows, info("镜像", n.Image))
		}
		if n.Type == "qemu" || len(n.Snapshots) > 0 {
			v.rows = append(v.rows, navLink("snapshots", "节点快照", fmt.Sprintf("%d 个", len(n.Snapshots)), cmdline("compute", "snapshot", "ls", target, node), func() { w.nodeSnapshots(current, n) }))
		}
		if !w.Cached {
			if current.State == "running" {
				v.rows = append(v.rows, navLink("connection", "连接与终端", "SSH、命令、隧道与文件传输", cmdline("compute", "ssh", target, node), func() {
					w.nodeConnections(current.ID, n.ID)

				}))
			}
			canResize := (!current.Reserved || current.ReserveType == "no") && slices.Contains([]string{"stopped", "approved", "pending_approval", "queuing", "reserve_queuing"}, current.State)
			canGrow := n.Type == "qemu" && (current.State == "stopped" || current.State == "approved")
			if canResize || canGrow {
				detail := "填写模板允许调整的资源"
				if !canResize {
					detail = "指定 QEMU 磁盘的目标总容量"
				}
				v.rows = append(v.rows, w.contextAction("调整节点规格", detail, []string{"compute", "resize"}, node, target))
			}
			if current.State == "running" || current.State == "stopped" {
				v.rows = append(v.rows, w.contextAction("新增服务映射", "为此节点指定服务名称、绑定地址和端口", []string{"compute", "service", "add"}, node, target))
			}
		}
		for _, s := range n.Services {
			detail := s.Type
			if s.Port > 0 {
				detail += fmt.Sprintf(" · %s:%d", s.Host, s.Port)
			}
			v.rows = append(v.rows, row{key: "service/" + string(s.ID), title: "服务 " + s.Name, detail: detail})
			if !w.Cached && (current.State == "running" || current.State == "stopped") && s.Type == "tcp" && !s.Builtin && s.ID != "" {
				remove := w.contextAction("移除服务 "+s.Name, detail, []string{"compute", "service", "delete"}, node, target, string(s.ID))
				remove.key = "remove-service/" + string(s.ID)
				v.rows = append(v.rows, remove)
			}
		}
		for _, s := range n.Storages {
			v.rows = append(v.rows, info("磁盘 / "+value(s["type"]), strings.TrimSpace(value(s["nodePath"])+" "+quantity(s["size"], 1024, " GiB"))))
		}
		return v
	})
}
func (w *workspace) nodeConnections(instanceID, nodeID domain.ID) {
	target, node := string(instanceID), "--node="+string(nodeID)
	w.local("连接与终端", cmdline("compute", "ssh", target, node), func() content {
		current, n, ok := w.node(instanceID, nodeID)
		if !ok || current.State != "running" || n.State == "stopped" || w.Cached {
			return content{empty: "此节点当前未运行。"}
		}
		c := content{}
		w.addSSH(&c, current, n)
		c.rows = append(c.rows,
			w.contextAction("SSH 会话选项", "选择 tmux 会话或设置断线重连", []string{"compute", "ssh"}, node, target),
			w.contextAction("运行远端命令", "按参数输入要执行的程序", []string{"compute", "exec"}, node, target),
			w.contextAction("学校终端", "通过算力平台进入此节点", []string{"compute", "console"}, node, target),
			w.contextAction("本地端口转发", "选择节点上需要访问的端口", []string{"compute", "forward"}, node, target),
			w.contextAction("打开 SFTP", "在此节点上传或下载文件", []string{"compute", "sftp"}, node, target),
		)
		return c
	})
}

func (w *workspace) loginMethods() {
	if w.Cached {
		return
	}
	origin := w.current()
	menu := &page{title: "学校登录", command: "bupt login", loginMenu: true}
	login := func(args ...string) {
		if !w.foreground(args...) || w.current() != menu {
			return
		}
		w.bar.loginRevision++ // Compute can require a new ticket while the portal remains signed in.
		w.connection.offerLogin = false
		w.back()
		if w.current() == origin && origin.blockedByLogin && !origin.loading && !origin.submitting && origin.queryMethod != "" {
			w.fetchPage(origin, true)
		}
		w.notice = "学校登录已完成"
	}
	menu.build = func() content {
		label := "先完成 WebVPN 扫码，再用下列方式确认学校身份。"
		pr, _ := w.Config.SelectedProfile()
		if pr.GatewayURL == "" || w.bar.snapshot.Effective(time.Now()).VPN == "authenticated" {
			label = "选择统一身份认证方式。学校要求核验网络时会先显示 WebVPN 二维码。"
		}
		return content{facts: []string{label}, rows: []row{
			link("qr", "扫码登录", "在手机上确认学校身份", "bupt login", func() { login("login") }),
			link("sso-password", "账号密码登录", "使用统一身份认证账号", "bupt login --sso-password", func() { login("login", "--sso-password") }),
		}}
	}
	w.push(menu)
}
func (w *workspace) access() content {
	v := content{facts: []string{"当前配置 " + w.Config.Selected + "   ·   SSH 路由 " + strconv.Itoa(len(w.report.Routes))}}
	s := w.bar.snapshot.Effective(time.Now())
	v.rows = []row{navLink("account", "查看学校账号", "校园身份、账户余额和服务协议", "bupt account", func() { w.portalPage("account", "学校账号", "bupt account", nil) })}
	if w.bar.watchProblem != "" && w.bar.watchProblem != w.connection.detail {
		v.rows = append(v.rows, info("状态通知", w.bar.watchProblem))
	}
	if !w.Cached && w.connection.code == "" {
		if !w.needsLogin() && s.Platform != "authenticated" {
			v.rows = append([]row{navLink("login", "登录学校账号", "选择统一身份认证方式", "bupt login", w.loginMethods)}, v.rows...)
		}
		if s.VPN == "authenticated" || s.Platform == "authenticated" {
			v.rows = append(v.rows, navLink("logout", "退出登录", "结束当前配置的学校会话", "bupt logout", func() {
				w.terminalReview("退出登录", "退出 "+w.Config.Selected+" 的学校会话并清除已保存的平台口令和实例终端密码。", []string{"logout"})
			}))
		}
		v.rows = append(v.rows, link("password", "保存实例终端密码", "首次准备 SSH 时使用", "bupt auth access", func() { w.foreground("auth", "access") }))
	}
	v.rows = append(v.rows, navLink("topbar", prefsTitle, "选择顶栏信息和自动核验方式", "", w.topbarSettings))
	for _, r := range w.report.Routes {
		detail := label(r.Kind) + " · " + label(r.State) + " · " + moment(r.VerifiedAt)
		v.rows = append(v.rows, navLink("route/"+string(r.InstanceID)+"/"+string(r.NodeID), "SSH · "+r.Name, detail, cmdline("ssh", string(r.InstanceID)), func() { w.instancePage(r.InstanceID) }))
	}
	p, _ := w.Config.SelectedProfile()
	kind := "本机"
	if p.Controller != "local" && p.Controller != "" {
		kind = "远程"
	}
	v.rows = append(v.rows, info("控制器", kind+" · "+label(w.report.Controller)))
	for _, x := range []struct{ id, title string }{{"news", "平台动态"}, {"agreement", "服务协议"}} {
		v.rows = append(v.rows, navLink(x.id, x.title, "", cmdline(x.id), func() { w.portalPage(x.id, x.title, cmdline(x.id), nil) }))
	}
	return v
}
func value(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func quantity(v any, divisor float64, unit string) string {
	n, e := strconv.ParseFloat(value(v), 64)
	if e != nil {
		return ""
	}
	return strconv.FormatFloat(n/divisor, 'f', -1, 64) + unit
}
func resourceSummary(m map[string]any) string {
	var fields []string
	for _, f := range []struct {
		k string
		d float64
		u string
	}{{"gpu", 100, " GPU"}, {"cpu", 1, " 核"}, {"memory", 1024, " GiB 内存"}, {"blockStorage", 1024, " GiB 磁盘"}} {
		if q := quantity(m[f.k], f.d, f.u); q != "" {
			fields = append(fields, q)
		}
	}
	return strings.Join(fields, " · ")
}
func orderedKeys[V any](m map[string]V) []string {
	r := make([]string, 0, len(m))
	for k := range m {
		r = append(r, k)
	}
	slices.Sort(r)
	return r
}

func (w *workspace) instanceBills(i domain.Instance) {
	w.usageWorkspace(30, true, i.ID)
}
