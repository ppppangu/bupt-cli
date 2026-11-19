package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
	"bupt.local/cli/internal/tuiprefs"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

type testAccountBackend struct {
	*fixtureBackend
	opened chan *testAccountStream
}

type testAccountStream struct {
	live   bool
	frames chan accountDelivery
	drop   chan struct{}
}

type accountDelivery struct {
	value domain.AccountStatus
	done  chan struct{}
}

func (b *testAccountBackend) WatchAccount(ctx context.Context, live bool, handle func(domain.AccountStatus) error) error {
	s := &testAccountStream{live, make(chan accountDelivery), make(chan struct{})}
	select {
	case b.opened <- s:
	case <-ctx.Done():
		return ctx.Err()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.drop:
			return errors.New("controller disconnected")
		case frame := <-s.frames:
			err := handle(frame.value)
			close(frame.done)
			if err != nil {
				return err
			}
		}
	}
}

func (b *testAccountBackend) stream(t *testing.T, wantLive bool) *testAccountStream {
	t.Helper()
	select {
	case s := <-b.opened:
		if s.live != wantLive {
			t.Fatalf("live=%v, want %v", s.live, wantLive)
		}
		return s
	case <-time.After(3 * time.Second):
		t.Fatal("no subscription")
		return nil
	}
}

func (s *testAccountStream) send(t *testing.T, snapshot domain.AccountStatus) {
	t.Helper()
	frame := accountDelivery{snapshot, make(chan struct{})}
	timeout := time.After(time.Second)
	select {
	case s.frames <- frame:
	case <-timeout:
		t.Fatal("subscription did not accept frame")
	}
	select {
	case <-frame.done:
	case <-timeout:
		t.Fatal("subscription did not consume frame")
	}
}

func testWindow(t *testing.T, cached bool, dir string) (*workspace, *testAccountBackend) {
	t.Helper()
	base := &fixtureBackend{reply: func(context.Context, string, map[string]any) (any, error) {
		return domain.StatusReport{}, nil
	}}
	w := fixtureWorkspace(t, base)
	w.Cached = cached
	w.Config.Paths.ConfigDir = dir
	b := &testAccountBackend{base, make(chan *testAccountStream, 4)}
	w.Backend = b
	return w, b
}

func positiveFrame(profile string) domain.AccountStatus {
	balance, running := 124.5, 2
	return domain.AccountStatus{Epoch: "first", Revision: 3, Profile: profile, VPN: "authenticated", Platform: "authenticated", Name: "李老师", CampusID: "T1234", Balance: &balance, Running: &running, ValidUntil: time.Now().Add(time.Minute)}
}

func TestAccountWatchReconnectsWithoutLosingDraftOrFocus(t *testing.T) {
	w, b := testWindow(t, false, filepath.Join(t.TempDir(), "config"))
	w.startAccountView()
	s := b.stream(t, true)
	initial := positiveFrame(w.Config.Selected)
	s.send(t, initial)
	waitFor(t, w, func() bool { return w.bar.snapshot.Name == initial.Name })
	click(t, w, "access")
	w.move(2)
	w.edit("输入", "未提交内容", func(string) error { return nil })
	page, input, key := w.current(), w.input, w.current().key

	logout := domain.AccountStatus{Epoch: initial.Epoch, Revision: 4, Profile: initial.Profile, VPN: "expired", Platform: "blocked_by_gateway"}
	s.send(t, logout)
	s.send(t, initial) // Older response delivered after a logout.
	drained := false
	w.post(w.ctx, func() { drained = true })
	waitFor(t, w, func() bool { return drained })
	if w.bar.snapshot.Revision != 4 || w.bar.snapshot.Name != "" || !w.needsLogin() {
		t.Fatalf("late positive replaced logout: %+v", w.bar.snapshot)
	}
	close(s.drop)
	waitFor(t, w, func() bool { return w.bar.watchProblem != "" })
	if v := w.bar.snapshot; v.Platform == "authenticated" || v.Name != "" || v.Balance != nil || v.CampusID != "" || v.Running != nil {
		t.Fatalf("disconnect retained private fields: %+v", v)
	}
	reconnected := b.stream(t, true)
	initial.Epoch, initial.Revision = "new-controller", 1
	reconnected.send(t, initial)
	waitFor(t, w, func() bool { return w.bar.snapshot.Epoch == initial.Epoch })
	if w.current() != page || w.current().key != key || w.input != input || string(w.input.value) != "未提交内容" || w.bar.watchProblem != "" || w.needsLogin() {
		t.Fatal("reconnection changed draft, focus, or login state")
	}
	initial.Profile = "another-user"
	reconnected.send(t, initial)
	waitFor(t, w, func() bool { return w.bar.watchProblem != "" })
	if w.bar.snapshot.Name != "" || w.bar.snapshot.Epoch != "" {
		t.Fatal("a frame for another profile remained visible")
	}
}

func TestExternalLoginRestoresProtectedRead(t *testing.T) {
	for _, during := range []bool{false, true} {
		t.Run(fmt.Sprintf("login_during_read_%t", during), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var reads atomic.Int32
			f := &fixtureBackend{reply: func(ctx context.Context, _ string, params map[string]any) (any, error) {
				if reads.Add(1) == 1 {
					close(started)
					select {
					case <-ctx.Done():
						return nil, ctx.Err()
					case <-release:
					}
					return nil, fmt.Errorf("read model: %w", domain.E("AUTH_REQUIRED", "fixture"))
				}
				if params["refresh"] != true {
					return nil, errors.New("retry did not refresh the protected read")
				}
				return domain.PortalPage{}, nil
			}}
			w := fixtureWorkspace(t, f)
			p := queryPage(w, "模型", "bupt api model", "portal.read", map[string]any{"section": "model"}, false, func(domain.PortalPage) content { return content{} })
			w.push(p)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("protected read did not start")
			}
			w.edit("备注", "保留", func(string) error { return nil })
			input := w.input
			if during {
				w.acceptAccount(positiveFrame(w.Config.Selected))
			}
			close(release)
			if !during {
				waitFor(t, w, func() bool { return p.blockedByLogin })
				w.acceptAccount(positiveFrame(w.Config.Selected))
			}
			waitFor(t, w, func() bool { return !p.loading && p.build != nil })
			if reads.Load() != 2 || p.problem != "" || w.input != input || w.current() != p {
				t.Fatal("protected read did not resume in place", reads.Load(), p.problem)
			}
		})
	}
}

func TestDirectAccountEntryReadsLocalReportAndVerifiesOnlyOnRequest(t *testing.T) {
	release := make(chan struct{})
	f := &fixtureBackend{reply: func(ctx context.Context, method string, params map[string]any) (any, error) {
		switch method {
		case "status.get":
			if params["cached"] != true {
				return nil, errors.New("account configuration fetched compute state")
			}
			return domain.StatusReport{}, nil
		case "account.refresh":
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
			}
			return domain.AccountStatus{VPN: "unknown", Platform: "unknown"}, nil
		}
		return nil, errors.New("unexpected method " + method)
	}}
	w := fixtureWorkspace(t, f)
	account, _ := navigation.Find("access")
	w.openArea(account)
	w.refreshStatus(true)
	waitFor(t, w, func() bool { return !w.refreshing })
	if calls := f.observed(); w.current().title != navigation.AccountTitle || w.statusErr != "" || len(calls) != 1 {
		t.Fatal(w.current().title, w.statusErr, calls)
	}
	click(t, w, "topbar")
	click(t, w, "verify")
	w.verifyAccount()
	close(release)
	waitFor(t, w, func() bool { return !w.bar.verifying })
	if calls := f.observed(); len(calls) != 2 || calls[1].method != "account.refresh" || w.notice != "暂时无法确认学校会话" {
		t.Fatal(w.notice, calls)
	}
}

func TestPreferenceChangesReachBothWindowsAndCachedModeStaysOffline(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "config")
	a, live := testWindow(t, false, dir)
	b, cached := testWindow(t, true, dir)
	a.startAccountView()
	b.startAccountView()
	live.stream(t, true)
	cached.stream(t, false)
	for _, w := range []*workspace{a, b} {
		click(t, w, "access")
		rows := w.view(w.current()).rows
		if rows[len(rows)-2].key != "news" || rows[len(rows)-1].key != "agreement" || w.current().title != navigation.AccountTitle {
			t.Fatal("unexpected account menu")
		}
		click(t, w, "topbar")
	}
	click(t, a, "name")
	waitFor(t, b, func() bool { return b.bar.prefs.ShowName })
	click(t, b, "auto_verify")
	waitFor(t, a, func() bool { return !a.bar.prefs.AutoVerify })
	live.stream(t, false)
	click(t, a, "defaults")
	waitFor(t, b, func() bool { return b.bar.prefs == tuiprefs.Defaults() })
	live.stream(t, true)
	if len(cached.opened) > 0 {
		t.Fatal("cached subscriber reopened when live preferences changed")
	}
	b.refresh()
	waitFor(t, b, func() bool { return !b.refreshing })
	for _, c := range cached.observed() {
		if c.method != "status.get" || c.params["cached"] != true {
			t.Fatalf("cached view sent %s %+v", c.method, c.params)
		}
	}
}

func TestAccountLineUsesTwoStatesAndResponsiveOptionalFields(t *testing.T) {
	s := positiveFrame("test")
	prefs := tuiprefs.Defaults()
	wide := accountLine(s, prefs, 120)
	for _, want := range []string{"WebVPN 已登录", "算力平台 已登录", "余额 124.5 点", "运行 2 台"} {
		if !strings.Contains(wide, want) {
			t.Fatal(wide)
		}
	}
	for _, hidden := range []string{"实时", "缓存", "门户", s.Name, s.CampusID} {
		if strings.Contains(wide, hidden) {
			t.Fatal(wide)
		}
	}
	prefs.ShowName, prefs.ShowCampusID = true, true
	if got := accountLine(s, prefs, 120); !strings.Contains(got, s.Name) || !strings.Contains(got, s.CampusID) {
		t.Fatal(got)
	}
	for _, width := range []int{26, 34, 60} {
		line := accountLine(s, prefs, width)
		if uniseg.StringWidth(line) > width || strings.Count(line, "已登录") != 2 {
			t.Fatalf("%d: %q", width, line)
		}
	}
	s.Platform, s.VPN = "not_configured", "not_configured"
	if line := accountLine(s, prefs, 120); !strings.Contains(line, "WebVPN 直连") || !strings.Contains(line, "算力平台 未配置") || strings.Contains(line, s.Name) || strings.Contains(line, "124.5") {
		t.Fatal(line)
	}
}

// Capture one rendered row inside the screen's owner goroutine.
type statusScreen struct {
	tcell.Screen
	line chan string
	y    int
}

func (s *statusScreen) Show() {
	s.Screen.Show()
	width, _ := s.Size()
	var b strings.Builder
	for x := 0; x < width; {
		r, comb, _, n := s.GetContent(x, s.y)
		b.WriteRune(r)
		b.WriteString(string(comb))
		x += max(1, n)
	}
	select {
	case s.line <- b.String():
	default:
	}
}

func TestStaticHomeRedrawsWhenAuthenticationEvidenceExpires(t *testing.T) {
	w, backend := testWindow(t, false, filepath.Join(t.TempDir(), "config"))
	s := &statusScreen{tcell.NewSimulationScreen("UTF-8"), make(chan string, 16), 2}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = runScreen(ctx, w.Options, s)
	}()
	defer func() { cancel(); <-done }()
	stream := backend.stream(t, true)
	status := positiveFrame(w.Config.Selected)
	status.RunningValidUntil = time.Now().Add(125 * time.Millisecond)
	status.ValidUntil = status.RunningValidUntil.Add(175 * time.Millisecond)
	stream.send(t, status)
	for _, wanted := range []string{"运行 2 台", "运行 — 台", "WebVPN 待确认"} {
		timeout := time.After(2 * time.Second)
		for {
			select {
			case line := <-s.line:
				if strings.Contains(line, wanted) {
					if wanted == "WebVPN 待确认" && strings.Contains(line, "124.5") {
						t.Fatal("expired balance still visible")
					}
					if wanted == "运行 — 台" && !strings.Contains(line, "WebVPN 已登录") {
						t.Fatal("running and login evidence must expire independently")
					}
					goto nextState
				}
			case <-timeout:
				t.Fatalf("static home did not render %q", wanted)
			}
		}
	nextState:
	}
}
