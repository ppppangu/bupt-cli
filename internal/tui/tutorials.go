package tui

import (
	"bupt.local/cli/internal/navigation"
)

func (w *workspace) helpHome() {
	w.local(navigation.HelpTitle, "bupt help tutorial", func() content {
		v := content{facts: []string{"作者可协助排查。", "账号、计费、审批和权限由学校管理部门处理。"}, rows: []row{
			navLink("feedback-topics", "反馈问题", "按问题类型生成邮件", "bupt feedback --template", w.feedbackTopics),
			navLink("usage-help", "使用帮助", "快捷键、模型 API 和裸算力指南", "bupt help tutorial", w.usageHelp),
			navLink("diagnostics", "运行诊断", "检查程序与控制器并查看脱敏报告", "bupt doctor --report", func() { w.diagnosticPage(nil, nil) }),
			navLink("author", "联系作者", "查看并复制作者邮箱", "bupt feedback", w.authorContact),
		}}
		v.rows = append(v.rows, navLink("access", navigation.AccountTitle, "学校登录、顶栏、公告和控制器设置", "bupt status", func() { a, _ := navigation.Find("access"); w.openArea(a) }))
		return v
	})
}

func (w *workspace) usageHelp() {
	w.local("使用帮助", "bupt help tutorial", func() content {
		v := content{}
		for _, l := range navigation.Lessons {
			if l.ID == "keys" || l.ID == "api" || l.ID == "compute" {
				v.rows = append(v.rows, w.lessonLink(l, true))
			}
		}
		return v
	})
}
func (w *workspace) tutorials() {
	w.local("完整教程", "bupt help tutorial", func() content {
		v := content{}
		for _, l := range navigation.Lessons {
			v.rows = append(v.rows, w.lessonLink(l, false))
		}
		v.rows = append(v.rows, navLink("reference", "CLI 命令参考", "按命令浏览用法和参数", "bupt help --all", w.commandReference))
		return v
	})
}
func (w *workspace) lessonLink(l navigation.Lesson, offerTutorials bool) row {
	cmd := "bupt help tutorial " + l.ID
	return navLink(l.ID, l.Title, "", cmd, func() {
		w.local(l.Title, cmd, func() content {
			lesson := w.textContent(l.Text)
			switch l.ID {
			case "start":
				lesson.rows = append(lesson.rows, navLink("home", "回到首页开始设置", "查看此设备当前需要的步骤", "bupt tui", func() {
					for len(w.pages) > 1 {
						w.back()
					}
				}))
			case "api":
				lesson.rows = append(lesson.rows, navLink("try", "打开在线调试", "选择用途，输入自己的问题", "bupt api model", func() { w.chooseTrialModel(); w.current().area = "api" }))
			case "compute":
				title := "开始申请实例"
				if w.Cached {
					title = "浏览已保存的模板"
				}
				lesson.rows = append(lesson.rows, navLink("templates", title, "选择机器规格", "bupt compute catalog", func() { w.chooseTemplate(); w.current().area = "compute" }))
			case "keys":
				lesson.rows = append(lesson.rows, navLink("reference", "CLI 命令参考", "按命令浏览用法和参数", "bupt help --all", w.commandReference))
			}
			if offerTutorials {
				lesson.rows = append(lesson.rows, navLink("tutorials", "查看完整教程", "首次使用、算力申请和常见问题", "bupt help tutorial", w.tutorials))
			}
			return lesson
		})
	})
}

func (w *workspace) commandReference() {
	w.textPage("CLI 命令参考", "bupt help --all", "bupt help COMMAND 查看指定命令\nbupt help --all 查看完整命令树\nbupt help tutorial 查看教程")
}

func (w *workspace) explainCommand(command string) {
	w.textPage("CLI 命令参考", command, command+"\n\n在终端运行 bupt help COMMAND 查看参数和选项。")
}
