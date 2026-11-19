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
	w.integrationExamples(nil)
	waitFor(t, w, func() bool { return !w.current().loading })
	first := w.current()
	actions := w.view(first).rows
	if r, _ := findPortalRow(content{rows: actions}, "base"); r.detail != base {
		t.Fatal(r)
	}
	screen := w.screen.(tcell.SimulationScreen)
	click(t, w, "base")
	if string(screen.GetClipboardData()) != base || w.current() != first {
		t.Fatal("address did not copy in place")
	}
	click(t, w, "model")
	if w.current().focus != 1 {
		t.Fatal("initial model selection lost")
	}
	click(t, w, "rank")
	click(t, w, "format")
	click(t, w, "json")
	click(t, w, "query")
	trialKey(w, tcell.KeyCtrlU)
	typeTrialText(w, "自定义\n问题")
	trialKey(w, tcell.KeyCtrlS)
	w.view(first)
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
	for i, r := range w.view(w.current()).rows {
		if r.key == "save" {
			w.move(i)
			trialKey(w, tcell.KeyRight)
			if w.input != nil {
				t.Fatal("right opened a file-write prompt")
			}
			trialKey(w, tcell.KeyEnter)
			if w.input == nil {
				t.Fatal("Enter on save copied instead of offering a path")
			}
			w.input = nil
			break
		}
	}
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
	click(t, w, "model")
	click(t, w, "chat")
	click(t, w, "model")
	click(t, w, "rank")
	w.view(first)
	if w.current().copyText != raw {
		t.Fatal("model edit was lost")
	}
	if w.current() != first || calls != 0 {
		t.Fatal("unexpected navigation or key lookup")
	}
	click(t, w, "trial")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "query")
	if string(w.current().editor.text) != payload.Query || calls != 1 {
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
	w.integrationExamples(nil)
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "base")
	if got := string(w.screen.(tcell.SimulationScreen).GetClipboardData()); got != "https://example.edu/v1" {
		t.Fatal(got)
	}
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

func TestExampleKeyPreparationReturnsRequestedScopeAndPublicID(t *testing.T) {
	f := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		switch m {
		case "portal.read":
			if p["section"] == "key" {
				return domain.PortalPage{Section: "key"}, nil
			}
			if p["section"] != "model" {
				t.Errorf("unexpected read %#v", p)
			}
			return domain.PortalPage{Section: "model", Models: []domain.PortalModel{{ID: "course", Kind: "chat"}}}, nil
		case "portal.change":
			if p["action"] != "key.create" || p["name"] != "课程" || len(p["models"].([]any)) != 1 || p["models"].([]any)[0] != "course" {
				t.Errorf("unexpected write %#v", p)
			}
			return map[string]any{"id": "course-id"}, nil
		}
		t.Errorf("unplanned request %s", m)
		return nil, nil
	}}
	w := fixtureWorkspace(t, f)
	form := &trialForm{model: domain.PortalModel{ID: "course"}}
	home := w.current()
	w.selectTrialKey(home, form, nil, true)
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "new")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "name")
	w.input.value = []rune("课程")
	trialKey(w, tcell.KeyEnter)
	click(t, w, "preview")
	confirm := w.current()
	for i, r := range w.view(confirm).rows {
		if r.key == "submit" {
			w.move(i)
		}
	}
	trialKey(w, tcell.KeyRight)
	if confirm.submitting || len(f.observed()) != 2 {
		t.Fatal("browsing created a key")
	}
	trialKey(w, tcell.KeyEnter)
	waitFor(t, w, func() bool { return w.current() == home })
	code := keySetup(w.Config.Selected, true, form.key)
	if !strings.Contains(code, "course-id") || !strings.Contains(code, "BUPT_API_KEY") || form.key != "course-id" || len(f.observed()) != 3 {
		t.Fatal(code, f.observed())
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

func TestModelToPythonKeepsModelAndNamedKey(t *testing.T) {
	const id = "selected-chat"
	models := []domain.PortalModel{{ID: "another-chat", Kind: "chat"}, {ID: id, Kind: "chat", Name: "所选模型"}}
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "portal.read" {
			return nil, errors.New("unexpected write")
		}
		page := domain.PortalPage{Section: p["section"].(string), Models: models, Data: map[string]any{"base_url": "https://example.invalid/v1"}}
		if page.Section == "key" {
			page.Keys = []domain.PortalKey{
				{ID: "course-id", Name: "我的课程", Stored: true, Status: "active", Models: []string{id}},
				{ID: "wrong-scope", Stored: true, Status: "active", Models: []string{"another-chat"}},
				{ID: "external-key", Status: "active", Models: []string{id}},
			}
		}
		return page, nil
	}}
	w := fixtureWorkspace(t, b)
	open := func(key string) { click(t, w, key); waitFor(t, w, func() bool { return !w.current().loading }) }
	open("api")
	open("model")
	open(id)
	open("guide")
	if r, _ := objectRow(t, w, "model"); r.detail != id {
		t.Fatal(r.detail)
	}
	open("format")
	open("python")
	open("setup")
	if _, ok := objectRow(t, w, "wrong-scope"); ok {
		t.Fatal("another model's key was offered")
	}
	if _, ok := objectRow(t, w, "external-key"); ok {
		t.Fatal("unavailable secret was offered for export")
	}
	if r, _ := objectRow(t, w, "course-id"); r.title != "我的课程" {
		t.Fatal(r)
	}
	open("course-id")
	if w.current().bench != "code" {
		t.Fatal("key selection navigated away")
	}
	open("config")
	w.view(w.current())
	if !strings.Contains(w.current().copyText, "course-id") || !strings.Contains(w.current().copyText, "BUPT_API_KEY") {
		t.Fatal(w.current().copyText)
	}
	open("config")
	w.view(w.current())
	if got := w.current().copyText; !strings.Contains(got, id) || !strings.Contains(got, "urllib.request") || strings.Contains(got, "another-chat") {
		t.Fatal(got)
	}
}

func TestWorkbenchRoundTripPreservesCodeContextAndModalCancellation(t *testing.T) {
	models := []domain.PortalModel{{ID: "chat-a", Kind: "chat"}, {ID: "chat-b", Kind: "chat"}, {ID: "embed", Kind: "embedding"}}
	f := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if m != "portal.read" {
			t.Errorf("navigation called %s", m)
		}
		return domain.PortalPage{Section: p["section"].(string), Models: models, Data: map[string]any{"base_url": "https://example.invalid/v1"}, Keys: []domain.PortalKey{
			{ID: "course", Name: "课程", Stored: true, Status: "active", Models: []string{"chat-a", "chat-b"}},
			{ID: "lab", Name: "实验", Stored: true, Status: "active", Models: []string{"chat-a"}},
		}}, nil
	}}
	w := fixtureWorkspace(t, f)
	open := func(key string) {
		click(t, w, key)
		waitFor(t, w, func() bool { return !w.current().loading })
		w.view(w.current())
	}
	w.integrationExamples(nil)
	code := w.current()
	waitFor(t, w, func() bool { return !code.loading })
	open("format")
	open("python")
	open("prompt")
	trialKey(w, tcell.KeyCtrlU)
	typeTrialText(w, "我的代码输入")
	trialKey(w, tcell.KeyEscape)
	open("setup")
	navType(w, "/实验")
	navPress(w, tcell.KeyEscape)
	if w.current() != code {
		t.Fatal("Esc did not cancel the filtered picker")
	}
	open("setup")
	navType(w, "/课程")
	navPress(w, tcell.KeyEnter)
	if w.current() != code {
		t.Fatal("Enter did not apply and close filtered key selector")
	}
	expected := w.view(code)
	body := code.copyText
	key, _ := findPortalRow(expected, "setup")
	if key.detail != "课程" || !strings.Contains(body, "我的代码输入") {
		t.Fatal(key, body)
	}
	open("trial")
	trial := w.current()
	open("model")
	if _, ok := objectRow(t, w, "embed"); ok {
		t.Fatal("incompatible trial model offered")
	}
	open("chat-b")
	open("prompt")
	typeTrialText(w, "试验")
	trialKey(w, tcell.KeyEscape)
	w.back()
	if w.current() != code || code.key != "trial" {
		t.Fatal("lost source focus")
	}
	w.view(code)
	if code.copyText != body {
		t.Fatal("implicit back changed the original code")
	}
	open("trial")
	open("prompt")
	typeTrialText(w, "带回")
	trialKey(w, tcell.KeyEscape)
	open("code")
	if w.current() != code || w.current() == trial || !strings.Contains(code.copyText, "我的代码输入带回") || !strings.Contains(code.copyText, "urllib.request") {
		t.Fatal("explicit transfer lost format or edited draft", code.copyText)
	}
}
