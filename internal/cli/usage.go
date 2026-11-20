package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"text/tabwriter"
	"time"

	"bupt.local/cli/internal/client"
	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

func (a *app) personalQuota(cmd *cobra.Command, _ []string) error {
	var raw map[string]any
	if err := a.rpc(cmd.Context(), "quota.get", nil, &raw); err != nil {
		return err
	}
	capacity := domain.CapacityFromQuota(raw)
	if capacity == nil {
		capacity = []domain.Capacity{}
	}
	return a.emit(capacity)
}

func (a *app) usageCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "usage", Short: "查看模型余额与算力账单", Args: exactArgs(0)}
	days := cmd.Flags().Int("days", 30, "最近天数（1 到 30，学校时区）")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if *days < 1 || *days > 30 {
			return usage("--days 取 1 到 30")
		}
		window := client.SchoolUsageWindow(time.Now(), *days)
		ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
		defer cancel()
		rows, err := client.ReadUsage(ctx, a.rpcRequest, window)
		if err != nil {
			return err
		}
		if a.json {
			return a.emit(map[string]any{"api": rows[0], "compute": rows[1]})
		}
		if a.jsonl {
			return a.emit(rows)
		}
		out := tabwriter.NewWriter(a.options.Out, 0, 4, 2, ' ', 0)
		fmt.Fprintf(out, "查询范围\t%s 至 %s（%s）\n", window.Start, window.End, window.Timezone)
		fmt.Fprintln(out, "\n模型 API")
		if rows[0].State != "unavailable" {
			var page domain.PortalContent[domain.PortalUsage]
			if err := json.Unmarshal(rows[0].Data, &page); err != nil {
				return domain.E("PROTOCOL_ERROR", "模型用量响应无效")
			}
			balance := "—"
			if page.Account != nil {
				balance = point(page.Account.Balance)
			}
			fee := "—"
			feeLabel := "本期扣费（积分）"
			if page.Data.Available == nil || *page.Data.Available {
				fee = point(page.Data.CostPoints)
			}
			if rows[0].State == "partial" {
				feeLabel = "门户报告扣费（积分，日期待核对）"
			}
			fmt.Fprintf(out, "余额（积分）\t%s\n%s\t%s\n", balance, feeLabel, fee)
		}
		if rows[0].Code != "" {
			fmt.Fprintf(out, "用量说明\t%s\n", rows[0].Code)
		}
		fmt.Fprintln(out, "\n裸算力")
		if rows[1].State != "unavailable" {
			var ledger client.ComputeLedger
			if err := json.Unmarshal(rows[1].Data, &ledger); err != nil {
				return domain.E("PROTOCOL_ERROR", "算力账单响应无效")
			}
			amount := ledger.BilledPoints
			if amount == "" {
				amount = "—"
			}
			fmt.Fprintf(out, "范围内账单\t%d 条（已读 %d 页）\n已公布计量（积分）\t%s\n", len(ledger.Items), ledger.PagesRead, amount)
			for _, bill := range append(ledger.Items, ledger.Undated...) {
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", scalar(bill.Date), scalar(bill.Source["appName"]), scalar(bill.DisplayAmount()), bill.Category)
			}
			if ledger.NextPage != 0 {
				fmt.Fprintf(out, "待核对页\t%d\n", ledger.NextPage)
			}
		}
		if rows[1].Code != "" {
			fmt.Fprintf(out, "账单说明\t%s\n", rows[1].Code)
		}
		return out.Flush()
	}
	return cmd
}
