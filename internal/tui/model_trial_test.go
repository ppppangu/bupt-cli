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
			field := "prompt"
			if kind == "embedding" {
				field = "item-0"
			}
			if kind == "rerank" {
				field = "query"
			}
			click(t, w, field)
			if w.composer == nil || string(w.composer.text) != "" {
				t.Fatal("input is not an empty editable field")
			}
			w.paste = true
			typeTrialText(w, "是我输入的\n下一行")
			trialKey(w, tcell.KeyCtrlD)
			w.paste = false
			if w.composer == nil || len(sent) > 0 {
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
				trialKey(w, tcell.KeyCtrlD)
			}
			if !p.submitting {
				t.Fatal("explicit submit did not start")
			}
			w.sendTrial(p, &trialForm{})
			in := <-sent
			if kind == "rerank" {
				if in.Query != "是我输入的\n下一行" || strings.Join(in.Documents, ",") != "候选甲,候选乙" {
					t.Fatal(in)
				}
			} else if strings.Join(in.Texts, "") != "是我输入的\n下一行" {
				t.Fatal(in)
			}
			close(release)
			waitFor(t, w, func() bool { return w.current() != p })
			if p.submitting {
				t.Fatal("stuck submitting")
			}
			w.back()
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
		return domain.PortalPage{}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.Cached = true
	w.modelPlayground(m, tpl)
	waitFor(t, w, func() bool { return !w.current().loading })
	for _, r := range w.view(w.current()).rows {
		if r.key == "send" {
			t.Fatal("cached mode offered a submission")
		}
	}
	click(t, w, "prompt")
	typeTrialText(w, "跳过\n保存")
	trialKey(w, tcell.KeyCtrlD)
	if w.composer == nil || w.composer.send != nil {
		t.Fatal("cached keyboard send")
	}
	trialKey(w, tcell.KeyCtrlS)
	click(t, w, "code")
	click(t, w, "json")
	if w.current().title != "请求 JSON" || !json.Valid([]byte(w.current().copyText)) {
		t.Fatal("offline preview missing")
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
	click(t, w, model.ID)
	p := w.current()
	waitFor(t, w, func() bool { return !p.loading })
	click(t, w, "prompt")
	typeTrialText(w, "我的草稿")
	trialKey(w, tcell.KeyCtrlS)
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
		key = key || r.key == "key" && strings.Contains(r.detail, "new-key")
	}
	if !draft || !key {
		t.Fatal("new key did not return to saved input", draft, key)
	}
}
