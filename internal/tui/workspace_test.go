package tui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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
	w.pages = []*page{{title: "平台总览", command: "bupt status", build: w.home}}
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
		case "up":
			if p["dry_run"] == true {
				return map[string]any{"dry_run": true, "run": domain.Run{InstanceID: "101", Policy: domain.RunPolicy{Cleanup: "release"}}}, nil
			}
			return domain.Run{ID: "run-receipt"}, nil
		case "job.get":
			return domain.Run{ID: "run-receipt", Name: "gpu-dev", State: "preparing"}, nil
		case "status.get":
			return domain.StatusReport{Source: "live", Platform: "authenticated"}, nil
		}
		return nil, errors.New("unexpected method " + method)
	}}
	w := fixtureWorkspace(t, f)
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
	waitFor(t, w, func() bool { return w.current().title == "运行详情" && !w.current().loading })
	var submitted []observed
	var previewKey any
	for _, c := range f.observed() {
		if c.method != "up" {
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
	click(t, w, "files")
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
	if w.current().title != "存储与文件" || w.current().key != "stores" {
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
		if !strings.Contains(screen.String(), "北邮算力") || strings.Contains(screen.String(), "--force-stop") {
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
	click(t, w, "access")
	click(t, w, "login")
	choices := w.view(w.current()).rows
	if len(choices) != 2 || choices[0].command != "bupt login" || choices[1].command != "bupt login --sso-password" || len(f.observed()) != 0 {
		t.Fatal("login choices did not reflect the CLI")
	}
	w.back()
	if w.current().title != "账号与平台" {
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
	for _, r := range w.view(w.current()).rows {
		if r.key == "m-1" && strings.HasPrefix(r.title, "■ ") {
			return
		}
	}
	t.Fatal("model was not preselected for the key")
}
