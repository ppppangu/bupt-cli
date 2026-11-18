package control

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
	"bupt.local/cli/internal/wire"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type corePlatform struct {
	mu       sync.Mutex
	calls    int
	requests []domain.TaskRequest
	stale    bool
}

func (a *corePlatform) WhoAmI(context.Context) (domain.Identity, error) {
	return domain.Identity{UID: "7", Username: "student", Email: "student@example.edu"}, nil
}
func (a *corePlatform) Login(ctx context.Context, _ string, _ string) (domain.Identity, error) {
	return a.WhoAmI(ctx)
}
func (a *corePlatform) AuthStatus(context.Context) domain.AuthStatus {
	return domain.AuthStatus{Gateway: "expired", Platform: "expired"}
}
func (a *corePlatform) GatewayStart(context.Context) (domain.AuthChallenge, error) {
	return domain.AuthChallenge{}, domain.E("UNSUPPORTED", "fixture")
}
func (a *corePlatform) GatewayPoll(context.Context) (domain.AuthChallenge, error) {
	return domain.AuthChallenge{}, domain.E("UNSUPPORTED", "fixture")
}
func (a *corePlatform) Logout(context.Context) error                        { return nil }
func (a *corePlatform) SetupPassword(context.Context, string, string) error { return nil }
func (a *corePlatform) Config(context.Context) (domain.PlatformConfig, error) {
	return domain.PlatformConfig{}, nil
}
func (a *corePlatform) Specs(context.Context) ([]domain.Spec, error) { return nil, nil }
func (a *corePlatform) Spec(context.Context, domain.ID) (domain.Spec, error) {
	return domain.Spec{ID: "9", Options: map[string]any{"resourceConstraints": `{"900":{"role":"workStation","constraints":[{"type":"CPU","value":"2","options":"2,4"},{"type":"Memory","value":"6144","options":"6144,12288"},{"type":"GPU","value":"0.1","options":"0.1,0.2"}]}}`}}, nil
}
func (a *corePlatform) Estimate(context.Context, domain.ID, map[string]any) (map[string]any, error) {
	return nil, nil
}
func (a *corePlatform) Sync(ctx context.Context) (domain.Snapshot, error) {
	a.mu.Lock()
	a.calls++
	idx := a.calls
	a.mu.Unlock()
	identity, _ := a.WhoAmI(ctx)
	return domain.Snapshot{Index: int64(idx), Identity: identity, Instances: []domain.Instance{{ID: "20", Name: "existing", OwnerID: "7", UserID: "7", Index: "1", State: "stopped", DurationSeconds: 10800}}}, nil
}
func (a *corePlatform) Submit(_ context.Context, r domain.TaskRequest) (domain.TaskResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.requests = append(a.requests, r)
	if a.stale && len(a.requests) == 1 {
		return domain.TaskResult{ID: r.ID, State: "need_repeat", Code: 1165}, domain.E("TASK_1165", "stale view")
	}
	return domain.TaskResult{ID: r.ID, State: "complete", InstanceID: "21"}, nil
}
func (a *corePlatform) Console(context.Context, domain.ID) (io.ReadWriteCloser, error) {
	return nil, domain.E("UNSUPPORTED", "fixture")
}
func (a *corePlatform) Close() error { return nil }

type coreVault struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (v *coreVault) Get(k string, out any) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.values[k]
	if !ok {
		return os.ErrNotExist
	}
	return json.Unmarshal(b, out)
}
func (v *coreVault) Put(k string, value any) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, e := json.Marshal(value)
	if e == nil {
		v.values[k] = b
	}
	return e
}
func (v *coreVault) Delete(k string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.values, k)
	return nil
}

func coreController(t *testing.T) (*Controller, *corePlatform) {
	t.Helper()
	dir, e := os.MkdirTemp("/tmp", "bupt-core-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg, e := config.Load(config.Options{HomeDir: dir, NoProject: true, Env: map[string]string{}})
	if e != nil {
		t.Fatal(e)
	}
	store, e := state.Open(cfg.Paths.StateDir)
	if e != nil {
		t.Fatal(e)
	}
	api := &corePlatform{}
	c, e := New(Options{Config: cfg, Store: store, Vault: &coreVault{values: map[string][]byte{}}, Factory: func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return api, nil }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close(); store.Close() })
	return c, api
}
func params(v any) Params { p, _ := Decode(domain.Result(v)); return p }

func TestTaskIdempotencyAndDefinitiveStaleRetry(t *testing.T) {
	c, a := coreController(t)
	a.stale = true
	p := params(map[string]any{"target": "20", "idempotency_key": "request-123"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v, e := c.Operation(ctx, "bupt", "start", p)
	if e != nil {
		t.Fatal(e)
	}
	record := v.(domain.TaskRecord)
	if len(record.AttemptIDs) != 1 {
		t.Fatal("stale rejection needs a tracked new attempt")
	}
	again, e := c.Operation(ctx, "bupt", "start", p)
	if e != nil {
		t.Fatal(e)
	}
	if again.(domain.TaskRecord).ID != record.ID {
		t.Fatal("idempotency identity changed")
	}
	a.mu.Lock()
	count := len(a.requests)
	a.mu.Unlock()
	if count != 2 {
		t.Fatalf("expected 2 wire tasks, got %d", count)
	}
	stored, e := c.Store.GetTask(record.ID)
	if e != nil || stored.Request.ID != record.ID {
		t.Fatal("original wire intent changed")
	}
	p.Set("duration", 123)
	if _, e = c.Operation(ctx, "bupt", "start", p); e == nil {
		t.Fatal("conflicting intent accepted")
	}
}
func TestConcurrentDuplicateTaskSubmitsOnce(t *testing.T) {
	c, a := coreController(t)
	p := params(map[string]any{"target": "20", "idempotency_key": "same"})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := c.Operation(context.Background(), "bupt", "start", p); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.requests) != 1 {
		t.Fatalf("submitted %d duplicates", len(a.requests))
	}
}
func TestCachedRoutesNeverTouchSchoolAuthentication(t *testing.T) {
	c, a := coreController(t)
	route := domain.Route{Profile: "bupt", Name: "gpu", InstanceID: "20", NodeID: "30", Kind: "direct", Host: "127.0.0.1", Port: 22, State: "ready"}
	if e := c.Store.PutRoute(route); e != nil {
		t.Fatal(e)
	}
	v, e := c.Handle(context.Background(), domain.RPCRequest{Method: "route.get", Profile: "bupt"}, params(map[string]any{"target": "gpu"}))
	if e != nil {
		t.Fatal(e)
	}
	if v.(domain.Route).InstanceID != "20" {
		t.Fatal("wrong route")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.calls != 0 {
		t.Fatal("SSH route consulted expired auth")
	}
}
func TestResourceBundleMapsIDsAndMiB(t *testing.T) {
	c, _ := coreController(t)
	result, e := c.resourceConstraints(context.Background(), "bupt", "9", nil, map[string]any{"memory": "12GiB"})
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"type":"CPU","value":"4"`) || !strings.Contains(string(encoded), `"900"`) {
		t.Fatalf("wrong template bundle: %s", encoded)
	}
	if _, e = c.resourceConstraints(context.Background(), "bupt", "9", nil, map[string]any{"cpu": "2", "memory": "12GiB"}); e == nil {
		t.Fatal("incompatible bundle accepted")
	}
}
func TestGatewayReplacesSpoofedIdentity(t *testing.T) {
	c, a := coreController(t)
	pub, _, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	key, _ := ssh.NewPublicKey(pub)
	if _, e = c.RegisterClient(ClientIdentity{ID: "reader", Profiles: []string{"bupt"}, PublicKey: string(ssh.MarshalAuthorizedKey(key)), ReadOnly: true}); e != nil {
		t.Fatal(e)
	}
	socket := filepath.Join(filepath.Dir(c.Store.Dir()), "test.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Serve(ctx, socket) }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, e = os.Stat(socket); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket not ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
	raw := string(domain.Result(domain.RPCRequest{Method: "auth.setup", Profile: "bupt", ClientID: "local", Params: domain.Result(map[string]string{"password": "fixture"})})) + "\n"
	var output strings.Builder
	if e = Gateway(context.Background(), socket, "reader", "rpc", strings.NewReader(raw), &output); e != nil {
		t.Fatal(e)
	}
	var response domain.RPCResponse
	if e = json.Unmarshal([]byte(output.String()), &response); e != nil {
		t.Fatal(e)
	}
	if response.Error == nil || response.Error.Code != "OWNER_REQUIRED" {
		t.Fatalf("spoofed identity allowed: %s", output.String())
	}
	a.mu.Lock()
	calls := a.calls
	a.mu.Unlock()
	if calls != 0 {
		t.Fatal("unauthorized request reached platform")
	}
	cancel()
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not shut down")
	}
}
func TestConsoleSendsPasswordOnlyAfterLoginChallenge(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	errc := make(chan error, 1)
	go func() {
		r := bufio.NewReader(remote)
		if _, e := r.ReadString('\r'); e != nil {
			errc <- e
			return
		}
		io.WriteString(remote, "node login: ")
		user, e := r.ReadString('\r')
		if e != nil || user != "student\r" {
			errc <- domain.E("TEST", "wrong user")
			return
		}
		io.WriteString(remote, "Password: ")
		password, e := r.ReadString('\r')
		if e != nil || password != "test-password\r" {
			errc <- domain.E("TEST", "wrong password")
			return
		}
		io.WriteString(remote, "\r\nstudent@node:~$ ")
		errc <- nil
	}()
	if e := AuthenticateConsole(context.Background(), local, "student", "test-password"); e != nil {
		t.Fatal(e)
	}
	if e := <-errc; e != nil {
		t.Fatal(e)
	}
}
func TestRPCReadLimit(t *testing.T) {
	var out any
	if e := wire.ReadJSON(bufio.NewReader(strings.NewReader("[]\n")), &out); e != nil {
		t.Fatal(e)
	}
}

func TestExpiredPreparedAllocationIsNeverSubmitted(t *testing.T) {
	c, a := coreController(t)
	id := uuid.NewString()
	now := time.Now().UTC()
	run := domain.Run{ID: id, Profile: "bupt", State: "starting", Phase: "activate", CreatedAt: now.Add(-time.Hour), UpdatedAt: now, Deadline: now.Add(-time.Minute), Policy: domain.RunPolicy{MaxRuntime: "1m", Cleanup: "release", StopGrace: "60s"}}
	if e := c.Store.PutRun(run); e != nil {
		t.Fatal(e)
	}
	p := params(map[string]any{"target": "20", "idempotency_key": id + "/start"})
	task, e := c.prepareTask(context.Background(), "bupt", "start", p)
	if e != nil {
		t.Fatal(e)
	}
	c.resumeTask(task)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, e := c.WaitTask(ctx, task.ID)
	if e == nil || result.State != "aborted" {
		t.Fatalf("expired task did not abort: %+v %v", result, e)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.requests) != 0 {
		t.Fatal("expired allocation was sent")
	}
}
func TestLeaseACLUsesStoredProfileAndLocalIDIsReserved(t *testing.T) {
	c, _ := coreController(t)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	key, _ := ssh.NewPublicKey(pub)
	canonical := string(ssh.MarshalAuthorizedKey(key))
	if _, e := c.RegisterClient(ClientIdentity{ID: "local", Owner: true, PublicKey: canonical}); e == nil {
		t.Fatal("reserved identity accepted")
	}
	if _, e := c.RegisterClient(ClientIdentity{ID: "scoped", Profiles: []string{"bupt"}, PublicKey: canonical}); e != nil {
		t.Fatal(e)
	}
	lease, e := c.Store.AllocatePort(domain.PortLease{Key: "profile-lease", Owner: "scoped", Profile: "other", InstanceID: "20", NodeID: "30"}, 32000, 32000, func(int) bool { return true })
	if e != nil {
		t.Fatal(e)
	}
	req := domain.RPCRequest{Method: "relay.enroll", ClientID: "scoped", Profile: "bupt"}
	_, e = c.Handle(context.Background(), req, params(map[string]string{"key": lease.Key, "public_key": canonical}))
	if e == nil {
		t.Fatal("caller selected profile bypassed stored lease scope")
	}
	if e := c.Authorize("local", "agent.stop", "bupt"); e == nil {
		t.Fatal("reserved remote identity bypassed authorization")
	}
}

func TestReloadAddsProfilesWithoutChangingRunPolicy(t *testing.T) {
	c, _ := coreController(t)
	now := time.Now().UTC()
	r := domain.Run{ID: uuid.NewString(), Profile: "bupt", State: "ready", Phase: "active", CreatedAt: now, UpdatedAt: now, Deadline: now.Add(time.Hour), Policy: domain.RunPolicy{MaxRuntime: "1h", Cleanup: "release"}}
	if e := c.Store.PutRun(r); e != nil {
		t.Fatal(e)
	}
	file := c.Configuration().Paths.ConfigFile
	if e := config.SetFile(file, "policy.max_runtime", "2h"); e != nil {
		t.Fatal(e)
	}
	if e := config.SetFile(file, "profiles.second", map[string]any{"controller": "local", "platform_url": "https://gpuhub.bupt.edu.cn", "gateway_url": "https://webvpn.bupt.edu.cn"}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.ReloadConfiguration(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, ok := c.Configuration().Profiles["second"]; !ok {
		t.Fatal("new profile not loaded")
	}
	stored, e := c.Store.GetRun(r.ID)
	if e != nil || stored.Policy.MaxRuntime != "1h" || !stored.Deadline.Equal(r.Deadline) {
		t.Fatal("live policy changed on reload")
	}
	if e := config.SetFile(file, "profiles.bupt.platform_url", "https://other.example.edu"); e != nil {
		t.Fatal(e)
	}
	if _, e := c.ReloadConfiguration(context.Background()); e == nil {
		t.Fatal("live profile endpoint was replaced")
	}
}

func TestPolicyUpdateRequiresExplicitImmediateStopAndSwitchesLimitMode(t *testing.T) {
	c, _ := coreController(t)
	now := time.Now().UTC()
	r := domain.Run{ID: uuid.NewString(), Profile: "bupt", State: "ready", Phase: "active", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now, Deadline: now.Add(time.Hour), PolicyRevision: 1, Policy: domain.RunPolicy{Until: now.Add(time.Hour), Cleanup: "release", StopGrace: "60s"}}
	if e := c.Store.PutRun(r); e != nil {
		t.Fatal(e)
	}
	patch := params(map[string]any{"target": r.ID, "policy": map[string]any{"max_runtime": "1h", "until": nil}})
	if _, e := c.UpdateRunPolicy(context.Background(), "bupt", patch); e == nil {
		t.Fatal("elapsed deadline silently initiated cleanup")
	}
	stored, _ := c.Store.GetRun(r.ID)
	if stored.PolicyRevision != 1 {
		t.Fatal("rejected policy mutated state")
	}
	patch.Set("dry_run", true)
	value, e := c.UpdateRunPolicy(context.Background(), "bupt", patch)
	if e != nil {
		t.Fatal(e)
	}
	preview := value.(domain.Run)
	if !preview.Policy.Until.IsZero() || preview.Policy.MaxRuntime != "1h" || preview.Deadline.After(now) {
		t.Fatal("partial policy did not clear the previous absolute limit")
	}
	stored, _ = c.Store.GetRun(r.ID)
	if stored.PolicyRevision != 1 {
		t.Fatal("preview persisted an elapsed deadline")
	}
	patch.Set("until", now.Add(time.Hour).Format(time.RFC3339))
	if _, e = c.UpdateRunPolicy(context.Background(), "bupt", patch); e == nil {
		t.Fatal("conflicting limit modes accepted")
	}
}

func TestPublicProjectionOmitsPrivateIntentHashAndPreservesIntegers(t *testing.T) {
	data := publicResult(map[string]any{"run": domain.Run{ID: "run", IntentHash: "private-command-digest"}, "exact": json.Number("9007199254740995")})
	if strings.Contains(string(data), "intent_hash") || strings.Contains(string(data), "private-command-digest") {
		t.Fatal("private intent digest exposed")
	}
	if !strings.Contains(string(data), "9007199254740995") {
		t.Fatal("integer precision was lost")
	}
}

func TestUnverifiedCachedRouteCannotBecomeReadyByReuse(t *testing.T) {
	c, _ := coreController(t)
	for _, status := range []string{"preparing", "unreachable", "stopped"} {
		route := domain.Route{Profile: "bupt", Name: "existing", InstanceID: "20", NodeID: "30", State: status, Kind: "direct", Host: "127.0.0.1", Port: 22}
		if e := c.Store.PutRoute(route); e != nil {
			t.Fatal(e)
		}
		if _, e := c.EnsureSSH(context.Background(), "bupt", "20", "", false); e == nil {
			t.Fatalf("%s route was treated as SSH ready", status)
		}
	}
}

func TestMembershipAloneDoesNotAuthorizeOtherOwners(t *testing.T) {
	snapshot := domain.Snapshot{Identity: domain.Identity{UID: "1", Groups: []domain.ID{"7"}, Memberships: []domain.ProjectMembership{{GroupID: "7", Role: "member"}}}}
	if e := Owned(snapshot, domain.Instance{ID: "10", OwnerID: "2", UserID: "2", GroupID: "7"}); e == nil {
		t.Fatal("project membership authorized another user's instance")
	}
	c, _ := coreController(t)
	if e := c.Authorize("", "key.import", "../other"); e == nil {
		t.Fatal("unsafe profile path was accepted")
	}
}

func TestAlternateSessionProbeRequiresControllerOwner(t *testing.T) {
	c, _ := coreController(t)
	if e := c.Store.Put("clients", "ci", ClientIdentity{ID: "ci", Profiles: []string{"bupt"}}); e != nil {
		t.Fatal(e)
	}
	if e := c.Authorize("ci", "auth.probe", "bupt"); e == nil {
		t.Fatal("scoped client could probe owner session via another IP")
	}
	if e := c.Store.Put("clients", "owner", ClientIdentity{ID: "owner", Owner: true}); e != nil {
		t.Fatal(e)
	}
	if e := c.Authorize("owner", "auth.probe", "bupt"); e != nil {
		t.Fatal(e)
	}
}

func TestProjectDetailPrefersEffectiveMembership(t *testing.T) {
	roles := []domain.ProjectMembership{{GroupID: "7", Role: "admin", State: "disabled"}, {GroupID: "8", Role: "admin", State: "active"}, {GroupID: "7", Role: "member", State: "active"}, {GroupID: "7", Role: "manager", State: "active"}}
	m, ok := projectMembership(roles, "7")
	if !ok || m.Role != "manager" {
		t.Fatal(m, ok)
	}
	if _, ok := projectMembership(roles, "9"); ok {
		t.Fatal("unrelated project selected")
	}
}
