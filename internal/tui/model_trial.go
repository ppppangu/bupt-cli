package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

type trialForm struct {
	models   []domain.PortalModel
	model    domain.PortalModel
	template domain.ModelTrial
	input    domain.TrialInput
	key      string
}
type trialOptions struct {
	keys                           []domain.PortalKey
	base, keyProblem, guideProblem string
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

var trialTasks = []struct{ kind, title, detail string }{
	{"chat", "问问题", "输入问题，阅读模型的回答"},
	{"embedding", "文本转向量", "输入文本得到向量，两段可比较语义相似度"},
	{"rerank", "按相关性排序", "输入问题和候选段落，查看排序结果"},
}

func trialTitle(kind string) string {
	for _, task := range trialTasks {
		if task.kind == kind {
			return task.title
		}
	}
	return "试用模型"
}

func (w *workspace) chooseTrialModel() {
	w.push(queryPage(w, "试用模型", "bupt api model", "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(page domain.PortalPage) content {
		groups := make(map[string][]domain.PortalModel)
		for _, m := range page.Models {
			if t, err := domain.TrialFor(m); err == nil {
				groups[t.Kind] = append(groups[t.Kind], m)
			}
		}
		v := content{}
		for _, task := range trialTasks {
			models := groups[task.kind]
			if len(models) == 0 {
				continue
			}
			v.rows = append(v.rows, navLink(task.kind, task.title, task.detail, "bupt help api model try --all", func() {
				at := slices.IndexFunc(models, func(m domain.PortalModel) bool { return m.Health == "ok" })
				m := models[max(0, at)]
				t, _ := domain.TrialFor(m)
				w.modelPlayground(m, t, models...)
			}))
		}
		if len(v.rows) == 0 {
			v.facts = []string{"当前目录暂无支持试用的模型。"}
		}
		v.rows = append(v.rows, navLink("catalog", "查看全部模型", "按模型 ID 查看能力、价格和状态", "bupt api model", func() { w.portalPage("model", "模型", "bupt api model", nil) }))
		return v
	}))
}
func (w *workspace) modelPlayground(m domain.PortalModel, t domain.ModelTrial, models ...domain.PortalModel) {
	f := &trialForm{model: m, template: t, models: models}
	switch t.Kind {
	case "chat":
		f.input.Texts = []string{""}
		f.input.MaxTokens = t.Sample.MaxTokens
	case "embedding":
		f.input.Texts = []string{""}
	case "rerank":
		f.input.Documents = []string{"", ""}
	}
	w.openTrialForm(f)
}

func (w *workspace) openTrialForm(f *trialForm) {
	p := &page{title: trialTitle(f.template.Kind), command: "bupt help api model try --all", queryMethod: "portal.read"}
	render := func(o trialOptions) func() content { return func() content { return w.trialContent(p, f, o) } }
	p.build = render(trialOptions{})
	p.load = func(ctx context.Context) (func() content, error) {
		var o trialOptions
		var keyErr error
		if !w.Cached {
			keys, e := w.readTrialPage(ctx, "key")
			if e != nil {
				keyErr = e
				o.keyProblem = readProblem(e)
			} else {
				o.keys = keys.Keys
			}
		}
		guide, guideErr := w.readTrialPage(ctx, "guide")
		if guideErr == nil {
			o.base, _ = guide.Data["base_url"].(string)
		} else {
			o.guideProblem = "代码中的 BUPT_BASE_URL 取自学校公布的 Base URL。"
		}
		return render(o), firstAccessError(keyErr, guideErr)
	}
	w.push(p)
	w.loadPage(p)
}
func (w *workspace) trialTextRow(key, title, text string, save func(string), send func()) row {
	display := strings.ReplaceAll(text, "\n", " ↵ ")
	if display == "" {
		display = "按 Enter 输入…"
	}
	r := navLink(key, title, display, "", func() { w.editTrialText(title, text, save, send) })
	r.verb = "编辑"
	return r
}
func (w *workspace) trialTexts(v *content, items *[]string, noun string, send func()) {
	for i, s := range *items {
		v.rows = append(v.rows, w.trialTextRow(fmt.Sprintf("item-%d", i), fmt.Sprintf("%s %d", noun, i+1), s, func(s string) { (*items)[i] = s }, send))
	}
	if len(*items) < domain.TrialItemLimit {
		v.rows = append(v.rows, navLink("add", "添加"+noun, "", "", func() {
			w.editTrialText("新增"+noun, "", func(s string) {
				if strings.TrimSpace(s) != "" {
					*items = append(*items, s)
				}
			}, send)
		}))
	}
	if len(*items) > 1 {
		v.rows = append(v.rows, link("remove", "移除最后一段", fmt.Sprintf("%s %d", noun, len(*items)), "", func() { *items = (*items)[:len(*items)-1] }))
	}
}
func (w *workspace) trialNumber(key, title string, current *int, minVal, maxVal int) row {
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
			return nil
		})
	})
}
func (w *workspace) trialInputRows(v *content, f *trialForm, send func()) {
	switch f.template.Kind {
	case "chat":
		v.rows = append(v.rows, w.trialTextRow("prompt", "你的问题", f.input.Texts[0], func(s string) { f.input.Texts[0] = s }, send))
	case "embedding":
		w.trialTexts(v, &f.input.Texts, "文本", send)
	case "rerank":
		v.rows = append(v.rows, w.trialTextRow("query", "检索问题", f.input.Query, func(s string) { f.input.Query = s }, send))
		w.trialTexts(v, &f.input.Documents, "候选段落", send)
	}
}

func (w *workspace) trialLimitRow(v *content, f *trialForm) {
	switch f.template.Kind {
	case "chat":
		v.rows = append(v.rows, w.trialNumber("max", "输出上限（Token）", &f.input.MaxTokens, 1, 4096))
	case "rerank":
		if f.input.TopN > len(f.input.Documents) {
			f.input.TopN = 0
		}
		v.rows = append(v.rows, w.trialNumber("top", "返回条数（0 为全部）", &f.input.TopN, 0, len(f.input.Documents)))
	}
}
func (w *workspace) trialContent(p *page, f *trialForm, o trialOptions) content {
	v := content{facts: []string{"模型  " + f.model.ID, "每百万 Token · 输入 " + ntext(f.model.InputPrice) + " 点，输出 " + ntext(f.model.OutputPrice) + " 点"}}
	var keys []domain.PortalKey
	for _, k := range o.keys {
		if k.Status == "active" && k.Stored && slices.Contains(k.Models, f.model.ID) {
			keys = append(keys, k)
		}
	}
	at := slices.IndexFunc(keys, func(k domain.PortalKey) bool { return k.ID == f.key })
	if at < 0 && !p.loading {
		f.key = ""
		if len(keys) == 1 {
			at, f.key = 0, keys[0].ID
		}
	}

	ready := func() bool {
		return !w.Cached && !p.loading && p.problemCode == "" && !w.needsLogin() && !w.bar.snapshot.Effective(time.Now()).ConsentRequired
	}
	var send func()
	if ready() && at >= 0 {
		key := f.key
		send = func() {
			// An open editor can outlive the key or session shown when it opened.
			w.view(p)
			if !ready() || key != f.key {
				w.notice = "请核对当前账号和调用 Key，再发送一次。"
				return
			}
			if _, err := f.model.TrialRequest(f.input); err != nil {
				w.notice = whenError(err)
				return
			}
			w.sendTrial(p, f)
		}
	}
	w.trialInputRows(&v, f, send)
	switch {
	case p.submitting:
		v.facts = append(v.facts, "已发送，正在等待模型结果…")
	case w.Cached:
		v.facts = append(v.facts, "离线草稿 · 可编辑输入和查看调用代码。")
	case o.keyProblem != "":
		v.facts = append(v.facts, o.keyProblem)
	case p.loading:
		v.facts = append(v.facts, "正在读取调用 Key…")
	case len(keys) == 0 && ready():
		v.rows = append(v.rows, navLink("create-key", "为此模型创建 Key", "完成后回到当前草稿", cmdline("api", "key", "create", "NAME", "--model", f.model.ID), func() {
			w.keyFormReturning([]string{f.model.ID}, p, func(receipt map[string]any) {
				f.key, _ = receipt["id"].(string)
				p.key = f.firstInput()
				w.fetchPage(p, true)
			})
		}))
	case send != nil:
		if _, err := f.model.TrialRequest(f.input); err == nil {
			button := link("send", "发送一次", "按学校实际用量计费", cmdline(f.args(true)...), send)
			button.verb = "发送一次"
			v.rows = append(v.rows, button)
		}
	}

	if !w.Cached && !p.loading && len(keys) > 0 {
		label, detail := "选择调用 Key", "选择一把已授权的 Key 后发送"
		if at >= 0 {
			label, detail = "调用 Key", keys[at].Name
			if detail == "" {
				detail = keys[at].ID
			}
		}
		if len(keys) == 1 {
			v.rows = append(v.rows, row{key: "key", title: label, detail: detail})
		} else {
			v.rows = append(v.rows, navLink("key", label, detail, "bupt api key", func() {
				w.local("选择调用 Key", "bupt api key", func() content {
					cv := content{}
					for _, k := range keys {
						cv.rows = append(cv.rows, link(k.ID, k.Name, k.ID, "", func() { f.key = k.ID; w.back() }))
					}
					return cv
				})
			}))
		}
	}
	if len(f.models) > 1 {
		v.rows = append(v.rows, navLink("model", "更换模型", "沿用当前输入 · "+f.model.ID, "bupt api model", func() {
			w.local("更换模型", "bupt api model", func() content {
				list := content{}
				for _, m := range f.models {
					detail := "输入 " + ntext(m.InputPrice) + " / 输出 " + ntext(m.OutputPrice) + " 点/百万 Token"
					if m.ID == f.model.ID {
						detail = "当前 · " + detail
					}
					list.rows = append(list.rows, link(m.ID, m.ID, detail, "", func() {
						if m.ID != f.model.ID {
							f.model = m
							f.template, _ = domain.TrialFor(m)
							f.key = ""
						}
						w.back()
					}))
				}
				return list
			})
			w.current().key = f.model.ID
		}))
	}
	w.trialLimitRow(&v, f)
	v.rows = append(v.rows, link("sample", "填入示例", f.template.Title, cmdline("api", "model", "example", f.model.ID), func() { f.input = copyTrial(f.template.Sample); w.notice = "已填入，可继续编辑" }))
	v.rows = append(v.rows, navLink("code", "查看调用代码", "请求 JSON、curl、Python、bupt", cmdline(f.args(false)...), func() {
		_, e := f.model.TrialRequest(f.input)
		if e != nil {
			w.notice = "请先补全输入或选择「填入示例」"
			return
		}
		w.local("调用代码", cmdline(f.args(false)...), func() content {
			cv := content{}
			if o.guideProblem != "" {
				cv.facts = []string{o.guideProblem}
			}
			for _, format := range snippetFormats {
				args := f.args(false, "--format", format.id)
				c := cmdline(args...)
				cv.rows = append(cv.rows, navLink(format.id, format.label, "", c, func() {
					s, err := f.model.ExampleCode(f.input, format.id, o.base)
					if err != nil {
						w.notice = whenError(err)
						return
					}
					w.snippetPage(format.label, c, s)
				}))
			}
			return cv
		})
	}))
	return v
}
func (w *workspace) sendTrial(p *page, f *trialForm) {
	if w.Cached || p.submitting || w.current() != p {
		return
	}
	p.submitting = true
	w.notice = ""
	input := copyTrial(f.input)
	command := cmdline(f.args(true)...)
	params := map[string]any{"model": f.model.ID, "key_id": f.key, "input": input, "idempotency_key": uuid.NewString()}
	go func() {
		ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
		defer cancel()
		var r domain.TrialResult
		err := w.Backend.Call(ctx, "portal.trial", params, &r)
		w.post(w.ctx, func() {
			p.submitting = false
			if w.current() != p {
				return
			}
			if err != nil {
				w.textPage("调用失败", command, whenError(err))
				return
			}
			p.focus = 0
			p.key = f.firstInput()
			title := map[string]string{"chat": "模型回复", "embedding": "向量结果", "rerank": "重排结果"}[r.Kind]
			text := r.Summary()
			inputText := strings.Join(input.Texts, "\n\n")
			if r.Kind == "rerank" {
				inputText = input.Query + "\n\n" + strings.Join(input.Documents, "\n\n")
			}
			w.local(title, command, func() content {
				out := w.textContent(text)
				out.facts = []string{r.Model}
				out.rows = append(out.rows, navLink("edit", "继续编辑输入", "", "", func() {
					w.back()
					p.filter, p.key = "", f.firstInput()
					if r := p.selected(w.view(p)); r != nil && r.key == f.firstInput() {
						w.activate()
					}
				}))
				out.rows = append(out.rows, navLink("input", "查看本次输入", "", command, func() { w.textPage("本次输入", command, inputText) }))
				if r.Kind == "embedding" {
					out.rows = append(out.rows, navLink("vectors", "查看完整向量", "", command, func() { w.objectPage("完整向量", command, r.Vectors) }))
				}
				return out
			})
		})
	}()
}
