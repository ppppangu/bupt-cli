package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
	"github.com/gdamore/tcell/v2"
)

type observed struct {
	method string
	params map[string]any
}
type fixtureBackend struct {
	mu    sync.Mutex
	calls []observed
	reply func(context.Context, string, map[string]any) (any, error)
}

func (f *fixtureBackend) Call(ctx context.Context, method string, params, out any) error {
	var m map[string]any
	_ = json.Unmarshal(domain.Result(params), &m)
	f.mu.Lock()
	f.calls = append(f.calls, observed{method, m})
	f.mu.Unlock()
	value, err := f.reply(ctx, method, m)
	if err != nil {
		return err
	}
	return json.Unmarshal(domain.Result(value), out)
}
func (f *fixtureBackend) Stream(context.Context, string, any, io.Reader, io.Writer) (map[string]any, error) {
	return nil, errors.New("unexpected stream")
}
func (f *fixtureBackend) observed() []observed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]observed{}, f.calls...)
}
func fixtureWorkspace(t *testing.T, f *fixtureBackend) *workspace {
	t.Helper()
	ctx, stop := context.WithCancel(t.Context())
	t.Cleanup(stop)
	cfg, e := config.Load(config.Options{HomeDir: t.TempDir(), Env: map[string]string{}, NoProject: true})
	if e != nil {
		t.Fatal(e)
	}
	cfg.Policy.MaxRuntime = "20m"
	s := tcell.NewSimulationScreen("UTF-8")
	if e := s.Init(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Fini)
	s.SetSize(80, 24)
	w := &workspace{Options: Options{Config: cfg, Backend: f, Timeout: time.Second}, screen: s, ctx: ctx, events: make(chan func(), 16), hasReport: true,
		report: domain.StatusReport{Profile: "test", Platform: "authenticated", Gateway: "authenticated", Source: "live", SnapshotAvailable: true, Instances: []domain.Instance{{ID: "101", Name: "gpu-dev", State: "stopped"}}, Runs: []domain.Run{}}}
	w.pages = []*page{{title: "校园算力平台", command: "bupt status", build: w.home}}
	return w
}
func click(t *testing.T, w *workspace, key string) {
	t.Helper()
	for i, r := range w.view(w.current()).rows {
		if r.key == key {
			w.move(i)
			w.handleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
			return
		}
	}
	t.Fatalf("row %q missing on %q", key, w.current().title)
}
func waitFor(t *testing.T, w *workspace, predicate func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for !predicate() {
		select {
		case f := <-w.events:
			f()
		case <-deadline:
			t.Fatalf("timed out on %s", w.current().title)
		}
	}
}
func TestNavigationPreviewsThenSubmitsOnceByStableID(t *testing.T) {
	f := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "instance.start":
			if p["dry_run"] == true {
				return map[string]any{"dry_run": true, "points_per_day": "1.25", "billing_start": "从预留开始可能计费", "run": domain.Run{InstanceID: "101", Deadline: time.Now().Add(20 * time.Minute), Policy: domain.RunPolicy{Cleanup: "release"}}}, nil
			}
			return domain.Run{ID: "run-receipt"}, nil
		case "status.get":
			return domain.StatusReport{Source: "live", Platform: "authenticated", Runs: []domain.Run{{ID: "run-receipt", Name: "gpu-dev", State: "preparing"}}}, nil
		}
		return nil, errors.New("unexpected method " + method)
	}}
	w := fixtureWorkspace(t, f)
	w.report.Instances[0].Reserved = true
	click(t, w, "compute")
	click(t, w, "instances")
	click(t, w, "101")
	click(t, w, "start")
	if len(f.observed()) != 0 {
		t.Fatal("browsing submitted a request")
	}
	click(t, w, "preview")
	waitFor(t, w, func() bool { return !w.current().loading })
	if len(f.observed()) != 1 || f.observed()[0].params["dry_run"] != true {
		t.Fatal(f.observed())
	}
	w.back()
	if w.current().title != "启动实例" {
		t.Fatal(w.current().title)
	}
	click(t, w, "preview")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "submit")
	w.activate()
	waitFor(t, w, func() bool { return w.current().bench == "instance" && w.current().tracking != nil && !w.refreshing })
	var submitted []observed
	var previewKey any
	for _, c := range f.observed() {
		if c.method != "instance.start" {
			continue
		}
		if c.params["dry_run"] == true {
			previewKey = c.params["idempotency_key"]
		} else {
			submitted = append(submitted, c)
		}
	}
	if len(submitted) != 1 {
		t.Fatal(f.observed())
	}
	p := submitted[0].params
	if p["target"] != "101" || p["adopt"] != true || p["wait"] != false || p["max_runtime"] != "20m" || p["idempotency_key"] == "" || p["idempotency_key"] != previewKey {
		t.Fatal(p)
	}
}
func TestCachedBrowsingAndCancelOfSlowList(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	f := &fixtureBackend{reply: func(ctx context.Context, method string, p map[string]any) (any, error) {
		if method == "status.get" && p["cached"] == true {
			return domain.StatusReport{Source: "cached"}, nil
		}
		if method == "data.config" {
			close(started)
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		}
		return nil, errors.New("unexpected " + method)
	}}
	w := fixtureWorkspace(t, f)
	w.Cached = true
	w.refreshStatus(true)
	waitFor(t, w, func() bool { return !w.refreshing })
	click(t, w, "compute")
	w.showHelp()
	click(t, w, "tools")
	click(t, w, "group/data")
	click(t, w, "stores")
	if w.current().problem == "" || len(f.observed()) != 1 {
		t.Fatal(f.observed())
	}
	w.back()
	w.Cached = false
	click(t, w, "stores")
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("read never started")
	}
	w.back()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("leaving a page did not cancel its fetch")
	}
	if w.current().title != "更多算力工具" || w.current().key != "stores" {
		t.Fatal(w.current().title, w.current().key)
	}
}
func TestProgressiveScreenFitsSmallTerminalAndIgnoresPastedCommands(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	for _, size := range [][2]int{{100, 26}, {52, 15}, {30, 10}} {
		s := w.screen.(tcell.SimulationScreen)
		s.SetSize(size[0], size[1])
		w.draw()
		if w.listHeight < 1 || w.listTop+w.listHeight > size[1] {
			t.Fatal(size, w.listTop, w.listHeight)
		}
		var screen strings.Builder
		for y := 0; y < size[1]; y++ {
			for x := 0; x < size[0]; {
				r, comb, _, width := s.GetContent(x, y)
				screen.WriteRune(r)
				screen.WriteString(string(comb))
				x += max(1, width)
			}
			screen.WriteByte('\n')
		}
		if !strings.Contains(screen.String(), "校园算力平台") || strings.Contains(screen.String(), "--force-stop") {
			t.Fatalf("wrong first layer: %q", screen.String())
		}
	}
	w.paste = true
	w.handleKey(tcell.NewEventKey(tcell.KeyRune, 'q', 0))
	w.handleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
	if w.quit || len(w.pages) != 1 {
		t.Fatal("pasted characters activated navigation")
	}
	w.paste = false
	area, _ := navigation.Find("compute")
	w.openArea(area)
	click(t, w, "instances")
	w.report.Instances = append(w.report.Instances, domain.Instance{ID: "102", Name: "second"})
	w.move(1)
	w.report.Instances[0], w.report.Instances[1] = w.report.Instances[1], w.report.Instances[0]
	w.draw()
	if w.current().key != "102" || w.current().focus != 0 {
		t.Fatal("selection was retargeted on refresh")
	}
}

func TestLoginMenuOffersBothSSOMethodsWithoutStartingEither(t *testing.T) {
	f := &fixtureBackend{}
	w := fixtureWorkspace(t, f)
	click(t, w, "tutorial")
	click(t, w, "access")
	click(t, w, "login")
	choices := w.view(w.current()).rows
	if len(choices) != 2 || choices[0].command != "bupt login" || choices[1].command != "bupt login --sso-password" || len(f.observed()) != 0 {
		t.Fatal("login choices did not reflect the CLI")
	}
	w.back()
	if w.current().title != navigation.AccountTitle {
		t.Fatal("login menu lost its place")
	}
}

func TestTwoServicesExposeModelDetailsAndContextualKeyCreation(t *testing.T) {
	f := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "portal.read" {
			t.Fatal("navigation caused a write or compute call", method)
		}
		page := domain.PortalPage{Section: p["section"].(string), Source: "live"}
		if page.Section == "model" {
			page.Models = []domain.PortalModel{{ID: "m-1", Name: "教学模型", Kind: "chat", Health: "ok", Context: "128K"}}
		}
		return page, nil
	}}
	w := fixtureWorkspace(t, f)
	click(t, w, "api")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "model")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "m-1")
	if w.current().command != "bupt api model show m-1" {
		t.Fatal(w.current().command)
	}
	click(t, w, "create")
	waitFor(t, w, func() bool { return !w.current().loading })
	v := w.view(w.current())
	if !strings.Contains(strings.Join(v.facts, " "), "授权模型  m-1") {
		t.Fatal("model creation lost its scope", v.facts)
	}
}

func TestAccountLogoutOpensReviewAndIsHiddenWhenCached(t *testing.T) {
	f := &fixtureBackend{}
	w := fixtureWorkspace(t, f)
	w.acceptAccount(positiveFrame(w.Config.Selected))
	click(t, w, "tutorial")
	click(t, w, "access")
	click(t, w, "logout")
	if w.current().title != "退出登录" || w.current().command != "bupt logout" || w.view(w.current()).rows[0].key != "back" || len(f.observed()) != 0 {
		t.Fatal("logout navigation did not open a cancellable preview")
	}
	w.back()
	w.Cached = true
	for _, r := range w.view(w.current()).rows {
		if r.key == "logout" {
			t.Fatal("logout available in cached mode")
		}
	}
}

func TestPlannedNavigationTreeThroughHomeShortcuts(t *testing.T) {
	backend := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "portal.read":
			return map[string]any{"section": "usage", "data": map[string]any{"days": 7, "periodCalls": 1, "inputSeries": []int{2}, "outputSeries": []int{4}, "dates": []string{"2026-11-18"}}}, nil
		case "resources.overview":
			return domain.ResourceOverview{CatalogSource: "live", Pools: []domain.ResourcePool{{ID: "p", Templates: []domain.TemplateSummary{{ID: "7", Name: "教学实例"}}}}}, nil
		case "spec.estimate":
			return map[string]any{"points_per_day": 0}, nil
		default:
			return nil, errors.New("unexpected navigation call " + method)
		}
	}}
	w := fixtureWorkspace(t, backend)
	home := w.current()
	names := func(prefix string) []string {
		var out []string
		for _, r := range w.view(w.current()).rows {
			if strings.HasPrefix(r.key, prefix) {
				out = append(out, strings.TrimSuffix(r.title, "  · 当前"))
			}
		}
		return out
	}
	check := func(prefix string, want ...string) {
		t.Helper()
		if got := names(prefix); !slices.Equal(got, want) {
			t.Fatalf("%s menu: %v; want %v", w.current().title, got, want)
		}
	}
	for _, width := range []int{60, 80, 120} {
		w.screen.(tcell.SimulationScreen).SetSize(width, 24)
		check("", "R  模型 API", "S  裸算力服务器", "U  余额与用量", "?  帮助与反馈")
		navType(w, "R")
		check("", "1  在线调试", "2  模型与价格", "3  在我的程序中调用", "4  API 密钥", "5  排行榜")
		navPress(w, tcell.KeyEscape)
		navType(w, "S")
		check("", "1  我的实例", "2  创建实例", "3  资源与价格")
		w.Cached = true
		check("", "1  我的实例", "2  创建实例", "3  资源与价格")
		if r, _ := objectRow(t, w, "new"); r.open != nil {
			t.Fatal("creation actionable offline")
		}
		w.Cached = false
		navType(w, "3")
		waitFor(t, w, func() bool { return !w.current().loading })
		w.draw()
		if w.current().key != "spec/7" {
			t.Fatal("utility link displaced the selected resource")
		}
		click(t, w, "tools")
		check("", "▸ 我的工作", "▸ 资源与申请", "▸ 存储与数据", "▸ 项目与费用")
		navPress(w, tcell.KeyEscape)
		navPress(w, tcell.KeyEscape)
		navPress(w, tcell.KeyEscape)
		navType(w, "U")
		usage := w.current()
		waitFor(t, w, func() bool { return !usage.loading })
		check("view/", "1  总览", "2  模型 API", "3  裸算力", "4  趋势", "5  明细")
		navType(w, "4")
		if _, ok := objectRow(t, w, "in"); !ok {
			t.Fatal("trend shortcut missing data")
		}
		navType(w, "5")
		if _, ok := objectRow(t, w, "day/1"); !ok || w.current() != usage {
			t.Fatal("details should be another view of the same workspace")
		}
		navPress(w, tcell.KeyEscape)
		navType(w, "?")
		check("", "反馈问题", "使用帮助", "运行诊断", "联系作者", "账户及相关设置")
		click(t, w, "usage-help")
		check("", "快捷键说明", "API 使用文档", "申请第一台实例并运行程序")
		click(t, w, "keys")
		click(t, w, "tutorials")
		if _, ok := objectRow(t, w, "compute"); !ok {
			t.Fatal("complete tutorials inaccessible")
		}
		navPress(w, tcell.KeyEscape)
		navPress(w, tcell.KeyEscape)
		navPress(w, tcell.KeyEscape)
		navPress(w, tcell.KeyEscape)
		if w.current() != home {
			t.Fatal("could not return to the original home")
		}
	}
}
