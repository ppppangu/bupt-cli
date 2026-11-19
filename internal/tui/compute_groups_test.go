package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func computeKeys(rows []row) []string {
	keys := make([]string, 0, len(rows))
	for _, r := range rows {
		keys = append(keys, r.key)
	}
	return keys
}

func TestComputeGroupDisclosureFilterAndMouse(t *testing.T) {
	b := &fixtureBackend{}
	w := fixtureWorkspace(t, b)
	w.report.ComputeFeatures = map[string]bool{"hpc": false, "dataset": true, "billing": false, "projects": false}
	click(t, w, "compute")
	p := w.current()
	keys := func() []string { return computeKeys(w.view(p).rows) }
	press := func(key tcell.Key) { w.handleKey(tcell.NewEventKey(key, 0, 0)) }
	typ := func(s string) {
		for _, r := range s {
			w.handleKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
		}
	}
	if got := keys(); !slices.Equal(got, []string{"instances", "new", "catalog"}) {
		t.Fatal(got)
	}
	w.showHelp()
	click(t, w, "tools")
	p = w.current()
	idx := slices.Index(keys(), "group/data")
	w.move(idx)
	press(tcell.KeyRight)
	press(tcell.KeyRight)
	if p.key != "stores" {
		t.Fatal(p.key)
	}
	press(tcell.KeyLeft)
	if p.key != "group/data" || !slices.Contains(keys(), "stores") {
		t.Fatal("left from a child should select its expanded group", keys(), p.key)
	}
	press(tcell.KeyLeft)
	if p.expanded["group/data"] {
		t.Fatal("second left should collapse the group")
	}
	typ("/公共数据")
	press(tcell.KeyEnter)
	if got := keys(); !slices.Equal(got, []string{"group/data", "dataset"}) || p.expanded["group/data"] {
		t.Fatal(got)
	}
	press(tcell.KeyBackspace2)
	if got := keys(); p.key != "group/data" || slices.Contains(got, "dataset") {
		t.Fatal(got, p.key)
	}
	screen := w.screen.(tcell.SimulationScreen)
	screen.SetSize(60, 24)
	typ("/我的配额")
	press(tcell.KeyEnter)
	w.draw()
	match := w.view(p).rows
	if len(match) != 2 || match[0].open != nil || p.key != "quota" || !strings.Contains(navLine(w, 23), "Esc 返回") {
		t.Fatal("filter should focus the matched action and explain Esc", p.key, navLine(w, 23))
	}
	press(tcell.KeyEnter)
	if w.current().title != "我的配额" {
		t.Fatal("Enter did not open the matched item")
	}
	press(tcell.KeyEscape)
	w.draw()
	if p.key != "quota" || p.filter != "我的配额" {
		t.Fatal("back lost the filtered selection")
	}
	press(tcell.KeyBackspace2)
	w.draw()
	if p.key != "group/data" || p.expanded["group/resources"] {
		t.Fatal("clearing the filter changed the original selection or expansion")
	}
	typ("/不存在")
	press(tcell.KeyCtrlU)
	press(tcell.KeyEnter)
	if got := keys(); p.key != "group/data" || slices.Contains(got, "dataset") {
		t.Fatal(got, p.key)
	}
	screen.SetSize(120, 27)
	w.draw()
	if w.listRight < 115 || strings.Contains(navLine(w, w.listTop), "选中项摘要") {
		t.Fatal("the compute menu repeated its selected row in a preview")
	}
	at := w.listTop + slices.Index(keys(), "group/org")
	mouse := func(x, y int) {
		w.mouse(tcell.NewEventMouse(x, y, tcell.Button1, 0))
		w.mouse(tcell.NewEventMouse(x, y, tcell.ButtonNone, 0))
	}
	mouse(w.listRight+3, at)
	if p.expanded["group/org"] {
		t.Fatal("right pane changed disclosure")
	}
	mouse(4, at)
	if got := keys(); !p.expanded["group/org"] || slices.Contains(got, "billing") || slices.Contains(got, "project") {
		t.Fatal(got)
	}
	screen.SetSize(52, 14)
	w.draw()
	if w.listRight < 48 {
		t.Fatal("narrow view kept sidebar", w.listRight)
	}
	if len(b.observed()) != 0 {
		t.Fatal(b.observed())
	}
}

func TestComputeRemoteOrderPagingIdentityAndFreshState(t *testing.T) {
	const id = "9007199254740993"
	listRead := 0
	finished := false
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "compute.read" {
			return nil, fmt.Errorf("unexpected %s", method)
		}
		one := map[string]any{"id": json.Number(id), "appName": "A", "state": "pending"}
		switch p["section"] {
		case "remote.order":
			listRead++
			items := []any{one, map[string]any{"id": "9", "appName": "B"}}
			if listRead == 2 {
				slices.Reverse(items)
			}
			return map[string]any{"items": items, "total": 26}, nil
		case "remote.order.show":
			if p["target"] != id {
				return nil, fmt.Errorf("wrong selected id %v", p)
			}
			if finished {
				one["state"] = "paid"
			}
			return one, nil
		}
		return nil, fmt.Errorf("unexpected %v", p)
	}}
	w := fixtureWorkspace(t, b)
	wait := func() { waitFor(t, w, func() bool { return !w.current().loading }) }
	click(t, w, "compute")
	w.showHelp()
	click(t, w, "tools")
	click(t, w, "group/resources")
	click(t, w, "remote")
	click(t, w, "remote.order")
	wait()
	key := "remote.order/" + id
	keys := computeKeys(w.view(w.current()).rows)
	w.move(slices.Index(keys, key))
	w.refresh()
	wait()
	_ = w.view(w.current())
	if w.current().key != key || w.current().focus != 1 {
		t.Fatal(w.current().key, w.current().focus)
	}
	click(t, w, key)
	wait()
	actions := w.view(w.current()).rows
	if i := slices.Index(computeKeys(actions), "action/compute/remote/order/cancel"); i < 0 || !strings.Contains(actions[i].command, "--confirm="+id) {
		t.Fatal(actions)
	}
	finished = true
	w.refresh()
	wait()
	if keys := computeKeys(w.view(w.current()).rows); slices.Contains(keys, "action/compute/remote/order/cancel") {
		t.Fatal(keys)
	}
	w.back()
	depth := len(w.pages)
	click(t, w, "next")
	wait()
	if len(w.pages) != depth || !slices.Contains(computeKeys(w.view(w.current()).rows), "prev") {
		t.Fatal(len(w.pages))
	}
	click(t, w, "prev")
	wait()
	if len(w.pages) != depth {
		t.Fatal(len(w.pages))
	}
}

func TestComputeReadRoutesAndRoleGates(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		if method != "compute.read" {
			return nil, errors.New(method)
		}
		switch s := p["section"].(string); s {
		case "admin":
			return map[string]any{"items": []any{map[string]any{"role": "platform_admin"}}}, nil
		case "job.show":
			return map[string]any{"idJob": "7", "clusterId": "31"}, nil
		case "remote.order.show":
			return map[string]any{"id": "7", "state": "pending"}, nil
		case "remote.catalog.show", "remote.instance.show", "cluster.show", "job.template.show", "job.request.show", "dataset.show":
			return map[string]any{"id": "7"}, nil
		case "job":
			return map[string]any{"items": []any{map[string]any{"idJob": "7", "clusterId": "31"}}}, nil
		default:
			return map[string]any{"items": []any{map[string]any{"id": "7", "state": "pending"}}}, nil
		}
	}}
	routes := []struct {
		section, cmd, key string
		actions           []string
	}{
		{"job", "job", "31/7", []string{"job/logs"}}, {"cluster", "cluster", "7", []string{"cluster/partition"}},
		{"remote.catalog", "remote catalog", "7", []string{"remote/order/create"}}, {"remote.instance", "remote instance", "7", []string{"remote/order/renew"}},
		{"remote.order", "remote order", "7", []string{"remote/order/payment-code", "remote/order/cancel"}},
		{"admin.cluster", "admin cluster", "7", []string{"admin/quota", "admin/quota/set"}}, {"admin.request", "admin request", "7", []string{"admin/approve", "admin/reject"}},
	}
	for _, tc := range routes {
		t.Run(tc.section, func(t *testing.T) {
			w := fixtureWorkspace(t, b)
			w.computeList(tc.section, tc.section, tc.cmd, 1)
			waitFor(t, w, func() bool { return !w.current().loading })
			click(t, w, tc.section+"/"+tc.key)
			waitFor(t, w, func() bool { return !w.current().loading })
			keys := computeKeys(w.view(w.current()).rows)
			for _, action := range tc.actions {
				if !slices.Contains(keys, "action/compute/"+action) {
					t.Fatal(keys)
				}
			}
			w.Cached = true
			if len(w.recordActions(tc.section, tc.key, map[string]any{"state": "pending"})) != 0 {
				t.Fatalf("cached write offered on %s", tc.section)
			}
		})
	}
	w := fixtureWorkspace(t, b)
	click(t, w, "compute")
	w.showHelp()
	click(t, w, "tools")
	click(t, w, "group/org")
	click(t, w, "admin")
	waitFor(t, w, func() bool { return !w.current().loading })
	if got := computeKeys(w.view(w.current()).rows); !slices.Equal(got, []string{"admin.users", "admin.cluster", "admin.billing", "admin.request"}) {
		t.Fatal(got)
	}
	for _, tc := range []struct{ key, sub string }{{"billing", "billing"}, {"billing", "billing.account"}, {"billing", "billing.storage"}, {"billing", "billing.history"}, {"billing", "billing.hpc"}, {"billing", "billing.prepayment"}, {"llm", "llm.account"}, {"llm", "llm.key"}, {"llm", "llm.model"}, {"llm", "llm.spend"}} {
		for w.current().title != "更多算力工具" {
			w.back()
		}
		click(t, w, tc.key)
		if tc.key == "billing" {
			waitFor(t, w, func() bool { return !w.current().loading })
			if tc.sub != "billing" {
				click(t, w, "category")
				click(t, w, tc.sub)
			}
		} else {
			click(t, w, tc.sub)
		}
		waitFor(t, w, func() bool { return !w.current().loading })
		calls := b.observed()
		if p := calls[len(calls)-1].params; p["section"] != tc.sub {
			t.Fatal(p)
		}
	}
}

func TestComputeFormRetryUsesSameKeyAndFixedTarget(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	calls := make(chan []string, 5)
	tries := 0
	w.Capture = func(_ context.Context, args []string) (string, error) {
		calls <- args
		if slices.Contains(args, "--dry-run") {
			return "允许预览", nil
		}
		tries++
		if tries == 1 {
			return "", errors.New("timeout after acceptance")
		}
		return "ok", nil
	}
	f := CommandForm{Path: []string{"compute", "admin", "approve"}, Runnable: true, Flags: []FormFlag{{Name: "confirm"}}}
	w.boundCommandInputs(f, "批准申请", []string{"--confirm=813", "--", "813"})
	click(t, w, "go")
	waitFor(t, w, func() bool { return !w.current().loading })
	preview := <-calls
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	first := <-calls
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	retry := <-calls
	if !slices.Equal(first, retry) || !strings.HasPrefix(first[0], "--idempotency-key=") || !slices.Contains(preview, first[0]) || !slices.Equal(first[1:], []string{"compute", "admin", "approve", "--confirm=813", "--", "813"}) {
		t.Fatal(preview, first, retry)
	}
}

func TestUnpagedSchoolListsAndMismatchedDetails(t *testing.T) {
	items := make([]map[string]any, 25)
	for i := range items {
		items[i] = map[string]any{"id": i + 1}
	}
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch p["section"] {
		case "remote.order.show":
			return map[string]any{"id": "99", "state": "pending"}, nil
		case "job.show":
			if p["target"] == "8" {
				return map[string]any{"idJob": "8", "clusterId": "32"}, nil
			}
			return nil, nil
		case "remote.catalog.show":
			return map[string]any{"name": "没有ID的规格"}, nil
		case "admin":
			return map[string]any{"items": []map[string]any{{"role": "member"}}}, nil
		default:
			return map[string]any{"items": items}, nil
		}
	}}
	w := fixtureWorkspace(t, b)
	wait := func() { waitFor(t, w, func() bool { return !w.current().loading }) }
	for _, section := range []string{"cluster", "job.template", "admin.cluster", "llm.model"} {
		w.computeList(section, section, section, 1)
		wait()
		if keys := computeKeys(w.view(w.current()).rows); len(keys) != 25 || slices.Contains(keys, "next") {
			t.Fatal(section, keys)
		}
		w.back()
	}
	w.computeList("admin", "管理身份", "admin", 1)
	wait()
	if len(w.view(w.current()).rows) != 0 {
		t.Fatal("admin actions exposed for regular user")
	}
	w.back()
	w.computeRecord("remote.order", "remote order", "A", "42", map[string]any{"id": "42", "state": "pending"})
	wait()
	if rows := w.view(w.current()).rows; len(rows) != 0 {
		t.Fatal(rows)
	}
	w.back()
	w.computeRecord("job", "job", "任务", "31/7", map[string]any{"clusterId": "31", "idJob": "7"})
	wait()
	if len(w.view(w.current()).rows) != 0 {
		t.Fatal("unknown school job received active actions")
	}
	w.back()
	w.computeRecord("job", "job", "作业", "31/8", map[string]any{"clusterId": "31", "idJob": "8"})
	wait()
	if rows := w.view(w.current()).rows; len(rows) != 0 {
		t.Fatal("another cluster's job was offered", rows)
	}
	w.back()
	w.computeRecord("remote.catalog", "remote catalog", "规格", "9", map[string]any{"id": "9"})
	wait()
	if keys := computeKeys(w.view(w.current()).rows); !slices.Equal(keys, []string{"detail"}) {
		t.Fatal("anonymous resource offered actions", keys)
	}
}

func TestProjectRowsGroupRolesAndRespectScope(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch p["section"] {
		case "project":
			return []any{map[string]any{"group_id": "7", "role": "admin", "state": "inactive"}, map[string]any{"group_id": "7", "role": "member", "state": "active"}}, nil
		case "project.show":
			return map[string]any{"membership": map[string]any{"group_id": "7", "role": "member", "state": "active"}}, nil
		default:
			return nil, errors.New(method)
		}
	}}
	w := fixtureWorkspace(t, b)
	w.computeList("project", "项目", "project", 1)
	waitFor(t, w, func() bool { return !w.current().loading })
	if rows := w.view(w.current()).rows; len(rows) != 1 || rows[0].key != "project/7" || len(rows[0].preview) != 2 {
		t.Fatal(rows)
	}
	click(t, w, "project/7")
	waitFor(t, w, func() bool { return !w.current().loading })
	if keys := computeKeys(w.view(w.current()).rows); !slices.Contains(keys, "storage") || slices.Contains(keys, "members") {
		t.Fatal(keys)
	}
}

func TestRemoteQuoteKeepsLiteralOrderName(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	submitted := make(chan []string, 1)
	w.Capture = func(_ context.Context, args []string) (string, error) {
		if slices.Contains(args, "--dry-run") {
			return `{"quote":"official","details":{"intent":{"action":"create","target":"7","name":"--quote=name","units":1},"spec_id":"7","spec_name":"教学 GPU","unit_price":1.25,"total":1.25,"unit":"点","duration_unit":"天"}}`, nil
		}
		submitted <- args
		return "ok", nil
	}
	w.runForm([]string{"compute", "remote", "order", "create", "--quote=previous", "--spec=7", "--", "--quote=name"}, true, "下单")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	got := <-submitted
	if !slices.Equal(got[1:], []string{"compute", "remote", "order", "create", "--quote=official", "--spec=7", "--", "--quote=name"}) {
		t.Fatal(got)
	}
}
