package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/uniseg"
)

func TestLeaderboardReadableTablesPrivacyAndRankOrdering(t *testing.T) {
	f := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if m != "portal.read" {
			t.Fatal("browsing changed preferences", m)
		}
		switch p["section"] {
		case "leaderboard":
			return map[string]any{"data": map[string]any{"windowDays": 30, "disclosureUnlocked": true, "me": map[string]any{"ranked": false, "reason": "beyond_top_100"},
				"modelLeaderboard": []map[string]any{{"rank": 2, "model": "tiny", "sharePercent": .001}, {"rank": 1, "model": "模型长名称模型长名称", "sharePercent": 40.5}},
				"userLeaderboard":  []map[string]any{{"rank": 1, "label": "用户 #01", "totalTokens": 123456}, {"rank": 1, "label": "用户 #02", "totalTokens": 123456}}}}, nil
		case "preference":
			return map[string]any{"data": map[string]any{"mode": "anonymous", "unlocked": true, "options": map[string]any{"partial": map[string]any{"available": true, "preview": "王*"}, "real": map[string]any{"available": false}}}}, nil
		}
		t.Fatal(p)
		return nil, nil
	}}
	w := fixtureWorkspace(t, f)
	w.portalPage("leaderboard", "排行榜", "bupt api leaderboard", nil)
	waitFor(t, w, func() bool { return !w.current().loading })
	for _, width := range []int{80, 30} {
		w.screen.(tcell.SimulationScreen).SetSize(width, 20)
		v := w.view(w.current())
		row, ok := findPortalRow(v, "model/0")
		if !ok || !strings.Contains(row.title, "40.50%") || uniseg.StringWidth(row.title) > w.metricWidth() {
			t.Fatal(width, row)
		}
		small, _ := findPortalRow(v, "model/1")
		if !strings.Contains(small.title, "<0.01%") {
			t.Fatal(small)
		}
		if _, ok := findPortalRow(v, "me"); ok || v.rows[0].key != "tab" {
			t.Fatal("model rankings included a user rank")
		}
	}
	click(t, w, "tab")
	me, _ := findPortalRow(w.view(w.current()), "me")
	if !strings.Contains(me.title, "100 名以外") || me.open != nil {
		t.Fatal(me)
	}
	u, ok := findPortalRow(w.view(w.current()), "user/0")
	if !ok || !strings.Contains(u.title, "123,456") || !strings.Contains(u.title, "用户 #01") {
		t.Fatal(u)
	}
	click(t, w, "user/1")
	if !strings.Contains(w.current().title, "用户 #02") {
		t.Fatal(w.current().title)
	}
	w.back()
	click(t, w, "preference")
	waitFor(t, w, func() bool { return !w.current().loading })
	v := w.view(w.current())
	r, _ := findPortalRow(v, "partial")
	if r.open == nil {
		t.Fatal("eligible mode hidden")
	}
	for _, r := range v.rows {
		if r.title == "实名" && r.open != nil {
			t.Fatal("ineligible mode selectable")
		}
	}
}

func TestUsageSeriesMissingDaysAndPeriodSwitch(t *testing.T) {
	f := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if m != "portal.read" {
			t.Fatal(m)
		}
		if p["days"] == float64(7) {
			return map[string]any{"account": map[string]any{"balance_points": 14}, "data": map[string]any{"days": 7, "usageAvailable": false}}, nil
		}
		return map[string]any{"data": map[string]any{"days": 30, "periodCalls": 0, "successRate": 0, "inputSeries": []any{3, nil, 0}, "outputSeries": []any{4, 1}, "modelDistribution": []map[string]any{{"name": "small", "value": 1}, {"name": "large", "value": 3}}}}, nil
	}}
	w := fixtureWorkspace(t, f)
	w.portalPage("usage", "余额与用量", "bupt api usage", nil)
	waitFor(t, w, func() bool { return !w.current().loading })
	root := w.current()
	for _, id := range []string{"view/overview", "view/api", "view/compute", "view/trend", "view/detail", "period", "balance", "calls", "tokens"} {
		if _, ok := objectRow(t, w, id); !ok {
			t.Fatal("missing overview control", id)
		}
	}
	click(t, w, "view/api")
	for _, id := range []string{"balance", "calls", "success", "tokens", "input-total", "output-total"} {
		if r, ok := objectRow(t, w, id); !ok || r.open != nil {
			t.Fatalf("missing or unexpectedly actionable summary %s", id)
		}
	}
	click(t, w, "view/trend")
	r, _ := objectRow(t, w, "in")
	if !strings.Contains(r.title, "?·") {
		t.Fatal(r)
	}
	r, _ = objectRow(t, w, "out")
	if !strings.HasSuffix(r.title, "?") {
		t.Fatal(r)
	}
	click(t, w, "view/detail")
	w.screen.(tcell.SimulationScreen).SetSize(34, 16)
	total, _ := objectRow(t, w, "day/2")
	if !strings.HasSuffix(total.title, "—") {
		t.Fatal("missing converted to zero", total)
	}
	click(t, w, "group")
	click(t, w, "model")
	r, _ = objectRow(t, w, "model/0")
	if !strings.Contains(r.title, "75.00%") {
		t.Fatal(r)
	}
	if w.current() != root || len(f.observed()) != 1 {
		t.Fatal("switching API views should reuse the resource and its measurement")
	}
	click(t, w, "view/api")
	click(t, w, "period")
	click(t, w, "7")
	waitFor(t, w, func() bool { return !root.loading })
	if r, _ := objectRow(t, w, "period"); !strings.Contains(r.title, "近 7 天") {
		t.Fatal(r)
	}
	if r, _ := objectRow(t, w, "balance"); !strings.Contains(r.title, "14 点") {
		t.Fatal(r)
	}
	if _, ok := objectRow(t, w, "unavailable"); !ok {
		t.Fatal(w.view(root))
	}

}
func TestTrendAssemblesSharedScaleAndUnknowns(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	a, b, k := trendLines(domain.PortalUsage{Input: []*int64{n(1), n(1), n(1), nil}, Output: []*int64{n(4), n(4), n(0), n(0)}}, 2)
	if a != "▂?" || b != "█·" || k != 2 {
		t.Fatal(a, b, k)
	}
}

func TestNarrowMetricsExposeCompleteIdentifierAndNumber(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	w.screen.(tcell.SimulationScreen).SetSize(30, 18)
	name := "很长的学校模型名称-with-a-long-suffix"
	amount := "9,007,199,254,740,993"
	r := w.metricRow("record", name, amount, "bupt api leaderboard")
	if uniseg.StringWidth(r.title) > w.metricWidth() || r.open == nil {
		t.Fatal(r)
	}
	r.open()
	var body string
	for _, row := range w.view(w.current()).rows {
		body += row.title
	}
	if !strings.Contains(body, name) || !strings.Contains(body, amount) {
		t.Fatal(body)
	}
	// Summary amounts reflow in place without tiny-value/detail pages.
	var full string
	for _, field := range w.staticMetric("input", "输入 Token", amount) {
		if field.open != nil || uniseg.StringWidth(field.title) > w.metricWidth() {
			t.Fatal(field)
		}
		full += field.title
	}
	if !strings.Contains(full, amount) {
		t.Fatal("truncated count", full)
	}
}

func TestCachedAPIOverviewAndMissingUsagePeriodAllowLocalNavigation(t *testing.T) {
	f := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if m != "portal.read" || p["cached"] != true {
			t.Fatal(m, p)
		}
		if p["section"] == "usage" && p["days"] == float64(30) {
			return map[string]any{"source": "cached", "observed_at": "2026-03-01T12:00:00Z", "warnings": []string{"用量尚未结算"}, "data": map[string]any{"periodCalls": 7}}, nil
		}
		return nil, errors.New("PORTAL_CACHE_MISSING")
	}}
	w := fixtureWorkspace(t, f)
	w.Cached = true
	click(t, w, "api")
	waitFor(t, w, func() bool { return !w.current().loading })
	if w.current().problem != "" || len(f.observed()) != 0 {
		t.Fatal("reading the navigation menu should not need a cached account or model directory")
	}
	w.back()
	click(t, w, "usage")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "period")
	click(t, w, "30")
	waitFor(t, w, func() bool { return !w.current().loading })
	p := w.current()
	v := w.view(p)
	if !strings.Contains(strings.Join(v.facts, " "), "保存于") || !strings.Contains(strings.Join(v.facts, " "), "用量尚未结算") {
		t.Fatal(v)
	}
	click(t, w, "period")
	click(t, w, "7")
	waitFor(t, w, func() bool { return !p.loading })
	if p.problem == "" || len(w.view(p).rows) != 6 || w.view(p).rows[5].key != "period" {
		t.Fatal(p.problem, w.view(p))
	}
	click(t, w, "period")
	click(t, w, "30")
	waitFor(t, w, func() bool { return !p.loading })
	if _, ok := findPortalRow(w.view(p), "calls"); !ok || p.problem != "" {
		t.Fatal(w.view(p), p.problem)
	}
}
func TestLeaderboardRejectsMissingPersonalRankAndHonorsAnonymousAvailability(t *testing.T) {
	f := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if p["section"] == "leaderboard" {
			return map[string]any{"data": map[string]any{"userLeaderboard": []any{}, "modelLeaderboard": []any{}, "me": map[string]any{"ranked": true}}}, nil
		}
		return map[string]any{"data": map[string]any{"mode": "real", "unlocked": true, "options": map[string]any{"anonymous": map[string]any{"available": false}}}}, nil
	}}
	w := fixtureWorkspace(t, f)
	w.leaderboardPage()
	waitFor(t, w, func() bool { return !w.current().loading })
	if w.current().problem == "" {
		t.Fatal("rank zero accepted")
	}
	w.back()
	w.leaderboardPreference()
	waitFor(t, w, func() bool { return !w.current().loading })
	r, ok := findPortalRow(w.view(w.current()), "匿名")
	if !ok || r.open != nil || r.detail != "不可选" {
		t.Fatal(r)
	}
}

func TestUsageIncludesPublishedDatesWithoutMeasurements(t *testing.T) {
	seven := int64(7)
	u := domain.PortalUsage{Input: []*int64{&seven}, Dates: []string{"2026-01-01", "2026-01-02"}}
	in, out, span := trendLines(u, 10)
	if in != "█?" || out != "??" || span != 1 {
		t.Fatal(in, out, span)
	}
	w := fixtureWorkspace(t, &fixtureBackend{})
	r, ok := findPortalRow(w.usageDayRows(u), "day/1")
	if !ok || !strings.Contains(r.title, "2026-01-02") || strings.Count(r.title, "—") != 3 {
		t.Fatal(r)
	}
}

func TestUnifiedLedgerPeriodFilterAndExactPoints(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if m == "portal.read" {
			return map[string]any{"data": map[string]any{"periodCalls": 1, "periodCostPoints": 0}}, nil
		}
		if m != "compute.read" || p["section"] != "billing" {
			return nil, errors.New(m)
		}
		return map[string]any{"total": 4, "items": []map[string]any{
			{"appId": "7", "appName": "课程", "endAt": today, "usagePoints": 0.1}, {"appId": "7", "appName": "课程", "endAt": today, "usagePoints": 0.2},
			{"appId": "7", "appName": "未知日期", "usagePoints": 10}, {"appId": "17", "appName": "其他实例", "endAt": today, "usagePoints": 80},
		}}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.usageWorkspace(7, true, "7")
	p := w.current()
	settle := func() { waitFor(t, w, func() bool { return !p.loading }) }
	settle()
	v := w.view(p)
	if !strings.Contains(strings.Join(v.facts, " "), "2 条") || !strings.Contains(strings.Join(v.facts, " "), "1 条缺少") {
		t.Fatal(v.facts)
	}
	var rows []string
	for _, r := range v.rows {
		rows = append(rows, r.title)
	}
	if got := strings.Join(rows, " "); !strings.Contains(got, "课程") || strings.Contains(got, "其他实例") {
		t.Fatal(got)
	}
	click(t, w, "view/compute")
	if r, _ := objectRow(t, w, "total"); !strings.Contains(r.title, "0.3 积分") {
		t.Fatal(r)
	}
	click(t, w, "view/trend")
	if r, _ := objectRow(t, w, "day/"+today); !strings.Contains(r.title, "0.3 积分") {
		t.Fatal(r)
	}
	click(t, w, "view/api")
	settle()
	if r, _ := objectRow(t, w, "fees"); !strings.Contains(r.title, "0 点") {
		t.Fatal(r)
	}
	click(t, w, "period")
	click(t, w, "custom")
	w.input.value = []rune("12")
	trialKey(w, tcell.KeyEnter)
	settle()
	calls := b.observed()
	if w.current() != p || calls[len(calls)-1].params["days"] != float64(12) {
		t.Fatal(calls)
	}
}
