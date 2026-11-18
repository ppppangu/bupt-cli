package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
)

func TestIntegrationExamplesSelectEditCopySaveAndTry(t *testing.T) {
	const base = "https://myai.bupt.edu.cn/llm-gw/v1"
	var calls int
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if m != "portal.read" {
			t.Errorf("preview used %s", m)
		}
		result := domain.PortalPage{Section: p["section"].(string)}
		switch result.Section {
		case "model":
			result.Models = []domain.PortalModel{{ID: "rank", Kind: "rerank"}, {ID: "chat", Kind: "chat"}}
		case "guide":
			result.Data = map[string]any{"base_url": base}
		case "key":
			calls++
		}
		return result, nil
	}}
	w := fixtureWorkspace(t, b)
	w.integrationExamples()
	waitFor(t, w, func() bool { return !w.current().loading })
	first := w.current()
	click(t, w, "base")
	trialKey(w, tcell.KeyEnter)
	screen := w.screen.(tcell.SimulationScreen)
	if got := string(screen.GetClipboardData()); got != base {
		t.Fatal(got)
	}
	w.back()
	click(t, w, "model")
	if w.current().focus != 1 {
		t.Fatal("initial model selection lost")
	}
	click(t, w, "rank")
	click(t, w, "format")
	click(t, w, "json")
	click(t, w, "input")
	click(t, w, "query")
	trialKey(w, tcell.KeyCtrlU)
	typeTrialText(w, "自定义\n问题")
	trialKey(w, tcell.KeyCtrlS)
	w.back()
	click(t, w, "preview")
	w.handleKey(tcell.NewEventKey(tcell.KeyRune, 'c', 0))
	raw := string(screen.GetClipboardData())
	var payload struct {
		Model, Query string
		Documents    []string
	}
	if json.Unmarshal([]byte(raw), &payload) != nil || payload.Model != "rank" || payload.Query != "自定义\n问题" || len(payload.Documents) == 0 {
		t.Fatal("invalid generated request", raw)
	}
	target := filepath.Join(t.TempDir(), "example.json")
	w.handleKey(tcell.NewEventKey(tcell.KeyRune, 's', 0))
	if err := w.input.done(target); err != nil {
		t.Fatal(err)
	}
	w.input = nil
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("example save permissions", err)
	}
	w.handleKey(tcell.NewEventKey(tcell.KeyRune, 's', 0))
	if err := w.input.done(target); err == nil {
		t.Fatal("overwrote an existing file")
	}
	w.input = nil
	disk, err := os.ReadFile(target)
	if err != nil || string(disk) != raw {
		t.Fatal(err)
	}
	w.back()
	click(t, w, "model")
	click(t, w, "chat")
	click(t, w, "model")
	click(t, w, "rank")
	click(t, w, "preview")
	if w.current().copyText != raw {
		t.Fatal("model edit was lost")
	}
	w.back()
	if w.current() != first || calls != 0 {
		t.Fatal("unexpected navigation or key lookup")
	}
	click(t, w, "trial")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "query")
	if string(w.composer.text) != payload.Query || calls != 1 {
		t.Fatal("trial draft not transferred")
	}
}
func TestOfflineExamplesRetainBaseAndReadOnlyTutorial(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if m != "portal.read" || p["cached"] != true {
			t.Errorf("unexpected request %s %#v", m, p)
		}
		if p["section"] == "model" {
			return nil, errors.New("目录暂不可用")
		}
		return domain.PortalPage{Data: map[string]any{"base_url": "https://example.edu/v1"}}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.Cached = true
	w.integrationExamples()
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "base")
	if w.current().copyText != "https://example.edu/v1" {
		t.Fatal("lost Base URL when model catalogue failed")
	}
	w.back()
	w.back()
	w.Commands = map[string]CommandForm{"api": {Path: []string{"api"}, Title: "api", Children: []CommandForm{{Path: []string{"api", "key", "delete"}, Title: "delete", Runnable: true}}}}
	var explained string
	w.Explain = func(name string) string { explained = name; return "只读命令参考" }
	before := len(b.observed())
	click(t, w, "tutorial")
	click(t, w, "keys")
	if w.current().command != "bupt help tutorial keys" {
		t.Fatal(w.current().command)
	}
	w.back()
	click(t, w, "reference")
	click(t, w, "all")
	if explained != "--all" {
		t.Fatal(explained)
	}
	w.back()
	click(t, w, "api")
	click(t, w, "delete")
	if explained != "api key delete --all" || len(b.observed()) != before {
		t.Fatal("reference executed a command", explained)
	}
	w.showHelp()
	if !strings.Contains(w.current().title, "帮助") {
		t.Fatal(w.current().title)
	}
}

func TestContextHelpDiscoverableAndScrollableOnSmallTerminal(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	w.computeHome()
	s := w.screen.(tcell.SimulationScreen)
	line := func(y, cols int) string {
		var b strings.Builder
		for x := 0; x < cols; {
			r, _, _, width := s.GetContent(x, y)
			b.WriteRune(r)
			x += max(1, width)
		}
		return b.String()
	}
	for _, cols := range []int{80, 40} {
		s.SetSize(cols, 12)
		w.draw()
		if !strings.Contains(line(11, cols), "?") {
			t.Fatalf("compute footer at %d cols: %s", cols, line(11, cols))
		}
	}
	w.statusErr = "控制器暂不可用"
	w.handleKey(tcell.NewEventKey(tcell.KeyRune, '?', 0))
	w.draw()
	if !strings.Contains(line(w.listTop, 40), "Enter") {
		t.Fatal("shortcuts not visible", line(w.listTop, 40))
	}
	w.move(len(w.view(w.current()).rows) - 1)
	w.draw()
	if w.current().scroll == 0 || !strings.Contains(line(w.listTop+w.listHeight-1, 40), "查看完整教程") {
		t.Fatal("help or navigation inaccessible")
	}
}
