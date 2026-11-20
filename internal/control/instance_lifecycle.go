package control

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"regexp"
	"slices"
	"strings"
	"time"

	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
)

var pointRate = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?([eE][+-]?[0-9]{1,2})?$`)

func dailyPoints(v any) (*big.Rat, string) {
	raw := fmt.Sprint(v)
	if len(raw) > 48 || !pointRate.MatchString(raw) {
		return nil, ""
	}
	n, ok := new(big.Rat).SetString(raw)
	if !ok || n.Sign() < 0 {
		return nil, ""
	}
	return n, raw
}

func createRequestHash(task domain.TaskRequest) string {
	sum := sha256.Sum256(domain.Result(struct {
		Owner   domain.ID
		Payload map[string]any
	}{task.UserID, task.Payload}))
	return hex.EncodeToString(sum[:])
}

func (c *Controller) priceCreate(ctx context.Context, profile string, p Params, task domain.TaskRequest, preview map[string]any) error {
	var cap *big.Rat
	if preview == nil {
		cap, _ = dailyPoints(p.String("max_points_per_day"))
		if cap == nil {
			return domain.E("COST_AUTH_REQUIRED", "取得新预留需要 --max-points-per-day RATE 授权学校日价")
		}
	}
	api, err := c.api(profile)
	if err != nil {
		return err
	}
	constraints, _ := task.Payload["resourceConstraints"].(map[string]any)
	spec := domain.ID(fmt.Sprint(task.Payload["appSpecEid"]))
	estimate, err := api.Estimate(ctx, spec, constraints)
	price, raw := dailyPoints(estimate["points_per_day"])
	if err != nil || price == nil {
		return domain.E("COST_ESTIMATE_UNAVAILABLE", "学校尚未返回可核对的积分日价，请重新估价")
	}
	if preview != nil {
		preview["points_per_day"] = raw
		preview["spec_id"] = spec
		preview["allocate_only"] = p.Bool("allocate_only")
		preview["billing_start"] = "学校未公布确切起点。预留建立或保留存储时即可产生费用，以学校账单为准。"
	} else if price.Cmp(cap) > 0 {
		return domain.E("COST_LIMIT_EXCEEDED", "学校最新日价 "+raw+" 积分超过本次授权，请重新预览")
	}
	return nil
}

func (c *Controller) priceRestore(ctx context.Context, profile string, p Params, i domain.Instance, preview map[string]any) (string, error) {
	api, err := c.api(profile)
	if err != nil {
		return "", err
	}
	spec, err := api.Spec(ctx, i.SpecID)
	if err != nil || spec.ID == "" || spec.ID != i.SpecID {
		return "", domain.E("COST_ESTIMATE_UNAVAILABLE", "无法核对原实例的模板")
	}
	groups, err := decodeResourceGroups(spec)
	if err != nil {
		return "", err
	}
	constraints := map[string]any{}
	if len(groups) > 0 {
		for _, node := range i.Nodes {
			gid, group, err := groupForNode(groups, node)
			if err != nil {
				return "", err
			}
			values, err := currentResources(group, node)
			if err != nil {
				return "", err
			}
			resolved := serializeResources(group, values)
			if old, exists := constraints[gid]; exists && string(domain.Result(old)) != string(domain.Result(resolved)) {
				return "", domain.E("COST_ESTIMATE_UNAVAILABLE", "同组节点资源不一致，无法核对算力日价")
			}
			constraints[gid] = resolved
		}
		if len(constraints) != len(groups) {
			return "", domain.E("COST_ESTIMATE_UNAVAILABLE", "无法核对原实例全部节点规格")
		}
	}
	target := domain.TaskRequest{UserID: i.UserID, Payload: map[string]any{
		"appSpecEid": i.SpecID.Number(), "resourceConstraints": constraints, "appEid": i.ID.Number(), "gid": i.GroupID.Number(),
	}}
	hash := createRequestHash(target)
	if expected := p.String("restore_quote_hash"); expected != "" && hash != expected {
		return "", domain.E("COST_SELECTION_CHANGED", "实例的算力配置已变化，请重新核对日价")
	}
	err = c.priceCreate(ctx, profile, p, target, preview)
	if err == nil && preview != nil {
		preview["billing_start"] = "分配算力或保留磁盘都可能产生费用。计费起点和金额以学校账单为准。"
	}
	return hash, err
}

func (c *Controller) inspectInstance(ctx context.Context, profile string, p Params) (any, error) {
	s, err := c.snapshot(ctx, profile)
	if err != nil {
		return nil, err
	}
	i, err := Resolve(s, p.String("target"))
	if err != nil && managedCode(err) == "NOT_FOUND" {
		run, found := c.GetRun(profile, p.String("target"))
		if found == nil && run.InstanceID == "" {
			return run, nil
		}
	}
	if err == nil {
		routes, _ := c.Store.ListRoutes(profile)
		i.SSHState = instanceSSHState(i, routes)
	}
	return i, err
}

func (c *Controller) instanceEvents(ctx context.Context, profile string, p Params) (any, error) {
	var s domain.Snapshot
	var readErr error
	source := "platform_snapshot"
	if !p.Bool("cached") {
		s, readErr = c.snapshot(ctx, profile)
	}
	if p.Bool("cached") || readErr != nil {
		source = "cached_platform_snapshot"
		if err := c.Store.Get("snapshots", profile, &s); err != nil {
			if readErr != nil {
				return nil, readErr
			}
			return nil, domain.E("NO_CACHE", "还没有可用的学校实例快照")
		}
	}
	i, err := Resolve(s, p.String("target"))
	if err != nil {
		return nil, err
	}
	n := limit(p, 100)
	local, err := c.Store.ListInstanceAudits(profile, string(i.ID), n)
	if err != nil {
		return nil, err
	}
	tasks, err := c.Store.ListTasks(profile)
	if err != nil {
		return nil, err
	}
	related := map[string]bool{}
	for _, t := range tasks {
		if t.Request.TargetID == i.ID || t.Result != nil && t.Result.InstanceID == i.ID {
			related[t.ID] = true
			for _, id := range t.AttemptIDs {
				related[id] = true
			}
		}
	}
	receipts := []domain.TaskResult{}
	for _, t := range s.Tasks {
		if t.State != "" && (t.InstanceID == i.ID || t.TargetID == i.ID || t.ID == i.OperateTaskID || related[t.ID]) {
			receipts = append(receipts, domain.TaskResult{
				ID: t.ID, State: t.State, Code: t.Code, InstanceID: t.InstanceID,
				TargetID: t.TargetID, Kind: t.Kind, EventIndex: t.EventIndex,
			})
		}
	}
	slices.SortFunc(receipts, func(a, b domain.TaskResult) int {
		return cmp.Or(cmp.Compare(b.EventIndex, a.EventIndex), cmp.Compare(a.ID, b.ID))
	})
	school := map[string]any{
		"source": source, "history_complete": false, "snapshot_index": s.Index,
		"receipts": receipts[:min(n, len(receipts))], "limited": len(receipts) > n,
	}
	if readErr != nil {
		school["live_error"] = managedCode(readErr)
	}
	return map[string]any{
		"instance_id": i.ID,
		"school":      school,
		"controller":  map[string]any{"source": "controller_audit", "events": local, "limit": n},
	}, nil
}

func (c *Controller) waitOperation(ctx context.Context, profile, id string) (any, error) {
	run, err := c.Store.GetRun(id)
	if err == nil && run.Profile == profile {
		c.kickRun(id)
		return c.waitManaged(ctx, id, false)
	}
	if r, err := c.restartReceipt(profile, id); err == nil {
		if r.RestartID == id && strings.HasPrefix(r.Phase, "restart_") {
			c.kickRun(r.ID)
			return c.waitManaged(ctx, r.ID, false)
		}
		return r, nil
	}
	task, err := c.Store.GetTask(id)
	if err != nil || task.Profile != profile {
		return nil, domain.E("NOT_FOUND", "此配置没有该操作回执")
	}
	if task.Verified {
		return task, taskError(task)
	}
	if !terminalTask(task.State) {
		c.resumeTask(task)
	}
	task, err = c.WaitTask(ctx, id)
	if err != nil {
		return task, err
	}
	switch task.Operation {
	case "stop", "force-stop", "release", "delete", "cancel-queue":
	default:
		return task, nil
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		s, err := c.syncSnapshot(ctx, profile, true)
		if err != nil {
			if ctx.Err() != nil {
				return task, &domain.Error{Code: "TASK_PENDING", Message: "本次操作状态待核对", TaskID: id, Retryable: true}
			}
			return task, err
		}
		i, err := Resolve(s, string(task.Request.TargetID))
		if err != nil && managedCode(err) != "NOT_FOUND" {
			return task, err
		}
		if instanceTaskObserved(task.Operation, i) {
			c.intentMu.Lock()
			defer c.intentMu.Unlock()
			return c.confirmManualTask(ctx, task)
		}
		select {
		case <-ctx.Done():
			return task, &domain.Error{Code: "TASK_PENDING", Message: "平台尚未确认实例的最终状态", TaskID: id, Retryable: true}
		case <-timer.C:
		}
	}
}

func (c *Controller) restartReceipt(profile, id string) (domain.Run, error) {
	var r domain.Run
	if err := c.Store.Get("restart_receipts", id, &r); err == nil && r.Profile == profile {
		return r, nil
	}
	for _, r := range mustRuns(c, profile) {
		if r.RestartID == id {
			return r, nil
		}
	}
	return r, domain.E("NOT_FOUND", "找不到重启回执")
}

func (c *Controller) waitInstance(ctx context.Context, profile string, p Params) (any, error) {
	if id := p.String("operation"); id != "" {
		return c.waitOperation(ctx, profile, id)
	}
	want := p.String("state")
	if want == "" {
		want = "ssh-ready"
	}
	target := p.String("target")
	node := p.String("node")
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		r, runErr := c.GetRun(profile, target)
		if runErr == nil && managedRunTerminal(r.State) && r.InstanceID == "" {
			return nil, &domain.Error{Code: "RUN_ENDED", Message: "申请在分配实例之前结束", RunID: r.ID, TaskID: r.LastTaskID}
		}
		ref := target
		if runErr == nil && r.InstanceID != "" {
			ref = string(r.InstanceID)
		}
		s, err := c.snapshot(ctx, profile)
		if err != nil {
			return nil, err
		}
		i, err := Resolve(s, ref)
		if err != nil && (runErr != nil || managedCode(err) != "NOT_FOUND") {
			return nil, err
		}
		if err == nil && want != "ssh-ready" && i.State == want {
			return i, nil
		}
		if err == nil && want == "ssh-ready" {
			if i.State == "running" {
				selected, err := SelectNode(i, node)
				if err != nil {
					return nil, err
				}
				route, err := c.CachedRoute(profile, string(i.ID), string(selected.ID))
				if err == nil && route.State == "ready" {
					probe, cancel := context.WithTimeout(ctx, 3*time.Second)
					ssh, err := connect.DialSSH(probe, route)
					cancel()
					if err == nil {
						ssh.Close()
						i.SSHState = "ready"
						return i, nil
					}
					if managedCode(err) == "SSH_HOST_KEY_MISMATCH" {
						return nil, err
					}
				}
				if runErr != nil || managedRunTerminal(r.State) {
					return nil, domain.E("SSH_NOT_READY", "请先准备并验证此实例的 SSH 路由")
				}
			}
			if (i.State == "stopped" || i.State == "approved" || i.State == "released") && (runErr != nil || r.State == "allocated" || (managedRunTerminal(r.State) && r.State != "failed")) {
				return nil, domain.E("INSTANCE_NOT_STARTED", "请先运行 bupt compute instance start INSTANCE")
			}
		}
		if runErr == nil {
			if managedRunTerminal(r.State) || r.Phase == "restart_failed" {
				return nil, &domain.Error{Code: "RUN_ENDED", Message: "操作在 SSH 就绪之前结束", RunID: r.ID, TaskID: r.LastTaskID}
			}
			if r.LastError != "" && (r.AuthState == "action_required" || strings.HasPrefix(r.LastError, "SSH_") || strings.HasPrefix(r.LastError, "CONSOLE_") || r.LastError == "RELAY_REQUIRED") {
				return nil, &domain.Error{Code: r.LastError, Message: "实例操作仍在跟踪，需要处理后续步骤", RunID: r.ID, TaskID: r.LastTaskID}
			}
			c.kickRun(r.ID)
		}
		select {
		case <-ctx.Done():
			if runErr == nil {
				return nil, &domain.Error{Code: "RUN_PENDING", Message: "等待结束，控制器保留操作回执", RunID: r.ID, TaskID: r.LastTaskID, Retryable: true}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Controller) job(profile, target string) (domain.Run, error) {
	r, err := c.GetRun(profile, target)
	if err == nil && r.Kind != "job" {
		return domain.Run{}, domain.E("NOT_FOUND", "此目标不是托管作业")
	}
	return r, err
}

// Manual operations validate ownership and the platform preconditions before
// changing an active run's cleanup state.
func (c *Controller) manualInstance(ctx context.Context, profile, op string, p Params) (any, error) {
	if p.Bool("dry_run") {
		return c.Operation(ctx, profile, op, p)
	}
	t, err := c.prepareManualTask(ctx, profile, op, p)
	if err != nil {
		return nil, err
	}
	if terminalTask(t.State) {
		if taskError(t) == nil && !t.Verified && p.Bool("wait") {
			return c.waitOperation(ctx, profile, t.ID)
		}
		return t, taskError(t)
	}
	c.resumeTask(t)
	if p.Bool("wait") {
		return c.waitOperation(ctx, profile, t.ID)
	}
	return t, nil
}

func (c *Controller) advanceRestart(ctx context.Context, r domain.Run, i domain.Instance, intent runIntent) (bool, error) {
	target := Params{"target": domain.Result(i.ID)}
	phase := "restart/" + r.RestartID
	if r.Phase == "restart_stop" {
		_, done, err := c.runPhase(ctx, r, phase+"/stop", "stop", target)
		if err != nil {
			return false, err
		}
		if !done || (i.State != "stopped" && i.State != "approved") {
			return false, nil
		}
		return true, c.Store.UpdateRun(r.ID, func(v *domain.Run) error {
			if v.Phase == "restart_stop" {
				v.Phase, v.State = "restart_start", "starting"
			}
			return nil
		})
	}
	if i.State != "running" {
		if i.State == "starting" || strings.Contains(i.State, "queuing") {
			return false, nil
		}
		if !i.Reserved && i.ReserveType != "no" {
			return false, restoreRequired()
		}
		seconds := int64(time.Until(r.Deadline).Seconds())
		if seconds <= 0 {
			return false, nil
		}
		if i.DurationSeconds > 0 {
			seconds = min(seconds, i.DurationSeconds)
		}
		target.Set("duration", seconds)
		_, _, err := c.runPhase(ctx, r, phase+"/start", "start", target)
		return false, err
	}
	if _, err := c.Store.GetTask(TaskID(r.Profile, "start", r.ID+"/"+phase+"/start")); state.IsNotFound(err) {
		return false, domain.E("RESTART_CHANGED", "实例状态已被外部操作改变")
	} else if err != nil {
		return false, err
	}
	route, err := c.EnsureSSH(ctx, r.Profile, string(i.ID), intent.Node, true)
	if err != nil {
		return false, err
	}
	return true, c.Store.UpdateRun(r.ID, func(v *domain.Run) error {
		if v.Phase == "restart_start" && v.Deadline.After(time.Now()) {
			v.State, v.Phase, v.NodeID, v.LastError = "ready", "active", route.NodeID, ""
		}
		return nil
	})
}

func startable(i domain.Instance) error {
	if !instanceStopped(i) || i.State == "released" {
		return domain.E("INVALID_STATE", "启动要求已有实例处于停机状态")
	}
	if !instanceReserved(i) {
		return restoreRequired()
	}
	return nil
}

func restoreRequired() error {
	return &domain.Error{
		Code:    "RESTORE_REQUIRED",
		Message: "实例需要先分配算力才能启动",
		Hint:    "用 bupt compute instance restore 为该实例重新分配算力",
	}
}

func (c *Controller) checkStart(ctx context.Context, profile, target string) error {
	s, err := c.snapshot(ctx, profile)
	if err != nil {
		return err
	}
	i, err := Resolve(s, target)
	if err != nil {
		return err
	}
	if err = Owned(s, i); err != nil {
		return err
	}
	return startable(i)
}

func (c *Controller) restartExistingRun(ctx context.Context, r domain.Run, p Params, fingerprint string, preview map[string]any) (domain.Run, error) {
	if r.Kind == "job" || r.Phase != "active" || !r.Deadline.After(time.Now()) {
		return r, domain.E("INSTANCE_IN_USE", "请先处理此实例正在进行的操作或托管作业")
	}
	s, err := c.snapshot(ctx, r.Profile)
	if err != nil {
		return r, err
	}
	i, err := Resolve(s, string(r.InstanceID))
	if err != nil {
		return r, err
	}
	if err = Owned(s, i); err != nil {
		return r, err
	}
	if i.State != "running" {
		return r, domain.E("INVALID_STATE", "重启要求实例处于运行状态")
	}
	if preview != nil {
		return r, nil
	}
	if r.RestartID != "" {
		if err := c.Store.Put("restart_receipts", r.RestartID, r); err != nil {
			return r, err
		}
	}
	id := TaskID(r.Profile, "restart", p.String("idempotency_key"))
	err = c.Store.UpdateRun(r.ID, func(v *domain.Run) error {
		if v.Phase != "active" || v.Kind == "job" || !v.Deadline.After(time.Now()) {
			return domain.E("INSTANCE_IN_USE", "实例当前有操作进行中")
		}
		v.RestartHash = fingerprint
		v.RestartID, v.Phase, v.State, v.LastError = id, "restart_stop", "stopping", ""
		r = *v
		return nil
	})
	return r, err
}
