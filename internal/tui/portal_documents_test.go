package tui

import (
	"context"
	"strings"
	"testing"
)

func findPortalRow(v content, id string) (row, bool) {
	for _, r := range v.rows {
		if r.key == id {
			return r, true
		}
	}
	return row{}, false
}
func TestDocumentReaderAndNewsCursor(t *testing.T) {
	headTitle := "甲"
	f := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "portal.read" {
			t.Fatal("reading posted a write", method)
		}
		var data map[string]any
		item := func(id, title string) map[string]any {
			return map[string]any{"id": id, "title": title, "excerpt": "简介", "publishedAt": "2026-11-18T00:00:00Z", "read": false, "seen": false}
		}
		switch p["section"] {
		case "agreement":
			data = map[string]any{"id": "v1", "title": "协议", "body": `第一段\n第二段`}
		case "news":
			if p["cursor"] == "c2" {
				data = map[string]any{"items": []any{item("a", "甲更新"), item("b", "乙")}, "nextCursor": nil}
			} else {
				data = map[string]any{"items": []any{item("a", headTitle)}, "nextCursor": "c2"}
			}
		case "news.show":
			data = map[string]any{"id": "b", "title": "乙", "bodyMd": "正文第一行\n正文第二行"}
		}
		return map[string]any{"data": data}, nil
	}}
	w := fixtureWorkspace(t, f)
	wait := func() { waitFor(t, w, func() bool { return !w.current().loading }) }
	w.portalPage("agreement", "服务协议", "bupt agreement", nil)
	wait()
	v := w.view(w.current())
	if len(v.rows) != 2 || v.rows[0].title != "第一段" || v.rows[1].title != "第二段" {
		t.Fatal(v)
	}
	w.back()
	w.portalPage("news", "平台动态", "bupt news", nil)
	wait()
	list := w.current()
	click(t, w, "more/c2")
	wait()
	v = w.view(list)
	if len(v.rows) != 2 || v.rows[0].title != "甲更新" || v.rows[1].title != "乙" {
		t.Fatal("cursor merge", v)
	}
	click(t, w, "news/b")
	wait()
	v = w.view(w.current())
	if len(v.rows) < 2 || !strings.HasPrefix(v.rows[0].title, "正文第一行") || v.rows[1].title != "正文第二行" {
		t.Fatal("direct article", v)
	}
	w.back()
	if w.current() != list || w.view(list).rows[list.focus].key != "news/b" {
		t.Fatal("lost article anchor")
	}
	headTitle = "新首页"
	w.refresh()
	wait()
	v = w.view(list)
	if v.rows[0].title != "新首页" {
		t.Fatal("refresh did not restart at first cursor", v)
	}
}
