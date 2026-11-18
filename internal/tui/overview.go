package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
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
	parts := []string{"bupt"}
	for _, arg := range args {
		if arg == "" || strings.ContainsFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_.-/:=@", r))
		}) {
			arg = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
		parts = append(parts, arg)
	}
	return strings.Join(parts, " ")
}
func info(key, value string) row { return row{key: key, title: key, detail: value} }
func link(key, title, detail, cmd string, open func()) row {
	return row{key: key, title: title, detail: detail, command: cmd, open: open}
}
func (w *workspace) local(title, cmd string, body func() content) {
	w.push(&page{title: title, command: cmd, build: body})
}
func (w *workspace) needsLogin() bool {
	return w.report.Gateway == "expired" || w.report.Platform == "expired" || w.report.Platform == "blocked_by_gateway" || w.report.Platform == "unauthenticated"
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
		"work": {"bupt job ls", w.work}, "files": {"bupt data stores", w.files}, "access": {"bupt auth status", w.access},
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
	v := content{empty: "当前账号暂无实例。"}
	if w.hasReport && !w.report.SnapshotAvailable && w.report.Source != "live" && len(w.report.Instances) == 0 {
		v.empty = "尚无实例快照，可从账号与连接登录。"
	}
	for _, i := range w.report.Instances {
		title := i.Name
		if alias := w.instanceName(i); alias != title {
			title += " · " + alias
		}
		res := "未预留"
		if i.Reserved {
			res = "已预留"
		}
		v.rows = append(v.rows, link(string(i.ID), title, label(i.State)+" · "+res+" · SSH "+label(i.SSHState), cmdline("inspect", string(i.ID)), func() { w.instancePage(i.ID) }))
	}
	if !w.Cached {
		v.rows = append(v.rows, link("new", "+ 从模板新建", "选择模板和资源，再预览申请", "bupt spec ls", w.catalog))
	}
	return v
}
func (w *workspace) instancePage(id domain.ID) {
	cmd := cmdline("inspect", string(id))
	title := string(id)
	if i, ok := w.instance(id); ok {
		title = i.Name
	}
	w.local(title, cmd, func() content {
		i, ok := w.instance(id)
		if !ok {
			return content{empty: "当前快照中已找不到此实例。"}
		}
		v := content{facts: []string{fmt.Sprintf("%s   ·   已预留 %s   ·   SSH %s", label(i.State), yes(i.Reserved), label(i.SSHState)), "实例 " + string(i.ID) + "   ·   逻辑名 " + w.instanceName(i)}}
		if !i.ExpiresAt.IsZero() {
			v.facts = append(v.facts, "平台预计到期  "+moment(i.ExpiresAt))
		}
		if !w.Cached {
			if i.State == "stopped" || i.State == "approved" || i.State == "running" && !i.Managed {
				name := "启动并准备 SSH"
				if i.State == "running" {
					name = "纳入生命周期管理"
				}
				v.rows = append(v.rows, link("start", name, "设置运行上限并预览", "bupt up "+quoteArg(w.instanceName(i)), func() { w.startForm(i) }))
			}
			if i.State == "running" || w.report.Source != "live" && i.SSHState == "ready" {
				for _, n := range i.Nodes {
					w.addSSH(&v, i, n)
				}
				if i.Managed {
					v.rows = append(v.rows, link("job", "提交后台作业", "沿用此周期的截止时间", cmdline("run", "--instance", string(id), "--", "COMMAND"), func() { w.jobForm(i) }))
				}
			}
		}
		for _, n := range i.Nodes {
			v.rows = append(v.rows, link("node/"+string(n.ID), "节点 "+n.Name, resourceSummary(n.Resources), cmd, func() { w.nodePage(i, n) }))
		}
		w.lifecycleRows(&v, i)
		v.rows = append(v.rows, link("history", "运行与期限", "本实例的作业、清理策略和历史回执", cmdline("status", string(id)), func() { w.runsPage(id, false) }))
		if !w.Cached {
			if i.State == "running" || i.Reserved || i.Managed {
				v.rows = append(v.rows, link("down", "停止并释放", "保留实例磁盘", cmdline("down", string(id), "--cleanup", "release"), func() { w.endReview(i) }))
			}
		}
		v.rows = append(v.rows, link("maintenance", "维护与记录", "本实例的操作回执和维护选项", "bupt help instance", func() {
			w.local("维护与记录", "bupt help instance", func() content {
				current, ok := w.instance(id)
				if !ok {
					return content{empty: "当前快照中已找不到此实例。"}
				}
				maintenance := content{rows: []row{link("receipts", "实例操作回执", "查看与此实例相关的操作结果", "bupt task ls", func() { w.scopedTasks(id) })}}
				if !w.Cached && current.State == "stopped" && !current.Reserved {
					maintenance.rows = append(maintenance.rows, link("delete", "永久删除…", "删除实例和实例磁盘", cmdline("instance", "delete", string(id)), func() { w.deleteReview(current) }))
				}
				return maintenance
			})
		}))
		return v
	})
}
func quoteArg(s string) string { return strings.TrimPrefix(cmdline(s), "bupt ") }
func (w *workspace) addSSH(v *content, i domain.Instance, n domain.Node) {
	var route *domain.Route
	for _, r := range w.report.Routes {
		if r.InstanceID == i.ID && r.NodeID == n.ID && r.State == "ready" {
			route = &r
			break
		}
	}
	args := []string{"ssh", string(i.ID), "--node", string(n.ID)}
	name := "连接 SSH"
	detail := n.Name
	if route == nil {
		args = []string{"ssh", "ensure", string(i.ID), "--node", string(n.ID)}
		name = "准备 SSH"
		detail += " · 安装并验证公钥"
	}
	v.rows = append(v.rows, link("ssh/"+string(n.ID), name, detail, cmdline(args...), func() {
		if route != nil {
			w.foreground(args...)
			return
		}
		w.terminalReview(name, detail, args)
	}))
}
func (w *workspace) nodePage(i domain.Instance, n domain.Node) {
	w.local(n.Name, cmdline("inspect", string(i.ID)), func() content {
		current, ok := w.instance(i.ID)
		if !ok {
			return content{empty: "当前快照中已找不到此实例。"}
		}
		idx := slices.IndexFunc(current.Nodes, func(node domain.Node) bool { return node.ID == n.ID })
		if idx < 0 {
			return content{empty: "当前快照中已找不到此节点。"}
		}
		n := current.Nodes[idx]
		v := content{facts: []string{n.Type + " · " + n.Role + " · " + label(n.State), resourceSummary(n.Resources)}}
		v.rows = []row{info("节点 ID", string(n.ID)), info("登录用户", n.Username), link("snapshots", "节点快照", fmt.Sprintf("%d 个", len(n.Snapshots)), cmdline("compute", "snapshot", "ls", string(i.ID), "--node", string(n.ID)), func() { w.nodeSnapshots(i, n) })}
		for _, s := range n.Services {
			detail := s.Type
			if s.Port > 0 {
				detail += fmt.Sprintf(" · %s:%d", s.Host, s.Port)
			}
			v.rows = append(v.rows, info("服务 "+s.Name, detail))
		}
		for _, s := range n.Storages {
			v.rows = append(v.rows, info("磁盘 / "+value(s["type"]), strings.TrimSpace(value(s["nodePath"])+" "+quantity(s["size"], 1024, " GiB"))))
		}
		return v
	})
}
func (w *workspace) loginMethods() {
	w.local("学校登录", "bupt login", func() content {
		return content{facts: []string{"WebVPN 过期时先扫码。第二步选择统一身份认证方式。"}, rows: []row{
			link("qr", "扫码登录", "用手机确认", "bupt login", func() { w.foreground("login") }),
			link("sso-password", "账号密码登录", "使用学校统一身份认证账号", "bupt login --sso-password", func() { w.foreground("login", "--sso-password") }),
		}}
	})
}
func (w *workspace) access() content {
	v := content{facts: []string{"当前配置 " + w.Config.Selected + "   ·   SSH 路由 " + strconv.Itoa(len(w.report.Routes))}}
	v.rows = []row{info("WebVPN", label(w.report.Gateway)), info("算力平台", label(w.report.Platform))}
	if w.report.AuthReason != "" {
		v.rows = append(v.rows, info("认证状态", label(w.report.AuthReason)))
	}
	if !w.Cached {
		v.rows = append(v.rows, link("login", "学校登录", "选择统一身份认证方式", "bupt login", w.loginMethods),
			link("password", "保存实例终端密码", "首次准备 SSH 时使用", "bupt auth access", func() { w.foreground("auth", "access") }))
	}
	for _, x := range []struct{ id, title string }{{"account", "门户账号"}, {"news", "平台动态"}, {"agreement", "服务协议"}} {
		v.rows = append(v.rows, link(x.id, x.title, "", cmdline(x.id), func() { w.portalPage(x.id, x.title, cmdline(x.id), nil) }))
	}
	for _, r := range w.report.Routes {
		detail := label(r.Kind) + " · " + label(r.State) + " · " + moment(r.VerifiedAt)
		v.rows = append(v.rows, link("route/"+string(r.InstanceID)+"/"+string(r.NodeID), "SSH · "+r.Name, detail, cmdline("ssh", string(r.InstanceID)), func() { w.instancePage(r.InstanceID) }))
	}
	p, _ := w.Config.SelectedProfile()
	kind := "本机"
	if p.Controller != "local" && p.Controller != "" {
		kind = "远程"
	}
	v.rows = append(v.rows, info("控制器", kind+" · "+label(w.report.Controller)))
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
