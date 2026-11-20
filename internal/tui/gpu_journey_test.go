package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
)

func TestInstancesSSHUsesVerifiedInstanceAndNode(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, method string, _ map[string]any) (any, error) {
		if method != "status.get" {
			t.Errorf("unexpected call %s", method)
		}
		return domain.StatusReport{Source: "cached"}, nil
	}}
	w := fixtureWorkspace(t, b)
	i := domain.Instance{ID: "101", Name: "gpu-dev", State: "running", Nodes: []domain.Node{{ID: "7", Name: "worker"}}}
	route := domain.Route{InstanceID: i.ID, NodeID: "7", State: "ready"}
	w.report.Instances = []domain.Instance{i}
	w.report.Routes = []domain.Route{route, {InstanceID: i.ID, NodeID: "obsolete", State: "ready"}}
	single := w.report
	var action TerminalAction
	w.Foreground = func(_ context.Context, request TerminalAction) error { action = request; return nil }
	w.computeHome()
	click(t, w, "instances")
	w.draw()
	navType(w, "/连接 gpu-dev")
	navPress(w, tcell.KeyEnter)
	if r := w.current().selected(w.view(w.current())); r == nil || r.title != "连接 gpu-dev" || r.forward {
		t.Fatal("expected one direct SSH action in the home filter", r)
	}
	navPress(w, tcell.KeyRight)
	if action.Name != "" {
		t.Fatal("browsing started SSH")
	}
	navPress(w, tcell.KeyEnter)
	waitFor(t, w, func() bool { return !w.refreshing })
	if action.Name != "ssh" || !slices.Equal(action.Args, []string{"101"}) || action.Options["node"] != "7" {
		t.Fatal(action)
	}
	navPress(w, tcell.KeyBackspace2)
	w.report = single
	second := i
	second.ID, second.Name = "202", "gpu-lab"
	w.report.Instances = append(w.report.Instances, second)
	w.report.Routes = append(w.report.Routes, domain.Route{InstanceID: second.ID, NodeID: "7", State: "ready"})
	click(t, w, "ssh/choose")
	navType(w, "/gpu-lab")
	navPress(w, tcell.KeyEnter)
	waitFor(t, w, func() bool { return !w.refreshing })
	if action.Name != "ssh" || !slices.Equal(action.Args, []string{"202"}) || action.Options["node"] != "7" {
		t.Fatal("nodes with the same ID in different instances were confused", action)
	}
	w.back()
	w.report = single
	w.report.Instances[0].State = "stopped"
	if r := w.sshTargets(); len(r.rows) != 0 {
		t.Fatal("a stopped instance offered a connection", r.rows)
	}
	w.report.Instances[0].SSHState = "ready"
	if got := w.instanceSSHState(w.report.Instances[0]); got == "ready" {
		t.Fatal("the stopped instance was still advertised as SSH ready")
	}
	w.report = domain.StatusReport{Source: "cached", Routes: []domain.Route{route}}
	if len(w.sshTargets().rows) != 1 {
		t.Fatal("lost the controller's prepared route without a school snapshot")
	}
	w.Cached = true
	if len(w.sshTargets().rows) != 0 {
		t.Fatal("read-only browsing offered a terminal connection")
	}
}

func TestInstanceListShowsSSHAndExpiryAtEveryWidth(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	expires := time.Now().Add(20 * time.Minute)
	i := &w.report.Instances[0]
	i.State, i.ExpiresAt, i.Nodes = "running", expires, []domain.Node{{ID: "7", Name: "worker"}}
	w.report.Routes = []domain.Route{{InstanceID: i.ID, NodeID: "7", State: "ready"}}
	w.computeHome()
	click(t, w, "instances")
	for _, cols := range []int{120, 80, 60} {
		w.screen.(tcell.SimulationScreen).SetSize(cols, 24)
		w.draw()
		line := navLine(w, w.listTop)
		if !strings.Contains(line, "SSH 就绪 · 平台预留剩余 20 分钟") || strings.Contains(line, "选中项摘要") != (cols == 120) {
			t.Errorf("unexpected %d-column instance row: %s", cols, line)
		}
		if cols == 120 && !strings.Contains(navLine(w, w.listTop+4), "平台预留到期") {
			t.Fatal("wide preview missing the absolute expiry")
		}
		if !w.redrawAt.Equal(expires.Add(-19 * time.Minute)) {
			t.Fatal("the idle countdown would not update", w.redrawAt)
		}
	}
	for _, tc := range []struct {
		expiry time.Time
		want   string
	}{{time.Time{}, "到期待确认"}, {time.Now().Add(-time.Minute), "已到期"}} {
		i.ExpiresAt = tc.expiry
		w.draw()
		if !strings.Contains(navLine(w, w.listTop), tc.want) || !w.redrawAt.IsZero() {
			t.Fatal(navLine(w, w.listTop), w.redrawAt)
		}
	}
}

func TestReallocateComputeExplainsPriceAndNextSSHStep(t *testing.T) {
	instance := domain.Instance{ID: "101", Name: "gpu-dev", State: "stopped", ReserveType: "manual"}
	run := domain.Run{ID: "allocate-101", InstanceID: instance.ID, Kind: "allocation", Phase: "active", State: "allocated", Deadline: time.Now().Add(20 * time.Minute)}
	backend := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "instance.restore":
			if p["dry_run"] == true {
				return domain.InstanceCreatePreview{
					DryRun: true, PointsPerDay: "2.40", BillingStart: "分配算力后可能产生费用", Run: &run,
				}, nil
			}
			return run, nil
		case "status.get":
			return domain.StatusReport{Source: "live", Instances: []domain.Instance{instance}, Runs: []domain.Run{run}}, nil
		default:
			return nil, errors.New("unexpected RPC " + method)
		}
	}}
	w := fixtureWorkspace(t, backend)
	w.report.Instances = []domain.Instance{instance}
	w.computeHome()
	click(t, w, "instances")
	screen := w.screen.(tcell.SimulationScreen)
	for _, width := range []int{60, 80} {
		screen.SetSize(width, 24)
		w.draw()
		if line := navLine(w, w.listTop); !strings.Contains(line, "算力未分配") {
			t.Fatalf("allocation status hidden at %d columns: %q", width, line)
		}
	}
	click(t, w, string(instance.ID))
	page := w.current()

	w.report.Instances[0].ReserveType = "no"
	if _, ok := objectRow(t, w, "restore"); ok {
		t.Fatal("an on-demand instance already offers direct startup")
	}
	if _, ok := objectRow(t, w, "start"); !ok {
		t.Fatal("on-demand instance lacks startup")
	}

	w.report.Instances[0] = instance
	v := w.view(page)
	if len(v.rows) == 0 || v.rows[0].title != "重新分配算力" ||
		!strings.Contains(v.rows[0].detail, "分配后可启动并连接 SSH") || !strings.Contains(v.facts[0], "算力未分配") {
		t.Fatalf("instance detail does not show the first step: %+v", v)
	}
	click(t, w, "restore")
	if preview, ok := objectRow(t, w, "preview"); !ok || preview.title != "核对价格与时限" {
		t.Fatal("allocation form does not offer price review")
	}
	click(t, w, "preview")
	waitFor(t, w, func() bool { return !w.current().loading })
	var reviewLines []string
	for _, row := range w.view(w.current()).rows {
		reviewLines = append(reviewLines, row.title)
	}
	review := strings.Join(reviewLines, "\n")
	for _, want := range []string{"2.40 积分/天", "可能产生费用", "原磁盘保留", "启动并准备 SSH"} {
		if !strings.Contains(review, want) {
			t.Fatalf("allocation review missing %q: %q", want, review)
		}
	}
	calls := backend.observed()
	if len(calls) != 1 || calls[0].method != "instance.restore" || calls[0].params["target"] != string(instance.ID) || calls[0].params["dry_run"] != true {
		t.Fatalf("review should only quote the selected instance: %+v", calls)
	}

	click(t, w, "submit")
	waitFor(t, w, func() bool { return w.current() == page && !w.refreshing })
	v = w.view(page)
	if !page.watch || !strings.Contains(strings.Join(v.facts, "\n"), "正在核对可用算力") {
		t.Fatal("allocation reported completion before the instance snapshot caught up")
	}
	if _, ok := objectRow(t, w, "restore"); ok {
		t.Fatal("allocation is still being verified")
	}
	w.report.Instances[0].Reserved = true
	if start, ok := objectRow(t, w, "start"); !ok || start.title != "启动并准备 SSH" {
		t.Fatal("verified allocation did not expose the next step")
	}
	calls = backend.observed()
	if len(calls) < 2 || calls[1].method != "instance.restore" || calls[1].params["max_points_per_day"] != "2.40" {
		t.Fatalf("allocation did not use the confirmed price: %+v", calls)
	}
}

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
	if got := computeKeys(w.view(w.current()).rows); !slices.Equal(got, []string{"pool/P1", "spec/11", "pool/P2", "spec/22", "tools"}) {
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
		if m != "spec.estimate" || !ok || p["spec_id"] != "17" {
			return nil, errors.New("unexpected quote")
		}
		if res["gpu"] == "0.2" {
			return map[string]any{"points_per_day": 0}, nil
		}
		if res["gpu"] != "0.1" {
			return nil, errors.New("unexpected resources")
		}
		return json.RawMessage(`{"points_per_day":` + dayRate + "}"), nil
	}}
	w := fixtureWorkspace(t, b)
	d := &draft{spec: domain.Spec{ID: "17"}, resources: map[string]string{"gpu": "0.1"}}
	w.resourceForm("创建实例", d)
	first := d.quote
	<-first.ready
	if first.rate != dayRate {
		t.Fatal(first.label())
	}
	w.draftOptions(d)
	click(t, w, "gpu")
	if err := w.input.done("0.2"); err != nil {
		t.Fatal(err)
	}
	if d.quote == first {
		t.Fatal("stale quote")
	}
	<-d.quote.ready
	if d.quote.rate != "0" {
		t.Fatal(d.quote.label())
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
			return map[string]any{"dry_run": true, "points_per_day": "1.25", "billing_start": "从预留开始可能计费", "run": domain.Run{Deadline: limit, Policy: domain.RunPolicy{Cleanup: "release"}},
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
		case "instance.start":
			request = p
			return map[string]any{"dry_run": true, "points_per_day": "1.25", "billing_start": "从预留开始可能计费", "run": r}, nil
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
	w.back() // The node picker applied its selection and closed.
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

func TestManagedCountdownUsesCurrentCycleAndEarlierBoundary(t *testing.T) {
	now := time.Now()
	platform := now.Add(2 * time.Hour)
	w := fixtureWorkspace(t, &fixtureBackend{})
	current := domain.Run{ID: "current", InstanceID: "101", CreatedAt: now, State: "ready", Deadline: now.Add(30 * time.Minute), Policy: domain.RunPolicy{Cleanup: "release"}}
	finished := current
	finished.ID = "old"
	finished.State = "completed"
	finished.CreatedAt = now.Add(time.Minute)
	other := current
	other.InstanceID = "999"
	other.ID = "other"
	other.Deadline = now.Add(time.Minute)
	w.report.Runs = []domain.Run{finished, other, current}
	i := &w.report.Instances[0]
	i.ExpiresAt = platform
	i.State = "running"
	i.RunID = current.ID
	run := w.instanceRun(*i)
	if run == nil || run.ID != current.ID {
		t.Fatal("did not bind the current cycle")
	}
	for _, tc := range []struct {
		expiry, deadline  time.Time
		cleanup, contains string
	}{
		{platform, now.Add(30 * time.Minute), "release", "30 分钟后停止并释放"},
		{now.Add(5 * time.Minute), now.Add(time.Hour), "delete", "平台预留剩余 5 分钟"},
		{platform, now.Add(time.Minute), "none", "平台预留剩余 2 小时"},
		{time.Time{}, now.Add(2 * time.Minute), "stop", "2 分钟后停止运行"},
		{platform, now, "delete", "清理待核对"},
	} {
		r := *run
		r.Deadline, r.Policy.Cleanup = tc.deadline, tc.cleanup
		got, _ := instanceTiming(tc.expiry, &r, now)
		if got != tc.contains && !strings.Contains(got, tc.contains) {
			t.Fatal(got, tc.contains)
		}
	}
	w.computeHome()
	click(t, w, "instances")
	w.screen.(tcell.SimulationScreen).SetSize(60, 24)
	w.draw()
	if !strings.Contains(navLine(w, w.listTop), "30 分钟后停止并释放") {
		t.Fatal(navLine(w, w.listTop))
	}
	click(t, w, "101")
	facts := strings.Join(w.view(w.current()).facts, " ")
	if !strings.Contains(facts, "平台预留到期") || !strings.Contains(facts, "本次托管截止") {
		t.Fatal(facts)
	}
	if row, ok := objectRow(t, w, "progress"); !ok || row.command != "bupt status current" {
		t.Fatal(row)
	}
}

func TestProjectSelectionAndDailyPriceReachCreationReview(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		switch m {
		case "compute.read":
			if p["section"] != "project" {
				return nil, errors.New("wrong project query")
			}
			return []domain.ProjectMembership{{GroupID: "5", Name: "旧项目", Role: "member", State: "disabled"}, {GroupID: "7", Name: "课堂项目", Role: "member", State: "active"}, {GroupID: "7", Role: "manager"}, {GroupID: "8", Role: "member"}}, nil
		case "spec.estimate":
			return map[string]any{"points_per_day": "12.500"}, nil
		case "instance.create":
			if p["dry_run"] != true || p["group_id"] != "7" {
				return nil, errors.New("unexpected write or project")
			}
			return map[string]any{"dry_run": true, "points_per_day": "12.750", "billing_start": "从预留开始可能计费", "run": domain.Run{Deadline: time.Now().Add(time.Hour), Policy: domain.RunPolicy{Cleanup: "release"}}}, nil
		}
		return nil, errors.New(m)
	}}
	w := fixtureWorkspace(t, b)
	d := &draft{name: "实验", limit: "30m", spec: domain.Spec{ID: "91", Options: map[string]any{"scope": "group"}}}
	w.resourceForm("创建实例", d)
	owner := w.current()
	<-d.quote.ready
	if !strings.Contains(strings.Join(w.view(owner).facts, " "), "12.500 积分/天") {
		t.Fatal("daily rate not visible on draft")
	}
	click(t, w, "preview")
	if w.current() != owner || owner.key != "group" {
		t.Fatal("required project was skipped")
	}
	click(t, w, "group")
	waitFor(t, w, func() bool { return !w.current().loading })
	rows := w.view(w.current()).rows
	if len(rows) != 2 || rows[0].title != "课堂项目" || rows[1].title != "项目 8" {
		t.Fatal(rows)
	}
	click(t, w, "project/7")
	click(t, w, "preview")
	waitFor(t, w, func() bool { return !w.current().loading })
	if !strings.Contains(strings.Join(computeRowTitles(w.view(w.current()).rows), " "), "12.750 积分/天") || len(b.observed()) != 3 {
		t.Fatal("confirmation lost or refetched the rate", b.observed())
	}
}

func TestMissingQuotaCanBeReadAgainAndShowsRealZero(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, m string, _ map[string]any) (any, error) {
		if m != "quota.get" {
			return nil, errors.New(m)
		}
		return map[string]any{"UserQuota:11": map[string]any{"gpu": 0, "gpuUsed": 0, "domain": "课堂"}}, nil
	}}
	w := fixtureWorkspace(t, b)
	w.quotas()
	if !strings.Contains(strings.Join(w.view(w.current()).facts, " "), "尚未取得") {
		t.Fatal("unknown quota not explained")
	}
	click(t, w, "retry-quota")
	waitFor(t, w, func() bool { return !w.current().loading })
	for _, r := range w.view(w.current()).rows {
		if r.title == "GPU" && strings.Contains(r.detail, "0 / 0") {
			return
		}
	}
	t.Fatal("missing quota did not recover to published zero")
}

type journeyLogBackend struct{ *fixtureBackend }

func (b journeyLogBackend) Stream(_ context.Context, method string, params any, _ io.Reader, out io.Writer) (map[string]any, error) {
	p := params.(map[string]any)
	if method != "job.logs" || p["target"] != "job-1" || p["stream"] != "stdout" {
		return nil, errors.New("wrong job output")
	}
	_, err := io.WriteString(out, "训练完成")
	return nil, err
}

func TestUploadRunAndReadOutputOnSelectedInstance(t *testing.T) {
	i := domain.Instance{ID: "101", Name: "gpu-lab", RunID: "cycle", Managed: true, State: "running", Nodes: []domain.Node{{ID: "7", Name: "head"}, {ID: "8", Name: "worker"}}}
	cycle := domain.Run{ID: "cycle", Kind: "session", State: "ready", InstanceID: i.ID, NodeID: "8", Deadline: time.Now().Add(time.Hour), Policy: domain.RunPolicy{Cleanup: "release"}}
	report := domain.StatusReport{Source: "live", Instances: []domain.Instance{i}, Runs: []domain.Run{cycle}, Routes: []domain.Route{{InstanceID: i.ID, NodeID: "7", State: "ready"}, {InstanceID: i.ID, NodeID: "8", State: "ready"}}}
	job := cycle
	job.ID, job.Kind, job.JobName = "job-1", "job", "课程训练"
	b := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		switch m {
		case "status.get":
			if p["target"] == job.ID {
				return domain.StatusReport{Runs: []domain.Run{job}, Instances: report.Instances}, nil
			}
			return report, nil
		case "job.run":
			if p["target"] != "101" || p["inherit_policy"] != true || fmt.Sprint(p["command"]) != "[python train.py]" {
				return nil, errors.New("incorrect target or arguments")
			}
			if p["dry_run"] == true {
				return map[string]any{"dry_run": true, "points_per_day": "1.25", "billing_start": "从预留开始可能计费", "run": job}, nil
			}
			return job, nil
		}
		return nil, errors.New(m)
	}}
	w := fixtureWorkspace(t, b)
	w.Backend = journeyLogBackend{b}
	w.report = report
	var transfer TerminalAction
	w.Foreground = func(_ context.Context, action TerminalAction) error { transfer = action; return nil }
	w.computeHome()
	click(t, w, "instances")
	click(t, w, "101")
	origin := w.current()
	click(t, w, "upload")
	if target, _ := objectRow(t, w, "8"); !strings.Contains(target.detail, "作业节点") {
		t.Fatal(target)
	}
	click(t, w, "8")
	typeTrialText(w, "./train.py")
	trialKey(w, tcell.KeyEnter)
	typeTrialText(w, "train.py")
	trialKey(w, tcell.KeyEnter)
	if transfer.Name != "" {
		t.Fatal("browsing uploaded the file")
	}
	click(t, w, "apply")
	waitFor(t, w, func() bool { return !w.refreshing })
	if transfer.Name != "cp" || transfer.Options["node"] != "8" || !slices.Equal(transfer.Args, []string{"./train.py", "101:train.py"}) {
		t.Fatal(transfer)
	}
	if w.current() != origin || origin.key != "job" {
		t.Fatal("upload did not return to the next action on the target instance")
	}
	click(t, w, "job")
	click(t, w, "0")
	typeTrialText(w, "python")
	trialKey(w, tcell.KeyEnter)
	click(t, w, "add")
	typeTrialText(w, "train.py")
	trialKey(w, tcell.KeyEnter)
	click(t, w, "preview")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "submit")
	waitFor(t, w, func() bool { return w.current().title == "运行详情" && !w.current().loading && !w.refreshing })
	click(t, w, "stdout")
	waitFor(t, w, func() bool { return !w.current().loading })
	if !strings.Contains(w.view(w.current()).rows[0].title, "训练完成") {
		t.Fatal("missing output for submitted job")
	}
	count := 0
	for _, call := range b.observed() {
		if call.method == "job.run" && call.params["dry_run"] != true {
			count++
		}
	}
	if count != 1 {
		t.Fatal("submission count", count)
	}
}

func TestAllocationWaitsForSnapshotThenTracksStopOnSameInstance(t *testing.T) {
	var mu sync.Mutex
	receipt := domain.Run{ID: "create-1", Name: "课程 GPU", Kind: "allocation", State: "preparing"}
	report := domain.StatusReport{Source: "live", SnapshotAvailable: true, Runs: []domain.Run{receipt}}
	task := domain.TaskRecord{ID: "stop-1", State: "submitted"}
	backend := &fixtureBackend{reply: func(_ context.Context, m string, p map[string]any) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		switch m {
		case "instance.create":
			if p["dry_run"] == true {
				return map[string]any{"dry_run": true, "points_per_day": "1.25", "billing_start": "从预留开始可能计费", "run": domain.Run{Deadline: time.Now().Add(time.Hour)}}, nil
			}
			return receipt, nil
		case "status.get":
			return report, nil
		case "instance.stop":
			if p["dry_run"] == true {
				return map[string]any{"dry_run": true}, nil
			}
			return task, nil
		case "task.get":
			if p["target"] != "stop-1" {
				t.Errorf("unexpected receipt %v", p)
			}
			return task, nil
		}
		return nil, errors.New(m)
	}}
	w := fixtureWorkspace(t, backend)
	w.computeHome()
	list := w.current()
	w.review(change{title: "创建实例", method: "instance.create", params: map[string]any{"name": "课程 GPU"}})
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "submit")
	waitFor(t, w, func() bool { return w.current().bench == "instance" && !w.refreshing })
	p := w.current()
	if len(w.pages) != 3 || w.pages[1] != list || !strings.Contains(strings.Join(w.view(p).facts, " "), "等待学校") || !p.watch {
		t.Fatal("missing scoped wait state", p, w.pages)
	}
	update := func(state string, alloc, stopped bool) {
		mu.Lock()
		receipt.InstanceID = "101"
		receipt.State = "allocated"
		report.Runs = []domain.Run{receipt}
		if alloc {
			report.Instances = []domain.Instance{{ID: "101", Name: receipt.Name, State: state}}
		}
		if stopped {
			task.State = "complete"
		}
		mu.Unlock()
		w.refresh()
		waitFor(t, w, func() bool { return !w.refreshing && !p.loading })
		w.view(p)
	}
	update("", false, false)
	if p.instanceID != "101" || !p.watch {
		t.Fatal("stopped polling between receipt and instance")
	}
	update("running", true, false)
	if w.current() != p {
		t.Fatal("allocation replaced the current work object")
	}
	click(t, w, "stop")
	waitFor(t, w, func() bool { return !w.current().loading })
	if !strings.Contains(strings.Join(func() []string {
		var s []string
		for _, r := range w.view(w.current()).rows {
			s = append(s, r.title)
		}
		return s
	}(), ""), "运行任务会中断") {
		t.Fatal("stop did not disclose its impact")
	}
	click(t, w, "submit")
	waitFor(t, w, func() bool { return w.current() == p && !p.loading && !w.refreshing })
	update("running", true, true)
	if !p.watch {
		t.Fatal("receipt preceded school state; poll stopped")
	}
	update("stopped", true, true)
	if p.watch || w.current() != p {
		t.Fatal("confirmed state not reflected in the original detail")
	}
}

func TestCatalogQuotesOnlyFocusedTemplatesAndKeepsPublishedZero(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "resources.overview":
			return domain.ResourceOverview{CatalogSource: "live", Pools: []domain.ResourcePool{{ID: "pool", Templates: []domain.TemplateSummary{{ID: "1", Name: "免费"}, {ID: "2", Name: "付费"}}}}}, nil
		case "spec.estimate":
			if p["spec_id"] == "1" {
				return map[string]any{"points_per_day": 0}, nil
			}
			return map[string]any{"points_per_day": 12.5}, nil
		}
		return nil, errors.New(method)
	}}
	w := fixtureWorkspace(t, b)
	w.catalog()
	p := w.current()
	waitFor(t, w, func() bool { return !p.loading })
	selectRate := func(key, want string) {
		w.draw()
		waitFor(t, w, func() bool { row, ok := objectRow(t, w, key); return ok && strings.Contains(row.detail, want) })
	}
	selectRate("spec/1", "0 积分/天")
	if len(b.observed()) != 2 {
		t.Fatal("loading the directory looked up other templates")
	}
	navPress(w, tcell.KeyDown)
	selectRate("spec/2", "12.5 积分/天")
	navPress(w, tcell.KeyUp)
	selectRate("spec/1", "0 积分/天")
	if len(b.observed()) != 3 {
		t.Fatal("revisiting a priced template triggered another read")
	}
}

func computeRowTitles(rows []row) []string {
	var titles []string
	for _, r := range rows {
		titles = append(titles, r.title)
	}
	return titles
}
