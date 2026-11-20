package cli

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

func (a *app) portalQuery(use, label, section string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: label, Args: exactArgs(0)}
	if strings.Contains(use, " ") {
		cmd.Args = exactArgs(1)
	}
	cached := cmd.Flags().Bool("cached", false, "只读控制器保存的快照")
	fresh := cmd.Flags().Bool("refresh", false, "刷新本栏目")
	if section == "usage" {
		cmd.Flags().Int("days", 30, "用量统计天数（1 到 30）")
	}
	if section == "news" {
		cmd.Flags().Int("limit", 20, "每页条数")
		cmd.Flags().String("cursor", "", "下一页游标")
		cmd.Flags().String("only", "", "按门户支持的状态筛选")
		cmd.Flags().String("order", "", "按门户支持的顺序排列")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if *cached && *fresh {
			return usage("choose --cached or --refresh")
		}
		p := map[string]any{"section": section, "cached": *cached, "refresh": *fresh}
		for _, name := range []string{"days", "limit", "cursor", "only", "order"} {
			if f := cmd.Flags().Lookup(name); f != nil {
				if f.Value.Type() == "int" {
					p[name], _ = cmd.Flags().GetInt(name)
				} else {
					p[name], _ = cmd.Flags().GetString(name)
				}
			}
		}
		if section == "news.show" {
			p["target"] = args[0]
		}
		var v domain.PortalPage
		if err := a.rpc(cmd.Context(), "portal.read", p, &v); err != nil {
			return err
		}
		target := ""
		if len(args) > 0 {
			target = args[0]
		}
		return a.displayPortal(v, target)
	}
	return cmd
}
func (a *app) apiCommand() *cobra.Command {
	g := a.portalQuery("api", "大模型 API 服务总览", "overview")
	models := a.portalQuery("model", "查询全部可见模型、能力、上下文、价格和健康状态", "model")

	models.AddCommand(a.modelTrialCommand(false), a.modelTrialCommand(true))
	models.AddCommand(a.portalQuery("show ID", "查看模型指标和采样历史", "model"), a.portalQuery("ls", "列出模型", "model"))
	keys := a.portalQuery("key", "管理 API Key、模型范围和控制器保管状态", "key")

	keys.AddCommand(a.portalQuery("ls", "列出 API Key", "key"), a.portalQuery("show ID", "查看 Key 的状态和模型范围", "key"))
	create := a.portalMutation("create NAME", "为指定模型创建 Key", "key.create")
	create.Flags().StringSlice("model", nil, "允许的真实模型 ID，可重复")
	keys.AddCommand(create)
	for _, op := range []struct{ name, label string }{{"enable", "启用 Key"}, {"disable", "停用 Key"}, {"delete", "永久删除 Key"}} {
		keys.AddCommand(a.portalMutation(op.name+" ID", op.label, "key."+op.name))
	}
	keys.AddCommand(advancedCommand(a.exportPortalKey()))
	defaultTo(models, "ls")
	defaultTo(keys, "ls")
	pref := a.portalQuery("display", "查看本人在排行榜中的展示方式", "preference")
	pref.AddCommand(a.portalMutation("set MODE", "设置 anonymous、partial 或 real", "preference.set"))
	leaderboard := a.portalQuery("leaderboard", "查看门户排行榜", "leaderboard")
	leaderboard.AddCommand(pref)
	g.AddCommand(models, keys, a.portalQuery("usage", "查询余额、调用次数和按模型统计的用量", "usage"), advancedCommand(a.portalQuery("guide", "取得公网网关地址及 Python、curl 接入示例", "guide")), leaderboard)
	return g
}
func (a *app) portalCommonCommands() []*cobra.Command {
	news := a.portalQuery("news", "查看平台动态", "news")
	news.AddCommand(a.portalQuery("show ID", "阅读一条平台动态", "news.show"), a.portalMutation("read ID|all", "标记已读", "news.read"))
	ag := a.portalQuery("agreement", "查看门户当前的服务协议", "agreement")
	ag.AddCommand(a.portalMutation("accept ID", "确认已阅读并接受指定版本", "agreement.accept"))
	return []*cobra.Command{a.portalQuery("account", "查看已登录的门户账号", "account"), news, ag}
}
func (a *app) portalMutation(use, label, op string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: label, Args: exactArgs(1)}
	if op == "key.delete" || op == "agreement.accept" {
		cmd.Flags().String("confirm", "", "确认目标 ID")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		p := map[string]any{"action": op, "idempotency_key": uuid.NewString()}
		switch op {
		case "key.create":
			models, _ := cmd.Flags().GetStringSlice("model")
			if len(models) == 0 || strings.TrimSpace(args[0]) == "" {
				return usage("key create NAME requires --model MODEL_ID")
			}
			p["name"] = args[0]
			p["models"] = models
		case "preference.set":
			p["mode"] = args[0]
		default:
			p["target"] = args[0]
		}
		if f := cmd.Flags().Lookup("confirm"); f != nil {
			v, _ := cmd.Flags().GetString("confirm")
			if !a.dryRun && v != args[0] {
				return usage("confirm the selected ID with --confirm ID")
			}
			p["confirm"] = v
		}
		return a.callAndPrint(cmd, "portal.change", p)
	}
	return cmd
}
func (a *app) exportPortalKey() *cobra.Command {
	cmd := &cobra.Command{Use: "export ID", Short: "将控制器保管的 Key 写入新私有文件或显式管道", Args: exactArgs(1)}
	dest := cmd.Flags().StringP("file", "o", "", "新文件路径，或用 - 写入管道")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if *dest == "" {
			return usage("key export requires --file FILE or --file -")
		}
		pipe := *dest == "-"
		if pipe && (isTerminal(a.options.Out) || a.json || a.jsonl || a.ids) {
			return usage("--file - requires a pipe or redirected stdout, without output-format flags")
		}
		if a.dryRun {
			return a.emit(map[string]any{"dry_run": true, "id": args[0], "output": *dest})
		}
		var f *os.File
		if !pipe {
			var e error
			f, e = os.OpenFile(*dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
		}
		complete := false
		if f != nil {
			defer func() {
				f.Close()
				if !complete {
					_ = os.Remove(*dest)
				}
			}()
		}
		var v struct{ Secret string }
		if e := a.rpc(cmd.Context(), "portal.export", map[string]any{"target": args[0]}, &v); e != nil {
			return e
		}
		if v.Secret == "" {
			return domain.E("KEY_NOT_STORED", "控制器未返回完整值。")
		}
		if pipe {
			_, e := fmt.Fprintln(a.options.Out, v.Secret)
			return e
		}
		if _, e := fmt.Fprintln(f, v.Secret); e != nil {
			return e
		}
		if e := f.Close(); e != nil {
			return e
		}
		complete = true
		return a.emit(map[string]any{"id": args[0], "output": *dest})
	}
	return cmd
}

func point(n *float64) string {
	if n == nil {
		return "—"
	}
	return fmt.Sprint(*n)
}
func (a *app) displayPortal(v domain.PortalPage, target string) error {
	if a.field == "balance" {
		if v.Account == nil || v.Account.Balance == nil {
			return domain.E("FIELD_UNAVAILABLE", "学校尚未返回余额")
		}
		_, err := fmt.Fprintln(a.options.Out, *v.Account.Balance)
		return err
	}
	if target != "" && (v.Section == "model" || v.Section == "key") {
		if v.Section == "model" {
			for _, m := range v.Models {
				if m.ID == target {
					return a.emit(m)
				}
			}
		} else {
			for _, k := range v.Keys {
				if k.ID == target {
					return a.emit(k)
				}
			}
		}
		return domain.E("NOT_FOUND", "当前账号没有该 ID。")
	}
	if a.jsonl || a.ids {
		if v.Section == "model" {
			if len(v.Models) == 0 {
				return nil
			}
			return a.emit(v.Models)
		}
		if v.Section == "key" {
			if len(v.Keys) == 0 {
				return nil
			}
			return a.emit(v.Keys)
		}

	}
	if a.json || a.jsonl || a.ids {
		return a.emit(v)
	}
	for _, warning := range v.Warnings {
		fmt.Fprintln(a.options.Err, scalar(warning))
	}
	out := tabwriter.NewWriter(a.options.Out, 0, 4, 2, ' ', 0)
	switch v.Section {
	case "model":
		fmt.Fprintln(out, "模型 ID\t名称\t能力\t上下文\t输入价\t输出价\t健康状态")
		for _, m := range v.Models {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", scalar(m.ID), scalar(m.Name), scalar(m.Kind), scalar(m.Context), point(m.InputPrice), point(m.OutputPrice), scalar(m.Health))
		}
		fmt.Fprintln(out, "价格单位为点 / 百万 Token")
	case "key":
		fmt.Fprintln(out, "ID\t名称\t状态\t允许模型\t累计用量（点）\t控制器保管")
		for _, k := range v.Keys {
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%t\n", scalar(k.ID), scalar(k.Name), scalar(k.Status), scalar(strings.Join(k.Models, ", ")), point(k.Used), k.Stored)
		}
		if len(v.Keys) == 0 {
			fmt.Fprintln(out, "暂无 API Key")
		}
	case "usage", "overview":
		if v.Account != nil {
			fmt.Fprintf(out, "余额（点）\t%s\n", point(v.Account.Balance))
		}
		for _, f := range []struct{ k, label string }{{"days", "统计天数"}, {"periodCalls", "调用次数"}, {"periodTokens", "Token 用量"}, {"successRate", "成功率"}, {"modelDistribution", "按模型分布"}, {"inputSeries", "每日输入 Token"}, {"outputSeries", "每日输出 Token"}} {
			if val, ok := v.Data[f.k]; ok {
				fmt.Fprintf(out, "%s\t%s\n", f.label, scalar(val))
			}
		}
		if v.Data["usageAvailable"] == false {
			fmt.Fprintln(out, "用量数据暂不可用")
		}
		if v.Section == "overview" {
			fmt.Fprintf(out, "API Key\t%d\n", len(v.Keys))
			a.humanHint("下一步  bupt api model 查询模型，bupt api usage 查询用量，bupt api key 管理密钥")
		}
	case "guide":
		fmt.Fprintln(out, "Base URL\t"+scalar(v.Data["base_url"]))
		fmt.Fprintln(out, scalar(v.Data["network"]))
		fmt.Fprintln(out, v.Data["curl"])
		fmt.Fprintln(out, v.Data["python"])
	case "account":
		return a.emit(v.Account)
	default:
		return a.emit(v.Data)
	}
	fmt.Fprintf(out, "\n%s · %s\n", scalar(v.Source), v.ObservedAt.Local().Format("2006-01-02 15:04"))
	return out.Flush()
}
