package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
)

var usageViews = []exampleChoice{
	{"overview", "总览"}, {"api", "模型 API"}, {"compute", "裸算力"}, {"trend", "趋势"}, {"detail", "明细"},
}

// Views for one service share their loaded data, period and resource scope.
type usageScreen struct {
	w               *workspace
	page            *page
	days            int
	view, section   string
	ledger, byModel bool
	instance        domain.ID
}

func (w *workspace) usagePage(days int) { w.usageWorkspace(days, false, "") }
func (w *workspace) usageWorkspace(days int, ledger bool, instance domain.ID) {
	p := &page{title: "余额与用量", shortcuts: map[rune]string{}}
	u := &usageScreen{w: w, page: p, days: days, ledger: ledger, instance: instance, view: "overview", section: "billing"}
	if ledger {
		u.view = "compute"
		if instance != "" {
			u.view = "detail"
		}
	}
	for i, v := range usageViews {
		p.shortcuts[rune('1'+i)] = "view/" + v.id
	}
	p.key = "view/" + u.view
	u.configure()
	w.push(p)
}

func (u *usageScreen) selectView(id string) {
	if u.view == id {
		return
	}
	oldSource := u.ledger
	switch id {
	case "overview", "api":
		u.ledger = false
	case "compute":
		u.ledger = true
	}
	u.view = id
	u.page.filter, u.page.key, u.page.reading, u.page.scroll = "", "view/"+id, false, 0
	if oldSource != u.ledger {
		u.reload()
	}
}

func (u *usageScreen) controls() content {
	v := content{fullWidth: true}
	for i, tab := range usageViews {
		title := fmt.Sprintf("%d  %s", i+1, tab.label)
		if tab.id == u.view {
			title += "  · 当前"
		}
		v.rows = append(v.rows, link("view/"+tab.id, title, "", "", func() { u.selectView(tab.id) }))
	}
	v.rows = append(v.rows, link("period", fmt.Sprintf("统计周期  近 %d 天", u.days), "", "", u.pickPeriod))
	if u.view == "trend" || u.view == "detail" {
		label, source := "模型 API", "api"
		if u.ledger {
			label, source = "裸算力", "compute"
		}
		v.rows = append(v.rows, link("source", "数据来源  "+label, "", "", func() {
			u.w.chooseExample("数据来源", []exampleChoice{{"api", "模型 API"}, {"compute", "裸算力"}}, source, func(c exampleChoice) {
				next := c.id == "compute"
				if u.ledger != next {
					u.ledger = next
					u.reload()
				}
			})
		}))
	}
	return v
}

func (u *usageScreen) pickPeriod() {
	w := u.w
	apply := func(n int) {
		if u.days != n {
			u.days = n
			u.reload()
		}
	}
	selected := strconv.Itoa(u.days)
	if u.days != 7 && u.days != 30 {
		selected = "custom"
	}
	w.chooseExample("选择统计周期", []exampleChoice{{"7", "近 7 天"}, {"30", "近 30 天"}, {"custom", "自定义最近天数（1 至 30 天）"}}, selected, func(c exampleChoice) {
		if c.id == "custom" {
			w.edit("统计最近多少天（1 至 30）", strconv.Itoa(u.days), func(raw string) error {
				n, err := strconv.Atoi(strings.TrimSpace(raw))
				if err != nil || n < 1 || n > 30 {
					return fmt.Errorf("请填写 1 至 30 的整数")
				}
				apply(n)
				return nil
			})
			return
		}
		n, _ := strconv.Atoi(c.id)
		apply(n)
	})
}
func (u *usageScreen) reload() {
	u.page.filter = ""
	u.configure()
	u.w.fetchPage(u.page, false)
}
func (u *usageScreen) configure() {
	p, w := u.page, u.w
	p.readNavigation = u.controls
	p.needsSchool = u.ledger
	if u.ledger {
		p.command, p.queryMethod = "bupt compute usage", "compute.read"
		if enabled, known := w.report.ComputeFeatures["billing"]; known && !enabled {
			p.needsSchool = false
			p.load = func(context.Context) (func() content, error) {
				return func() content {
					v := u.controls()
					v.facts = []string{"学校尚未开放裸算力账单。"}
					return v
				}, nil
			}
		} else {
			p.load = u.ledgerLoader()
		}
		return
	}
	p.command, p.queryMethod = fmt.Sprintf("bupt api usage --days %d", u.days), "portal.read"
	p.load = queryPage(w, p.title, p.command, p.queryMethod, map[string]any{"section": "usage", "days": u.days, "cached": w.Cached}, false, u.portalContent).load
}

func (u *usageScreen) portalContent(reply domain.PortalContent[domain.PortalUsage]) content {
	w, data := u.w, reply.Data
	v := u.controls()
	v.facts = portalNotes(reply.PortalObservation)
	if u.view == "overview" || u.view == "api" {
		balance := "—"
		if reply.Account != nil {
			balance = ntext(reply.Account.Balance)
		}
		v.rows = append(v.rows, w.staticMetric("balance", "模型账户余额", balance+" 点")...)
		if reply.Account != nil && reply.Account.FreeQuota != nil {
			v.rows = append(v.rows, w.staticMetric("quota", "模型免费额度", ntext(reply.Account.FreeQuota)+" 点")...)
		}
		fee := "门户未提供"
		if data.CostPoints != nil {
			fee = ntext(data.CostPoints) + " 点"
		}
		v.rows = append(v.rows, w.staticMetric("fees", "本期模型实扣积分", fee)...)
		if u.view == "overview" {
			for _, c := range w.report.Capacity {
				v.rows = append(v.rows, w.staticMetric("quota/"+c.Scope+"/"+c.Resource, "算力配额 · "+c.Resource+"（已用 / 上限）", c.Amounts())...)
			}
		}
	}
	if data.Available != nil && !*data.Available {
		v.rows = append(v.rows, row{key: "unavailable", title: "本期模型用量暂不可用。"})
		return v
	}
	switch u.view {
	case "overview", "api":
		v.rows = append(v.rows, w.staticMetric("calls", "模型调用次数", countText(data.Calls))...)
		v.rows = append(v.rows, w.staticMetric("tokens", "Token 用量", countText(data.Tokens))...)
		if u.view == "api" {
			v.rows = append(v.rows, w.staticMetric("success", "成功率", percent(data.SuccessRate))...)
			v.rows = append(v.rows, w.staticMetric("input-total", "输入 Token", countText(periodSum(data.Input, u.days)))...)
			v.rows = append(v.rows, w.staticMetric("output-total", "输出 Token", countText(periodSum(data.Output, u.days)))...)
		}
	case "trend":
		in, out, span := trendLines(data, max(1, w.metricWidth()-5))
		if span == 0 {
			v.rows = append(v.rows, row{key: "trend", title: "暂无每日用量。"})
			break
		}
		v.rows = append(v.rows, row{key: "trend", title: fmt.Sprintf("每日 Token 趋势 · 每格 %d 日 · ? 缺测", span)}, row{key: "in", title: "入  " + in}, row{key: "out", title: "出  " + out})
		table := w.usageDayRows(data)
		v.rows = append(v.rows, table.rows...)
	case "detail":
		group, by := "日期", "date"
		if u.byModel {
			group, by = "模型", "model"
		}
		v.rows = append(v.rows, link("group", "明细分组  "+group, "", "", func() {
			w.chooseExample("明细分组", []exampleChoice{{"date", "按日期"}, {"model", "按模型"}}, by, func(c exampleChoice) { u.byModel = c.id == "model"; u.page.filter = "" })
		}))
		var table content
		if u.byModel {
			table = w.usageModelRows(data.Models, u.page.command)
		} else {
			table = w.usageDayRows(data)
		}
		v.facts = append(v.facts, "/ 筛选"+group+" · 学校按"+group+"汇总用量")
		if len(table.rows) == 0 {
			v.rows = append(v.rows, row{key: "empty", title: table.empty})
		} else {
			v.rows = append(v.rows, table.rows...)
		}
	}
	return v
}
