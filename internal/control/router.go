package control

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
)

func (c *Controller) Handle(ctx context.Context, r domain.RPCRequest, p Params) (result any, err error) {
	profile := r.Profile
	if profile == "" {
		profile = c.Configuration().Selected
	}
	var profileError error
	profile, profileError = c.authorizedProfile(r, p)
	if profileError != nil {
		return nil, profileError
	}
	if e := c.Authorize(r.ClientID, r.Method, profile); e != nil {
		return nil, e
	}
	if r.Method == "account.snapshot" || r.Method == "account.refresh" {
		f, e := c.account(profile)
		if e != nil {
			return nil, e
		}
		if r.Method == "account.refresh" {
			return c.refreshAccount(ctx, f)
		}
		return f.current()
	}
	// A gateway denial on a live operation also invalidates idle subscribers.
	if !p.Bool("cached") && !p.Bool("dry_run") {
		ticket := c.accountTicket(profile)
		defer func() {
			if err != nil {
				ticket.failure(err, false)
			}
		}()
	}
	if p.Bool("dry_run") && (isMutation(r.Method) || r.Method == "instance.wait" || r.Method == "run.wait") {
		return map[string]any{"dry_run": true, "operation": r.Method, "profile": profile}, nil
	}
	if r.Method == "portal.read" && p.String("section") == "admin" {
		if err := c.Authorize(r.ClientID, "compute.admin", profile); err != nil {
			return nil, err
		}
	}
	if r.Method == "compute.read" && (strings.HasPrefix(p.String("section"), "admin") || p.String("section") == "project.show") {
		if err := c.Authorize(r.ClientID, "compute.admin", profile); err != nil {
			return nil, err
		}
	}
	if !p.Bool("cached") && !p.Bool("dry_run") && (p.Bool("compute") || strings.HasPrefix(r.Method, "spec.") || strings.HasPrefix(r.Method, "data.") || r.Method == "resources.overview" || r.Method == "quota.get" || r.Method == "compute.read") {
		a, e := c.api(profile)
		if e != nil {
			return nil, e
		}
		if a, ok := a.(interface{ PrepareCompute(context.Context) error }); ok {
			if e = a.PrepareCompute(ctx); e != nil {
				return nil, e
			}
		}
	}
	switch r.Method {
	case "compute.remote.checkout":
		if err := c.Authorize(r.ClientID, "compute.admin", profile); err != nil {
			return nil, err
		}
		return c.remoteCheckout(ctx, profile, p)
	case "compute.remote.order":
		if err := c.Authorize(r.ClientID, "compute.admin", profile); err != nil {
			return nil, err
		}
		a, e := c.api(profile)
		if e != nil {
			return nil, e
		}
		native, ok := a.(*platform.Client)
		if !ok {
			return nil, domain.E("UNSUPPORTED", "缺少远程订单适配器。")
		}
		return native.RemoteOrder(ctx, p.String("target"), p.String("action"), p.String("confirm"), p.Bool("dry_run"))
	case "data.edit":
		a, e := c.api(profile)
		if e != nil {
			return nil, e
		}
		native, ok := a.(*platform.Client)
		if !ok {
			return nil, domain.E("UNSUPPORTED", "此适配器没有文件操作接口。")
		}
		result, e := native.EditStorage(ctx, p.String("action"), platform.StoragePath{Store: p.String("store"), Path: p.String("path"), GroupID: p.ID("group_id")}, p.String("new_name"), p.String("confirm"), p.Bool("dry_run"))
		if e == nil && !p.Bool("dry_run") {
			c.Audit("data."+p.String("action"), profile, p.String("path"), "", "", "文件操作已由平台确认")
		}
		return result, e
	case "compute.quota.request":
		return c.Operation(ctx, profile, "quota-request", p)
	case "compute.admin.approve", "compute.admin.reject":
		if !p.Bool("dry_run") && (p.String("target") == "" || p.String("confirm") != p.String("target")) {
			return nil, domain.E("CONFIRM_REQUIRED", "请确认要处理的申请 ID。")
		}
		op := "approve-request"
		if r.Method == "compute.admin.reject" {
			op = "reject-request"
		}
		return c.Operation(ctx, profile, op, p)
	case "compute.admin.update":
		return c.adminQuota(ctx, profile, p)
	case "compute.read":
		return c.computeRead(ctx, profile, p)
	case "portal.trial":
		return c.modelTrial(ctx, profile, p)
	case "portal.read":
		return c.PortalRead(ctx, profile, p)
	case "portal.change":
		return c.PortalWrite(ctx, profile, p)
	case "portal.export":
		if p.Bool("dry_run") {
			return map[string]any{"dry_run": true, "operation": "portal.export"}, nil
		}
		return c.PortalExport(ctx, profile, p.String("target"))
	case "resources.overview":
		return c.Resources(ctx, profile, p)
	case "status.get":
		return c.Status(ctx, profile, p)
	case "agent.status":
		scope := profile
		if r.ClientID == "" {
			scope = ""
		}
		runs, err := c.Store.ListRuns(scope)
		if err != nil {
			return nil, err
		}
		tasks, err := c.Store.ListTasks(scope)
		if err != nil {
			return nil, err
		}
		routes, err := c.Store.ListRoutes(scope)
		if err != nil {
			return nil, err
		}
		active, pending := 0, 0
		for _, run := range runs {
			if !managedRunTerminal(run.State) {
				active++
			}
		}
		for _, t := range tasks {
			if t.ParentID == "" && (!terminalTask(t.State) || t.Manual && t.State == "complete" && !t.Verified && taskError(t) == nil) {
				pending++
			}
		}
		health := map[string]any{
			"state": "running", "protocol": 1, "controller": "local", "version": c.opts.Version,
			"runs": len(runs), "routes": len(routes), "active_runs": active, "pending_tasks": pending, "relay_enabled": c.opts.AuthorizedKeys != "",
		}
		if r.ClientID == "" {
			health["other_connections"] = max(0, c.connections.Load()-1)
		}
		return health, nil
	case "agent.stop":
		return map[string]any{"state": "stopping", "routes_preserved": true}, nil
	case "agent.reload":
		return c.ReloadConfiguration(ctx)
	case "route.list":
		return c.Store.ListRoutes(profile)
	case "route.get":
		if p.Bool("repair") {
			if p.Bool("dry_run") {
				return map[string]any{"dry_run": true, "operation": "ssh.ensure", "target": p.String("target"), "node": p.String("node"), "steps": []string{"verify platform ownership and running state", "log into instance console", "enroll public keys and pin SSH host key", "verify direct SSH or configure a loopback relay tunnel"}}, nil
			}
			return c.EnsureSSH(ctx, profile, p.String("target"), p.String("node"), true)
		}
		return c.CachedRoute(profile, p.String("target"), p.String("node"))
	case "key.list":
		return c.ClientKeys(profile), nil
	case "key.import":
		return c.RegisterDeviceKey(ctx, profile, p.String("name"), p.String("public_key"), p.String("target"), p.String("node"))
	case "key.revoke":
		return c.RevokeDeviceKey(ctx, profile, p.String("target"))
	case "client.register":
		var v ClientIdentity
		if e := json.Unmarshal(r.Params, &v); e != nil {
			return nil, e
		}
		return c.RegisterClient(v)
	case "client.list":
		var clients []ClientIdentity
		e := c.Store.List("clients", &clients)
		return clients, e
	case "client.revoke":
		var v ClientIdentity
		if e := c.Store.Get("clients", p.String("target"), &v); e != nil {
			return nil, e
		}
		v.Revoked = true
		return c.RegisterClient(v)
	case "relay.reserve":
		return c.RelayReserve(r.ClientID, profile, p.ID("instance_id"), p.ID("node_id"))
	case "relay.enroll":
		return c.RelayEnroll(r.ClientID, p.String("key"), p.String("public_key"))
	case "relay.revoke":
		return c.RelayRevoke(r.ClientID, p.String("key"))
	case "relay.list":
		return c.Store.ListPortLeases(r.ClientID)
	case "auth.status", "auth.info", "auth.access", "auth.login", "auth.refresh", "auth.setup", "auth.logout", "auth.gateway.start", "auth.gateway.poll", "auth.gateway.confirm", "auth.captcha":
		return c.auth(ctx, profile, r.Method, p)
	case "auth.start":
		return c.StartLogin(ctx, profile, p)
	case "auth.poll":
		return c.PollLogin(ctx, profile)
	case "auth.sso.submit":
		return c.SubmitLoginPassword(ctx, profile, p)
	case "auth.probe":
		a, e := c.api(profile)
		if e != nil {
			return nil, e
		}
		if probe, ok := a.(interface {
			ProbeSession(context.Context, string) (platform.SessionProbe, error)
		}); ok {
			return probe.ProbeSession(ctx, p.String("proxy"))
		}
		return nil, domain.E("UNSUPPORTED", "adapter does not provide isolated session probes")
	case "spec.list":
		a, e := c.api(profile)
		if e != nil {
			return nil, e
		}
		v, e := a.Specs(ctx)
		if e != nil {
			return nil, e
		}
		return projectList(v, p)
	case "preset.resolve":
		return c.ResolvePreset(ctx, profile, p)
	case "spec.show":
		return c.ResolveSpec(ctx, profile, p.String("target"))
	case "spec.estimate":
		a, e := c.api(profile)
		if e != nil {
			return nil, e
		}
		id := p.ID("spec_id")
		constraints := p.Map("constraints")
		if name := p.String("preset"); name != "" {
			pr, e := c.preset(p, profile)
			if e != nil {
				return nil, e
			}
			if id == "" {
				id = pr.SpecID
			}
			if !p.Has("constraints") {
				constraints = pr.Constraints
			}
		}
		if id == "" {
			return nil, domain.E("SPEC_REQUIRED", "choose --spec-id or a preset")
		}
		if !numericSpecSelector(string(id)) {
			spec, e := c.ResolveSpec(ctx, profile, string(id))
			if e != nil {
				return nil, e
			}
			id = spec.ID
		}
		constraints, e = c.resourceConstraints(ctx, profile, id, constraints, p.Map("resources"))
		if e != nil {
			return nil, e
		}
		return a.Estimate(ctx, id, constraints)
	case "quota.get":
		s, e := c.snapshot(ctx, profile)
		return s.Quota, e
	case "instance.list":
		v, e := c.Instances(ctx, profile, p.Bool("cached"))
		if e != nil {
			return nil, e
		}
		return projectList(v, p)
	case "instance.inspect":
		return c.inspectInstance(ctx, profile, p)
	case "instance.wait":
		return c.waitInstance(ctx, profile, p)
	case "instance.events":
		return c.instanceEvents(ctx, profile, p)
	case "instance.create", "instance.start", "instance.restart", "instance.restore", "instance.adopt", "instance.clone":
		return c.ManagedOperation(ctx, profile, strings.TrimPrefix(r.Method, "instance."), p)
	case "up":
		return c.Up(ctx, profile, p)
	case "down":
		return c.Down(ctx, profile, p)
	case "instance.stop":
		op := "stop"
		if p.Bool("force") {
			op = "force-stop"
		}
		return c.manualInstance(ctx, profile, op, p)
	case "instance.delete":
		p.Set("confirm_delete", true)
		return c.manualInstance(ctx, profile, "delete", p)
	case "instance.release":
		return c.manualInstance(ctx, profile, "release", p)
	case "instance.cancel-queue":
		return c.manualInstance(ctx, profile, "cancel-queue", p)
	case "instance.resize", "instance.disk-grow":
		return c.Operation(ctx, profile, strings.TrimPrefix(r.Method, "instance."), p)
	case "snapshot.create", "snapshot.restore", "snapshot.delete":
		return c.Operation(ctx, profile, strings.ReplaceAll(r.Method, ".", "-"), p)
	case "service.add", "service.delete":
		return c.Operation(ctx, profile, strings.ReplaceAll(r.Method, ".", "-"), p)
	case "service.list", "snapshot.list":
		s, e := c.snapshot(ctx, profile)
		if e != nil {
			return nil, e
		}
		i, e := Resolve(s, p.String("target"))
		if e != nil {
			return nil, e
		}
		n, e := SelectNode(i, p.String("node"))
		if e != nil {
			return nil, e
		}
		if r.Method == "service.list" {
			return n.Services, nil
		}
		return n.Snapshots, nil
	case "lease.status":
		run, e := c.GetRun(profile, p.String("target"))
		if e != nil {
			return nil, e
		}
		v := map[string]any{"run": run}
		s, e := c.snapshot(ctx, profile)
		if e != nil {
			v["platform_status"] = rpcError(e)
			return v, nil
		}
		for _, i := range s.Instances {
			if i.ID == run.InstanceID {
				v["platform_expiry"] = i.ExpiresAt
				v["platform_state"] = i.State
			}
		}
		pending := []domain.Renewal{}
		for _, q := range s.Renewals {
			if q.TargetID == run.InstanceID {
				pending = append(pending, q)
			}
		}
		v["requests"] = pending
		return v, nil
	case "lease.set", "job.update":
		if r.Method == "job.update" {
			if _, e := c.job(profile, p.String("target")); e != nil {
				return nil, e
			}
		}
		return c.UpdateRunPolicy(ctx, profile, p)
	case "lease.renew":
		d, e := time.ParseDuration(p.String("duration"))
		if e != nil || d < time.Second {
			return nil, domain.E("INVALID_DURATION", "supply a positive --duration")
		}
		run, e := c.GetRun(profile, p.String("target"))
		if e != nil {
			return nil, e
		}
		s, e := c.snapshot(ctx, profile)
		if e != nil {
			return nil, e
		}
		i, e := Resolve(s, string(run.InstanceID))
		if e != nil {
			return nil, e
		}
		if i.ExpiresAt.IsZero() {
			return nil, domain.E("EXPIRY_UNKNOWN", "platform expiry is not known")
		}
		if i.ExpiresAt.Add(d).After(run.Deadline) {
			return nil, domain.E("LIMIT_EXCEEDED", "renewal would exceed the managed run deadline; update the lease explicitly")
		}
		p.Set("seconds", int64(d/time.Second))
		p.Set("target", string(run.InstanceID))
		return c.Operation(ctx, profile, "renew", p)
	case "job.run":
		return c.RunJob(ctx, profile, p)
	case "run.list", "job.list":
		v, e := c.Store.ListRuns(profile)
		if e != nil {
			return nil, e
		}
		if r.Method == "run.list" {
			return projectList(v, p)
		}
		filtered := make([]domain.Run, 0, len(v))
		for _, run := range v {
			if run.Kind == "job" {
				filtered = append(filtered, run)
			}
		}
		return projectList(filtered, p)
	case "job.get":
		return c.job(profile, p.String("target"))
	case "run.get":
		return c.GetRun(profile, p.String("target"))
	case "run.wait":
		condition := p.String("condition")
		if condition != "" && condition != "ready" && condition != "finished" {
			return nil, domain.E("INVALID_CONDITION", "wait condition must be ready or finished")
		}
		r, e := c.GetRun(profile, p.String("target"))
		if e != nil {
			return nil, e
		}
		if !managedRunTerminal(r.State) {
			if e = c.Store.UpdateRun(r.ID, func(cur *domain.Run) error { cur.LastError = ""; return nil }); e != nil {
				return nil, e
			}
			c.kickRun(r.ID)
		}
		return c.waitManaged(ctx, r.ID, condition == "finished")
	case "job.cancel":
		if _, e := c.job(profile, p.String("target")); e != nil {
			return nil, e
		}
		return c.CancelRun(ctx, profile, p)
	case "task.list":
		v, e := c.Store.ListTasks(profile)
		if e != nil {
			return nil, e
		}
		return projectList(v, p)
	case "task.get", "task.wait":
		t, e := c.Store.GetTask(p.String("target"))
		if e != nil {
			return nil, e
		}
		if t.Profile != profile {
			return nil, domain.E("NOT_FOUND", "task not found in profile")
		}
		if r.Method == "task.wait" {
			return c.WaitTask(ctx, t.ID)
		}
		return t, nil
	case "audit.list":
		after := time.Time{}
		if p.String("after") != "" {
			var e error
			after, e = time.Parse(time.RFC3339, p.String("after"))
			if e != nil {
				return nil, domain.E("INVALID_TIME", "after must use RFC3339")
			}
		}
		v, e := c.Store.ListAudits(after, limit(p, 100))
		if e != nil {
			return nil, e
		}
		out := []domain.AuditEvent{}
		for _, x := range v {
			if x.Profile == profile || x.Profile == "" {
				out = append(out, x)
			}
		}
		return out, nil
	case "connect.list", "connect.stat", "connect.mkdir", "connect.rename", "connect.remove":
		return c.HandleFiles(ctx, profile, r.Method, p)
	case "data.config", "data.list":
		a, e := c.api(profile)
		if e != nil {
			return nil, e
		}
		storage, ok := a.(interface {
			StorageConfig(context.Context) ([]platform.Storage, error)
			ListFiles(context.Context, platform.StoragePath) ([]platform.File, error)
		})
		if !ok {
			return nil, domain.E("UNSUPPORTED", "adapter does not expose storage")
		}
		if r.Method == "data.config" {
			return storage.StorageConfig(ctx)
		}
		path, e := storagePath(ctx, storage, p)
		if e != nil {
			return nil, e
		}
		return storage.ListFiles(ctx, path)
	case "doctor":
		return c.Doctor(ctx, profile, p), nil
	default:
		return nil, domain.E("UNKNOWN_METHOD", "unknown controller method")
	}
}

// A relay request is scoped to the persisted lease profile, never to a profile
// supplied alongside its opaque key. This also lets proxy commands omit it.
func (c *Controller) authorizedProfile(r domain.RPCRequest, p Params) (string, error) {
	switch r.Method {
	case "relay.enroll", "relay.revoke", "relay.proxy":
		l, e := c.Store.GetPortLease(p.String("key"))
		if e != nil {
			return "", e
		}
		return l.Profile, nil
	}
	if r.Profile != "" {
		return r.Profile, nil
	}
	return c.Configuration().Selected, nil
}

func isMutation(method string) bool {
	for _, m := range []string{"portal.trial", "auth.start", "auth.poll", "auth.sso.submit", "auth.access", "auth.login", "auth.refresh", "auth.setup", "auth.logout", "auth.gateway.start", "auth.gateway.poll", "auth.gateway.confirm", "key.import", "key.revoke", "client.register", "client.revoke", "relay.reserve", "relay.enroll", "relay.revoke", "lease.set", "lease.renew", "job.update", "job.cancel", "agent.stop", "agent.reload", "connect.mkdir", "connect.rename", "connect.remove"} {
		if method == m {
			return true
		}
	}
	return false
}
func limit(p Params, def int) int {
	n := p.Int("limit")
	if n <= 0 {
		return def
	}
	if n > 10000 {
		return 10000
	}
	return int(n)
}
func (c *Controller) auth(ctx context.Context, profile, method string, p Params) (any, error) {
	if method == "auth.logout" {
		defer c.lockLogin(profile)()
	}
	a, e := c.api(profile)
	if e != nil {
		return nil, e
	}
	switch method {
	case "auth.status":
		return a.AuthStatus(ctx), nil
	case "auth.info":
		if v, ok := a.(interface {
			AuthInfo(context.Context) (map[string]any, error)
		}); ok {
			return v.AuthInfo(ctx)
		}
		return nil, domain.E("UNSUPPORTED", "adapter has no authentication capability metadata")
	case "auth.access":
		if p.String("password") == "" {
			return nil, domain.E("CREDENTIALS_REQUIRED", "instance console password is required")
		}
		if c.Vault == nil {
			return nil, domain.E("SECRET_STORE", "protected credential storage is required")
		}
		if e = c.Vault.Put("platform/"+profile+"/instance_credentials", map[string]string{"password": p.String("password")}); e != nil {
			return nil, e
		}
		return map[string]any{"stored": true, "profile": profile, "purpose": "instance console SSH bootstrap"}, nil
	case "auth.gateway.start":
		return a.GatewayStart(ctx)
	case "auth.gateway.poll":
		return a.GatewayPoll(ctx)
	case "auth.gateway.confirm":
		if v, ok := a.(interface {
			GatewayConfirm(context.Context) (domain.AuthChallenge, error)
		}); ok {
			return v.GatewayConfirm(ctx)
		}
		return nil, domain.E("UNSUPPORTED", "adapter has no gateway confirmation endpoint")
	case "auth.captcha":
		if v, ok := a.(interface {
			Captcha(context.Context) (platform.Captcha, error)
		}); ok {
			return v.Captcha(ctx)
		}
		return nil, domain.E("UNSUPPORTED", "adapter has no captcha endpoint")
	case "auth.login":
		if p.String("username") == "" || p.String("password") == "" {
			return nil, domain.E("CREDENTIALS_REQUIRED", "supply username and password through protected input")
		}
		if v, ok := a.(interface {
			LoginWithOptions(context.Context, platform.LoginOptions) (domain.Identity, error)
		}); ok {
			return v.LoginWithOptions(ctx, platform.LoginOptions{Username: p.String("username"), Password: p.String("password"), Token: p.String("token"), CaptchaUUID: p.String("captcha_uuid"), CaptchaCode: p.String("captcha_code")})
		}
		return a.Login(ctx, p.String("username"), p.String("password"))
	case "auth.refresh":
		if i, e := a.WhoAmI(ctx); e == nil {
			return i, nil
		}
		if v, ok := a.(interface {
			LoginSaved(context.Context, ...platform.LoginOptions) (domain.Identity, error)
		}); ok {
			return v.LoginSaved(ctx)
		}
		return nil, domain.E("CREDENTIALS_REQUIRED", "no saved login is available")
	case "auth.setup":
		if e = a.SetupPassword(ctx, p.String("password"), p.String("old_password")); e != nil {
			return nil, e
		}
		return a.AuthStatus(ctx), nil
	case "auth.logout":
		if !p.Bool("force") {
			runs, _ := c.Store.ListRuns(profile)
			for _, run := range runs {
				if !runTerminal(run.State) {
					return nil, domain.E("ACTIVE_RUNS", "managed runs need platform access for cleanup; use --force to log out explicitly")
				}
			}
		}
		c.accountLogout(profile)
		logoutErr := a.Logout(ctx)
		if c.Vault != nil {
			ns := c.portalNamespace(profile)
			if i, err := c.portalIndex(ns); err == nil {
				i.Summary.State = "expired"
				i.ChangedAt = time.Now().UTC()
				i.Summary.CheckedAt = i.ChangedAt
				_ = c.Vault.Put(ns+"index", i)
			}
			_ = c.Vault.Put(c.portalNamespace(profile)+"compute-status", observedAuth{Gateway: "not_checked", Platform: "expired", Reason: "logged_out", At: time.Now().UTC()})
			_ = c.Vault.Delete("platform/" + profile + "/pending-service")
		}
		if logoutErr != nil {
			return nil, logoutErr
		}
		if c.Vault != nil {
			_ = c.Vault.Delete(loginFlowKey(profile))
		}
		return map[string]any{"logged_out": true, "ssh_routes_preserved": true}, nil
	}
	return nil, domain.E("UNKNOWN_METHOD", "unknown authentication method")
}
func runTerminal(s string) bool {
	switch s {
	case "done", "complete", "completed", "failed", "cancelled", "canceled", "expired", "stopped", "released", "deleted":
		return true
	}
	return false
}

// Filtering and projection happen after whitelisted platform serialization.
func projectList(v any, p Params) (any, error) {
	b, e := json.Marshal(v)
	if e != nil {
		return nil, e
	}
	rows := []map[string]any{}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.UseNumber()
	if e = d.Decode(&rows); e != nil {
		return nil, e
	}
	var filters, columns []string
	if e = p.Into("filter", &filters); e != nil {
		return nil, e
	}
	if e = p.Into("columns", &columns); e != nil {
		return nil, e
	}
	if s := p.String("state"); s != "" {
		filters = append(filters, "state="+s)
	}
	out := []map[string]any{}
	for _, row := range rows {
		match := true
		for _, f := range filters {
			k, v, ok := strings.Cut(f, "=")
			if !ok || k == "" {
				return nil, domain.E("INVALID_FILTER", "use field=value filters")
			}
			if fmt.Sprint(row[k]) != v {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		if len(columns) > 0 {
			selected := map[string]any{}
			for _, k := range columns {
				if val, ok := row[k]; ok {
					selected[k] = val
				} else {
					selected[k] = nil
				}
			}
			row = selected
		}
		out = append(out, row)
	}
	return out, nil
}

type storageLocator interface {
	StorageConfig(context.Context) ([]platform.Storage, error)
}

func storagePath(ctx context.Context, a storageLocator, p Params) (platform.StoragePath, error) {
	stores, e := a.StorageConfig(ctx)
	if e != nil {
		return platform.StoragePath{}, e
	}
	selected := p.String("store")
	matches := []platform.Storage{}
	for _, s := range stores {
		if selected != "" && (s.Name == selected || s.URLPath == selected) {
			matches = append(matches, s)
		}
		if selected == "" && s.IsWorkDir {
			matches = append(matches, s)
		}
	}
	if len(matches) != 1 {
		return platform.StoragePath{}, domain.E("STORAGE_REQUIRED", "choose one storage name from data stores")
	}
	return platform.StoragePath{Store: matches[0].URLPath, GroupID: p.ID("group_id"), Path: p.String("path")}, nil
}

func (c *Controller) Doctor(ctx context.Context, profile string, p Params) map[string]any {
	routes, _ := c.Store.ListRoutes(profile)
	runs, _ := c.Store.ListRuns(profile)
	checks := []map[string]any{}
	target := p.String("target")
	if target != "" {
		route, e := c.CachedRoute(profile, target, p.String("node"))
		if e != nil {
			checks = append(checks, map[string]any{"check": "ssh", "state": "not_prepared", "error": rpcError(e)})
		} else {
			checks = append(checks, c.checkRoute(ctx, route))
		}
	}
	timed, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	a, e := c.api(profile)
	auth := domain.AuthStatus{Gateway: "unknown", Platform: "unknown"}
	if e == nil {
		auth = a.AuthStatus(timed)
	}
	names := []string{}
	for _, r := range routes {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return map[string]any{"profile": profile, "controller": "running", "authentication": auth, "checks": checks, "cached_routes": len(routes), "managed_runs": len(runs), "targets": names, "ssh_command": "bupt ssh <instance>", "open_ssh_setup": "bupt ssh-config install <instance>", "auth_expiry_effect": "management requires login; established SSH routes do not"}
}
