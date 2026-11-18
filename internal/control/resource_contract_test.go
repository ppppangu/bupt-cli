package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
)

type resourceContractPlatform struct {
	*corePlatform
	specification domain.Spec
	snapshotValue domain.Snapshot
}

func (a *resourceContractPlatform) Spec(context.Context, domain.ID) (domain.Spec, error) {
	return a.specification, nil
}
func (a *resourceContractPlatform) Sync(context.Context) (domain.Snapshot, error) {
	return a.snapshotValue, nil
}
func resourceContractFixture(t *testing.T) (*Controller, *resourceContractPlatform, domain.Instance) {
	t.Helper()
	c, _ := coreController(t)
	source := domain.Instance{ID: "9007199254741001", Name: "source", SpecID: "9007199254740993", OwnerID: "7", UserID: "7", GroupID: "31", Domain: "source-domain", State: "stopped", ReserveType: "manual", Meta: map[string]any{"emailAddr": "source@example.edu", "Usage": map[string]any{"value": "source research"}}, Nodes: []domain.Node{{ID: "9007199254741003", Name: "runtime-node", Role: "workStation", Type: "qemu", GroupID: "9007199254740997", Index: "9007199254741005", Resources: map[string]any{"cpu": 4, "memory": 12288, "gpu": 20, "gpuType": "A100", "gpuMem": 8192, "accelDisk": 102400, "bindCpu": true, "arch": "x86_64"}}}}
	spec := domain.Spec{ID: source.SpecID, Name: "template", Options: map[string]any{"domain": "template-domain", "scope": "user", "canCustom": false, "resourceConstraints": `{"9007199254740995":{"role":"workStation","nodeName":"template-node","constraints":[{"type":"CPU","value":"2","options":"2,4"},{"type":"Memory","value":"6144","options":"6144,12288"},{"type":"GPU","value":"0.1:A100:8192","options":"0.1:A100:8192,0.2:A100:8192"},{"type":"AccelDisk","value":"51200","options":"51200,102400"},{"type":"BindCPU","value":"false","options":""},{"type":"CPUArch","value":"x86_64","options":""}]}}`}}
	a := &resourceContractPlatform{corePlatform: &corePlatform{}, specification: spec, snapshotValue: domain.Snapshot{Identity: domain.Identity{UID: "7", Username: "student", Email: "current@example.edu", Groups: []domain.ID{"31"}}, Instances: []domain.Instance{source}}}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return a, nil }
	return c, a, source
}
func resourceContractCode(t *testing.T, e error, want string) {
	t.Helper()
	if managedCode(e) != want {
		t.Fatalf("wanted %s, got %v", want, e)
	}
}
func payloadResources(t *testing.T, p map[string]any, gid string) map[string]string {
	t.Helper()
	b, e := json.Marshal(p["resourceConstraints"])
	if e != nil {
		t.Fatal(e)
	}
	var groups map[string][]struct{ Type, Value string }
	if e = json.Unmarshal(b, &groups); e != nil {
		t.Fatal(e)
	}
	out := map[string]string{}
	for _, f := range groups[gid] {
		out[f.Type] = f.Value
	}
	return out
}

func TestPlatformMiBConversionHasNoByteScaleOrRounding(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		value any
		want  string
	}{{"memory", "6GiB", "6144"}, {"memory", "1.5GiB", "1536"}, {"memory", json.Number("12288"), "12288"}, {"memory", "1048576B", "1"}, {"memory", "1024KiB", "1"}, {"accelDisk", "0", "0"}, {"disk", "50GiB", "51200"}, {"bindCpu", true, "true"}} {
		got, e := resourceValue(tc.kind, tc.value)
		if e != nil || got != tc.want {
			t.Fatalf("%s/%v -> %q %v", tc.kind, tc.value, got, e)
		}
	}
	for _, value := range []string{"1MB", "0.1MiB", "1B", "0", "-1", "NaN", "999999999999999999999TiB"} {
		if _, e := resourceValue("memory", value); e == nil {
			t.Errorf("accepted nonintegral/invalid MiB %s", value)
		}
	}
}

func TestCreateWireBillingDomainGroupAndNumericIDs(t *testing.T) {
	c, a, _ := resourceContractFixture(t)
	p := params(map[string]any{"name": "new", "spec_id": a.specification.ID})
	task, e := c.buildTask(context.Background(), "bupt", "create", p)
	if e != nil {
		t.Fatal(e)
	}
	if task.Payload["billId"] != "" || task.Payload["gid"] != json.Number("0") || task.Payload["domain"] != "template-domain" {
		t.Fatal(task.Payload)
	}
	b, _ := json.Marshal(task.Payload)
	if !strings.Contains(string(b), `"appSpecEid":9007199254740993`) {
		t.Fatal("ID precision lost", string(b))
	}
	r := payloadResources(t, task.Payload, "9007199254740995")
	if r["Memory"] != "6144" || r["GPU"] != "0.1:A100:8192" || r["BindCPU"] != "false" {
		t.Fatal(r)
	}
	p.Set("group_id", "7")
	p.Set("bill_id", "invoice-opaque-id")
	task, e = c.buildTask(context.Background(), "bupt", "create", p)
	if e != nil || task.Payload["gid"] != json.Number("0") || task.Payload["billId"] != "invoice-opaque-id" {
		t.Fatal(task, e)
	}
	a.specification.Options["scope"] = "group"
	_, e = c.buildTask(context.Background(), "bupt", "create", p)
	resourceContractCode(t, e, "PROJECT_REQUIRED")
	p.Set("group_id", "31")
	if _, e = c.buildTask(context.Background(), "bupt", "create", p); e != nil {
		t.Fatal(e)
	}
}

func TestCloneInheritsSourceTemplateAndAllocationWithoutReset(t *testing.T) {
	c, _, source := resourceContractFixture(t)
	p := params(map[string]any{"target": source.ID, "name": "copy"})
	task, e := c.buildTask(context.Background(), "bupt", "clone", p)
	if e != nil {
		t.Fatal(e)
	}
	if task.Payload["appSpecEid"] != source.SpecID.Number() || task.Payload["cloneAppEid"] != source.ID.Number() || task.Payload["gid"] != json.Number("31") || task.Payload["domain"] != "source-domain" || task.Payload["emailAddr"] != "source@example.edu" || task.Payload["usage"] != "source research" {
		t.Fatal(task.Payload)
	}
	r := payloadResources(t, task.Payload, "9007199254740995")
	if r["CPU"] != "4" || r["Memory"] != "12288" || r["GPU"] != "0.2:A100:8192" || r["AccelDisk"] != "102400" || r["BindCPU"] != "true" {
		t.Fatal("clone reset current allocation", r)
	}
	p.Set("resources", map[string]any{"memory": "6GiB"})
	task, e = c.buildTask(context.Background(), "bupt", "clone", p)
	if e != nil {
		t.Fatal(e)
	}
	r = payloadResources(t, task.Payload, "9007199254740995")
	if r["CPU"] != "2" || r["Memory"] != "6144" || r["GPU"] != "0.1:A100:8192" || r["AccelDisk"] != "102400" {
		t.Fatal("clone package override reset independent fields", r)
	}
	p.Set("spec_id", "8")
	_, e = c.buildTask(context.Background(), "bupt", "clone", p)
	resourceContractCode(t, e, "CLONE_TEMPLATE_MISMATCH")
}

func TestNativeResizeUsesNodeGIDNumbersAndPreservesOtherFields(t *testing.T) {
	c, _, source := resourceContractFixture(t)
	p := params(map[string]any{"target": source.ID, "resources": map[string]any{"accelDisk": "50GiB"}})
	task, e := c.buildTask(context.Background(), "bupt", "resize", p)
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]any{"gid": source.Nodes[0].GroupID.Number(), "cpu": json.Number("4"), "memory": json.Number("12288"), "gpu": "0.2:A100:8192", "accelDisk": json.Number("51200"), "bindCpu": "true", "nodeName": "template-node", "nodeRole": "workStation"}
	for key, value := range want {
		if task.Payload[key] != value {
			t.Errorf("field %s: got %v (%T), expected %v (%T)", key, task.Payload[key], task.Payload[key], value, value)
		}
	}
	if _, ok := task.Payload["eaasIndex"]; ok {
		t.Fatal("resize added an undocumented index")
	}
	p.Set("resources", map[string]any{"cpu": "2"})
	task, e = c.buildTask(context.Background(), "bupt", "resize", p)
	if e != nil || task.Payload["memory"] != json.Number("6144") || task.Payload["gpu"] != "0.1:A100:8192" || task.Payload["accelDisk"] != json.Number("102400") {
		t.Fatal(task, e)
	}
}

func TestResourceContractsRejectUnsafeStatesAndAmbiguousRoles(t *testing.T) {
	c, a, source := resourceContractFixture(t)
	for _, state := range []string{"running", "starting", "stopping", "creating", "removing"} {
		a.snapshotValue.Instances[0].State = state
		_, e := c.buildTask(context.Background(), "bupt", "resize", params(map[string]any{"target": source.ID, "resources": map[string]any{"cpu": "2"}}))
		resourceContractCode(t, e, "RELEASE_REQUIRED")
		for _, op := range []string{"disk-grow", "snapshot-create"} {
			_, e = c.buildTask(context.Background(), "bupt", op, params(map[string]any{"target": source.ID, "size_gb": 60}))
			resourceContractCode(t, e, "STOP_REQUIRED")
		}
	}
	a.snapshotValue.Instances[0].State = "stopped"
	a.snapshotValue.Instances[0].Reserved = true
	_, e := c.buildTask(context.Background(), "bupt", "resize", params(map[string]any{"target": source.ID, "resources": map[string]any{"cpu": "2"}}))
	resourceContractCode(t, e, "RELEASE_REQUIRED")
	a.snapshotValue.Instances[0].Reserved = false
	a.snapshotValue.Instances[0].Nodes[0].Role = "unknown"
	_, e = c.buildTask(context.Background(), "bupt", "resize", params(map[string]any{"target": source.ID, "resources": map[string]any{"cpu": "2"}}))
	resourceContractCode(t, e, "RESOURCE_GROUP_REQUIRED")
	a.snapshotValue.Instances[0].Nodes = append(source.Nodes, source.Nodes[0])
	_, e = c.buildTask(context.Background(), "bupt", "clone", params(map[string]any{"target": source.ID, "name": "multi"}))
	resourceContractCode(t, e, "CLONE_LAYOUT_UNSUPPORTED")
}

func TestExactConstraintsAreValidatedAndCompleteDefaultsAreMerged(t *testing.T) {
	c, a, _ := resourceContractFixture(t)
	tests := []struct {
		raw  any
		code string
	}{{map[string]any{"999": []any{map[string]any{"type": "CPU", "value": "2"}}}, "RESOURCE_GROUP_UNKNOWN"}, {map[string]any{"9007199254740995": []any{map[string]any{"type": "CPU", "value": 4}}}, "INVALID_CONSTRAINTS"}, {map[string]any{"9007199254740995": []any{map[string]any{"type": "CPU", "value": "4", "options": "4,999"}}}, "INVALID_CONSTRAINTS"}, {map[string]any{"9007199254740995": []any{map[string]any{"type": "CPU", "value": "4"}, map[string]any{"type": "CPU", "value": "2"}}}, "INVALID_CONSTRAINTS"}, {map[string]any{"9007199254740995": []any{map[string]any{"type": "CPU", "value": "999"}}}, "RESOURCE_OPTION"}, {[]any{}, "INVALID_RESOURCES"}}
	for _, tc := range tests {
		_, e := c.buildTask(context.Background(), "bupt", "create", params(map[string]any{"name": "copy", "spec_id": a.specification.ID, "constraints": tc.raw}))
		resourceContractCode(t, e, tc.code)
	}
	exact := map[string]any{"9007199254740995": []any{map[string]any{"type": "CPU", "value": "4"}}}
	task, e := c.buildTask(context.Background(), "bupt", "create", params(map[string]any{"name": "copy", "spec_id": a.specification.ID, "constraints": exact}))
	if e != nil {
		t.Fatal(e)
	}
	r := payloadResources(t, task.Payload, "9007199254740995")
	if r["CPU"] != "4" || r["Memory"] != "12288" || r["GPU"] != "0.2:A100:8192" || r["AccelDisk"] != "51200" {
		t.Fatal(r)
	}
}

func TestFixedResourcesAndDefaultOutsideOptionsAreNotCustomOverrides(t *testing.T) {
	g := resourceGroup{Constraints: []resourceField{{Type: "CPU", Value: "2", Options: "3,4"}, {Type: "Memory", Value: "6144"}}}
	base := defaultResources(g)
	if _, e := selectResources(g, base, map[string]string{"CPU": "2"}); e != nil {
		t.Fatal("default excluded from option union", e)
	}
	if _, e := selectResources(g, base, map[string]string{"Memory": "12288"}); managedCode(e) != "RESOURCE_OPTION" {
		t.Fatal("fixed field was treated as customizable", e)
	}
	g.Constraints[0].Value = "1"
	g.Constraints[0].Options = "2,2"
	g.Constraints[1].Value = "2048"
	g.Constraints[1].Options = "4096,8192"
	if _, e := selectResources(g, defaultResources(g), map[string]string{"CPU": "2"}); managedCode(e) != "RESOURCE_COMBINATION_AMBIGUOUS" {
		t.Fatal("ambiguous package silently picked a size", e)
	}
	values, e := selectResources(g, map[string]string{"CPU": "2", "Memory": "8192"}, map[string]string{"CPU": "1"})
	if e != nil || values["Memory"] != "2048" {
		t.Fatal("declared default bundle outside options was rejected", values, e)
	}
}

func TestDiskAndSnapshotUseNativeNodeGroupAndIndex(t *testing.T) {
	c, _, source := resourceContractFixture(t)
	for _, op := range []string{"disk-grow", "snapshot-create", "snapshot-restore", "snapshot-delete"} {
		task, e := c.buildTask(context.Background(), "bupt", op, params(map[string]any{"target": source.ID, "size_gb": 60, "snapshot_id": "88", "name": "backup", "description": "before update"}))
		if e != nil {
			t.Fatal(e)
		}
		if task.Payload["gid"] != source.Nodes[0].GroupID.Number() || task.Payload["eaasIndex"] != source.Nodes[0].Index.Number() {
			t.Fatalf("%s used the app group or wrong node index: %v", op, task.Payload)
		}
	}
}

func TestCloneAndCustomServiceCapabilityGuards(t *testing.T) {
	c, a, source := resourceContractFixture(t)
	a.snapshotValue.Instances[0].Nodes[0].Type = "container"
	_, e := c.buildTask(context.Background(), "bupt", "clone", params(map[string]any{"target": source.ID, "name": "clone"}))
	resourceContractCode(t, e, "CLONE_LAYOUT_UNSUPPORTED")
	a.snapshotValue.Instances[0].Nodes[0].Type = "qemu"
	a.snapshotValue.Instances[0].OwnerID = "8"
	a.snapshotValue.Instances[0].UserID = "8"
	_, e = c.buildTask(context.Background(), "bupt", "clone", params(map[string]any{"target": source.ID, "name": "clone"}))
	resourceContractCode(t, e, "OWNERSHIP_UNVERIFIED")
	a.snapshotValue.Instances[0].OwnerID = "7"
	a.snapshotValue.Instances[0].UserID = "7"
	_, e = c.buildTask(context.Background(), "bupt", "service-add", params(map[string]any{"target": source.ID, "name": " ssh ", "port": 2222}))
	resourceContractCode(t, e, "RESERVED_SERVICE")
	_, e = c.buildTask(context.Background(), "bupt", "service-add", params(map[string]any{"target": source.ID, "name": "custom", "port": 2222, "bind": "localhost"}))
	resourceContractCode(t, e, "INVALID_ADDRESS")
	a.snapshotValue.Instances[0].State = "starting"
	_, e = c.buildTask(context.Background(), "bupt", "service-add", params(map[string]any{"target": source.ID, "name": "custom", "port": 2222}))
	resourceContractCode(t, e, "INVALID_STATE")
}
