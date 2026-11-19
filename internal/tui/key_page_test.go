package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
)

func TestKeyReviewReachableFromLastFilteredModel(t *testing.T) {
	var models []domain.PortalModel
	for i := 1; i <= 24; i++ {
		models = append(models, domain.PortalModel{ID: fmt.Sprintf("course-%02d", i), Kind: "chat"})
	}
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		return domain.PortalPage{Section: "model", Models: models}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.screen.(tcell.SimulationScreen).SetSize(60, 24)
	parent := w.current()
	w.keyFormReturning(nil, parent, func(map[string]any) {})
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "name")
	typeTrialText(w, "课程")
	trialKey(w, tcell.KeyEnter)
	navType(w, "/course-24")
	navPress(w, tcell.KeyEnter)
	navPress(w, tcell.KeyEnter)
	if !strings.Contains(strings.Join(w.view(w.current()).facts, " "), "已选 1 个模型") {
		t.Fatal("missing pinned selection count")
	}
	navPress(w, tcell.KeyF2)
	if w.current().title != "核对创建调用密钥" || !strings.Contains(strings.Join(w.view(w.current()).facts, " "), "course-24") || len(b.observed()) != 1 {
		t.Fatal("review should show the fixed scope without creating a key")
	}
}

func TestPortalAccountLabelsVerifiedCampusNumbers(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	for _, tc := range []struct{ sid, field, value string }{
		{"cas_0021", "学工号", "0021"},
		{"0042", "学工号", "0042"},
		{"cas_guest", "账号标识", "cas_guest"},
		{"cas_", "账号标识", "cas_"},
	} {
		page := domain.PortalPage{Account: &domain.PortalAccount{SID: tc.sid}}
		v := w.portalContent("account", "bupt account", page)
		item, ok := findPortalRow(v, tc.field)
		if !ok || item.detail != tc.value || item.open != nil {
			t.Errorf("%s: wanted a read-only %s %q", tc.sid, tc.field, tc.value)
		}
	}
	if v := w.portalContent("account", "bupt account", domain.PortalPage{}); v.empty == "" || len(v.rows) != 0 {
		t.Fatal("missing profile should have an empty state")
	}
}

func TestKeyExportIsAnExplicitTerminalOperation(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	form := CommandForm{Path: []string{"api", "key", "export"}, Query: true, Export: true, Flags: []FormFlag{{Name: "output"}}}
	w.boundCommandInputs(form, "导出 Key", []string{"--output=key.txt", "demo"})
	click(t, w, "go")
	p := w.current()
	if _, ok := findPortalRow(w.view(p), "apply"); !ok || p.load != nil {
		t.Fatal("export must require explicit terminal confirmation, even after a refresh")
	}
}

func TestKeyManagementPreservesScopeAndReturnsToSelection(t *testing.T) {
	key := domain.PortalKey{ID: "lab-key", Name: "课程作业", Models: []string{"chat", "retired"}, Status: "active"}
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "portal.read":
			if p["section"] == "model" {
				return nil, domain.E("NO_CACHE", "目录暂不可用")
			}
			return domain.PortalPage{Section: "key", Keys: []domain.PortalKey{key}}, nil
		case "portal.change":
			if p["action"] != "key.disable" || p["target"] != key.ID {
				t.Fatal(p)
			}
			key.Status = "disabled"
			return map[string]any{"id": key.ID}, nil
		default:
			t.Fatalf("unexpected call %s", method)
			return nil, nil
		}
	}}
	w := fixtureWorkspace(t, b)
	w.portalPage("key", "API Key", "bupt api key", nil)
	list := w.current()
	waitFor(t, w, func() bool { return !list.loading })
	if w.view(list).rows[0].key != key.ID {
		t.Fatal("expected existing keys before the creation shortcut")
	}
	click(t, w, key.ID)
	click(t, w, "models")
	waitFor(t, w, func() bool { return !w.current().loading })
	var ids []string
	for _, r := range w.view(w.current()).rows {
		ids = append(ids, r.title)
	}
	if !strings.Contains(strings.Join(ids, "\n"), "retired") {
		t.Fatal("missing catalogue discarded known scope", ids)
	}
	w.back()
	detail := w.current()
	for i, r := range w.view(detail).rows {
		if r.key == "disable" {
			w.move(i)
			break
		}
	}
	w.handleKey(tcell.NewEventKey(tcell.KeyRight, 0, 0))
	for _, c := range b.observed() {
		if c.method == "portal.change" {
			t.Fatal("Right submitted a key change")
		}
	}
	click(t, w, "disable")
	waitFor(t, w, func() bool { return !detail.submitting })
	if w.current() != detail {
		t.Fatal("status update left the current key")
	}
	w.back()
	waitFor(t, w, func() bool { return !list.loading })
	r, ok := findPortalRow(w.view(list), key.ID)
	if !ok || !strings.Contains(r.detail, "已停用") || list.key != key.ID {
		t.Fatal("updated key was not selected on the original list", r, list.key)
	}
}

func TestNewKeyRevealNeedsStorageReceiptAndClosesOnOrigin(t *testing.T) {
	const secret = "test-only-created-value"
	for _, stored := range []bool{false, true} {
		t.Run(fmt.Sprintf("stored=%t", stored), func(t *testing.T) {
			b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
				switch m {
				case "portal.change":
					return map[string]any{"id": "created", "item": domain.PortalKey{ID: "created", Stored: stored}}, nil
				case "portal.export":
					if p["target"] != "created" {
						t.Errorf("wrong key %v", p)
					}
					return map[string]string{"secret": secret}, nil
				}
				return nil, fmt.Errorf("unexpected %s", m)
			}}
			w := fixtureWorkspace(t, b)
			origin := w.current()
			w.confirmPortalChange("创建密钥", nil, map[string]any{"action": "key.create"}, origin, func(map[string]any) {})
			click(t, w, "submit")
			waitFor(t, w, func() bool { return w.current() == origin })
			if _, ok := objectRow(t, w, "new-secret"); ok != stored || len(b.observed()) != 1 {
				t.Fatal("completion read a secret or offered a missing value")
			}
			if !stored {
				return
			}
			click(t, w, "new-secret")
			waitFor(t, w, func() bool { return !w.current().loading })
			detail := w.current()
			if !detail.overlay || !strings.Contains(strings.Join(w.view(detail).facts, ""), secret) {
				t.Fatal("not a temporary reveal")
			}
			screen := w.screen.(tcell.SimulationScreen)
			if len(screen.GetClipboardData()) > 0 {
				t.Fatal("copied without an explicit action")
			}
			w.refresh()
			click(t, w, "copy")
			if string(screen.GetClipboardData()) != secret {
				t.Fatal("explicit copy failed")
			}
			click(t, w, "done")
			if w.current() != origin || origin.newKeyID != "" || len(b.observed()) != 2 {
				t.Fatal("one-time reveal remained active or was re-read")
			}
		})
	}
}
