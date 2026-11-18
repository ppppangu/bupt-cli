package domain

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Capacity is a reported usage/limit pair in display units. A missing side
// remains nil, even when the other side is explicitly zero.
type Capacity struct {
	Scope    string   `json:"scope"`
	Resource string   `json:"resource"`
	Used     *float64 `json:"used,omitempty"`
	Limit    *float64 `json:"limit,omitempty"`
	Unit     string   `json:"unit,omitempty"`
}

func (m Capacity) Amounts() string {
	show := func(n *float64) string {
		if n == nil {
			return "—"
		}
		return strconv.FormatFloat(*n, 'f', -1, 64)
	}
	return strings.TrimSpace(show(m.Used) + " / " + show(m.Limit) + " " + m.Unit)
}

func capacityNumber(v any, divisor float64) *float64 {
	n, err := strconv.ParseFloat(fmt.Sprint(v), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	n /= divisor
	return &n
}

func CapacityFromQuota(raw map[string]any) []Capacity {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	type field struct {
		key, label, unit string
		divisor          float64
	}
	var out []Capacity
	for _, key := range keys {
		row, ok := raw[key].(map[string]any)
		if !ok {
			continue
		}
		var fields []field
		var scope string
		switch {
		case strings.HasPrefix(key, "UserQuota:"):
			scope, _ = row["domain"].(string)
			if scope == "" {
				scope = "账号"
			}
			fields = []field{{"appCount", "实例", "", 1}, {"gpu", "GPU", "", 100}, {"cpu", "CPU", "核", 1}, {"memory", "内存", "GiB", 1024}, {"blockStorage", "块存储", "GiB", 1024}, {"fileStorage", "文件存储", "GiB", 1024}}
		case strings.HasPrefix(key, "UserStorageQuota:"):
			scope, _ = row["storageName"].(string)
			if scope == "" {
				scope = "存储空间"
			}
			fields = []field{{"blockStorage", "块存储", "GiB", 1024}, {"fileStorage", "文件存储", "GiB", 1024}}
		default:
			continue
		}
		for _, f := range fields {
			used, limit := row[f.key+"Used"], row[f.key]
			if strings.HasPrefix(key, "UserStorageQuota:") {
				used, limit = row[f.key+"UsedInMb"], row[f.key+"QuotaInMb"]
			} else if used == nil {
				used = row["used"+strings.ToUpper(f.key[:1])+f.key[1:]]
			}
			m := Capacity{Scope: scope, Resource: f.label, Unit: f.unit, Used: capacityNumber(used, f.divisor), Limit: capacityNumber(limit, f.divisor)}
			if m.Used != nil || m.Limit != nil {
				out = append(out, m)
			}
		}
	}
	return out
}
