package cli

import (
	"fmt"
	"text/tabwriter"

	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

func (a *app) resourcesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "resources", Short: "查看账号配额和可用模板所在的资源池", Args: exactArgs(0)}
	group := cmd.Flags().String("group-id", "", "列出指定项目的模板")
	cached := cmd.Flags().Bool("cached", false, "Read previously observed quota and catalogue without contacting the school")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if *group != "" {
			if *cached {
				return usage("项目模板查询需要实时读取")
			}
			return a.callAndPrint(cmd, "spec.list", map[string]any{"group_id": *group})
		}
		var report domain.ResourceOverview
		if err := a.rpc(cmd.Context(), "resources.overview", map[string]any{"cached": *cached}, &report); err != nil {
			return err
		}
		if a.jsonl {
			var templates []domain.TemplateSummary
			for _, pool := range report.Pools {
				templates = append(templates, pool.Templates...)
			}
			if templates == nil {
				templates = []domain.TemplateSummary{}
			}
			return a.emit(templates)
		}
		if a.json {
			return a.emit(map[string]any{"pools": report.Pools, "source": report.CatalogSource, "observed_at": report.CatalogAt})
		}
		if a.ids {
			ids := []domain.ID{}
			for _, pool := range report.Pools {
				for _, t := range pool.Templates {
					ids = append(ids, t.ID)
				}
			}
			return writeIDLines(a.options.Out, ids)
		}
		out := tabwriter.NewWriter(a.options.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(out, "\n资源池与模板 · "+scalar(report.CatalogSource))
		for _, pool := range report.Pools {
			fmt.Fprintln(out, scalar(pool.Name))
			fmt.Fprintln(out, "模板 ID\t名称\t版本\tCPU 核\tGPU（1/100 卡）\t内存 MiB\t价格\t审批")
			for _, t := range pool.Templates {
				approval := "—"
				if t.AutoApprove != nil {
					if *t.AutoApprove {
						approval = "自动"
					} else {
						approval = "需审批"
					}
				}
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\t%s\t待估价\t%s\n", scalar(t.ID), scalar(t.Name), scalar(t.Version), scalar(t.Resources["cpu"]), scalar(t.Resources["gpu"]), scalar(t.Resources["memory"]), approval)
			}
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
		a.humanHint("下一步  bupt compute catalog estimate --spec SPEC")
		return out.Flush()
	}
	return cmd
}
