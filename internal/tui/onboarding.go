package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/install"
)

type controllerRecovery struct {
	code, detail string
	action       install.WorkspaceAction
	starting     bool
	pending      bool
}

func errorCode(err error) string {
	var detail *domain.Error
	if errors.As(err, &detail) {
		return detail.Code
	}
	return ""
}

func connectionError(code string) bool {
	return code == "AGENT_NOT_RUNNING" || code == "CONTROLLER_CONNECT" || code == "CONTROLLER_UNAVAILABLE"
}

func (w *workspace) reconnectedSince(epoch, code string) bool {
	return connectionError(code) && w.bar.snapshot.Epoch != "" && w.bar.snapshot.Epoch != epoch
}

func remoteTransportError(code string) bool {
	switch code {
	case "", "INVALID_RELAY", "INVALID_HOST_KEY", "INSECURE_KEY_FILE", "INVALID_KEY_FILE", "INVALID_PATH", "CLIENT_REVOKED":
		return true
	default:
		return strings.HasPrefix(code, "SSH_")
	}
}

// An online GPU status request can return a usable cached instance list
// together with a fresh authentication denial. SSH routes remain usable.
func computeLoginCode(s domain.StatusReport) string {
	if s.Gateway == "expired" || s.Platform == "blocked_by_gateway" {
		return "GATEWAY_AUTH_EXPIRED"
	}
	if s.Platform == "expired" || s.Platform == "unauthenticated" || s.AuthReason == "AUTH_1032" {
		return "AUTH_REQUIRED"
	}
	if schoolLoginCode(s.AuthReason) {
		return s.AuthReason
	}
	for _, warning := range s.Warnings {
		if schoolLoginCode(warning) || warning == "AUTH_1032" {
			return "AUTH_REQUIRED"
		}
	}
	return ""
}

func readProblem(err error) string {
	switch errorCode(err) {
	case "GATEWAY_AUTH_EXPIRED":
		return "学校网络登录已过期。"
	case "AUTH_REQUIRED", "AUTH_EXPIRED":
		return "学校要求重新确认身份。"
	case "AGREEMENT_REQUIRED":
		return "学校要求先阅读并接受本版服务协议。"
	case "AGENT_NOT_RUNNING":
		return "本地控制器尚未运行。"
	}
	var detail *domain.Error
	if errors.As(err, &detail) {
		return detail.Message
	}
	return err.Error()
}

// A local socket that is absent or refusing connections supports setup. Other
// connection failures support reconnecting to the configured controller only.
func (w *workspace) controllerProblem(err error) string {
	code := errorCode(err)
	profile, e := w.Config.SelectedProfile()
	if e != nil {
		return readProblem(e)
	}
	remote := profile.Controller != "" && profile.Controller != "local"
	if !connectionError(code) && !(remote && remoteTransportError(code)) {
		return readProblem(err)
	}
	if remote {
		code = "REMOTE_DISCONNECTED"
	}
	if w.connection.code != code || remote {
		w.connection.code = code
		w.connection.pending = false
		w.connection.action = ""
		w.connection.detail = readProblem(err)
		if remote {
			w.connection.detail = "远程控制器连接失败。" + readProblem(err)
		} else if code == "AGENT_NOT_RUNNING" && !w.Cached && w.StartWorkspace != nil {
			w.connection.action, e = install.InspectWorkspace(w.Config)
			if e != nil {
				w.connection.detail = readProblem(e)
			} else if w.connection.action == install.WorkspaceInitialize {
				w.connection.detail = "首次使用需要在这台电脑启动个人控制器。"
			} else if w.connection.action == install.WorkspaceResume {
				w.connection.detail = "此工作区的本机控制器已停止。"
			}
		}
	}
	return w.connection.detail
}

func (w *workspace) controllerAvailable() {
	if w.connection.code != "" {
		w.connection.code, w.connection.detail, w.connection.action = "", "", ""
		w.statusErr = ""
	}
}

func (w *workspace) checkPendingSetup() {
	if w.Cached || w.StartWorkspace == nil {
		return
	}
	action := install.PendingWorkspace(w.Config)
	w.connection.pending = action != ""
	w.connection.action = action
}

// The overview of each area can report a workspace-wide issue. Local readers,
// tutorials and nested forms keep their own context and feedback.
func (w *workspace) statusPage(p *page) bool {
	return p == w.pages[0] || len(w.pages) > 1 && p == w.pages[1] && p.area != ""
}

func (w *workspace) recoveryContent(p *page, v content) content {
	if w.Cached || p.loginMenu || p.filter != "" {
		return v
	}
	statusPage := w.statusPage(p)
	statusNeedsLogin := statusPage && p.area == w.statusArea && schoolLoginCode(w.statusCode)
	var r row
	switch {
	case w.connection.pending && statusPage:
		r = link("workspace-start", "完成个人工作区初始化", "完成本机设备登记后继续", "", w.startWorkspace)
		r.verb = "继续"
	case w.connection.code != "" && (statusPage || connectionError(p.problemCode) || p.queryMethod != "" && p.problem != ""):
		switch w.connection.action {
		case install.WorkspaceInitialize:
			r = link("workspace-start", "初始化个人工作区", "创建本机身份并启动个人控制器", "bupt init", w.startWorkspace)
			r.verb = "初始化"
		case install.WorkspaceResume:
			r = link("workspace-start", "启动本机控制器", "使用已安装的个人工作区", "", w.startWorkspace)
			r.verb = "启动"
		case install.WorkspaceProcess, install.WorkspaceResumeProcess:
			r = link("workspace-start", "作为本机进程启动", "系统重启后可在这里再次启动", "", w.startWorkspace)
			r.verb = "启动"
		default:
			r = link("reconnect", "重新连接控制器", "检查当前工作区的连接", "bupt agent status", w.reconnectController)
			r.verb = "重连"
		}
	case p.blockedByLogin || statusNeedsLogin || (statusPage || p.queryMethod == "portal.read" || p.needsSchool) && w.needsLogin():
		r = navLink("login", "登录学校账号", "完成学校认证后继续当前任务", "bupt login", w.loginMethods)
	case !p.agreementDocument && (p.problemCode == "AGREEMENT_REQUIRED" || (p.queryMethod == "portal.read" || statusPage && (p == w.pages[0] || p.area == "api" || p.area == "access")) && w.bar.snapshot.Effective(time.Now()).ConsentRequired):
		r = navLink("agreement-required", "阅读学校服务协议", "阅读正文后可确认本版本", "bupt agreement", func() { w.reviewRequiredAgreement(p) })
	case p.problem != "" && p.queryMethod != "" && p.load != nil:
		r = link("retry", "重新读取", "重试此页的查询", p.command, func() { w.fetchPage(p, true) })
		r.verb = "重试"
	default:
		return v
	}
	r.recovery = true
	if p.problem != "" && p.readNavigation == nil {
		v = content{}
	}
	recovery := []row{r}
	if r.key == "reconnect" && w.connection.detail != "" {
		detail := w.connection.detail
		more := navLink("controller-diagnostic", "查看连接原因", "", "", func() { w.textPage("控制器连接", "", detail) })
		more.recovery = true
		recovery = append(recovery, more)
	}
	v.rows = append(recovery, v.rows...)
	if !p.interacted && !w.filtering && w.input == nil && w.composer == nil {
		p.key = r.key
	}
	return v
}

func (w *workspace) reconnectController() {
	w.startAccountWatch()
	w.refreshStatus(false)
}

func (w *workspace) startWorkspace() {
	action := w.connection.action
	if w.Cached || w.StartWorkspace == nil || action == "" || w.connection.starting {
		return
	}
	w.connection.starting = true
	w.notice = "正在启动本机控制器…"
	go func() {
		ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
		defer cancel()
		err := w.StartWorkspace(ctx, string(action))
		w.post(w.ctx, func() {
			w.connection.starting = false
			if errorCode(err) == "USER_SERVICE_UNAVAILABLE" && w.connection.code == "AGENT_NOT_RUNNING" && action == install.WorkspaceInitialize {
				w.connection.action = install.WorkspaceProcess
				w.connection.detail = "系统没有可用的用户服务管理器，可选择用本机进程运行。"
				w.statusErr, w.notice = w.connection.detail, ""
				return
			}
			if err != nil {
				w.notice = readProblem(err)
				// A concurrent window may already have installed the workspace.
				w.connection.code = ""
			} else {
				w.controllerAvailable()
				w.checkPendingSetup()
				w.notice = "个人工作区已就绪"
			}
			w.reconnectController()
		})
	}()
}
