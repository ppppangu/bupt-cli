// Package tui presents the current account through the same controller as the
// CLI. Only the event loop owns UI state. RPC replies are applied on that loop.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
	"github.com/gdamore/tcell/v2"
)

type Backend interface {
	Call(context.Context, string, any, any) error
	Stream(context.Context, string, any, io.Reader, io.Writer) (map[string]any, error)
}
type Options struct {
	Commands        map[string]CommandForm
	Capture         func(context.Context, []string) (string, error)
	InitialArea     string
	Explain         func(string) string
	Config          *config.Config
	Backend         Backend
	Foreground      func(context.Context, []string) error
	Cached, NoColor bool
	Timeout         time.Duration
}
type row struct {
	key, title, detail, command string
	open                        func()
}
type content struct {
	fullWidth bool
	facts     []string
	rows      []row
	empty     string
}
type page struct {
	watch                bool
	title, command, area string
	build                func() content
	load                 func(context.Context) (func() content, error)
	needsSchool          bool
	focus, scroll        int
	key, filter, problem string
	loading, submitting  bool
	generation           int
	cancel               context.CancelFunc
}
type prompt struct {
	title   string
	value   []rune
	done    func(string) error
	problem string
}
type workspace struct {
	composer *trialEditor
	fatal    error
	Options
	ctx                           context.Context
	screen                        tcell.Screen
	events                        chan func()
	pages                         []*page
	report                        domain.StatusReport
	hasReport, refreshing         bool
	statusCancel                  context.CancelFunc
	statusGeneration              int
	statusErr, notice             string
	input                         *prompt
	filtering, quit, paste        bool
	listTop, listHeight, firstRow int
	lastMouse                     tcell.ButtonMask
}

func Run(ctx context.Context, o Options) error {
	screen, err := tcell.NewScreen()
	if err != nil {
		return fmt.Errorf("打开终端失败（可用 bupt status 查看总览）: %w", err)
	}
	return runScreen(ctx, o, screen)
}
func runScreen(ctx context.Context, o Options, s tcell.Screen) error {
	if err := s.Init(); err != nil {
		return err
	}
	defer s.Fini()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	w := &workspace{Options: o, ctx: ctx, screen: s, events: make(chan func(), 16)}
	w.pages = []*page{{title: "平台总览", command: "bupt status", build: w.home}}
	s.EnableMouse(tcell.MouseButtonEvents)
	s.EnablePaste()
	input := make(chan tcell.Event)
	done := make(chan struct{})
	defer close(done)
	go s.ChannelEvents(input, done)
	if area, ok := navigation.Find(o.InitialArea); ok {
		w.openArea(area)
	}
	w.refreshStatus(true)
	w.draw()
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for !w.quit {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			p := w.current()
			if !p.watch || p.loading || p.problem != "" || p.submitting {
				continue
			}
			w.loadPage(p)
		case apply := <-w.events:
			apply()
		case event, ok := <-input:
			if !ok {
				return nil
			}
			switch e := event.(type) {
			case *tcell.EventResize:
				s.Sync()
			case *tcell.EventKey:
				w.handleKey(e)
			case *tcell.EventMouse:
				w.mouse(e)
			case *tcell.EventError:
				return e
			case *tcell.EventPaste:
				w.paste = e.Start()
			}
		}
		w.draw()
	}
	return w.fatal
}
func (w *workspace) post(ctx context.Context, f func()) {
	select {
	case <-ctx.Done():
	case <-w.ctx.Done():
	case w.events <- f:
	}
}
func (w *workspace) refreshStatus(initial bool) {
	if w.statusCancel != nil {
		w.statusCancel()
	}
	ctx, cancel := context.WithCancel(w.ctx)
	w.statusCancel = cancel
	w.statusGeneration++
	g := w.statusGeneration
	w.refreshing = true
	w.statusErr = ""
	inCompute := w.current().area == "compute"
	cachedOnly := w.current().area == "api" || len(w.pages) == 1
	go func() {
		defer cancel()
		load := func(cached, final bool) {
			var report domain.StatusReport
			call, stop := context.WithTimeout(ctx, w.Timeout)
			params := map[string]any{"cached": cached}
			if inCompute && !cached {
				params["compute"] = true
			}
			err := w.Backend.Call(call, "status.get", params, &report)
			stop()
			if ctx.Err() != nil {
				return
			}
			w.post(ctx, func() {
				if w.statusGeneration != g {
					return
				}
				if err == nil {
					w.report = report
					w.hasReport = true
					w.statusErr = ""
				} else if final {
					w.statusErr = err.Error()
				}
				if final {
					w.refreshing = false
				}
			})
		}
		if initial && !w.Cached && !cachedOnly {
			load(true, false)
		}
		load(w.Cached || cachedOnly, true)
	}()
}
func (w *workspace) current() *page { return w.pages[len(w.pages)-1] }
func (w *workspace) push(p *page) {
	if p.area == "" {
		p.area = w.current().area
	}
	w.pages = append(w.pages, p)
	w.notice = ""
	w.input = nil
	w.filtering = false
	if p.build == nil {
		w.loadPage(p)
	}
}
func (w *workspace) back() {
	w.notice = ""
	if w.input != nil {
		w.input = nil
		return
	}
	if w.filtering {
		w.filtering = false
		return
	}
	if len(w.pages) < 2 {
		return
	}
	p := w.current()
	if p.submitting {
		w.notice = "正在提交，回执将显示在此页"
		return
	}
	if p.cancel != nil {
		p.cancel()
		p.generation++
	}
	w.pages = w.pages[:len(w.pages)-1]
	if parent := w.current(); parent.loading {
		w.loadPage(parent)
	}
}

type manualRefresh struct{}

func (w *workspace) loadPage(p *page) { w.fetchPage(p, false) }
func (w *workspace) fetchPage(p *page, refresh bool) {
	if p.load == nil {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.generation++
	g := p.generation
	p.problem = ""
	p.loading = false
	if p.needsSchool && w.Cached {
		p.problem = "缓存模式仅显示已保存的数据。用 bupt tui 查看在线内容。"
		return
	}
	ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
	p.cancel = cancel
	p.loading = true
	if refresh {
		ctx = context.WithValue(ctx, manualRefresh{}, true)
	}
	go func() {
		build, err := p.load(ctx)
		defer cancel()
		w.post(w.ctx, func() {
			if p.generation != g {
				return
			}
			p.loading = false
			if err != nil {
				p.problem = err.Error()
				return
			}
			p.build = build
		})
	}()
}
func (w *workspace) refresh() {
	w.notice = ""
	p := w.current()
	if p.submitting {
		return
	}
	if p.load != nil {
		w.fetchPage(p, true)
	} else {
		w.refreshStatus(false)
	}
}
func queryPage[T any](w *workspace, title, command, method string, params any, school bool, show func(T) content) *page {
	return &page{title: title, command: command, needsSchool: school, load: func(ctx context.Context) (func() content, error) {
		var value T
		request := params
		if original, ok := params.(map[string]any); ok && method == "portal.read" && ctx.Value(manualRefresh{}) == true && !w.Cached {
			copy := maps.Clone(original)
			copy["refresh"] = true
			request = copy
		}
		if err := w.Backend.Call(ctx, method, request, &value); err != nil {
			return nil, err
		}
		return func() content { return show(value) }, nil
	}}
}
func (w *workspace) edit(title, value string, done func(string) error) {
	w.input = &prompt{title: title, value: []rune(value), done: done}
}
func (w *workspace) foreground(args ...string) {
	if w.Cached {
		w.notice = "当前为缓存浏览模式"
		return
	}
	if w.Foreground == nil {
		w.notice = "此终端无法打开交互操作"
		return
	}
	if err := w.screen.Suspend(); err != nil {
		w.notice = err.Error()
		return
	}
	err := w.Foreground(w.ctx, args)
	if resume := w.screen.Resume(); resume != nil {
		w.fatal = resume
		w.quit = true
		return
	}
	if err != nil {
		w.notice = err.Error()
	} else {
		w.notice = "操作已完成"
	}
	w.screen.Sync()
	w.refreshStatus(false)
}
func (w *workspace) view(p *page) content {
	var v content
	if p.build != nil {
		v = p.build()
	}
	if p.filter != "" {
		query := strings.ToLower(p.filter)
		filtered := make([]row, 0, len(v.rows))
		for _, r := range v.rows {
			if strings.Contains(strings.ToLower(r.title+" "+r.detail+" "+r.key), query) {
				filtered = append(filtered, r)
			}
		}
		v.rows = filtered
		v.empty = "未找到匹配项，按 / 修改筛选。"
	}
	if p.key != "" {
		if i := slices.IndexFunc(v.rows, func(r row) bool { return r.key == p.key }); i >= 0 {
			p.focus = i
		}
	}
	p.focus = max(0, min(p.focus, len(v.rows)-1))
	if len(v.rows) > 0 {
		p.key = v.rows[p.focus].key
	} else {
		p.key = ""
	}
	return v
}
func (w *workspace) move(to int) {
	p := w.current()
	rows := w.view(p).rows
	if len(rows) == 0 {
		return
	}
	p.focus = max(0, min(to, len(rows)-1))
	p.key = rows[p.focus].key
}
func (w *workspace) activate() {
	p := w.current()
	rows := w.view(p).rows
	if p.submitting || p.loading || p.problem != "" || len(rows) == 0 {
		return
	}
	selected := rows[p.focus]
	if selected.open != nil {
		selected.open()
	} else if selected.detail != "" {
		w.textPage(selected.title, p.command, selected.detail)
	}
}
func (w *workspace) mouse(e *tcell.EventMouse) {
	if w.composer != nil {
		return
	}
	x, y := e.Position()
	buttons := e.Buttons()
	pressed := buttons&tcell.Button1 != 0 && w.lastMouse&tcell.Button1 == 0
	w.lastMouse = buttons
	if w.input != nil || w.filtering {
		return
	}
	switch {
	case buttons&tcell.WheelDown != 0:
		w.move(w.current().focus + 1)
	case buttons&tcell.WheelUp != 0:
		w.move(w.current().focus - 1)
	case pressed && y == 0 && x < 4 && len(w.pages) > 1:
		w.back()
	case pressed && y >= w.listTop && y < w.listTop+w.listHeight:
		i := w.firstRow + y - w.listTop
		if i < len(w.view(w.current()).rows) {
			w.move(i)
			w.activate()
		}
	}
}
func (w *workspace) handleKey(e *tcell.EventKey) {
	if w.composer != nil {
		w.trialEditKey(e)
		return
	}
	if w.paste && ((w.input == nil && !w.filtering) || e.Key() != tcell.KeyRune) {
		return
	}
	if e.Key() == tcell.KeyCtrlC {
		w.quit = true
		return
	}
	if w.input != nil || w.filtering {
		w.handleText(e)
		return
	}
	p := w.current()
	if p.submitting && e.Rune() != 'q' {
		return
	}
	switch e.Key() {
	case tcell.KeyUp:
		w.move(p.focus - 1)
	case tcell.KeyDown:
		w.move(p.focus + 1)
	case tcell.KeyPgDn:
		w.move(p.focus + max(1, w.listHeight))
	case tcell.KeyPgUp:
		w.move(p.focus - max(1, w.listHeight))
	case tcell.KeyHome:
		w.move(0)
	case tcell.KeyEnd:
		w.move(len(w.view(p).rows) - 1)
	case tcell.KeyEnter, tcell.KeyRight:
		w.activate()
	case tcell.KeyEscape, tcell.KeyBackspace, tcell.KeyBackspace2, tcell.KeyLeft:
		w.back()
	case tcell.KeyRune:
		switch e.Rune() {
		case 'q':
			w.quit = true
		case 'j':
			w.move(p.focus + 1)
		case 'k':
			w.move(p.focus - 1)
		case '/':
			w.filtering = true
		case 'r':
			w.refresh()
		case '?':
			w.showHelp()
		}
	}
}
func (w *workspace) handleText(e *tcell.EventKey) {
	p := w.current()
	var value []rune
	if w.input != nil {
		value = w.input.value
	} else {
		value = []rune(p.filter)
	}
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
		}
		return
	case tcell.KeyEscape:
		if w.input != nil {
			w.input = nil
		} else {
			w.filtering = false
			p.filter = ""
			p.key = ""
		}
		return
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if len(value) > 0 {
			value = value[:len(value)-1]
		}
	case tcell.KeyCtrlU:
		value = nil
	case tcell.KeyRune:
		if len(value) < 2048 && !unicode.IsControl(e.Rune()) {
			value = append(value, e.Rune())
		}
	}
	if w.input != nil {
		w.input.value = value
		w.input.problem = ""
	} else {
		p.filter = string(value)
		p.key = ""
		p.focus = 0
	}
}
func (w *workspace) showHelp() {
	p := w.current()
	if p.title == "操作指南" {
		return
	}
	facts := []string{"↑ ↓ 或 j k 选择，Enter 打开，Esc 返回上一层。", "/ 筛选列表，PgUp / PgDn 翻页，r 刷新，q 退出。"}
	var rows []row
	if p.command != "" {
		command := p.command
		if items := w.view(p).rows; len(items) > 0 && items[p.focus].command != "" {
			command = items[p.focus].command
		}
		rows = append(rows, link("cli", "当前操作的命令", command, command, func() { w.textPage("等效命令", command, command) }))
	}
	if area, ok := navigation.Find(p.area); ok {
		for _, name := range area.Commands {
			rows = append(rows, link(name, "bupt "+name, "查看用法和常用选项", "bupt help "+name, func() {
				if w.Explain != nil {
					w.textPage("bupt "+name, "bupt help "+name, w.Explain(name))
				}
			}))
		}
	}
	w.push(&page{title: "操作指南", command: "bupt help --all", build: func() content { return content{facts: facts, rows: rows} }})
}
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u2069') {
			return ' '
		}
		return r
	}, s)
}
func whenError(err error) string {
	var de *domain.Error
	if errors.As(err, &de) && de.RunID != "" {
		return fmt.Sprintf("%s · Run %s", de.Error(), de.RunID)
	}
	return err.Error()
}
