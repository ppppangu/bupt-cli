// Package tui presents the current account through the same controller as the
// CLI. Only the event loop owns UI state. RPC replies are applied on that loop.
package tui

import (
	"context"
	"encoding/json"
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
	"bupt.local/cli/internal/feedback"
	"bupt.local/cli/internal/navigation"
	"github.com/gdamore/tcell/v2"
)

type Backend interface {
	Call(context.Context, string, any, any) error
	Stream(context.Context, string, any, io.Reader, io.Writer) (map[string]any, error)
}
type Options struct {
	StartWorkspace  func(context.Context, string) error
	ReloadPresets   func() (*config.Config, error)
	PresetAction    func(context.Context, string, map[string]any, bool) (json.RawMessage, error)
	InitialArea     string
	Version         string
	Config          *config.Config
	Backend         Backend
	Foreground      func(context.Context, TerminalAction) error
	Cached, NoColor bool
	Timeout         time.Duration
}
type row struct {
	prose                       bool
	aliases                     string
	forward, recovery           bool
	verb                        string
	group                       string
	preview                     []string
	key, title, detail, command string
	open                        func()
}

func (r row) matches(query string) bool {
	return strings.Contains(strings.ToLower(strings.Join([]string{r.title, r.detail, r.key, r.aliases}, " ")), query)
}

type content struct {
	incident    feedback.Incident
	panelTitle  string
	panelFooter string
	panel       []string
	redrawAt    time.Time
	document    bool
	split       bool
	groups      []navGroup
	fullWidth   bool
	facts       []string
	rows        []row
	empty       string
}
type page struct {
	incident          feedback.Incident
	onLeave           func()
	onFocus           func(string)
	newKeyID          string
	overlay           bool
	shortcuts         map[rune]string
	acceptDraft       func(*trialForm)
	bench             string
	interrupt         context.CancelFunc
	instanceID        domain.ID
	tracking          *instanceOperation
	createStep        bool
	priceDraft        *draft
	nextKey           string
	editor            *trialEditor
	agreementDocument bool
	problemCode       string
	loginMenu         bool
	queryMethod       string
	stale             bool
	expanded          map[string]bool
	filterAnchor      string
	filterStart       struct {
		focus, scroll int
		reading       bool
	}
	searchStart struct {
		query, key    string
		focus, scroll int
		reading       bool
	}
	filterPos            int
	interacted, reading  bool
	copyText             string
	watch                bool
	title, command, area string
	build                func() content
	// Local navigation that remains safe when the query has no usable data.
	readNavigation       func() content
	load                 func(context.Context) (func() content, error)
	needsSchool          bool
	focus, scroll        int
	key, filter, problem string
	loading, submitting  bool
	unsubmitted          bool
	blockedByLogin       bool
	readLoginRevision    uint64
	readConsentRevision  uint64
	generation           int
	cancel               context.CancelFunc
}
type prompt struct {
	pos     int
	title   string
	value   []rune
	done    func(string) error
	problem string
}
type workspace struct {
	statusIncident         feedback.Incident
	trialDrafts            map[string]savedTrial
	redrawAt               time.Time
	statusArea, statusCode string
	connection             controllerRecovery
	bar                    accountView
	fatal                  error
	Options
	ctx                                                context.Context
	screen                                             tcell.Screen
	inputEvents                                        chan tcell.Event
	inputDone                                          chan struct{}
	events                                             chan func()
	pages                                              []*page
	report                                             domain.StatusReport
	hasReport, refreshing                              bool
	statusCancel                                       context.CancelFunc
	statusGeneration                                   int
	statusErr, notice                                  string
	input                                              *prompt
	filtering, quit, paste                             bool
	listTop, listHeight, firstRow, listLeft, listRight int
	lastMouse                                          tcell.ButtonMask
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
	w.pages = []*page{{title: "校园算力平台", command: "bupt status", build: w.home}}
	w.startAccountView()
	s.EnableMouse(tcell.MouseButtonEvents)
	s.EnablePaste()
	w.startInputEvents()
	defer w.stopInputEvents()
	if area, ok := navigation.Find(o.InitialArea); ok {
		w.openArea(area)
	}
	w.refreshStatus(true)
	w.draw()
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	expiry := time.NewTimer(0)
	defer expiry.Stop()
	var setupCheck <-chan time.Time
	for !w.quit {
		var expires <-chan time.Time
		until := w.bar.snapshot.NextExpiry()
		if !w.redrawAt.IsZero() && (until.IsZero() || w.redrawAt.Before(until)) {
			until = w.redrawAt
		}
		if !until.IsZero() {
			expiry.Reset(time.Until(until))
			expires = expiry.C
		}
		// Another terminal can finish local device enrollment without changing
		// the school account revision. Recheck just the pending local record.
		if setupCheck == nil && w.connection.pending && !w.connection.starting {
			setupCheck = time.After(time.Second)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-expires:
			w.bar.snapshot = w.bar.snapshot.Effective(time.Now())
		case <-setupCheck:
			setupCheck = nil
			if !w.connection.pending || w.connection.starting {
				continue
			}
			w.checkPendingSetup()
			if w.connection.pending {
				continue
			}
		case <-ticker.C:
			p := w.current()
			if !p.watch || p.loading || p.problem != "" || p.submitting {
				continue
			}
			if p.bench == "instance" {
				if !w.refreshing {
					w.refreshStatus(false)
				}
			}
			w.loadPage(p)
		case apply := <-w.events:
			apply()
		case event, ok := <-w.inputEvents:
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
	readLogin, readEpoch := w.bar.loginRevision, w.bar.snapshot.Epoch
	w.refreshing = true
	w.statusErr = ""
	w.statusIncident = feedback.Incident{}
	area := w.current().area
	inCompute := area == "compute"
	cachedOnly := !inCompute && area != "resources" && area != "work" && area != "files"
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
				// A late denial from the old session must not replace a login
				// or controller reconnection observed while this request ran.
				code := errorCode(err)
				renewed := !cached && readLogin != w.bar.loginRevision && (schoolLoginCode(code) || computeLoginCode(report) != "")
				if final && (renewed || w.reconnectedSince(readEpoch, code)) {
					w.refreshing = false
					if area == w.current().area {
						w.refreshStatus(false)
					}
					return
				}
				if err == nil {
					w.controllerAvailable()
					w.report = report
					w.hasReport = true
					w.statusErr = ""
					w.statusArea, w.statusCode = area, ""
					if !cached {
						w.statusCode = computeLoginCode(report)
						if w.statusCode != "" {
							w.statusErr = "管理算力资源需要确认学校身份。已准备的 SSH 连接仍可使用。"
							w.statusIncident = feedback.FromError("status.get", domain.E(w.statusCode, ""))
						}
					}
				} else if final {
					w.statusErr = w.controllerProblem(err)
					w.statusArea, w.statusCode = area, errorCode(err)
					w.statusIncident = feedback.FromError("status.get", err)
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
func (w *workspace) popup(p *page) {
	p.overlay = true
	w.push(p)
}
func (w *workspace) picker(title string, build func() content) {
	w.popup(&page{title: title, build: build})
}
func (w *workspace) back() {
	w.notice = ""
	if w.input != nil {
		w.input = nil
		w.notice = "已取消编辑，原值保留"
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
		w.notice = "请求已经发出，等待回执。退出等待也不会撤销操作"
		return
	}
	if p.onLeave != nil {
		p.onLeave()
	}
	if p.cancel != nil {
		p.cancel()
		p.generation++
	}
	if p.priceDraft != nil {
		p.priceDraft.quote.cancel()
	}
	w.pages = w.pages[:len(w.pages)-1]
	if p.unsubmitted {
		w.notice = "已返回修改，操作未提交"
	}
	if parent := w.current(); !w.resumeProtectedRead(parent) && (parent.loading || parent.stale) {
		w.loadPage(parent)
	}
}

type manualRefresh struct{}

func (w *workspace) loadPage(p *page) { w.fetchPage(p, false) }
func (w *workspace) fetchPage(p *page, refresh bool) {
	p.stale = false
	if p.load == nil {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.generation++
	g := p.generation
	p.problem = ""
	p.problemCode = ""
	p.incident = feedback.Incident{}
	p.blockedByLogin = false
	p.readLoginRevision = w.bar.loginRevision
	p.readConsentRevision = w.bar.consentRevision
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
	load := p.load
	readEpoch := w.bar.snapshot.Epoch
	go func() {
		build, err := load(ctx)
		defer cancel()
		w.post(w.ctx, func() {
			if p.generation != g {
				return
			}
			p.loading = false
			if build != nil {
				// Composite reads render their available sections and any errors in place.
				p.build = build
			}
			if err != nil {
				p.problemCode = errorCode(err)
				p.incident = feedback.FromError(p.queryMethod, err)
				if p.queryMethod != "" && w.reconnectedSince(readEpoch, p.problemCode) {
					if p == w.current() {
						w.fetchPage(p, true)
					} else {
						p.stale = true
					}
					return
				}
				if connectionError(p.problemCode) {
					w.controllerProblem(err)
				}
				if build == nil {
					p.problem = readProblem(err)
				}
				p.blockedByLogin = loginRequired(err)
				w.resumeProtectedRead(p)
			}
		})
	}()
}
func (w *workspace) refresh() {
	w.notice = ""
	p := w.current()
	if p.submitting {
		return
	}
	if p.overlay && p.load == nil {
		return
	}
	if p.priceDraft != nil {
		w.loadDraftQuote(p.priceDraft)
		if p.load == nil {
			return
		}
	}
	if p.load != nil {
		if p.bench == "instance" {
			w.refreshStatus(false)
		}
		w.fetchPage(p, true)
	} else {
		w.refreshStatus(false)
		if len(w.pages) == 1 || p.title == navigation.AccountTitle || p.title == prefsTitle {
			w.verifyAccount()
		}
	}
}
func queryPage[T any](w *workspace, title, command, method string, params any, school bool, show func(T) content) *page {
	return &page{title: title, command: command, needsSchool: school, queryMethod: method, load: func(ctx context.Context) (func() content, error) {
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
	w.input = &prompt{title: title, value: []rune(value), pos: len([]rune(value)), done: done}
}
func (w *workspace) foreground(action TerminalAction) bool {
	origin := w.current()
	if w.Cached {
		w.notice = "当前为缓存浏览模式"
		return false
	}
	if w.Foreground == nil {
		w.notice = "此终端无法打开交互操作"
		return false
	}
	reading := w.inputEvents != nil
	w.stopInputEvents()
	if err := w.screen.Suspend(); err != nil {
		if reading {
			w.startInputEvents()
		}
		w.notice = err.Error()
		origin.incident = feedback.FromError(action.Name, err)
		return false
	}
	err := w.Foreground(w.ctx, action)
	if resume := w.screen.Resume(); resume != nil {
		w.fatal = resume
		w.quit = true
		return false
	}
	if reading {
		w.startInputEvents()
	}
	if err != nil {
		w.notice = err.Error()
		if !errors.Is(err, context.Canceled) {
			issue := feedback.FromError(action.Name, err)
			if action.Name == "ssh" && errorCode(err) != "" {
				w.notice = ""
				w.sshFailure(action, issue)
				return false
			}
			origin.incident = issue
		}
	} else {
		origin.incident = feedback.Incident{}
		w.notice = "操作已完成"
		if action.Name == "compute storage push" {
			w.invalidateStorage()
		}
	}
	w.screen.Sync()
	w.refreshStatus(false)
	return err == nil
}

func (w *workspace) startInputEvents() {
	w.inputEvents = make(chan tcell.Event)
	w.inputDone = make(chan struct{})
	go w.screen.ChannelEvents(w.inputEvents, w.inputDone)
}

func (w *workspace) stopInputEvents() {
	if w.inputEvents == nil {
		return
	}
	close(w.inputDone)
	for range w.inputEvents {
	}
	w.inputEvents, w.inputDone = nil, nil
}
func (w *workspace) view(p *page) content {
	var v content
	if (p.loading || p.problem != "") && p.readNavigation != nil {
		v = p.readNavigation()
	} else if p.build != nil {
		v = p.build()
	}
	if len(v.groups) > 0 {
		if p.filter != "" {
			q := strings.ToLower(p.filter)
			v.rows = slices.DeleteFunc(slices.Clone(v.rows), func(r row) bool { return !r.matches(q) })
		}
		v.rows = append(v.rows, w.groupRows(p, v.groups)...)
	}
	if p.newKeyID != "" {
		id := p.newKeyID
		v.rows = append([]row{navLink("new-secret", "查看刚创建的密钥", "从此控制器读取一次，可复制", "", func() { w.revealCreatedKey(p, id) })}, v.rows...)
	}
	v = w.recoveryContent(p, v)
	issue := w.pageIncident(p, v)
	if issue.Code != "" && !p.loading {
		v.rows = append(v.rows, w.feedbackAction(issue))
	}
	v = w.completeFields(p, v)
	if len(v.groups) == 0 && p.filter != "" {
		query := strings.ToLower(p.filter)
		filtered := make([]row, 0, len(v.rows))
		for _, r := range v.rows {
			if r.matches(query) {
				filtered = append(filtered, r)
			}
		}
		v.rows = filtered
		v.empty = "本页没有匹配项，按 / 修改或按退格清除。"
	}
	if len(v.rows) == 0 {
		// An initial load can temporarily hide a preselected object.
		p.focus, p.scroll = 0, 0
		return v
	}
	p.focus = max(0, min(p.focus, len(v.rows)-1))
	if p.key != "" {
		for i, r := range v.rows {
			if r.key == p.key && r.open != nil {
				p.focus, p.reading = i, false
				return v
			}
		}
		p.key = ""
	}
	if p.reading || v.document && v.rows[0].open == nil {
		p.reading = true
		return v
	}
	i := actionFrom(v.rows, p.focus, 1)
	if i < 0 {
		i = actionFrom(v.rows, p.focus-1, -1)
	}
	if i >= 0 {
		p.selectRow(v.rows, i)
	}
	return v
}

func (w *workspace) showHelp() {
	p := w.current()
	if p.title == "本页帮助" {
		return
	}
	instructions := "↑↓ 或 j/k 选择，Enter 执行所选项。Tab / Shift+Tab 切换控件。\n→ 进入可浏览的页面或展开分组，← 返回上一级。\nEsc 返回上一级，/ 本页筛选，退格清除筛选，r 刷新，q 退出。"
	if p.overlay {
		instructions = "↑↓ 或 j/k 选择，Enter 应用，Esc 取消。/ 筛选，Tab 或 Shift+Tab 切换控件。"
	}
	if p.nextKey == "preview" {
		instructions += "\nSpace 切换授权模型，F2 核对授权。"
	}
	if p.shortcuts['n'] != "" {
		instructions += "\nn 新建当前资源。"
	}
	v := w.view(p)
	if p.bench == "code" {
		instructions += "\nc 复制当前预览，s 另存为新文件。选「环境配置与依赖」可以切换到配置说明。"
	} else if p.copyText != "" {
		instructions = "↑↓ 阅读，Enter 或 c 复制全文。\ns 另存为新文件。可用 Tab 跳到操作，再按 Enter 执行所选项。"
	} else if v.document || actionFrom(v.rows, 0, 1) < 0 {
		instructions = "↑↓ 滚动正文，PgUp/PgDn 翻页。\nTab 跳至可用操作，Enter 执行，Esc 返回。"
	}
	var selected string
	if r := p.selected(v); r != nil {
		selected = r.command
		instructions += "\n\n选中项　" + strings.TrimSpace(r.title)
		if r.detail != "" {
			instructions += "\n" + r.detail
		}
	}
	cmd := selected
	if cmd == "" {
		cmd = p.command
	}
	if cmd != "" {
		instructions += "\n\n对应命令\n" + cmd
	}
	issue := w.pageIncident(p, v)
	w.local("本页帮助", "bupt help tutorial", func() content {
		v := w.textContent(instructions)
		v.facts = []string{p.title}
		add := func(key, label, command string) {
			if command != "" {
				v.rows = append(v.rows, navLink(key, label, command, command, func() { w.explainCommand(command) }))
			}
		}
		add("cli", "选中行的 CLI 用法", selected)
		if p.command != selected {
			add("page", "本页相关的 CLI 用法", p.command)
		}
		if p.area == "compute" && p.title != "更多算力工具" {
			v.rows = append(v.rows, w.computeToolsLink())
		}
		if issue.Code != "" {
			v.rows = append(v.rows, w.feedbackAction(issue))
		}
		v.rows = append(v.rows, navLink("help-feedback", navigation.HelpTitle, "", "bupt feedback", w.helpHome))
		v.rows = append(v.rows, navLink("tutorial", "查看完整教程", "", "bupt help tutorial", w.tutorials))
		return v
	})
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
