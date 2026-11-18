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
	return content{rows: []row{
		navLink("api", "大模型 API 服务", "向模型提问、接入自己的程序、查看用量", "bupt api", w.apiHome),
		navLink("compute", "裸算力服务", "申请计算资源、连接实例、运行作业", "bupt compute", w.computeHome),
		navLink("access", navigation.AccountTitle, "学校登录、顶栏设置、公告与协议", "bupt status", func() {
			area, _ := navigation.Find("access")
			w.openArea(area)
		}),
		navLink("tutorial", "教程说明", "首次使用和常见任务，可离线阅读", "bupt help tutorial", w.tutorials),
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
	v := content{empty: "当前账号没有可显示的记录。", facts: portalNotes(domain.PortalObservation{
		Source: page.Source, ObservedAt: page.ObservedAt, Warnings: page.Warnings,
	})}
	switch section {
	case "overview":
		for _, x := range []struct{ id, title, detail string }{{"model", "模型", "ID、能力、上下文、价格和健康状态"}, {"usage", "余额与用量", "按时间和模型查看消耗"}, {"key", "API Key", "模型范围、状态和密钥保管"}, {"guide", "接入示例", "公网 Base URL、选择模型并生成代码"}, {"leaderboard", "排行榜", "平台榜单与本人展示方式"}} {
			sub := "bupt api " + x.id
			v.rows = append(v.rows, navLink(x.id, x.title, x.detail, sub, func() { w.portalPage(x.id, x.title, sub, nil) }))
		}
		v.rows = append([]row{navLink("trial", "试用模型", "问问题、比较文本或排序", "bupt help api model try --all", w.chooseTrialModel)}, v.rows...)
	case "model":
		for _, m := range page.Models {
			capability := m.Kind
			t, trialErr := domain.TrialFor(m)
			if trialErr == nil {
				capability = trialTitle(t.Kind)
			}
			health := m.Health
			switch health {
			case "ok":
				health = "正常"
			case "", "unknown":
				health = "待核验"
			}
			fields := []string{capability}
			if m.Context != "" {
				fields = append(fields, m.Context)
			}
			fields = append(fields, ntext(m.InputPrice)+"/"+ntext(m.OutputPrice)+" 点/百万 Token", health)
			detail := strings.Join(fields, " · ")
			show := cmdline("api", "model", "show", m.ID)
			v.rows = append(v.rows, navLink(m.ID, m.ID, detail, show, func() {
				title := m.Name
				if title == "" {
					title = m.ID
				}
				w.local(title, show, func() content {
					cv := content{facts: []string{m.ID}, rows: []row{info("能力", capability), info("上下文", label(m.Context)), info("输入 / 输出价", ntext(m.InputPrice)+" / "+ntext(m.OutputPrice)+" 点/百万 Token"), info("健康状态", health), info("监测范围", label(m.Coverage)), info("最近采样", label(m.SampledAt)), info("延迟", ntext(m.LatencyMS)+" ms")}}
					if m.Description != "" {
						cv.facts = append(cv.facts, m.Description)
					}
					if trialErr == nil {
						cv.rows = append([]row{navLink("try", "试用模型", trialTitle(t.Kind), "bupt help api model try --all", func() { w.modelPlayground(m, t) })}, cv.rows...)
					} else {
						cv.rows = append(cv.rows, info("模型试用", trialErr.Error()))
					}
					if len(m.History) > 0 {
						cv.rows = append(cv.rows, navLink("history", "历史采样", "", show, func() { w.objectPage("历史采样", show, m.History) }))
					}
					if !w.Cached {
						cv.rows = append(cv.rows, navLink("create", "为此模型创建 Key", "填写用途并核对授权范围", cmdline("api", "key", "create", "NAME", "--model", m.ID), func() { w.keyForm([]string{m.ID}) }))
					}
					return cv
				})
			}))
		}
	case "key":
		v.empty = "当前账号还没有 API Key。"
		for _, k := range page.Keys {
			show := cmdline("api", "key", "show", k.ID)
			v.rows = append(v.rows, navLink(k.ID, k.Name, portalKeySummary(k), show, func() { w.portalKey(k) }))
		}
		if !w.Cached {
			title := "创建 API Key"
			if len(page.Keys) == 0 {
				title = "创建第一把 API Key"
			}
			v.rows = append(v.rows, navLink("new", title, "为程序或模型试用选择授权范围", "bupt api key create NAME --model ID", func() {
				listPage := w.current()
				w.keyFormReturning(nil, listPage, func(map[string]any) { w.fetchPage(listPage, true) })
			}))
		}
	case "account":
		a := page.Account
		if a == nil {
			v.empty = "门户尚未返回账号信息。"
			break
		}
		idLabel, id := "账号标识", a.SID
		if number := domain.CampusNumber(a.SID); number != "" {
			idLabel, id = "学工号", number
		}
		v.rows = append(v.rows, info(idLabel, id), info("姓名", a.Name), info("余额（点）", ntext(a.Balance)))
		if a.College != "" {
			v.rows = append(v.rows, info("院系", a.College))
		}
		if a.ConsentRequired && (w.Cached || !w.bar.snapshot.Effective(time.Now()).ConsentRequired) {
			v.rows = append(v.rows, navLink("agreement", "阅读待确认的服务协议", "", "bupt agreement", func() {
				w.reviewRequiredAgreement(w.current())
			}))
		}
	default:
		for _, k := range orderedKeys(page.Data) {
			data := page.Data[k]
			v.rows = append(v.rows, navLink(k, k, value(data), cmd, func() { w.objectPage(k, cmd, data) }))
		}
	}
	return v
}
func portalKeySummary(k domain.PortalKey) string {
	status := label(k.Status)
	switch k.Status {
	case "active":
		status = "已启用"
	case "disabled":
		status = "已停用"
	}
	return fmt.Sprintf("%s · %d 个授权模型", status, len(k.Models))
}

func (w *workspace) portalKey(k domain.PortalKey) {
	listPage := w.current()
	show := cmdline("api", "key", "show", k.ID)
	w.local(k.Name, show, func() content {
		v := content{facts: []string{portalKeySummary(k)}, rows: []row{
			info("Key ID", k.ID), info("累计用量（点）", ntext(k.Used)), info("控制器保管", yes(k.Stored)),
		}}
		if k.CreatedAt != "" {
			v.rows = append(v.rows, info("创建时间", portalTime(k.CreatedAt)))
		}
		if len(k.Models) > 0 {
			v.rows = append(v.rows, navLink("models", "查看授权模型", "模型 ID、价格和状态", "bupt api model", func() {
				p := queryPage(w, "授权模型", "bupt api model", "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(page domain.PortalPage) content {
					page.Models = slices.DeleteFunc(slices.Clone(page.Models), func(m domain.PortalModel) bool { return !slices.Contains(k.Models, m.ID) })
					models := w.portalContent("model", "bupt api model", page)
					for _, id := range k.Models {
						if !slices.ContainsFunc(page.Models, func(m domain.PortalModel) bool { return m.ID == id }) {
							models.rows = append(models.rows, info(id, "已授权，当前目录未列出"))
						}
					}
					return models
				})
				p.readNavigation = func() content { return w.textContent(strings.Join(k.Models, "\n")) }
				w.push(p)
			}))
		}
		if w.Cached {
			return v
		}
		for _, op := range []struct{ id, title, effect string }{
			{"enable", "启用", "允许用这把 Key 调用授权模型"},
			{"disable", "停用", "暂停调用，以后可以重新启用"},
			{"delete", "删除", "永久删除这把 Key"},
		} {
			if op.id == "enable" && k.Status == "active" || op.id == "disable" && k.Status == "disabled" {
				continue
			}
			args := []string{"api", "key", op.id, k.ID}
			p := map[string]any{"action": "key." + op.id, "target": k.ID}
			if op.id == "delete" {
				args = append(args, "--confirm", k.ID)
				p["confirm"] = k.ID
			}
			v.rows = append(v.rows, link(op.id, op.title+"…", op.effect, cmdline(args...), func() {
				w.confirmPortalChange(op.title+" "+k.Name, args, p, listPage, func(map[string]any) { w.fetchPage(listPage, true) })
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
		// Launching from a model pins that scope and returns to the calling page.
		fixedScope := returnTo != nil && len(initial) == 1
		v := content{}
		if fixedScope {
			if !slices.ContainsFunc(p.Models, func(m domain.PortalModel) bool { return m.ID == initial[0] }) {
				v.empty = "该模型已不在当前目录。请返回选择其他模型。"
				return v
			}
			v.facts = []string{"授权模型  " + initial[0], "创建后回到「" + returnTo.title + "」。"}
		}
		v.rows = append(v.rows, w.field("name", "用途名称", &name, "必填，例如课程实验"))
		if !fixedScope {
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
		}
		args := []string{"api", "key", "create", name}
		for _, id := range selected {
			args = append(args, "--model", id)
		}
		v.rows = append(v.rows, navLink("preview", "核对并创建", strings.Join(selected, "、"), cmdline(args...), func() {
			if strings.TrimSpace(name) == "" || len(selected) == 0 {
				w.notice = "请填写名称并选择模型"
				return
			}
			for _, id := range selected {
				if !slices.ContainsFunc(p.Models, func(m domain.PortalModel) bool { return m.ID == id }) {
					w.notice = "授权范围中有模型已离开目录，请返回重新选择。"
					return
				}
			}
			after := done
			if after == nil {
				after = func(map[string]any) { w.portalPage("key", "API Key", "bupt api key", nil) }
			}
			w.confirmPortalChange("创建 API Key", args, map[string]any{"action": "key.create", "name": name, "models": slices.Clone(selected)}, returnTo, after, "用途  "+name, "授权模型  "+strings.Join(selected, "、"), "完整密钥将保存在当前控制器。")
		}))
		return v
	}))
}
func (w *workspace) confirmPortalChange(title string, args []string, params map[string]any, returnTo *page, done func(map[string]any), summary ...string) {
	if w.Cached {
		return
	}
	key := uuid.NewString()
	cmd := cmdline(args...)
	switch params["action"] {
	case "key.enable":
		summary = append(summary, "启用后，持有这把 Key 的程序可以调用授权模型。")
	case "key.disable":
		summary = append(summary, "停用后，使用这把 Key 的调用将被拒绝。以后可以重新启用。")
	case "key.delete":
		summary = append(summary, "这把 Key 将被永久删除。使用它的程序需要换用其他 Key。")
	}
	w.local(title, cmd, func() content {
		submit := link("submit", "确认"+title, "", cmd, func() {
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
					w.notice = "已完成 · " + title
				})
			}()
		})
		submit.verb = "确认"
		return content{facts: summary, rows: []row{link("back", "返回", "", "", w.back), submit}}
	})
}
