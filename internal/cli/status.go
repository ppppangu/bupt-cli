package cli

import (
	"bupt.local/cli/internal/domain"
	"fmt"
	"github.com/spf13/cobra"
	"slices"
	"strings"
	"text/tabwriter"
)

func (a *app) statusCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "status [INSTANCE|RUN]", Short: "Show login, instances and managed work in one view", Args: rangeArgs(0, 1)}
	cmd.Flags().Bool("cached", false, "Read saved state without contacting the school")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		cached, _ := cmd.Flags().GetBool("cached")
		p := map[string]any{"cached": cached}
		if cmd.Name() == "compute" {
			p["compute"] = true
		}
		if len(args) > 0 {
			p["target"] = args[0]
		}
		var report domain.StatusReport
		if e := a.rpc(cmd.Context(), "status.get", p, &report); e != nil {
			return e
		}
		if a.json || a.jsonl {
			return a.emit(report)
		}
		if a.quiet {
			return a.emit(report.Platform)
		}
		out := tabwriter.NewWriter(a.options.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintf(out, "PROFILE\t%s\nCONTROLLER\t%s\nWEBVPN\t%s\n裸算力会话\t%s\nDATA\t%s\n", scalar(report.Profile), scalar(report.Controller), scalar(report.Gateway), scalar(report.Platform), report.Source)
		if len(args) == 0 {
			fmt.Fprintf(out, "API 门户\t%s", scalar(report.API.State))
			if !report.API.CheckedAt.IsZero() {
				fmt.Fprintf(out, "（%s）", report.API.CheckedAt.Local().Format("01-02 15:04"))
			}
			fmt.Fprintln(out)
		}
		if report.AuthReason != "" {
			fmt.Fprintf(out, "AUTH REASON\t%s\n", scalar(report.AuthReason))
		}
		active := 0
		for _, run := range report.Runs {
			if !displayRunEnded(run.State) {
				active++
			}
		}
		fmt.Fprintf(out, "WORK\t%d active runs, %d pending tasks\n", active, report.PendingTasks)
		if len(args) == 0 && len(report.Capacity) > 0 {
			var parts []string
			firstScope := report.Capacity[0].Scope
			for _, m := range report.Capacity {
				if m.Scope == firstScope && (m.Resource == "实例" || m.Resource == "GPU" || m.Resource == "CPU" || m.Resource == "内存") {
					parts = append(parts, m.Resource+" "+m.Amounts())
				}
			}
			if len(parts) > 0 {
				fmt.Fprintf(out, "配额（已用 / 上限，%s）\t%s\n", scalar(firstScope), strings.Join(parts, "  ·  "))
			}
		}
		if e := out.Flush(); e != nil {
			return e
		}
		if len(report.Instances) > 0 {
			fmt.Fprintln(a.options.Out)
			rows := []map[string]any{}
			for _, i := range report.Instances {
				rows = append(rows, map[string]any{"id": i.ID, "name": i.Name, "state": i.State, "ssh_state": i.SSHState, "reserved": i.Reserved})
			}
			if e := a.emit(rows); e != nil {
				return e
			}
		}
		if len(args) > 0 && len(report.Runs) > 0 {
			runs := slices.Clone(report.Runs)
			slices.SortStableFunc(runs, func(a, b domain.Run) int { return b.CreatedAt.Compare(a.CreatedAt) })
			fmt.Fprintln(a.options.Out, "\n最近的托管周期")
			rows := []map[string]any{}
			for _, r := range runs[:min(3, len(runs))] {
				rows = append(rows, map[string]any{"id": r.ID, "name": r.Name, "state": r.State, "deadline": r.Deadline.Local().Format("01-02 15:04"), "cleanup": r.CleanupState})
			}
			if e := a.emit(rows); e != nil {
				return e
			}
		}
		for _, warning := range report.Warnings {
			fmt.Fprintln(a.options.Err, "Warning:", scalar(warning))
		}
		for _, run := range report.Runs {
			if run.LastError != "" && !displayRunEnded(run.State) {
				fmt.Fprintf(a.options.Err, "run %s: %s; bupt job inspect %s\n", run.ID, scalar(run.LastError), run.ID)
			}
		}
		if !cached && report.Platform != "authenticated" {
			fmt.Fprintln(a.options.Err, "Next: bupt login (cached SSH routes remain independent of school login)")
		}
		return nil
	}
	return cmd
}
func displayRunEnded(s string) bool {
	switch s {
	case "complete", "completed", "done", "expired", "stopped", "released", "deleted", "failed", "cancelled", "canceled":
		return true
	}
	return false
}
