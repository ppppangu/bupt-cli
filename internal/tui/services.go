package tui

import (
	"cmp"
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
		navLink("api", "R  "+navigation.APITitle, "在线调试，或在自己的程序中调用模型", "bupt api", w.apiHome),
		navLink("compute", "S  "+navigation.ComputeTitle, "创建、连接和管理 CPU / GPU 实例", "bupt compute", w.computeHome),
		navLink("usage", "U  余额与用量", "查看模型用量与服务器账单", "bupt api usage", func() { w.usagePage(7) }),
		navLink("tutorial", "?  "+navigation.HelpTitle, "使用帮助、诊断和问题反馈", "bupt feedback", w.helpHome),
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
	w.push(&page{title: navigation.APITitle, area: "api", command: "bupt api", shortcuts: map[rune]string{'1': "trial", '2': "model", '3': "guide", '4': "key", '5': "leaderboard"}, build: func() content {
		return content{rows: []row{
			navLink("trial", "1  在线调试", "编辑输入，发送并就地查看结果", "bupt help api model invoke --all", w.chooseTrialModel),
			navLink("model", "2  模型与价格", "按用途选模型，查看输入和输出单价", "bupt api model", func() { w.portalPage("model", "模型与价格", "bupt api model", nil) }),
			navLink("guide", "3  在我的程序中调用", "生成 Python 或 curl 代码", "bupt api guide", func() { w.integrationExamples(nil) }),
			navLink("key", "4  API 密钥", "创建密钥、查看授权、启用或停用", "bupt api key", func() { w.portalPage("key", "API 密钥", "bupt api key", nil) }),
			navLink("leaderboard", "5  排行榜", "查看模型热度和用户用量榜", "bupt api leaderboard", w.leaderboardPage),
		}}
	}})
}
func (w *workspace) portalPage(section, title, cmd string, extra map[string]any) {
	if section == "guide" {
		w.integrationExamples(nil)
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

	if section == "model" {
		w.cataloguePage(p, cmd, params)
	}

	if section == "key" && !w.Cached {
		p.shortcuts = map[rune]string{'n': "new"}
		p.nextKey = "new"
	}
	w.push(p)
}
func (w *workspace) cataloguePage(p *page, cmd string, params map[string]any) {
	task, order := exampleChoice{"", "全部任务"}, exampleChoice{"name", "模型名称"}
	p.load = queryPage(w, p.title, cmd, "portal.read", params, false, func(data domain.PortalPage) content {
		all := data.Models
		models := slices.Clone(all)
		if task.id != "" {
			models = supportedModels(models, task.id)
		}
		slices.SortStableFunc(models, func(a, b domain.PortalModel) int {
			if order.id != "name" {
				x, y := a.InputPrice, b.InputPrice
				if order.id == "latency" {
					x, y = a.LatencyMS, b.LatencyMS
				}
				if x != nil && y != nil {
					if c := cmp.Compare(*x, *y); c != 0 {
						return c
					}
				} else if x != nil {
					return -1
				} else if y != nil {
					return 1
				}
			}
			return cmp.Compare(a.ID, b.ID)
		})
		data.Models = models
		v := w.portalContent("model", cmd, data)
		v.rows = append([]row{link("filter-task", "按任务筛选", task.label, "", func() {
			opts := []exampleChoice{{"", "全部任务"}}
			for _, t := range trialTasks {
				if t.kind != "similarity" && len(supportedModels(all, t.kind)) > 0 {
					opts = append(opts, exampleChoice{t.kind, t.title})
				}
			}
			w.chooseExample("按任务筛选", opts, task.id, func(c exampleChoice) { task = c })
		}), link("sort", "排序", order.label, "", func() {
			w.chooseExample("模型排序", []exampleChoice{{"name", "模型名称"}, {"price", "输入价格从低到高"}, {"latency", "延迟从低到高"}}, order.id, func(c exampleChoice) { order = c })
		})}, v.rows...)
		if len(models) == 0 {
			v.rows = append(v.rows, info("模型", "此任务暂无模型"))
		}
		return v
	}).load
}
func (w *workspace) portalContent(section, cmd string, page domain.PortalPage) content {
	v := content{empty: "当前账号没有可显示的记录。", facts: portalNotes(domain.PortalObservation{
		Source: page.Source, ObservedAt: page.ObservedAt, Warnings: page.Warnings,
	})}
	switch section {
	case "model":
		v.empty = "当前目录没有可见模型。可按 r 重新读取。"
		for _, m := range page.Models {
			capability, health := modelLabels(m)
			fields := []string{capability}
			fields = append(fields, "入 "+ntext(m.InputPrice)+" / 出 "+ntext(m.OutputPrice)+" 点/百万 Token", health)
			if m.Context != "" {
				fields = append(fields, "上下文 "+m.Context)
			}
			detail := strings.Join(fields, " · ")
			show := cmdline("api", "model", "show", m.ID)
			r := navLink(m.ID, m.ID, detail, show, func() { w.modelDetail(m) })
			r.aliases = m.Name + " " + m.Vendor
			v.rows = append(v.rows, r)
		}
	case "key":
		v.empty = "当前账号还没有 API Key。"
		for _, k := range page.Keys {
			show := cmdline("api", "key", "show", k.ID)
			v.rows = append(v.rows, navLink(k.ID, cmp.Or(k.Name, k.ID), portalKeySummary(k), show, func() { w.portalKey(k) }))
		}
		if !w.Cached {
			title := "创建 API Key"
			if len(page.Keys) == 0 {
				title = "创建第一把 API Key"
			} else {
				v.facts = append(v.facts, "n 新建密钥")
			}
			v.rows = append(v.rows, navLink("new", title, "为程序或模型试用选择授权范围", "bupt api key create NAME --model ID", func() {
				listPage := w.current()
				w.keyFormReturning(nil, listPage, func(result map[string]any) {
					if id, _ := result["id"].(string); id != "" {
						listPage.filter, listPage.key = "", id
					}
					w.fetchPage(listPage, true)
				})
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
func modelLabels(m domain.PortalModel) (string, string) {
	capability := m.Kind
	if t, err := domain.TrialFor(m); err == nil {
		capability = trialTitle(t.Kind)
	}
	health := m.Health
	switch health {
	case "ok":
		health = "正常"
	case "", "unknown":
		health = "待核验"
	}
	return capability, health
}
func (w *workspace) modelDetail(initial domain.PortalModel) {
	id := initial.ID
	cmd := cmdline("api", "model", "show", id)
	createdKey := ""
	expanded := false
	p := &page{title: cmp.Or(initial.Name, id), command: cmd, queryMethod: "portal.read"}
	render := func(m domain.PortalModel) content {
		if m.ID != id {
			return content{empty: "当前目录已没有此模型。"}
		}
		capability, health := modelLabels(m)
		v := content{facts: []string{id}}
		if m.Description != "" {
			v.facts = append(v.facts, m.Description)
		}
		if t, err := domain.TrialFor(m); err == nil {
			v.rows = append(v.rows, navLink("try", "在线调试", capability, "bupt help api model invoke --all", func() {
				f := w.modelPlayground(m, t)
				if createdKey != "" {
					f.key = createdKey
				}
			}),
				navLink("guide", "用这个模型生成代码", "带入模型和调用密钥", "bupt api guide", func() {
					f := &trialForm{model: m, template: t, input: copyTrial(t.Sample)}
					if stored, ok := w.trialDrafts[w.draftSlot(f)]; ok {
						f = stored.draft.clone()
						f.model = m
						f.template = t
					}
					if createdKey != "" {
						f.key = createdKey
						f.keyName = ""
						f.keyModels = []string{id}
					}
					w.integrationExamples(f)
				}))
		}
		if !w.Cached {
			v.rows = append(v.rows, navLink("create", "为此模型创建密钥", "完成后返回当前模型", cmdline("api", "key", "create", "NAME", "--model", id), func() {
				w.keyFormReturning([]string{id}, p, func(receipt map[string]any) { createdKey, _ = receipt["id"].(string) })
			}))
		}
		v.rows = append(v.rows, info("能力", capability), info("上下文", label(m.Context)), info("输入 / 输出价", ntext(m.InputPrice)+" / "+ntext(m.OutputPrice)+" 点/百万 Token"), info("健康状态", health), info("监测范围", label(m.Coverage)), info("最近采样", label(m.SampledAt)), info("延迟", ntext(m.LatencyMS)+" ms"))
		if len(m.History) > 0 {
			v.rows = append(v.rows, link("history", "历史监测", fmt.Sprintf("%d 个采样时段", len(m.History)), cmd, func() { expanded = !expanded }))
			if expanded {
				v.rows = append(v.rows, link("export-history", "导出监测 JSON", "保存学校返回的采样记录", cmd, func() { raw, _ := json.MarshalIndent(m.History, "", "  "); w.saveSnippet(string(raw)) }))
				for _, sample := range m.History {
					v.rows = append(v.rows, info(portalTime(value(sample["hourStart"])), fmt.Sprintf("%s · 成功 %s / 失败 %s / 缺测 %s", label(value(sample["state"])), label(value(sample["ok"])), label(value(sample["failed"])), label(value(sample["missing"])))))
				}
			}
		}
		return v
	}
	p.build = func() content { return render(initial) }
	p.load = queryPage(w, p.title, cmd, "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(data domain.PortalPage) content {
		for _, m := range data.Models {
			if m.ID == id {
				p.title = cmp.Or(m.Name, id)
				return render(m)
			}
		}
		return content{empty: "当前目录已没有此模型。"}
	}).load
	w.push(p)
}
func portalKeySummary(k domain.PortalKey) string {
	status := label(k.Status)
	switch k.Status {
	case "active":
		status = "已启用"
	case "disabled":
		status = "已停用"
	}
	detail := fmt.Sprintf("%s · %d 个授权模型", status, len(k.Models))
	if k.CreatedAt != "" {
		detail += " · " + portalTime(k.CreatedAt)
	}
	return detail
}

func (w *workspace) portalKey(k domain.PortalKey) {
	listPage := w.current()
	show := cmdline("api", "key", "show", k.ID)
	id := k.ID
	p := &page{title: cmp.Or(k.Name, id), command: show, queryMethod: "portal.read"}
	p.build = func() content {
		usage := "当前控制器未保管完整密钥"
		if k.Stored {
			usage = "已保存在当前控制器"
			if k.Status == "active" && len(k.Models) > 0 {
				usage += "，可用于授权模型"
			}
		}
		v := content{facts: []string{portalKeySummary(k)}, rows: []row{
			info("密钥编号（Key ID）", k.ID), info("累计用量（点）", ntext(k.Used)), info("密钥保管", usage),
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
				if op.id == "delete" {
					w.confirmPortalChange(op.title+" "+k.Name, args, p, listPage, func(map[string]any) { w.fetchPage(listPage, true) })
					return
				}
				detail := w.current()
				w.applyPortalChange(detail, p, func(map[string]any) {
					if op.id == "enable" {
						k.Status = "active"
					} else {
						k.Status = "disabled"
					}
					listPage.stale = true
					w.notice = "已" + op.title + "密钥"
				})
			}))
		}
		if k.Stored {
			v.rows = append(v.rows, link("export", "导出到新私有文件", "", cmdline("api", "key", "export", k.ID, "--file", "FILE"), func() {
				w.edit("新文件路径", "", func(path string) error {
					if strings.TrimSpace(path) == "" || path == "-" {
						return fmt.Errorf("请输入新文件路径")
					}
					w.terminalReview("导出 API Key", path, []string{"api", "key", "export", k.ID, "--file", path}, TerminalAction{Name: "api key export", Args: []string{k.ID}, Options: map[string]string{"file": path}})
					return nil
				})
			}))
		}
		return v
	}
	draw := p.build
	p.load = func(ctx context.Context) (func() content, error) {
		data, e := w.readTrialPage(ctx, "key")
		if e != nil {
			return nil, e
		}
		for _, fresh := range data.Keys {
			if fresh.ID == id {
				applied := false
				return func() content {
					if !applied {
						k = fresh
						p.title = cmp.Or(k.Name, id)
						applied = true
					}
					return draw()
				}, nil
			}
		}
		return func() content { return content{empty: "这把密钥已不在当前账号的列表中。"} }, nil
	}
	w.push(p)
}
func (w *workspace) keyFormReturning(initial []string, returnTo *page, done func(map[string]any)) {
	selected := slices.Clone(initial)
	name := ""
	var form *page
	form = queryPage(w, "创建调用密钥", "bupt api key create NAME --model ID", "portal.read", map[string]any{"section": "model"}, true, func(p domain.PortalPage) content {
		// Launching from a model pins that scope and returns to the calling page.
		fixedScope := len(initial) == 1
		v := content{}
		if len(p.Models) == 0 {
			v.empty = "当前目录没有可授权的模型。按 r 重新读取。"
			return v
		}
		if fixedScope {
			if !slices.ContainsFunc(p.Models, func(m domain.PortalModel) bool { return m.ID == initial[0] }) {
				v.empty = "该模型已不在当前目录。请返回选择其他模型。"
				return v
			}
			v.facts = append(v.facts, "授权模型  "+initial[0]+" · 创建后返回「"+returnTo.title+"」")
		}
		v.facts = append(v.facts, fmt.Sprintf("已选 %d 个模型 · F2 下一步", len(selected)))
		v.rows = append(v.rows, w.field("name", "用途名称", &name, "必填，例如课程实验"))
		args := []string{"api", "key", "create", name}
		for _, id := range selected {
			args = append(args, "--model", id)
		}
		v.rows = append(v.rows, navLink("preview", "下一步，核对授权", fmt.Sprintf("已选 %d 个模型", len(selected)), cmdline(args...), func() {
			if strings.TrimSpace(name) == "" {
				form.filter, form.key = "", "name"
				w.notice = "请先填写用途名称，例如课程实验"
				return
			}
			if len(selected) == 0 {
				w.notice = "请至少选择一个授权模型"
				return
			}
			for _, id := range selected {
				if !slices.ContainsFunc(p.Models, func(m domain.PortalModel) bool { return m.ID == id }) {
					w.notice = "授权范围中有模型已离开目录，请返回重新选择。"
					return
				}
			}
			w.confirmPortalChange("创建调用密钥", args, map[string]any{"action": "key.create", "name": name, "models": slices.Clone(selected)}, returnTo, done, "用途  "+name, "授权模型  "+strings.Join(selected, "、"), "完整密钥将保存在当前控制器。")
		}))
		if !fixedScope {
			for _, m := range p.Models {
				mark, verb := "□ ", "勾选"
				if slices.Contains(selected, m.ID) {
					mark, verb = "■ ", "取消勾选"
				}
				option := link(m.ID, mark+m.ID, m.Name, "", func() {
					if n := slices.Index(selected, m.ID); n >= 0 {
						selected = slices.Delete(selected, n, n+1)
					} else {
						selected = append(selected, m.ID)
					}
				})
				option.verb = verb
				v.rows = append(v.rows, option)
			}
		}
		return v
	})
	form.nextKey = "preview"
	w.popup(form)
}
func (w *workspace) confirmPortalChange(title string, args []string, params map[string]any, returnTo *page, done func(map[string]any), summary ...string) {
	if w.Cached {
		return
	}
	params["idempotency_key"] = uuid.NewString()
	cmd := cmdline(args...)
	switch params["action"] {
	case "key.enable":
		summary = append(summary, "启用后，持有这把 Key 的程序可以调用授权模型。")
	case "key.disable":
		summary = append(summary, "停用后，使用这把 Key 的调用将被拒绝。以后可以重新启用。")
	case "key.delete":
		summary = append(summary, "这把 Key 将被永久删除。使用它的程序需要换用其他 Key。")
	}
	w.local("核对"+title, cmd, func() content {
		submit := link("submit", "确认"+title, "", cmd, func() {
			p := w.current()
			w.applyPortalChange(p, params, func(result map[string]any) {
				for len(w.pages) > 1 && w.current() != returnTo {
					w.back()
				}
				if params["action"] == "key.create" {
					id, _ := result["id"].(string)
					if item := object(result["item"]); id != "" && item["id"] == id && item["stored_in_controller"] == true {
						returnTo.newKeyID = id
					}
				}
				done(result)
				w.notice = "已完成 · " + title
			})
		})
		submit.verb = "确认"
		return content{facts: summary, rows: []row{link("back", "返回", "", "", w.back), submit}}
	})
}

func (w *workspace) applyPortalChange(p *page, params map[string]any, done func(map[string]any)) {
	if w.Cached || p.submitting {
		return
	}
	if params["idempotency_key"] == nil {
		params["idempotency_key"] = uuid.NewString()
	}
	p.submitting = true
	go func() {
		ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
		defer cancel()
		var result map[string]any
		err := w.Backend.Call(ctx, "portal.change", params, &result)
		w.post(w.ctx, func() {
			p.submitting = false
			if err != nil {
				w.notice = whenError(err)
				return
			}
			done(result)
		})
	}()
}
func (w *workspace) revealCreatedKey(origin *page, id string) {
	// Freshly created keys are exported only on explicit reveal. Plaintext lives
	// in this transient frame and is never copied into status, help or a command.
	var p *page
	p = queryPage(w, "新密钥 · 关闭后隐藏", "", "portal.export", map[string]any{"target": id}, true, func(data map[string]string) content {
		secret := data["secret"]
		if secret == "" {
			return content{empty: "控制器没有返回完整密钥。"}
		}
		origin.newKeyID = ""
		p.load = nil
		cols, _ := w.screen.Size()
		lines := panelPreview([]string{secret}, min(76, cols-2)-4, 100)
		return content{facts: lines, rows: []row{link("copy", "复制密钥", "", "", func() { w.screen.SetClipboard([]byte(secret)); w.notice = "已向终端请求复制密钥" }), link("done", "完成", "", "", w.back)}}
	})
	w.popup(p)
}
