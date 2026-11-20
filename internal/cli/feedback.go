package cli

import (
	"context"
	"fmt"
	"time"

	"bupt.local/cli/internal/feedback"
	"github.com/spf13/cobra"
)

func (a *app) feedbackCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use: "feedback", Short: "联系作者和生成反馈邮件模板", Args: exactArgs(0),
		Annotations: map[string]string{"skip_config": "true"},
	}
	flags := cmd.Flags()
	template := flags.Bool("template", false, "将纯文本反馈模板写到 stdout")
	diagnostics := flags.Bool("diagnostics", false, "附加脱敏诊断摘要")
	category := flags.String("category", "", "问题类别 bug、usage、platform 或 suggestion")
	at := flags.String("at", "", "实际发生时间（RFC3339）")
	var issue feedback.Incident
	flags.StringVar(&issue.Operation, "operation", "", "规范操作名，例如 bupt ssh")
	flags.StringVar(&issue.Code, "code", "", "错误码")
	flags.StringVar(&issue.RequestID, "request-id", "", "请求编号")
	flags.StringVar(&issue.RunID, "run-id", "", "操作编号")
	flags.StringVar(&issue.TaskID, "task-id", "", "任务编号")
	completeValues(cmd, "category", "bug", "usage", "platform", "suggestion")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if !*template {
			for _, name := range []string{"diagnostics", "category", "operation", "code", "request-id", "run-id", "task-id", "at"} {
				if flags.Changed(name) {
					return usage("用 --template 生成带上下文的反馈模板")
				}
			}
			if a.json || a.jsonl {
				return a.emit(map[string]any{"email": feedback.Email, "scope": feedback.Scope, "school_support": feedback.SchoolSupport})
			}
			_, err := fmt.Fprintf(a.options.Out, "bupt 问题反馈\n\n%s\n联系邮箱  %s\n%s\n\n脱敏诊断  bupt doctor --report\n邮件模板  bupt feedback --template\n",
				feedback.Scope, feedback.Email, feedback.SchoolSupport)
			return err
		}
		if a.json || a.jsonl {
			return usage("--template 输出纯文本，请单独使用")
		}
		safe := issue.Safe()
		if *at != "" {
			when, err := time.Parse(time.RFC3339, *at)
			if err != nil {
				return usage("--at 使用 RFC3339 格式")
			}
			safe.Time = when
		}
		chosen := safe.Category()
		if *category != "" {
			var ok bool
			chosen, ok = feedback.FindCategory(*category)
			if !ok {
				return usage("类别可选 bug、usage、platform 或 suggestion")
			}
		}
		draft := feedback.Draft{Category: chosen, Incident: safe, Version: a.options.Version}
		if *diagnostics {
			r := a.supportReport(cmd, false)
			draft.Report = &r
		}
		_, err := fmt.Fprint(a.options.Out, draft.Text())
		return err
	}
	return cmd
}

func (a *app) supportReport(cmd *cobra.Command, offline bool) feedback.Report {
	configErr := a.loadConfig(cmd)
	var check func(context.Context) error
	if !offline {
		check = func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, a.timeout)
			defer cancel()
			b, err := a.getBackend()
			if err != nil {
				return err
			}
			return feedback.CheckController(ctx, b.Call)
		}
	}
	return feedback.Collect(cmd.Context(), a.options.Version, configErr, check)
}
