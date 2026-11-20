package tui

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/feedback"
	"github.com/google/uuid"
)

type trialForm struct {
	models     []domain.PortalModel
	model      domain.PortalModel
	template   domain.ModelTrial
	input      domain.TrialInput
	key        string
	keyName    string
	keyModels  []string
	external   bool
	similarity bool
}
type trialOptions struct {
	keys       []domain.PortalKey
	keyProblem string
}

func copyTrial(in domain.TrialInput) domain.TrialInput {
	in.Texts = slices.Clone(in.Texts)
	in.Documents = slices.Clone(in.Documents)
	return in
}
func (f *trialForm) args(send bool, options ...string) []string {
	return f.input.ModelArgs(f.model.ID, f.key, send, options...)
}

func (f *trialForm) firstInput() string {
	switch f.template.Kind {
	case "embedding":
		return "item-0"
	case "rerank":
		return "query"
	default:
		return "prompt"
	}
}

func (f *trialForm) saveQuestion(text string) { f.input.Texts[0] = text }

var trialTasks = []struct{ kind, title, detail string }{
	{"chat", "对话", "输入问题，获取模型回复"},
	{"embedding", "文本向量", "将一段或多段文本转换为向量"},
	{"similarity", "语义相似度", "取两段文本的向量，在本机计算余弦相似度"},
	{"rerank", "重排序", "按问题与各候选段落的相关性排序"},
}

func trialTitle(kind string) string {
	for _, task := range trialTasks {
		if task.kind == kind {
			return task.title
		}
	}
	return "在线调试"
}
func supportedModels(models []domain.PortalModel, kind string) []domain.PortalModel {
	if kind == "similarity" {
		kind = "embedding"
	}
	var out []domain.PortalModel
	for _, m := range models {
		if t, e := domain.TrialFor(m); e == nil && (kind == "" || t.Kind == kind) {
			out = append(out, m)
		}
	}
	return out
}
func (w *workspace) chooseTrialModel() {
	var p *page
	p = queryPage(w, "在线调试", "bupt api model", "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(c domain.PortalPage) content { return w.trialTaskChoices(p, c.Models, nil) })
	w.push(p)
}
func (w *workspace) trialTaskChoices(owner *page, catalog []domain.PortalModel, current *trialForm) content {
	v := content{facts: []string{"选择任务"}, empty: "目录中暂无支持调试的模型。"}
	for _, task := range trialTasks {
		models := supportedModels(catalog, task.kind)
		if len(models) == 0 {
			continue
		}
		v.rows = append(v.rows, navLink(task.kind, task.title, task.detail, "bupt help api model invoke --all", func() {
			if w.current() != owner {
				w.back()
			}
			open := func(m domain.PortalModel) {
				if owner.onLeave != nil {
					owner.onLeave()
				}
				t, _ := domain.TrialFor(m)
				f := newTrialForm(m, t, catalog)
				f.similarity = task.kind == "similarity"
				if f.similarity {
					f.input.Texts = []string{"", ""}
				}
				if current != nil && current.template.Kind == f.template.Kind && current.similarity == f.similarity {
					f.input = copyTrial(current.input)
					if slices.Contains(current.keyModels, m.ID) {
						f.key, f.keyName, f.keyModels = current.key, current.keyName, current.keyModels
					}
					w.trialWorkspace(owner, f, w.trialDrafts[w.draftSlot(f)].outcome)
				} else {
					w.resumeTrial(owner, f)
				}
			}
			if len(models) == 1 {
				open(models[0])
				return
			}
			selected := models[max(0, slices.IndexFunc(models, func(m domain.PortalModel) bool { return m.Health == "ok" }))].ID
			if current != nil && slices.ContainsFunc(models, func(m domain.PortalModel) bool { return m.ID == current.model.ID }) {
				selected = current.model.ID
			}
			w.modelPicker(models, selected, open)
		}))
	}
	return v
}
func (w *workspace) changeTask(p *page, f *trialForm) {
	if len(f.models) > 0 {
		w.picker("选择任务", func() content { return w.trialTaskChoices(p, f.models, f) })
	} else {
		w.popup(queryPage(w, "选择任务", "bupt api model", "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(c domain.PortalPage) content { return w.trialTaskChoices(p, c.Models, f) }))
	}
}
func (w *workspace) modelPicker(models []domain.PortalModel, selected string, apply func(domain.PortalModel)) {
	w.picker("选择模型", func() content {
		v := content{empty: "没有兼容的模型。"}
		for _, m := range models {
			v.rows = append(v.rows, link(m.ID, cmp.Or(m.Name, m.ID), m.ID+" · 输入 "+ntext(m.InputPrice)+" 点/百万 Token", "", func() { w.back(); apply(m) }))
		}
		return v
	})
	w.current().key = selected
	w.view(w.current())
}
func newTrialForm(m domain.PortalModel, t domain.ModelTrial, models []domain.PortalModel) *trialForm {
	f := &trialForm{model: m, template: t, models: models}
	switch t.Kind {
	case "chat":
		f.input = domain.TrialInput{Texts: []string{""}, MaxTokens: t.Sample.MaxTokens}
	case "embedding":
		f.input.Texts = []string{""}
	case "rerank":
		f.input.Documents = []string{"", ""}
	}
	return f
}
func (f *trialForm) clone() *trialForm { copy := *f; copy.input = copyTrial(f.input); return &copy }
func (w *workspace) modelPlayground(m domain.PortalModel, t domain.ModelTrial, models ...domain.PortalModel) *trialForm {
	f := newTrialForm(m, t, models)
	w.resumeTrial(nil, f)
	return f
}

type trialOutcome struct {
	incident                        feedback.Incident
	result                          *domain.TrialResult
	message, reference              string
	summary                         string
	unknown, acknowledged, expanded bool
}

func (w *workspace) switchWorkbench(bench string, f *trialForm) {
	draft := f.clone()
	if len(w.pages) > 1 {
		parent := w.pages[len(w.pages)-2]
		if parent.bench == bench && parent.acceptDraft != nil {
			w.back()
			parent.acceptDraft(draft)
			return
		}
	}
	if bench == "code" {
		w.integrationExamples(draft)
	} else {
		w.openTrialForm(draft)
	}
}

type savedTrial struct {
	draft   *trialForm
	outcome trialOutcome
}

func (w *workspace) draftSlot(f *trialForm) string {
	return fmt.Sprintf("%s/%s/%s/%t/%s", w.Config.Selected, w.bar.snapshot.CampusID, f.template.Kind, f.similarity, f.model.ID)
}
func (w *workspace) resumeTrial(p *page, f *trialForm) {
	last := trialOutcome{}
	if saved, ok := w.trialDrafts[w.draftSlot(f)]; ok {
		f.input = copyTrial(saved.draft.input)
		f.key, f.keyName = saved.draft.key, saved.draft.keyName
		last = saved.outcome
	} else {
		saved := w.readDraft("model/" + w.draftSlot(f))
		if !saved.Updated.IsZero() {
			if saved.MaxTokens > 0 && f.template.Kind == "chat" {
				f.input.MaxTokens = saved.MaxTokens
			}
			if f.template.Kind == "rerank" && saved.TopN <= len(f.input.Documents) {
				f.input.TopN = saved.TopN
			}
		}
	}
	w.trialWorkspace(p, f, last)
}
func (w *workspace) openTrialForm(f *trialForm) {
	last := w.trialDrafts[w.draftSlot(f)].outcome
	w.trialWorkspace(nil, f, last)
}
func (w *workspace) rememberTrial(f *trialForm, last trialOutcome) {
	if w.trialDrafts == nil {
		w.trialDrafts = map[string]savedTrial{}
	}
	w.trialDrafts[w.draftSlot(f)] = savedTrial{draft: f.clone(), outcome: last}
}
func (w *workspace) trialWorkspace(p *page, f *trialForm, last trialOutcome) {
	fresh := p == nil
	if fresh {
		p = &page{}
	}
	p.title, p.bench, p.command, p.queryMethod = "在线调试", "trial", "bupt help api model invoke --all", "portal.read"
	p.filter, p.key, p.scroll, p.focus, p.reading = "", f.firstInput(), 0, 0, false
	owner := w.bar.snapshot.CampusID
	p.onLeave = func() {
		if owner != w.bar.snapshot.CampusID {
			return
		}
		w.rememberTrial(f, last)
	}
	p.acceptDraft = func(d *trialForm) { *f = *d }
	render := func(o trialOptions) func() content { return func() content { return w.trialContent(p, f, o, &last) } }
	p.build = render(trialOptions{})
	p.readNavigation = p.build
	p.load = func(ctx context.Context) (func() content, error) {
		var o trialOptions
		if !w.Cached {
			keys, e := w.readTrialPage(ctx, "key")
			if e != nil {
				o.keyProblem = readProblem(e)
				return render(o), e
			}
			o.keys = keys.Keys
		}
		return render(o), nil
	}
	if fresh {
		w.push(p)
	}
	w.loadPage(p)
}
func (w *workspace) trialTextRow(key, title, text string, save func(string)) row {
	display := strings.ReplaceAll(text, "\n", " ↵ ")
	if display == "" {
		display = "按 Enter 输入…"
	}
	r := navLink(key, title, display, "", func() { w.editTrialText(title, text, save) })
	r.verb = "编辑"
	return r
}
func (w *workspace) trialTexts(v *content, items *[]string, noun string) {
	for i, s := range *items {
		v.rows = append(v.rows, w.trialTextRow(fmt.Sprintf("item-%d", i), fmt.Sprintf("%s %d", noun, i+1), s, func(s string) { (*items)[i] = s }))
	}
	if len(*items) < domain.TrialItemLimit {
		v.rows = append(v.rows, navLink("add", "添加"+noun, "", "", func() {
			w.editTrialText("新增"+noun, "", func(s string) {
				if strings.TrimSpace(s) != "" {
					*items = append(*items, s)
				}
			})
		}))
	}
	if len(*items) > 1 {
		v.rows = append(v.rows, link("remove", "移除最后一段", fmt.Sprintf("%s %d", noun, len(*items)), "", func() { *items = (*items)[:len(*items)-1] }))
	}
}
func (w *workspace) trialNumber(key, title string, current *int, minVal, maxVal int, save func()) row {
	display := strconv.Itoa(*current)
	if *current == 0 {
		display = "全部"
	}
	return navLink(key, title, display, "", func() {
		w.edit(title, strconv.Itoa(*current), func(s string) error {
			n, e := strconv.Atoi(strings.TrimSpace(s))
			if e != nil || n < minVal || n > maxVal {
				return fmt.Errorf("请填写 %d 至 %d 的整数", minVal, maxVal)
			}
			*current = n
			save()
			return nil
		})
	})
}
func (w *workspace) trialInputRows(v *content, f *trialForm) {
	switch f.template.Kind {
	case "chat":
		v.rows = append(v.rows, w.trialTextRow("prompt", "你的问题", f.input.Texts[0], f.saveQuestion))
	case "embedding":
		if f.similarity {
			for i, text := range f.input.Texts {
				v.rows = append(v.rows, w.trialTextRow(fmt.Sprintf("item-%d", i), fmt.Sprintf("比较文本 %d", i+1), text, func(s string) { f.input.Texts[i] = s }))
			}
		} else {
			w.trialTexts(v, &f.input.Texts, "文本")
		}
	case "rerank":
		v.rows = append(v.rows, w.trialTextRow("query", "检索问题", f.input.Query, func(s string) { f.input.Query = s }))
		w.trialTexts(v, &f.input.Documents, "候选段落")
	}
}

func (w *workspace) trialLimitRow(v *content, f *trialForm) {
	switch f.template.Kind {
	case "chat":
		v.rows = append(v.rows, w.trialNumber("max", "输出上限（Token）", &f.input.MaxTokens, 1, 4096, func() { w.saveModelDraft(f) }))
	case "rerank":
		if f.input.TopN > len(f.input.Documents) {
			f.input.TopN = 0
		}
		v.rows = append(v.rows, w.trialNumber("top", "返回条数（0 为全部）", &f.input.TopN, 0, len(f.input.Documents), func() { w.saveModelDraft(f) }))
	}
}
func usableModelKeys(all []domain.PortalKey, model string) []domain.PortalKey {
	var keys []domain.PortalKey
	for _, k := range all {
		if k.ID != "" && k.Status == "active" && k.Stored && slices.Contains(k.Models, model) {
			keys = append(keys, k)
		}
	}
	return keys
}

func (w *workspace) createTrialKey(p *page, f *trialForm) {
	w.keyFormReturning([]string{f.model.ID}, p, func(r map[string]any) {
		f.key, _ = r["id"].(string)
		f.keyName, _ = object(r["item"])["name"].(string)
		f.keyModels = []string{f.model.ID}
		f.external = false
		if p.bench == "trial" {
			w.fetchPage(p, true)
		}
	})
}

// Key selection is a transient field of either workbench. Creating a key
// returns directly to the resource that invoked this field.
func (w *workspace) selectTrialKey(origin *page, f *trialForm, known []domain.PortalKey, allowExternal bool) {
	show := func(all []domain.PortalKey) content {
		keys := usableModelKeys(all, f.model.ID)
		v := content{facts: []string{"授权模型  " + f.model.ID}}
		for _, k := range keys {
			name := cmp.Or(k.Name, k.ID)
			detail := k.ID
			if k.ID == f.key {
				detail = "已选 · " + detail
			}
			v.rows = append(v.rows, link(k.ID, name, detail, "bupt api key", func() { f.key, f.keyName, f.external = k.ID, name, false; f.keyModels = k.Models; w.back() }))
		}
		if len(keys) == 0 {
			v.facts = append(v.facts, "当前控制器没有可用于此模型的密钥。")
		}
		if !w.Cached {
			v.rows = append(v.rows, navLink("new", "为此模型创建密钥", "仅授权当前模型", "bupt api key create", func() { w.back(); w.createTrialKey(origin, f) }))
		}
		if allowExternal {
			v.rows = append(v.rows, link("external", "使用已有的完整密钥", "从程序的 BUPT_API_KEY 环境变量读取", "", func() { f.key, f.keyName, f.external = "", "", true; w.back() }))
		}
		return v
	}
	p := queryPage(w, "选择调用密钥", "bupt api key", "portal.read", map[string]any{"section": "key", "cached": w.Cached}, false, func(data domain.PortalPage) content { return show(data.Keys) })
	if origin.bench == "trial" {
		p.build = func() content { return show(known) }
	}
	w.popup(p)
	w.current().key = f.key
}

func (w *workspace) changeTrialModel(f *trialForm) {
	apply := func(m domain.PortalModel) {
		if m.ID != f.model.ID {
			f.model = m
			f.template, _ = domain.TrialFor(m)
			if !slices.Contains(f.keyModels, m.ID) {
				f.key, f.keyName, f.external = "", "", false
			}
		}
	}
	if len(f.models) > 0 {
		w.modelPicker(supportedModels(f.models, f.template.Kind), f.model.ID, apply)
		return
	}
	w.popup(queryPage(w, "更换模型", "bupt api model", "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(c domain.PortalPage) content {
		v := content{empty: "没有兼容的模型。"}
		for _, m := range supportedModels(c.Models, f.template.Kind) {
			v.rows = append(v.rows, link(m.ID, cmp.Or(m.Name, m.ID), m.ID, "", func() { w.back(); apply(m) }))
		}
		return v
	}))
	w.current().key = f.model.ID
}

func (w *workspace) trialContent(p *page, f *trialForm, o trialOptions, last *trialOutcome) content {
	kind := f.template.Kind
	if f.similarity {
		kind = "similarity"
	}
	meta := "每百万 Token · 输入 " + ntext(f.model.InputPrice) + " / 输出 " + ntext(f.model.OutputPrice) + " 积分"
	if f.model.Context != "" {
		meta += " · 上下文 " + f.model.Context
	}
	v := content{facts: []string{trialTitle(kind) + "  ·  " + f.model.ID, meta}}
	keys := usableModelKeys(o.keys, f.model.ID)
	at := slices.IndexFunc(keys, func(k domain.PortalKey) bool { return k.ID == f.key })
	if !p.loading && o.keyProblem == "" {
		if at < 0 {
			f.key, f.keyName = "", ""
			if len(keys) == 1 {
				at = 0
			}
		}
		if at >= 0 {
			k := keys[at]
			f.key, f.keyName = k.ID, cmp.Or(k.Name, k.ID)
			f.keyModels = k.Models
		}
	}
	w.trialInputRows(&v, f)
	ready := !w.Cached && !p.loading && o.keyProblem == "" && p.problemCode == "" && !w.needsLogin() && !w.bar.snapshot.Effective(time.Now()).ConsentRequired
	send := row{key: "send", title: "发送请求", detail: "按用量计费"}
	switch {
	case p.submitting:
		send.detail = "已提交，正在等待模型回复…"
	case w.Cached:
		send.detail = "离线草稿"
	case o.keyProblem != "":
		send.detail = o.keyProblem
	case p.loading:
		send.detail = "正在读取密钥…"
	case !ready:
		send.detail = "请先完成账号验证"
	case last.unknown && !last.acknowledged:
		send.detail = "结果未知，请先核查用量"
	case at < 0:
		send.detail = "请先选择可用密钥"
	default:
		if _, e := f.model.TrialRequest(f.input); e == nil {
			send = link("send", "发送请求", "按用量计费", cmdline(f.args(true)...), func() { w.submitTrial(p, f, last) })
			send.verb = "发送"
		} else {
			send.detail = "请先补全输入"
		}
	}
	v.rows = append(v.rows, send)
	if !w.Cached && f.template.Kind != "embedding" {
		v.rows = append(v.rows, link("clear-draft", "清除本机参数草稿", "恢复输出上限或返回条数的默认值", "", func() {
			if w.clearDraft("model/" + w.draftSlot(f)) {
				f.input.MaxTokens, f.input.TopN = f.template.Sample.MaxTokens, 0
				w.notice = "已清除本机参数草稿"
			}
		}))
	}
	if !w.Cached && ready && len(keys) == 0 {
		v.rows = append(v.rows, navLink("create-key", "创建调用密钥", "仅授权当前模型，完成后继续调试", "bupt api key create", func() { w.createTrialKey(p, f) }))
	} else if !w.Cached && !p.loading {
		v.rows = append(v.rows, link("key", "调用密钥", cmp.Or(f.keyName, "选择已授权的密钥"), "bupt api key", func() { w.selectTrialKey(p, f, o.keys, false) }))
	}
	v.rows = append(v.rows, link("model", "更换模型", f.model.ID, "bupt api model", func() { w.changeTrialModel(f) }))
	w.trialLimitRow(&v, f)
	v.rows = append(v.rows, link("task", "切换调试任务", trialTitle(kind), "bupt api model", func() { w.changeTask(p, f) }))
	v.rows = append(v.rows, link("sample", "填入示例", f.template.Title, "", func() {
		fill := func() { f.input = copyTrial(f.template.Sample); w.notice = "已填入示例，可继续编辑" }
		nonempty := strings.TrimSpace(f.input.Query+strings.Join(f.input.Texts, "")+strings.Join(f.input.Documents, "")) != ""
		if !nonempty {
			fill()
			return
		}
		w.picker("用示例覆盖当前输入？", func() content {
			return content{rows: []row{link("back", "保留当前输入", "", "", w.back), link("replace", "覆盖并填入示例", "", "", func() { w.back(); fill() })}}
		})
	}), navLink("code", "生成调用代码", "沿用模型、参数和输入", "bupt api guide", func() { w.switchWorkbench("code", f) }))
	v.panelTitle = "最近一次结果"
	switch {
	case p.submitting:
		v.panel = []string{"请求已提交，正在等待回复…"}
	case last.result != nil:
		r := last.result
		v.panelTitle += " · 成功 · " + r.Model
		v.panel = strings.Split(last.summary, "\n")
		v.panelFooter = r.Metrics()
		if n := len(v.panel); n > 0 {
			v.panel = v.panel[:n-1]
		}
		body := func() string { data, _ := json.MarshalIndent(r, "", "  "); return string(data) }
		v.rows = append(v.rows, link("details", "展开或收起完整结果", "输入输出用量、耗时及完整返回值", "", func() { last.expanded = !last.expanded }))
		v.rows = append(v.rows, link("copy", "复制结果", "复制可读结果", "", func() { w.copySnippet(last.summary) }), link("export", "导出结果 JSON", "保存完整回复、向量或排序", "", func() { w.saveSnippet(body()) }))
		if last.expanded {
			v.rows = append(v.rows, link("copy-full", "复制完整结果 JSON", "", "", func() { w.copySnippet(body()) }))
			v.panel = nil
			full := last.summary
			if r.Kind == "embedding" {
				full += "\n\n" + body()
			}
			v.rows = append(v.rows, w.textContent(full).rows...)
		}
	case last.message != "":
		v.incident = last.incident
		v.panelTitle += " · " + map[bool]string{true: "结果未知", false: "失败"}[last.unknown]
		v.panel = strings.Split(last.message, "\n")
		if last.reference != "" {
			v.panel = append(v.panel, "本次提交编号 "+last.reference)
		}
		if last.unknown {
			v.rows = append([]row{navLink("usage", "核查近期 API 用量", "本次提交编号 "+last.reference, "bupt api usage --days 7", func() { w.trialUsage(last) })}, v.rows...)
		}
	default:
		v.panel = []string{"发送后在这里查看回复。"}
	}
	return v
}
func (w *workspace) trialUsage(last *trialOutcome) {
	w.popup(queryPage(w, "核查近期 API 用量", "bupt api usage --days 7", "portal.read", map[string]any{"section": "usage", "days": 7, "refresh": true}, false, func(r domain.PortalContent[domain.PortalUsage]) content {
		v := content{facts: []string{"本次提交编号 " + last.reference, "学校近 7 天合计 · " + countText(r.Data.Calls) + " 次调用 · " + countText(r.Data.Tokens) + " Token", "汇总可能有延迟，无法据此判定单次扣费。"}}
		v.rows = []row{link("back", "返回调试", "", "", w.back), link("ack", "已核查，允许手动发起新请求", "仍需单独选择「发送请求」", "", func() { last.acknowledged = true; w.back() })}
		return v
	}))
}

func (w *workspace) submitTrial(p *page, f *trialForm, last *trialOutcome) {
	if w.Cached || p.submitting || w.current() != p || last.unknown && !last.acknowledged {
		return
	}
	ref := uuid.NewString()
	params := domain.ModelInvocation{Model: f.model.ID, KeyID: f.key, Input: copyTrial(f.input), IdempotencyKey: ref}
	if last.unknown && last.acknowledged {
		params.ResendAfter = last.reference
	}
	ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
	p.submitting, p.interrupt = true, cancel
	*last = trialOutcome{reference: ref}
	w.notice = ""
	go func() {
		defer cancel()
		var r domain.TrialResult
		err := w.Backend.Call(ctx, "portal.trial", params, &r)
		interrupted := ctx.Err() != nil
		w.post(w.ctx, func() {
			p.submitting = false
			p.interrupt = nil
			if err != nil {
				var de *domain.Error
				code := errorCode(err)
				last.unknown = !errors.As(err, &de) || slices.Contains([]string{"MODEL_RESULT_UNKNOWN", "MODEL_RECONCILIATION_REQUIRED", "TRIAL_ALREADY_SUBMITTED", "MODEL_GATEWAY_ERROR", "MODEL_RESPONSE_LARGE", "MODEL_PROTOCOL"}, code) || connectionError(code) || interrupted
				last.message = whenError(err)
				if de != nil && de.RequestID != "" && (code == "MODEL_RECONCILIATION_REQUIRED" || code == "TRIAL_ALREADY_SUBMITTED") {
					last.reference = de.RequestID
				} else if de != nil && de.RequestID != "" {
					last.message += "\n网关请求编号 " + de.RequestID
				}
				if last.unknown {
					p.key, p.filter, p.scroll = "usage", "", 0
				}
				last.incident = feedback.FromError("portal.trial", err)
				if last.incident.RequestID == "" {
					last.incident.RequestID = last.reference
				}
				if last.unknown {
					last.incident.State = "uncertain"
				}
				return
			}
			last.result = &r
			last.summary = r.Summary()
			if f.similarity {
				for _, line := range strings.Split(last.summary, "\n") {
					if strings.Contains(line, "余弦相似度") {
						last.summary = line + "\n" + strings.Replace(last.summary, line, "", 1)
						break
					}
				}
			}
			p.key = f.firstInput()
		})
	}()
}
