package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
)

func TestLoginRefreshesMetadataOnExamplesAndTrialPages(t *testing.T) {
	for _, tc := range []struct{ entry, denied string }{{"guide", "guide"}, {"guide", "model"}, {"trial", "key"}, {"trial", "guide"}} {
		t.Run(tc.entry+"_"+tc.denied, func(t *testing.T) {
			w, b := testWindow(t, false, filepath.Join(t.TempDir(), "config"))
			var authorized atomic.Bool
			b.reply = func(_ context.Context, method string, params map[string]any) (any, error) {
				if method != "portal.read" {
					return nil, errors.New("unexpected write or inference request")
				}
				section := params["section"].(string)
				if section == tc.denied && !authorized.Load() {
					return nil, domain.E("AUTH_REQUIRED", "metadata needs a login")
				}
				page := domain.PortalPage{Section: section}
				switch section {
				case "guide":
					page.Data = map[string]any{"base_url": "https://example.edu/v1"}
				case "model":
					page.Models = []domain.PortalModel{{ID: "chat", Kind: "chat"}}
				case "key":
					page.Keys = []domain.PortalKey{{ID: "new-key", Stored: true, Status: "active", Models: []string{"chat"}}}
				}
				return page, nil
			}
			w.startAccountView()
			stream := b.stream(t, true)
			click(t, w, "api")
			waitFor(t, w, func() bool { return !w.current().loading })
			click(t, w, tc.entry)
			if tc.entry == "trial" {
				waitFor(t, w, func() bool { return !w.current().loading })
				click(t, w, "chat")
			}
			p := w.current()
			waitFor(t, w, func() bool { return !p.loading })
			if !p.blockedByLogin || p.problem != "" {
				t.Fatal("partial metadata did not register a recoverable rejection")
			}
			hasDraft := tc.entry == "trial" || tc.denied == "guide"
			if hasDraft {
				if tc.entry == "guide" {
					click(t, w, "input")
				}
				click(t, w, "prompt")
				trialKey(w, tcell.KeyCtrlU)
				typeTrialText(w, "临时输入")
			}
			editor := w.composer
			before := len(b.observed())
			authorized.Store(true)
			stream.send(t, positiveFrame(w.Config.Selected))
			waitFor(t, w, func() bool { return w.bar.snapshot.Platform == "authenticated" })
			if editor != w.composer {
				t.Fatal("external login replaced the active editor")
			}
			if w.current() != p {
				if len(b.observed()) != before {
					t.Fatal("read hidden example page before returning to it")
				}
				trialKey(w, tcell.KeyCtrlS)
				w.back()
			}
			waitFor(t, w, func() bool { return !p.loading && !p.blockedByLogin })
			for _, call := range b.observed()[before:] {
				if call.method != "portal.read" || call.params["refresh"] != true {
					t.Fatalf("unexpected post-login request: %+v", call)
				}
			}
			if len(b.observed()) != before+2 || p.problem != "" {
				t.Fatal("metadata was not refreshed exactly once per section", b.observed())
			}
			rows := w.view(p).rows
			if tc.entry == "trial" {
				var key bool
				for _, r := range rows {
					key = key || r.key == "key" && strings.Contains(r.detail, "new-key")
				}
				if !key || w.composer != editor {
					t.Fatal("trial did not show the restored key and retain the editor")
				}
				trialKey(w, tcell.KeyCtrlS)
			} else {
				click(t, w, "base")
				if w.current().copyText != "https://example.edu/v1" {
					t.Fatal("missing recovered gateway")
				}
				w.back()
				click(t, w, "input")
			}
			if hasDraft {
				for _, r := range w.view(w.current()).rows {
					if r.key == "prompt" && strings.Contains(r.detail, "临时输入") {
						return
					}
				}
				t.Fatal("lost model draft")
			}
		})
	}
}
