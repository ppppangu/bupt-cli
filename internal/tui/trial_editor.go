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
}

func (w *workspace) editTrialText(title, value string, save func(string)) {
	text := []rune(value)
	w.current().editor = &trialEditor{title: title, text: text, pos: len(text), size: len(value), save: save}
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
	e.message = ""
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
	p := w.current()
	e := p.editor
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
		p.editor = nil
		e.save(string(e.text))
	}
	if e.done {
		switch k {
		case tcell.KeyEnter, tcell.KeyCtrlJ:
			finish()
			return
		case tcell.KeyTab, tcell.KeyBacktab, tcell.KeyUp, tcell.KeyLeft:
			e.done = false
			return
		case tcell.KeyCtrlC, tcell.KeyEscape, tcell.KeyCtrlS, tcell.KeyCtrlD:
		default:
			return
		}
	}
	switch k {
	case tcell.KeyEscape, tcell.KeyCtrlC, tcell.KeyCtrlS:
		finish()
	case tcell.KeyCtrlD:
		e.message = "Esc 保留草稿，回到工作台选择「发送请求」。"
	case tcell.KeyEnter, tcell.KeyCtrlJ:
		e.insert('\n')
	case tcell.KeyRune:
		if !unicode.IsControl(key.Rune()) {
			e.insert(key.Rune())
		}
	case tcell.KeyTab, tcell.KeyBacktab:
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
		width, height := w.screen.Size()
		width -= 4
		if p.bench != "" && height >= 14 && w.listRight > w.listLeft {
			width = w.listRight - w.listLeft
		}
		dir := 1
		if k == tcell.KeyUp {
			dir = -1
		}
		e.vertical(dir, max(1, width))
	case tcell.KeyCtrlU:
		e.text = nil
		e.pos = 0
		e.size = 0
		e.message = ""
	}
}
func (w *workspace) drawTrialEditor(width, height int, c palette) {
	p := w.current()
	e := p.editor
	s := w.screen
	left, limit, start := 2, width-4, 2
	if p.bench != "" && height >= 14 {
		left, limit = w.listLeft, w.listRight-w.listLeft
		start = min(w.listTop+1, height-5)
		for y := start - 1; y < height-3; y++ {
			for x := left; x < left+limit; x++ {
				s.SetContent(x, y, ' ', nil, c.base)
			}
		}
	} else if height >= 14 {
		w.drawHeader(p, 2, limit, c)
		start = 5
	}
	put(s, left, start-1, limit, "编辑 · "+e.title, c.accent)
	lines := e.lines(limit)
	cx, cy := e.caret(lines)
	available := max(1, height-start-4)
	top := max(0, cy-available+1)
	for i := top; i < len(lines) && i < top+available; i++ {
		line := lines[i]
		put(s, left, start+i-top, limit, string(e.text[line.start:line.end]), c.base)
	}
	if len(e.text) == 0 {
		put(s, left, start, limit, "在这里输入…", c.muted)
	}
	put(s, 2, height-4, limit, e.message, c.warning)
	if e.done {
		put(s, 2, height-3, limit, "› 完成编辑", c.selected)
	} else {
		s.ShowCursor(left+min(cx, limit-1), start+cy-top)
	}
	put(s, 2, height-2, limit, "Enter 换行  Esc 保留草稿并返回", c.base)
	put(s, 2, height-1, limit, "Tab 完成编辑  Ctrl+U 清空", c.muted)
	s.Show()
}
