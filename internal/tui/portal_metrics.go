package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

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
		current, other := "模型使用热度", "用户用量榜"
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
		if tab == "models" {
			v.rows = append(v.rows, row{key: "heading", title: metricLine(width, "名次  模型", "调用量占比")})
			for i, r := range d.Models {
				v.rows = append(v.rows, w.metricRow(fmt.Sprintf("model/%d", i), fmt.Sprintf("%2d    %s", r.Rank, r.Model), percent(r.Share), cmd))
			}
			if len(d.Models) == 0 {
				v.rows = append(v.rows, row{key: "empty", title: "暂无模型排行。"})
			}
		} else {
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
			if d.Me != nil && d.Me.Ranked {
				if i := slices.IndexFunc(d.Users, func(r domain.UserRank) bool { return r.Rank == d.Me.Rank }); i >= 0 {
					v.rows = append(v.rows, link("locate", "定位我的排名", "", "", func() { w.current().key = fmt.Sprintf("user/%d", i); w.current().filter = "" }))
				}
			}
			if d.Unlocked {
				v.rows = append(v.rows, navLink("preference", "我的榜单展示方式", "", cmd+" display", w.leaderboardPreference))
			}
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
				params := map[string]any{"action": "preference.set", "mode": m.id}
				done := func(map[string]any) { w.fetchPage(page, true); w.notice = "已更新为" + m.title }
				if m.id == "real" {
					w.confirmPortalChange("在榜单显示实名", args, params, page, done, "公开范围  用户用量排行榜")
					return
				}
				w.applyPortalChange(page, params, done)
			}))
		}
		return v
	})
	w.popup(page)
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

// The billing service paginates per verified school account. Keep unknown dates
// visible and separate from the selected period's arithmetic.
func (u *usageScreen) ledgerLoader() func(context.Context) (func() content, error) {
	w := u.w
	section, days, instance := u.section, u.days, u.instance
	return func(ctx context.Context) (func() content, error) {
		var records []map[string]any
		var previous string
		for page := 1; ; page++ {
			var reply struct {
				Items []map[string]any
				Total json.Number
			}
			var raw json.RawMessage
			if err := w.Backend.Call(ctx, "compute.read", map[string]any{"section": section, "page": page, "limit": 100}, &raw); err != nil {
				return nil, err
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if err := decoder.Decode(&reply); err != nil {
				return nil, err
			}
			signature := string(domain.Result(reply.Items))
			if page > 1 && signature == previous && len(reply.Items) > 0 {
				return nil, fmt.Errorf("账单分页重复，请刷新重试")
			}
			records = append(records, reply.Items...)
			previous = signature
			n, err := reply.Total.Int64()
			if err == nil && int64(page*100) >= n || err != nil && len(reply.Items) < 100 || len(reply.Items) == 0 {
				break
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		now := time.Now()
		start := now.AddDate(0, 0, 1-days).Format("2006-01-02")
		end := now.Format("2006-01-02")
		var details, undated []row
		missing := 0
		totals := map[string]*big.Rat{}
		precision := 0
		var runtimeSeconds big.Int
		runtimeKnown := true
		complete := true
		count := 0
		for i, m := range records {
			if instance != "" && computeValue(m, "appId") != string(instance) {
				continue
			}
			raw := computeValue(m, "endAt", "createdAt", "startAt", "updatedAt")
			date := computeTime(raw)
			dated := len(date) >= 10
			if dated {
				date = date[:10]
				_, err := time.Parse("2006-01-02", date)
				dated = err == nil
			}
			if dated && (date < start || date > end) {
				continue
			}
			if !dated {
				date = "日期待核实"
				missing++
			} else {
				count++
				if seconds, ok := new(big.Int).SetString(value(m["runningSeconds"]), 10); ok && seconds.Sign() >= 0 {
					runtimeSeconds.Add(&runtimeSeconds, seconds)
				} else {
					runtimeKnown = false
				}
				points, digits := billingPoints(value(m["usagePoints"]))
				if points == nil {
					complete = false
				} else {
					if totals[date] == nil {
						totals[date] = new(big.Rat)
					}
					totals[date].Add(totals[date], points)
					precision = max(precision, digits)
				}
			}
			name := computeValue(m, "appName", "name", "nodeName", "appId", "id")
			lines := recordSummary(m)
			summary := strings.Join(lines, " · ")
			amount := computeValue(m, "usagePoints")
			if amount != "" {
				amount += " 积分"
			} else {
				amount = strings.TrimSpace(computeValue(m, "cost", "amount", "sum") + " " + computeValue(m, "currency"))
			}
			r := navLink(fmt.Sprintf("bill/%d", i), strings.TrimSpace(date+"  "+name+"  "+amount), summary, "bupt compute usage", func() {
				w.local(name, "bupt compute usage", func() content { return w.textContent(strings.Join(lines, "\n")) })
			})
			r.aliases = name + " " + computeValue(m, "appId") + " " + summary
			if dated {
				details = append(details, r)
			} else {
				undated = append(undated, r)
			}
		}
		if len(undated) > 0 {
			details = append(details, info("结算日期待核实", ""))
			details = append(details, undated...)
		}
		return func() content {
			v := u.controls()
			opts := []exampleChoice{{"billing", "实例费用"}, {"billing.storage", "存储费用"}, {"billing.account", "账户管理账单"}, {"billing.history", "账户流水"}}
			if enabled, known := w.report.ComputeFeatures["hpc"]; !known || enabled {
				opts = append(opts, exampleChoice{"billing.hpc", "调度账单"})
			}
			if enabled, known := w.report.ComputeFeatures["prepayment"]; !known || enabled {
				opts = append(opts, exampleChoice{"billing.prepayment", "预付费记录"})
			}
			which := ""
			for _, o := range opts {
				if o.id == section {
					which = o.label
				}
			}
			v.rows = append(v.rows, link("category", "账单类型  "+which, "", "", func() {
				w.chooseExample("账单类型", opts, section, func(c exampleChoice) {
					if u.section != c.id {
						u.section = c.id
						u.reload()
					}
				})
			}))
			v.facts = []string{fmt.Sprintf("%s 至 %s · %d 条%s", start, end, count, which)}
			if instance != "" {
				v.facts = append(v.facts, "实例 "+string(instance))
			}
			if missing > 0 {
				v.facts = append(v.facts, fmt.Sprintf("另有 %d 条缺少结算日期，单列展示。", missing))
			}
			switch u.view {
			case "trend":
				if count > 0 && complete {
					for _, d := range orderedKeys(totals) {
						v.rows = append(v.rows, w.staticMetric("day/"+d, d, totals[d].FloatString(precision)+" 积分")...)
					}
				} else if count == 0 {
					v.rows = append(v.rows, info("本期没有可用于趋势的已注明日期的账单", ""))
				} else {
					v.rows = append(v.rows, info("学校账单缺少完整的每日积分", ""))
				}
			case "detail":
				v.facts = append(v.facts, "/ 筛选日期、实例名称或编号")
				if len(details) == 0 {
					v.rows = append(v.rows, info("本期没有账单记录", ""))
				} else {
					v.rows = append(v.rows, details...)
				}
			default:
				if count > 0 && complete {
					sum := new(big.Rat)
					for _, d := range orderedKeys(totals) {
						sum.Add(sum, totals[d])
					}
					v.rows = append(v.rows, w.staticMetric("total", "本期计量", sum.FloatString(precision)+" 积分")...)
				}
				if count > 0 && section == "billing" {
					elapsed := "账单未提供完整时长"
					if runtimeKnown {
						elapsed = runtimeSeconds.String() + " 秒"
					}
					v.rows = append(v.rows, w.staticMetric("runtime", "已结算的实例运行时长", elapsed)...)
				}
				v.rows = append(v.rows, link("records", "查看这些账单的明细", "", "bupt compute usage", func() { u.selectView("detail") }))
			}
			return v
		}, nil
	}
}
func billingPoints(raw string) (*big.Rat, int) {
	n, e := strconv.ParseFloat(raw, 64)
	if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || len(raw) > 128 {
		return nil, 0
	}
	number, exponent, _ := strings.Cut(strings.ToLower(raw), "e")
	power, _ := strconv.Atoi(exponent)
	if power < -324 || power > 308 {
		return nil, 0
	}
	_, fraction, _ := strings.Cut(number, ".")
	amount, ok := new(big.Rat).SetString(raw)
	if !ok {
		return nil, 0
	}
	return amount, max(0, len(fraction)-power)
}
func (w *workspace) usageDayRows(u domain.PortalUsage) content {
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
				r.aliases = line[0]
				v.rows = append(v.rows, r)
			}
			for col, header := range []string{"输入", "输出", "合计"} {
				cells := w.staticMetric(key+"/"+header, header, line[col+1])
				for j := range cells {
					cells[j].aliases = line[0]
				}
				v.rows = append(v.rows, cells...)
			}
		}
	}
	return v
}
func (w *workspace) usageModelRows(records []domain.ModelConsumption, cmd string) content {
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

	v := content{fullWidth: true, empty: "暂无按模型划分的用量。"}
	width := w.metricWidth()
	for i, m := range models {
		share := "—"
		if complete && total > 0 {
			p := float64(*m.Value) * 100 / total
			share = percent(&p)
		}
		measure := countText(m.Value) + " Token · " + share
		r := navLink(fmt.Sprintf("model/%d", i), metricLine(width, m.Name, measure), "", cmd, func() {
			w.textPage(m.Name, cmd, "模型  "+m.Name+"\nToken  "+countText(m.Value)+"\nToken 用量占比  "+share)
		})
		r.aliases = m.Name
		v.rows = append(v.rows, r)
	}
	return v
}
