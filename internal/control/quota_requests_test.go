package control

import (
	"bupt.local/cli/internal/domain"
	"encoding/json"
	"testing"
)

func TestQuotaRequestBindsActualOwnerAndConvertsUnits(t *testing.T) {
	row := map[string]any{"userId": json.Number("7"), "domain": "", "gpu": 10, "memory": 2048}
	s := domain.Snapshot{Identity: domain.Identity{UID: "7"}, Quota: map[string]any{"UserQuota:42": row}}
	p := params(map[string]any{"target": "42", "gpu": "0.2", "memory": "8GiB", "reason": "研究"})
	r, e := quotaRequest(s, p)
	if e != nil || r.Type != "CreateUserOpRequestTask" {
		t.Fatal(r, e)
	}
	var data map[string]any
	if json.Unmarshal([]byte(r.Payload["data"].(string)), &data) != nil || data["gpu"] != float64(20) || data["memory"] != float64(8192) {
		t.Fatal(data)
	}
	row["userId"] = json.Number("8")
	if _, e = quotaRequest(s, p); e == nil {
		t.Fatal("accepted another user's quota")
	}
}
