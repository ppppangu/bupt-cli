package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
	"github.com/google/uuid"
)

func (c *Controller) buildTask(ctx context.Context, profile, op string, p Params) (domain.TaskRequest, error) {
	s, e := c.snapshot(ctx, profile)
	if e != nil {
		return domain.TaskRequest{}, e
	}
	return c.buildTaskFromSnapshot(ctx, profile, op, p, s)
}

func (c *Controller) buildTaskFromSnapshot(ctx context.Context, profile, op string, p Params, s domain.Snapshot) (domain.TaskRequest, error) {
	var e error
	if op == "quota-request" {
		return quotaRequest(s, p)
	}
	t := domain.TaskRequest{UserID: s.Identity.UID, Payload: map[string]any{}}
	if op == "approve-request" || op == "reject-request" {
		a, err := c.api(profile)
		if err != nil {
			return t, err
		}
		checker, ok := a.(interface {
			VerifyComputeAdmin(context.Context, domain.Identity) error
		})
		if !ok {
			return t, domain.E("ADMIN_SCOPE_REQUIRED", "需要学校管理员授权。")
		}
		if err = checker.VerifyComputeAdmin(ctx, s.Identity); err != nil {
			return t, err
		}
		for _, r := range s.Renewals {
			if string(r.ID) != p.String("target") {
				continue
			}
			if r.State != "pending" && r.State != "created" {
				return t, domain.E("REQUEST_CLOSED", "申请已处理。")
			}
			t.Type = "ApproveUserOpRequestTask"
			t.TargetID = r.ID
			t.Payload = map[string]any{"OpRequestEid": r.ID.Number(), "type": r.Type}
			if op == "reject-request" {
				if p.String("reason") == "" {
					return t, domain.E("REASON_REQUIRED", "请填写拒绝理由。")
				}
				t.Type = "DenyUserOpRequestTask"
				t.Payload["reason"] = p.String("reason")
			}
			return t, nil
		}
		return t, domain.E("NOT_FOUND", "学校未返回此申请。")
	}
	if op == "create" || op == "clone" {
		preset := domain.Preset{}
		if p.String("preset") != "" {
			preset, e = c.preset(p, profile)
			if e != nil {
				return t, e
			}
		}
		var source *domain.Instance
		if op == "clone" {
			src, err := Resolve(s, p.String("target"))
			if err != nil {
				return t, err
			}
			if err = Owned(s, src); err != nil {
				return t, err
			}
			if src.OwnerID != s.Identity.UID && src.UserID != s.Identity.UID {
				return t, domain.E("OWNERSHIP_UNVERIFIED", "the native clone UI is restricted to the instance owner")
			}
			if src.State != "stopped" && src.State != "approved" {
				return t, domain.E("STOP_REQUIRED", "stop the source instance before cloning")
			}
			if len(src.Nodes) != 1 {
				return t, domain.E("CLONE_LAYOUT_UNSUPPORTED", "cloning currently requires one source node with a known allocation")
			}
			if src.Nodes[0].Type != "qemu" {
				return t, domain.E("CLONE_LAYOUT_UNSUPPORTED", "the native clone capability is exposed only for single-node QEMU instances")
			}
			source = &src
		}
		specID := p.ID("spec_id")
		if specID == "" {
			specID = preset.SpecID
		}
		if specID == "" && source != nil {
			specID = source.SpecID
		}
		if specID == "" {
			return t, domain.E("SPEC_REQUIRED", "select a template using --spec or a preset")
		}
		if err := validateNumericResourceID(specID, "specification", false); err != nil {
			return t, err
		}
		if source != nil && specID != source.SpecID {
			return t, domain.E("CLONE_TEMPLATE_MISMATCH", "clone must use the source instance template")
		}
		name := p.String("name")
		if strings.TrimSpace(name) == "" || len([]rune(name)) > 128 {
			return t, domain.E("INVALID_NAME", "name must contain 1–128 nonblank characters")
		}
		for _, existing := range s.Instances {
			if existing.Name == name {
				return t, domain.E("NAME_CONFLICT", "an instance already has this name; inspect or adopt it")
			}
		}
		api, err := c.api(profile)
		if err != nil {
			return t, err
		}
		spec, err := api.Spec(ctx, specID)
		if err != nil {
			return t, err
		}
		if spec.ID == "" || spec.ID != specID {
			return t, domain.E("SPEC_UNAVAILABLE", "template response does not match the selected specification")
		}
		exact := preset.Constraints
		if p.Has("constraints") {
			exact, err = requestedResourceMap(p, "constraints")
			if err != nil {
				return t, err
			}
		}
		var current *domain.Node
		if source != nil {
			current = &source.Nodes[0]
		}
		resources, err := requestedResourceMap(p, "resources")
		if err != nil {
			return t, err
		}
		constraints, err := resourcesForSpec(spec, exact, resources, current)
		if err != nil {
			return t, err
		}
		gid := preset.GroupID
		if gid == "" && source != nil {
			gid = source.GroupID
		}
		if p.Has("group_id") {
			gid = p.ID("group_id")
		}
		if gid == s.Identity.UID {
			gid = "0"
		}
		if err = validateNumericResourceID(gid, "group", true); err != nil {
			return t, err
		}
		if strings.EqualFold(scalarResource(spec.Options["scope"]), "group") && (gid == "" || gid == "0") {
			return t, domain.E("PROJECT_REQUIRED", "this template requires an explicit project group")
		}
		bill := string(preset.BillID)
		if p.Has("bill_id") {
			bill = p.String("bill_id")
			if bill == "" && p.ID("bill_id") != "" {
				bill = string(p.ID("bill_id"))
			}
		}
		email := p.String("email")
		if email == "" && source != nil {
			email = instanceStringMeta(*source, "emailAddr")
		}
		if email == "" {
			email = s.Identity.Email
		}
		if email == "" {
			return t, domain.E("EMAIL_REQUIRED", "platform requires the account email or --email")
		}
		usage := p.String("usage")
		if usage == "" {
			usage = preset.Usage
		}
		if usage == "" && source != nil {
			usage = instanceStringMeta(*source, "Usage")
			if usage == "" {
				usage = instanceStringMeta(*source, "usage")
			}
		}
		if len([]rune(usage)) > 256 {
			return t, domain.E("INVALID_USAGE", "usage description must not exceed 256 characters")
		}
		dom := p.String("domain")
		if dom == "" {
			dom = preset.Domain
		}
		if dom == "" && source != nil {
			dom = source.Domain
		}
		if dom == "" {
			dom = scalarResource(spec.Options["domain"])
		}
		t.Type = "CreateAppTask"
		t.Payload = map[string]any{"appSpecEid": specID.Number(), "appName": name, "gid": gid.Number(), "emailAddr": email, "billId": bill, "usage": usage, "resourceConstraints": constraints, "domain": dom}
		if raw, ok := spec.Options["resourceConstraints"]; !ok || raw == nil || raw == "" {
			delete(t.Payload, "resourceConstraints")
		}
		if source != nil {
			t.Payload["cloneAppEid"] = source.ID.Number()
		}
		if expected := p.String("expected_create_hash"); expected != "" {
			if createRequestHash(t) != expected {
				return t, domain.E("COST_SELECTION_CHANGED", "学校规格或账单归属已变化，请重新核对费用")
			}
			if p.Has("max_points_per_day") {
				if err := c.priceCreate(ctx, profile, p, t, nil); err != nil {
					return t, err
				}
			}
		}
		return t, nil
	}
	i, e := Resolve(s, p.String("target"))
	if e != nil {
		return t, e
	}
	if e = Owned(s, i); e != nil {
		return t, e
	}
	t.TargetID = i.ID
	base := map[string]any{"appEid": i.ID.Number(), "gid": i.GroupID.Number(), "eaasIndex": i.Index.Number()}
	switch op {
	case "start":
		if i.State == "running" {
			return t, domain.E("ALREADY_RUNNING", "instance is already running")
		}
		seconds := p.Int("duration")
		if seconds == 0 {
			seconds = i.DurationSeconds
		}
		if seconds <= 0 {
			return t, domain.E("DURATION_REQUIRED", "platform runtime duration is unknown")
		}
		t.Type = "StartAppTask"
		t.Payload = map[string]any{"appEid": i.ID.Number(), "gid": i.GroupID.Number(), "ownerId": i.OwnerID.Number(), "domain": i.Domain, "expDuration": seconds}
	case "stop":
		t.Type = "StopAppTask"
		base["forceStop"] = false
		t.Payload = base
	case "force-stop":
		t.Type = "ForceStopAppTask"
		t.Payload = map[string]any{"appEid": i.ID.Number()}
	case "release":
		if i.State != "stopped" && i.State != "approved" {
			return t, domain.E("INSTANCE_RUNNING", "请先运行 compute instance stop")
		}
		t.Type = "AppUnreserveTask"
		t.Payload = base
	case "restore":
		if p.String("restore_quote_hash") != "" {
			if _, err := c.priceRestore(ctx, profile, p, i, nil); err != nil {
				return t, err
			}
		}
		t.Type = "AppReserveTask"
		t.Payload = map[string]any{"appEid": i.ID.Number(), "gid": i.GroupID.Number(), "appUid": i.UserID.Number(), "domain": i.Domain}
	case "delete":
		if p.Bool("confirm_delete") && !p.Bool("dry_run") && p.String("confirm") != string(i.ID) {
			return t, domain.E("CONFIRM_REQUIRED", "请用 --confirm INSTANCE_ID 确认目标")
		}
		if (i.State != "stopped" && i.State != "approved" && i.State != "released") || i.Reserved {
			return t, domain.E("RELEASE_REQUIRED", "stop and release before permanent deletion")
		}
		t.Type = "RemoveAppTask"
		t.Payload = map[string]any{"appEid": i.ID.Number(), "gid": i.GroupID.Number(), "appUid": i.UserID.Number(), "domain": i.Domain}
	case "cancel-queue":
		if !strings.Contains(i.State, "queuing") {
			return t, domain.E("INVALID_STATE", "仅可取消排队中的实例")
		}
		if i.State == "reserve_queuing" {
			t.Type = "CancelAppReserveQueuingTask"
		} else {
			t.Type = "CancelAppQueuingTask"
		}
		t.Payload = base
	case "renew":
		if p.Int("seconds") <= 0 {
			return t, domain.E("INVALID_DURATION", "renewal seconds must be positive")
		}
		for _, r := range s.Renewals {
			if r.TargetID == i.ID && r.Type == "UpdateExpStopAt" && r.State == "pending" {
				return t, domain.E("RENEWAL_PENDING", "an equivalent renewal request is awaiting approval")
			}
		}
		reason := p.String("reason")
		if reason == "" {
			reason = "bupt managed runtime renewal"
		}
		data, _ := json.Marshal(map[string]any{"delaySeconds": p.Int("seconds")})
		t.Type = "CreateUserOpRequestTask"
		t.Payload = map[string]any{"tgtEid": i.ID.Number(), "type": "UpdateExpStopAt", "reason": reason, "data": string(data)}
	case "resize", "disk-grow", "snapshot-create", "snapshot-restore", "snapshot-delete", "service-add", "service-delete":
		n, e := SelectNode(i, p.String("node"))
		if e != nil {
			return t, e
		}
		switch op {
		case "resize":
			allowed := map[string]bool{"pending_approval": true, "stopped": true, "approved": true, "queuing": true, "reserve_queuing": true}
			if !allowed[i.State] || (i.ReserveType != "no" && i.Reserved) {
				return t, domain.E("RELEASE_REQUIRED", "resource changes require a permitted stopped/released state")
			}
			api, err := c.api(profile)
			if err != nil {
				return t, err
			}
			spec, err := api.Spec(ctx, i.SpecID)
			if err != nil {
				return t, err
			}
			if spec.ID == "" || spec.ID != i.SpecID {
				return t, domain.E("SPEC_UNAVAILABLE", "instance template cannot be verified")
			}
			resources, err := requestedResourceMap(p, "resources")
			if err != nil {
				return t, err
			}
			exact, err := requestedResourceMap(p, "constraints")
			if err != nil {
				return t, err
			}
			values, group, err := resizeResources(spec, n, resources, exact)
			if err != nil {
				return t, err
			}
			nodeName := group.NodeName
			if nodeName == "" {
				nodeName = n.Name
			}
			role := group.Role
			if role == "" {
				role = n.Role
			}
			t.Type = "UpdateNodeResourceTask"
			t.Payload = map[string]any{"nodeEid": n.ID.Number(), "gid": n.GroupID.Number(), "domain": i.Domain, "appSpecEid": i.SpecID.Number(), "nodeName": nodeName, "nodeRole": role}
			for k, v := range values {
				t.Payload[k] = v
			}
		case "disk-grow":
			if n.Type != "qemu" {
				return t, domain.E("UNSUPPORTED", "disk expansion is only exposed for QEMU nodes")
			}
			if i.State != "stopped" && i.State != "approved" {
				return t, domain.E("STOP_REQUIRED", "stop before disk expansion")
			}
			var request domain.GrowDiskRequest
			if err := json.Unmarshal(domain.Result(p), &request); err != nil || request.TargetCapacityGB <= 0 {
				return t, domain.E("INVALID_SIZE", "supply total target disk capacity in GB")
			}
			t.Type = "EnlargeQemuNodeDiskTask"
			t.Payload = map[string]any{"nodeEid": n.ID.Number(), "gid": n.GroupID.Number(), "domain": i.Domain, "eaasIndex": n.Index.Number(), "newSizeInGb": request.TargetCapacityGB}
		case "snapshot-create", "snapshot-restore", "snapshot-delete":
			if n.Type != "qemu" {
				return t, domain.E("UNSUPPORTED", "this template does not expose QEMU snapshots")
			}
			if i.State != "stopped" && i.State != "approved" {
				return t, domain.E("STOP_REQUIRED", "stop before snapshot operations")
			}
			t.Payload = map[string]any{"nodeEid": n.ID.Number(), "gid": n.GroupID.Number(), "domain": i.Domain, "eaasIndex": n.Index.Number()}
			t.Type = map[string]string{"snapshot-create": "CreateSnapshotTask", "snapshot-restore": "ApplySnapshotTask", "snapshot-delete": "RemoveSnapshotTask"}[op]
			if op == "snapshot-create" {
				t.Payload["name"] = p.String("name")
				t.Payload["desc"] = p.String("description")
			} else {
				if p.ID("snapshot_id") == "" {
					return t, domain.E("SNAPSHOT_REQUIRED", "snapshot ID is required")
				}
				t.Payload["snapshotEid"] = p.ID("snapshot_id").Number()
			}
		case "service-add":
			if i.State != "stopped" && i.State != "running" {
				return t, domain.E("INVALID_STATE", "custom services require a stopped or running instance")
			}
			name := strings.TrimSpace(p.String("name"))
			if reservedService(name) {
				return t, domain.E("RESERVED_SERVICE", "service name is empty or reserved")
			}
			port := p.Int("port")
			if port < 1 || port > 65535 {
				return t, domain.E("INVALID_PORT", "port must be 1–65535")
			}
			bind := strings.TrimSpace(p.String("bind"))
			if bind == "" {
				bind = "127.0.0.1"
			}
			if address := net.ParseIP(bind); address == nil || address.To4() == nil || strings.Contains(bind, ":") {
				return t, domain.E("INVALID_ADDRESS", "custom services require an IPv4 bind address")
			}
			t.Type = "AddServicePortMapTask"
			t.Payload = map[string]any{"serviceName": name, "serviceType": "tcp", "serviceCheck": map[string]any{"type": "ignore"}, "nodeEid": n.ID.Number(), "portMap": map[string]any{"label": name, "innerBind": bind, "port": port}, "exposeOnGw": true}
		case "service-delete":
			if i.State != "stopped" && i.State != "running" {
				return t, domain.E("INVALID_STATE", "custom services require a stopped or running instance")
			}
			var svc *domain.Service
			for x := range n.Services {
				if string(n.Services[x].ID) == p.String("service_id") || n.Services[x].Name == p.String("service_id") {
					svc = &n.Services[x]
				}
			}
			if svc == nil {
				return t, domain.E("NOT_FOUND", "service not found")
			}
			if svc.Type != "tcp" {
				return t, domain.E("RESERVED_SERVICE", "only custom TCP port-map services can be deleted")
			}
			if svc.Builtin || reservedService(svc.Name) {
				return t, domain.E("RESERVED_SERVICE", "template services cannot be deleted")
			}
			t.Type = "RemoveServicePortMapTask"
			t.Payload = map[string]any{"serviceEid": svc.ID.Number()}
		}
	default:
		return t, domain.E("UNKNOWN_OPERATION", "unsupported instance operation")
	}
	return t, nil
}
func reservedService(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "_") {
		return true
	}
	for _, v := range []string{"ctrl", "console", "desktop", "jupyter", "slurm", "interface", "jupyterhub", "ssh"} {
		if s == v {
			return true
		}
	}
	return false
}
func (c *Controller) Operation(ctx context.Context, profile, op string, p Params) (any, error) {
	if p.Bool("dry_run") {
		req, e := c.buildTask(ctx, profile, op, p)
		if e != nil {
			return nil, e
		}
		return map[string]any{"dry_run": true, "operation": op, "request": req}, nil
	}
	rec, e := c.prepareTask(ctx, profile, op, p)
	if e != nil {
		return nil, e
	}
	if terminalTask(rec.State) {
		return rec, taskError(rec)
	}
	c.resumeTask(rec)
	if p.Has("wait") && !p.Bool("wait") || p.Bool("no_wait") {
		return rec, nil
	}
	return c.WaitTask(ctx, rec.ID)
}
func (c *Controller) prepareTask(ctx context.Context, profile, op string, p Params) (domain.TaskRecord, error) {
	c.intentMu.Lock()
	defer c.intentMu.Unlock()
	return c.prepareTaskLocked(ctx, profile, op, p, "")
}

type taskAlias struct {
	TaskID string
	Hash   string
}

func (c *Controller) taskReceipt(id, hash string) (domain.TaskRecord, error) {
	var alias taskAlias
	if err := c.Store.Get("task_keys", id, &alias); err == nil {
		if alias.Hash != hash {
			return domain.TaskRecord{}, domain.E("IDEMPOTENCY_CONFLICT", "request key was used with different arguments")
		}
		return c.Store.GetTask(alias.TaskID)
	} else if !state.IsNotFound(err) {
		return domain.TaskRecord{}, err
	}
	task, err := c.Store.GetTask(id)
	if err == nil && task.IntentHash != hash {
		err = domain.E("IDEMPOTENCY_CONFLICT", "request key was used with different arguments")
	}
	return task, err
}

func (c *Controller) prepareTaskLocked(ctx context.Context, profile, op string, p Params, runID string) (domain.TaskRecord, error) {
	id := TaskID(profile, op, p.String("idempotency_key"))
	hash := intentHash(profile, op, p)
	if existing, err := c.taskReceipt(id, hash); !state.IsNotFound(err) {
		return existing, err
	}
	req, err := c.buildTask(ctx, profile, op, p)
	if err != nil {
		return domain.TaskRecord{}, err
	}
	if runID == "" && req.TargetID != "" && lifecycleTask(op) {
		if task, err := c.pendingInstanceTask(ctx, profile, domain.Instance{ID: req.TargetID, State: "unknown"}); err != nil {
			return task, err
		} else if task.ID != "" {
			return task, lifecycleBusy(task)
		}
		if run, err := c.GetRun(profile, string(req.TargetID)); err == nil && !managedRunTerminal(run.State) && run.Phase != "active" {
			return domain.TaskRecord{}, runBusy(run)
		}
	}
	req.ID = id
	return c.savePreparedTask(domain.TaskRecord{
		ID: id, Profile: profile, Operation: op, Request: req, IntentHash: hash, RunID: runID,
	}, p)
}

func (c *Controller) savePreparedTask(t domain.TaskRecord, p Params) (domain.TaskRecord, error) {
	t, created, err := c.Store.CreateTaskWithIntent(t, p)
	if err == nil && created {
		c.Audit("task."+t.State, t.Profile, string(t.Request.TargetID), t.ID, t.RunID, t.Operation)
	}
	return t, err
}

func (c *Controller) resumeTask(t domain.TaskRecord) {
	c.spawn("task/"+t.ID, func() { c.reconcileTask(t.ID) })
}

func (c *Controller) reconcileTask(id string) {
	t, err := c.Store.GetTask(id)
	if err != nil || terminalTask(t.State) {
		return
	}
	if t.Manual && c.bindManualTask(t) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(c.ctx, 90*time.Second)
	defer cancel()

	request := t.Request
	if len(t.AttemptIDs) > 0 {
		child, err := c.Store.GetTask(t.AttemptIDs[len(t.AttemptIDs)-1])
		if err != nil {
			return
		}
		request = child.Request
	}

	maySubmit := t.State == "prepared" || t.SubmissionUnsent
	var stale bool
	if !maySubmit {
		snapshot, err := c.syncSnapshot(ctx, t.Profile, true)
		if err != nil {
			return
		}
		for _, known := range snapshot.Tasks {
			if known.ID != request.ID || !terminalTask(known.State) {
				continue
			}
			if known.Code == 1165 && len(t.AttemptIDs) == 0 {
				stale = true
				break
			}
			c.saveTaskResult(t, known, nil)
			return
		}
		if !stale {
			t.State = "uncertain"
			_ = c.Store.PutTask(t)
			return // Missing receipt alone is never permission to resend.
		}
	}
	blocked, err := c.allocationBlocked(t)
	if err != nil {
		return
	}
	if blocked {
		c.saveTaskResult(t, domain.TaskResult{ID: t.ID, State: "aborted", Message: "managed activation cancelled before submission"}, nil)
		return
	}
	api, err := c.api(t.Profile)
	if err != nil {
		return
	}
	if !stale {
		t.State, t.SubmissionUnsent = "submitted", false
		if c.Store.PutTask(t) != nil {
			return
		}
		result, err := c.submitTaskForRun(ctx, api, t, request)
		if result.Code != 1165 || len(t.AttemptIDs) > 0 {
			c.saveTaskResult(t, result, err)
			return
		}
	}
	// 1165 definitively rejects the old view. Only now may a fresh UUID be
	// allocated, and only once, after repeating ownership and price checks.
	var params Params
	if err := c.Store.Get("task_intents", t.ID, &params); err != nil {
		return
	}
	fresh, err := c.buildTask(ctx, t.Profile, t.Operation, params)
	if err != nil {
		if managedRetryable(err) {
			return
		}
		c.saveTaskResult(t, domain.TaskResult{ID: t.ID, State: "rejected", Code: 1165, Message: err.Error()}, nil)
		return
	}
	fresh.ID = uuid.NewString()
	child := domain.TaskRecord{ID: fresh.ID, ParentID: t.ID, IntentHash: t.IntentHash, Profile: t.Profile, Operation: t.Operation, RunID: t.RunID, Request: fresh, State: "prepared"}
	if _, _, err := c.Store.CreateTask(child); err != nil {
		return
	}
	t.AttemptIDs = append(t.AttemptIDs, fresh.ID)
	t.State, t.SubmissionUnsent = "submitted", false
	if err := c.Store.PutTask(t); err != nil {
		return
	}
	result, err := c.submitTaskForRun(ctx, api, t, fresh)
	c.saveTaskResult(child, result, err)
	c.saveTaskResult(t, result, err)
}

func (c *Controller) submitTaskForRun(ctx context.Context, api domain.PlatformAPI, t domain.TaskRecord, req domain.TaskRequest) (domain.TaskResult, error) {
	if t.RunID == "" || !allocatingTask(t.Operation) {
		return api.Submit(ctx, req)
	}
	if blocked, err := c.allocationBlocked(t); err != nil {
		return domain.TaskResult{ID: req.ID, Unsent: true}, err
	} else if blocked {
		return domain.TaskResult{ID: req.ID, State: "aborted", Message: "managed activation expired before submission"}, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if blocked, err := c.allocationBlocked(t); blocked || err != nil {
					cancel()
					return
				}
			}
		}
	}()
	result, err := api.Submit(ctx, req)
	cancel()
	<-done
	return result, err
}

func allocatingTask(op string) bool {
	switch op {
	case "create", "clone", "start", "restore", "renew":
		return true
	}
	return false
}

func (c *Controller) saveTaskResult(t domain.TaskRecord, result domain.TaskResult, err error) {
	t.SubmissionUnsent = result.Unsent
	switch {
	case terminalTask(result.State):
		t.State, t.Result = result.State, &result
	case result.Unsent && (strings.Contains(managedCode(err), "AUTH") || strings.Contains(managedCode(err), "CAPTCHA")):
		t.State = "auth_required"
	case result.Unsent:
		t.State = "prepared"
	default:
		t.State = "uncertain"
	}
	if c.Store.PutTask(t) == nil {
		c.Audit("task."+t.State, t.Profile, string(t.Request.TargetID), t.ID, t.RunID, fmt.Sprintf("code=%d", result.Code))
		if t.ParentID == "" && t.RunID != "" && terminalTask(t.State) {
			c.wakeRunForReceipt(t)
		}
	}
}

func (c *Controller) wakeRunForReceipt(t domain.TaskRecord) {
	c.spawn("receipt/"+t.ID, func() {
		for range 10 {
			if c.kickRun(t.RunID) {
				return
			}
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(100 * time.Millisecond):
			}
		}
	})
}

// Prepared allocating tasks must not outlive the finite run that authorized
// them. Unknown outcomes are reconciled by UUID without another submission.
func (c *Controller) allocationBlocked(t domain.TaskRecord) (bool, error) {
	if !allocatingTask(t.Operation) {
		return false, nil
	}
	runID := t.RunID
	if runID == "" {
		var p Params
		if e := c.Store.Get("task_intents", t.ID, &p); e != nil {
			return false, e
		}
		var ok bool
		runID, _, ok = strings.Cut(p.String("idempotency_key"), "/")
		if _, e := uuid.Parse(runID); !ok || e != nil {
			return false, nil
		}
	}
	r, e := c.Store.GetRun(runID)
	if e != nil {
		return false, e
	}
	if r.Profile != t.Profile {
		return true, domain.E("PROFILE_MISMATCH", "managed task profile changed")
	}
	return r.CancelRequested || !r.Deadline.After(time.Now()) || r.Phase == "cleanup" || r.Phase == "manual_wait" || r.Phase == "terminal", nil
}
