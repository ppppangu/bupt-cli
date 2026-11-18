package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

type palette struct{ base, title, muted, accent, selected, warning tcell.Style }

func colors(plain bool) palette {
	s := tcell.StyleDefault
	if plain {
		return palette{s, s.Bold(true), s, s.Bold(true), s.Reverse(true), s.Bold(true)}
	}
	return palette{s, s.Bold(true), s.Foreground(tcell.ColorSlateGray), s.Foreground(tcell.ColorLightSeaGreen).Bold(true), s.Background(tcell.ColorDarkCyan).Foreground(tcell.ColorWhite).Bold(true), s.Foreground(tcell.ColorOrange)}
}
func fit(text string, width int) string {
	if width <= 0 {
		return ""
	}
	text = clean(text)
	if uniseg.StringWidth(text) <= width {
		return text
	}
	var b strings.Builder
	g := uniseg.NewGraphemes(text)
	cells := 0
	for g.Next() {
		if cells+g.Width() > width-1 {
			break
		}
		b.WriteString(g.Str())
		cells += g.Width()
	}
	return b.String() + "…"
}
func put(s tcell.Screen, x, y, width int, text string, style tcell.Style) {
	if width <= 0 {
		return
	}
	g := uniseg.NewGraphemes(fit(text, width))
	for g.Next() {
		rs := g.Runes()
		n := g.Width()
		if n > 0 && len(rs) > 0 {
			s.SetContent(x, y, rs[0], rs[1:], style)
			x += n
		}
	}
}
func bar(s tcell.Screen, y, width int, style tcell.Style, ch rune) {
	for x := 0; x < width; x++ {
		s.SetContent(x, y, ch, nil, style)
	}
}
func (w *workspace) draw() {
	s := w.screen
	cols, height := s.Size()
	p := w.current()
	c := colors(w.NoColor)
	s.Clear()
	s.HideCursor()
	if cols < 28 || height < 10 {
		put(s, 0, 0, cols, "请放大终端，q 退出", c.title)
		s.Show()
		return
	}
	if w.composer != nil {
		w.drawTrialEditor(cols, height, c)
		return
	}
	margin := 2
	if cols < 50 {
		margin = 1
	}
	width := cols - 2*margin
	path := []string{"北邮算力"}
	if len(w.pages) > 1 {
		path = append(path, "总览")
		for _, v := range w.pages[1:] {
			path = append(path, v.title)
		}
	} else {
		path = append(path, "平台总览")
	}
	crumb := strings.Join(path, "  ›  ")
	if len(w.pages) > 1 {
		crumb = "‹ " + crumb
	}
	if len(path) > 2 && uniseg.StringWidth(crumb) > width-12 {
		crumb = "‹  …  ›  " + p.title
	}
	right := w.Config.Selected
	if profile, e := w.Config.SelectedProfile(); e == nil {
		if profile.Controller == "local" || profile.Controller == "" {
			right += " · 本机控制器"
		} else {
			right += " · 远程控制器"
		}
	}
	rightWidth := uniseg.StringWidth(right)
	leftWidth := width
	if cols >= 60 {
		leftWidth -= rightWidth + 3
		put(s, cols-margin-rightWidth, 0, rightWidth, right, c.muted)
	}
	put(s, margin, 0, leftWidth, crumb, c.title)
	bar(s, 1, cols, c.muted, '─')
	put(s, margin, 2, width, accountLine(w.bar.snapshot.Effective(time.Now()), w.bar.prefs, width), c.accent)
	v := w.view(p)
	y := 4
	status := p.problem
	if status == "" {
		status = w.statusErr
	}
	if status == "" && w.hasReport && len(w.report.Warnings) > 0 {
		status = strings.Join(w.report.Warnings, " · ")
	}
	if status != "" {
		put(s, margin, y, width, status, c.warning)
		y++
	}
	for _, line := range v.facts {
		if y >= height-6 {
			break
		}
		put(s, margin, y, width, line, c.base)
		y++
	}
	if len(v.facts) > 0 && height-y > 9 {
		y++
	}
	if p.filter != "" || w.filtering {
		put(s, margin, y, width, "筛选  /"+p.filter, c.accent)
		y++
	}
	w.listTop = y
	w.listHeight = max(0, height-4-y)
	if p.focus < p.scroll {
		p.scroll = p.focus
	}
	if w.listHeight > 0 && p.focus >= p.scroll+w.listHeight {
		p.scroll = p.focus - w.listHeight + 1
	}
	p.scroll = max(0, min(p.scroll, len(v.rows)-w.listHeight))
	w.firstRow = p.scroll
	listWidth := width
	wide := cols >= 96 && len(v.rows) > 0 && (v.split || len(v.groups) > 0)
	if wide {
		listWidth = (width * 3) / 5
	}
	w.listRight = margin + listWidth
	labelWidth := min(26, max(12, listWidth/3))
	for i := p.scroll; i < len(v.rows) && i < p.scroll+w.listHeight; i++ {
		r := v.rows[i]
		style := c.base
		detail := c.muted
		mark := "  "
		if i == p.focus {
			style = c.selected
			detail = c.selected
			mark = "› "
			bar(s, y, w.listRight, style, ' ')
		}
		if v.fullWidth {
			put(s, margin, y, width, mark+r.title, style)
			y++
			continue
		}
		put(s, margin, y, labelWidth, mark+r.title, style)
		put(s, margin+labelWidth+1, y, listWidth-labelWidth-1, r.detail, detail)
		y++
	}
	if wide {
		r := v.rows[p.focus]
		left := w.listRight + 2
		remaining := cols - margin - left
		for y := w.listTop; y < w.listTop+w.listHeight; y++ {
			s.SetContent(w.listRight, y, '│', nil, c.muted)
		}
		put(s, left, w.listTop, remaining, strings.TrimSpace(r.title), c.title)
		lines := r.preview
		if len(lines) == 0 {
			lines = []string{r.detail}
		}
		for i, line := range lines {
			if i+2 >= w.listHeight {
				break
			}
			put(s, left, w.listTop+2+i, remaining, line, c.base)
		}
	}
	if len(v.rows) == 0 {
		summary := v.empty
		switch {
		case p.loading:
			summary = "正在读取…"
		case p.problem != "":
			summary = "按 r 重试，Esc 返回"
		case !w.hasReport && p.build != nil && w.refreshing:
			summary = "正在读取…"
		case summary == "":
			summary = "暂无记录"
		}
		put(s, margin, min(y, height-4), width, summary, c.muted)
	}
	bar(s, height-3, cols, c.muted, '─')
	cmd := p.command
	if len(v.rows) > 0 && v.rows[p.focus].command != "" {
		cmd = v.rows[p.focus].command
	}
	if p.loading {
		put(s, margin, height-2, width, "正在读取…", c.accent)
	} else if p.submitting {
		put(s, margin, height-2, width, "正在提交…", c.accent)
	} else if w.notice != "" {
		put(s, margin, height-2, width, w.notice, c.warning)
	} else if cmd != "" {
		put(s, margin, height-2, width, "$ "+cmd, c.muted)
	}
	footer := "↑↓ 选择   Enter 打开   Esc 返回   / 筛选   r 刷新   ? 帮助   q 退出"
	if width < 65 {
		footer = "↑↓  Enter 打开  Esc 返回  / 筛选  ?  q"
	}
	if len(v.groups) > 0 && width >= 65 {
		footer = "↑↓ 选择   → 展开   ← 收起   ? 帮助   Enter 打开   / 筛选   q 退出"
	}
	if w.filtering {
		footer = "输入筛选词   Enter 完成   Esc 清除   Ctrl-U 清空"
	}
	if width < 42 && !w.filtering {
		footer = "↑↓  Enter  Esc  ?  q"
	}
	if p.copyText != "" && !w.filtering {
		footer = "↑↓ 阅读   Enter/c 复制全文   s 另存   Esc 返回"
		if width < 50 {
			footer = "c 复制   s 另存   Esc 返回"
		}
	}
	if len(v.rows) > w.listHeight && !w.filtering {
		footer = fmt.Sprintf("%d/%d  ", p.focus+1, len(v.rows)) + footer
	}
	put(s, margin, height-1, width, footer, c.base)
	if w.input != nil {
		bar(s, height-4, cols, c.selected, ' ')
		put(s, margin, height-4, width, w.input.title, c.selected)
		bar(s, height-3, cols, c.base, ' ')
		val := string(w.input.value)
		tail := []rune(clean(val))
		for len(tail) > 0 && uniseg.StringWidth(string(tail)) > width-3 {
			tail = tail[1:]
		}
		put(s, margin, height-3, width, "› "+string(tail), c.title)
		s.ShowCursor(min(cols-1, margin+2+uniseg.StringWidth(string(tail))), height-3)
		bar(s, height-2, cols, c.base, ' ')
		put(s, margin, height-2, width, w.input.problem, c.warning)
		bar(s, height-1, cols, c.base, ' ')
		put(s, margin, height-1, width, "Enter 保存   Esc 取消   Ctrl-U 清空", c.muted)
	}
	s.Show()
}
