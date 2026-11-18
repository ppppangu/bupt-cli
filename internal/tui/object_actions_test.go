package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
)

func objectRow(t *testing.T, w *workspace, key string) (row, bool) {
	t.Helper()
	for _, r := range w.view(w.current()).rows {
		if r.key == key {
			return r, true
		}
	}
	return row{}, false
}

func TestNodeAndSnapshotActionsTrackSelectedObject(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	i := &w.report.Instances[0]
	i.Nodes = []domain.Node{{ID: "17", Name: "gpu", Type: "qemu", Snapshots: []map[string]any{{"eid": "23", "name": "baseline"}}, Services: []domain.Service{{ID: "20", Name: "ssh", Type: "tcp", Builtin: true}, {ID: "21", Name: "training", Type: "tcp"}}}}
	w.instancePage(i.ID)
	click(t, w, "maintenance")
	for _, key := range []string{"action/compute/clone", "action/compute/preset/set", "action/compute/instance/events", "action/compute/instance/wait", "action/compute/instance/adopt"} {
		if _, ok := objectRow(t, w, key); !ok {
			t.Errorf("stopped instance lacks %s", key)
		}
	}
	w.back()
	click(t, w, "node/17")
	for _, key := range []string{"action/compute/resize", "action/compute/service/add", "remove-service/21", "snapshots"} {
		if _, ok := objectRow(t, w, key); !ok {
			t.Errorf("node lacks %s", key)
		}
	}
	remove, _ := objectRow(t, w, "remove-service/21")
	if remove.command != "bupt compute service delete --node=17 101 21" {
		t.Fatal(remove.command)
	}
	if _, ok := objectRow(t, w, "remove-service/20"); ok {
		t.Fatal("built-in service offered for removal")
	}
	click(t, w, "snapshots")
	click(t, w, "23")
	if _, ok := objectRow(t, w, "restore"); !ok {
		t.Fatal("stopped QEMU lacks restore")
	}
	i.State = "running"
	if _, ok := objectRow(t, w, "restore"); ok {
		t.Fatal("snapshot writable after start")
	}
	w.back()
	if _, ok := objectRow(t, w, "new"); ok {
		t.Fatal("running QEMU can create snapshots")
	}
	w.back()
	click(t, w, "connection")
	for _, op := range []string{"ssh", "exec", "console", "forward", "sftp"} {
		r, ok := objectRow(t, w, "action/compute/"+op)
		if !ok || !strings.HasSuffix(r.command, "--node=17 101") {
			t.Errorf("missing node binding: %s: %q", op, r.command)
		}
	}
	i.State = "stopped"
	if _, ok := objectRow(t, w, "action/compute/exec"); ok {
		t.Fatal("stopped node still offers a terminal")
	}
	w.back()
	w.Cached = true
	if _, ok := objectRow(t, w, "remove-service/21"); ok {
		t.Fatal("offline mutation")
	}
}

func TestProjectFileActionsKeepScopeAndReloadAfterChange(t *testing.T) {
	var lists int
	b := &fixtureBackend{reply: func(_ context.Context, method string, params map[string]any) (any, error) {
		switch method {
		case "data.config":
			return []platform.Storage{{Name: "Shared", URLPath: "school"}}, nil
		case "data.list":
			lists++
			if params["group_id"] != "702" || params["store"] != "school" {
				return nil, errors.New("lost storage scope")
			}
			if lists == 1 {
				return []platform.File{{Name: "--profile=other"}, {Name: "/"}, {Name: `a\b`}}, nil
			}
			return []platform.File{{Name: "changed"}}, nil
		}
		return nil, errors.New("unexpected RPC " + method)
	}}
	w := fixtureWorkspace(t, b)
	w.projectStores("702")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "school")
	waitFor(t, w, func() bool { return !w.current().loading })
	if _, ok := objectRow(t, w, "/"); ok {
		t.Fatal("unsafe filename visible")
	}
	click(t, w, "--profile=other")
	r, _ := objectRow(t, w, "action/compute/storage/rename")
	if r.command != "bupt compute storage rename --store=school --group-id=702 -- --profile=other" {
		t.Fatal(r.command)
	}
	click(t, w, "get")
	if err := w.input.done("new file"); err != nil {
		t.Fatal(err)
	}
	if w.current().command != "bupt compute storage pull --store=school --group-id=702 -- --profile=other 'new file'" {
		t.Fatal(w.current().command)
	}
	w.back()
	rename := CommandForm{Path: []string{"compute", "storage", "rename"}, Runnable: true, Flags: []FormFlag{{Name: "store"}, {Name: "group-id"}}}
	storage := CommandForm{Path: []string{"compute", "storage"}, Children: []CommandForm{rename}}
	w.Commands = map[string]CommandForm{"compute": {Path: []string{"compute"}, Children: []CommandForm{storage}}}
	var calls [][]string
	w.Capture = func(_ context.Context, args []string) (string, error) {
		calls = append(calls, slices.Clone(args))
		return "ok", nil
	}
	click(t, w, "action/compute/storage/rename")
	click(t, w, "arg")
	input := w.input
	w.input = nil
	if err := input.done("changed"); err != nil {
		t.Fatal(err)
	}
	click(t, w, "go")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	expected := []string{"compute", "storage", "rename", "--store=school", "--group-id=702", "--", "--profile=other", "changed"}
	if len(calls) != 2 || !slices.Contains(calls[0], "--dry-run") || slices.Contains(calls[1], "--dry-run") {
		t.Fatal(calls)
	}
	for _, argv := range calls {
		if !slices.Equal(argv[len(argv)-len(expected):], expected) {
			t.Fatal(calls)
		}
	}
	w.back() // Result → form.
	w.back() // Form → original file.
	waitFor(t, w, func() bool { return !w.current().loading })
	if _, ok := objectRow(t, w, "get"); ok {
		t.Fatal("renamed file still offers download")
	}
	w.back()
	waitFor(t, w, func() bool { return !w.current().loading })
	if _, ok := objectRow(t, w, "changed"); !ok || lists != 3 {
		t.Fatal("directory did not reload", lists)
	}
}

func TestPresetActionsUseCurrentConfiguration(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	fresh := *w.Config
	fresh.Presets = map[string]domain.Preset{"gpu": {Name: "gpu", SpecID: "913"}}
	w.ReloadPresets = func() (*config.Config, error) { return &fresh, nil }
	w.presets()
	click(t, w, "gpu")
	r, ok := objectRow(t, w, "action/compute/preset/rm")
	if !ok || r.command != "bupt compute preset rm gpu" {
		t.Fatal(r.command)
	}
	fresh.DefaultPreset = "gpu"
	w.refreshPresets()
	if _, ok := objectRow(t, w, "action/compute/preset/rm"); ok {
		t.Fatal("default should be cleared before removal")
	}
	r, _ = objectRow(t, w, "action/compute/preset/use")
	if r.command != "bupt compute preset use -" {
		t.Fatal(r.command)
	}
	w.back()
	w.ReloadPresets = func() (*config.Config, error) { return nil, errors.New("invalid settings") }
	w.presets()
	if !strings.Contains(w.notice, "invalid settings") {
		t.Fatal(w.notice)
	}
}

func TestQuotaAndRunActionsUseServerObjectIDs(t *testing.T) {
	b := &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
		switch method {
		case "quota.get":
			return map[string]any{"UserQuota:51": map[string]any{"domain": "campus"}, "UserStorageQuota:52": map[string]any{"storageName": "home"}, "Bad:99": map[string]any{}}, nil
		case "job.get":
			return domain.Run{ID: "job1", Kind: "job", State: "running"}, nil
		case "task.get":
			return domain.TaskRecord{ID: "ticket", State: "submitted"}, nil
		}
		return nil, errors.New("unexpected RPC")
	}}
	w := fixtureWorkspace(t, b)
	w.quotas()
	click(t, w, "request")
	waitFor(t, w, func() bool { return !w.current().loading })
	v := w.view(w.current())
	if len(v.rows) != 2 {
		t.Fatal(v.rows)
	}
	for _, id := range []string{"51", "52"} {
		if !slices.ContainsFunc(v.rows, func(r row) bool { return r.command == "bupt compute quota request "+id }) {
			t.Error("missing quota", id)
		}
	}
	w.runPage("job1")
	waitFor(t, w, func() bool { return !w.current().loading })
	for _, op := range []string{"attach", "wait", "update"} {
		r, _ := objectRow(t, w, "action/compute/run/"+op)
		if r.command != "bupt compute run "+op+" job1" {
			t.Error(r.command)
		}
	}
	w.taskPage("ticket")
	waitFor(t, w, func() bool { return !w.current().loading })
	r, _ := objectRow(t, w, "action/compute/task/wait")
	if r.command != "bupt compute task wait ticket" {
		t.Fatal(r.command)
	}
}
