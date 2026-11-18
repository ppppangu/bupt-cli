package tui

import (
	"slices"
	"strings"

	"bupt.local/cli/internal/navigation"
)

func (w *workspace) tutorials() {
	w.local("教程说明", "bupt help tutorial", func() content {
		v := content{}
		for _, l := range navigation.Lessons {
			cmd := "bupt help tutorial " + l.ID
			v.rows = append(v.rows, link(l.ID, l.Title, "", cmd, func() { w.textPage(l.Title, cmd, l.Text) }))
		}
		v.rows = append(v.rows, link("reference", "CLI 命令参考", "按命令浏览用法和参数", "bupt help --all", w.commandReference))
		return v
	})
}

func (w *workspace) commandReference() {
	w.local("CLI 命令参考", "bupt help --all", func() content {
		v := content{rows: []row{link("all", "完整命令树和全局选项", "", "bupt help --all", func() { w.explainCommand("bupt help --all") })}}
		for _, k := range orderedKeys(w.Commands) {
			f := w.Commands[k]
			v.rows = append(v.rows, link(f.Title, f.Title, f.Summary, "bupt help "+strings.Join(f.Path, " ")+" --all", func() { w.referenceGroup(f) }))
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
		v := content{rows: []row{link("usage", "用法和选项", f.Summary, cmd, usage)}}
		for _, sub := range f.Children {
			v.rows = append(v.rows, link(sub.Title, sub.Title, sub.Summary, "bupt help "+strings.Join(sub.Path, " ")+" --all", func() { w.referenceGroup(sub) }))
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
