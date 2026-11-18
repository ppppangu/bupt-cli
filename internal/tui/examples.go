package tui

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"bupt.local/cli/internal/domain"
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
	w.push(&page{title: title, command: cmd, copyText: body, build: func() content { return w.textContent(body) }})
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

func (w *workspace) integrationExamples() {
	browser := &exampleBrowser{format: snippetFormats[0], drafts: make(map[string]*trialForm)}
	p := &page{title: "接入示例", command: "bupt api guide"}
	p.load = func(ctx context.Context) (func() content, error) {
		guide, guideErr := w.readTrialPage(ctx, "guide")
		catalog, catalogErr := w.readTrialPage(ctx, "model")
		base, _ := guide.Data["base_url"].(string)
		var models []domain.PortalModel
		for _, m := range catalog.Models {
			if _, err := domain.TrialFor(m); err == nil {
				models = append(models, m)
			}
		}
		return func() content {
			v := content{}
			if base != "" {
				v.facts = append(v.facts, "Base URL  "+base)
				v.rows = append(v.rows, link("base", "公网 Base URL", base, "bupt api guide -q", func() { w.snippetPage("Base URL", "bupt api guide -q", base) }))
			} else {
				v.facts = append(v.facts, "尚未取得 Base URL。可在 BUPT_BASE_URL 中设置门户公布的地址。")
			}
			if guideErr != nil {
				v.facts = append(v.facts, whenError(guideErr))
			}
			switch {
			case catalogErr != nil:
				v.facts = append(v.facts, whenError(catalogErr))
			case len(models) == 0:
				v.facts = append(v.facts, "当前目录没有可生成示例的模型。")
			default:
				w.exampleRows(&v, browser, models, base)
			}
			return v
		}, firstLoginError(guideErr, catalogErr)
	}
	w.push(p)
}

func (b *exampleBrowser) draft(models []domain.PortalModel) *trialForm {
	at := slices.IndexFunc(models, func(m domain.PortalModel) bool { return m.ID == b.model })
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
	return f
}

func (w *workspace) exampleRows(v *content, b *exampleBrowser, models []domain.PortalModel, base string) {
	f := b.draft(models)
	cmd := cmdline(f.args(false, "--format", b.format.id)...)
	v.rows = append(v.rows, link("model", "模型", f.model.ID, "", func() {
		choices := make([]exampleChoice, len(models))
		for i, m := range models {
			choices[i] = exampleChoice{m.ID, m.ID}
		}
		w.chooseExample("选择模型", choices, b.model, func(c exampleChoice) { b.model = c.id })
	}), link("format", "示例格式", b.format.label, "", func() {
		w.chooseExample("选择示例格式", snippetFormats, b.format.id, func(c exampleChoice) { b.format = c })
	}), link("preview", "展开示例全文", b.format.label+" · "+f.model.ID, cmd, func() {
		code, err := f.model.ExampleCode(f.input, b.format.id, base)
		if err != nil {
			w.notice = whenError(err)
			return
		}
		w.snippetPage(b.format.label, cmd, code)
	}))
	switch b.format.id {
	case "curl", "python":
		v.rows = append(v.rows, link("setup", "运行准备", "设置 BUPT_API_KEY，直接使用公网网关", "bupt api key", func() { w.snippetPage("配置当前终端的 Key", "bupt api key", keySetup(true)) }))
	case "bupt":
		v.rows = append(v.rows, link("setup", "运行准备", "选择控制器保管的 Key ID", "bupt api key", func() { w.snippetPage("配置当前终端的 Key ID", "bupt api key", keySetup(false)) }))
	}
	v.rows = append(v.rows, link("input", "编辑示例输入", f.template.Title, "", func() {
		w.local("编辑示例输入", "", func() content {
			cv := content{}
			w.trialInputRows(&cv, f, nil)
			return cv
		})
	}))
	if !w.Cached {
		v.rows = append(v.rows, link("trial", "带入试用模型", "沿用当前输入，选好 Key 后可发送", "bupt help api model try --all", func() {
			w.openTrialForm(&trialForm{model: f.model, template: f.template, input: copyTrial(f.input)})
		}))
	}
	v.facts = append(v.facts, "实际发送请求后，学校按所选模型的用量计费。")
}

func (w *workspace) chooseExample(title string, choices []exampleChoice, selected string, selectChoice func(exampleChoice)) {
	w.local(title, "", func() content {
		v := content{}
		for _, c := range choices {
			detail := ""
			if c.id == selected {
				detail = "已选"
			}
			v.rows = append(v.rows, link(c.id, c.label, detail, "", func() { selectChoice(c); w.back() }))
		}
		return v
	})
	w.move(slices.IndexFunc(choices, func(c exampleChoice) bool { return c.id == selected }))
}

func keySetup(exportKey bool) string {
	text := `bupt api key
printf 'Key ID > '
IFS= read -r BUPT_KEY_ID
export BUPT_KEY_ID`
	if exportKey {
		text += `
BUPT_API_KEY=$(bupt api key export --output - -- "$BUPT_KEY_ID") && export BUPT_API_KEY`
	}
	return text
}
