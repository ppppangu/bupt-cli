package tui

import (
	"context"
	"errors"
	"net"
	"slices"
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
	w.Capture = func(_ context.Context, args []string) (string, error) {
		if slices.Contains(args, "--dry-run") {
			return `{"quote":"q","details":{"intent":{"action":"renew","target":"5","units":1},"spec_id":"5","spec_name":"GPU","unit":"CNY","unit_price":2,"duration_unit":"天","total":2}}`, nil
		}
		attempts++
		if attempts == 1 {
			return "", domain.E("QUOTE_CHANGED", "需要重报")
		}
		return "", domain.E("ORDER_RESULT_UNCERTAIN", "待核对")
	}
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
	calls := make(chan []string, 2)
	w.Capture = func(_ context.Context, args []string) (string, error) {
		calls <- slices.Clone(args)
		return "学校预览", nil
	}
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
	want := []string{"compute", "quota", "request", "--gpu=0", "--memory=1.5GiB", "--no-wait", "--reason=教学 作业", "--", "51"}
	preview := <-calls
	if !slices.Equal(preview[len(preview)-len(want):], want) || !slices.Contains(preview, "--dry-run") {
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
	if got := <-calls; !slices.Equal(got[1:], want) || !slices.Contains(preview, got[0]) {
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
	calls := make(chan []string, 3)
	doc := `{"quote":"server-quote","details":{"intent":{"action":"renew","target":"8234","units":2},"spec_id":"81","spec_name":"GPU","unit":"CNY","unit_price":4503599627370496.55,"duration_unit":"周","total":9007199254740993.10}}`
	w.Capture = func(_ context.Context, args []string) (string, error) { calls <- slices.Clone(args); return doc, nil }
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
	want := []string{"compute", "remote", "order", "renew", "--units=2", "--", "8234"}
	if !slices.Equal(request[len(request)-len(want):], want) || !slices.Contains(request, "--dry-run") {
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
	if args := <-calls; !slices.Contains(args, "--quote=server-quote") || slices.Contains(args, "--dry-run") {
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
	w.Capture = func(_ context.Context, args []string) (string, error) {
		n++
		if slices.Contains(args, "--dry-run") {
			return "预览", nil
		}
		return "", &net.OpError{Op: "read", Net: "unix", Err: net.ErrClosed}
	}
	w.runForm([]string{"compute", "storage", "rm", "--confirm=file", "--", "file"}, true, "删除文件")
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
