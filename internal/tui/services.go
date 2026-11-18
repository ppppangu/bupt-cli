package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
	"github.com/google/uuid"
)

func (w *workspace) serviceHome() content {
	api := "模型、余额与密钥"
	current := w.bar.snapshot.Effective(time.Now())
	if current.Platform == "authenticated" && current.Balance != nil {
		api = fmt.Sprintf("余额 %g 点", *current.Balance)
	}
	compute := "资源目录、实例与作业"
	if current.Platform == "authenticated" && current.Running != nil {
		compute = fmt.Sprintf("%d 个实例正在运行", *current.Running)
	}
	return content{rows: []row{
		link("api", "大模型 API 服务", api, "bupt api", w.apiHome),
		link("compute", "裸算力服务", compute, "bupt compute", w.computeHome),
		link("access", navigation.AccountTitle, "学校登录、顶栏设置、公告与协议", "bupt status", func() {
			area, _ := navigation.Find("access")
			w.openArea(area)
		}),
		link("tutorial", "教程说明", "TUI 入门、CLI 与任务指南", "bupt help tutorial", w.tutorials),
	}}
}
func (w *workspace) objectPage(title, cmd string, obj any) {
	raw, _ := json.MarshalIndent(obj, "", "  ")
	w.textPage(title, cmd, string(raw))
}
func ntext(n *float64) string {
	if n == nil {
		return "—"
	}
	return strconv.FormatFloat(*n, 'f', -1, 64)
}
func (w *workspace) apiHome() {
	w.portalPage("overview", "大模型 API 服务", "bupt api", nil)
	w.current().area = "api"
}
func (w *workspace) portalPage(section, title, cmd string, extra map[string]any) {
	if section == "guide" {
		w.integrationExamples()
		return
	}
	switch section {
	case "usage":
		days, _ := extra["days"].(int)
		if days == 0 {
			days = 30
		}
		w.usagePage(days)
		return
	case "leaderboard":
		w.leaderboardPage()
		return
	case "preference":
		w.leaderboardPreference()
		return
	case "news":
		w.newsList()
		return
	case "agreement", "news.show":
		w.portalDocument(section, title, cmd, extra)
		return
	}
	params := map[string]any{"section": section, "cached": w.Cached}
	for k, v := range extra {
		params[k] = v
	}
	p := queryPage(w, title, cmd, "portal.read", params, false, func(v domain.PortalPage) content { return w.portalContent(section, cmd, v) })
	if section == "overview" {
		p.readNavigation = func() content { return w.portalContent(section, cmd, domain.PortalPage{}) }
	}
	w.push(p)
}
func (w *workspace) portalContent(section, cmd string, page domain.PortalPage) content {
	v := content{empty: "当前账号没有可显示的记录。"}
	if !page.ObservedAt.IsZero() {
		v.facts = append(v.facts, label(page.Source)+" · "+moment(page.ObservedAt))
	}
	v.facts = append(v.facts, page.Warnings...)
	switch section {
	case "overview":
		if page.Account != nil {
			v.facts = append(v.facts, "余额 "+ntext(page.Account.Balance)+" 点 · "+strconv.Itoa(len(page.Keys))+" 把 API Key")
		}
		for _, x := range []struct{ id, title, detail string }{{"model", "模型", "ID、能力、上下文、价格和健康状态"}, {"usage", "余额与用量", "按时间和模型查看消耗"}, {"key", "API Key", "模型范围、状态和密钥保管"}, {"guide", "接入示例", "公网 Base URL、选择模型并生成代码"}, {"leaderboard", "排行榜", "平台榜单与本人展示方式"}} {
			sub := "bupt api " + x.id
			v.rows = append(v.rows, link(x.id, x.title, x.detail, sub, func() { w.portalPage(x.id, x.title, sub, nil) }))
		}
		v.rows = append([]row{link("trial", "试用模型", "输入自己的问题或文本，查看实际返回", "bupt help api model try --all", w.chooseTrialModel)}, v.rows...)
		if !w.Cached {
			v.rows = append(v.rows, link("login", "平台登录", "", "bupt login", w.loginMethods))
		}
	case "model":
		for _, m := range page.Models {
			detail := strings.TrimSpace(m.Kind + " · " + m.Context + " · " + ntext(m.InputPrice) + "/" + ntext(m.OutputPrice) + " 点/M · " + m.Health)
			show := cmdline("api", "model", "show", m.ID)
			v.rows = append(v.rows, link(m.ID, m.ID, detail, show, func() {
				w.local(m.Name, show, func() content {
					cv := content{facts: []string{m.ID, m.Description}, rows: []row{info("能力", m.Kind), info("上下文", m.Context), info("输入 / 输出价", ntext(m.InputPrice)+" / "+ntext(m.OutputPrice)+" 点/百万 Token"), info("健康状态", m.Health+" · "+m.Coverage), info("最近采样", m.SampledAt), info("延迟", ntext(m.LatencyMS)+" ms")}}
					if t, e := domain.TrialFor(m); e == nil {
						cv.rows = append([]row{link("try", "试用模型", "输入自己的内容并发送", "bupt help api model try --all", func() { w.modelPlayground(m, t) })}, cv.rows...)
					} else {
						cv.rows = append(cv.rows, info("模型试用", e.Error()))
					}
					if len(m.History) > 0 {
						cv.rows = append(cv.rows, link("history", "历史采样", "", show, func() { w.objectPage("历史采样", show, m.History) }))
					}
					if !w.Cached {
						cv.rows = append(cv.rows, link("create", "为此模型创建 Key", "", cmdline("api", "key", "create", "NAME", "--model", m.ID), func() { w.keyForm([]string{m.ID}) }))
					}
					return cv
				})
			}))
		}
	case "key":
		if !w.Cached {
			v.rows = append(v.rows, link("new", "+ 创建 API Key", "选择模型范围", "bupt api key create NAME --model ID", func() { w.keyForm(nil) }))
		}
		for _, k := range page.Keys {
			show := cmdline("api", "key", "show", k.ID)
			v.rows = append(v.rows, link(k.ID, k.Name, k.Status+" · "+strings.Join(k.Models, ", "), show, func() { w.portalKey(k) }))
		}

	default:
		if section == "account" {
			v.rows = append(v.rows, info("学工号", page.Account.SID), info("姓名", page.Account.Name), info("余额（点）", ntext(page.Account.Balance)))
		}
		for _, k := range orderedKeys(page.Data) {
			data := page.Data[k]
			v.rows = append(v.rows, link(k, k, value(data), cmd, func() { w.objectPage(k, cmd, data) }))
		}
	}
	return v
}
func (w *workspace) portalKey(k domain.PortalKey) {
	show := cmdline("api", "key", "show", k.ID)
	w.local(k.Name, show, func() content {
		v := content{rows: []row{info("ID", k.ID), info("状态", k.Status), info("允许模型", strings.Join(k.Models, ", ")), info("累计用量（点）", ntext(k.Used)), info("创建时间", k.CreatedAt), info("控制器保管", yes(k.Stored))}}
		if len(k.Models) > 0 {
			v.rows = append(v.rows, link("models", "查看授权模型的价格与状态", strings.Join(k.Models, ", "), "bupt api model", func() {
				w.push(queryPage(w, "授权模型", "bupt api model", "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(page domain.PortalPage) content {
					page.Models = slices.DeleteFunc(slices.Clone(page.Models), func(m domain.PortalModel) bool { return !slices.Contains(k.Models, m.ID) })
					return w.portalContent("model", "bupt api model", page)
				}))
			}))
		}
		if w.Cached {
			return v
		}
		for _, op := range []struct{ id, title string }{{"enable", "启用"}, {"disable", "停用"}, {"delete", "删除"}} {
			if op.id == "enable" && k.Status == "active" || op.id == "disable" && k.Status == "disabled" {
				continue
			}
			args := []string{"api", "key", op.id, k.ID}
			p := map[string]any{"action": "key." + op.id, "target": k.ID}
			if op.id == "delete" {
				args = append(args, "--confirm", k.ID)
				p["confirm"] = k.ID
			}
			v.rows = append(v.rows, link(op.id, op.title, k.Name, cmdline(args...), func() {
				w.portalConfirm(op.title+" "+k.Name, args, p, func() { w.portalPage("key", "API Key", "bupt api key", nil) })
			}))
		}
		if k.Stored {
			v.rows = append(v.rows, link("export", "导出到新私有文件", "", cmdline("api", "key", "export", k.ID, "--output", "FILE"), func() {
				w.edit("新文件路径", "", func(path string) error {
					if strings.TrimSpace(path) == "" || path == "-" {
						return fmt.Errorf("请输入新文件路径")
					}
					w.terminalReview("导出 API Key", path, []string{"api", "key", "export", k.ID, "--output", path})
					return nil
				})
			}))
		}
		return v
	})
}
func (w *workspace) keyForm(initial []string) { w.keyFormReturning(initial, nil, nil) }
func (w *workspace) keyFormReturning(initial []string, returnTo *page, done func(map[string]any)) {
	selected := slices.Clone(initial)
	name := ""
	w.push(queryPage(w, "创建 API Key", "bupt api key create NAME --model ID", "portal.read", map[string]any{"section": "model"}, true, func(p domain.PortalPage) content {
		v := content{rows: []row{w.field("name", "用途名称", &name, "例如 科研脚本")}}
		for _, m := range p.Models {
			mark := "□ "
			if slices.Contains(selected, m.ID) {
				mark = "■ "
			}
			v.rows = append(v.rows, link(m.ID, mark+m.ID, m.Name, "", func() {
				if n := slices.Index(selected, m.ID); n >= 0 {
					selected = slices.Delete(selected, n, n+1)
				} else {
					selected = append(selected, m.ID)
				}
			}))
		}
		args := []string{"api", "key", "create", name}
		for _, id := range selected {
			args = append(args, "--model", id)
		}
		v.rows = append(v.rows, link("preview", "确认模型范围", strings.Join(selected, ", "), cmdline(args...), func() {
			if strings.TrimSpace(name) == "" || len(selected) == 0 {
				w.notice = "请填写名称并选择模型"
				return
			}
			after := done
			if after == nil {
				after = func(map[string]any) { w.portalPage("key", "API Key", "bupt api key", nil) }
			}
			w.confirmPortalChange("创建 API Key", args, map[string]any{"action": "key.create", "name": name, "models": selected}, returnTo, after)
		}))
		return v
	}))
}
func (w *workspace) portalConfirm(title string, args []string, params map[string]any, done func()) {
	w.confirmPortalChange(title, args, params, nil, func(map[string]any) { done() })
}
func (w *workspace) confirmPortalChange(title string, args []string, params map[string]any, returnTo *page, done func(map[string]any)) {
	if w.Cached {
		return
	}
	key := uuid.NewString()
	cmd := cmdline(args...)
	w.local(title, cmd, func() content {
		return content{facts: []string{cmd}, rows: []row{link("back", "返回", "", "", w.back), link("submit", "确认"+title, "", cmd, func() {
			p := w.current()
			if p.submitting {
				return
			}
			p.submitting = true
			params["idempotency_key"] = key
			go func() {
				ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
				defer cancel()
				var result map[string]any
				e := w.Backend.Call(ctx, "portal.change", params, &result)
				w.post(w.ctx, func() {
					p.submitting = false
					if e != nil {
						w.notice = whenError(e)
						return
					}
					for len(w.pages) > 1 && w.current() != returnTo && w.current().title != "大模型 API 服务" && w.current().title != navigation.AccountTitle {
						w.back()
					}
					done(result)
					w.notice = "学校已确认操作"
				})
			}()
		})}}
	})
}
