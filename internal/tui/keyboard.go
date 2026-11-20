package tui

import (
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

func actionFrom(rows []row, i, direction int) int {
	for ; i >= 0 && i < len(rows); i += direction {
		if rows[i].open != nil {
			return i
		}
	}
	return -1
}

func (p *page) selectRow(rows []row, i int) {
	p.focus, p.key, p.reading = i, rows[i].key, false
}

func (p *page) selected(v content) *row {
	if p.key == "" || p.reading || p.focus < 0 || p.focus >= len(v.rows) || v.rows[p.focus].open == nil {
		return nil
	}
	return &v.rows[p.focus]
}

// move selects an exact row (for a direct hit, a stable ID, or a preselection).
// Scrolling a document is independent of activating a row.
func (w *workspace) move(to int) {
	p := w.current()
	p.interacted = true
	v := w.view(p)
	if len(v.rows) == 0 {
		return
	}
	to = max(0, min(to, len(v.rows)-1))
	if v.rows[to].open != nil {
		p.selectRow(v.rows, to)
	}
}

func (w *workspace) travel(delta int) {
	p := w.current()
	v := w.view(p)
	n := len(v.rows)
	if n == 0 || delta == 0 {
		return
	}
	w.notice = ""
	p.interacted = true
	dir := 1
	if delta < 0 {
		dir = -1
	}
	height := max(1, w.listHeight)
	last := max(0, n-height)
	start := max(0, min(n-1, p.focus+delta))
	i := actionFrom(v.rows, start, dir)
	// Cross a long read-only section one viewport at a time. Short runs of
	// labels/table headings are skipped in one keystroke.
	nearViewport := i >= p.scroll-1 && i <= p.scroll+height
	nearSelection := !p.reading && (i-p.focus)*dir <= height
	if i >= 0 && i != p.focus && (nearViewport || nearSelection) {
		p.selectRow(v.rows, i)
		return
	}
	next := max(0, min(last, p.scroll+delta))
	if next != p.scroll || v.document && dir < 0 && p.key != "" {
		p.key, p.reading = "", true
		p.scroll, p.focus = next, start
	}
}

func (w *workspace) edge(end bool) {
	p := w.current()
	v := w.view(p)
	if len(v.rows) == 0 {
		return
	}
	w.notice = ""
	p.interacted = true
	p.key, p.reading, p.focus, p.scroll = "", true, 0, 0
	if end {
		p.focus = len(v.rows) - 1
		p.scroll = max(0, len(v.rows)-max(1, w.listHeight))
	}
	if v.rows[p.focus].open != nil {
		p.selectRow(v.rows, p.focus)
	}
}

func (w *workspace) activate() {
	p := w.current()
	v := w.view(p)
	r := p.selected(v)
	if r == nil || p.submitting || (p.loading || p.problem != "") && p.readNavigation == nil && !r.recovery {
		return
	}
	p.interacted = true
	r.open()
}

func (w *workspace) mouse(e *tcell.EventMouse) {
	cols, height := w.screen.Size()
	if cols < 28 || height < 10 {
		return
	}
	x, y := e.Position()
	buttons := e.Buttons()
	pressed := buttons&tcell.Button1 != 0 && w.lastMouse&tcell.Button1 == 0
	w.lastMouse = buttons
	p := w.current()
	if p.editor != nil {
		return
	}
	if p.overlay && pressed && y == 0 {
		return
	}
	if w.input != nil || w.filtering {
		return
	}
	switch {
	case buttons&tcell.WheelDown != 0:
		w.travel(1)
	case buttons&tcell.WheelUp != 0:
		w.travel(-1)
	case pressed && w.listHeight > 0 && y == 0 && x >= 0 && x < 4 && len(w.pages) > 1:
		w.back()
	case pressed && y >= w.listTop && y < w.listTop+w.listHeight && x >= w.listLeft && x < w.listRight:
		i := w.firstRow + y - w.listTop
		v := w.view(w.current())
		if i < len(v.rows) && v.rows[i].open != nil {
			w.current().interacted = true
			w.current().selectRow(v.rows, i)
			w.activate()
		}
	}
}
func (w *workspace) handleKey(e *tcell.EventKey) {
	cols, height := w.screen.Size()
	if cols < 28 || height < 10 {
		if !w.paste && (e.Rune() == 'q' || e.Key() == tcell.KeyCtrlC) {
			w.quit = true
		}
		return
	}
	if w.current().editor != nil {
		w.trialEditKey(e)
		return
	}
	if w.paste && ((w.input == nil && !w.filtering) || e.Key() != tcell.KeyRune) {
		return
	}
	if e.Key() == tcell.KeyCtrlC {
		if w.input != nil {
			w.input = nil
			w.notice = "已取消编辑，原值保留"
		} else if p := w.current(); p.interrupt != nil {
			p.interrupt()
			w.notice = "正在中断等待…"
		} else {
			w.quit = true
		}
		return
	}
	if w.filtering && w.current().nextKey != "" && e.Key() == tcell.KeyF2 {
		w.filtering = false
	}
	if w.input != nil || w.filtering {
		w.handleText(e)
		return
	}
	p := w.current()
	if p.submitting && e.Rune() != 'q' {
		if e.Key() == tcell.KeyEscape {
			w.notice = "请求已经发出，等待回执。退出等待也不会撤销操作"
		}
		return
	}
	if p.copyText != "" {
		switch {
		case e.Rune() == 'c' || e.Key() == tcell.KeyEnter && p.selected(w.view(p)) == nil:
			w.copySnippet(p.copyText)
			return
		case e.Rune() == 's':
			w.saveSnippet(p.copyText)
			return
		}
	}
	switch e.Key() {
	case tcell.KeyF2:
		if p.nextKey != "" && p.build != nil && !p.loading && p.problem == "" && !p.blockedByLogin {
			for _, r := range p.build().rows {
				if r.key == p.nextKey && r.open != nil {
					r.open()
					break
				}
			}
		}
	case tcell.KeyUp:
		w.travel(-1)
	case tcell.KeyDown:
		w.travel(1)
	case tcell.KeyPgDn:
		w.travel(max(1, w.listHeight))
	case tcell.KeyPgUp:
		w.travel(-max(1, w.listHeight))
	case tcell.KeyHome:
		w.edge(false)
	case tcell.KeyEnd:
		w.edge(true)
	case tcell.KeyTab, tcell.KeyBacktab:
		v := w.view(p)
		direction, start := 1, p.focus+1
		if e.Key() == tcell.KeyBacktab {
			direction, start = -1, p.focus-1
		}
		i := actionFrom(v.rows, start, direction)
		if i < 0 {
			start = 0
			if direction < 0 {
				start = len(v.rows) - 1
			}
			i = actionFrom(v.rows, start, direction)
		}
		if i >= 0 {
			p.interacted = true
			p.selectRow(v.rows, i)
		}
	case tcell.KeyRight:
		p.interacted = true
		if !w.disclose(true) {
			if r := p.selected(w.view(p)); r != nil && r.forward {
				w.activate()
			}
		}
	case tcell.KeyEnter:
		w.activate()
	case tcell.KeyEscape:
		if len(w.pages) == 1 && p.filter != "" {
			w.clearFilter(p)
		} else {
			w.back()
		}
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if p.filter != "" {
			w.clearFilter(p)
		} else {
			w.back()
		}
	case tcell.KeyLeft:
		p.interacted = true
		if !w.disclose(false) {
			w.back()
		}
	case tcell.KeyRune:
		id := p.shortcuts[e.Rune()]
		if len(w.pages) == 1 {
			id = map[rune]string{'R': "api", 'S': "compute", 'U': "usage", '?': "tutorial"}[e.Rune()]
		}
		if id != "" {
			v := w.view(p)
			for i, r := range v.rows {
				if r.key == id && r.open != nil {
					p.selectRow(v.rows, i)
					w.activate()
					return
				}
			}
		}
		switch e.Rune() {
		case 'q':
			w.quit = true
		case ' ':
			if r := p.selected(w.view(p)); r != nil && (r.verb == "勾选" || r.verb == "取消勾选") {
				w.activate()
			}
		case 'j':
			w.travel(1)
		case 'k':
			w.travel(-1)
		case '/':
			p.interacted = true
			p.searchStart.query, p.searchStart.key, p.searchStart.focus, p.searchStart.scroll, p.searchStart.reading = p.filter, p.key, p.focus, p.scroll, p.reading
			if p.filter == "" {
				p.filterAnchor = p.key
				p.filterStart.focus, p.filterStart.scroll, p.filterStart.reading = p.focus, p.scroll, p.reading
			}
			p.filterPos = len([]rune(p.filter))
			w.filtering = true
		case 'r':
			w.refresh()
		case '?':
			w.showHelp()
		}
	}
}
func (w *workspace) clearFilter(p *page) {
	p.key, p.filterAnchor, p.filter = p.filterAnchor, "", ""
	p.focus, p.scroll, p.reading = p.filterStart.focus, p.filterStart.scroll, p.filterStart.reading
	w.filtering = false
}

func (w *workspace) handleText(e *tcell.EventKey) {
	p := w.current()
	var value []rune
	var pos int
	if w.input != nil {
		value = w.input.value
		pos = w.input.pos
	} else {
		value = []rune(p.filter)
		pos = p.filterPos
	}
	pos = max(0, min(pos, len(value)))
	switch e.Key() {
	case tcell.KeyEnter:
		if w.input != nil {
			input := w.input
			w.input = nil
			if err := input.done(string(value)); err != nil {
				input.problem = err.Error()
				w.input = input
			}
		} else {
			w.filtering = false
			if p.overlay && p.nextKey == "" {
				w.activate()
				return
			}
			if p.filter == "" {
				w.clearFilter(p)
			}
		}
		return
	case tcell.KeyEscape:
		if w.input != nil {
			w.input = nil
			w.notice = "已取消编辑，原值保留"
		} else if p.overlay {
			w.filtering = false
			w.back()
		} else {
			p.filter, p.key, p.focus, p.scroll, p.reading = p.searchStart.query, p.searchStart.key, p.searchStart.focus, p.searchStart.scroll, p.searchStart.reading
			w.filtering = false
		}
		return
	case tcell.KeyLeft:
		pos = graphemeStep(value, pos, -1)
	case tcell.KeyRight:
		pos = graphemeStep(value, pos, 1)
	case tcell.KeyHome, tcell.KeyCtrlA:
		pos = 0
	case tcell.KeyEnd, tcell.KeyCtrlE:
		pos = len(value)
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		start := graphemeStep(value, pos, -1)
		value = slices.Delete(value, start, pos)
		pos = start
	case tcell.KeyDelete:
		value = slices.Delete(value, pos, graphemeStep(value, pos, 1))
	case tcell.KeyCtrlU:
		value, pos = nil, 0
	case tcell.KeyRune:
		if len(value) < 2048 && !unicode.IsControl(e.Rune()) {
			value = slices.Insert(value, pos, e.Rune())
			pos++
		}
	default:
		return
	}
	if w.input != nil {
		w.input.value, w.input.pos = value, pos
		w.input.problem = ""
	} else {
		p.filterPos = pos
		if next := string(value); next != p.filter {
			p.filter, p.key = next, ""
			p.focus, p.scroll, p.reading = 0, 0, false
		}
	}
}

// Indices are rune offsets, including when one grapheme contains several runes.
func graphemeStep(text []rune, pos, direction int) int {
	g := uniseg.NewGraphemes(string(text))
	start := 0
	for g.Next() {
		end := start + utf8.RuneCountInString(g.Str())
		if direction < 0 && pos <= end {
			return start
		}
		if direction > 0 && pos < end {
			return end
		}
		start = end
	}
	return len(text)
}
