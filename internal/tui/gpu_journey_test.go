package tui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
)

func TestCachedTemplateDirectoryNeverStartsCreation(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		if m != "resources.overview" || p["cached"] != true {
			t.Errorf("offline browse used %s %#v", m, p)
		}
		return domain.ResourceOverview{CatalogSource: "cached", Pools: []domain.ResourcePool{
			{ID: "P1", Name: "第一资源池", Templates: []domain.TemplateSummary{{ID: "11", Name: "同名模板"}}},
			{ID: "P2", Name: "第二资源池", Templates: []domain.TemplateSummary{{ID: "22", Name: "同名模板"}}},
		}}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.Cached = true
	w.catalog()
	waitFor(t, w, func() bool { return !w.current().loading })
	if got := computeKeys(w.view(w.current()).rows); !slices.Equal(got, []string{"pool/P1", "spec/11", "pool/P2", "spec/22"}) {
		t.Fatal(got)
	}
	click(t, w, "spec/22")
	v := w.view(w.current())
	var lines []string
	for _, r := range v.rows {
		lines = append(lines, r.title)
	}
	if !strings.Contains(strings.Join(lines, "\n"), "第二资源池") || !strings.Contains(strings.Join(lines, "\n"), "22") || len(b.observed()) != 1 {
		t.Fatal(lines, b.observed())
	}
}

func TestIncompleteManagedPreviewCannotBeSubmitted(t *testing.T) {
	b := &fixtureBackend{reply: func(context.Context, string, map[string]any) (any, error) {
		return map[string]any{"dry_run": true}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.review(change{title: "创建实例", method: "instance.create", params: map[string]any{}})
	waitFor(t, w, func() bool { return !w.current().loading })
	if _, ok := objectRow(t, w, "submit"); ok {
		t.Fatal("unverified managed deadline offered confirmation")
	}
}

func TestEstimateUsesCurrentDraftAndKeepsExactSchoolRate(t *testing.T) {
	const dayRate = "123.1234567890123456789"
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		res, ok := p["resources"].(map[string]any)
		if m != "spec.estimate" || !ok || res["gpu"] != "0.1" || p["spec_id"] != "17" {
			t.Errorf("%s, %v", m, p)
		}
		return json.RawMessage(`{"points_per_day":` + dayRate + "}"), nil
	}}
	w := fixtureWorkspace(t, b)
	d := &draft{spec: domain.Spec{ID: "17"}, resources: map[string]string{"gpu": "0.1"}}
	w.estimate(d)
	waitFor(t, w, func() bool { return !w.current().loading })
	_ = w.view(w.current())
	if d.price != dayRate {
		t.Fatal(d.price)
	}
	w.back()
	w.draftOptions(d)
	click(t, w, "gpu")
	if err := w.input.done("0.2"); err != nil || d.price != "" {
		t.Fatal("changed selection retained stale estimate", err)
	}
}

func TestFirstInstanceFromKeyboardKeepsTemplateAndPreviewsBeforeCreating(t *testing.T) {
	limit := time.Now().Add(20 * time.Minute)
	var writes int
	backend := &fixtureBackend{reply: func(_ context.Context, method string, params map[string]any) (any, error) {
		switch method {
		case "resources.overview":
			return domain.ResourceOverview{CatalogSource: "live", Pools: []domain.ResourcePool{{ID: "first", Name: "教学资源池", Templates: []domain.TemplateSummary{{ID: "902", Name: "教学模板", Resources: map[string]any{"gpu": 10, "cpu": 2, "memory": 6144}}}}}}, nil
		case "spec.show":
			if params["target"] != "902" {
				t.Errorf("wrong template lookup: %v", params)
			}
			return domain.Spec{ID: "902", Name: "教学模板", Version: "v1"}, nil
		case "instance.create":
			if params["name"] != "--my-lab" || params["spec_id"] != "902" {
				t.Errorf("wrong request: %v", params)
			}
			if params["dry_run"] != true {
				writes++
			}
			return map[string]any{"dry_run": true, "run": domain.Run{Deadline: limit, Policy: domain.RunPolicy{Cleanup: "release"}},
				"resource_constraints": map[string]any{"g": []map[string]string{{"type": "GPU", "value": "0.2:A100:8192"}, {"type": "CPU", "value": "4"}, {"type": "Memory", "value": "12288"}}}}, nil
		default:
			return nil, errors.New("unexpected RPC " + method)
		}
	}}
	w := fixtureWorkspace(t, backend)
	w.report.Instances = nil
	w.Config.Policy.MaxRuntime = ""
	press := func(k tcell.Key) { w.handleKey(tcell.NewEventKey(k, 0, 0)) }
	click(t, w, "compute") // First Enter from the home screen.
	if got := w.view(w.current()).rows[w.current().focus].key; got != "new" {
		t.Fatalf("first compute action: %s", got)
	}
	press(tcell.KeyEnter) // One combined catalogue.
	waitFor(t, w, func() bool { return !w.current().loading })
	v := w.view(w.current())
	if w.current().key != "spec/902" || v.rows[0].title != "教学资源池" || !strings.Contains(v.rows[w.current().focus].detail, "6 GiB") {
		t.Fatal(v)
	}
	press(tcell.KeyEnter) // Third Enter opens the naming field's page.
	waitFor(t, w, func() bool { return !w.current().loading })
	v = w.view(w.current())
	if w.current().title != "创建实例" || w.current().key != "name" || !strings.Contains(strings.Join(v.facts, "\n"), "2 核") {
		t.Fatal(v)
	}
	fields := computeKeys(v.rows)
	if slices.Contains(fields, "gpu") || !slices.Contains(fields, "options") {
		t.Fatal(fields)
	}
	click(t, w, "preview")
	if w.current().key != "name" || !strings.Contains(w.notice, "名称") {
		t.Fatal(w.notice)
	}
	click(t, w, "name")
	if err := w.input.done("--my-lab"); err != nil {
		t.Fatal(err)
	}
	w.input = nil
	click(t, w, "preview")
	if w.current().key != "limit" || !strings.Contains(w.notice, "时限") && !strings.Contains(w.notice, "上限") {
		t.Fatal(w.notice)
	}
	click(t, w, "limit")
	if err := w.input.done("20m"); err != nil {
		t.Fatal(err)
	}
	w.input = nil
	click(t, w, "preview")
	waitFor(t, w, func() bool { return !w.current().loading })
	v = w.view(w.current())
	var content []string
	for _, r := range v.rows {
		content = append(content, r.title, r.detail)
	}
	display := strings.Join(content, "\n")
	for _, want := range []string{"--my-lab", "4 核", "12288 MiB", "A100", "预计截止", "停止并释放资源"} {
		if !strings.Contains(display, want) {
			t.Errorf("review missing %q: %s", want, display)
		}
	}
	if !strings.HasSuffix(w.current().command, "-- --my-lab") {
		t.Fatal(w.current().command)
	}
	w.move(slices.Index(computeKeys(v.rows), "submit"))
	press(tcell.KeyRight)
	if writes != 0 || w.current().title != "确认创建实例" {
		t.Fatal("right submitted or navigated")
	}
}

func TestRunProgressUsesTheMatchingNodeRouteAndExistingBound(t *testing.T) {
	deadline := time.Now().UTC().Add(time.Hour)
	r := domain.Run{ID: "run-1", InstanceID: "42", NodeID: "7", Name: "demo", Kind: "session", State: "ready", Deadline: deadline, Policy: domain.RunPolicy{Cleanup: "release", MaxRuntime: "1h"}}
	i := domain.Instance{ID: "42", State: "running", RunID: r.ID, SSHState: "ready", Nodes: []domain.Node{{ID: "8", Name: "other"}, {ID: "7", Name: "worker"}}}
	routes := []domain.Route{{InstanceID: "43", NodeID: "7", State: "ready"}, {InstanceID: "42", NodeID: "8", State: "ready"}}
	var request map[string]any
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		switch m {
		case "status.get":
			if p["cached"] != true || p["target"] != r.ID {
				t.Errorf("run poll must use its local receipt: %v", p)
			}
			return domain.StatusReport{Runs: []domain.Run{r}, Instances: []domain.Instance{i}, Routes: routes}, nil
		case "up":
			request = p
			return map[string]any{"dry_run": true, "run": r}, nil
		default:
			return nil, errors.New(m)
		}
	}}
	w := fixtureWorkspace(t, b)
	w.runPage(r.ID)
	settle := func() { waitFor(t, w, func() bool { return !w.current().loading }); _ = w.view(w.current()) }
	settle()
	row, ok := objectRow(t, w, "ssh/7")
	if !ok || row.title != "准备 SSH" || row.forward {
		t.Fatal(row)
	}
	if _, ok := objectRow(t, w, "ssh/8"); ok {
		t.Fatal("off-target node route")
	}
	routes = append(routes, domain.Route{InstanceID: i.ID, NodeID: r.NodeID, State: "ready"})
	w.refresh()
	settle()
	row, _ = objectRow(t, w, "ssh/7")
	if row.title != "连接 SSH" || row.forward {
		t.Fatal(row)
	}
	r.Phase = "cleanup"
	w.refresh()
	settle()
	if _, ok := objectRow(t, w, "ssh/7"); ok {
		t.Fatal("cleanup offered a shell")
	}
	r.Phase = "active"
	r.State = "allocated"
	r.Kind = "allocation"
	i.State = "stopped"
	w.refresh()
	settle()
	click(t, w, "start")
	click(t, w, "7")
	settle()
	if request["dry_run"] != true || request["target"] != "42" || request["name"] != "demo" || request["node"] != "7" || request["until"] != deadline.Format(time.RFC3339Nano) || request["max_runtime"] != nil {
		t.Fatal(request)
	}
	w.back() // Confirmation → node choice → scoped receipt.
	w.back()
	if w.current().title != "运行详情" {
		t.Fatal(w.current().title)
	}
	w.Cached = true
	w.refresh()
	settle()
	if _, ok := objectRow(t, w, "start"); ok || w.current().watch {
		t.Fatal("offline receipt offered allocation or kept polling")
	}
}
