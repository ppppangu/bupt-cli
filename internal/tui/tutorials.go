package tui

import (
	"slices"
	"strings"

	"bupt.local/cli/internal/navigation"
)

func (w *workspace) helpHome() {
	w.local(navigation.HelpTitle, "bupt help tutorial", func() content {
		v := content{}
		for _, l := range navigation.Lessons {
			if l.ID == "keys" || l.ID == "api" {
				v.rows = append(v.rows, w.lessonLink(l, true))
			}
		}
		v.rows = append(v.rows, navLink("access", navigation.AccountTitle, "学校登录、顶栏、公告和控制器设置", "bupt status", func() { a, _ := navigation.Find("access"); w.openArea(a) }))
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
	w.local("CLI 命令参考", "bupt help --all", func() content {
		v := content{rows: []row{navLink("all", "完整命令树和全局选项", "", "bupt help --all", func() { w.explainCommand("bupt help --all") })}}
		for _, k := range orderedKeys(w.Commands) {
			f := w.Commands[k]
			v.rows = append(v.rows, navLink(f.Title, f.Title, f.Summary, "bupt help "+strings.Join(f.Path, " ")+" --all", func() { w.referenceGroup(f) }))
		}
		return v
	})
}
func (w *workspace) referenceGroup(f CommandForm) {
	name := strings.Join(f.Path, " ")
	cmd := "bupt help " + name + " --all"
	usage := func() {
		if w.Explain != nil {
			w.textPage(name, cmd, w.Explain(name+" --all"))
		}
	}
	if len(f.Children) == 0 {
		usage()
		return
	}
	w.local(name, cmd, func() content {
		v := content{rows: []row{navLink("usage", "用法和选项", f.Summary, cmd, usage)}}
		for _, sub := range f.Children {
			v.rows = append(v.rows, navLink(sub.Title, sub.Title, sub.Summary, "bupt help "+strings.Join(sub.Path, " ")+" --all", func() { w.referenceGroup(sub) }))
		}
		return v
	})
}

// Explain the longest known command prefix. Arguments and shell-quoted input
// are displayed as examples and never passed to the CLI help interpreter.
func (w *workspace) explainCommand(command string) {
	args := strings.Fields(strings.TrimPrefix(command, "bupt "))
	topic := "--all"
	if len(args) > 0 {
		if args[0] == "help" {
			topic = strings.Join(args[1:], " ")
		} else if f, ok := w.Commands[args[0]]; ok {
			for _, part := range args[1:] {
				i := slices.IndexFunc(f.Children, func(sub CommandForm) bool { return sub.Title == part })
				if i < 0 {
					break
				}
				f = f.Children[i]
			}
			topic = strings.Join(f.Path, " ") + " --all"
		}
	}
	text := command
	if w.Explain != nil {
		text += "\n\n" + w.Explain(topic)
	}
	w.textPage("CLI 用法", command, text)
}
