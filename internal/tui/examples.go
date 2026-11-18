package tui

import (
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
	w.push(&page{title: title, command: cmd, copyText: body, build: func() content {
		v := w.textContent(body)
		copy := link("copy", "复制全文", "", cmd, func() { w.copySnippet(body) })
		copy.verb = "复制全文"
		save := link("save", "另存为新文件", "", "", func() { w.saveSnippet(body) })
		save.verb = "选择路径"
		v.rows = append(v.rows, copy, save)
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

func (w *workspace) integrationExamples() {
	browser := &exampleBrowser{format: snippetFormats[0], drafts: make(map[string]*trialForm)}
	p := &page{title: "接入示例", command: "bupt api guide", queryMethod: "portal.read"}
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
			if base == "" {
				v.facts = append(v.facts, "尚未取得 Base URL。可在 BUPT_BASE_URL 中设置门户公布的地址。")
			}
			if guideErr != nil {
				v.facts = append(v.facts, whenError(guideErr))
			}
			var draft *trialForm
			switch {
			case catalogErr != nil:
				v.facts = append(v.facts, whenError(catalogErr))
			case len(models) == 0:
				v.facts = append(v.facts, "当前目录没有可生成示例的模型。")
			default:
				draft = browser.draft(models)
				w.exampleOptions(&v, browser, models, draft)
			}
			if base != "" {
				v.rows = append(v.rows, navLink("base", "公网地址（Base URL）", base, "bupt api guide -q", func() { w.snippetPage("Base URL", "bupt api guide -q", base) }))
			}
			if draft != nil {
				w.exampleOutput(&v, draft, browser.format, base)
			}
			return v
		}, firstAccessError(guideErr, catalogErr)
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

func (w *workspace) exampleOptions(v *content, b *exampleBrowser, models []domain.PortalModel, f *trialForm) {
	v.facts = append(v.facts, "在自己的程序里调用模型。运行请求后按学校用量计费。")
	input := f.input.Query
	if len(f.input.Texts) > 0 {
		input = f.input.Texts[0]
		if len(f.input.Texts) > 1 {
			input = fmt.Sprintf("%d 段文本 · %s", len(f.input.Texts), input)
		}
	}
	if len(f.input.Documents) > 0 {
		input = fmt.Sprintf("%d 个候选段落 · %s", len(f.input.Documents), input)
	}
	v.rows = append(v.rows, navLink("model", "选择模型", f.model.ID, "", func() {
		choices := make([]exampleChoice, len(models))
		for i, m := range models {
			choices[i] = exampleChoice{m.ID, m.ID}
		}
		w.chooseExample("选择模型", choices, b.model, func(c exampleChoice) { b.model = c.id })
	}), navLink("format", "示例格式", b.format.label, "", func() {
		w.chooseExample("选择示例格式", snippetFormats, b.format.id, func(c exampleChoice) { b.format = c })
	}), navLink("input", "编辑示例输入", input, "", func() {
		w.local("编辑示例输入", "", func() content {
			cv := content{}
			w.trialInputRows(&cv, f, nil)
			w.trialLimitRow(&cv, f)
			return cv
		})
	}))
	if b.format.id != "json" {
		external := b.format.id != "bupt"
		key := "BUPT_KEY_ID"
		if external {
			key = "BUPT_API_KEY"
		}
		v.rows = append(v.rows, navLink("setup", "准备调用密钥", "设置 "+key, "bupt api key", func() {
			w.exampleKeySetup(f.model.ID, key, external)
		}))
	}
}

func (w *workspace) exampleOutput(v *content, f *trialForm, format exampleChoice, base string) {
	cmd := cmdline(f.args(false, "--format", format.id)...)
	v.rows = append(v.rows, navLink("preview", "查看并复制代码", format.label+" · "+f.model.ID, cmd, func() {
		code, err := f.model.ExampleCode(f.input, format.id, base)
		if err != nil {
			w.notice = whenError(err)
			return
		}
		w.snippetPage(format.label, cmd, code)
	}))
	if !w.Cached {
		v.rows = append(v.rows, navLink("trial", "在 TUI 中试用", "沿用当前模型和输入", "bupt help api model try --all", func() {
			w.openTrialForm(&trialForm{model: f.model, template: f.template, input: copyTrial(f.input)})
		}))
	}
}

func (w *workspace) exampleKeySetup(model, variable string, external bool) {
	created := ""
	w.local("准备调用密钥", "bupt api key", func() content {
		v := content{facts: []string{"选择一把已启用、允许 " + model + " 的 Key。"}}
		if created != "" {
			v.facts = []string{"新 Key 已授权此模型，配置命令已选用 " + created + "。"}
		} else if external {
			v.facts = append(v.facts, "已有完整密钥时，将它设置为程序的 BUPT_API_KEY。")
		}
		v.rows = append(v.rows, navLink("instructions", "使用 bupt 保管的 Key", "复制终端命令以设置 "+variable, "bupt api key", func() {
			w.snippetPage("设置 "+variable, "bupt api key", keySetup(external, created))
		}))
		if created == "" && !w.Cached {
			v.rows = append(v.rows, navLink("new", "为此模型创建 Key", model, cmdline("api", "key", "create", "NAME", "--model", model), func() {
				w.keyFormReturning([]string{model}, w.current(), func(result map[string]any) { created, _ = result["id"].(string) })
			}))
		}
		return v
	})
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

func keySetup(exportKey bool, id string) string {
	text := `bupt api key
printf 'Key ID > '
IFS= read -r BUPT_KEY_ID
export BUPT_KEY_ID`
	if id != "" {
		text = "export BUPT_KEY_ID=" + shellquote.Literal(id)
	}
	if exportKey {
		text += `
BUPT_API_KEY=$(bupt api key export --output - -- "$BUPT_KEY_ID") && export BUPT_API_KEY`
	}
	return text
}
