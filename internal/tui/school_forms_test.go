package tui

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"bupt.local/cli/internal/domain"
	"github.com/gdamore/tcell/v2"
)

func TestInstanceTransferAsksForSourceThenDestination(t *testing.T) {
	for _, tc := range []struct {
		upload                   bool
		source, dest, title, cmd string
	}{
		{true, "./my data", "work/data", "本机源文件", "bupt cp --node 10 -- './my data' 9:work/data"},
		{false, "work/data", "./my data", "实例源文件", "bupt cp --node 10 -- 9:work/data './my data'"},
	} {
		w := fixtureWorkspace(t, &fixtureBackend{})
		w.copyForm("9", "10", tc.upload, nil)
		if w.input.title != tc.title {
			t.Fatal(w.input.title)
		}
		if err := w.input.done(tc.source); err != nil {
			t.Fatal(err)
		}
		if err := w.input.done(tc.dest); err != nil {
			t.Fatal(err)
		}
		if w.current().command != tc.cmd {
			t.Fatal(w.current().command)
		}
	}
}

func TestRemoteOrderRecoverFromQuoteChangeAndUnknownReceipt(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	attempts := 0
	recordFormCalls(w, "compute.remote.checkout", func(p map[string]any) (any, error) {
		if p["dry_run"] == true {
			return json.RawMessage(`{"quote":"q","details":{"intent":{"action":"renew","target":"5","units":1},"spec_id":"5","spec_name":"GPU","unit":"CNY","unit_price":2,"duration_unit":"天","total":2}}`), nil
		}
		attempts++
		if attempts == 1 {
			return nil, domain.E("QUOTE_CHANGED", "需要重报")
		}
		return nil, domain.E("ORDER_RESULT_UNCERTAIN", "待核对")
	})
	w.remoteOrderForm(true, "5", "课程", "天")
	click(t, w, "go")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	if _, ok := objectRow(t, w, "submit"); ok {
		t.Fatal("stale quote still has submit")
	}
	click(t, w, "requote")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	if _, ok := objectRow(t, w, "orders"); !ok || w.current().load != nil || attempts != 2 {
		t.Fatal("uncertain order can replay", attempts)
	}
}

func fillSchoolField(t *testing.T, w *workspace, key, value string) {
	t.Helper()
	click(t, w, key)
	if w.input == nil {
		t.Fatalf("%s did not open an editor", key)
	}
	p := w.input
	w.input = nil
	if err := p.done(value); err != nil {
		t.Fatal(err)
	}
}

func TestPersonalQuotaFormUsesPublishedLimitsAndPreservesUnits(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{reply: func(_ context.Context, method string, _ map[string]any) (any, error) {
		if method != "quota.get" {
			return nil, errors.New(method)
		}
		return map[string]any{"UserQuota:51": map[string]any{"domain": "科研", "gpu": 100, "memory": 2048}, "UserStorageQuota:52": map[string]any{"storageName": "home", "fileStorageQuotaInMb": 0}}, nil
	}})
	calls := recordFormCalls(w, "compute.quota.request", func(map[string]any) (any, error) { return "学校预览", nil })
	w.quotaRequests()
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "UserQuota:51")
	for _, absent := range []string{"arg", "disk", "instances", "cpu", "file-storage"} {
		if _, ok := objectRow(t, w, absent); ok {
			t.Fatalf("unexpected field %s", absent)
		}
	}
	gpu, _ := objectRow(t, w, "gpu")
	mem, _ := objectRow(t, w, "memory")
	if !strings.Contains(gpu.detail, "1 卡") || !strings.Contains(mem.detail, "2 GiB") {
		t.Fatal(gpu, mem)
	}
	click(t, w, "go")
	if len(calls) != 0 || w.notice == "" {
		t.Fatal("empty request reached controller")
	}
	fillSchoolField(t, w, "gpu", "0")
	fillSchoolField(t, w, "memory", "1.5")
	fillSchoolField(t, w, "reason", "教学 作业")
	click(t, w, "wait")
	click(t, w, "go")
	waitFor(t, w, func() bool { return !w.current().loading })
	preview := <-calls
	if preview["target"] != "51" || preview["gpu"] != "0" || preview["memory"] != "1.5GiB" || preview["wait"] != true || preview["reason"] != "教学 作业" || preview["dry_run"] != true {
		t.Fatal(preview)
	}
	r, ok := objectRow(t, w, "submit")
	if !ok || r.forward {
		t.Fatal("explicit submit missing", r)
	}
	for i, rr := range w.view(w.current()).rows {
		if rr.key == "submit" {
			w.move(i)
			break
		}
	}
	w.handleKey(tcell.NewEventKey(tcell.KeyRight, 0, 0))
	if len(calls) != 0 {
		t.Fatal("navigation sent an application")
	}
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	if got := <-calls; got["idempotency_key"] != preview["idempotency_key"] || got["dry_run"] != nil || got["target"] != "51" {
		t.Fatal(got)
	}
	w.back()
	w.back()
	click(t, w, "UserStorageQuota:52")
	if r, ok := objectRow(t, w, "file-storage"); !ok || !strings.Contains(r.detail, "0 GiB") {
		t.Fatal(r)
	}
	if _, ok := objectRow(t, w, "gpu"); ok {
		t.Fatal("storage offered compute quota")
	}
}

func TestRemoteFormBindsFreshQuoteAndRequiresCompletePrice(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	doc := `{"quote":"server-quote","details":{"intent":{"action":"renew","target":"8234","units":2},"spec_id":"81","spec_name":"GPU","unit":"CNY","unit_price":4503599627370496.55,"duration_unit":"周","total":9007199254740993.10}}`
	calls := recordFormCalls(w, "compute.remote.checkout", func(map[string]any) (any, error) { return json.RawMessage(doc), nil })
	actions := w.recordActions("remote.instance", "8234", map[string]any{"name": "工位 A", "durationUnit": "周"})
	actions[0].open()
	if _, ok := objectRow(t, w, "arg"); ok {
		t.Fatal("generic argument in renewal")
	}
	if _, ok := objectRow(t, w, "name"); ok {
		t.Fatal("mutable renewal target")
	}
	fillSchoolField(t, w, "units", "2")
	click(t, w, "go")
	waitFor(t, w, func() bool { return !w.current().loading })
	request := <-calls
	if request["target"] != "8234" || request["units"] != float64(2) || request["dry_run"] != true {
		t.Fatal(request)
	}
	page := w.view(w.current())
	var lines []string
	for _, row := range page.rows {
		lines = append(lines, row.title)
	}
	if joined := strings.Join(lines, "\n"); !strings.Contains(joined, "9007199254740993.10 CNY") || !strings.Contains(joined, "2 周") {
		t.Fatal(joined)
	}
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	if args := <-calls; args["quote"] != "server-quote" || args["dry_run"] != nil {
		t.Fatal(args)
	}
	w.back()
	doc = strings.Replace(doc, `"unit":"CNY"`, `"unit":""`, 1)
	click(t, w, "go")
	waitFor(t, w, func() bool { return !w.current().loading })
	<-calls
	if _, ok := objectRow(t, w, "submit"); ok {
		t.Fatal("unpriced order offered for confirmation")
	}
}

func TestUncertainFileEditOffersFreshListing(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	n := 0
	recordFormCalls(w, "data.edit", func(p map[string]any) (any, error) {
		n++
		if p["dry_run"] == true {
			return "预览", nil
		}
		return nil, &net.OpError{Op: "read", Net: "unix", Err: net.ErrClosed}
	})
	w.reviewForm(formRequest{method: "data.edit", params: map[string]any{"action": "remove", "path": "file", "confirm": "file"}}, true, "删除文件")
	waitFor(t, w, func() bool { return !w.current().loading })
	click(t, w, "submit")
	waitFor(t, w, func() bool { return !w.current().submitting })
	if _, ok := objectRow(t, w, "submit"); ok {
		t.Fatal("uncertain write can run twice")
	}
	if _, ok := objectRow(t, w, "verify"); !ok || n != 2 {
		t.Fatal(n)
	}
}

// Observe structured requests at the same boundary used by the real frontend.
func recordFormCalls(w *workspace, method string, reply func(map[string]any) (any, error)) chan map[string]any {
	b := w.Backend.(*fixtureBackend)
	prior := b.reply
	calls := make(chan map[string]any, 8)
	b.reply = func(ctx context.Context, name string, p map[string]any) (any, error) {
		if name == method {
			calls <- p
			return reply(p)
		}
		if prior != nil {
			return prior(ctx, name, p)
		}
		return nil, errors.New(name)
	}
	return calls
}
func TestDiskExpansionFormBindsControllerSizeInGB(t *testing.T) {
	form := formOperations["compute instance resize"].form([]string{"compute", "instance", "resize"})
	request, err := formParams(form, []string{"instance-123"}, map[string]string{"size-gb": "60", "node": "node-7"})
	if err != nil || request.method != "instance.disk-grow" || request.disk == nil ||
		request.disk.TargetCapacityGB != 60 || request.disk.Target != "instance-123" || request.disk.Node != "node-7" {
		t.Fatalf("%+v: %v", request, err)
	}
}
