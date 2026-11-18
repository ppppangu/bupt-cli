package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

func (a *app) resourcesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "resources", Short: "查看账号配额和可用模板所在的资源池", Args: exactArgs(0)}
	cached := cmd.Flags().Bool("cached", false, "Read previously observed quota and catalogue without contacting the school")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		var report domain.ResourceOverview
		if err := a.rpc(cmd.Context(), "resources.overview", map[string]any{"cached": *cached}, &report); err != nil {
			return err
		}
		if a.json || a.jsonl {
			return a.emit(report)
		}
		if a.quiet {
			var ids []domain.ID
			for _, pool := range report.Pools {
				for _, t := range pool.Templates {
					ids = append(ids, t.ID)
				}
			}
			return a.emit(ids)
		}
		out := tabwriter.NewWriter(a.options.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(out, "账号配额（已用 / 上限） · "+scalar(report.QuotaSource))
		for _, m := range report.Capacity {
			fmt.Fprintf(out, "  %s\t%s\t%s\n", scalar(m.Scope), m.Resource, m.Amounts())
		}
		if len(report.Capacity) == 0 {
			fmt.Fprintln(out, "  暂无配额数据")
		}
		fmt.Fprintln(out, "\n资源池与模板 · "+scalar(report.CatalogSource))
		for _, pool := range report.Pools {
			if cmd.Name() == "catalog" {
				fmt.Fprintln(out, scalar(pool.Name))
				fmt.Fprintln(out, "模板 ID\t名称\t版本\tCPU 核\tGPU（1/100 卡）\t内存 MiB\t审批")
				for _, t := range pool.Templates {
					approval := "—"
					if t.AutoApprove != nil {
						if *t.AutoApprove {
							approval = "自动"
						} else {
							approval = "需审批"
						}
					}
					fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", scalar(t.ID), scalar(t.Name), scalar(t.Version), scalar(t.Resources["cpu"]), scalar(t.Resources["gpu"]), scalar(t.Resources["memory"]), approval)
				}
				continue
			}
			var names []string
			for _, t := range pool.Templates {
				name := scalar(t.Name)
				if t.Version != "" {
					name += "@" + scalar(t.Version)
				}
				names = append(names, name)
			}
			fmt.Fprintf(out, "  %s\t%s\n", scalar(pool.Name), strings.Join(names, "  ·  "))
		}
		if len(report.Pools) == 0 {
			if report.CatalogSource == "unavailable" {
				fmt.Fprintln(out, "  尚无模板目录")
			} else {
				fmt.Fprintln(out, "  当前账号可见的模板为 0 个")
			}
		}
		for _, warning := range report.Warnings {
			fmt.Fprintln(a.options.Err, scalar(warning))
		}
		fmt.Fprintln(out, "\nbupt spec show NAME@VERSION   查看模板规格")
		return out.Flush()
	}
	return cmd
}
