package client

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

func TestComputeLedgerPaginatesAndAccountsForUncertainty(t *testing.T) {
	window := SchoolUsageWindow(time.Date(2026, 11, 19, 17, 0, 0, 0, time.UTC), 7)
	if window.Start != "2026-11-14" || window.End != "2026-11-20" || window.Timezone != "Asia/Shanghai" {
		t.Fatal(window)
	}
	record := func(id int, time, points, state string) map[string]any {
		return map[string]any{"id": id, "appId": "20", "endAt": time, "usagePoints": points, "state": state}
	}
	first := record(3, "2026-11-19T17:00:00Z", "0.100000000000000001", "settled")
	second := record(2, "2026-11-18", "0.2", "settled")
	unsettled := record(1, "2026-11-18", "99", "unsettled")
	for _, mode := range []string{"complete", "timeout", "repeated", "undated", "changed"} {
		t.Run(mode, func(t *testing.T) {
			var queries []int
			call := func(_ context.Context, method string, query, out any) error {
				if method != "compute.read" {
					t.Fatal(method)
				}
				var req struct {
					Section     string
					Page, Limit int
				}
				if err := json.Unmarshal(domain.Result(query), &req); err != nil || req.Section != "billing" || req.Limit != 100 {
					t.Fatal(req, err)
				}
				queries = append(queries, req.Page)
				var items []map[string]any
				switch req.Page {
				case 1:
					items = []map[string]any{first}
				case 2:
					switch mode {
					case "timeout":
						return context.DeadlineExceeded
					case "repeated":
						items = []map[string]any{first}
					case "changed":
						items = []map[string]any{record(3, "2026-11-20", "9", "settled"), second, unsettled}
					case "undated":
						items = []map[string]any{first, record(2, "", "9", "settled"), unsettled}
					default:
						items = []map[string]any{first, second, unsettled}
					}
				default:
					return domain.E("FIXTURE", fmt.Sprintf("unexpected page %d", req.Page))
				}
				*out.(*json.RawMessage) = domain.Result(map[string]any{"page": req.Page, "total": 3, "items": items})
				return nil
			}
			ledger, err := ReadComputeLedger(t.Context(), call, "billing", window, "20")
			if err != nil || fmt.Sprint(queries) != "[1 2]" {
				t.Fatal(ledger, err, queries)
			}
			switch mode {
			case "complete":
				if !ledger.Complete || ledger.BilledPoints != "0.300000000000000001" || ledger.Duplicates != 1 || len(ledger.Items) != 3 || ledger.Items[0].Date != window.End {
					t.Fatal(ledger)
				}
			case "timeout":
				if ledger.Complete || ledger.BilledPoints != "" || ledger.Code != "TIMEOUT" || ledger.NextPage != 2 || len(ledger.Items) != 1 {
					t.Fatal(ledger)
				}
			case "repeated":
				if ledger.Complete || ledger.Code != "BILLING_PAGE_REPEATED" || ledger.NextPage != 2 || len(ledger.Items) != 1 {
					t.Fatal(ledger)
				}
			case "undated":
				if !ledger.Complete || ledger.Code != "BILLING_DATE_UNKNOWN" || ledger.BilledPoints != "" || len(ledger.Undated) != 1 {
					t.Fatal(ledger)
				}
			case "changed":
				if ledger.Complete || ledger.Code != "BILLING_RECORD_CHANGED" || ledger.BilledPoints != "" {
					t.Fatal(ledger)
				}
			}
		})
	}
}

func TestSchoolCostWithoutCurrencyRetainsValueWithoutInferringPoints(t *testing.T) {
	window := SchoolUsageWindow(time.Now(), 7)
	ledger, err := ReadComputeLedger(t.Context(), func(_ context.Context, _ string, _ any, result any) error {
		*result.(*json.RawMessage) = domain.Result(map[string]any{"total": 1, "items": []map[string]any{
			{"id": 7, "startAt": window.End, "state": 2, "cost": json.Number("1.2345000000000000001")},
		}})
		return nil
	}, "billing", window, "")
	if err != nil || !ledger.Complete || ledger.Code != "BILLING_UNIT_UNKNOWN" || ledger.BilledPoints != "" || len(ledger.Items) != 1 {
		t.Fatal(ledger, err)
	}
	record := ledger.Items[0]
	if record.Points != "" || record.Unit != "" || record.Amount != "1.2345000000000000001" ||
		record.DisplayAmount() != "1.2345000000000000001（单位未公布）" {
		t.Fatal(record)
	}
}
