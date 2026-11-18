package tui

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

type trialForm struct {
	available []domain.PortalKey
	model     domain.PortalModel
	template  domain.ModelTrial
	input     domain.TrialInput
	key       string
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
func (w *workspace) chooseTrialModel() {
	w.push(queryPage(w, "选择试用模型", "bupt api model", "portal.read", map[string]any{"section": "model", "cached": w.Cached}, false, func(page domain.PortalPage) content {
		v := content{empty: "当前账号没有可试用的模型。"}
		for _, m := range page.Models {
			t, e := domain.TrialFor(m)
			if e != nil {
				v.rows = append(v.rows, info(m.ID, e.Error()))
				continue
			}
			label := map[string]string{"chat": "输入问题", "embedding": "输入向量文本", "rerank": "输入问题与候选段落"}[t.Kind]
			v.rows = append(v.rows, link(m.ID, m.ID, label, "bupt help api model try --all", func() { w.modelPlayground(m, t) }))
		}
		return v
	}))
}
func (w *workspace) modelPlayground(m domain.PortalModel, t domain.ModelTrial) {
	f := &trialForm{model: m, template: t}
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
	m := f.model
	p := &page{title: "试用模型 · " + m.ID, command: "bupt help api model try --all"}
	render := func(o trialOptions) func() content { return func() content { return w.trialContent(p, f, o) } }
	p.build = render(trialOptions{})
	p.load = func(ctx context.Context) (func() content, error) {
		var o trialOptions
		var keyErr error
		if !w.Cached {
			keys, e := w.readTrialPage(ctx, "key")
			if e != nil {
				keyErr = e
				o.keyProblem = whenError(e)
			} else {
				for _, k := range keys.Keys {
					if k.Status == "active" && k.Stored && slices.Contains(k.Models, m.ID) {
						o.keys = append(o.keys, k)
					}
				}
			}
		}
		guide, guideErr := w.readTrialPage(ctx, "guide")
		if guideErr == nil {
			o.base, _ = guide.Data["base_url"].(string)
		} else {
			o.guideProblem = "代码中的 BUPT_BASE_URL 取自学校公布的 Base URL。"
		}
		return render(o), firstLoginError(keyErr, guideErr)
	}
	w.push(p)
	w.loadPage(p)
}
func (w *workspace) trialTextRow(key, title, text string, save func(string), send func()) row {
	display := strings.ReplaceAll(text, "\n", " ↵ ")
	if display == "" {
		display = "按 Enter 输入…"
	}
	return link(key, title, display, "", func() { w.editTrialText(title, text, save, send) })
}
func (w *workspace) trialTexts(v *content, items *[]string, noun string, send func()) {
	for i, s := range *items {
		v.rows = append(v.rows, w.trialTextRow(fmt.Sprintf("item-%d", i), fmt.Sprintf("%s %d", noun, i+1), s, func(s string) { (*items)[i] = s }, send))
	}
	if len(*items) < domain.TrialItemLimit {
		v.rows = append(v.rows, link("add", "添加"+noun, "", "", func() {
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
	return link(key, title, display, "", func() {
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
		v.rows = append(v.rows, w.trialTextRow("prompt", "你的问题", f.input.Texts[0], func(s string) { f.input.Texts[0] = s }, send), w.trialNumber("max", "输出上限（Token）", &f.input.MaxTokens, 1, 4096))
	case "embedding":
		w.trialTexts(v, &f.input.Texts, "文本", send)
	case "rerank":
		v.rows = append(v.rows, w.trialTextRow("query", "检索问题", f.input.Query, func(s string) { f.input.Query = s }, send))
		w.trialTexts(v, &f.input.Documents, "候选段落", send)
		if f.input.TopN > len(f.input.Documents) {
			f.input.TopN = 0
		}
		v.rows = append(v.rows, w.trialNumber("top", "返回条数（0 为全部）", &f.input.TopN, 0, len(f.input.Documents)))
	}
}
func (w *workspace) trialContent(p *page, f *trialForm, o trialOptions) content {
	v := content{facts: []string{f.model.ID + " · 输入 " + ntext(f.model.InputPrice) + " / 输出 " + ntext(f.model.OutputPrice) + " 点/百万 Token"}}
	if p.submitting {
		v.facts = append(v.facts, "已发送，正在等待模型结果…")
	}
	f.available = o.keys
	var send func()
	if !w.Cached {
		send = func() {
			if p.loading {
				w.notice = "正在读取 Key 和网关配置"
				return
			}
			if _, err := f.model.TrialRequest(f.input); err != nil {
				w.notice = whenError(err)
				return
			}
			// The key lookup may have finished while the editor was open.
			w.view(p)
			if !slices.ContainsFunc(f.available, func(k domain.PortalKey) bool { return k.ID == f.key }) {
				w.notice = "请先选择一把可用 Key"
				return
			}
			w.sendTrial(p, f)
		}
	}
	w.trialInputRows(&v, f, send)
	if !w.Cached {
		keyName := "选择已授权的 Key"
		at := slices.IndexFunc(o.keys, func(k domain.PortalKey) bool { return k.ID == f.key })
		if at < 0 {
			if len(o.keys) == 1 {
				at = 0
				f.key = o.keys[0].ID
			} else if !p.loading {
				f.key = ""
			}
		}
		if at >= 0 {
			keyName = o.keys[at].Name + " · " + o.keys[at].ID
		}
		if o.keyProblem != "" {
			v.facts = append(v.facts, o.keyProblem)
		}
		if len(o.keys) > 0 {
			v.rows = append(v.rows, link("key", "API Key", keyName, "bupt api key", func() {
				w.local("选择 API Key", "bupt api key", func() content {
					cv := content{}
					for _, k := range o.keys {
						cv.rows = append(cv.rows, link(k.ID, k.Name, k.ID, "", func() { f.key = k.ID; w.back() }))
					}
					return cv
				})
			}))
		} else if !p.loading && o.keyProblem == "" {
			v.rows = append(v.rows, link("create-key", "创建可用 Key", "当前没有已启用、已保管并授权此模型的 Key", cmdline("api", "key", "create", "NAME", "--model", f.model.ID), func() {
				w.keyFormReturning([]string{f.model.ID}, p, func(receipt map[string]any) { f.key, _ = receipt["id"].(string); w.fetchPage(p, true) })
			}))
		}
		v.rows = append(v.rows, link("send", "发送一次", "提交当前输入，按学校实际用量计费", cmdline(f.args(true)...), send))
	} else {
		v.facts = append(v.facts, "缓存模式 · 可编辑和预览请求，在线试用请用 bupt tui api。")
	}
	v.rows = append(v.rows, link("sample", "填入示例", f.template.Title, cmdline("api", "model", "example", f.model.ID), func() { f.input = copyTrial(f.template.Sample); w.notice = "已填入，可继续编辑" }))
	v.rows = append(v.rows, link("code", "查看调用代码", "请求 JSON、curl、Python、bupt", cmdline(f.args(false)...), func() {
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
				cv.rows = append(cv.rows, link(format.id, format.label, "", c, func() {
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
			p.key = map[string]string{"chat": "prompt", "embedding": "item-0", "rerank": "query"}[r.Kind]
			title := map[string]string{"chat": "模型回复", "embedding": "向量结果", "rerank": "重排结果"}[r.Kind]
			text := r.Summary()
			if r.Kind == "chat" {
				text = "你\n" + input.Texts[0] + "\n\n模型\n" + text
			}
			if r.Kind == "rerank" {
				text = "检索问题\n" + input.Query + "\n\n" + text
			}
			w.local(title, command, func() content {
				out := w.textContent(text)
				out.facts = []string{r.Model}
				if r.Kind == "embedding" {
					out.rows = append(out.rows, link("vectors", "查看完整向量", "", command, func() { w.objectPage("完整向量", command, r.Vectors) }))
				}
				out.rows = append(out.rows, link("edit", "继续编辑输入", "", "", func() { w.back(); p.filter = ""; w.move(0); w.activate() }))
				return out
			})
		})
	}()
}
