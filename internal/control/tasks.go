package control

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

func (c *Controller) buildTask(ctx context.Context, profile, op string, p Params) (domain.TaskRequest, error) {
	s, e := c.snapshot(ctx, profile)
	if e != nil {
		return domain.TaskRequest{}, e
	}
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
		if i.State == "running" || i.State == "starting" {
			return t, domain.E("INSTANCE_RUNNING", "stop the instance before release; use down for both")
		}
		t.Type = "AppUnreserveTask"
		t.Payload = base
	case "restore":
		t.Type = "AppReserveTask"
		t.Payload = map[string]any{"appEid": i.ID.Number(), "gid": i.GroupID.Number(), "appUid": i.UserID.Number(), "domain": i.Domain}
	case "delete":
		if i.State == "running" || i.Reserved {
			return t, domain.E("RELEASE_REQUIRED", "stop and release before permanent deletion")
		}
		t.Type = "RemoveAppTask"
		t.Payload = map[string]any{"appEid": i.ID.Number(), "gid": i.GroupID.Number(), "appUid": i.UserID.Number(), "domain": i.Domain}
	case "cancel-queue":
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
			if p.Int("size_gb") <= 0 {
				return t, domain.E("INVALID_SIZE", "supply total target disk capacity in GB")
			}
			t.Type = "EnlargeQemuNodeDiskTask"
			t.Payload = map[string]any{"nodeEid": n.ID.Number(), "gid": n.GroupID.Number(), "domain": i.Domain, "eaasIndex": n.Index.Number(), "newSizeInGb": p.Int("size_gb")}
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
	id := TaskID(profile, op, p.String("idempotency_key"))
	hash := intentHash(profile, op, p)
	if existing, e := c.Store.GetTask(id); e == nil {
		if existing.IntentHash != hash {
			return existing, domain.E("IDEMPOTENCY_CONFLICT", "request key was used with different arguments")
		}
		return existing, nil
	}
	req, e := c.buildTask(ctx, profile, op, p)
	if e != nil {
		return domain.TaskRecord{}, e
	}
	req.ID = id
	now := time.Now().UTC()
	rec := domain.TaskRecord{ID: id, Profile: profile, Operation: op, Request: req, IntentHash: hash, State: "prepared", CreatedAt: now, UpdatedAt: now}
	rec, _, e = c.Store.CreateTask(rec)
	if e != nil {
		return rec, e
	}
	if e = c.Store.Put("task_intents", id, p); e != nil {
		return rec, e
	}
	c.Audit("task.prepared", profile, string(req.TargetID), id, "", op)
	return rec, nil
}
func (c *Controller) resumeTask(t domain.TaskRecord) {
	c.spawn("task/"+t.ID, func() {
		api, e := c.api(t.Profile)
		if e != nil {
			return
		}
		request := t.Request
		var recovered *domain.TaskResult
		if len(t.AttemptIDs) > 0 {
			if child, ce := c.Store.GetTask(t.AttemptIDs[len(t.AttemptIDs)-1]); ce == nil {
				request = child.Request
			}
		}
		if blocked, be := c.allocationBlocked(t); be != nil || blocked {
			if t.State == "prepared" {
				t.State = "aborted"
				t.Result = &domain.TaskResult{ID: t.ID, State: "aborted", Message: "managed activation cancelled before submission"}
			} else {
				s, se := c.snapshot(c.ctx, t.Profile)
				if se == nil {
					for _, known := range s.Tasks {
						if known.ID == request.ID && terminalTask(known.State) {
							t.Result = &known
							t.State = known.State
						}
					}
				}
				if !terminalTask(t.State) {
					t.State = "uncertain"
				}
			}
			t.UpdatedAt = time.Now().UTC()
			_ = c.Store.PutTask(t)
			return
		}
		if t.State == "uncertain" || t.State == "submitted" || t.State == "auth_required" {
			s, se := c.snapshot(c.ctx, t.Profile)
			if se != nil {
				t.State = "auth_required"
				t.UpdatedAt = time.Now().UTC()
				_ = c.Store.PutTask(t)
				return
			}
			for _, known := range s.Tasks {
				if known.ID == request.ID && terminalTask(known.State) {
					if known.Code == 1165 {
						recovered = &known
						break
					}
					t.Result = &known
					t.State = known.State
					t.UpdatedAt = time.Now().UTC()
					_ = c.Store.PutTask(t)
					return
				}
			}
			if time.Since(t.CreatedAt) > time.Hour && recovered == nil {
				t.State = "uncertain"
				_ = c.Store.PutTask(t)
				return
			}
		}
		t.State = "submitted"
		t.UpdatedAt = time.Now().UTC()
		_ = c.Store.PutTask(t)
		ctx, cancel := context.WithTimeout(c.ctx, 30*time.Minute)
		defer cancel()
		guardDone := make(chan struct{})
		guardStopped := make(chan struct{})
		guardTask := t
		defer func() { close(guardDone); <-guardStopped }()
		go func() {
			defer close(guardStopped)
			tick := time.NewTicker(250 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-guardDone:
					return
				case <-ctx.Done():
					return
				case <-tick.C:
					blocked, e := c.allocationBlocked(guardTask)
					if e != nil || blocked {
						cancel()
						return
					}
				}
			}
		}()
		var result domain.TaskResult
		var err error
		if recovered != nil {
			result = *recovered
			err = domain.E("TASK_1165", "recovered stale view rejection")
		} else {
			result, err = api.Submit(ctx, request)
		}
		if result.Code == 1165 {
			var params Params
			if c.Store.Get("task_intents", t.ID, &params) == nil {
				blocked, be := c.allocationBlocked(t)
				if req, re := c.buildTask(ctx, t.Profile, t.Operation, params); re == nil && !blocked && be == nil {
					req.ID = uuid.NewString()
					child := domain.TaskRecord{ID: req.ID, ParentID: t.ID, IntentHash: t.IntentHash, Profile: t.Profile, Operation: t.Operation, Request: req, State: "prepared", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
					if saved, _, ce := c.Store.CreateTask(child); ce == nil {
						t.AttemptIDs = append(t.AttemptIDs, req.ID)
						t.UpdatedAt = time.Now().UTC()
						_ = c.Store.PutTask(t)
						result, err = api.Submit(ctx, req)
						saved.Result = &result
						saved.State = result.State
						if saved.State == "" {
							saved.State = "uncertain"
						}
						_ = c.Store.PutTask(saved)
					}
				}
			}
		}
		t.UpdatedAt = time.Now().UTC()
		if err != nil {
			if terminalTask(result.State) {
				t.State = result.State
				t.Result = &result
			} else {
				t.State = "uncertain"
				if strings.Contains(err.Error(), "AUTH") || strings.Contains(err.Error(), "CAPTCHA") {
					t.State = "auth_required"
				}
			}
			_ = c.Store.PutTask(t)
			c.Audit("task.pending", t.Profile, string(t.Request.TargetID), t.ID, "", "task requires reconciliation")
			return
		}
		t.State = result.State
		t.Result = &result
		_ = c.Store.PutTask(t)
		c.Audit("task."+result.State, t.Profile, string(t.Request.TargetID), t.ID, "", fmt.Sprintf("code=%d", result.Code))
	})
}

// Prepared allocating tasks must not outlive the finite run that authorized
// them. Unknown outcomes are reconciled by UUID without another submission.
func (c *Controller) allocationBlocked(t domain.TaskRecord) (bool, error) {
	switch t.Operation {
	case "create", "clone", "start", "restore", "renew":
	default:
		return false, nil
	}
	var p Params
	if e := c.Store.Get("task_intents", t.ID, &p); e != nil {
		return false, e
	}
	key := p.String("idempotency_key")
	runID, _, ok := strings.Cut(key, "/")
	if !ok {
		return false, nil
	}
	if _, e := uuid.Parse(runID); e != nil {
		return false, nil
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
