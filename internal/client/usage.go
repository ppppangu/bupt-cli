package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

const campusTimezone = "Asia/Shanghai"

var campusClock = time.FixedZone(campusTimezone, 8*60*60)

type UsageWindow struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Timezone string `json:"timezone"`
	Days     int    `json:"days"`
}

func SchoolUsageWindow(now time.Time, days int) UsageWindow {
	today := now.In(campusClock)
	return UsageWindow{Start: today.AddDate(0, 0, 1-days).Format(time.DateOnly), End: today.Format(time.DateOnly), Timezone: campusTimezone, Days: days}
}

type BillRecord struct {
	Source   map[string]any `json:"record"`
	Date     string         `json:"date,omitempty"`
	Points   string         `json:"points,omitempty"`
	Amount   string         `json:"amount,omitempty"`
	Unit     string         `json:"unit,omitempty"`
	Category string         `json:"category"`
}

func (b BillRecord) DisplayAmount() string {
	if b.Amount == "" {
		return "金额未公布"
	}
	if b.Unit == "" {
		return b.Amount + "（单位未公布）"
	}
	return b.Amount + " " + b.Unit
}

type ComputeLedger struct {
	Section      string       `json:"section"`
	Window       UsageWindow  `json:"window"`
	Items        []BillRecord `json:"items"`
	Undated      []BillRecord `json:"undated"`
	PagesRead    int          `json:"pages_read"`
	NextPage     int          `json:"next_page,omitempty"`
	Duplicates   int          `json:"duplicates,omitempty"`
	Complete     bool         `json:"complete"`
	Code         string       `json:"code,omitempty"`
	BilledPoints string       `json:"reported_bill_points,omitempty"`
}

type callUsage func(context.Context, string, any, any) error

type UsageSource struct {
	Source string          `json:"source"`
	State  string          `json:"state"`
	Data   json.RawMessage `json:"data,omitempty"`
	Code   string          `json:"code,omitempty"`
	Window UsageWindow     `json:"requested_window"`
}

func ReadUsage(ctx context.Context, call callUsage, window UsageWindow) ([]UsageSource, error) {
	api := UsageSource{Source: "api", State: "ok", Window: window}
	if err := call(ctx, "portal.read", domain.PortalInput{Section: "usage", Days: window.Days}, &api.Data); err != nil {
		api.State, api.Code = "unavailable", usageCode(err)
	} else {
		var page struct {
			Data domain.PortalUsage `json:"data"`
		}
		if err := json.Unmarshal(api.Data, &page); err != nil {
			api.State, api.Code = "unavailable", "PORTAL_PROTOCOL"
		} else if page.Data.Available != nil && !*page.Data.Available {
			api.State, api.Code = "partial", "API_USAGE_UNAVAILABLE"
		} else if !window.MatchesPortal(page.Data) {
			api.State, api.Code = "partial", "API_WINDOW_UNCONFIRMED"
		}
	}
	compute := UsageSource{Source: "compute", State: "ok", Window: window}
	ledger, err := ReadComputeLedger(ctx, call, "billing", window, "")
	if err != nil {
		compute.State, compute.Code = "unavailable", usageCode(err)
	} else {
		compute.Data = domain.Result(ledger)
		if !ledger.Complete || ledger.Code != "" {
			compute.State, compute.Code = "partial", ledger.Code
		}
	}
	if api.State == "unavailable" && compute.State == "unavailable" {
		return nil, domain.E("USAGE_UNAVAILABLE", "模型用量和算力账单均暂不可用（"+api.Code+" / "+compute.Code+"）")
	}
	return []UsageSource{api, compute}, nil
}

func (w UsageWindow) MatchesPortal(u domain.PortalUsage) bool {
	return w.Days > 0 && u.Days == w.Days && len(u.Dates) == w.Days && u.Dates[0] == w.Start && u.Dates[len(u.Dates)-1] == w.End
}

// ReadComputeLedger follows the school's descending page/limit API. Matches
// are filtered by campus dates here because the school endpoints do not share
// a verified range filter or stable snapshot cursor.
func ReadComputeLedger(ctx context.Context, call callUsage, section string, window UsageWindow, instance domain.ID) (ComputeLedger, error) {
	const pageSize, maxPages = 100, 200
	out := ComputeLedger{Section: section, Window: window, Items: []BillRecord{}, Undated: []BillRecord{}}
	seenIDs := map[string]string{}
	seenPages := map[string]bool{}
	expected, unique := int64(-1), int64(0)
	for page := 1; page <= maxPages; page++ {
		var raw json.RawMessage
		query := struct {
			Section string `json:"section"`
			Page    int    `json:"page"`
			Limit   int    `json:"limit"`
		}{section, page, pageSize}
		err := call(ctx, "compute.read", query, &raw)
		var reply struct {
			Items []map[string]any
			Total json.Number
			Page  int
		}
		if err == nil {
			d := json.NewDecoder(bytes.NewReader(raw))
			d.UseNumber()
			if d.Decode(&reply) != nil || reply.Items == nil || reply.Page != 0 && reply.Page != page {
				err = domain.E("BILLING_PROTOCOL", "学校账单响应缺少当前页的有效记录")
			}
		}
		if err != nil {
			if page == 1 {
				return out, err
			}
			out.Code, out.NextPage = usageCode(err), page
			break
		}
		total, known := reply.Total.Int64()
		if known == nil && total >= 0 {
			if expected < 0 {
				expected = total
			}
			if expected != total {
				out.Code = "BILLING_PAGES_CHANGED"
			}
		}
		signature := string(domain.Result(reply.Items))
		if seenPages[signature] && len(reply.Items) > 0 {
			out.Code, out.NextPage = "BILLING_PAGE_REPEATED", page
			break
		}
		seenPages[signature] = true
		out.PagesRead++
		for _, record := range reply.Items {
			if id := billField(record, "id", "eid"); id != "" {
				original, exists := seenIDs[id]
				current := string(domain.Result(record))
				if exists {
					out.Duplicates++
					if original != current {
						out.Code = "BILLING_RECORD_CHANGED"
					}
					continue
				}
				seenIDs[id] = current
			}
			unique++
			if instance != "" && billField(record, "appId") != string(instance) {
				continue
			}
			bill := billRecord(record)
			if bill.Date == "" {
				out.Undated = append(out.Undated, bill)
			} else if bill.Date >= window.Start && bill.Date <= window.End {
				out.Items = append(out.Items, bill)
			}
		}
		if expected >= 0 && unique >= expected {
			if unique != expected {
				out.Code = "BILLING_PAGES_CHANGED"
			}
			out.Complete = out.Code == ""
			break
		}
		if len(reply.Items) == 0 {
			out.Complete = expected < 0 && out.Code == ""
			if !out.Complete && out.Code == "" {
				out.Code, out.NextPage = "BILLING_PAGE_INCOMPLETE", page
			}
			break
		}
		if page == maxPages {
			out.Code, out.NextPage = "BILLING_PAGE_LIMIT", page+1
		}
	}
	amount := new(big.Rat)
	precision := 0
	known := out.Complete && len(out.Undated) == 0
	hasPoints := false
	for _, bill := range out.Items {
		if bill.Category != "settled" && bill.Category != "bill" {
			continue
		}
		p, digits := BillingPoints(bill.Points)
		if p == nil {
			known = false
			if out.Code == "" {
				switch {
				case bill.Amount == "":
					out.Code = "BILLING_AMOUNT_UNKNOWN"
				case bill.Unit == "":
					out.Code = "BILLING_UNIT_UNKNOWN"
				default:
					out.Code = "BILLING_UNIT_NOT_POINTS"
				}
			}
			continue
		}
		hasPoints = true
		amount.Add(amount, p)
		precision = max(precision, digits)
	}
	if known && hasPoints {
		out.BilledPoints = amount.FloatString(precision)
	}
	if len(out.Undated) > 0 && out.Code == "" {
		out.Code = "BILLING_DATE_UNKNOWN"
	}
	return out, nil
}

func BillDate(raw string) string {
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if ms <= 0 {
			return ""
		}
		if len(raw) == 10 {
			return time.Unix(ms, 0).In(campusClock).Format(time.DateOnly)
		}
		if len(raw) == 13 {
			return time.UnixMilli(ms).In(campusClock).Format(time.DateOnly)
		}
	}
	for _, form := range []string{time.RFC3339Nano, time.DateTime, time.DateOnly} {
		if v, err := time.ParseInLocation(form, raw, campusClock); err == nil {
			return v.In(campusClock).Format(time.DateOnly)
		}
	}
	return ""
}

func billRecord(record map[string]any) BillRecord {
	category := "bill"
	switch strings.ToLower(billField(record, "state", "status")) {
	case "settled", "charged", "paid", "completed":
		category = "settled"
	case "pending", "unsettled", "processing":
		category = "unsettled"
	case "estimated", "estimate":
		category = "estimate"
	case "cancelled", "canceled", "failed", "void":
		category = "void"
	}
	points := billField(record, "usagePoints")
	amount, unit := points, "积分"
	if p, _ := BillingPoints(points); p == nil {
		points = ""
		amount, unit = billField(record, "cost", "amount", "sum"), billField(record, "currency")
		if p, _ := BillingPoints(amount); p == nil {
			amount, unit = "", ""
		}
	}
	return BillRecord{
		Source: record, Date: BillDate(billField(record, "endAt", "createdAt", "startAt", "updatedAt")),
		Points: points, Amount: amount, Unit: unit, Category: category,
	}
}

func billField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		switch v := m[key].(type) {
		case string:
			if v != "" {
				return v
			}
		case json.Number:
			return string(v)
		}
	}
	return ""
}

func BillingPoints(raw string) (*big.Rat, int) {
	if len(raw) == 0 || len(raw) > 128 || strings.ContainsAny(raw, "/_xX") {
		return nil, 0
	}
	number, exponent, exp := strings.Cut(strings.ToLower(raw), "e")
	var power int
	if exp {
		v, err := strconv.Atoi(exponent)
		if err != nil || v < -324 || v > 308 {
			return nil, 0
		}
		power = v
	}
	_, fraction, _ := strings.Cut(number, ".")
	amount, ok := new(big.Rat).SetString(raw)
	if !ok {
		return nil, 0
	}
	return amount, max(0, len(fraction)-power)
}

func usageCode(err error) string {
	var problem *domain.Error
	switch {
	case errors.As(err, &problem):
		return problem.Code
	case errors.Is(err, context.DeadlineExceeded):
		return "TIMEOUT"
	case errors.Is(err, context.Canceled):
		return "INTERRUPTED"
	default:
		return "CONTROLLER_IO"
	}
}
