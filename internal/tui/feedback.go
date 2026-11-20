package tui

import (
	"context"
	"strings"

	"bupt.local/cli/internal/feedback"
)

func (w *workspace) feedbackTopics() {
	w.local("反馈问题", "bupt feedback", func() content {
		v := content{}
		for _, category := range feedback.Categories() {
			v.rows = append(v.rows, navLink(category.ID, category.Label, "", "", func() {
				w.feedbackDraft(category, feedback.Incident{})
			}))
		}
		return v
	})
}

func (w *workspace) feedbackAction(issue feedback.Incident) row {
	r := navLink("feedback-error", "反馈此问题", "预览带当前错误和回执的邮件", "bupt feedback --template", func() {
		w.feedbackDraft(issue.Category(), issue)
	})
	r.recovery = true
	return r
}

func (w *workspace) copyFeedback(text, message string) {
	w.screen.SetClipboard([]byte(text))
	w.notice = message
}

func (w *workspace) authorContact() {
	w.local("联系作者", "bupt feedback", func() content {
		return content{
			facts: []string{"作者邮箱  " + feedback.Email, feedback.SchoolSupport},
			rows: []row{
				link("copy-email", "复制作者邮箱", feedback.Email, "", func() {
					w.copyFeedback(feedback.Email, "已向终端请求复制作者邮箱")
				}),
				navLink("new-feedback", "生成反馈邮件", "", "bupt feedback --template", w.feedbackTopics),
			},
		}
	})
}

func (w *workspace) feedbackDraft(category feedback.Category, issue feedback.Incident) {
	draft := feedback.Draft{Category: category, Incident: issue, Version: w.Version}
	w.local("反馈邮件预览", "bupt feedback --template", func() content {
		body := draft.Text()
		v := w.textContent(body + "\n" + feedback.Review)
		actions := []row{
			link("copy-mail", "复制邮件内容", "粘贴到邮件客户端，补充问题描述后发送", "", func() {
				w.copyFeedback(body, "已向终端请求复制邮件内容，请粘贴到邮件客户端发送。")
			}),
			link("copy-email", "复制作者邮箱", feedback.Email, "", func() {
				w.copyFeedback(feedback.Email, "已向终端请求复制作者邮箱")
			}),
		}
		attachment := navLink("diagnostics", "附加脱敏诊断", "先查看报告，再决定是否附加", "bupt doctor --report", func() {
			w.diagnosticPage(nil, func(r feedback.Report) {
				draft.Report = &r
				w.back()
				w.notice = "已附加脱敏诊断"
			})
		})
		if draft.Report != nil {
			attachment = link("diagnostics", "移除诊断附件", "", "", func() {
				draft.Report = nil
				w.notice = "已移除诊断附件"
			})
		}
		actions = append(actions, attachment, navLink("back", "返回修改", "回到原页面", "", w.back))
		v.rows = append(actions, v.rows...)
		return v
	})
}

func (w *workspace) pageIncident(p *page, v content) feedback.Incident {
	if v.incident.Code != "" {
		return v.incident
	}
	if p.incident.Code != "" {
		return p.incident
	}
	if w.statusPage(p) && p.area == w.statusArea {
		return w.statusIncident
	}
	return feedback.Incident{}
}

func (w *workspace) diagnosticPage(ssh *TerminalAction, attach func(feedback.Report)) {
	p := &page{title: "脱敏诊断", command: "bupt doctor --report"}
	backend := w.Backend
	version, cached := w.Version, w.Cached
	p.load = func(ctx context.Context) (func() content, error) {
		var check func(context.Context) error
		if backend != nil {
			check = func(ctx context.Context) error { return feedback.CheckController(ctx, backend.Call) }
		}
		r := feedback.Collect(ctx, version, nil, check)
		var sshState string
		if ssh != nil && len(ssh.Args) > 0 && r.Controller == "reachable" && !cached {
			var detail struct {
				Checks []struct{ Check, State string }
			}
			err := backend.Call(ctx, "doctor", map[string]any{"target": ssh.Args[0], "node": ssh.Options["node"]}, &detail)
			sshState = "SSH 路由：待核查"
			if err != nil {
				sshState += " · " + feedback.FromError("", err).Code
			} else {
				for _, item := range detail.Checks {
					if item.Check != "ssh" {
						continue
					}
					switch item.State {
					case "reachable":
						sshState = "SSH 路由：可连接"
					case "unreachable":
						sshState = "SSH 路由：无法连接"
					case "not_prepared":
						sshState = "SSH 路由：尚未准备"
					}
				}
			}
		}
		return func() content {
			v := w.textContent(strings.TrimSpace(r.Text()))
			if sshState != "" {
				v.facts = []string{sshState}
			}
			actions := []row{link("copy-report", "复制脱敏诊断报告", "只复制预览中的诊断摘要", "", func() {
				w.copyFeedback(r.Text(), "已向终端请求复制脱敏诊断")
			})}
			if attach != nil {
				actions = append([]row{link("attach-report", "附加到邮件", "返回邮件预览", "", func() { attach(r) })}, actions...)
			}
			v.rows = append(actions, v.rows...)
			return v
		}, nil
	}
	w.push(p)
}

func (w *workspace) sshFailure(action TerminalAction, issue feedback.Incident) {
	reason := "无法建立 SSH 连接，或连接已中断。"
	switch issue.Code {
	case "SSH_NOT_PREPARED", "SSH_NOT_READY":
		reason = "实例的 SSH 路由尚未就绪。"
	case "SSH_HOST_KEY_MISMATCH":
		reason = "实例的主机密钥与已核验记录不同，请先检查连接状态。"
	case "AGENT_NOT_RUNNING", "CONTROLLER_CONNECT", "CONTROLLER_UNAVAILABLE":
		reason = "当前控制器无法连接。"
	}
	facts := []string{"实例  " + strings.Join(action.Args, " "), "原因  " + reason, "错误码  " + issue.Code}
	w.local("SSH 连接失败", "bupt ssh INSTANCE", func() content {
		return content{facts: facts, rows: []row{
			link("retry-ssh", "重试连接", "", "", func() { w.back(); w.foreground(action) }),
			navLink("check-ssh", "检查连接状态", "", "bupt doctor ssh", func() { w.diagnosticPage(&action, nil) }),
			w.feedbackAction(issue),
			navLink("back", "返回原页面", "", "", w.back),
		}}
	})
}
