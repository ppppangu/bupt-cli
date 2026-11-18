package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

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
		me, _ := findPortalRow(v, "me")
		if !strings.Contains(me.title, "100 名以外") {
			t.Fatal(me)
		}
	}
	click(t, w, "tab")
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
	v := w.view(root)
	for _, id := range []string{"calls", "success", "tokens", "in", "out", "days", "models"} {
		if _, ok := findPortalRow(v, id); !ok {
			t.Fatal(id)
		}
	}
	r, _ := findPortalRow(v, "in")
	if !strings.Contains(r.title, "?·") {
		t.Fatal(r)
	}
	r, _ = findPortalRow(v, "out")
	if !strings.HasSuffix(r.title, "?") {
		t.Fatal(r)
	}
	click(t, w, "days")
	w.screen.(tcell.SimulationScreen).SetSize(34, 16)
	days := w.view(w.current())
	total, _ := findPortalRow(days, "day/2/合计")
	if !strings.HasSuffix(total.title, "—") {
		t.Fatal("missing converted to zero", total)
	}
	w.back()
	click(t, w, "models")
	models := w.view(w.current()).rows
	if len(models) != 2 || models[0].key != "model/0" || !strings.Contains(models[0].title, "75.00%") {
		t.Fatal(models)
	}
	w.back()
	click(t, w, "period")
	waitFor(t, w, func() bool { return !w.current().loading })
	v = w.view(root)
	if w.current() != root || len(v.facts) != 1 || !strings.Contains(v.facts[0], "7 天") {
		t.Fatal("period navigation lost page or balance", v)
	}
	if bal, ok := findPortalRow(v, "balance"); !ok || !strings.Contains(bal.title, "14 点") {
		t.Fatal(bal)
	}
	if _, ok := findPortalRow(v, "unavailable"); !ok {
		t.Fatal(v)
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
	if w.current().problem == "" {
		t.Fatal("missing overview reported success")
	}
	click(t, w, "usage")
	waitFor(t, w, func() bool { return !w.current().loading })
	p := w.current()
	v := w.view(p)
	if !strings.Contains(strings.Join(v.facts, " "), "保存于") || !strings.Contains(strings.Join(v.facts, " "), "用量尚未结算") {
		t.Fatal(v)
	}
	click(t, w, "period")
	waitFor(t, w, func() bool { return !p.loading })
	if p.problem == "" || len(w.view(p).rows) != 1 || w.view(p).rows[0].key != "period" {
		t.Fatal(p.problem, w.view(p))
	}
	click(t, w, "period")
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
	w.usageDays(u, "bupt api usage")
	r, ok := findPortalRow(w.view(w.current()), "day/1")
	if !ok || !strings.Contains(r.title, "2026-01-02") || strings.Count(r.title, "—") != 3 {
		t.Fatal(r)
	}
}
