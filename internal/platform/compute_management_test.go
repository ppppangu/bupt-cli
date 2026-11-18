package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bupt.local/cli/internal/domain"
)

func TestAdminLimitRequiresRealRoleAndReviewedRevision(t *testing.T) {
	var admin bool
	writes := 0
	cpu := 2
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/user":
			writeJSON(w, map[string]any{"code": 1, "uid": 7, "eid": 17})
		case "/auth/admin/verify":
			if !admin {
				w.WriteHeader(401)
				return
			}
			writeJSON(w, map[string]any{"retCode": 0, "data": map[string]any{"userId": 7}})
		case "/api/hpc/cluster/2/resourceLimits":
			writeJSON(w, map[string]any{"code": 0, "data": []map[string]any{{"account": "lab", "limit": map[string]int{"cpu": cpu, "ram": 8}}}})
		case "/api/hpc/cluster/2/resourceLimits/lab":
			var v map[string]int
			if !admin || r.Method != "PUT" || json.NewDecoder(r.Body).Decode(&v) != nil || v["ram"] != 8 || v["cpu"] != 4 {
				t.Error("unreviewed limits changed")
			}
			writes++
			cpu = 4
			writeJSON(w, map[string]int{"code": 0})
		default:
			t.Fatal("unexpected route", r.URL.Path)
		}
	}))
	defer server.Close()
	c, _ := New(domain.Profile{Name: "a", PlatformURL: server.URL}, nil)
	c.http.Transport = server.Client().Transport
	defer c.Close()
	_, e := c.AdminLimits(t.Context(), "2")
	assertCode(t, e, "ADMIN_SCOPE_REQUIRED")
	admin = true
	v, e := c.UpdateAdminLimit(t.Context(), "2", "lab", "cpu", "4", "", true)
	if e != nil || writes != 0 {
		t.Fatal("preview", e)
	}
	_, e = c.UpdateAdminLimit(t.Context(), "2", "lab", "cpu", "4", "stale", false)
	assertCode(t, e, "STALE_QUOTA")
	_, e = c.UpdateAdminLimit(t.Context(), "2", "lab", "cpu", "4", v["revision"].(string), false)
	if e != nil || writes != 1 {
		t.Fatal("update", e, writes)
	}
}
func TestStorageEditsStayInsideSelectedDirectoryAndRequireDeleteConfirmation(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		writeJSON(w, map[string]int{"errCode": 0})
	}))
	defer srv.Close()
	c := testClient(t, srv)
	p := StoragePath{Store: "personal", Path: "docs/report"}
	if _, e := c.EditStorage(t.Context(), "remove", p, "", "", false); e == nil {
		t.Fatal("removed without confirmation")
	}
	if _, e := c.EditStorage(t.Context(), "mkdir", StoragePath{Path: "../bad"}, "", "", false); e == nil {
		t.Fatal("accepted traversal")
	}
	for _, op := range []string{"mkdir", "rename", "remove"} {
		if _, e := c.EditStorage(t.Context(), op, p, "next", "report", false); e != nil {
			t.Fatal(op, e)
		}
	}
	if len(seen) != 3 || seen[0] != "POST /api/storage/personal/folders/docs" || seen[1] != "PUT /api/storage/personal/folders/docs/report" || seen[2] != "DELETE /api/storage/personal/folders/docs" {
		t.Fatal(seen)
	}
}
