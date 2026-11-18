package cli

import (
	"bupt.local/cli/internal/domain"
	"fmt"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"strings"
	"unicode"
)

func (a *app) modelTrialCommand(send bool) *cobra.Command {
	cmd := &cobra.Command{Use: "example MODEL_ID", Short: "显示适合所选模型的请求示例", Args: exactArgs(1)}
	if send {
		cmd.Use = "try MODEL_ID"
		cmd.Short = "使用所选 Key 发送一次模型请求"
		cmd.Flags().String("key", "", "由控制器保管且已授权此模型的 Key ID")
		cmd.Flags().Bool("sample", false, "使用该模型的示例输入")
	} else {
		cmd.Flags().String("format", "curl", "curl、python 或 json")
	}
	cmd.Flags().Bool("cached", false, "用缓存目录生成示例")
	texts := cmd.Flags().StringArray("text", nil, "对话内容或向量文本；向量文本可重复指定")
	query := cmd.Flags().String("query", "", "重排问题")
	docs := cmd.Flags().StringArray("document", nil, "重排候选文本，可重复指定")
	maxTokens := cmd.Flags().Int("max-tokens", 128, "对话输出上限")
	top := cmd.Flags().Int("top", 0, "重排返回条数，0 表示全部")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cached, _ := cmd.Flags().GetBool("cached")
		if send && cached && !a.dryRun {
			return usage("在线发送请去掉 --cached；离线可以使用 model example")
		}
		if a.quiet && !send {
			return usage("使用 --format 选择示例输出")
		}
		key, _ := cmd.Flags().GetString("key")
		if send && !a.dryRun && key == "" {
			return usage("model try 需要 --key KEY_ID")
		}
		var catalog domain.PortalPage
		if e := a.rpc(cmd.Context(), "portal.read", map[string]any{"section": "model", "cached": cached}, &catalog); e != nil {
			return e
		}
		model, e := catalog.TrialModel(args[0])
		if e != nil {
			return e
		}
		template, e := domain.TrialFor(model)
		if e != nil {
			return e
		}
		var input domain.TrialInput
		sample, _ := cmd.Flags().GetBool("sample")
		if !send || sample {
			input = template.Sample
		}
		if cmd.Flags().Changed("text") {
			input.Texts = *texts
		}
		if cmd.Flags().Changed("query") {
			input.Query = *query
		}
		if cmd.Flags().Changed("document") {
			input.Documents = *docs
		}
		if cmd.Flags().Changed("max-tokens") {
			input.MaxTokens = *maxTokens
		}
		if cmd.Flags().Changed("top") {
			input.TopN = *top
		}
		request, e := model.TrialRequest(input)
		if e != nil {
			return e
		}
		if send && !a.dryRun {
			var result domain.TrialResult
			p := map[string]any{"model": model.ID, "input": input, "key_id": key, "idempotency_key": uuid.NewString()}
			if e := a.rpc(cmd.Context(), "portal.trial", p, &result); e != nil {
				return e
			}
			if a.json || a.jsonl {
				return a.emit(result)
			}
			if a.quiet {
				return a.emit(result.Model)
			}
			text := strings.Map(func(r rune) rune {
				if unicode.IsControl(r) && r != '\n' && r != '\t' {
					return ' '
				}
				return r
			}, result.Summary())
			_, e = fmt.Fprintln(a.options.Out, text)
			return e
		}
		var guide domain.PortalPage
		e = a.rpc(cmd.Context(), "portal.read", map[string]any{"section": "guide", "cached": cached}, &guide)
		if e != nil && !cached {
			return e
		}
		base, _ := guide.Data["base_url"].(string)
		if a.json || a.jsonl || a.dryRun {
			return a.emit(map[string]any{"model": model.ID, "kind": request.Kind, "method": "POST", "base_url": base, "path": request.Path, "body": request.Body, "dry_run": true})
		}
		format, _ := cmd.Flags().GetString("format")
		snippet, e := request.Code(format, base)
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(a.options.Out, snippet)
		return e
	}
	return cmd
}
