package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/coder/websocket"
)

func TestEventCreatedByUpdatesExistingActorView(t *testing.T) {
	actor := &view{Index: 5, Entities: map[string]map[domain.ID]map[string]any{"App": {"101": {"eid": json.Number("101"), "hasReserved": false}}}}
	s := &eventSocket{index: 5, views: map[string]*view{"ResourceActor": actor}, tasks: map[string]domain.TaskResult{}, notify: make(chan struct{})}
	update := map[string]any{"operations": []any{map[string]any{"operName": "entityUpdate", "typeName": "App", "eid": 101, "fields": []any{map[string]any{"name": "hasReserved", "value": true}}}}}
	b, _ := json.Marshal([]any{"EVENT", map[string]any{"type": "AppReserved", "createdBy": "ResourceActor", "index": 6, "previousIndex": 5, "viewUpdate": string(domain.Result(update))}})
	if e := s.event(b); e != nil {
		t.Fatal(e)
	}
	if len(s.views) != 1 || !boolValue(actor.Entities["App"]["101"]["hasReserved"]) {
		t.Fatal("event created a shadow view instead of updating the real actor", s.views)
	}
}

func TestPendingTaskRecoversFromFullViewWithoutResending(t *testing.T) {
	const taskID = "32448adc-a967-4eee-8ad6-e85caeb43b25"
	tasks := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/user" {
			writeJSON(w, map[string]any{"code": 0, "uid": 7, "uname": "student"})
			return
		}
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ctx := r.Context()
		_ = ws.Write(ctx, websocket.MessageText, []byte("40"))
		var submissions int
		ready, recovered := false, false
		for {
			name, p, err := readEvent(ctx, ws)
			if err != nil {
				return
			}
			switch name {
			case "TASK":
				if !ready {
					t.Error("task sent before ready acknowledgement")
				}
				if p["id"] != taskID || p["type"] != "StartAppTask" || p["payload"] != `{"appEid":10}` {
					t.Errorf("unexpected task frame: %v", p)
				}
				submissions++
				// Apply the task, but suppress incremental events and TASK_RET.
			case "SYNC":
				ready = false
				if integer(p["index"]) != -1 {
					_ = sendEvent(ctx, ws, "SYNCED", nil)
					continue
				}
				dump := dumpFixture(5)
				resource := dump["views"].([]any)[0].(map[string]any)
				entities := resource["view"].(map[string]any)["memViewDump"].(map[string]any)["entities"].(map[string]any)
				app := entities["App"].([]any)[0].(map[string]any)
				if submissions == 0 {
					app["state"] = "stopped"
				} else {
					entities["Task"] = []any{map[string]any{"eid": 80, "id": taskID, "type": "StartAppTask", "state": "complete", "payload": map[string]any{"appEid": 10}}}
					recovered = true
				}
				_ = sendEvent(ctx, ws, "VIEW_DUMP", dump)
			case "READY_RECEIVE_EVENT":
				ready = true
				if recovered {
					tasks <- submissions
					recovered = false
				}
			}
		}
	}))
	defer srv.Close()
	c := testClient(t, srv)
	defer c.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	initial, err := c.Sync(ctx)
	if err != nil || len(initial.Instances) != 1 || initial.Instances[0].State != "stopped" {
		t.Fatalf("initial snapshot: %+v, %v", initial.Instances, err)
	}
	s := c.socket
	req := domain.TaskRequest{ID: taskID, Type: "StartAppTask", UserID: "7", TargetID: "10"}
	if _, err := s.sendTask(ctx, req, []byte(`{"appEid":10}`)); err != nil {
		t.Fatal(err)
	}
	stale, err := c.Sync(ctx)
	if err != nil || stale.Instances[0].State != "stopped" || terminalTask(stale.Tasks[0].State) {
		t.Fatalf("incremental snapshot: %+v, %+v, %v", stale.Instances, stale.Tasks, err)
	}
	s.mu.Lock()
	s.lastFullSync = time.Now().Add(-taskReceiptRefreshInterval)
	s.mu.Unlock()
	result, err := s.waitTask(ctx, taskID, time.Millisecond)
	if err != nil || result.ID != taskID || result.State != "complete" || result.TargetID != "10" {
		t.Fatalf("recovered receipt: %+v, %v", result, err)
	}
	live, err := c.SyncTaskReceipts(ctx)
	if err != nil || live.Instances[0].State != "running" {
		t.Fatalf("recovered snapshot: %+v, %v", live.Instances, err)
	}
	select {
	case count := <-tasks:
		if count != 1 {
			t.Fatalf("platform received %d task frames", count)
		}
	case <-ctx.Done():
		t.Fatal("school did not receive the full synchronization acknowledgement")
	}
}

func TestDuplicateTaskReceiptReconcilesOriginalResult(t *testing.T) {
	const taskID = "c7d6500a-85df-45eb-b90f-87ea516f75c1"
	submitted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/user" {
			writeJSON(w, map[string]any{"code": 0, "uid": 7, "uname": "student"})
			return
		}
		ws, e := websocket.Accept(w, r, nil)
		if e != nil {
			return
		}
		defer ws.CloseNow()
		ctx, stop := context.WithTimeout(r.Context(), 3*time.Second)
		defer stop()
		_ = ws.Write(ctx, websocket.MessageText, []byte(`0{"sid":"test","pingInterval":10000,"pingTimeout":10000}`))
		_ = ws.Write(ctx, websocket.MessageText, []byte("40"))
		for {
			name, p, e := readEvent(ctx, ws)
			if e != nil {
				return
			}
			switch name {
			case "SYNC":
				dump := dumpFixture(5)
				if submitted {
					views := dump["views"].([]any)
					views = append(views, map[string]any{"name": "TaskService", "view": map[string]any{"index": 5, "memViewDump": map[string]any{"entities": map[string]any{"Task": []any{map[string]any{"eid": 500, "id": taskID, "type": "AppReserveTask", "state": "complete", "code": 0}}}, "relations": []any{}}}})
					dump["views"] = views
				}
				_ = sendEvent(ctx, ws, "VIEW_DUMP", dump)
			case "TASK":
				if submitted {
					t.Error("duplicate UUID was resubmitted after receipt")
				}
				submitted = true
				_ = sendEvent(ctx, ws, "TASK_RET", map[string]any{"id": p["id"], "state": "rejected", "code": 1004})
			}
		}
	}))
	defer srv.Close()
	c := testClient(t, srv)
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	result, e := c.Submit(ctx, domain.TaskRequest{ID: taskID, Type: "AppReserveTask", Payload: map[string]any{"appEid": 101}})
	if e != nil || result.State != "complete" || result.Code != 0 {
		t.Fatal(result, e)
	}
}
