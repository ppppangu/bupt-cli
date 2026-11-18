package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
	"github.com/google/uuid"
)

type runIntent struct {
	RequestKey  string            `json:"request_key,omitempty"`
	RequestHash string            `json:"request_hash,omitempty"`
	Operation   string            `json:"operation"`
	Platform    Params            `json:"platform"`
	Command     []string          `json:"command,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	WorkDir     string            `json:"work_dir,omitempty"`
	PrepareSSH  bool              `json:"prepare_ssh"`
	Node        string            `json:"node,omitempty"`
}

func runIntentKey(id string) string { return "run/" + id + "/intent" }
func managedRunTerminal(s string) bool {
	switch s {
	case "completed", "cancelled", "expired", "failed", "stopped", "released", "deleted":
		return true
	}
	return false
}
func platformRunParams(p Params) Params {
	out := Params{}
	for _, k := range []string{"target", "name", "preset", "preset_snapshot", "spec_id", "group_id", "bill_id", "email", "usage", "domain", "constraints", "resources", "duration"} {
		if v, ok := p[k]; ok {
			out[k] = v
		}
	}
	return out
}
func managedCode(e error) string {
	var de *domain.Error
	if errors.As(e, &de) {
		return de.Code
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	if errors.Is(e, context.Canceled) {
		return "CANCELLED"
	}
	return "OPERATION_FAILED"
}
func managedRetryable(e error) bool {
	var de *domain.Error
	if errors.As(e, &de) && de.Retryable {
		return true
	}
	code := managedCode(e)
	for _, part := range []string{"AUTH", "CAPTCHA", "TOKEN", "NETWORK", "EVENT_", "TASK_PENDING", "TIMEOUT", "CANCELLED", "POLICY_CHANGED"} {
		if strings.Contains(code, part) {
			return true
		}
	}
	return false
}
func (c *Controller) loadRunIntent(r domain.Run) (runIntent, error) {
	var in runIntent
	if c.Vault == nil {
		return in, domain.E("SECRET_STORE", "managed runs require an encrypted intent store")
	}
	if e := c.Vault.Get(runIntentKey(r.ID), &in); e != nil {
		return in, domain.E("RUN_INTENT_UNAVAILABLE", "encrypted run intent is unavailable")
	}
	return in, nil
}

func (c *Controller) GetRun(profile, target string) (domain.Run, error) {
	if target == "" {
		return domain.Run{}, domain.E("TARGET_REQUIRED", "supply a run or instance target")
	}
	if profile == "" {
		profile = c.Configuration().Selected
	}
	if r, e := c.Store.GetRun(target); e == nil {
		if r.Profile != profile {
			return domain.Run{}, domain.E("NOT_FOUND", "run belongs to another profile")
		}
		return r, nil
	}
	runs, e := c.Store.ListRuns(profile)
	if e != nil {
		return domain.Run{}, e
	}
	matches := []domain.Run{}
	active := []domain.Run{}
	for _, r := range runs {
		if target == r.Name || target == string(r.InstanceID) {
			matches = append(matches, r)
			if !managedRunTerminal(r.State) {
				active = append(active, r)
			}
		}
	}
	if len(active) == 1 {
		return active[0], nil
	}
	if len(active) > 1 {
		return domain.Run{}, domain.E("AMBIGUOUS_RUN", "target has multiple active runs; use a run ID")
	}
	if len(matches) > 0 {
		latest := matches[len(matches)-1]
		for _, r := range matches {
			if r.InstanceID != latest.InstanceID {
				return domain.Run{}, domain.E("AMBIGUOUS_RUN", "name refers to different instances; use a run ID")
			}
		}
		return latest, nil
	}
	return domain.Run{}, domain.E("NOT_FOUND", "managed run not found")
}

func (c *Controller) Up(ctx context.Context, profile string, p Params) (any, error) {
	return c.newManagedRun(ctx, profile, "up", p, false)
}
func (c *Controller) ManagedOperation(ctx context.Context, profile, op string, p Params) (any, error) {
	switch op {
	case "create", "clone", "start", "restore", "adopt":
		return c.newManagedRun(ctx, profile, op, p, false)
	default:
		return nil, domain.E("UNKNOWN_OPERATION", "operation is not a managed allocation action")
	}
}
func (c *Controller) RunJob(ctx context.Context, profile string, p Params) (any, error) {
	if !p.Has("work_dir") && p.Has("workdir") {
		copy := Params{}
		for k, v := range p {
			copy[k] = v
		}
		copy["work_dir"] = p["workdir"]
		p = copy
	}
	return c.newManagedRun(ctx, profile, "up", p, true)
}

func (c *Controller) newManagedRun(ctx context.Context, profile, op string, p Params, job bool) (any, error) {
	if profile == "" {
		profile = c.Configuration().Selected
	}
	if _, ok := c.Configuration().Profiles[profile]; !ok {
		return nil, domain.E("PROFILE_NOT_FOUND", "profile is not configured")
	}
	c.intentMu.Lock()
	r, e := c.prepareManagedRun(ctx, profile, op, p, job)
	c.intentMu.Unlock()
	if e != nil {
		return nil, e
	}
	if p.Bool("dry_run") {
		return map[string]any{"dry_run": true, "run": r}, nil
	}
	c.kickRun(r.ID)
	if p.Bool("no_wait") || p.Bool("detach") || (p.Has("wait") && !p.Bool("wait")) {
		return r, nil
	}
	return c.waitManaged(ctx, r.ID, false)
}
func (c *Controller) prepareManagedRun(ctx context.Context, profile, op string, p Params, job bool) (domain.Run, error) {
	if selector := p.String("spec_id"); selector != "" && !numericSpecSelector(selector) {
		spec, e := c.ResolveSpec(ctx, profile, selector)
		if e != nil {
			return domain.Run{}, e
		}
		copy := Params{}
		for key, value := range p {
			copy[key] = value
		}
		copy.Set("spec_id", string(spec.ID))
		p = copy
	}
	var command []string
	env := map[string]string{}
	if job {
		if e := p.Into("command", &command); e != nil {
			return domain.Run{}, e
		}
		if len(command) == 0 {
			_ = p.Into("argv", &command)
		}
		if e := p.Into("env", &env); e != nil {
			return domain.Run{}, e
		}
		if e := validateJob(command, env, p.String("work_dir")); e != nil {
			return domain.Run{}, e
		}
	}
	target := p.String("target")
	name := p.String("name")
	if name == "" {
		if preset := p.String("preset"); preset != "" {
			if pr, e := c.preset(p, profile); e == nil {
				name = pr.Name
			}
		}
	}
	lookup := target
	if lookup == "" {
		lookup = name
	}
	key := p.String("idempotency_key")
	if job && name == "" && target == "" {
		generated := uuid.NewString()
		if key != "" {
			generated = TaskID(profile, "job-name", key)
		}
		name = "job-" + generated[:8]
		lookup = name
	}
	kind := "session"
	if op == "create" || op == "clone" || op == "restore" {
		kind = "allocation"
	}
	if job {
		kind = "job"
	}
	intent := runIntent{Operation: op, Platform: platformRunParams(p), Command: command, Env: env, WorkDir: p.String("work_dir"), PrepareSSH: kind != "allocation" && !p.Bool("no_ssh"), Node: p.String("node")}
	fingerprint := intentHash(profile, "managed/"+kind+"/"+op, p)
	intent.RequestKey = key
	intent.RequestHash = fingerprint
	if key != "" {
		rid := TaskID(profile, "managed-run", key)
		var alias struct {
			RunID string `json:"run_id"`
			Hash  string `json:"hash"`
		}
		if c.Store.Get("run_keys", rid, &alias) == nil {
			if alias.Hash != fingerprint {
				return domain.Run{}, domain.E("IDEMPOTENCY_CONFLICT", "run key was used for different arguments")
			}
			return c.Store.GetRun(alias.RunID)
		}
		if existing, e := c.Store.GetRun(rid); e == nil {
			if existing.IntentHash != fingerprint {
				return existing, domain.E("IDEMPOTENCY_CONFLICT", "run key was used for different arguments")
			}
			return existing, nil
		}
	}
	if job && key != "" && lookup != "" {
		if existing, e := c.GetRun(profile, lookup); e == nil {
			if old, e := c.loadRunIntent(existing); e == nil && old.RequestKey == key && old.RequestHash == fingerprint {
				if !p.Bool("dry_run") {
					_ = c.Store.Put("run_keys", TaskID(profile, "managed-run", key), map[string]string{"run_id": existing.ID, "hash": fingerprint})
				}
				return existing, nil
			}
		}
	}
	if lookup != "" && op != "clone" && op != "create" {
		if existing, e := c.GetRun(profile, lookup); e == nil && !managedRunTerminal(existing.State) {
			if job && existing.Kind == "job" {
				return existing, domain.E("INSTANCE_IN_USE", "instance already has a tracked job; attach by run ID")
			}
			if existing.Phase == "manual_wait" || existing.CancelRequested || existing.State == "cleanup_pending" {
				return existing, domain.E("CLEANUP_PENDING", "finish the current cleanup before starting another cycle")
			}
			if job || existing.Kind == "allocation" && kind == "session" {
				old, e := c.loadRunIntent(existing)
				if e != nil {
					return existing, e
				}
				old.Operation = "up"
				old.Command = command
				old.Env = env
				old.WorkDir = intent.WorkDir
				old.PrepareSSH = intent.PrepareSSH
				old.Node = intent.Node
				if job {
					old.RequestKey = key
					old.RequestHash = fingerprint
				}
				if job && !p.Bool("inherit_policy") {
					policy, e := c.policy(p, profile)
					if e != nil {
						return existing, e
					}
					deadline, e := policy.Deadline(existing.CreatedAt)
					if e != nil {
						return existing, e
					}
					if !deadline.After(time.Now()) {
						return existing, domain.E("LIMIT_EXPIRED", "the requested run limit has already elapsed")
					}
					existing.Policy = policy
					existing.Deadline = deadline
					existing.PolicyRevision++
				}
				if !p.Bool("dry_run") {
					if e = c.Vault.Put(runIntentKey(existing.ID), old); e != nil {
						return existing, e
					}
					existing.Kind = kind
					if job && p.String("name") != "" {
						existing.JobName = p.String("name")
					}
					existing.Phase = "activate"
					existing.State = "preparing"
					existing.UpdatedAt = time.Now().UTC()
					if e = c.Store.PutRun(existing); e != nil {
						return existing, e
					}
				}
				if job && key != "" && !p.Bool("dry_run") {
					if e = c.Store.Put("run_keys", TaskID(profile, "managed-run", key), map[string]string{"run_id": existing.ID, "hash": fingerprint}); e != nil {
						return existing, e
					}
				}
			}
			return existing, nil
		}
	}
	managedBefore := false
	if lookup != "" && op != "create" && op != "clone" {
		previous, err := c.GetRun(profile, lookup)
		if err == nil && previous.InstanceID != "" {
			target = string(previous.InstanceID)
			managedBefore = true
			if name == "" {
				name = previous.Name
			}
		}
		if err != nil && managedCode(err) != "NOT_FOUND" {
			return domain.Run{}, err
		}
	}
	policy, e := c.policy(p, profile)
	if e != nil {
		return domain.Run{}, e
	}
	now := time.Now().UTC()
	deadline, e := policy.Deadline(now)
	if e != nil {
		return domain.Run{}, e
	}
	grace, e := time.ParseDuration(policy.StopGrace)
	if e != nil || grace < 60*time.Second {
		return domain.Run{}, domain.E("INVALID_POLICY", "normal stop grace must be at least 60 seconds")
	}
	if c.Vault == nil {
		return domain.Run{}, domain.E("SECRET_STORE", "managed runs require encrypted credentials and intent storage")
	}
	s, e := c.snapshot(ctx, profile)
	if e != nil {
		return domain.Run{}, e
	}
	var instance domain.Instance
	if target != "" && op != "clone" {
		instance, e = Resolve(s, target)
		if e != nil {
			return domain.Run{}, e
		}
		if e = Owned(s, instance); e != nil {
			return domain.Run{}, e
		}
	}
	if instance.ID == "" && (op == "start" || op == "restore" || op == "adopt") {
		return domain.Run{}, domain.E("TARGET_REQUIRED", "this operation requires an existing instance")
	}
	if instance.ID != "" {
		for _, other := range mustRuns(c, profile) {
			if other.InstanceID == instance.ID && !managedRunTerminal(other.State) {
				return other, domain.E("INSTANCE_IN_USE", "instance already belongs to an active managed cycle")
			}
		}
		if op == "up" && !p.Bool("adopt") && !managedBefore {
			return domain.Run{}, domain.E("ADOPTION_REQUIRED", "use adopt or --adopt to manage an existing instance")
		}
		if name == "" {
			name = instance.Name
		}
		intent.Platform.Set("target", string(instance.ID))
		if op == "adopt" && instance.State != "running" {
			kind = "allocation"
			intent.PrepareSSH = false
		}
	}
	if name == "" {
		if preset := p.String("preset"); preset != "" {
			if pr, e := c.preset(p, profile); e == nil {
				name = pr.Name
			}
		}
	}
	if name == "" {
		return domain.Run{}, domain.E("NAME_REQUIRED", "supply a name for the managed instance")
	}
	intent.Platform.Set("name", name)
	if instance.ID == "" {
		createOp := "create"
		if op == "clone" {
			createOp = "clone"
		}
		if _, e = c.buildTask(ctx, profile, createOp, intent.Platform); e != nil {
			return domain.Run{}, e
		}
	}
	rid := uuid.NewString()
	if key != "" {
		rid = TaskID(profile, "managed-run", key)
	}
	r := domain.Run{ID: rid, Profile: profile, Name: name, InstanceID: instance.ID, Kind: kind, Phase: "activate", State: "preparing", CreatedAt: now, UpdatedAt: now, Deadline: deadline, Policy: policy, PolicyRevision: 1, IntentHash: fingerprint, CreatedInstance: instance.ID == "", ConfigSources: c.runSources(p, profile)}
	if p.Bool("dry_run") {
		return r, nil
	}
	if e = c.Vault.Put(runIntentKey(rid), intent); e != nil {
		return r, domain.E("SECRET_STORE", "could not persist encrypted run intent")
	}
	if e = c.Store.PutRun(r); e != nil {
		return r, e
	}
	c.Audit("run.created", profile, string(r.InstanceID), "", r.ID, "bounded managed cycle created")
	return r, nil
}
func mustRuns(c *Controller, profile string) []domain.Run {
	runs, _ := c.Store.ListRuns(profile)
	return runs
}
func (c *Controller) kickRun(id string) {
	c.spawn("run/"+id, func() {
		ctx, cancel := context.WithTimeout(c.ctx, 2*time.Minute)
		defer cancel()
		for range 8 {
			progress, e := c.advanceRun(ctx, id)
			if e != nil {
				_ = c.Store.UpdateRun(id, func(r *domain.Run) error {
					r.LastError = managedCode(e)
					if strings.Contains(r.LastError, "AUTH") || strings.Contains(r.LastError, "CAPTCHA") || strings.Contains(r.LastError, "TOKEN") {
						r.AuthState = "action_required"
					}
					return nil
				})
				return
			}
			if !progress {
				return
			}
		}
	})
}
func (c *Controller) Reconcile(ctx context.Context) error {
	runs, e := c.Store.ListRuns("")
	if e != nil {
		return e
	}
	for _, r := range runs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !managedRunTerminal(r.State) {
			c.kickRun(r.ID)
		}
	}
	return nil
}
func (c *Controller) waitManaged(ctx context.Context, id string, cleanup bool) (domain.Run, error) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		r, e := c.Store.GetRun(id)
		if e != nil {
			return r, e
		}
		if managedRunTerminal(r.State) {
			if r.State == "failed" {
				return r, &domain.Error{Code: "RUN_FAILED", Message: "managed run failed; inspect its lifecycle status", RunID: r.ID, TaskID: r.LastTaskID}
			}
			if !cleanup {
				return r, &domain.Error{Code: "RUN_ENDED", Message: "run ended before becoming ready", RunID: r.ID, TaskID: r.LastTaskID}
			}
			return r, nil
		}
		if r.LastError != "" && (r.AuthState == "action_required" || strings.HasPrefix(r.LastError, "CONSOLE_") || r.LastError == "RELAY_REQUIRED" || r.LastError == "SSH_NOT_READY") {
			return r, &domain.Error{Code: r.LastError, Message: "run remains tracked and needs action before it can continue", RunID: r.ID, TaskID: r.LastTaskID, Retryable: true}
		}
		if !cleanup && (r.State == "running" || r.Kind != "job" && (r.State == "ready" || r.State == "allocated")) {
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, &domain.Error{Code: "RUN_PENDING", Message: "waiting ended; run remains managed", RunID: r.ID, TaskID: r.LastTaskID, Retryable: true}
		case <-tick.C:
			c.kickRun(id)
		}
	}
}

func (c *Controller) Down(ctx context.Context, profile string, p Params) (any, error) {
	if profile == "" {
		profile = c.Configuration().Selected
	}
	target := p.String("target")
	r, e := c.GetRun(profile, target)
	newRun := false
	if e == nil && managedRunTerminal(r.State) {
		now := time.Now().UTC()
		r = domain.Run{ID: uuid.NewString(), Profile: profile, Name: r.Name, InstanceID: r.InstanceID, NodeID: r.NodeID, Kind: "session", State: "cleanup_pending", Phase: "cleanup", CreatedAt: now, UpdatedAt: now, Deadline: now, Policy: r.Policy, PolicyRevision: 1}
		newRun = true
	}
	if e != nil {
		var de *domain.Error
		if !errors.As(e, &de) || de.Code != "NOT_FOUND" {
			return nil, e
		}
		s, se := c.snapshot(ctx, profile)
		if se != nil {
			return nil, se
		}
		i, se := Resolve(s, target)
		if se != nil {
			return nil, se
		}
		if se = Owned(s, i); se != nil {
			return nil, se
		}
		policy, se := c.policy(p, profile)
		if se != nil {
			return nil, se
		}
		now := time.Now().UTC()
		r = domain.Run{ID: uuid.NewString(), Profile: profile, Name: i.Name, InstanceID: i.ID, Kind: "session", State: "cleanup_pending", Phase: "cleanup", CreatedAt: now, UpdatedAt: now, Deadline: now, Policy: policy, PolicyRevision: 1}
		newRun = true
	}
	cleanup := r.Policy.Cleanup
	if p.Has("cleanup") {
		cleanup = p.String("cleanup")
	}
	if cleanup == "" {
		cleanup = "release"
	}
	switch cleanup {
	case "release", "stop", "none", "delete":
	default:
		return nil, domain.E("INVALID_POLICY", "unknown cleanup mode")
	}
	if p.Bool("dry_run") {
		return map[string]any{"dry_run": true, "run_id": r.ID, "cleanup": cleanup}, nil
	}
	if newRun {
		if e = c.Store.PutRun(r); e != nil {
			return nil, e
		}
	}
	e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
		cur.CancelRequested = true
		cur.Phase = "cleanup"
		cur.State = "cleanup_pending"
		cur.CleanupState = "pending"
		cur.Policy.Cleanup = cleanup
		if cur.TerminalReason == "" || managedRunTerminal(r.State) {
			cur.TerminalReason = "down"
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	c.kickRun(r.ID)
	if p.Bool("no_wait") || (p.Has("wait") && !p.Bool("wait")) {
		return c.Store.GetRun(r.ID)
	}
	return c.waitManaged(ctx, r.ID, true)
}
func (c *Controller) CancelRun(ctx context.Context, profile string, p Params) (any, error) {
	r, e := c.GetRun(profile, p.String("target"))
	if e != nil {
		return nil, e
	}
	if managedRunTerminal(r.State) {
		return r, nil
	}
	if !p.Bool("dry_run") {
		if e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.TerminalReason = "cancelled"; return nil }); e != nil {
			return nil, e
		}
	}
	copy := Params{}
	for k, v := range p {
		copy[k] = v
	}
	copy.Set("target", r.ID)
	return c.Down(ctx, profile, copy)
}
func (c *Controller) UpdateRunPolicy(ctx context.Context, profile string, p Params) (any, error) {
	r, e := c.GetRun(profile, p.String("target"))
	if e != nil {
		return nil, e
	}
	if managedRunTerminal(r.State) {
		return r, domain.E("RUN_ENDED", "start a new managed cycle before changing a completed policy")
	}
	policy := r.Policy
	if e = p.Into("policy", &policy); e != nil {
		return r, e
	}
	var patch Params
	if e = p.Into("policy", &patch); e != nil {
		return r, e
	}
	var requestedUntil time.Time
	if patch.Has("until") {
		if e = patch.Into("until", &requestedUntil); e != nil {
			return r, e
		}
		policy.Until = requestedUntil
	}
	maxRequested := patch.Has("max_runtime") && patch.String("max_runtime") != ""
	untilRequested := !requestedUntil.IsZero()
	if p.Has("max_runtime") && p.String("max_runtime") != "" {
		maxRequested = true
	}
	if p.Has("until") && p.String("until") != "" {
		untilRequested = true
	}
	if maxRequested && untilRequested {
		return r, domain.E("INVALID_POLICY", "choose max_runtime or until, not both")
	}
	if maxRequested {
		policy.Until = time.Time{}
	}
	if untilRequested {
		policy.MaxRuntime = ""
	}
	if p.Has("max_runtime") {
		policy.MaxRuntime = p.String("max_runtime")
		policy.Until = time.Time{}
	}
	if p.Has("until") {
		until, e := time.Parse(time.RFC3339, p.String("until"))
		if e != nil {
			return r, domain.E("INVALID_POLICY", "until must be RFC3339")
		}
		policy.Until = until
		policy.MaxRuntime = ""
	}
	if p.Has("auto_renew") {
		policy.AutoRenew = p.Bool("auto_renew")
	}
	if p.Has("cleanup") {
		policy.Cleanup = p.String("cleanup")
	}
	switch policy.Cleanup {
	case "none", "stop", "release", "delete":
	default:
		return r, domain.E("INVALID_POLICY", "invalid cleanup policy")
	}
	grace, e := time.ParseDuration(policy.StopGrace)
	if e != nil || grace < 60*time.Second {
		return r, domain.E("INVALID_POLICY", "stop grace must be at least 60 seconds")
	}
	deadline, e := policy.Deadline(r.CreatedAt)
	if p.Bool("stop_now") && policy.MaxRuntime == "" && !policy.Until.IsZero() {
		deadline = policy.Until
		e = nil
	}
	if e != nil {
		return r, e
	}
	if !deadline.After(time.Now()) && !p.Bool("stop_now") && !p.Bool("dry_run") {
		return r, domain.E("STOP_CONFIRMATION_REQUIRED", "the new limit has already elapsed; use --stop-now to request immediate cleanup")
	}
	if p.Bool("dry_run") {
		r.Policy = policy
		r.Deadline = deadline
		r.PolicyRevision++
		return r, nil
	}
	e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
		if cur.PolicyRevision != r.PolicyRevision {
			return domain.E("POLICY_CONFLICT", "policy changed; refresh its revision")
		}
		if p.Has("revision") && int64(cur.PolicyRevision) != p.Int("revision") {
			return domain.E("POLICY_CONFLICT", "policy changed; refresh its revision")
		}
		cur.Policy = policy
		cur.Deadline = deadline
		cur.PolicyRevision++
		return nil
	})
	if e != nil {
		return r, e
	}
	c.Audit("run.policy", profile, string(r.InstanceID), "", r.ID, "bounded policy updated")
	c.kickRun(r.ID)
	return c.Store.GetRun(r.ID)
}
func (c *Controller) ObserveManualOperation(ctx context.Context, profile, op string, p Params) error {
	if p.Bool("dry_run") {
		return nil
	}
	if profile == "" {
		profile = c.Configuration().Selected
	}
	switch op {
	case "stop", "force-stop", "release", "delete":
	default:
		return nil
	}
	runs, e := c.Store.ListRuns(profile)
	if e != nil {
		return e
	}
	target := p.String("target")
	for _, r := range runs {
		if managedRunTerminal(r.State) || target != string(r.InstanceID) && target != r.Name {
			continue
		}
		if e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
			cur.CancelRequested = true
			cur.TerminalReason = "manual-" + op
			cur.Phase = "manual_wait"
			cur.State = "cleanup_pending"
			cur.CleanupState = "manual_operation"
			return nil
		}); e != nil {
			return e
		}
	}
	return nil
}

// runPhase reuses the recorded request even when a fresh snapshot has a newer
// index. Only Operation's explicit stale-index rejection path may replace it.
func (c *Controller) runPhase(ctx context.Context, r domain.Run, phase, op string, p Params) (domain.TaskRecord, bool, error) {
	key := r.ID + "/" + phase
	taskID := TaskID(r.Profile, op, key)
	t, e := c.Store.GetTask(taskID)
	if e == nil {
		if terminalTask(t.State) {
			return t, true, taskError(t)
		}
		c.resumeTask(t)
		return t, false, nil
	}
	if !state.IsNotFound(e) {
		return t, false, e
	}
	safe := platformRunParams(p)
	for _, k := range []string{"seconds", "reason"} {
		if p.Has(k) {
			safe[k] = p[k]
		}
	}
	safe.Set("idempotency_key", key)
	safe.Set("no_wait", true)
	safe.Set("wait", false)
	result, e := c.Operation(ctx, r.Profile, op, safe)
	if e != nil {
		return t, false, e
	}
	t, ok := result.(domain.TaskRecord)
	if !ok {
		return t, false, domain.E("TASK_PROTOCOL", "operation did not return a task record")
	}
	_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.LastTaskID = t.ID; return nil })
	return t, terminalTask(t.State), nil
}

func (c *Controller) advanceRun(ctx context.Context, id string) (bool, error) {
	r, e := c.Store.GetRun(id)
	if e != nil || managedRunTerminal(r.State) {
		return false, e
	}
	now := time.Now().UTC()
	if r.Phase != "manual_wait" && (r.CancelRequested || !r.Deadline.After(now) || r.ExitCode != nil) {
		reason := r.TerminalReason
		if reason == "" {
			if r.CancelRequested {
				reason = "cancelled"
			} else if r.ExitCode != nil {
				reason = "job_completed"
			} else {
				reason = "deadline"
			}
		}
		if r.Phase != "cleanup" {
			if e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
				cur.Phase = "cleanup"
				cur.State = "cleanup_pending"
				cur.CleanupState = "pending"
				cur.TerminalReason = reason
				return nil
			}); e != nil {
				return false, e
			}
			r, _ = c.Store.GetRun(r.ID)
		}
	}
	if r.Kind == "job" && r.InstanceID != "" && (r.Phase == "cleanup" || r.Phase == "active") {
		if r.Phase == "cleanup" && r.ExitCode == nil {
			if observed, err := c.invokeJob(ctx, r, "cancel"); err == nil && observed.ExitCode != nil && (observed.State == "complete" || observed.State == "failed") {
				_, _ = c.recordJobStatus(r, observed)
				r, _ = c.Store.GetRun(r.ID)
			}
		} else if r.Phase == "active" {
			progress, e := c.observeJob(ctx, r)
			if e != nil {
				_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.LastError = managedCode(e); return nil })
			}
			if progress {
				return true, nil
			}
		}
		if r.Phase == "cleanup" {
			archiveBudget := 30 * time.Second
			if remaining := time.Until(r.Deadline); remaining < archiveBudget {
				archiveBudget = max(remaining, time.Second)
			}
			archiveCtx, cancel := context.WithTimeout(ctx, archiveBudget)
			if e := c.archiveJobLogs(archiveCtx, r); e != nil {
				_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.LastError = managedCode(e); return nil })
			} else if strings.HasPrefix(r.LastError, "LOG_ARCHIVE_") {
				_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.LastError = ""; return nil })
			}
			cancel()
		}
	}
	s, e := c.snapshot(ctx, r.Profile)
	if e != nil {
		return false, e
	}
	_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.AuthState = "authenticated"; return nil })
	if r.InstanceID == "" {
		if r.Phase == "cleanup" {
			return c.cleanupUnresolvedCreate(ctx, r, s)
		}
		intent, e := c.loadRunIntent(r)
		if e != nil {
			return false, e
		}
		op := "create"
		if intent.Operation == "clone" {
			op = "clone"
		}
		t, done, e := c.runPhase(ctx, r, "create", op, intent.Platform)
		if e != nil {
			_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
				if managedRetryable(e) {
					cur.State = "create_pending"
				} else {
					cur.State = "failed"
					cur.Phase = "terminal"
				}
				cur.LastError = managedCode(e)
				return nil
			})
			return false, e
		}
		if !done {
			return false, nil
		}
		instanceID := associatedInstance(t, s)
		if instanceID == "" {
			return false, domain.E("CREATE_UNCERTAIN", "create completed but no task-associated instance is visible")
		}
		e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.InstanceID = instanceID; cur.Phase = "activate"; return nil })
		return true, e
	}
	instance, e := Resolve(s, string(r.InstanceID))
	if e != nil {
		if managedCode(e) == "NOT_FOUND" {
			if r.Phase == "cleanup" || r.Phase == "manual_wait" || r.Phase == "active" {
				return c.finishRun(ctx, r, "absent")
			}
			return false, domain.E("INSTANCE_NOT_VISIBLE", "waiting for the created instance to become visible")
		}
		return false, e
	}
	if e = Owned(s, instance); e != nil {
		return false, e
	}
	if r.Phase == "manual_wait" {
		done := false
		switch r.TerminalReason {
		case "manual-stop", "manual-force-stop":
			done = instance.State == "stopped" || instance.State == "approved"
		case "manual-release":
			done = !instance.Reserved && instance.State != "running" && instance.State != "starting"
		}
		if done {
			return c.finishRun(ctx, r, "manual_complete")
		}
		return false, nil
	}
	if r.Phase == "cleanup" {
		return c.cleanupRun(ctx, r, instance, s)
	}
	if r.Phase == "active" {
		if r.Kind != "allocation" && instance.State != "running" && instance.State != "starting" {
			_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.TerminalReason = "external_stop"; return nil })
			return c.finishRun(ctx, r, "reservation_retained")
		}
		if r.Kind == "job" && r.JobState == "" {
			return c.launchJob(ctx, r)
		}
		return false, c.renewRun(ctx, r, instance, s)
	}
	intent, e := c.loadRunIntent(r)
	if e != nil {
		return false, e
	}
	if r.Kind == "allocation" {
		if intent.Operation == "restore" && !instance.Reserved && instance.ReserveType != "no" {
			_, done, e := c.runPhase(ctx, r, "restore", "restore", Params{"target": domain.Result(string(instance.ID))})
			if e != nil {
				return false, c.activationFailed(r, e)
			}
			if !done {
				return false, nil
			}
			return false, nil
		}
		e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.State = "allocated"; cur.Phase = "active"; return nil })
		return true, e
	}
	if instance.State != "running" {
		if instance.State == "pending_approval" || strings.Contains(instance.State, "queuing") || instance.State == "starting" || instance.State == "stopping" {
			return false, nil
		}
		if !instance.Reserved && instance.ReserveType != "no" {
			_, _, e = c.runPhase(ctx, r, "restore", "restore", Params{"target": domain.Result(string(instance.ID))})
			if e != nil {
				return false, c.activationFailed(r, e)
			}
			_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.State = "restoring"; return nil })
			return false, nil
		}
		seconds := int64(time.Until(r.Deadline).Seconds())
		if seconds < 1 {
			return true, nil
		}
		if configured := intent.Platform.Int("duration"); configured > 0 && configured < seconds {
			seconds = configured
		}
		if instance.DurationSeconds > 0 && instance.DurationSeconds < seconds {
			seconds = instance.DurationSeconds
		}
		params := Params{"target": domain.Result(string(instance.ID)), "duration": domain.Result(seconds)}
		_, _, e = c.runPhase(ctx, r, "start", "start", params)
		if e != nil {
			return false, c.activationFailed(r, e)
		}
		_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.State = "starting"; return nil })
		return false, nil
	}
	if intent.PrepareSSH {
		repair := r.State == "starting" || r.State == "restoring"
		prepareCtx, prepareCancel := context.WithDeadline(ctx, r.Deadline)
		route, e := c.EnsureSSH(prepareCtx, r.Profile, string(r.InstanceID), intent.Node, repair)
		prepareCancel()
		if e != nil {
			return false, e
		}
		if e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.NodeID = route.NodeID; return nil }); e != nil {
			return false, e
		}
	}
	e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
		if cur.CancelRequested || !cur.Deadline.After(time.Now()) || cur.Phase == "cleanup" || cur.Phase == "manual_wait" {
			return domain.E("RUN_LIMIT_REACHED", "activation ended before readiness")
		}
		cur.State = "ready"
		cur.Phase = "active"
		cur.LastError = ""
		return nil
	})
	return true, e
}
func associatedInstance(t domain.TaskRecord, s domain.Snapshot) domain.ID {
	if t.Result != nil && t.Result.InstanceID != "" {
		return t.Result.InstanceID
	}
	ids := append([]string{t.ID}, t.AttemptIDs...)
	for _, i := range s.Instances {
		for _, task := range ids {
			if i.OperateTaskID == task {
				return i.ID
			}
		}
	}
	return ""
}
func (c *Controller) activationFailed(r domain.Run, e error) error {
	if managedRetryable(e) {
		return e
	}
	_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
		cur.Phase = "cleanup"
		cur.State = "cleanup_pending"
		cur.CleanupState = "pending"
		cur.TerminalReason = "failed"
		cur.LastError = managedCode(e)
		return nil
	})
	return e
}
func (c *Controller) cleanupUnresolvedCreate(ctx context.Context, r domain.Run, s domain.Snapshot) (bool, error) {
	intent, e := c.loadRunIntent(r)
	if e != nil {
		return false, e
	}
	op := "create"
	if intent.Operation == "clone" {
		op = "clone"
	}
	t, e := c.Store.GetTask(TaskID(r.Profile, op, r.ID+"/create"))
	if state.IsNotFound(e) {
		return c.finishRun(ctx, r, "no_allocation")
	}
	if e != nil {
		return false, e
	}
	if target := associatedInstance(t, s); target != "" {
		e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.InstanceID = target; return nil })
		return true, e
	}
	if terminalTask(t.State) && t.State != "complete" {
		return c.finishRun(ctx, r, "no_allocation")
	}
	c.resumeTask(t)
	return false, domain.E("CREATE_UNCERTAIN", "cleanup awaits the outstanding create result")
}

func (c *Controller) cleanupRun(ctx context.Context, r domain.Run, i domain.Instance, snapshot domain.Snapshot) (bool, error) {
	if r.Policy.Cleanup == "none" {
		return c.finishRun(ctx, r, "not_requested")
	}
	target := Params{"target": domain.Result(string(i.ID))}
	if strings.Contains(i.State, "queuing") {
		_, _, e := c.runPhase(ctx, r, "cancel-queue", "cancel-queue", target)
		return false, e
	}
	stopped := i.State == "stopped" || i.State == "approved" || i.State == "pending_approval"
	if !stopped {
		if r.StopRequestedAt.IsZero() {
			task, _, e := c.runPhase(ctx, r, "stop", "stop", target)
			if e != nil {
				return false, e
			}
			accepted := task.State == "complete" && task.Result != nil && task.Result.Code == 0
			ids := append([]string{task.ID}, task.AttemptIDs...)
			for _, observed := range snapshot.Tasks {
				for _, id := range ids {
					if observed.ID == id && (observed.State == "running" || observed.State == "complete") {
						accepted = true
					}
				}
			}
			if !accepted {
				return false, nil
			}
			now := time.Now().UTC()
			if e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
				if cur.StopRequestedAt.IsZero() {
					cur.StopRequestedAt = now
				}
				cur.CleanupState = "stopping"
				return nil
			}); e != nil {
				return false, e
			}
			r.StopRequestedAt = now
		}
		grace, _ := time.ParseDuration(r.Policy.StopGrace)
		if grace < 60*time.Second {
			grace = 60 * time.Second
		}
		if time.Since(r.StopRequestedAt) >= grace && r.Policy.ForceStop {
			_, _, e := c.runPhase(ctx, r, "force-stop", "force-stop", target)
			_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.CleanupState = "force_stopping"; return nil })
			return false, e
		}
		_, _, e := c.runPhase(ctx, r, "stop", "stop", target)
		return false, e
	}
	if r.Policy.Cleanup == "stop" {
		return c.finishRun(ctx, r, "stopped")
	}
	if i.Reserved {
		_, _, e := c.runPhase(ctx, r, "release", "release", target)
		_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.CleanupState = "releasing"; return nil })
		return false, e
	}
	if r.Policy.Cleanup == "delete" {
		_, _, e := c.runPhase(ctx, r, "delete", "delete", target)
		_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.CleanupState = "deleting"; return nil })
		return false, e
	}
	return c.finishRun(ctx, r, "released")
}
func (c *Controller) finishRun(ctx context.Context, r domain.Run, cleanup string) (bool, error) {
	e := c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
		cur.Phase = "terminal"
		cur.CleanupState = cleanup
		switch cur.TerminalReason {
		case "deadline":
			cur.State = "expired"
		case "cancelled":
			cur.State = "cancelled"
		case "failed":
			cur.State = "failed"
		case "job_completed":
			if cur.ExitCode != nil && *cur.ExitCode != 0 {
				cur.State = "failed"
			} else {
				cur.State = "completed"
			}
		default:
			cur.State = "stopped"
		}
		return nil
	})
	if e != nil {
		return false, e
	}
	if cleanup != "not_requested" && r.InstanceID != "" {
		short, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = c.CloseRoutes(short, r.Profile, r.InstanceID)
		cancel()
	}
	c.Audit("run.finished", r.Profile, string(r.InstanceID), "", r.ID, "managed lifecycle finished")
	return false, nil
}

func (c *Controller) launchJob(ctx context.Context, r domain.Run) (bool, error) {
	if e := c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.JobState = "launching"; return nil }); e != nil {
		return false, e
	}
	status, e := c.invokeJob(ctx, r, "launch")
	if e != nil {
		return false, e
	}
	return c.recordJobStatus(r, status)
}
func (c *Controller) observeJob(ctx context.Context, r domain.Run) (bool, error) {
	if r.JobState == "" {
		return false, nil
	}
	if r.JobState == "launching" {
		return c.launchJob(ctx, r)
	}
	var applied int
	_ = c.Store.Get("job_policy_applied", r.ID, &applied)
	if applied != r.PolicyRevision {
		if _, e := c.invokeJob(ctx, r, "policy"); e != nil {
			return false, e
		}
		_ = c.Store.Put("job_policy_applied", r.ID, r.PolicyRevision)
	}
	status, e := c.invokeJob(ctx, r, "status")
	if e != nil {
		return false, e
	}
	return c.recordJobStatus(r, status)
}
func (c *Controller) recordJobStatus(r domain.Run, status JobStatusInfo) (bool, error) {
	terminal := status.State == "complete" || status.State == "failed"
	e := c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
		cur.JobState = status.State
		cur.RemotePID = status.PID
		if status.StartedAt > 0 {
			cur.JobStartedAt = time.Unix(status.StartedAt, 0).UTC()
		}
		if terminal && status.ExitCode != nil {
			cur.ExitCode = status.ExitCode
			if cur.TerminalReason == "" {
				cur.TerminalReason = "job_completed"
			}
			cur.Phase = "cleanup"
			cur.State = "cleanup_pending"
			cur.CleanupState = "pending"
		} else if status.State == "running" {
			cur.State = "running"
		} else if status.State == "unknown" {
			cur.LastError = "JOB_LAUNCH_UNCERTAIN"
		}
		return nil
	})
	return terminal, e
}

func number(v any) float64 {
	if m, ok := v.(map[string]any); ok {
		return number(m["value"])
	}
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		n, _ := x.Float64()
		return n
	case string:
		var n json.Number
		if json.Unmarshal([]byte(x), &n) == nil {
			v, _ := n.Float64()
			return v
		}
	case int:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}
func renewalPlan(now time.Time, r domain.Run, i domain.Instance, cfg domain.PlatformConfig) (int64, string) {
	if !r.Policy.AutoRenew {
		return 0, "disabled"
	}
	if i.ExpiresAt.IsZero() {
		return 0, "unknown_expiry"
	}
	if !i.ExpiresAt.Before(r.Deadline) {
		return 0, "within_limit"
	}
	remaining := i.ExpiresAt.Sub(now).Seconds()
	if remaining <= 0 {
		return 0, "expired"
	}
	maxWindow := number(cfg.App["applyTimeNearExpStopAt"]) * 60
	minWindow := number(i.Meta["UpdateAppStopAtRequestDeadline"]) * 3600
	if minWindow < 0 || maxWindow < 0 || maxWindow > 0 && minWindow > maxWindow {
		return 0, "invalid_platform_window"
	}
	if remaining < minWindow {
		return 0, "request_deadline_missed"
	}
	if maxWindow > 0 && remaining > maxWindow {
		return 0, "waiting_window"
	}
	if _, ok := cfg.App["delayRequestLimit"]; !ok {
		return 0, "limits_unknown"
	}
	limit := number(cfg.App["delayRequestLimit"]) * 86400
	if limit <= 0 {
		return 0, "renewal_not_allowed"
	}
	seconds := math.Min(r.Deadline.Sub(i.ExpiresAt).Seconds(), limit)
	maxReserve := number(cfg.App["maxReserveTime"]) * 86400
	if maxReserve > 0 {
		reserved := number(i.Meta["reservedAtOrQueueAt"])
		if reserved > 1e12 {
			reserved /= 1000
		}
		if reserved <= 0 {
			return 0, "reservation_age_unknown"
		}
		seconds = math.Min(seconds, reserved+maxReserve-float64(i.ExpiresAt.Unix()))
	}
	if seconds < 1 {
		return 0, "platform_limit_reached"
	}
	return int64(math.Floor(seconds)), "eligible"
}
func (c *Controller) renewRun(ctx context.Context, r domain.Run, i domain.Instance, s domain.Snapshot) error {
	if !r.Policy.AutoRenew {
		return nil
	}
	for _, request := range s.Renewals {
		if request.TargetID != i.ID || request.Type != "UpdateExpStopAt" {
			continue
		}
		if request.State == "pending" {
			_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
				cur.RenewalState = "pending_approval"
				cur.RenewalRequestID = request.ID
				return nil
			})
			return nil
		}
	}
	if r.RenewalTaskID != "" {
		var matching *domain.Renewal
		for x := range s.Renewals {
			candidate := &s.Renewals[x]
			if candidate.TargetID == i.ID && candidate.Type == "UpdateExpStopAt" && (candidate.ID == r.RenewalRequestID || candidate.CreatedAt.Unix() >= r.LastRenewalAttempt.Unix()-1 && candidate.RequestedSeconds == r.RenewalRequestedSeconds) {
				matching = candidate
			}
		}
		stateName := "awaiting_approval"
		if matching != nil {
			stateName = matching.State
		}
		if stateName == "approved" && i.ExpiresAt.After(r.RenewalBaseExpiry) {
			_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
				cur.RenewalState = "verified"
				cur.RenewalTaskID = ""
				cur.RenewalRequestID = ""
				return nil
			})
			return nil
		}
		if stateName == "denied" || stateName == "expired" {
			_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.RenewalState = stateName; return nil })
			return nil
		}
		t, e := c.Store.GetTask(r.RenewalTaskID)
		if state.IsNotFound(e) {
			latest, loadErr := c.Store.GetRun(r.ID)
			if loadErr != nil {
				return loadErr
			}
			seconds := min(r.RenewalRequestedSeconds, int64(latest.Deadline.Sub(i.ExpiresAt).Seconds()))
			if !latest.Policy.AutoRenew || seconds <= 0 || !latest.Deadline.After(time.Now()) {
				return c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
					cur.RenewalTaskID = ""
					cur.RenewalState = "bounded_without_submission"
					return nil
				})
			}
			if r.RenewalBaseExpiry.IsZero() {
				return domain.E("RENEWAL_STATE_INVALID", "renewal recovery has no original expiry")
			}
			phase := fmt.Sprintf("renew/%d", r.RenewalBaseExpiry.Unix())
			if TaskID(r.Profile, "renew", r.ID+"/"+phase) != r.RenewalTaskID {
				return domain.E("RENEWAL_STATE_INVALID", "renewal task identity does not match its phase")
			}
			_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.RenewalRequestedSeconds = seconds; return nil })
			params := Params{"target": domain.Result(string(i.ID)), "seconds": domain.Result(seconds), "reason": domain.Result("bounded managed run " + r.ID)}
			_, _, submitErr := c.runPhase(ctx, latest, phase, "renew", params)
			return submitErr
		}
		if e != nil {
			return e
		}
		if e == nil && !terminalTask(t.State) {
			c.resumeTask(t)
		}
		if e == nil && terminalTask(t.State) && t.State != "complete" {
			stateName = "rejected"
		}
		_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.RenewalState = stateName; return nil })
		return nil
	}
	api, e := c.api(r.Profile)
	if e != nil {
		return e
	}
	cfg, e := api.Config(ctx)
	if e != nil {
		return e
	}
	seconds, status := renewalPlan(time.Now(), r, i, cfg)
	_ = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.RenewalState = status; return nil })
	if seconds <= 0 {
		return nil
	}
	phase := fmt.Sprintf("renew/%d", i.ExpiresAt.Unix())
	taskID := TaskID(r.Profile, "renew", r.ID+"/"+phase)
	// Persist the intent marker before submission, so a controller crash cannot
	// create another pending request for the same expiry.
	if e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error {
		if cur.PolicyRevision != r.PolicyRevision || cur.CancelRequested || cur.Phase != "active" {
			return domain.E("POLICY_CHANGED", "renewal was deferred because the managed policy changed")
		}
		cur.RenewalTaskID = taskID
		cur.RenewalBaseExpiry = i.ExpiresAt
		cur.RenewalRequestedSeconds = seconds
		cur.LastRenewalAttempt = time.Now().UTC()
		cur.RenewalState = "submitting"
		return nil
	}); e != nil {
		return e
	}
	params := Params{"target": domain.Result(string(i.ID)), "seconds": domain.Result(seconds), "reason": domain.Result("bounded managed run " + r.ID)}
	_, _, e = c.runPhase(ctx, r, phase, "renew", params)
	return e
}
