package tui

import (
	"context"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
)

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
	click(t, w, "disable")
	for i, r := range w.view(w.current()).rows {
		if r.key == "submit" {
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
	click(t, w, "submit")
	waitFor(t, w, func() bool { return w.current() == list && !list.loading })
	r, ok := findPortalRow(w.view(list), key.ID)
	if !ok || !strings.Contains(r.detail, "已停用") || list.key != key.ID {
		t.Fatal("updated key was not selected on the original list", r, list.key)
	}
}
