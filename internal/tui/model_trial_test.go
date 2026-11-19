package tui

import (
	"bupt.local/cli/internal/domain"
	"context"
	"encoding/json"
	"github.com/gdamore/tcell/v2"
	"strings"
	"testing"
)

func trialKey(w *workspace, k tcell.Key) { w.handleKey(tcell.NewEventKey(k, 0, 0)) }
func typeTrialText(w *workspace, s string) {
	for _, r := range s {
		if r == '\n' {
			trialKey(w, tcell.KeyEnter)
		} else {
			w.handleKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
		}
	}
}
func TestTypeAndSendOwnTextInTUI(t *testing.T) {
	for _, kind := range []string{"chat", "embedding", "rerank"} {
		t.Run(kind, func(t *testing.T) {
			model := domain.PortalModel{ID: "m-" + kind, Kind: kind}
			tmpl, _ := domain.TrialFor(model)
			sent := make(chan domain.TrialInput, 3)
			release := make(chan struct{})
			f := &fixtureBackend{reply: func(ctx context.Context, method string, p map[string]any) (any, error) {
				if method == "portal.read" {
					page := domain.PortalPage{Section: p["section"].(string)}
					if page.Section == "key" {
						page.Keys = []domain.PortalKey{{ID: "key-ok", Name: "教学", Status: "active", Stored: true, Models: []string{model.ID}}, {ID: "key-disabled", Status: "disabled", Stored: true, Models: []string{model.ID}}}
					}
					if page.Section == "guide" {
						page.Data = map[string]any{"base_url": "https://myai.bupt.edu.cn/llm-gw/v1"}
					}
					return page, nil
				}
				if method != "portal.trial" || p["key_id"] != "key-ok" || p["model"] != model.ID || p["idempotency_key"] == "" {
					t.Error("wrong submit or key", method)
				}
				var in domain.TrialInput
				_ = json.Unmarshal(domain.Result(p["input"]), &in)
				sent <- in
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return domain.TrialResult{Model: model.ID, Kind: kind, Answer: "模型回复在这里", Vectors: [][]float64{{1, 0}}, Ranked: []domain.RankedText{{Index: 1, Text: "候选乙", Score: .9}}}, nil
			}}
			w := fixtureWorkspace(t, f)
			w.modelPlayground(model, tmpl)
			p := w.current()
			waitFor(t, w, func() bool { return !p.loading })
			switch kind {
			case "embedding":
				click(t, w, "item-0")
			case "rerank":
				click(t, w, "query")
			case "chat":
				click(t, w, "prompt")
				trialKey(w, tcell.KeyCtrlD)
				if p.submitting {
					t.Fatal("editor submitted empty input")
				}
			}
			if w.current().editor == nil || string(w.current().editor.text) != "" {
				t.Fatal("input is not an empty editable field")
			}
			w.paste = true
			typeTrialText(w, "是我输入的\n下一行")
			trialKey(w, tcell.KeyCtrlD)
			w.paste = false
			if w.current().editor == nil || len(sent) > 0 {
				t.Fatal("paste submitted the prompt")
			}
			if kind == "rerank" {
				trialKey(w, tcell.KeyCtrlS)
				for i, key := range []string{"item-0", "item-1"} {
					click(t, w, key)
					typeTrialText(w, []string{"候选甲", "候选乙"}[i])
					trialKey(w, tcell.KeyCtrlS)
				}
				click(t, w, "send")
			} else {
				trialKey(w, tcell.KeyEscape)
				row, _ := objectRow(t, w, "send")
				if row.open == nil {
					t.Fatal("valid draft cannot send")
				}
				w.paste = true
				trialKey(w, tcell.KeyEnter)
				w.paste = false
				if p.submitting {
					t.Fatal("paste submitted from workbench")
				}
				click(t, w, "send")
			}
			if !p.submitting {
				t.Fatal("explicit submit did not start")
			}
			trialKey(w, tcell.KeyEnter)
			in := <-sent
			if kind == "rerank" {
				if in.Query != "是我输入的\n下一行" || strings.Join(in.Documents, ",") != "候选甲,候选乙" {
					t.Fatal(in)
				}
			} else if strings.Join(in.Texts, "") != "是我输入的\n下一行" {
				t.Fatal(in)
			}
			close(release)
			waitFor(t, w, func() bool { return !p.submitting })
			if p.submitting {
				t.Fatal("stuck submitting")
			}
			if w.current() != p || !strings.Contains(w.view(p).panelTitle, "成功") {
				t.Fatal("result left workbench", w.view(p))
			}
			click(t, w, p.key)
			if w.current().editor == nil {
				t.Fatal("cannot edit next request")
			}
			trialKey(w, tcell.KeyEscape)
			w.refresh()
			waitFor(t, w, func() bool { return !p.loading })
			calls := 0
			for _, c := range f.observed() {
				if c.method == "portal.trial" {
					calls++
				}
			}
			if calls != 1 {
				t.Fatal("extra paid calls", calls)
			}
		})
	}
}
func TestOfflineTrialCanEditButCannotSend(t *testing.T) {
	m := domain.PortalModel{ID: "m", Kind: "chat"}
	tpl, _ := domain.TrialFor(m)
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "portal.read" || p["cached"] != true {
			t.Error("network or send in cached mode", method, p)
		}
		return domain.PortalPage{Section: p["section"].(string), Models: []domain.PortalModel{m}}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.Cached = true
	w.modelPlayground(m, tpl)
	waitFor(t, w, func() bool { return !w.current().loading })
	for _, r := range w.view(w.current()).rows {
		if r.key == "send" && r.open != nil {
			t.Fatal("cached mode offered a submission")
		}
	}
	click(t, w, "prompt")
	typeTrialText(w, "跳过\n保存")
	trialKey(w, tcell.KeyCtrlD)
	if w.current().editor == nil || w.current().submitting {
		t.Fatal("cached keyboard send")
	}
	trialKey(w, tcell.KeyCtrlS)
	click(t, w, "code")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "format")
	click(t, w, "json")
	w.view(w.current())
	if w.current().bench != "code" || !json.Valid([]byte(w.current().copyText)) {
		t.Fatal("offline preview missing")
	}
}

func TestChatChoosesKeyAndRechecksBeforeSending(t *testing.T) {
	model := domain.PortalModel{ID: "chat", Kind: "chat"}
	template, _ := domain.TrialFor(model)
	f := &fixtureBackend{reply: func(_ context.Context, method string, params map[string]any) (any, error) {
		if method == "portal.trial" {
			return domain.TrialResult{Kind: "chat", Model: model.ID, Answer: "收到"}, nil
		}
		return domain.PortalPage{Section: params["section"].(string), Keys: []domain.PortalKey{
			{ID: "A", Status: "active", Stored: true, Models: []string{model.ID}},
			{ID: "B", Status: "active", Stored: true, Models: []string{model.ID}},
		}}, nil
	}}
	w := fixtureWorkspace(t, f)
	w.screen.(tcell.SimulationScreen).SetSize(60, 24)
	w.modelPlayground(model, template)
	p := w.current()
	click(t, w, "prompt")
	typeTrialText(w, "课堂提问")
	trialKey(w, tcell.KeyEscape)
	waitFor(t, w, func() bool { return !p.loading })
	click(t, w, "key")
	if !w.current().overlay {
		t.Fatal("key is not a local picker")
	}
	navPress(w, tcell.KeyEscape)
	if w.current() != p {
		t.Fatal("cancel changed location")
	}
	click(t, w, "key")
	click(t, w, "B")
	if w.current() != p {
		t.Fatal("applying a key required a second back")
	}
	w.bar.snapshot.Platform = "expired"
	if r, _ := objectRow(t, w, "send"); r.open != nil {
		t.Fatal("stale authorization")
	}
	w.bar.snapshot = positiveFrame(w.Config.Selected)
	if r, _ := objectRow(t, w, "prompt"); r.detail != "课堂提问" {
		t.Fatal(r)
	}
	click(t, w, "send")
	waitFor(t, w, func() bool { return !p.submitting })
	if w.current() != p {
		t.Fatal("result replaced workbench")
	}

	calls := f.observed()
	if len(calls) != 2 || calls[1].method != "portal.trial" || calls[1].params["key_id"] != "B" {
		t.Fatal("expected two metadata reads and one explicit send with B", calls)
	}
}

func TestPlaygroundShortcutAndKeyCreationReturnToDraft(t *testing.T) {
	model := domain.PortalModel{ID: "class-model", Kind: "chat"}
	created := false
	f := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method == "portal.change" {
			if p["action"] != "key.create" {
				t.Error(method, p)
			}
			created = true
			return map[string]any{"id": "new-key"}, nil
		}
		if method != "portal.read" {
			t.Error("unexpected submission", method)
		}
		page := domain.PortalPage{Section: p["section"].(string)}
		if page.Section == "model" {
			page.Models = []domain.PortalModel{model}
		}
		if page.Section == "key" && created {
			page.Keys = []domain.PortalKey{{ID: "new-key", Name: "new", Stored: true, Status: "active", Models: []string{model.ID}}}
		}
		return page, nil
	}}
	w := fixtureWorkspace(t, f)
	click(t, w, "api")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "trial")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "chat")
	p := w.current()
	click(t, w, "prompt")
	typeTrialText(w, "我的草稿")
	trialKey(w, tcell.KeyEscape)
	waitFor(t, w, func() bool { return !p.loading })
	if r, _ := objectRow(t, w, "send"); r.open != nil {
		t.Fatal("send without key")
	}
	click(t, w, "create-key")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "name")
	typeTrialText(w, "new")
	trialKey(w, tcell.KeyEnter)
	click(t, w, "preview")
	click(t, w, "submit")
	waitFor(t, w, func() bool { return w.current() == p && !p.loading })
	var draft, key bool
	for _, r := range w.view(p).rows {
		draft = draft || r.key == "prompt" && strings.Contains(r.detail, "我的草稿")
		key = key || r.key == "key" && r.detail == "new"
	}
	if !draft || !key || p.editor != nil || w.current() != p {
		t.Fatal("new key did not return to saved input", draft, key)
	}
}

func TestTrialPurposesSwitchModelAndKeepDraft(t *testing.T) {
	models := []domain.PortalModel{{ID: "embedding", Kind: "embedding"}, {ID: "chat-A", Kind: "chat", Health: "ok"}, {ID: "chat-B", Kind: "chat"}}
	b := &fixtureBackend{reply: func(_ context.Context, method string, params map[string]any) (any, error) {
		if method != "portal.read" {
			t.Errorf("browsing or editing submitted %s", method)
		}
		return domain.PortalPage{Section: params["section"].(string), Models: models, Keys: []domain.PortalKey{
			{ID: "a", Name: "课程 A", Status: "active", Stored: true, Models: []string{"chat-A"}},
			{ID: "b", Status: "disabled", Stored: true, Models: []string{"chat-B"}},
			{ID: "c", Status: "active", Models: []string{"chat-B"}},
		}}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.chooseTrialModel()
	waitFor(t, w, func() bool { return !w.current().loading })
	menu := w.view(w.current())
	if len(menu.rows) < 2 || menu.rows[0].key != "chat" || menu.rows[1].key != "embedding" {
		t.Fatal("default order must follow purpose", menu.rows)
	}
	trialKey(w, tcell.KeyEnter)
	if !w.current().overlay {
		t.Fatal("missing compatible-model picker")
	}
	click(t, w, "chat-A")
	p := w.current()
	click(t, w, "prompt")
	waitFor(t, w, func() bool { return !p.loading })
	typeTrialText(w, "你好\n第二段")
	trialKey(w, tcell.KeyDown)
	trialKey(w, tcell.KeyRight)
	if w.current().editor == nil || !w.current().editor.done {
		t.Fatal("expected the local finish-editing choice")
	}
	trialKey(w, tcell.KeyEnter)
	if w.current().editor != nil {
		t.Fatal("Enter on the finish choice should return to the draft")
	}
	if _, ok := findPortalRow(w.view(p), "send"); !ok {
		t.Fatal("completed draft should offer explicit send with the authorized key")
	}
	click(t, w, "model")
	click(t, w, "chat-B")
	v := w.view(p)
	question, _ := findPortalRow(v, "prompt")
	send, _ := findPortalRow(v, "send")
	_, create := findPortalRow(v, "create-key")
	if !strings.Contains(question.detail, "你好 ↵ 第二段") || send.open != nil || !create {
		t.Fatal("new model did not retain input or recheck Key scope", question.detail, send, create)
	}
	click(t, w, "prompt")
	typeTrialText(w, "。")
	trialKey(w, tcell.KeyCtrlD)
	if w.current().editor == nil {
		t.Fatal("editor offered the previous model's send shortcut")
	}
	trialKey(w, tcell.KeyEscape)
	click(t, w, "model")
	click(t, w, "chat-A")
	v = w.view(p)
	key, _ := findPortalRow(v, "key")
	question, _ = findPortalRow(v, "prompt")
	if !strings.HasSuffix(question.detail, "。") || key.detail != "课程 A" {
		t.Fatal("returning from editor or model selection lost the draft or its allowed key")
	}
}

func TestEditorMovesThroughWrappedChineseAndDeletesGraphemes(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	w.screen.(tcell.SimulationScreen).SetSize(28, 10)
	text := strings.Repeat("中", 14) + "e\u0301"
	var saved string
	w.editTrialText("你的问题", text, func(v string) { saved = v })
	trialKey(w, tcell.KeyHome)
	trialKey(w, tcell.KeyDown)
	if w.current().editor.done || w.current().editor.pos != 12 {
		t.Fatal("Down skipped a visible wrapped line", w.current().editor.pos)
	}
	trialKey(w, tcell.KeyEnd)
	trialKey(w, tcell.KeyBackspace2)
	trialKey(w, tcell.KeyDown)
	trialKey(w, tcell.KeyEnter)
	if saved != strings.Repeat("中", 14) {
		t.Fatal("expected the full grapheme to be removed", saved)
	}
}

func TestChatSampleAndUncertainCallLinkToUsage(t *testing.T) {
	m := domain.PortalModel{ID: "chat", Kind: "chat"}
	tpl, _ := domain.TrialFor(m)
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method == "portal.trial" {
			return nil, domain.E("MODEL_RESULT_UNKNOWN", "请核对用量")
		}
		if p["section"] == "usage" {
			return map[string]any{"data": map[string]any{"days": 7, "periodCalls": 1}}, nil
		}
		return domain.PortalPage{Section: p["section"].(string), Keys: []domain.PortalKey{{ID: "key", Status: "active", Stored: true, Models: []string{m.ID}}}}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.modelPlayground(m, tpl)
	form := w.current()
	waitFor(t, w, func() bool { return !form.loading })
	click(t, w, "sample")
	click(t, w, "prompt")
	if string(form.editor.text) != tpl.Sample.Texts[0] {
		t.Fatal("empty sample missing")
	}
	typeTrialText(w, "！")
	navPress(w, tcell.KeyEscape)
	click(t, w, "sample")
	if !w.current().overlay {
		t.Fatal("overwrote existing input")
	}
	navPress(w, tcell.KeyEscape)
	if r, _ := objectRow(t, w, "prompt"); r.detail != tpl.Sample.Texts[0]+"！" {
		t.Fatal(r)
	}
	click(t, w, "send")
	waitFor(t, w, func() bool { return !form.submitting })
	if w.current() != form || !strings.Contains(w.view(form).panelTitle, "结果未知") {
		t.Fatal("unknown outcome not shown in place")
	}
	if r, _ := objectRow(t, w, "send"); r.open != nil {
		t.Fatal("offered immediate duplicate")
	}
	click(t, w, "usage")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "ack")
	if w.current() != form {
		t.Fatal("did not return to workbench")
	}
	if r, _ := objectRow(t, w, "send"); r.open == nil {
		t.Fatal("manual sending still blocked")
	}
	calls := b.observed()
	if len(calls) != 3 || calls[1].method != "portal.trial" || calls[2].params["days"] != float64(7) {
		t.Fatal(calls)
	}
}

func TestInterruptedWaitAndRejectedKeyHaveDistinctRetryBehavior(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "key-rejected", true: "Ctrl-C"}[interrupted], func(t *testing.T) {
			m := domain.PortalModel{ID: "course", Kind: "chat"}
			template, _ := domain.TrialFor(m)
			started := make(chan struct{})
			b := &fixtureBackend{reply: func(ctx context.Context, method string, params map[string]any) (any, error) {
				if method == "portal.read" {
					return domain.PortalPage{Section: "key", Keys: []domain.PortalKey{{ID: "course-key", Stored: true, Status: "active", Models: []string{m.ID}}}}, nil
				}
				close(started)
				if interrupted {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, domain.E("MODEL_KEY_REJECTED", "密钥无效")
			}}
			w := fixtureWorkspace(t, b)
			w.modelPlayground(m, template)
			p := w.current()
			waitFor(t, w, func() bool { return !p.loading })
			click(t, w, "sample")
			click(t, w, "send")
			<-started
			if interrupted {
				trialKey(w, tcell.KeyCtrlC)
			}
			waitFor(t, w, func() bool { return !p.submitting })
			v := w.view(p)
			if w.current() != p || w.quit || strings.Contains(v.panelTitle, "结果未知") != interrupted {
				t.Fatalf("wrong disposition %q", v.panelTitle)
			}
			if _, ok := findPortalRow(v, "usage"); ok != interrupted {
				t.Fatal("incorrect usage recovery")
			}
			if len(b.observed()) != 2 {
				t.Fatal("automatic resubmission")
			}
		})
	}
}

func TestChangingTasksInPlacePreservesDraftAndFitsTerminal(t *testing.T) {
	models := []domain.PortalModel{{ID: "vec", Kind: "embedding"}, {ID: "chat", Kind: "chat"}, {ID: "unknown", Kind: "video"}}
	b := &fixtureBackend{reply: func(_ context.Context, method string, params map[string]any) (any, error) {
		if method == "portal.trial" {
			return domain.TrialResult{Kind: "embedding", Model: "vec", Vectors: [][]float64{{1, 0}, {0, 1}}}, nil
		}
		return domain.PortalPage{Section: params["section"].(string), Models: models, Keys: []domain.PortalKey{{ID: "mine", Stored: true, Status: "active", Models: []string{"vec", "chat"}}}}, nil
	}}
	w := fixtureWorkspace(t, b)
	root := w.current()
	w.chooseTrialModel()
	p := w.current()
	settle := func() { waitFor(t, w, func() bool { return !p.loading && !p.submitting }) }
	settle()
	if r, _ := objectRow(t, w, "rerank"); r.open != nil {
		t.Fatal("unpublished task")
	}
	click(t, w, "chat")
	settle()
	click(t, w, "prompt")
	typeTrialText(w, "保留的问题")
	trialKey(w, tcell.KeyEscape)
	click(t, w, "task")
	click(t, w, "similarity")
	settle()
	if w.current() != p {
		t.Fatal("switching tasks grew the navigation stack")
	}
	click(t, w, "sample")
	click(t, w, "send")
	settle()
	for _, columns := range []int{60, 80, 120} {
		w.screen.(tcell.SimulationScreen).SetSize(columns, 24)
		w.draw()
		full := func() string {
			var s strings.Builder
			for i := 0; i < 24; i++ {
				s.WriteString(navLine(w, i))
				s.WriteByte('\n')
			}
			return s.String()
		}
		if !strings.Contains(full(), "余弦相似度  0.0000") || !strings.Contains(full(), "发送请求") {
			t.Fatalf("missing action or result at %d columns\\n%s", columns, full())
		}
		click(t, w, "item-0")
		w.draw()
		if !strings.Contains(full(), "余弦相似度") {
			t.Fatal("editing hid the result", columns)
		}
		trialKey(w, tcell.KeyEscape)
	}
	click(t, w, "task")
	click(t, w, "chat")
	settle()
	if r, _ := objectRow(t, w, "prompt"); r.detail != "保留的问题" {
		t.Fatal("switch back lost its previous draft", r)
	}
	trialKey(w, tcell.KeyEscape)
	if w.current() != root {
		t.Fatal("Esc returned to a spent task picker")
	}
}
