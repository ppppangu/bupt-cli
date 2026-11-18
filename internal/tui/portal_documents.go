package tui

import (
	"context"
	"fmt"
	"slices"
	"time"

	"bupt.local/cli/internal/domain"
)

func portalNotes(o domain.PortalObservation) []string {
	var facts []string
	if o.Source == "cached" && !o.ObservedAt.IsZero() {
		facts = append(facts, "保存于 "+moment(o.ObservedAt))
	}
	return append(facts, o.Warnings...)
}

func portalTime(raw string) string {
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return raw
}

func (w *workspace) portalDocument(section, title, cmd string, extra map[string]any) {
	params := map[string]any{"section": section, "cached": w.Cached}
	for k, v := range extra {
		params[k] = v
	}
	var p *page
	p = queryPage(w, title, cmd, "portal.read", params, false, func(result domain.PortalContent[domain.PortalDocument]) content {
		d := result.Data
		v := w.textContent(d.Body)
		v.facts = []string{d.Title}
		if d.PublishedAt != "" {
			v.facts = append(v.facts, portalTime(d.PublishedAt))
		}
		if d.Version != "" {
			v.facts = append(v.facts, "版本 "+d.Version)
		}
		v.facts = append(v.facts, portalNotes(result.PortalObservation)...)
		if w.Cached {
			return v
		}
		if section == "agreement" && result.Account != nil && result.Account.ConsentRequired {
			args := []string{"agreement", "accept", d.ID, "--confirm", d.ID}
			v.rows = append(v.rows, link("accept", "接受本版本服务协议", "", cmdline(args...), func() {
				w.confirmPortalChange("接受协议 "+d.Version, args, map[string]any{"action": "agreement.accept", "target": d.ID, "confirm": d.ID}, p, func(map[string]any) { w.fetchPage(p, true) })
			}))
		}
		if section == "news.show" {
			args := []string{"news", "read", d.ID}
			v.rows = append(v.rows, link("read", "标记为已读", "", cmdline(args...), func() {
				w.confirmPortalChange("标记为已读", args, map[string]any{"action": "news.read", "target": d.ID}, p, func(map[string]any) { w.back(); w.refresh() })
			}))
		}
		return v
	})
	w.push(p)
}

func joinNews(before, after []domain.PortalNewsItem) []domain.PortalNewsItem {
	out := slices.Clone(before)
	at := make(map[string]int, len(before))
	for i, item := range out {
		at[item.ID] = i
	}
	for _, item := range after {
		if i, ok := at[item.ID]; ok {
			out[i] = item
			continue
		}
		at[item.ID] = len(out)
		out = append(out, item)
	}
	return out
}

// Each cursor request captures its own immutable prefix. Only fetchPage's
// generation-checked callback publishes the result to the event loop.
func (w *workspace) newsList() {
	p := &page{title: "平台动态", command: "bupt news"}
	var load func([]domain.PortalNewsItem, []string, string)
	load = func(before []domain.PortalNewsItem, visited []string, cursor string) {
		p.load = func(ctx context.Context) (func() content, error) {
			before, visited, cursor := before, visited, cursor
			if ctx.Value(manualRefresh{}) == true {
				before = nil
				visited = nil
				cursor = ""
			}
			params := map[string]any{"section": "news", "limit": 20, "cached": w.Cached}
			if cursor != "" {
				params["cursor"] = cursor
			}
			if ctx.Value(manualRefresh{}) == true && !w.Cached {
				params["refresh"] = true
			}
			var doc domain.PortalContent[domain.PortalNews]
			if err := w.Backend.Call(ctx, "portal.read", params, &doc); err != nil {
				return nil, err
			}
			items := joinNews(before, doc.Data.Items)
			next := doc.Data.NextCursor
			return func() content {
				v := content{empty: "暂无平台动态。"}
				if doc.Data.UnreadCount != nil && len(items) > 0 {
					v.facts = []string{fmt.Sprintf("已显示 %d 条 · 未读 %d 条", len(items), *doc.Data.UnreadCount)}
				}
				v.facts = append(v.facts, portalNotes(doc.PortalObservation)...)
				for _, item := range items {
					state := "未读"
					if item.Read {
						state = "已读"
					}
					cmd := cmdline("news", "show", item.ID)
					v.rows = append(v.rows, link("news/"+item.ID, item.Title, state+" · "+portalTime(item.PublishedAt)+"  "+item.Excerpt, cmd, func() { w.portalDocument("news.show", item.Title, cmd, map[string]any{"target": item.ID}) }))
				}
				if next != "" {
					if next == cursor || slices.Contains(visited, next) {
						v.facts = append(v.facts, "学校重复返回了同一分页游标。")
					} else {
						v.rows = append(v.rows, link("more/"+next, "加载更多", "", "bupt news --cursor "+quoteArg(next), func() {
							if !p.loading {
								load(items, append(slices.Clone(visited), cursor), next)
								w.fetchPage(p, false)
							}
						}))
					}
				}
				if !w.Cached && doc.Data.UnreadCount != nil && *doc.Data.UnreadCount > 0 {
					v.rows = append(v.rows, link("all", "全部标记已读", "", "bupt news read all", func() {
						w.confirmPortalChange("全部标记已读", []string{"news", "read", "all"}, map[string]any{"action": "news.read", "target": "all"}, p, func(map[string]any) { load(nil, nil, ""); w.fetchPage(p, true) })
					}))
				}
				return v
			}, nil
		}
	}
	load(nil, nil, "")
	w.push(p)
}
