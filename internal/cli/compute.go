package cli

import (
	"bupt.local/cli/internal/domain"
	"fmt"
	"github.com/spf13/cobra"
	"strconv"
	"strings"
	"text/tabwriter"
)

func defaultTo(g *cobra.Command, name string) {
	sub, _, _ := g.Find([]string{name})
	g.Flags().AddFlagSet(sub.LocalNonPersistentFlags())
	g.Args = exactArgs(0)
	g.RunE = sub.RunE
	if g.Annotations == nil {
		g.Annotations = map[string]string{}
	}
	g.Annotations["default_child"] = name
}
func (a *app) computeQuery(use, label, section string) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: label, Args: exactArgs(0)}
	page := cmd.Flags().Int("page", 1, "页码")
	limit := cmd.Flags().Int("limit", 25, "每页条数")
	if section == "project" || section == "request" {
		cmd.Flags().Bool("cached", false, "读取已保存的快照")
	}
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		cached, _ := cmd.Flags().GetBool("cached")
		return a.callAndPrint(cmd, "compute.read", map[string]any{"section": section, "page": *page, "limit": *limit, "cached": cached})
	}
	return cmd
}
func (a *app) computeCommand() (*cobra.Command, []*cobra.Command) {
	g := a.statusCommand()
	g.Use = "compute"
	g.Short = "裸算力服务总览"
	g.Args = exactArgs(0)
	catalog := &cobra.Command{}
	catalog.Use = "catalog"
	catalog.Short = "资源池与可申请规格"
	showSpec := a.simpleRPC("show SPEC", "查看模板规格", "spec.show", 1, 1, targetParams)
	catalogList := a.resourcesCommand()
	catalogList.Use = "ls"
	catalog.AddCommand(catalogList, showSpec, a.estimateCommand())
	defaultTo(catalog, "ls")
	instances := a.instanceCommands()
	show, _, _ := instances.Find([]string{"inspect"})
	show.Use = "show INSTANCE"
	list, _, _ := instances.Find([]string{"ls"})
	list.Flags().Bool("cached", false, "读取上次观察到的实例")
	original := list.RunE
	list.RunE = func(cmd *cobra.Command, args []string) error {
		if a.json || a.jsonl || a.ids || cmd.Flags().Changed("columns") || cmd.Flags().Changed("filter") || cmd.Flags().Changed("state") {
			return original(cmd, args)
		}
		cached, _ := cmd.Flags().GetBool("cached")
		var items []domain.Instance
		if e := a.rpc(cmd.Context(), "instance.list", map[string]any{"cached": cached}, &items); e != nil {
			return e
		}
		out := tabwriter.NewWriter(a.options.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(out, "ID\t名称\t状态\t节点资源\t预留\t到期\tSSH")
		for _, i := range items {
			until := "—"
			if !i.ExpiresAt.IsZero() {
				until = i.ExpiresAt.Local().Format("01-02 15:04")
			}
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%t\t%s\t%s\n", scalar(i.ID), scalar(i.Name), scalar(i.State), instanceResources(i), i.Reserved, until, scalar(i.SSHState))
		}
		return out.Flush()
	}
	defaultTo(instances, "ls")
	storage := a.dataCommands()
	storage.Use = "storage"

	defaultTo(storage, "stores")
	run := a.jobCommands()
	run.Use = "job"
	run.Short = "CLI 托管运行、日志和退出结果"
	defaultTo(run, "ls")
	job := a.computeQuery("job", "学校调度作业", "job")
	requests := a.computeQuery("request", "调度提交记录", "job.request")
	requests.AddCommand(a.computeDetail("show ID", "调度提交详情", "job.request.show", false))
	templates := a.computeQuery("template", "调度作业模板", "job.template")
	templates.AddCommand(a.computeDetail("show ID", "模板详情与作业参数", "job.template.show", false))
	job.AddCommand(a.computeQuery("ls", "列出学校调度作业", "job"), a.computeDetail("show ID", "学校作业状态与退出结果", "job.show", true), a.computeDetail("logs ID", "学校作业输出", "job.log", true))
	admin := a.computeQuery("admin", "查询学校实际授予的平台管理身份", "admin")
	admin.AddCommand(a.computeQuery("user", "分页查询已授权的账户", "admin.users"), a.adminQuotaCommands(), a.computeQuery("cluster", "后台计算集群", "admin.cluster"), a.computeQuery("usage", "后台实例计量", "admin.billing"), a.computeQuery("request", "学校申请及其审批状态", "admin.request"), a.approvalCommand("approve"), a.approvalCommand("reject"))
	project := a.computeQuery("project", "我的项目及实际角色", "project")
	project.AddCommand(a.computeDetail("show GROUP", "项目权限、成员与实例", "project.show", false))
	cluster := a.computeQuery("cluster", "学校计算集群", "cluster")
	cluster.AddCommand(a.computeDetail("show ID", "集群详情", "cluster.show", false), a.computeDetail("partition ID", "集群分区", "partition", false))
	billing := a.computeQuery("usage", "实例账单和计量", "billing")
	for _, v := range []struct{ id, label string }{{"storage", "存储账单"}, {"hpc", "调度账单"}, {"account", "账户管理账单"}, {"history", "个人账户流水"}, {"prepayment", "预付费记录"}} {
		billing.AddCommand(a.computeQuery(v.id, v.label, "billing."+v.id))
	}
	datasets := a.computeQuery("dataset", "公共数据集", "dataset")
	datasets.AddCommand(a.computeDetail("show ID", "数据集详情", "dataset.show", false))
	notices := a.computeQuery("notice", "裸算力站内通知", "notice")
	notices.AddCommand(a.computeDetail("show ID", "通知全文", "notice.show", false))
	remote := a.computeQuery("remote", "远程算力商城与我的订单", "remote.order")
	for _, r := range []struct{ id, title string }{{"catalog", "远程可租资源"}, {"instance", "远程实例"}, {"order", "我的远程订单"}} {
		c := a.computeQuery(r.id, r.title, "remote."+r.id)
		c.AddCommand(a.computeDetail("show ID", r.title+"详情", "remote."+r.id+".show", false))
		if r.id == "order" {
			c.AddCommand(a.remoteOrderAction("cancel"), a.remoteOrderAction("payment-code"), a.remoteCheckout("create"), a.remoteCheckout("renew"))
		}
		remote.AddCommand(c)
	}
	llm := a.computeQuery("llm", "裸算力站内附加模型账户的独立账目", "llm.account")
	for _, r := range []struct{ id, title string }{{"model", "该站模型目录"}, {"key", "该站 Key 元数据"}, {"spend", "该站模型消费记录"}} {
		llm.AddCommand(a.computeQuery(r.id, r.title, "llm."+r.id))
	}
	scheduler := &cobra.Command{Use: "scheduler", Short: "学校调度系统"}
	scheduler.AddCommand(job, requests, templates)
	quota := a.simpleRPC("quota", "个人与存储额度", "quota.get", 0, 0, nil)
	quota.RunE = a.personalQuota
	quota.AddCommand(a.quotaRequestCommand())
	repair := a.sshRepairCommand()
	repair.Use = "prepare-ssh INSTANCE"
	instances.AddCommand(advancedCommand(repair), advancedCommand(a.cloneCommand()), advancedCommand(a.resizeCommand()))
	g.AddCommand(catalog, instances, storage, run, scheduler, billing, quota)
	for _, c := range []*cobra.Command{a.featureCommand(), datasets, notices, llm, a.computeQuery("request", "平台申请与审批状态", "request"),
		a.presetCommands(), a.leaseCommands(), a.taskCommands(), a.auditCommands(), a.snapshotCommands(), a.serviceCommands()} {
		g.AddCommand(advancedCommand(c))
	}
	admin.AddCommand(a.portalQuery("portal", "查看模型门户管理身份", "admin"))
	return g, []*cobra.Command{advancedCommand(admin), advancedCommand(cluster), advancedCommand(remote), advancedCommand(project)}
}

func instanceResources(i domain.Instance) string {
	var parts []string
	for _, f := range []struct {
		key, unit string
		ratio     float64
	}{{"cpu", " 核", 1}, {"gpu", " GPU", 100}, {"memory", " GiB", 1024}} {
		var sum float64
		var found bool
		for _, n := range i.Nodes {
			if amount, e := strconv.ParseFloat(fmt.Sprint(n.Resources[f.key]), 64); e == nil {
				sum += amount
				found = true
			}
		}
		if found {
			parts = append(parts, strconv.FormatFloat(sum/f.ratio, 'f', -1, 64)+f.unit)
		}
	}
	if len(parts) == 0 {
		return "—"
	}
	return strings.Join(parts, " / ")
}
