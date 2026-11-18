package cli

import (
	"fmt"
	"github.com/spf13/cobra"
)

func (a *app) computeDetail(use, description, section string, cluster bool) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: description, Args: exactArgs(1)}
	if cluster {
		cmd.Flags().String("cluster", "", "调度集群 ID")
	}
	if section == "job.log" {
		cmd.Flags().String("stream", "stdout", "stdout 或 stderr")
	}
	if section == "project.show" {
		cmd.Flags().Bool("cached", false, "从已保存的项目快照读取")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		v, _ := cmd.Flags().GetString("cluster")
		if cluster && v == "" {
			return usage("--cluster ID is required")
		}
		stream, _ := cmd.Flags().GetString("stream")
		cached, _ := cmd.Flags().GetBool("cached")
		return a.callAndPrint(cmd, "compute.read", map[string]any{"section": section, "target": args[0], "cluster": v, "stream": stream, "cached": cached})
	}
	return cmd
}
func (a *app) adminQuotaCommands() *cobra.Command {
	cmd := a.computeDetail("quota CLUSTER", "查看集群已发布的账户限额", "admin.limits", false)
	set := &cobra.Command{Use: "set CLUSTER ACCOUNT", Short: "预览或更新一个现有的数值限额", Args: exactArgs(2)}
	field := set.Flags().String("field", "", "学校发布的限额字段")
	value := set.Flags().String("value", "", "新的数值")
	revision := set.Flags().String("revision", "", "预览返回的版本")
	set.RunE = func(cmd *cobra.Command, args []string) error {
		if *field == "" || *value == "" {
			return usage("quota set requires --field and --value")
		}
		if !a.dryRun && *revision == "" {
			return usage("先用 --dry-run 预览，再指定 --revision")
		}
		return a.callAndPrint(cmd, "compute.admin.update", map[string]any{"cluster": args[0], "account": args[1], "field": *field, "value": *value, "revision": *revision})
	}
	cmd.AddCommand(set)
	return cmd
}

func (a *app) featureCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "features", Short: "查看学校对本部署启用的可选栏目", Args: exactArgs(0)}
	cached := cmd.Flags().Bool("cached", false, "读取已保存的配置")
	fresh := cmd.Flags().Bool("refresh", false, "刷新学校配置")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		var flags map[string]bool
		if e := a.rpc(cmd.Context(), "compute.read", map[string]any{"section": "capabilities", "cached": *cached, "refresh": *fresh}, &flags); e != nil {
			return e
		}
		if a.json || a.jsonl || a.quiet {
			return a.emit(flags)
		}
		for _, f := range []struct{ id, title string }{{"app", "算力实例"}, {"pool", "资源池"}, {"hpc", "学校调度"}, {"billing", "计量与账单"}, {"remoteApp", "远程算力"}, {"aiModel", "算力站内模型服务"}, {"dataset", "数据集"}, {"projects", "项目"}, {"prepayment", "预付费账户"}} {
			s := "学校未公布此开关"
			if v, ok := flags[f.id]; ok {
				if v {
					s = "已开放"
				} else {
					s = "未启用"
				}
			}
			fmt.Fprintf(a.options.Out, "%s\t%s\n", f.title, s)
		}
		return nil
	}
	return cmd
}

func (a *app) quotaRequestCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "request QUOTA_ID", Short: "向学校申请更改自己的资源额度", Args: exactArgs(1)}
	for _, f := range []struct{ name, desc string }{{"cpu", "CPU 核数"}, {"gpu", "GPU 卡数，例如 0.2"}, {"memory", "内存，例如 8GiB"}, {"instances", "实例数量"}, {"disk", "块存储容量，例如 10GiB"}, {"file-storage", "文件存储容量，例如 10GiB"}, {"reason", "申请理由"}} {
		cmd.Flags().String(f.name, "", f.desc)
	}
	addTaskFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		p := map[string]any{"target": args[0]}
		copyChangedFlags(cmd, p)
		for flag, field := range map[string]string{"instances": "appCount", "disk": "blockStorage", "file-storage": "fileStorage"} {
			if cmd.Flags().Changed(flag) {
				p[field], _ = cmd.Flags().GetString(flag)
			}
		}
		return a.callAndPrint(cmd, "compute.quota.request", p)
	}
	return cmd
}
func (a *app) approvalCommand(name string) *cobra.Command {
	cmd := &cobra.Command{Use: name + " REQUEST_ID", Short: map[string]string{"approve": "批准学校申请", "reject": "拒绝学校申请"}[name], Args: exactArgs(1)}
	confirm := cmd.Flags().String("confirm", "", "确认目标申请 ID")
	reason := cmd.Flags().String("reason", "", "学校审批意见")
	addTaskFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !a.dryRun && *confirm != args[0] {
			return usage("该操作需要 --confirm REQUEST_ID")
		}
		p := map[string]any{"target": args[0], "confirm": *confirm, "reason": *reason}
		copyChangedFlags(cmd, p)
		return a.callAndPrint(cmd, "compute.admin."+name, p)
	}
	return cmd
}
func (a *app) remoteOrderAction(action string) *cobra.Command {
	cmd := &cobra.Command{Use: action + " ORDER_ID", Short: map[string]string{"cancel": "取消已选订单", "payment-code": "取得现有订单的学校付款码"}[action], Args: exactArgs(1)}
	if action == "cancel" {
		cmd.Flags().String("confirm", "", "确认订单 ID")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		confirm, _ := cmd.Flags().GetString("confirm")
		if action == "cancel" && !a.dryRun && confirm != args[0] {
			return usage("请核对订单并指定 --confirm ORDER_ID")
		}
		return a.callAndPrint(cmd, "compute.remote.order", map[string]any{"action": action, "target": args[0], "confirm": confirm})
	}
	return cmd
}
func (a *app) remoteCheckout(action string) *cobra.Command {
	use := "create NAME"
	if action == "renew" {
		use = "renew INSTANCE_ID"
	}
	cmd := &cobra.Command{Use: use, Short: map[string]string{"create": "按已预览的学校报价创建远程算力订单", "renew": "按已预览的学校报价续期远程实例"}[action], Args: exactArgs(1)}
	units := cmd.Flags().Int("units", 1, "学校计价周期数量")
	quote := cmd.Flags().String("quote", "", "本控制器预览返回的报价 ID")
	if action == "create" {
		cmd.Flags().String("spec", "", "远程资源规格 ID")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		p := map[string]any{"action": action, "units": *units, "quote": *quote}
		if action == "create" {
			p["name"] = args[0]
			p["target"], _ = cmd.Flags().GetString("spec")
		} else {
			p["target"] = args[0]
		}
		if !a.dryRun && *quote == "" {
			return usage("先使用 --dry-run 查看报价，再以 --quote ID 确认下单")
		}
		return a.callAndPrint(cmd, "compute.remote.checkout", p)
	}
	return cmd
}
