package control

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"bupt.local/cli/internal/state"
	"github.com/google/uuid"
)

type Options struct {
	Config         *config.Config
	Store          *state.Store
	Vault          domain.SecretStore
	Factory        func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error)
	AuthorizedKeys string
	Socket         string
	Executable     string
	Principal      string
	JobBackend     JobBackend
}
type Controller struct {
	accounts      accountFeeds
	Config        *config.Config
	Store         *state.Store
	Vault         domain.SecretStore
	opts          Options
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	loginLocks    sync.Map // profile name to *sync.Mutex
	intentMu      sync.Mutex
	keysMu        sync.Mutex
	apis          map[string]domain.PlatformAPI
	working       map[string]bool
	lastAuth      map[string]time.Time
	wg            sync.WaitGroup
	configuration atomic.Pointer[config.Config]
	closed        bool
}

func New(o Options) (*Controller, error) {
	if o.Config == nil || o.Store == nil {
		return nil, domain.E("CONFIG", "controller requires config and state")
	}
	if o.Factory == nil {
		o.Factory = func(p domain.Profile, v domain.SecretStore) (domain.PlatformAPI, error) { return platform.New(p, v) }
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{Config: o.Config, Store: o.Store, Vault: o.Vault, opts: o, ctx: ctx, cancel: cancel, apis: map[string]domain.PlatformAPI{}, working: map[string]bool{}, lastAuth: map[string]time.Time{}}
	c.configuration.Store(o.Config)
	for profile := range o.Config.Profiles {
		// An unavailable projection is reported by that profile's account RPC;
		// a colliding legacy profile name must not disable other profiles.
		_, _ = c.account(profile)
	}
	return c, nil
}
func (c *Controller) Configuration() *config.Config {
	if v := c.configuration.Load(); v != nil {
		return v
	}
	return c.Config
}
func (c *Controller) Close() error {
	c.cancel()
	c.closeAccounts()
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		c.wg.Wait()
		return nil
	}
	c.closed = true
	apis := make([]domain.PlatformAPI, 0, len(c.apis))
	for _, a := range c.apis {
		apis = append(apis, a)
	}
	c.mu.Unlock()
	for _, a := range apis {
		_ = a.Close()
	}
	c.wg.Wait()
	return nil
}
func (c *Controller) api(profile string) (domain.PlatformAPI, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, domain.E("AGENT_STOPPING", "controller is stopping")
	}
	if profile == "" {
		profile = c.Configuration().Selected
	}
	p, ok := c.Configuration().Profiles[profile]
	if !ok {
		return nil, domain.E("PROFILE_NOT_FOUND", "unknown profile")
	}
	if a := c.apis[profile]; a != nil {
		return a, nil
	}
	a, e := c.opts.Factory(p, c.Vault)
	if e != nil {
		return nil, e
	}
	c.apis[profile] = a
	return a, nil
}
func (c *Controller) snapshot(ctx context.Context, p string) (domain.Snapshot, error) {
	ticket := c.accountTicket(p)
	a, e := c.api(p)
	if e != nil {
		return domain.Snapshot{}, e
	}
	if auth, ok := a.(interface{ PrepareCompute(context.Context) error }); ok {
		if err := auth.PrepareCompute(ctx); err != nil {
			ticket.failure(err, false)
			return domain.Snapshot{}, err
		}
	}
	s, e := a.Sync(ctx)
	var de *domain.Error
	if e != nil && errors.As(e, &de) && de.Code == "AUTH_EXPIRED" && c.Vault != nil {
		c.mu.Lock()
		allowed := time.Since(c.lastAuth[p]) > time.Minute
		if allowed {
			c.lastAuth[p] = time.Now()
		}
		c.mu.Unlock()
		if allowed {
			var cred struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if c.Vault.Get("platform/"+p+"/credentials", &cred) == nil && cred.Username != "" && cred.Password != "" {
				if _, le := a.Login(ctx, cred.Username, cred.Password); le == nil {
					s, e = a.Sync(ctx)
				} else {
					e = le
				}
			}
		}
	}
	if e == nil {
		_ = c.Store.Put("snapshots", p, s)
		ticket.instances(s)
	} else {
		ticket.failure(e, false)
	}
	return s, e
}
func (c *Controller) Audit(kind, p, target, task, run, message string) {
	_ = c.Store.AppendAudit(domain.AuditEvent{Time: time.Now().UTC(), Kind: kind, Profile: p, Target: target, TaskID: task, RunID: run, Message: message})
}
func (c *Controller) spawn(key string, fn func()) bool {
	c.mu.Lock()
	if c.closed || c.ctx.Err() != nil || c.working[key] {
		c.mu.Unlock()
		return false
	}
	c.working[key] = true
	c.wg.Add(1)
	c.mu.Unlock()
	go func() { defer c.wg.Done(); defer func() { c.mu.Lock(); delete(c.working, key); c.mu.Unlock() }(); fn() }()
	return true
}

type Params map[string]json.RawMessage

func Decode(b json.RawMessage) (Params, error) {
	p := Params{}
	if len(b) == 0 {
		return p, nil
	}
	e := json.Unmarshal(b, &p)
	return p, e
}
func (p Params) String(k string) string { var s string; _ = json.Unmarshal(p[k], &s); return s }
func (p Params) Bool(k string) bool     { var b bool; _ = json.Unmarshal(p[k], &b); return b }
func (p Params) Int(k string) int64 {
	var n json.Number
	if json.Unmarshal(p[k], &n) == nil {
		v, _ := n.Int64()
		return v
	}
	return 0
}
func (p Params) Has(k string) bool { _, ok := p[k]; return ok }
func (p Params) Into(k string, out any) error {
	if len(p[k]) == 0 {
		return nil
	}
	return json.Unmarshal(p[k], out)
}
func (p Params) Set(k string, v any)   { p[k] = domain.Result(v) }
func (p Params) ID(k string) domain.ID { var i domain.ID; _ = json.Unmarshal(p[k], &i); return i }
func (p Params) Map(k string) map[string]any {
	m := map[string]any{}
	if len(p[k]) > 0 {
		d := json.NewDecoder(strings.NewReader(string(p[k])))
		d.UseNumber()
		_ = d.Decode(&m)
	}
	return m
}
func (c *Controller) configForProfile(profile string) (*config.Config, error) {
	cfg := c.Configuration()
	if profile == "" || profile == cfg.Selected {
		return cfg, nil
	}
	return cfg.ForProfile(profile)
}
func (c *Controller) policy(p Params, profiles ...string) (domain.RunPolicy, error) {
	profile := ""
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	cfg, e := c.configForProfile(profile)
	if e != nil {
		return domain.RunPolicy{}, e
	}
	v := cfg.Policy
	if name := p.String("preset"); name != "" {
		pr, e := c.preset(p, profile)
		if e != nil {
			return v, e
		}
		v = pr.Policy
	}
	if e := p.Into("policy", &v); e != nil {
		return v, e
	}
	if p.Has("max_runtime") {
		v.MaxRuntime = p.String("max_runtime")
		v.Until = time.Time{}
	}
	if p.Has("until") {
		t, e := time.Parse(time.RFC3339, p.String("until"))
		if e != nil {
			return v, domain.E("INVALID_POLICY", "until must be RFC3339")
		}
		v.Until = t
		v.MaxRuntime = ""
	}
	if p.Has("cleanup") {
		v.Cleanup = p.String("cleanup")
	}
	if p.Has("auto_renew") {
		v.AutoRenew = p.Bool("auto_renew")
	}
	if v.Cleanup == "" {
		v.Cleanup = "release"
	}
	if v.StopGrace == "" {
		v.StopGrace = "60s"
	}
	if v.Cleanup != "release" && v.Cleanup != "stop" && v.Cleanup != "none" && v.Cleanup != "delete" {
		return v, domain.E("INVALID_POLICY", "cleanup must be none, stop, release or delete")
	}
	if _, e := time.ParseDuration(v.StopGrace); e != nil {
		return v, domain.E("INVALID_POLICY", "invalid stop_grace")
	}
	return v, nil
}

func (c *Controller) preset(p Params, profiles ...string) (domain.Preset, error) {
	if p.Has("preset_snapshot") {
		var v domain.Preset
		e := p.Into("preset_snapshot", &v)
		return v, e
	}
	if name := p.String("preset"); name != "" {
		profile := ""
		if len(profiles) > 0 {
			profile = profiles[0]
		}
		cfg, e := c.configForProfile(profile)
		if e != nil {
			return domain.Preset{}, e
		}
		return cfg.Preset(name)
	}
	return domain.Preset{}, nil
}

func (c *Controller) runSources(p Params, profiles ...string) map[string]string {
	out := map[string]string{}
	profile := ""
	if len(profiles) > 0 {
		profile = profiles[0]
	}
	cfg, e := c.configForProfile(profile)
	if e != nil {
		cfg = c.Configuration()
	}
	for key, value := range cfg.Sources {
		out[key] = value
	}
	var reported map[string]string
	if p.Into("config_sources", &reported) == nil && len(reported) <= 2000 {
		for key, value := range reported {
			if len(key) <= 256 && len(value) <= 2048 {
				out[key] = value
			}
		}
	}
	return out
}
func Resolve(s domain.Snapshot, target string) (domain.Instance, error) {
	if target == "" {
		return domain.Instance{}, domain.E("TARGET_REQUIRED", "supply an instance ID or unique name")
	}
	found := []domain.Instance{}
	for _, i := range s.Instances {
		if string(i.ID) == target {
			return i, nil
		}
		if i.Name == target {
			found = append(found, i)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return domain.Instance{}, domain.E("AMBIGUOUS_TARGET", "name matches multiple instances; use an ID")
	}
	return domain.Instance{}, domain.E("NOT_FOUND", "instance not found")
}
func Owned(s domain.Snapshot, i domain.Instance) error {
	if s.Identity.UID == "" {
		return domain.E("AUTH_REQUIRED", "authenticated identity is required")
	}
	if i.OwnerID == s.Identity.UID || i.UserID == s.Identity.UID {
		return nil
	}
	return domain.E("OWNERSHIP_UNVERIFIED", "instance ownership could not be verified; project membership alone does not authorize lifecycle changes")
}
func SelectNode(i domain.Instance, node string) (domain.Node, error) {
	candidates := []domain.Node{}
	for _, n := range i.Nodes {
		if node != "" && (string(n.ID) == node || n.Name == node) {
			return n, nil
		}
		if node == "" && (n.Role == "workStation" || n.Role == "workstation" || n.Role == "head") {
			candidates = append(candidates, n)
		}
	}
	if node == "" && len(i.Nodes) == 1 {
		return i.Nodes[0], nil
	}
	if len(candidates) == 1 {
		return candidates[0], nil
	}
	if len(candidates) > 1 {
		return domain.Node{}, domain.E("NODE_REQUIRED", "multiple login nodes; specify --node")
	}
	return domain.Node{}, domain.E("NODE_NOT_FOUND", "login node not found")
}
func TaskID(profile, op, key string) string {
	if key == "" {
		return uuid.NewString()
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("bupt/v1/"+profile+"/"+op+"/"+key)).String()
}
func intentHash(profile, op string, p Params) string {
	clean := Params{}
	for k, v := range p {
		switch k {
		case "wait", "no_wait", "timeout", "dry_run", "idempotency_key", "config_sources":
			continue
		}
		clean[k] = v
	}
	b, _ := json.Marshal(struct {
		Profile, Operation string
		Params             Params
	}{profile, op, clean})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func terminalTask(s string) bool {
	return s == "complete" || s == "rejected" || s == "aborted" || s == "failed" || s == "need_repeat"
}
func taskError(t domain.TaskRecord) error {
	if t.State == "complete" {
		return nil
	}
	if t.Result != nil {
		return &domain.Error{Code: fmt.Sprintf("TASK_%d", t.Result.Code), Message: t.Result.Message, TaskID: t.ID}
	}
	return &domain.Error{Code: "TASK_PENDING", Message: "task outcome is not yet confirmed", TaskID: t.ID, Retryable: true}
}
func (c *Controller) WaitTask(ctx context.Context, id string) (domain.TaskRecord, error) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		t, e := c.Store.GetTask(id)
		if e != nil {
			return t, e
		}
		if terminalTask(t.State) {
			return t, taskError(t)
		}
		if t.State == "auth_required" {
			return t, &domain.Error{Code: "AUTH_REQUIRED", Message: "task remains tracked; refresh school authentication", TaskID: id, Retryable: true}
		}
		if t.State == "uncertain" {
			return t, taskError(t)
		}
		select {
		case <-ctx.Done():
			return t, &domain.Error{Code: "TASK_PENDING", Message: "waiting ended; task remains tracked", TaskID: id, Retryable: true}
		case <-tick.C:
		}
	}
}
func (c *Controller) Instances(ctx context.Context, p string, cached bool) ([]domain.Instance, error) {
	s, e := c.snapshot(ctx, p)
	if e != nil {
		if !cached {
			return nil, e
		}
		if ce := c.Store.Get("snapshots", p, &s); ce != nil {
			return nil, e
		}
	}
	runs, _ := c.Store.ListRuns(p)
	routes, _ := c.Store.ListRoutes(p)
	for x := range s.Instances {
		for _, r := range runs {
			if r.InstanceID == s.Instances[x].ID {
				s.Instances[x].Managed = true
				s.Instances[x].RunID = r.ID
			}
		}
		for _, r := range routes {
			if r.InstanceID == s.Instances[x].ID {
				s.Instances[x].SSHState = r.State
			}
		}
	}
	sort.Slice(s.Instances, func(a, b int) bool { return s.Instances[a].Name < s.Instances[b].Name })
	return s.Instances, nil
}
