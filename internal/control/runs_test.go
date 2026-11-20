package control

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
	"github.com/google/uuid"
)

type lifecyclePlatform struct {
	sshFixture *domain.Route
	domain.PlatformAPI
	mu              sync.Mutex
	snap            domain.Snapshot
	requests        []domain.TaskRequest
	authExpired     bool
	normalStopWorks bool
	price           string
	failSyncAfter   int
	syncCalls       int
	cfg             domain.PlatformConfig
}

func (a *lifecyclePlatform) Sync(context.Context) (domain.Snapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.syncCalls++
	if a.authExpired || a.failSyncAfter > 0 && a.syncCalls >= a.failSyncAfter {
		return domain.Snapshot{}, domain.E("GATEWAY_AUTH_EXPIRED", "fixture login expired")
	}
	b, _ := json.Marshal(a.snap)
	var out domain.Snapshot
	_ = json.Unmarshal(b, &out)
	return out, nil
}
func (a *lifecyclePlatform) Config(context.Context) (domain.PlatformConfig, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cfg, nil
}
func (a *lifecyclePlatform) Spec(context.Context, domain.ID) (domain.Spec, error) {
	return domain.Spec{ID: "9", Options: map[string]any{"resourceConstraints": `{"900":{"role":"workStation","constraints":[{"type":"CPU","value":"2","options":"2,4"}]}}`}}, nil
}
func (a *lifecyclePlatform) Close() error { return nil }
func (a *lifecyclePlatform) Submit(ctx context.Context, request domain.TaskRequest) (domain.TaskResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, request)
	r := domain.TaskResult{ID: request.ID, State: "complete"}
	target := domain.ID(strings.Trim(string(domain.Result(request.Payload["appEid"])), `"`))
	if request.Type == "CreateAppTask" {
		name, _ := request.Payload["appName"].(string)
		i := domain.Instance{ID: "20", Name: name, OperateTaskID: request.ID, OwnerID: "7", UserID: "7", GroupID: "0", SpecID: "9", Index: "10", State: "approved", ReserveType: "manual", DurationSeconds: 3600}
		i.Nodes = []domain.Node{{ID: "40", Role: "workStation", Resources: map[string]any{"cpu": "2"}}}
		if route := a.sshFixture; route != nil {
			i.Nodes = []domain.Node{{ID: "40", Role: "workStation", Resources: map[string]any{"cpu": "2"}, Username: route.Username, Services: []domain.Service{{ID: "10", Name: "console"}, {ID: "11", Name: "ssh", Host: route.Host, Port: route.Port, InnerPort: 22}}}}
		}
		a.snap.Instances = append(a.snap.Instances, i)
		r.InstanceID = i.ID
	} else if request.Type == "CreateUserOpRequestTask" {
		var data struct {
			Delay int64 `json:"delaySeconds"`
		}
		_ = json.Unmarshal([]byte(request.Payload["data"].(string)), &data)
		a.snap.Renewals = append(a.snap.Renewals, domain.Renewal{ID: "500", TargetID: "20", Type: "UpdateExpStopAt", State: "pending", RequestedSeconds: data.Delay, CreatedAt: time.Now().UTC()})
	} else {
		for index := range a.snap.Instances {
			i := &a.snap.Instances[index]
			if i.ID != target {
				continue
			}
			switch request.Type {
			case "AppReserveTask":
				i.Reserved = true
			case "StartAppTask":
				i.State = "running"
				i.ExpiresAt = time.Now().Add(time.Duration(number(request.Payload["expDuration"])) * time.Second)
			case "StopAppTask":
				if a.normalStopWorks {
					i.State = "stopped"
				}
			case "CancelAppQueuingTask", "CancelAppReserveQueuingTask":
				i.State = "stopped"
			case "ForceStopAppTask":
				i.State = "stopped"
			case "AppUnreserveTask":
				i.Reserved = false
			case "RemoveAppTask":
				a.snap.Instances = append(a.snap.Instances[:index], a.snap.Instances[index+1:]...)
			}
			break
		}
	}
	a.snap.Tasks = append(a.snap.Tasks, r)
	a.snap.Index++
	return r, nil
}
func (a *lifecyclePlatform) count(typ string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := 0
	for _, r := range a.requests {
		if r.Type == typ {
			n++
		}
	}
	return n
}

type lifecycleJobBackend struct {
	mu              sync.Mutex
	calls           []string
	uniqueLaunches  map[string]bool
	status          JobStatusInfo
	loseLaunchReply bool
	input           JobRequest
	logs            map[string]string
}

func (b *lifecycleJobBackend) Invoke(ctx context.Context, route domain.Route, q JobRequest) (JobStatusInfo, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls = append(b.calls, q.Action)
	if q.Action == "launch" {
		if b.uniqueLaunches == nil {
			b.uniqueLaunches = map[string]bool{}
		}
		b.uniqueLaunches[q.RunID] = true
		b.input = q
		if b.status.State == "" {
			b.status = JobStatusInfo{State: "running", PID: 123, StartedAt: time.Now().Unix()}
		}
		if b.loseLaunchReply {
			b.loseLaunchReply = false
			return JobStatusInfo{}, domain.E("JOB_TRANSPORT", "launch reply lost")
		}
	}
	return b.status, nil
}
func (b *lifecycleJobBackend) ReadLog(ctx context.Context, route domain.Route, id, stream string, offset int64, w io.Writer) (int64, error) {
	b.mu.Lock()
	text := b.logs[stream]
	b.mu.Unlock()
	if offset > int64(len(text)) {
		return 0, io.ErrUnexpectedEOF
	}
	n, e := io.WriteString(w, text[offset:])
	return int64(n), e
}

func lifecycleController(t *testing.T) (*Controller, *lifecyclePlatform, *lifecycleJobBackend) {
	t.Helper()
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	store, e := state.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	api := &lifecyclePlatform{normalStopWorks: true, snap: domain.Snapshot{Identity: domain.Identity{UID: "7", Username: "student", Email: "student@example.edu"}}, cfg: domain.PlatformConfig{App: map[string]any{"delayRequestLimit": 1, "maxReserveTime": 0, "applyTimeNearExpStopAt": 15}}}
	backend := &lifecycleJobBackend{logs: map[string]string{"stdout": "hello\n", "stderr": ""}}
	vault := &coreVault{values: map[string][]byte{}}
	cfg := &config.Config{Selected: "test", Profiles: map[string]domain.Profile{"test": {Name: "test", PlatformURL: "https://example.invalid"}}, Policy: domain.RunPolicy{Cleanup: "release", StopGrace: "60s", ForceStop: true}}
	c, e := New(Options{Config: cfg, Store: store, Vault: vault, JobBackend: backend, Factory: func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return api, nil }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close(); store.Close() })
	return c, api, backend
}
func lifecycleParams(m map[string]any) Params { p, _ := Decode(domain.Result(m)); return p }
func lifecycleWait(t *testing.T, c *Controller, id string, predicate func(domain.Run) bool) domain.Run {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	var r domain.Run
	for time.Now().Before(deadline) {
		c.kickRun(id)
		r, _ = c.Store.GetRun(id)
		if predicate(r) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run did not reach expected state: %+v", r)
	return r
}
func fixtureActiveRun(t *testing.T, c *Controller, a *lifecyclePlatform, job bool) domain.Run {
	t.Helper()
	now := time.Now().UTC()
	i := domain.Instance{ID: "20", Name: "existing", OwnerID: "7", UserID: "7", GroupID: "0", SpecID: "9", State: "running", ReserveType: "manual", Reserved: true, ExpiresAt: now.Add(10 * time.Minute), DurationSeconds: 3600, Meta: map[string]any{"UpdateAppStopAtRequestDeadline": 0.05}, Nodes: []domain.Node{{ID: "40", Username: "student"}}}
	a.mu.Lock()
	a.snap.Instances = []domain.Instance{i}
	a.mu.Unlock()
	r := domain.Run{ID: uuid.NewString(), Profile: "test", Name: i.Name, InstanceID: i.ID, NodeID: "40", Kind: "session", Phase: "active", State: "ready", CreatedAt: now, UpdatedAt: now, Deadline: now.Add(2 * time.Hour), Policy: domain.RunPolicy{MaxRuntime: "2h", Cleanup: "release", StopGrace: "60s", ForceStop: true, AutoRenew: true}, PolicyRevision: 1}
	if job {
		r.Kind = "job"
		r.State = "running"
		r.JobState = "running"
	}
	if e := c.Store.PutRun(r); e != nil {
		t.Fatal(e)
	}
	if e := c.Store.PutRoute(domain.Route{Profile: "test", Name: i.Name, InstanceID: i.ID, NodeID: "40", State: "ready", Kind: "direct"}); e != nil {
		t.Fatal(e)
	}
	intent := runIntent{Operation: "up", Platform: Params{"target": domain.Result("20")}, Command: []string{"echo", "hello"}, PrepareSSH: false}
	if e := c.Vault.Put(runIntentKey(r.ID), intent); e != nil {
		t.Fatal(e)
	}
	return r
}

func TestManagedCreateRestoreStartReuseAndVerifiedRelease(t *testing.T) {
	c, a, _ := lifecycleController(t)
	p := lifecycleParams(map[string]any{"name": "cycle", "spec_id": "9", "max_runtime": "2h", "max_points_per_day": "1.25", "no_ssh": true, "no_wait": true})
	result, e := c.Up(context.Background(), "test", p)
	if e != nil {
		t.Fatal(e)
	}
	r := result.(domain.Run)
	ready := lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.State == "ready" })
	if ready.InstanceID != "20" || a.count("CreateAppTask") != 1 || a.count("AppReserveTask") != 1 || a.count("StartAppTask") != 1 {
		t.Fatalf("bad activation %+v", ready)
	}
	oldDeadline := ready.Deadline
	result, e = c.Up(context.Background(), "test", lifecycleParams(map[string]any{"target": "cycle", "no_wait": true}))
	if e != nil || result.(domain.Run).ID != r.ID || !result.(domain.Run).Deadline.Equal(oldDeadline) {
		t.Fatal("up did not reuse bounded cycle", result, e)
	}
	_, e = c.Down(context.Background(), "test", lifecycleParams(map[string]any{"target": r.ID, "no_wait": true}))
	if e != nil {
		t.Fatal(e)
	}
	finished := lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return managedRunTerminal(r.State) })
	if finished.CleanupState != "released" || a.count("StopAppTask") != 1 || a.count("AppUnreserveTask") != 1 {
		t.Fatal(finished)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.snap.Instances[0].State != "stopped" || a.snap.Instances[0].Reserved {
		t.Fatal("cleanup completed without verified platform state")
	}
}

func TestFiniteCapRequiredBeforeAllocationAndDryRunHasNoManagedMutations(t *testing.T) {
	c, a, _ := lifecycleController(t)
	_, e := c.Up(context.Background(), "test", lifecycleParams(map[string]any{"name": "no-cap", "spec_id": "9", "no_wait": true}))
	if managedCode(e) != "LIMIT_REQUIRED" {
		t.Fatal(e)
	}
	a.mu.Lock()
	a.snap.Instances = []domain.Instance{{ID: "20", Name: "existing", OwnerID: "7", State: "running", Reserved: true}}
	a.mu.Unlock()
	_, e = c.Down(context.Background(), "test", lifecycleParams(map[string]any{"target": "20", "dry_run": true}))
	if e != nil {
		t.Fatal(e)
	}
	runs, _ := c.Store.ListRuns("test")
	if len(runs) != 0 {
		t.Fatal("dry-run persisted a cleanup run", runs)
	}
	tasks, _ := c.Store.ListTasks("test")
	if len(tasks) != 0 || a.count("StopAppTask") != 0 {
		t.Fatal("dry-run submitted a platform mutation")
	}
	r := fixtureActiveRun(t, c, a, false)
	_, e = c.CancelRun(context.Background(), "test", lifecycleParams(map[string]any{"target": r.ID, "dry_run": true}))
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.UpdateRunPolicy(context.Background(), "test", lifecycleParams(map[string]any{"target": r.ID, "max_runtime": "1h", "dry_run": true}))
	if e != nil {
		t.Fatal(e)
	}
	_ = c.ObserveManualOperation(context.Background(), "test", "stop", lifecycleParams(map[string]any{"target": "20", "dry_run": true}))
	unchanged, _ := c.Store.GetRun(r.ID)
	if unchanged.CancelRequested || unchanged.PolicyRevision != r.PolicyRevision || unchanged.Phase != "active" {
		t.Fatal("dry-run changed lifecycle", unchanged)
	}
}

func TestExpiredAuthenticationKeepsSSHRouteAndCleanupPending(t *testing.T) {
	c, a, _ := lifecycleController(t)
	r := fixtureActiveRun(t, c, a, false)
	_ = c.Store.UpdateRun(r.ID, func(r *domain.Run) error { r.Deadline = time.Now().Add(-time.Second); return nil })
	a.mu.Lock()
	a.authExpired = true
	a.mu.Unlock()
	r = lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.State == "cleanup_pending" && r.AuthState == "action_required" })
	route, e := c.CachedRoute("test", "20", "40")
	if e != nil || route.State != "ready" {
		t.Fatal("authentication expiry affected cached route", route, e)
	}
	if a.count("StopAppTask") != 0 {
		t.Fatal("attempted mutation without authentication")
	}
	a.mu.Lock()
	a.authExpired = false
	a.mu.Unlock()
	finished := lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.State == "expired" })
	if finished.CleanupState != "released" {
		t.Fatal(finished)
	}
}

func TestNormalStopWaitsAtLeastSixtySecondsBeforeForce(t *testing.T) {
	c, a, _ := lifecycleController(t)
	r := fixtureActiveRun(t, c, a, false)
	a.mu.Lock()
	a.normalStopWorks = false
	a.mu.Unlock()
	_, e := c.Down(context.Background(), "test", lifecycleParams(map[string]any{"target": r.ID, "no_wait": true}))
	if e != nil {
		t.Fatal(e)
	}
	lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return !r.StopRequestedAt.IsZero() && a.count("StopAppTask") == 1 })
	for range 5 {
		c.kickRun(r.ID)
		time.Sleep(10 * time.Millisecond)
	}
	if a.count("ForceStopAppTask") != 0 {
		t.Fatal("forced stop before grace elapsed")
	}
	_ = c.Store.UpdateRun(r.ID, func(r *domain.Run) error { r.StopRequestedAt = time.Now().Add(-61 * time.Second); return nil })
	finished := lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return managedRunTerminal(r.State) })
	if a.count("ForceStopAppTask") != 1 || finished.CleanupState != "released" {
		t.Fatal(finished)
	}
}

func TestStopGraceDoesNotStartWhenAuthBlocksGentleSubmission(t *testing.T) {
	c, a, _ := lifecycleController(t)
	r := fixtureActiveRun(t, c, a, false)
	_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
		cur.CancelRequested = true
		cur.Phase = "cleanup"
		cur.State = "cleanup_pending"
		return nil
	})
	a.mu.Lock()
	a.normalStopWorks = false
	a.failSyncAfter = a.syncCalls + 2
	a.mu.Unlock()
	_, e := c.advanceRun(context.Background(), r.ID)
	if managedCode(e) != "GATEWAY_AUTH_EXPIRED" {
		t.Fatal(e)
	}
	r, _ = c.Store.GetRun(r.ID)
	if !r.StopRequestedAt.IsZero() || a.count("StopAppTask") != 0 {
		t.Fatal("auth-blocked stop started force grace", r)
	}
	a.mu.Lock()
	a.failSyncAfter = 0
	a.mu.Unlock()
	r = lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return !r.StopRequestedAt.IsZero() })
	if time.Since(r.StopRequestedAt) > time.Second || a.count("ForceStopAppTask") != 0 {
		t.Fatal("force grace was not anchored to accepted normal stop", r)
	}
}

func TestCompletedJobArchivesWhileSchoolAuthIsExpired(t *testing.T) {
	c, a, b := lifecycleController(t)
	r := fixtureActiveRun(t, c, a, true)
	code := 0
	b.mu.Lock()
	b.status = JobStatusInfo{State: "complete", ExitCode: &code}
	b.mu.Unlock()
	a.mu.Lock()
	a.authExpired = true
	a.mu.Unlock()
	r = lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.State == "cleanup_pending" && r.AuthState == "action_required" })
	info, e := c.JobLogArchive(r.ID, "stdout")
	if e != nil || !info.Complete || info.Bytes == 0 {
		t.Fatal("auth expiry prevented SSH log archival", info, e)
	}
	route, e := c.CachedRoute("test", "20", "40")
	if e != nil || route.State != "ready" {
		t.Fatal("auth expiry closed existing route", route, e)
	}
}

func TestRenewalWindowUnitsAndLifetimeBound(t *testing.T) {
	now := time.Now()
	r := domain.Run{Deadline: now.Add(2 * time.Hour), Policy: domain.RunPolicy{AutoRenew: true}}
	i := domain.Instance{ExpiresAt: now.Add(10 * time.Minute), Meta: map[string]any{"UpdateAppStopAtRequestDeadline": 0.05, "reservedAtOrQueueAt": now.Add(-23 * time.Hour).Unix()}}
	cfg := domain.PlatformConfig{App: map[string]any{"delayRequestLimit": 1, "maxReserveTime": 1, "applyTimeNearExpStopAt": 15}}
	seconds, status := renewalPlan(now, r, i, cfg)
	if status != "eligible" || seconds < 2998 || seconds > 3000 {
		t.Fatal("days/hours/minutes limits misinterpreted", seconds, status)
	}
	i.ExpiresAt = now.Add(16 * time.Minute)
	if _, s := renewalPlan(now, r, i, cfg); s != "waiting_window" {
		t.Fatal(s)
	}
	i.ExpiresAt = now.Add(2 * time.Minute)
	if _, s := renewalPlan(now, r, i, cfg); s != "request_deadline_missed" {
		t.Fatal(s)
	}
	i.ExpiresAt = r.Deadline
	if _, s := renewalPlan(now, r, i, cfg); s != "within_limit" {
		t.Fatal(s)
	}
}

func TestRenewalRecoversMissingTaskAndRequiresApprovalPlusExpiry(t *testing.T) {
	c, a, _ := lifecycleController(t)
	r := fixtureActiveRun(t, c, a, false)
	a.mu.Lock()
	i := a.snap.Instances[0]
	a.mu.Unlock()
	phase := "renew/" + fmtInt(i.ExpiresAt.Unix())
	r.RenewalBaseExpiry = i.ExpiresAt
	r.RenewalRequestedSeconds = 3600
	r.RenewalTaskID = TaskID("test", "renew", r.ID+"/"+phase)
	r.LastRenewalAttempt = time.Now()
	r.RenewalState = "submitting"
	if e := c.Store.PutRun(r); e != nil {
		t.Fatal(e)
	}
	r = lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.RenewalState == "pending_approval" })
	if a.count("CreateUserOpRequestTask") != 1 {
		t.Fatal("did not recover missing request")
	}
	for range 5 {
		c.kickRun(r.ID)
		time.Sleep(5 * time.Millisecond)
	}
	if a.count("CreateUserOpRequestTask") != 1 {
		t.Fatal("duplicated pending renewal")
	}
	a.mu.Lock()
	a.snap.Renewals[0].State = "approved"
	a.mu.Unlock()
	r = lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.RenewalState == "approved" })
	if r.RenewalTaskID == "" {
		t.Fatal("claimed success before expiry changed")
	}
	a.mu.Lock()
	a.snap.Instances[0].ExpiresAt = i.ExpiresAt.Add(time.Hour)
	a.mu.Unlock()
	r = lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.RenewalState == "verified" })
	if r.RenewalTaskID != "" {
		t.Fatal(r)
	}
}
func fmtInt(n int64) string { return string(domain.Result(n)) }

func TestUnconfirmedManualStopKeepsOriginalDeadline(t *testing.T) {
	c, school, _ := lifecycleController(t)
	ctx := t.Context()
	run := fixtureActiveRun(t, c, school, false)
	if err := c.ObserveManualOperation(ctx, "test", "stop", lifecycleParams(map[string]any{"target": "20"})); err != nil {
		t.Fatal(err)
	}
	if _, err := c.advanceRun(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	if school.count("AppUnreserveTask") != 0 {
		t.Fatal("manual stop applied the later cleanup early")
	}
	if err := c.Store.UpdateRun(run.ID, func(r *domain.Run) error {
		r.Deadline = time.Now().Add(-time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	final := lifecycleWait(t, c, run.ID, func(r domain.Run) bool { return managedRunTerminal(r.State) })
	if final.TerminalReason != "deadline" || school.count("StopAppTask") != 1 {
		t.Fatal(final, school.count("StopAppTask"))
	}
}

func TestCreateAuthFailureIsResumableAndNoDuplicateCreate(t *testing.T) {
	c, a, _ := lifecycleController(t)
	p := lifecycleParams(map[string]any{"name": "auth-cycle", "spec_id": "9", "max_runtime": "2h", "max_points_per_day": "1.25", "no_ssh": true, "no_wait": true})
	c.intentMu.Lock()
	r, e := c.prepareManagedRun(context.Background(), "test", "up", p, false, nil)
	c.intentMu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	a.mu.Lock()
	a.failSyncAfter = a.syncCalls + 2
	a.mu.Unlock()
	_, e = c.advanceRun(context.Background(), r.ID)
	if managedCode(e) != "GATEWAY_AUTH_EXPIRED" {
		t.Fatal(e)
	}
	r, _ = c.Store.GetRun(r.ID)
	if managedRunTerminal(r.State) {
		t.Fatal("auth expiry terminalized the run", r)
	}
	a.mu.Lock()
	a.failSyncAfter = 0
	a.mu.Unlock()
	r = lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.State == "ready" })
	if a.count("CreateAppTask") != 1 {
		t.Fatal("create was duplicated")
	}
}

func TestJobIntentPrivacyLaunchRecoveryAndEncryptedArchive(t *testing.T) {
	c, a, b := lifecycleController(t)
	base := fixtureActiveRun(t, c, a, false)
	b.mu.Lock()
	b.loseLaunchReply = true
	b.mu.Unlock()
	p := lifecycleParams(map[string]any{"target": base.ID, "max_runtime": "2h", "command": []string{"train", "--token=VERY_PRIVATE_ARGUMENT"}, "env": map[string]string{"API_KEY": "VERY_PRIVATE_ENV"}, "work_dir": "/private/work", "no_wait": true, "idempotency_key": "private-job-1"})
	value, e := c.RunJob(context.Background(), "test", p)
	if e != nil {
		t.Fatal(e)
	}
	r := value.(domain.Run)
	running := lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.State == "running" })
	if len(running.Command) != 0 || running.WorkDir != "" {
		t.Fatal("plaintext command in run state")
	}
	raw, e := os.ReadFile(filepath.Join(c.Store.Dir(), "state.db"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte("VERY_PRIVATE")) {
		t.Fatal("command or environment leaked to state database")
	}
	b.mu.Lock()
	if len(b.uniqueLaunches) != 1 {
		t.Error("duplicate detached job")
	}
	code := 0
	b.status = JobStatusInfo{State: "complete", ExitCode: &code, PID: 123}
	b.mu.Unlock()
	finished := lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.State == "completed" })
	if finished.CleanupState != "released" {
		t.Fatal(finished)
	}
	repeated, e := c.RunJob(context.Background(), "test", p)
	if e != nil || repeated.(domain.Run).ID != r.ID {
		t.Fatal("job retry did not retain its original run after cleanup", repeated, e)
	}
	info, e := c.JobLogArchive(r.ID, "stdout")
	if e != nil || !info.Complete {
		t.Fatal(info, e)
	}
	var output bytes.Buffer
	if _, e = c.ReadArchivedJobLog(context.Background(), r.ID, "stdout", 0, &output); e != nil || output.String() != "hello\n" {
		t.Fatal(output.String(), e)
	}
}

func TestManualStopAndExternalStopNeverRestart(t *testing.T) {
	for _, manual := range []bool{false, true} {
		c, a, _ := lifecycleController(t)
		r := fixtureActiveRun(t, c, a, false)
		if manual {
			if e := c.ObserveManualOperation(context.Background(), "test", "stop", lifecycleParams(map[string]any{"target": "20"})); e != nil {
				t.Fatal(e)
			}
		}
		a.mu.Lock()
		a.snap.Instances[0].State = "stopped"
		a.mu.Unlock()
		r = lifecycleWait(t, c, r.ID, func(r domain.Run) bool { return r.State == "stopped" })
		if a.count("StartAppTask") != 0 || a.count("AppUnreserveTask") != 0 {
			t.Fatal("manual/external stop caused another mutation", r)
		}
	}
}

func TestDetachedPythonLauncherDoesNotDuplicateOrPersistCommand(t *testing.T) {
	python, e := exec.LookPath("python3")
	if e != nil {
		t.Skip("Python unavailable")
	}
	home := t.TempDir()
	rid := uuid.NewString()
	marker := filepath.Join(home, "count")
	q := JobRequest{Action: "launch", RunID: rid, Command: []string{python, "-c", `import os,pathlib,time;time.sleep(.1);p=pathlib.Path(os.environ['MARKER']);p.write_text((p.read_text() if p.exists() else '')+'once\n')`}, Env: map[string]string{"MARKER": marker, "PRIVATE_TEST": "VERY_PRIVATE_REMOTE"}, Deadline: time.Now().Add(time.Minute).Unix(), StopGrace: 60, ForceStop: true}
	body, _ := json.Marshal(q)
	launch := func() error {
		cmd := exec.Command(python, "-c", jobProgram)
		cmd.Env = append(os.Environ(), "HOME="+home)
		cmd.Stdin = bytes.NewReader(body)
		var out bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &out
		return cmd.Run()
	}
	errch := make(chan error, 2)
	go func() { errch <- launch() }()
	go func() { errch <- launch() }()
	if e = <-errch; e != nil {
		t.Fatal(e)
	}
	if e = <-errch; e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(marker)
		if string(data) == "once\n" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, _ := os.ReadFile(marker)
	if string(data) != "once\n" {
		t.Fatal("launcher duplicated or failed", string(data))
	}
	dir, _ := JobDirectory(rid)
	_ = filepath.Walk(filepath.Join(home, dir), func(p string, info os.FileInfo, e error) error {
		if e == nil && !info.IsDir() {
			b, _ := os.ReadFile(p)
			if bytes.Contains(b, []byte("VERY_PRIVATE_REMOTE")) {
				t.Error("remote command environment persisted in status", p)
			}
		}
		return nil
	})
}
