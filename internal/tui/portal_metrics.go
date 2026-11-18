package tui

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
	"github.com/rivo/uniseg"
)

func countText(value *int64) string {
	if value == nil {
		return "—"
	}
	s := strconv.FormatInt(*value, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
func percent(v *float64) string {
	if v == nil {
		return "—"
	}
	if *v > 0 && *v < 0.01 {
		return "<0.01%"
	}
	return strconv.FormatFloat(*v, 'f', 2, 64) + "%"
}
func metricLine(width int, name, amount string) string {
	if uniseg.StringWidth(name)+2+uniseg.StringWidth(amount) > width {
		amount = fit(amount, max(6, width/2))
		name = fit(name, max(1, width-uniseg.StringWidth(amount)-2))
	}
	return name + strings.Repeat(" ", max(1, width-uniseg.StringWidth(amount)-uniseg.StringWidth(name))) + amount
}

// Data records can open a complete label and value when the table abbreviates them.
func (w *workspace) metricRow(key, name, amount, cmd string) row {
	return navLink(key, metricLine(w.metricWidth(), name, amount), "", cmd, func() { w.textPage(name, cmd, name+"\n"+amount) })
}
func (w *workspace) metricWidth() int { cols, _ := w.screen.Size(); return max(18, cols-6) }

// Summary values are readable in place, including exact counts on narrow terminals.
func (w *workspace) staticMetric(key, name, amount string) []row {
	width := w.metricWidth()
	if uniseg.StringWidth(name)+2+uniseg.StringWidth(amount) <= width {
		return []row{{key: key, title: metricLine(width, name, amount)}}
	}
	rows := w.textContent(name + "  " + amount).rows
	for i := range rows {
		rows[i].key = fmt.Sprintf("%s/%d", key, i)
	}
	rows[0].key = key
	return rows
}

func (w *workspace) leaderboardPage() {
	const cmd = "bupt api leaderboard"
	tab := "models"
	w.push(queryPage(w, "排行榜", cmd, "portal.read", map[string]any{"section": "leaderboard", "cached": w.Cached}, false, func(page domain.PortalContent[domain.PortalLeaderboard]) content {
		d := page.Data
		v := content{fullWidth: true, facts: portalNotes(page.PortalObservation)}
		width := w.metricWidth()
		if d.Days > 0 {
			v.facts = append(v.facts, fmt.Sprintf("近 %d 天", d.Days))
		}
		current, other := "模型热度榜", "用户用量榜"
		if tab == "users" {
			current, other = other, current
		}
		change := link("tab", current+"  ·  切换到"+other, "", cmd, func() {
			if tab == "models" {
				tab = "users"
			} else {
				tab = "models"
			}
		})
		change.verb = "切到" + other
		v.rows = append(v.rows, change)
		mine := "暂无数据"
		if m := d.Me; m != nil {
			switch {
			case m.Ranked:
				mine = fmt.Sprintf("第 %d 名 · %s Token", m.Rank, countText(m.Tokens))
			case m.Reason == "beyond_top_100":
				mine = "前 100 名以外"
			default:
				mine = "本期未上榜"
			}
		}
		v.rows = append(v.rows, w.staticMetric("me", "我的位置", mine)...)
		if d.Unlocked {
			v.rows = append(v.rows, navLink("preference", "我的榜单展示方式", "", cmd+" display", w.leaderboardPreference))
		}
		if tab == "models" {
			v.rows = append(v.rows, row{key: "heading", title: metricLine(width, "名次  模型", "调用量占比")})
			for i, r := range d.Models {
				v.rows = append(v.rows, w.metricRow(fmt.Sprintf("model/%d", i), fmt.Sprintf("%2d    %s", r.Rank, r.Model), percent(r.Share), cmd))
			}
			if len(d.Models) == 0 {
				v.rows = append(v.rows, row{key: "empty", title: "暂无模型排行。"})
			}
		} else {
			v.rows = append(v.rows, row{key: "heading", title: metricLine(width, "名次  用户", "Token")})
			for i, r := range d.Users {
				v.rows = append(v.rows, w.metricRow(fmt.Sprintf("user/%d", i), fmt.Sprintf("%2d    %s", r.Rank, r.Label), countText(r.Tokens), cmd))
			}
			if len(d.Users) == 0 {
				v.rows = append(v.rows, row{key: "empty", title: "暂无用户排行。"})
			}
		}
		return v
	}))
}

func (w *workspace) leaderboardPreference() {
	const cmd = "bupt api leaderboard display"
	var page *page
	page = queryPage(w, "我的榜单展示方式", cmd, "portal.read", map[string]any{"section": "preference", "cached": w.Cached}, false, func(r domain.PortalContent[domain.LeaderboardPreference]) content {
		d := r.Data
		v := content{facts: portalNotes(r.PortalObservation)}
		for _, m := range []struct{ id, title string }{{"anonymous", "匿名"}, {"partial", "脱敏"}, {"real", "实名"}} {
			opt, published := d.Options[m.id]
			desc := opt.Preview
			if m.id == d.Mode {
				v.rows = append(v.rows, info(m.title, "当前 · "+desc))
				continue
			}
			if w.Cached || published && !opt.Available || m.id != "anonymous" && (!d.Unlocked || !published) {
				v.rows = append(v.rows, info(m.title, "不可选"))
				continue
			}
			args := []string{"api", "leaderboard", "display", "set", m.id}
			v.rows = append(v.rows, link(m.id, m.title, desc, cmdline(args...), func() {
				w.confirmPortalChange("更新榜单展示为"+m.title, args, map[string]any{"action": "preference.set", "mode": m.id}, page, func(map[string]any) { w.fetchPage(page, true) })
			}))
		}
		return v
	})
	w.push(page)
}
func at(series []*int64, i int) *int64 {
	if i < len(series) {
		return series[i]
	}
	return nil
}
func bucketLabel(u domain.PortalUsage, i int) string {
	if i < len(u.Dates) && u.Dates[i] != "" {
		return u.Dates[i]
	}
	return fmt.Sprintf("第%d日", i+1)
}
func combined(a, b *int64) *int64 {
	if a == nil || b == nil || *a > math.MaxInt64-*b {
		return nil
	}
	n := *a + *b
	return &n
}

func periodSum(series []*int64, days int) *int64 {
	if len(series) != days {
		return nil
	}
	var n int64
	for _, v := range series {
		if v == nil || n > math.MaxInt64-*v {
			return nil
		}
		n += *v
	}
	return &n
}

// Use one linear scale for both series. A dot means a known zero and a
// question mark means the school supplied no measurement for that bucket.
func trendLines(u domain.PortalUsage, width int) (string, string, int) {
	n := max(len(u.Input), len(u.Output), len(u.Dates))
	if n == 0 {
		return "", "", 0
	}
	span := (n + width - 1) / width
	buckets := func(series []*int64) []*int64 {
		var out []*int64
		for i := 0; i < n; i += span {
			var sum int64
			valid := true
			for j := i; j < min(i+span, n); j++ {
				v := at(series, j)
				if v == nil {
					valid = false
					break
				}
				if *v < 0 || sum > math.MaxInt64-*v {
					valid = false
					break
				}
				sum += *v
			}
			if valid {
				out = append(out, &sum)
			} else {
				out = append(out, nil)
			}
		}
		return out
	}
	a, b := buckets(u.Input), buckets(u.Output)
	var peak int64
	for _, set := range [][]*int64{a, b} {
		for _, v := range set {
			if v != nil {
				peak = max(peak, *v)
			}
		}
	}
	draw := func(values []*int64) string {
		var s strings.Builder
		levels := []rune("▁▂▃▄▅▆▇█")
		for _, v := range values {
			switch {
			case v == nil:
				s.WriteByte('?')
			case *v <= 0:
				s.WriteRune('·')
			default:
				i := int(math.Ceil(float64(*v)/float64(peak)*8)) - 1
				s.WriteRune(levels[min(7, max(0, i))])
			}
		}
		return s.String()
	}
	return draw(a), draw(b), span
}
func (w *workspace) usagePage(days int) {
	p := &page{title: "余额与用量", queryMethod: "portal.read"}
	var selectPeriod func(int)
	selectPeriod = func(period int) {
		cmd := fmt.Sprintf("bupt api usage --days %d", period)
		p.command = cmd
		other := 7
		if period == 7 {
			other = 30
		}
		changePeriod := link("period", fmt.Sprintf("统计周期  近 %d 天 · 切换至近 %d 天", period, other), "", fmt.Sprintf("bupt api usage --days %d", other), func() { selectPeriod(other); w.fetchPage(p, false) })
		changePeriod.verb = fmt.Sprintf("切换至 %d 天", other)
		p.readNavigation = func() content {
			return content{fullWidth: true, rows: []row{changePeriod}}
		}
		load := queryPage(w, p.title, cmd, "portal.read", map[string]any{"section": "usage", "days": period, "cached": w.Cached}, false, func(reply domain.PortalContent[domain.PortalUsage]) content {
			u := reply.Data
			v := content{fullWidth: true, facts: portalNotes(reply.PortalObservation), rows: []row{changePeriod}}
			balance := "—"
			if reply.Account != nil {
				balance = ntext(reply.Account.Balance)
			}
			v.rows = append(v.rows, w.staticMetric("balance", "账户余额", balance+" 点")...)
			if u.Available != nil && !*u.Available {
				v.rows = append(v.rows, row{key: "unavailable", title: "本期用量暂不可用。"})
				return v
			}
			width := w.metricWidth()
			for _, k := range []struct{ id, title, amount string }{{"calls", "调用次数", countText(u.Calls)}, {"success", "成功率", percent(u.SuccessRate)}, {"tokens", "总 Token", countText(u.Tokens)}, {"input-total", "输入 Token", countText(periodSum(u.Input, period))}, {"output-total", "输出 Token", countText(periodSum(u.Output, period))}} {
				v.rows = append(v.rows, w.staticMetric(k.id, k.title, k.amount)...)
			}
			input, output, span := trendLines(u, max(1, width-5))
			if span > 0 {
				v.rows = append(v.rows, row{key: "trend", title: fmt.Sprintf("每日趋势 · 每格 %d 日 · ? 缺测", span)}, row{key: "in", title: "入  " + input}, row{key: "out", title: "出  " + output}, navLink("days", "查看每日输入与输出", "", cmd, func() { w.usageDays(u, cmd) }))
			} else {
				v.rows = append(v.rows, row{key: "trend", title: "暂无每日用量。"})
			}
			v.rows = append(v.rows, navLink("models", "查看各模型用量占比", "", cmd, func() { w.usageModels(u.Models, cmd) }))
			return v
		})
		p.load = load.load
	}
	selectPeriod(days)
	w.push(p)
}
func (w *workspace) usageDays(u domain.PortalUsage, cmd string) {
	w.local("每日用量", cmd, func() content {
		v := content{fullWidth: true, empty: "暂无每日记录。"}
		width := w.metricWidth()
		lines := [][4]string{{"日期/序号", "输入", "输出", "合计"}}
		var widths [4]int
		for i := max(len(u.Input), len(u.Output), len(u.Dates)) - 1; i >= 0; i-- {
			in, out := at(u.Input, i), at(u.Output, i)
			lines = append(lines, [4]string{bucketLabel(u, i), countText(in), countText(out), countText(combined(in, out))})
		}
		if len(lines) == 1 {
			return v
		}
		for _, line := range lines {
			for i, cell := range line {
				widths[i] = max(widths[i], uniseg.StringWidth(cell))
			}
		}
		wide := widths[0]+widths[1]+widths[2]+widths[3]+6 <= width
		for i, line := range lines {
			key := fmt.Sprintf("day/%d", i)
			if wide {
				var text strings.Builder
				for col, cell := range line {
					pad := strings.Repeat(" ", widths[col]-uniseg.StringWidth(cell))
					if col == 0 {
						text.WriteString(cell + pad)
					} else {
						text.WriteString("  " + pad + cell)
					}
				}
				v.rows = append(v.rows, row{key: key, title: text.String()})
			} else if i > 0 {
				for j, r := range w.textContent(line[0]).rows {
					r.key = fmt.Sprintf("%s/date/%d", key, j)
					v.rows = append(v.rows, r)
				}
				for col, header := range []string{"输入", "输出", "合计"} {
					v.rows = append(v.rows, w.staticMetric(key+"/"+header, header, line[col+1])...)
				}
			}
		}
		return v
	})
}
func (w *workspace) usageModels(records []domain.ModelConsumption, cmd string) {
	models := slices.Clone(records)
	sort.SliceStable(models, func(i, j int) bool {
		if models[j].Value == nil {
			return models[i].Value != nil
		}
		return models[i].Value != nil && *models[i].Value > *models[j].Value
	})
	total := float64(0)
	complete := true
	for _, m := range models {
		if m.Value == nil {
			complete = false
		} else {
			total += float64(*m.Value)
		}
	}
	w.local("模型用量", cmd, func() content {
		v := content{fullWidth: true, empty: "暂无按模型划分的用量。"}
		width := w.metricWidth()
		for i, m := range models {
			share := "—"
			if complete && total > 0 {
				p := float64(*m.Value) * 100 / total
				share = percent(&p)
			}
			measure := countText(m.Value) + " · " + share
			v.rows = append(v.rows, navLink(fmt.Sprintf("model/%d", i), metricLine(width, m.Name, measure), "", cmd, func() {
				w.textPage(m.Name, cmd, "模型  "+m.Name+"\nToken  "+countText(m.Value)+"\n消耗占比  "+share)
			}))
		}
		return v
	})
}
