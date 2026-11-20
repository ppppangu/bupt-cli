package tui

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/shellquote"
)

type exampleChoice struct{ id, label string }

var snippetFormats = []exampleChoice{
	{"curl", "curl"}, {"python", "Python 标准库"}, {"json", "请求 JSON"}, {"bupt", "bupt 命令"},
}

func (w *workspace) copySnippet(text string) {
	w.screen.SetClipboard([]byte(text))
	w.notice = "已向终端发送复制请求，也可按 s 保存为文件。"
}

func (w *workspace) saveSnippet(text string) {
	w.edit("保存到新文件", "", func(path string) error {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("请填写新文件路径")
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, writeErr := f.WriteString(text)
		closeErr := f.Close()
		if writeErr != nil || closeErr != nil {
			_ = os.Remove(path)
			if writeErr != nil {
				return writeErr
			}
			return closeErr
		}
		w.notice = "已保存 " + path
		return nil
	})
}
func (w *workspace) snippetPage(title, cmd, body string) {
	w.guidedSnippet(title, cmd, body, "")
}
func (w *workspace) guidedSnippet(title, cmd, body, hint string, next ...row) {
	w.push(&page{title: title, command: cmd, copyText: body, build: func() content {
		v := w.textContent(body)
		if hint != "" {
			v.facts = []string{hint}
		}
		copy := link("copy", "复制全文", "", cmd, func() { w.copySnippet(body) })
		copy.verb = "复制全文"
		save := link("save", "另存为新文件", "", "", func() { w.saveSnippet(body) })
		save.verb = "选择路径"
		v.rows = append(v.rows, copy, save)
		v.rows = append(v.rows, next...)
		return v
	}})
}

// Only the playground can submit a draft. Switching models here retains edits
// for this visit, including across a catalogue refresh.
type exampleBrowser struct {
	model  string
	format exampleChoice
	drafts map[string]*trialForm
}

func (w *workspace) readTrialPage(ctx context.Context, section string) (domain.PortalPage, error) {
	var result domain.PortalPage
	fresh := ctx.Value(manualRefresh{}) == true && !w.Cached
	err := w.Backend.Call(ctx, "portal.read", map[string]any{"section": section, "cached": w.Cached, "refresh": fresh}, &result)
	return result, err
}

func (w *workspace) integrationExamples(seed *trialForm) {
	browser := &exampleBrowser{format: snippetFormats[0], drafts: make(map[string]*trialForm)}
	if seed != nil {
		browser.model = seed.model.ID
		browser.drafts[browser.model] = seed
	}
	p := &page{title: "代码工作台", bench: "code", command: "bupt api guide", queryMethod: "portal.read"}
	p.acceptDraft = func(d *trialForm) { browser.model = d.model.ID; browser.drafts[browser.model] = d }
	owner := w.bar.snapshot.CampusID
	p.onLeave = func() {
		if f := browser.drafts[browser.model]; f != nil && owner == w.bar.snapshot.CampusID {
			w.rememberTrial(f, w.trialDrafts[w.draftSlot(f)].outcome)
		}
	}
	var expanded, config bool
	p.load = func(ctx context.Context) (func() content, error) {
		guide, ge := w.readTrialPage(ctx, "guide")
		catalog, ce := w.readTrialPage(ctx, "model")
		base, _ := guide.Data["base_url"].(string)
		models := supportedModels(catalog.Models, "")
		return func() content {
			v := content{}
			p.copyText = ""
			if ge != nil {
				v.facts = append(v.facts, whenError(ge))
			}
			if ce != nil {
				v.facts = append(v.facts, whenError(ce))
			}
			if base == "" {
				v.facts = append(v.facts, "Base URL 待学校提供，可在环境变量 BUPT_BASE_URL 中设置。")
			}
			var f *trialForm
			if len(models) > 0 {
				f = browser.draft(models)
			} else if ce != nil {
				f = browser.drafts[browser.model]
			}
			if len(models) > 0 {
				v.rows = append(v.rows, link("model", "模型", browser.model, "bupt api model", func() {
					w.modelPicker(models, browser.model, func(m domain.PortalModel) {
						if f != nil && browser.drafts[m.ID] == nil {
							t, _ := domain.TrialFor(m)
							if t.Kind == f.template.Kind {
								copy := f.clone()
								copy.model, copy.template = m, t
								if !slices.Contains(copy.keyModels, m.ID) {
									copy.key, copy.keyName, copy.external = "", "", false
								}
								browser.drafts[m.ID] = copy
							}
						}
						browser.model = m.ID
					})
				}))
			}
			if f == nil {
				v.facts = append(v.facts, "请选择目录中支持调用示例的模型。")
			} else {
				f.models = models
				if f.similarity {
					v.facts = append(v.facts, "向量由网关返回，余弦相似度在本机计算。")
				}
				format := browser.format
				v.rows = append(v.rows, link("format", "示例格式", format.label, "", func() {
					w.chooseExample("选择示例格式", snippetFormats, browser.format.id, func(c exampleChoice) {
						browser.format = c
						if c.id == "json" {
							config = false
						}
					})
				}))
				w.trialInputRows(&v, f)
				w.trialLimitRow(&v, f)
				if format.id != "json" {
					key := cmp.Or(f.keyName, f.key, "选择或创建")
					if f.external && format.id != "bupt" {
						key = "环境变量 BUPT_API_KEY"
					}
					v.rows = append(v.rows, link("setup", "调用密钥", key, "bupt api key", func() { w.selectTrialKey(p, f, nil, format.id != "bupt") }), link("config", "环境配置与依赖", "查看或收起配置命令", "", func() { config = !config; expanded = false }))
				}
				cmd := cmdline(f.args(false, "--format", format.id)...)
				generate := f.model.ExampleCode
				if f.similarity {
					generate = f.model.SimilarityExample
				}
				body, err := generate(f.input, format.id, base)
				if config && format.id != "json" {
					hint := "在运行程序的终端配置密钥。"
					if f.similarity && format.id == "curl" {
						hint = "curl 取得向量，Python 3 标准库计算余弦相似度。"
					}
					if format.id == "python" {
						hint = "使用 Python 3 标准库，无需安装第三方包。保存为 example.py 后运行 python3 example.py。"
					}
					v.facts = append(v.facts, hint)
					switch {
					case f.key != "":
						body = keySetup(w.Config.Selected, format.id != "bupt", f.key)
						err = nil
					case f.external && format.id != "bupt":
						body = "在运行程序的环境中设置 BUPT_API_KEY，值为已授权此模型的完整密钥。"
						err = nil
					default:
						body = "先在「调用密钥」中选择密钥。"
						err = nil
					}
					v.panelTitle = "环境配置"
				} else {
					v.panelTitle = "代码预览 · " + format.label
				}
				if err != nil {
					v.panel = []string{"补全输入即可预览此格式的代码。"}
				} else {
					p.copyText = body
					v.panel = strings.Split(body, "\n")
					caption := "代码"
					if config {
						caption = "配置说明"
					}
					v.rows = append(v.rows, link("copy", "复制"+caption, "c · 全文", cmd, func() { w.copySnippet(body) }), link("save", "另存"+caption, "s · 写入新文件", cmd, func() { w.saveSnippet(body) }), link("preview", "展开或收起全文", format.label, cmd, func() { expanded = !expanded }))
				}
				v.rows = append(v.rows, navLink("trial", "在线调试", "带当前模型和输入打开调试工作台", "bupt help api model invoke --all", func() { w.switchWorkbench("trial", f) }))
			}
			if base != "" {
				v.rows = append(v.rows, link("base", "复制 Base URL", base, "bupt api guide", func() { w.copySnippet(base) }))
			}
			if expanded && p.copyText != "" {
				v.panel = nil
				v.rows = append(v.rows, w.textContent(p.copyText).rows...)
			}
			return v
		}, firstAccessError(ge, ce)
	}
	w.push(p)
}

func (b *exampleBrowser) draft(models []domain.PortalModel) *trialForm {
	at := slices.IndexFunc(models, func(m domain.PortalModel) bool { return m.ID == b.model })
	if at < 0 && b.model != "" {
		return nil
	}
	if at < 0 {
		at = max(0, slices.IndexFunc(models, func(m domain.PortalModel) bool { t, _ := domain.TrialFor(m); return t.Kind == "chat" }))
	}
	m := models[at]
	b.model = m.ID
	t, _ := domain.TrialFor(m)
	f := b.drafts[m.ID]
	if f == nil || f.template.Kind != t.Kind {
		f = &trialForm{template: t, input: copyTrial(t.Sample)}
		b.drafts[m.ID] = f
	}
	f.model = m
	f.template = t
	return f
}

func (w *workspace) chooseExample(title string, choices []exampleChoice, selected string, selectChoice func(exampleChoice)) {
	w.picker(title, func() content {
		v := content{}
		for _, c := range choices {
			detail := ""
			if c.id == selected {
				detail = "已选"
			}
			v.rows = append(v.rows, link(c.id, c.label, detail, "", func() { w.back(); selectChoice(c) }))
		}
		return v
	})
	w.move(slices.IndexFunc(choices, func(c exampleChoice) bool { return c.id == selected }))
}

func keySetup(profile string, exportKey bool, id string) string {
	text := "export BUPT_PROFILE=" + shellquote.Literal(profile) + "\nexport BUPT_KEY_ID=" + shellquote.Literal(id)
	if exportKey {
		text += `
BUPT_API_KEY=$(bupt api key export --file - -- "$BUPT_KEY_ID") && export BUPT_API_KEY`
	}
	return text
}
