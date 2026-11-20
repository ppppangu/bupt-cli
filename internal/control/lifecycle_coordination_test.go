package control

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
)

type delayedLifecycle struct {
	*lifecyclePlatform
	action              string
	sent                chan domain.TaskRequest
	finish              chan struct{}
	loseAcknowledgement bool
	hideReceipt         atomic.Bool
	request             atomic.Pointer[domain.TaskRequest]
}

func (s *delayedLifecycle) Submit(ctx context.Context, req domain.TaskRequest) (domain.TaskResult, error) {
	if req.Type != s.action {
		return s.lifecyclePlatform.Submit(ctx, req)
	}
	s.request.Store(&req)
	select {
	case s.sent <- req:
	case <-ctx.Done():
		return domain.TaskResult{}, ctx.Err()
	}
	select {
	case <-s.finish:
	case <-ctx.Done():
		return domain.TaskResult{}, ctx.Err()
	}
	r, err := s.lifecyclePlatform.Submit(ctx, req)
	if s.loseAcknowledgement {
		return domain.TaskResult{ID: req.ID}, domain.E("TASK_PENDING", "fixture lost acknowledgement after applying change")
	}
	return r, err
}

func (s *delayedLifecycle) Sync(ctx context.Context) (domain.Snapshot, error) {
	snap, err := s.lifecyclePlatform.Sync(ctx)
	if req := s.request.Load(); req != nil && s.hideReceipt.Load() {
		snap.Tasks = slices.DeleteFunc(snap.Tasks, func(t domain.TaskResult) bool { return t.ID == req.ID })
	}
	return snap, err
}

type fullReceiptLifecycle struct{ *delayedLifecycle }

func (s fullReceiptLifecycle) SyncTaskReceipts(ctx context.Context) (domain.Snapshot, error) {
	return s.lifecyclePlatform.Sync(ctx)
}

func waitLifecycleRequest(t *testing.T, s *delayedLifecycle) domain.TaskRequest {
	t.Helper()
	select {
	case req := <-s.sent:
		return req
	case <-time.After(4 * time.Second):
		t.Fatal("expected a task to reach the school adapter")
		return domain.TaskRequest{}
	}
}

func callInstance(t *testing.T, c *Controller, op, key, target string) (any, error) {
	t.Helper()
	request := domain.RPCRequest{Profile: "test", Method: "instance." + op}
	return c.Handle(t.Context(), request, lifecycleParams(map[string]any{
		"target": target, "max_runtime": "1h", "max_points_per_day": "1.25", "idempotency_key": key, "wait": false, "no_ssh": true,
	}))
}

func TestLifecycleConcurrentInterleaving(t *testing.T) {
	for _, tc := range []struct {
		op, platformTask, state string
		reserved                bool
	}{
		{"stop", "StopAppTask", "running", true},
		{"release", "AppUnreserveTask", "stopped", true},
		{"restore", "AppReserveTask", "stopped", false},
		{"start", "StartAppTask", "stopped", true},
		{"restart", "StopAppTask", "running", true},
	} {
		t.Run(tc.op, func(t *testing.T) {
			c, school, _ := lifecycleController(t)
			school.snap.Instances = []domain.Instance{{
				ID: "20", Name: "existing", OwnerID: "7", UserID: "7", GroupID: "0",
				SpecID: "9", Nodes: []domain.Node{{ID: "40", Role: "workStation", Resources: map[string]any{"cpu": "2"}}}, State: tc.state, ReserveType: "manual", Reserved: tc.reserved, DurationSeconds: 3600,
			}}
			slow := &delayedLifecycle{lifecyclePlatform: school, action: tc.platformTask, sent: make(chan domain.TaskRequest, 20), finish: make(chan struct{})}
			var release sync.Once
			defer release.Do(func() { close(slow.finish) })
			c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return slow, nil }
			first, err := callInstance(t, c, tc.op, "initial", "existing")
			if err != nil {
				t.Fatal(err)
			}
			submitted := waitLifecycleRequest(t, slow)
			var work sync.WaitGroup
			for _, op := range []string{"start", "stop", "restart", "release", "restore"} {
				for index := range 3 {
					work.Go(func() {
						second, err := callInstance(t, c, op, fmt.Sprintf("concurrent-%s-%d", op, index), "20")
						if op == tc.op && (op == "stop" || op == "release") {
							task, ok := second.(domain.TaskRecord)
							if err != nil || !ok || task.ID != submitted.ID {
								t.Errorf("%s should join the existing task: %v %v", op, second, err)
							}
						} else if managedCode(err) != "INSTANCE_OPERATION_PENDING" {
							t.Errorf("%s ran while %s was in flight: %v %v", op, tc.op, second, err)
						}
					})
				}
			}
			work.Wait()
			release.Do(func() { close(slow.finish) })
			if tc.op == "restart" {
				// No SSH route is needed to observe the restart's stop barrier.
				if _, err := c.WaitTask(t.Context(), submitted.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				switch v := first.(type) {
				case domain.TaskRecord:
					out, err := c.waitOperation(ctx, "test", v.ID)
					verified, _ := out.(domain.TaskRecord)
					if err != nil || !verified.Verified {
						t.Fatalf("receipt did not confirm state: %v %v", out, err)
					}
				case domain.Run:
					if _, err := c.waitManaged(ctx, v.ID, false); err != nil {
						t.Fatal(err)
					}
				}
			}
			if school.count(tc.platformTask) != 1 {
				t.Fatal("duplicate task escaped admission")
			}
		})
	}
}

func TestLifecycleLostReplyReconcilesAcrossControllerRestart(t *testing.T) {
	c, school, _ := lifecycleController(t)
	fixtureActiveRun(t, c, school, false)
	slow := &delayedLifecycle{lifecyclePlatform: school, action: "StopAppTask",
		sent: make(chan domain.TaskRequest, 20), finish: make(chan struct{}), loseAcknowledgement: true}
	slow.hideReceipt.Store(true)
	close(slow.finish)
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return slow, nil }

	first, err := callInstance(t, c, "stop", "original", "20")
	if err != nil {
		t.Fatal(err)
	}
	id := first.(domain.TaskRecord).ID
	waitLifecycleRequest(t, slow)
	deadline := time.After(4 * time.Second)
	for {
		rec, err := c.Store.GetTask(id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.State == "uncertain" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("task did not record the lost reply")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	path := c.Store.Dir()
	if err := c.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	options := c.opts
	options.Store = store
	next, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	if _, err = callInstance(t, next, "start", "early", "20"); managedCode(err) != "INSTANCE_OPERATION_PENDING" {
		t.Fatalf("must fence even though stop was applied: %v", err)
	}
	for _, key := range []string{"original", "another-client"} {
		out, err := callInstance(t, next, "stop", key, "20")
		if err != nil || out.(domain.TaskRecord).ID != id {
			t.Fatal(out, err)
		}
	}
	slow.hideReceipt.Store(false)
	out, err := next.waitOperation(ctx, "test", id)
	if err != nil || !out.(domain.TaskRecord).Verified || school.count("StopAppTask") != 1 {
		t.Fatal(out, err)
	}
	noop, err := callInstance(t, next, "stop", "already-stopped", "20")
	if err != nil || !noop.(domain.TaskRecord).Noop {
		t.Fatal(noop, err)
	}
	started, err := callInstance(t, next, "start", "after-observation", "20")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := next.waitManaged(ctx, started.(domain.Run).ID, false); err != nil {
		t.Fatal(err)
	}
	if out, err := next.waitOperation(ctx, "test", id); err != nil || !out.(domain.TaskRecord).Verified {
		t.Fatal(out, err)
	}
	if school.count("StopAppTask") != 1 || school.count("StartAppTask") != 1 {
		t.Fatal("recovery did not preserve intended effects")
	}
}

func TestCleanupWaitsForLateRestoreBeforeReleasing(t *testing.T) {
	c, school, _ := lifecycleController(t)
	school.snap.Instances = []domain.Instance{{
		ID: "20", Name: "existing", OwnerID: "7", UserID: "7", GroupID: "0", SpecID: "9",
		State: "stopped", ReserveType: "manual",
		Nodes: []domain.Node{{ID: "40", Role: "workStation", Resources: map[string]any{"cpu": "2"}}},
	}}
	slow := &delayedLifecycle{
		lifecyclePlatform: school, action: "AppReserveTask",
		sent: make(chan domain.TaskRequest, 1), finish: make(chan struct{}),
	}
	slow.hideReceipt.Store(true)
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) {
		return fullReceiptLifecycle{slow}, nil
	}
	out, err := callInstance(t, c, "restore", "restore", "20")
	if err != nil {
		t.Fatal(err)
	}
	runID := out.(domain.Run).ID
	request := waitLifecycleRequest(t, slow)
	_, err = c.Down(t.Context(), "test", lifecycleParams(map[string]any{
		"target": runID, "cleanup": "release", "wait": false,
	}))
	if err != nil {
		t.Fatal(err)
	}
	lifecycleWait(t, c, runID, func(r domain.Run) bool {
		return r.Phase == "cleanup" && r.State == "cleanup_pending" && r.LastError == "TASK_PENDING"
	})
	snapshot, err := school.Sync(t.Context())
	if err != nil || snapshot.Instances[0].Reserved || school.count("AppUnreserveTask") != 0 {
		t.Fatalf("initial snapshot must remain stopped and unreserved: %+v %v", snapshot, err)
	}
	// The simulated school finally applies the original request and exposes its
	// outcome only in full snapshots after the controller requested cleanup.
	if _, err := school.Submit(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	final := lifecycleWait(t, c, runID, func(r domain.Run) bool { return managedRunTerminal(r.State) })
	snapshot, err = school.Sync(t.Context())
	if err != nil || final.CleanupState != "released" || snapshot.Instances[0].Reserved ||
		school.count("AppReserveTask") != 1 || school.count("AppUnreserveTask") != 1 || school.count("StartAppTask") != 0 {
		t.Fatalf("late reservation did not get released: %+v %+v %v", final, snapshot, err)
	}
}

func TestInstanceEventsSeparatesSchoolReceiptsAndLocalHistory(t *testing.T) {
	c, school, _ := lifecycleController(t)
	fixtureActiveRun(t, c, school, false)
	school.snap.Tasks = []domain.TaskResult{
		{ID: "own", TargetID: "20", Kind: "StopAppTask", State: "complete", EventIndex: 7, Data: map[string]any{"private": "must not show"}},
		{ID: "other", TargetID: "21", State: "complete"},
	}
	c.Audit("own", "test", "20", "", "", "local stop")
	c.Audit("other", "test", "21", "", "", "unrelated")
	out, err := c.instanceEvents(t.Context(), "test", lifecycleParams(map[string]any{"target": "existing", "limit": 1}))
	if err != nil {
		t.Fatal(err)
	}
	var events struct {
		School struct {
			Receipts []domain.TaskResult
			Complete bool `json:"history_complete"`
		}
		Controller struct{ Events []domain.AuditEvent }
	}
	if err := json.Unmarshal(domain.Result(out), &events); err != nil || events.School.Complete ||
		len(events.School.Receipts) != 1 || events.School.Receipts[0].ID != "own" || events.School.Receipts[0].Data != nil ||
		len(events.Controller.Events) != 1 || events.Controller.Events[0].Kind != "own" {
		t.Fatal(events, err)
	}
}
