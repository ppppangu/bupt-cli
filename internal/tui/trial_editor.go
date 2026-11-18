package tui

import (
	"strings"
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
func (e *trialEditor) remove(i int) {
	e.size -= utf8.RuneLen(e.text[i])
	e.text = append(e.text[:i], e.text[i+1:]...)
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
func (w *workspace) trialEditKey(key *tcell.EventKey) {
	e := w.composer
	k := key.Key()
	if w.paste {
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
	switch k {
	case tcell.KeyCtrlC:
		w.quit = true
	case tcell.KeyEscape:
		w.composer = nil
	case tcell.KeyCtrlS, tcell.KeyCtrlD:
		if k == tcell.KeyCtrlD && e.send == nil {
			e.message = "按 Ctrl+S 保存目前的输入"
			return
		}
		w.composer = nil
		e.save(string(e.text))
		if k == tcell.KeyCtrlD {
			e.send()
		}
	case tcell.KeyEnter, tcell.KeyCtrlJ:
		e.insert('\n')
	case tcell.KeyRune:
		if !unicode.IsControl(key.Rune()) {
			e.insert(key.Rune())
		}
	case tcell.KeyTab:
		e.insert(' ')
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if e.pos > 0 {
			e.pos--
			e.remove(e.pos)
		}
	case tcell.KeyDelete:
		if e.pos < len(e.text) {
			e.remove(e.pos)
		}
	case tcell.KeyLeft:
		if e.pos > 0 {
			e.pos--
		}
	case tcell.KeyRight:
		if e.pos < len(e.text) {
			e.pos++
		}
	case tcell.KeyHome, tcell.KeyCtrlA:
		e.pos = e.lineStart(e.pos)
	case tcell.KeyEnd, tcell.KeyCtrlE:
		e.pos = e.lineEnd(e.pos)
	case tcell.KeyUp:
		start := e.lineStart(e.pos)
		if start > 0 {
			e.pos = min(start-1, e.lineStart(start-1)+e.pos-start)
		}
	case tcell.KeyDown:
		next := e.lineEnd(e.pos) + 1
		if next <= len(e.text) {
			e.pos = min(e.lineEnd(next), next+e.pos-e.lineStart(e.pos))
		}
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
	var lines []string
	var line strings.Builder
	cells, offset, cy, cx := 0, 0, 0, 0
	flush := func() { lines = append(lines, line.String()); line.Reset(); cells = 0 }
	g := uniseg.NewGraphemes(string(e.text))
	for g.Next() {
		token := g.Str()
		size := g.Width()
		if token != "\n" && cells+size > limit {
			flush()
		}
		n := utf8.RuneCountInString(token)
		if e.pos >= offset && e.pos < offset+n {
			cy, cx = len(lines), cells
		}
		if token == "\n" {
			flush()
		} else {
			line.WriteString(token)
			cells += size
		}
		offset += n
	}
	if e.pos == offset {
		if cells >= limit {
			flush()
		}
		cy, cx = len(lines), cells
	}
	flush()
	available := height - 5
	top := max(0, cy-available+1)
	for i := top; i < len(lines) && i < top+available; i++ {
		put(s, 2, 2+i-top, limit, lines[i], c.base)
	}
	if e.message != "" {
		put(s, 2, height-3, limit, e.message, c.warning)
	}
	actions := "Ctrl+S 保存"
	if e.send != nil {
		actions = "Ctrl+D 发送 · " + actions
	}
	put(s, 1, height-2, width-2, actions, c.accent)
	put(s, 1, height-1, width-2, "Enter 换行 · Esc 取消", c.muted)
	s.ShowCursor(2+min(cx, limit-1), 2+cy-top)
	s.Show()
}
