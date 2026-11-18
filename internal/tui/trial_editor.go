package tui

import (
	"unicode"
	"unicode/utf8"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

type trialEditor struct {
	title     string
	text      []rune
	pos, size int
	done      bool
	message   string
	save      func(string)
	send      func()
}

func (w *workspace) editTrialText(title, value string, save func(string), send func()) {
	text := []rune(value)
	w.composer = &trialEditor{title: title, text: text, pos: len(text), size: len(value), save: save, send: send}
}
func (e *trialEditor) insert(r rune) {
	n := utf8.RuneLen(r)
	if n < 1 {
		return
	}
	if e.size+n > domain.TrialTextLimit {
		e.message = "这段输入已达 16 KiB"
		return
	}
	e.text = append(e.text, 0)
	copy(e.text[e.pos+1:], e.text[e.pos:])
	e.text[e.pos] = r
	e.pos++
	e.size += n
	e.message = ""
}
func (e *trialEditor) remove(start, end int) {
	e.size -= len(string(e.text[start:end]))
	e.text = append(e.text[:start], e.text[end:]...)
	e.pos = start
}
func (e *trialEditor) lineStart(i int) int {
	for i > 0 && e.text[i-1] != '\n' {
		i--
	}
	return i
}
func (e *trialEditor) lineEnd(i int) int {
	for i < len(e.text) && e.text[i] != '\n' {
		i++
	}
	return i
}

// Lines refer to rune offsets in the draft. Both keyboard movement and
// painting use these cell-width wraps, including wide characters.
type trialLine struct{ start, end int }

func (e *trialEditor) lines(width int) []trialLine {
	var lines []trialLine
	start, pos, cells := 0, 0, 0
	g := uniseg.NewGraphemes(string(e.text))
	for g.Next() {
		s := g.Str()
		if s == "\n" {
			lines = append(lines, trialLine{start, pos})
			pos++
			start, cells = pos, 0
			continue
		}
		if cells > 0 && cells+g.Width() > width {
			lines = append(lines, trialLine{start, pos})
			start, cells = pos, 0
		}
		cells += g.Width()
		pos += utf8.RuneCountInString(s)
	}
	lines = append(lines, trialLine{start, pos})
	if cells >= width {
		lines = append(lines, trialLine{pos, pos})
	}
	return lines
}

func (e *trialEditor) caret(lines []trialLine) (x, y int) {
	for y+1 < len(lines) && e.pos >= lines[y+1].start {
		y++
	}
	return uniseg.StringWidth(string(e.text[lines[y].start:e.pos])), y
}

func (e *trialEditor) vertical(dir, width int) {
	lines := e.lines(width)
	x, y := e.caret(lines)
	y += dir
	if y >= len(lines) {
		e.done = true
		return
	}
	if y < 0 {
		return
	}
	line := lines[y]
	e.pos = line.start
	g := uniseg.NewGraphemes(string(e.text[line.start:line.end]))
	for g.Next() && g.Width() <= x {
		x -= g.Width()
		e.pos += utf8.RuneCountInString(g.Str())
	}
	if y+1 < len(lines) && e.pos == lines[y+1].start {
		e.pos = graphemeStep(e.text, e.pos, -1)
	}
}
func (w *workspace) trialEditKey(key *tcell.EventKey) {
	e := w.composer
	k := key.Key()
	if w.paste {
		if e.done {
			return
		}
		switch k {
		case tcell.KeyRune:
			if !unicode.IsControl(key.Rune()) {
				e.insert(key.Rune())
			}
		case tcell.KeyEnter, tcell.KeyCtrlJ:
			e.insert('\n')
		case tcell.KeyTab:
			e.insert(' ')
		}
		return
	}
	finish := func() {
		w.composer = nil
		e.save(string(e.text))
	}
	if e.done {
		switch k {
		case tcell.KeyEnter, tcell.KeyCtrlJ:
			finish()
			return
		case tcell.KeyTab, tcell.KeyUp, tcell.KeyLeft:
			e.done = false
			return
		case tcell.KeyCtrlC, tcell.KeyEscape, tcell.KeyCtrlS, tcell.KeyCtrlD:
		default:
			return
		}
	}
	switch k {
	case tcell.KeyCtrlC:
		w.quit = true
	case tcell.KeyEscape, tcell.KeyCtrlS:
		finish()
	case tcell.KeyCtrlD:
		if e.send == nil {
			e.message = "请先完成编辑，返回表单查看可用操作。"
			return
		}
		finish()
		e.send()
	case tcell.KeyEnter, tcell.KeyCtrlJ:
		e.insert('\n')
	case tcell.KeyRune:
		if !unicode.IsControl(key.Rune()) {
			e.insert(key.Rune())
		}
	case tcell.KeyTab:
		e.done = true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		e.remove(graphemeStep(e.text, e.pos, -1), e.pos)
	case tcell.KeyDelete:
		e.remove(e.pos, graphemeStep(e.text, e.pos, 1))
	case tcell.KeyLeft:
		e.pos = graphemeStep(e.text, e.pos, -1)
	case tcell.KeyRight:
		e.pos = graphemeStep(e.text, e.pos, 1)
	case tcell.KeyHome, tcell.KeyCtrlA:
		e.pos = e.lineStart(e.pos)
	case tcell.KeyEnd, tcell.KeyCtrlE:
		e.pos = e.lineEnd(e.pos)
	case tcell.KeyUp, tcell.KeyDown:
		width, _ := w.screen.Size()
		dir := 1
		if k == tcell.KeyUp {
			dir = -1
		}
		e.vertical(dir, max(1, width-4))
	case tcell.KeyCtrlU:
		e.text = nil
		e.pos = 0
		e.size = 0
		e.message = ""
	}
}
func (w *workspace) drawTrialEditor(width, height int, c palette) {
	e := w.composer
	s := w.screen
	limit := width - 4
	put(s, 2, 0, limit, w.current().title+"  ›  "+e.title, c.title)
	bar(s, 1, width, c.muted, '─')
	lines := e.lines(limit)
	cx, cy := e.caret(lines)
	available := height - 6
	top := max(0, cy-available+1)
	for i := top; i < len(lines) && i < top+available; i++ {
		line := lines[i]
		put(s, 2, 2+i-top, limit, string(e.text[line.start:line.end]), c.base)
	}
	put(s, 2, height-4, limit, e.message, c.warning)
	style, action := c.base, "  完成编辑"
	hint := "Enter 换行 · 末行 ↓ 完成"
	if e.done {
		style, action = c.selected, "› 完成编辑"
		hint = "Enter 保存返回 · ↑ 编辑"
	} else {
		s.ShowCursor(2+min(cx, limit-1), 2+cy-top)
	}
	put(s, 1, height-3, width-2, action, style)
	put(s, 1, height-2, width-2, hint, c.accent)
	shortcuts := "Esc 存草稿 · Ctrl+S 保存"
	if e.send != nil {
		shortcuts = "Esc 存草稿 · Ctrl+D 发送"
	}
	put(s, 1, height-1, width-2, shortcuts, c.muted)
	s.Show()
}
