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

// cutCells splits without truncating a combining sequence or a wide rune.
func cutCells(text string, width int) (string, string) {
	g := uniseg.NewGraphemes(text)
	used, stop := 0, 0
	for g.Next() {
		if used+g.Width() > width {
			break
		}
		used += g.Width()
		_, stop = g.Positions()
	}
	return text[:stop], text[stop:]
}

// Read-only values never consume an individual keystroke. When a small
// terminal clips them, one reader offers their full unabridged values.
func (w *workspace) completeFields(p *page, v content) content {
	if v.document || len(v.groups) > 0 || p.overlay || p.bench != "" {
		return v
	}
	cols, height := w.screen.Size()
	margin := 2
	if cols < 50 {
		margin = 1
	}
	width := cols - 2*margin
	listWidth := width
	if cols >= 96 && v.split {
		listWidth = width * 3 / 5
	}
	label := min(28, max(12, listWidth*2/5))
	clipped := len(v.facts) > max(1, height-10)
	for _, s := range v.facts {
		if uniseg.StringWidth(s) > width {
			clipped = true
			break
		}
	}
	for _, r := range v.rows {
		if clipped {
			break
		}
		if r.open != nil {
			continue
		}
		if v.fullWidth {
			clipped = uniseg.StringWidth(r.title) > listWidth-2
			continue
		}
		size := label
		if listWidth < 54 {
			size = min(listWidth, max(label, 2+uniseg.StringWidth(r.title)))
		}
		clipped = uniseg.StringWidth(r.title) > size-2 || (r.detail != "" && uniseg.StringWidth(r.detail) > listWidth-size-1)
	}
	if !clipped {
		return v
	}
	title := p.title + " · 完整信息"
	fields, facts := v.rows, v.facts
	if actionFrom(fields, 0, 1) < 0 {
		v.document = true
	}
	v.rows = append(v.rows, navLink("view/full-fields", "查看完整信息", "阅读此页的完整字段", "", func() {
		lines := append([]string(nil), facts...)
		for _, r := range fields {
			if r.open != nil {
				continue
			}
			lines = append(lines, strings.TrimSpace(r.title+"  "+r.detail))
		}
		w.textPage(title, "", strings.Join(lines, "\n"))
	}))
	return v
}

func (w *workspace) drawHeader(p *page, margin, width int, c palette) {
	trail := []string{}
	for i, frame := range w.pages {
		if !frame.overlay {
			label := frame.title
			if i == 0 && frame != p {
				label = "校园算力"
			}
			if len(trail) == 0 || trail[len(trail)-1] != label {
				trail = append(trail, label)
			}
		}
		if frame == p {
			break
		}
	}
	title := p.title
	if len(trail) > 1 {
		title = "‹ " + title
	}
	profile := w.Config.Selected
	if cfg, err := w.Config.SelectedProfile(); err == nil {
		if cfg.Controller == "" || cfg.Controller == "local" {
			profile += " · 本机控制器"
		} else {
			profile += " · 远程控制器"
		}
	}
	budget := width
	if width >= 64 && uniseg.StringWidth(title+"   "+profile) <= width {
		n := uniseg.StringWidth(profile)
		put(w.screen, margin+width-n, 0, n, profile, c.muted)
		budget -= n + 3
	}
	for i := 0; i < len(trail)-1; i++ {
		crumb := "‹ " + strings.Join(trail[i:], " › ")
		if uniseg.StringWidth(crumb) <= budget {
			title = crumb
			break
		}
	}
	if uniseg.StringWidth(title) > budget {
		first, rest := cutCells(title, budget)
		put(w.screen, margin, 0, budget, first, c.title)
		put(w.screen, margin, 1, width, rest, c.title)
	} else {
		put(w.screen, margin, 0, budget, title, c.title)
		bar(w.screen, 1, width+2*margin, c.muted, '─')
	}
	status := w.bar.snapshot.Effective(time.Now())
	put(w.screen, margin, 2, width, accountLine(status, w.bar.prefs, width), c.muted)
}

func (w *workspace) draw() {
	s := w.screen
	cols, height := s.Size()
	p := w.current()
	c := colors(w.NoColor)
	s.Clear()
	s.HideCursor()
	w.redrawAt = time.Time{}
	w.listHeight = 0
	if cols < 28 || height < 10 {
		put(s, 0, 0, cols, "请放大终端，q 退出", c.title)
		s.Show()
		return
	}
	front := p
	for i := len(w.pages) - 1; i > 0 && p.overlay; i-- {
		p = w.pages[i-1]
	}
	if p == front && p.editor != nil && (p.bench == "" || height < 14) {
		w.drawTrialEditor(cols, height, c)
		return
	}
	margin := 2
	if cols < 50 {
		margin = 1
	}
	width := cols - 2*margin
	w.drawHeader(p, margin, width, c)
	v := w.view(p)
	w.redrawAt = v.redrawAt
	y := 4
	bottom := height - 3
	if height <= 15 {
		y, bottom = 3, height-2
	}
	status := p.problem
	if status == "" && w.statusPage(p) {
		status = w.statusErr
		if status == "" && w.hasReport {
			status = strings.Join(w.report.Warnings, " · ")
		}
	}
	if status != "" {
		put(s, margin, y, width, status, c.warning)
		y++
	}
	for _, line := range v.facts {
		if y >= bottom-2 {
			break
		}
		put(s, margin, y, width, line, c.base)
		y++
	}
	if len(v.facts) > 0 && bottom-y > 7 {
		y++
	}
	panelWide := len(v.panel) > 0 && cols >= 100
	if !panelWide && len(v.panel) > 0 && bottom-y >= 5 {
		put(s, margin, y, width, v.panelTitle, c.accent)
		y++
		for _, line := range panelPreview(v.panel, width, min(3, bottom-y-3)) {
			put(s, margin, y, width, line, c.base)
			y++
		}
		if v.panelFooter != "" && y < bottom-3 {
			put(s, margin, y, width, v.panelFooter, c.muted)
			y++
		}
	}
	if p.filter != "" || w.filtering && p == front {
		if w.filtering {
			w.drawInputLine(margin, y, width, "本页筛选  /", []rune(p.filter), p.filterPos, c.accent)
		} else {
			put(s, margin, y, width, "本页筛选  /"+p.filter, c.accent)
		}
		y++
	}
	w.listTop, w.listLeft, w.listHeight = y, margin, max(0, bottom-y)
	active := p.selected(v)
	if p == front && p.onFocus != nil && active != nil && !w.filtering && !p.loading && p.problem == "" {
		p.onFocus(active.key)
	}
	if active != nil && w.listHeight > 0 {
		p.scroll = min(p.scroll, p.focus)
		p.scroll = max(p.scroll, p.focus-w.listHeight+1)
	}
	p.scroll = max(0, min(p.scroll, len(v.rows)-w.listHeight))
	w.firstRow = p.scroll
	listWidth := width
	wide := panelWide || cols >= 96 && v.split && active != nil && len(active.preview) > 0
	if wide {
		listWidth = width * 3 / 5
		if panelWide {
			listWidth = width / 2
		}
	}
	w.listRight = margin + listWidth
	labelWidth := min(28, max(12, listWidth*2/5))
	selectionHint := ""
	for i := p.scroll; i < len(v.rows) && i < p.scroll+w.listHeight; i++ {
		r := &v.rows[i]
		style, detailStyle, mark := c.base, c.muted, "  "
		selected := r == active
		if selected {
			style, detailStyle, mark = c.selected, c.selected, "› "
			for x := margin; x < w.listRight; x++ {
				s.SetContent(x, y, ' ', nil, c.selected)
			}
		}
		name := mark + r.title
		if (v.document || r.prose) && r.open == nil {
			name = r.title
		}
		if v.fullWidth || r.prose {
			put(s, margin, y, listWidth, name, style)
		} else {
			label := labelWidth
			// Narrow terminals retain the object name. Secondary detail uses the
			// remaining space; the selected row can use the hint line as well.
			if listWidth < 54 {
				label = min(listWidth, max(label, uniseg.StringWidth(name)))
			}
			put(s, margin, y, label, name, style)
			room := listWidth - label - 1
			if room > 0 {
				put(s, margin+label+1, y, room, r.detail, detailStyle)
			}
			if selected && uniseg.StringWidth(name) > label {
				selectionHint = strings.TrimSpace(r.title)
				if uniseg.StringWidth(selectionHint) > width {
					_, remaining := cutCells(name, max(1, label-1))
					selectionHint = "…" + remaining
				}
			} else if selected && uniseg.StringWidth(r.detail) > room {
				selectionHint = r.detail
			}
		}
		y++
	}
	if wide {
		left := w.listRight + 2
		room := cols - margin - left
		for y := w.listTop; y < w.listTop+w.listHeight; y++ {
			s.SetContent(w.listRight, y, '│', nil, c.muted)
		}
		if panelWide {
			put(s, left, w.listTop, room, v.panelTitle, c.accent)
			rows := w.listHeight - 1
			if v.panelFooter != "" && rows > 2 {
				put(s, left, w.listTop+rows, room, v.panelFooter, c.muted)
				rows--
			}
			for i, line := range panelPreview(v.panel, room, rows) {
				put(s, left, w.listTop+1+i, room, line, c.base)
			}
		} else {
			put(s, left, w.listTop, room, "选中项摘要", c.muted)
			if w.listHeight >= 2 {
				put(s, left, w.listTop+1, room, strings.TrimSpace(active.title), c.title)
			}
			for i, line := range active.preview {
				if i+2 >= w.listHeight {
					break
				}
				put(s, left, w.listTop+2+i, room, line, c.base)
			}
		}
	}
	if len(v.rows) == 0 && w.listHeight > 0 {
		summary := v.empty
		switch {
		case p.loading || !w.hasReport && p.build != nil && w.refreshing:
			summary = "正在读取…"
		case p.problem != "":
			summary = "按 r 重试，Esc 返回"
		case summary == "":
			summary = "暂无记录"
		}
		put(s, margin, w.listTop, width, summary, c.muted)
	}
	if bottom == height-3 {
		bar(s, bottom, cols, c.muted, '─')
	}
	hint, hintStyle := selectionHint, c.muted
	switch {
	case p.submitting:
		hint, hintStyle = "正在提交…", c.accent
	case p.loading:
		hint, hintStyle = "正在读取…", c.accent
	case w.notice != "":
		hint, hintStyle = w.notice, c.warning
	case hint == "" && len(v.rows) > w.listHeight:
		hint = fmt.Sprintf("显示 %d–%d / %d", p.scroll+1, min(p.scroll+w.listHeight, len(v.rows)), len(v.rows))
	}
	if front != p {
		w.drawOverlay(front, cols, height, c)
		return
	}
	if p.editor != nil {
		w.drawTrialEditor(cols, height, c)
		return
	}
	if w.input == nil {
		put(s, margin, height-2, width, hint, hintStyle)
		put(s, margin, height-1, width, w.keyHints(p, v, active, width), c.base)
	} else {
		w.drawPrompt(margin, width, height, c)
	}
	s.Show()
}

func (w *workspace) keyHints(p *page, v content, r *row, width int) string {
	if w.filtering {
		return "Enter 应用  Esc 取消  ←→ 移动  Ctrl-U 清空"
	}
	primary := "↑↓ 阅读"
	switch {
	case r != nil:
		verb := r.verb
		if verb == "" {
			verb = "选择"
			if r.forward {
				verb = "打开"
			}
		}
		if r.forward {
			primary = "↑↓ 选择  Enter/→ " + verb
		} else {
			primary = "↑↓ 选择  Enter " + verb
		}
	case p.copyText != "":
		primary = "↑↓ 阅读  Enter/c 复制全文  s 另存"
	case len(v.rows) == 0:
		primary = "/ 本页筛选"
		if p.filter == "" {
			primary = "r 刷新"
		}
	}
	back := "Esc 返回"
	if p.filter != "" && len(w.pages) == 1 {
		back = "Esc 清除筛选"
	}
	if width < 45 {
		primary = strings.TrimPrefix(primary, "↑↓ 选择  ")
		primary = strings.Replace(primary, "Enter/→ ", "Enter ", 1)
		if p.filter != "" {
			return fit(primary, width-uniseg.StringWidth(back)-5) + "  " + back + "  ?"
		}
		if r == nil && p.copyText != "" {
			return "Enter 复制  s 另存  Esc  ?"
		}
		return fit(primary, width-10) + "  Esc  ?  q"
	}
	suffix := "  " + back + "  ? 帮助"
	if len(w.pages) == 1 && p.filter == "" {
		suffix = "  ? 帮助"
	}
	if uniseg.StringWidth(primary+suffix) > width {
		primary = strings.TrimPrefix(primary, "↑↓ 选择  ")
	}
	hints := fit(primary, width-uniseg.StringWidth(suffix)) + suffix
	options := []string{"/ 本页筛选", "r 刷新", "q 退出"}
	if p.filter != "" {
		options = append([]string{"退格 清除筛选"}, options...)
	}
	if p.bench != "" {
		options = append([]string{"Tab 换项"}, options...)
	}
	if p.shortcuts['n'] != "" {
		options = append([]string{"n 新建"}, options...)
	}
	if r != nil && r.key == r.group && r.verb == "收起" {
		options = append([]string{"→ 进入", "← 收起"}, options...)
	} else if r != nil && r.key == r.group {
		options = append([]string{"→ 展开"}, options...)
	}
	for _, opt := range options {
		if primary != opt && uniseg.StringWidth(hints+"  "+opt) <= width {
			hints += "  " + opt
		}
	}
	return hints
}

func (w *workspace) drawPrompt(margin, width, height int, c palette) {
	s := w.screen
	cols, _ := s.Size()
	p := w.input
	for y := height - 4; y < height; y++ {
		bar(s, y, cols, c.base, ' ')
	}
	put(s, margin, height-4, width, p.title, c.accent)
	w.drawInputLine(margin, height-3, width, "› ", p.value, p.pos, c.title)
	put(s, margin, height-2, width, p.problem, c.warning)
	put(s, margin, height-1, width, "←→ 移动  Enter 保存  Esc 取消", c.muted)
}

// Draw a suffix of the prefix so the insertion point stays inside this terminal.
func (w *workspace) drawInputLine(x, y, width int, label string, value []rune, pos int, style tcell.Style) {
	prefixWidth := uniseg.StringWidth(label)
	room := max(1, width-prefixWidth)
	pos = max(0, min(pos, len(value)))
	prefix := string(value[:pos])
	used := uniseg.StringWidth(prefix)
	firstByte := 0
	g := uniseg.NewGraphemes(prefix)
	for used >= room && g.Next() {
		used -= g.Width()
		_, firstByte = g.Positions()
	}
	put(w.screen, x, y, width, label+prefix[firstByte:]+string(value[pos:]), style)
	w.screen.ShowCursor(x+prefixWidth+used, y)
}

// Wrap only the visible part of a potentially long response.
func panelPreview(lines []string, width, limit int) []string {
	var out []string
	for _, line := range lines {
		for len(out) < limit {
			part, rest := cutCells(line, max(2, width))
			out = append(out, part)
			if rest == "" {
				break
			}
			line = rest
		}
		if len(out) >= limit {
			break
		}
	}
	return out
}

func (w *workspace) drawOverlay(p *page, cols, height int, c palette) {
	s := w.screen
	v := w.view(p)
	width := min(cols-2, 76)
	h := min(height-2, max(7, len(v.facts)+len(v.rows)+5))
	left, top := (cols-width)/2, (height-h)/2
	for y := top; y < top+h; y++ {
		for x := left; x < left+width; x++ {
			s.SetContent(x, y, ' ', nil, c.base)
		}
		s.SetContent(left, y, '│', nil, c.muted)
		s.SetContent(left+width-1, y, '│', nil, c.muted)
	}
	for x := left; x < left+width; x++ {
		s.SetContent(x, top, '─', nil, c.muted)
		s.SetContent(x, top+h-1, '─', nil, c.muted)
	}
	x, room, y := left+2, width-4, top+1
	put(s, x, y, room, p.title, c.title)
	y++
	if w.filtering || p.filter != "" {
		w.drawInputLine(x, y, room, "/ ", []rune(p.filter), p.filterPos, c.accent)
		if !w.filtering {
			s.HideCursor()
		}
		y++
	}
	for _, line := range v.facts {
		if y >= top+h-4 {
			break
		}
		put(s, x, y, room, line, c.muted)
		y++
	}
	w.listLeft, w.listRight, w.listTop, w.listHeight = x, x+room, y, max(1, top+h-2-y)
	active := p.selected(v)
	if active != nil {
		p.scroll = max(p.focus-w.listHeight+1, min(p.scroll, p.focus))
	}
	p.scroll = max(0, min(p.scroll, len(v.rows)-w.listHeight))
	w.firstRow = p.scroll
	for i := p.scroll; i < len(v.rows) && i < p.scroll+w.listHeight; i++ {
		r := &v.rows[i]
		style := c.base
		mark := "  "
		if r == active {
			style = c.selected
			mark = "› "
			for dx := x; dx < w.listRight; dx++ {
				s.SetContent(dx, y, ' ', nil, style)
			}
		}
		put(s, x, y, room, mark+strings.TrimSpace(r.title+"  "+r.detail), style)
		y++
	}
	if len(v.rows) == 0 {
		msg := v.empty
		if p.loading {
			msg = "正在读取…"
		} else if p.problem != "" {
			msg = p.problem
		}
		put(s, x, w.listTop, room, msg, c.muted)
	}
	hint := "↑↓ 选择  Enter 应用  Esc 取消  / 筛选"
	if p.nextKey == "preview" {
		hint = "Space 勾选  F2 核对  Esc 取消"
	}
	if w.filtering {
		hint = "Enter 应用选中项  Esc 取消"
		if p.nextKey != "" {
			hint = "Enter 完成筛选  Esc 取消"
		}
	} else if w.notice != "" {
		hint = w.notice
	}
	put(s, x, top+h-2, room, hint, c.accent)
	if w.input != nil {
		w.drawPrompt(2, cols-4, height, c)
	}
	s.Show()
}
