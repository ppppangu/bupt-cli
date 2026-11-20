package control

import (
	"context"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
)

func instanceStopped(i domain.Instance) bool {
	return i.State == "stopped" || i.State == "approved" || i.State == "released"
}

func instanceReserved(i domain.Instance) bool {
	return i.Reserved || i.ReserveType == "no"
}

func lifecycleTask(op string) bool {
	switch op {
	case "start", "stop", "force-stop", "release", "restore", "delete", "cancel-queue", "renew",
		"resize", "disk-grow", "snapshot-create", "snapshot-restore", "snapshot-delete", "service-add", "service-delete":
		return true
	}
	return false
}

func instanceTaskObserved(op string, i domain.Instance) bool {
	if i.State == "unknown" {
		return false
	}
	if i.ID == "" {
		return op == "delete"
	}
	switch op {
	case "stop", "force-stop":
		return instanceStopped(i)
	case "release":
		return instanceStopped(i) && !i.Reserved
	case "cancel-queue":
		return !strings.Contains(i.State, "queuing")
	}
	return false
}

// intentMu serializes publication of lifecycle work. The durable records and
// authoritative instance ID, rather than a lock held during I/O, fence later
// requests for this resource across controller restarts.
func (c *Controller) pendingInstanceTask(ctx context.Context, profile string, instance domain.Instance) (domain.TaskRecord, error) {
	tasks, err := c.Store.ListTasks(profile)
	if err != nil {
		return domain.TaskRecord{}, err
	}
	for _, t := range tasks {
		if t.ParentID != "" || t.Request.TargetID != instance.ID || !lifecycleTask(t.Operation) {
			continue
		}
		if !terminalTask(t.State) {
			return t, nil
		}
		if t.Manual && !t.Verified && t.State == "complete" && taskError(t) == nil {
			if !instanceTaskObserved(t.Operation, instance) {
				return t, nil
			}
			if _, err := c.confirmManualTask(ctx, t); err != nil {
				return t, err
			}
		}
	}
	return domain.TaskRecord{}, nil
}

func lifecycleBusy(t domain.TaskRecord) error {
	return &domain.Error{
		Code: "INSTANCE_OPERATION_PENDING", Message: "实例仍有操作待学校确认，请按回执查询后再继续",
		TaskID: t.ID, Retryable: true,
	}
}

func runBusy(r domain.Run) error {
	return &domain.Error{
		Code: "INSTANCE_OPERATION_PENDING", Message: "实例正在转换状态，请等待当前操作",
		RunID: r.ID, TaskID: r.LastTaskID, Retryable: true,
	}
}

func (c *Controller) resolveLifecycleTarget(s domain.Snapshot, profile, target string) (domain.Instance, error) {
	i, err := Resolve(s, target)
	if managedCode(err) == "NOT_FOUND" {
		if run, lookup := c.GetRun(profile, target); lookup == nil {
			if run.InstanceID == "" {
				return i, runBusy(run)
			}
			i, err = Resolve(s, string(run.InstanceID))
		} else if managedCode(lookup) == "AMBIGUOUS_RUN" {
			return i, lookup
		}
	}
	if err == nil {
		err = Owned(s, i)
	}
	return i, err
}

func (c *Controller) prepareManualTask(ctx context.Context, profile, op string, p Params) (domain.TaskRecord, error) {
	c.intentMu.Lock()
	defer c.intentMu.Unlock()
	id := TaskID(profile, op, p.String("idempotency_key"))
	hash := intentHash(profile, op, p)
	if t, err := c.taskReceipt(id, hash); !state.IsNotFound(err) {
		return t, err
	}
	s, err := c.snapshot(ctx, profile)
	if err != nil {
		return domain.TaskRecord{}, err
	}
	i, err := c.resolveLifecycleTarget(s, profile, p.String("target"))
	if err != nil {
		return domain.TaskRecord{}, err
	}
	if op == "delete" && p.String("confirm") != string(i.ID) {
		return domain.TaskRecord{}, domain.E("CONFIRM_REQUIRED", "请用 --confirm INSTANCE_ID 确认目标")
	}
	if t, err := c.pendingInstanceTask(ctx, profile, i); err != nil {
		return t, err
	} else if t.ID != "" {
		if t.Manual && t.Operation == op {
			err = c.Store.Put("task_keys", id, taskAlias{TaskID: t.ID, Hash: hash})
			return t, err
		}
		return t, lifecycleBusy(t)
	}

	r, err := c.GetRun(profile, string(i.ID))
	if err != nil && managedCode(err) != "NOT_FOUND" {
		return domain.TaskRecord{}, err
	}
	active := err == nil && !managedRunTerminal(r.State)
	queue := strings.Contains(i.State, "queuing")
	if active && r.Phase != "active" && r.Phase != "restart_failed" && !(op == "cancel-queue" && queue && r.Phase == "activate") {
		return domain.TaskRecord{}, runBusy(r)
	}

	now := time.Now().UTC()
	t := domain.TaskRecord{ID: id, Profile: profile, Operation: op, IntentHash: hash, Manual: true, CreatedAt: now}
	if op != "cancel-queue" && instanceTaskObserved(op, i) {
		t.Request = domain.TaskRequest{ID: id, Type: op, UserID: s.Identity.UID, TargetID: i.ID}
		t.State, t.Verified, t.Noop = "complete", true, true
		t.Result = &domain.TaskResult{ID: id, State: "complete", Message: "实例已达到请求状态"}
		return c.savePreparedTask(t, nil)
	}
	if (op == "stop" || op == "force-stop") && i.State != "running" && i.State != "starting" {
		return t, domain.E("INVALID_STATE", "仅可停机运行中或启动中的实例，排队时请使用 cancel-queue")
	}
	params := make(Params, len(p))
	for key, value := range p {
		params[key] = value
	}
	params.Set("target", string(i.ID))
	t.Request, err = c.buildTaskFromSnapshot(ctx, profile, op, params, s)
	if err != nil {
		return t, err
	}
	t.Request.ID = id
	if active {
		t.RunID = r.ID
	}
	t, err = c.savePreparedTask(t, params)
	if err == nil {
		err = c.bindManualTaskLocked(t)
	}
	return t, err
}

func (c *Controller) bindManualTask(t domain.TaskRecord) error {
	c.intentMu.Lock()
	defer c.intentMu.Unlock()
	return c.bindManualTaskLocked(t)
}

func (c *Controller) bindManualTaskLocked(t domain.TaskRecord) error {
	if t.RunID == "" {
		return nil
	}
	return c.Store.UpdateRun(t.RunID, func(r *domain.Run) error {
		if managedRunTerminal(r.State) || r.ManualTaskID == t.ID {
			return nil
		}
		if r.Phase == "manual_wait" {
			return runBusy(*r)
		}
		r.ManualPreviousPhase, r.ManualPreviousState = r.Phase, r.State
		r.ManualTaskID, r.LastTaskID = t.ID, t.ID
		r.CancelRequested = true
		r.TerminalReason, r.Phase, r.State, r.CleanupState = "manual-"+t.Operation, "manual_wait", "cleanup_pending", "manual_operation"
		return nil
	})
}

// Once observed, a receipt stays verified even if a later action changes the
// platform state. This lets older waits return their original outcome.
func (c *Controller) confirmManualTask(ctx context.Context, t domain.TaskRecord) (domain.TaskRecord, error) {
	if t.Verified {
		return t, nil
	}
	if t.RunID != "" {
		r, err := c.Store.GetRun(t.RunID)
		if err != nil {
			return t, err
		}
		if r.ManualTaskID == t.ID && !managedRunTerminal(r.State) {
			if _, err := c.finishRun(ctx, r, "manual_complete"); err != nil {
				return t, err
			}
		}
	}
	t.Verified = true
	return t, c.Store.PutTask(t)
}

func (c *Controller) advanceManualTask(ctx context.Context, r domain.Run, i domain.Instance) (bool, error) {
	c.intentMu.Lock()
	defer c.intentMu.Unlock()
	t, err := c.Store.GetTask(r.ManualTaskID)
	if err != nil {
		return false, err
	}
	if !terminalTask(t.State) {
		c.resumeTask(t)
		return false, nil
	}
	if err := taskError(t); err != nil {
		return true, c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
			if cur.Phase == "manual_wait" && cur.ManualTaskID == t.ID {
				cur.Phase, cur.State = cur.ManualPreviousPhase, cur.ManualPreviousState
				cur.CancelRequested = false
				cur.TerminalReason, cur.CleanupState, cur.ManualTaskID = "", "", ""
			}
			return nil
		})
	}
	if instanceTaskObserved(t.Operation, i) {
		_, err = c.confirmManualTask(ctx, t)
		return false, err
	}
	return false, nil
}
