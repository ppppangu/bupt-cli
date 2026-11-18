package control

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/wire"
)

func TestAccountBoundedStateMachine(t *testing.T) {
	// Exhaust all traces of five partial/full observations and clock expiry
	// through depth six against the same pure transition used in production.
	start := time.Unix(1000, 0).UTC()
	balance := 4.0
	steps := []domain.AccountObservation{
		{VPN: "authenticated", Platform: "authenticated", CampusID: "cas_101", Name: "A", Balance: &balance},
		{VPN: "authenticated", Platform: "authenticated", CampusID: "202", Name: "B"},
		{VPN: "expired"},
		{VPN: "unknown", Platform: "unknown"},
		{VPN: "authenticated"},
	}
	var visited int
	var explore func(domain.AccountStatus, time.Time, int)
	explore = func(v domain.AccountStatus, now time.Time, depth int) {
		visited++
		if v.Platform != "authenticated" && (v.Name != "" || v.CampusID != "" || v.Balance != nil || v.Running != nil) {
			t.Fatalf("private data survived loss of verified identity: %+v", v)
		}
		if v.Platform == "blocked_by_gateway" && v.VPN != "expired" {
			t.Fatalf("gateway recovery left platform blocked: %+v", v)
		}
		if v.Platform == "authenticated" && (v.VPN != "authenticated" || !now.Before(v.ValidUntil)) {
			t.Fatalf("affirmation without current evidence: %+v", v)
		}
		if v.CampusID == "202" && (v.Balance != nil || v.Name != "B") {
			t.Fatalf("account B inherited A facts: %+v", v)
		}
		if depth == 0 {
			return
		}
		later := now.Add(time.Second)
		for _, o := range steps {
			post := reduceAccount(v, o, later)
			if o.Platform == "" && v.Platform == "authenticated" && post.Platform == "authenticated" && post.ValidUntil.After(v.ValidUntil) {
				t.Fatal("gateway-only evidence renewed a portal observation")
			}
			explore(post, later, depth-1)
		}
		expired := now.Add(accountTTL)
		explore(v.Effective(expired), expired, depth-1)
	}
	explore(domain.AccountStatus{VPN: "unknown", Platform: "unknown"}, start, 6)
	if visited != 55987 {
		t.Fatal(visited)
	}
}

type accountFixture struct {
	flowPlatform
	probes       atomic.Int32
	preparations atomic.Int32
	entered      chan struct{}
	release      chan struct{}
	observation  domain.AccountObservation
}

func (f *accountFixture) PrepareCompute(context.Context) error { f.preparations.Add(1); return nil }
func (f *accountFixture) VerifiedPortalAccount() (domain.PortalAccount, bool) {
	return domain.PortalAccount{SID: "cas_101", Name: "Student"}, f.ssoReady
}
func (f *accountFixture) ProbeAccountStatus(ctx context.Context) (domain.AccountObservation, error) {
	f.probes.Add(1)
	select {
	case f.entered <- struct{}{}:
	default:
	}
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return domain.AccountObservation{}, ctx.Err()
		}
	}
	return f.observation, nil
}
func accountController(t *testing.T, adapter *accountFixture) *Controller {
	return streamTestController(t, domain.Profile{GatewayURL: "https://school.invalid"}, func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return adapter, nil })
}

type accountReader struct {
	net.Conn
	*bufio.Reader
}

func watchForTest(t *testing.T, c *Controller, id string, live bool) *accountReader {
	t.Helper()
	s, n := net.Pipe()
	t.Cleanup(func() { n.Close(); s.Close() })
	go func() { defer s.Close(); c.serveConnection(t.Context(), s) }()
	if err := wire.WriteJSON(n, domain.RPCRequest{ID: "watch", ClientID: id, Profile: "test", Method: "account.watch", Params: domain.Result(map[string]bool{"cached": !live})}); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(n)
	n.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack domain.RPCResponse
	if err := wire.ReadJSON(r, &ack); err != nil || ack.Error != nil {
		t.Fatalf("watch ack: %v %+v", err, ack.Error)
	}
	return &accountReader{n, r}
}
func (r *accountReader) until(t *testing.T, accept func(domain.AccountStatus) bool) domain.AccountStatus {
	t.Helper()
	r.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		k, b, e := wire.ReadFrame(r.Reader)
		if e != nil || k != wire.Output {
			t.Fatalf("account frame %v: %s %v", k, b, e)
		}
		var v domain.AccountStatus
		if e := json.Unmarshal(b, &v); e != nil {
			t.Fatal(e)
		}
		if accept(v) {
			return v
		}
	}
}

func TestAccountWatchCrossTerminalLoginLogoutAndRevocation(t *testing.T) {
	adapter := &accountFixture{}
	c := accountController(t, adapter)
	if e := c.Store.Put("clients", "reader", ClientIdentity{ID: "reader", Profiles: []string{"test"}, ReadOnly: true}); e != nil {
		t.Fatal(e)
	}
	first := watchForTest(t, c, "", false)
	second := watchForTest(t, c, "reader", false)
	unknown := func(v domain.AccountStatus) bool { return v.Platform == "unknown" }
	initial := first.until(t, unknown)
	second.until(t, unknown)
	f, _ := c.account("test")
	old, err := os.Open(f.path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	rpc := func(method string) {
		t.Helper()
		_, err := c.Handle(t.Context(), domain.RPCRequest{Profile: "test", Method: method}, Params{})
		if err != nil {
			t.Fatal(method, err)
		}
	}
	rpc("auth.start")
	rpc("auth.poll")
	rpc("auth.poll")
	confirmed := func(v domain.AccountStatus) bool {
		return v.CampusID == "101" && v.Name == "Student" && v.Platform == "authenticated"
	}
	want := first.until(t, confirmed)
	other := second.until(t, confirmed)
	if !reflect.DeepEqual(want, other) || want.Epoch != initial.Epoch || want.Revision <= initial.Revision {
		t.Fatal(want, other, initial)
	}
	b, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	var fromFile domain.AccountStatus
	if err := json.Unmarshal(b, &fromFile); err != nil || !reflect.DeepEqual(fromFile, want) {
		t.Fatal(string(b), err)
	}
	var beforeRename domain.AccountStatus
	if err := json.NewDecoder(old).Decode(&beforeRename); err != nil || beforeRename.Revision != initial.Revision {
		t.Fatal("status was rewritten in place", err)
	}
	for path, mode := range map[string]os.FileMode{f.path: 0600, filepath.Dir(f.path): 0700} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != mode {
			t.Fatalf("%s: %v %v", path, st, err)
		}
	}
	rpc("auth.logout")
	loggedOut := func(v domain.AccountStatus) bool {
		return v.Platform == "expired" && v.VPN == "expired" && v.CampusID == ""
	}
	first.until(t, loggedOut)
	second.until(t, loggedOut)
	if adapter.probes.Load() != 0 {
		t.Fatal("read-only cached subscriptions made upstream requests")
	}
	if err := c.Store.Put("clients", "reader", ClientIdentity{ID: "reader", Revoked: true}); err != nil {
		t.Fatal(err)
	}
	second.SetReadDeadline(time.Now().Add(4 * time.Second))
	k, data, e := wire.ReadFrame(second.Reader)
	var denied domain.Error
	if e != nil || k != wire.Failure || json.Unmarshal(data, &denied) != nil || denied.Code != "CLIENT_REVOKED" {
		t.Fatalf("revoked subscription still authorized: %d %s %v", k, data, e)
	}
}

func TestAccountSharesProbeAndFencesLateSuccess(t *testing.T) {
	adapter := &accountFixture{entered: make(chan struct{}, 1), release: make(chan struct{}), observation: domain.AccountObservation{VPN: "authenticated", Platform: "authenticated", Name: "Old", CampusID: "101"}}
	c := accountController(t, adapter)
	f, _ := c.account("test")
	a := watchForTest(t, c, "", true)
	b := watchForTest(t, c, "", true)
	initial := func(v domain.AccountStatus) bool { return v.Platform == "unknown" }
	a.until(t, initial)
	b.until(t, initial)
	select {
	case <-adapter.entered:
	case <-time.After(time.Second):
		t.Fatal("passive check did not start")
	}
	done := make(chan struct{})
	go func() { defer close(done); _, _ = c.refreshAccount(t.Context(), f) }()
	c.accountLogout("test")
	close(adapter.release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh did not return")
	}
	a.until(t, func(v domain.AccountStatus) bool { return v.Platform == "expired" })
	b.until(t, func(v domain.AccountStatus) bool { return v.Platform == "expired" })
	v, e := f.current()
	if e != nil || v.CampusID != "" || v.Platform != "expired" || adapter.probes.Load() != 1 {
		t.Fatal(v, e, adapter.probes.Load())
	}
}

func TestAccountBudgetAndOrderedFacts(t *testing.T) {
	adapter := &accountFixture{observation: domain.AccountObservation{VPN: "unknown", Platform: "unknown", RetryAfter: 3 * time.Minute}}
	c := accountController(t, adapter)
	f, _ := c.account("test")
	_, _ = c.refreshAccount(t.Context(), f)
	f.networkChanged()
	f.mu.Lock()
	f.nextProbe = time.Now().Add(-time.Second)
	f.mu.Unlock()
	_, _ = c.refreshAccount(t.Context(), f)
	if adapter.probes.Load() != 1 {
		t.Fatal("network event bypassed Retry-After")
	}
	good := domain.AccountObservation{VPN: "authenticated", Platform: "authenticated", CampusID: "101"}
	f.ticket().observe(good, true)
	stale, denial := f.ticket(), f.ticket()
	stale.observe(good, false)
	denial.failure(domain.E("GATEWAY_AUTH_EXPIRED", "expired"), false)
	stale.observe(good, false)
	v, _ := f.current()
	if v.VPN != "expired" || v.CampusID != "" {
		t.Fatal("concurrent positive swallowed definitive denial", v)
	}
	f.ticket().observe(good, true)
	olderBalance, newerBalance := f.ticket(), f.ticket()
	changed := good
	changed.Name = "latest"
	newerBalance.observe(changed, false)
	olderBalance.observe(good, false)
	if current, _ := f.current(); current.Name != "latest" {
		t.Fatal("late account read overwrote more recent confirmation", current)
	}
	old, newer := f.ticket(), f.ticket()
	snap := domain.Snapshot{Identity: domain.Identity{UID: "7", CampusID: "101"}, Instances: []domain.Instance{{OwnerID: "7", State: "running"}, {OwnerID: "9", UserID: "7", State: "running"}}}
	newer.instances(snap)
	snap.Instances = nil
	old.instances(snap)
	v, _ = f.current()
	if v.Running == nil || *v.Running != 1 {
		t.Fatal("older or foreign running count", v)
	}
	f.mu.Lock()
	f.value.RunningValidUntil = time.Now().Add(-time.Second)
	f.mu.Unlock()
	f.ticket().observe(good, false)
	v, _ = f.current()
	if v.Running != nil || v.Platform != "authenticated" {
		t.Fatal("account probe renewed stale running count", v)
	}
	ticket := f.ticket()
	f.networkChanged()
	ticket.observe(good, true)
	v, _ = f.current()
	if v.Platform == "authenticated" {
		t.Fatal("old-network interactive response reauthenticated the new network")
	}
}

func TestAccountReadUsesOnlyPassiveAdapter(t *testing.T) {
	adapter := &accountFixture{}
	c := accountController(t, adapter)
	for _, method := range []string{"account.snapshot", "account.refresh"} {
		if _, err := c.Handle(t.Context(), domain.RPCRequest{Method: method, Profile: "test"}, params(map[string]bool{"compute": true})); err != nil {
			t.Fatal(err)
		}
	}
	if adapter.preparations.Load() != 0 || adapter.probes.Load() != 1 {
		t.Fatal("account read attempted compute login", adapter.preparations.Load(), adapter.probes.Load())
	}
}

func TestAccountReloadFenceAndPathIsolation(t *testing.T) {
	c := accountController(t, &accountFixture{})
	old, _ := c.account("test")
	before := old.ticket()
	oldDeadline := time.Now().UTC().Add(time.Hour)
	old.mu.Lock()
	old.retryAfter = oldDeadline
	old.mu.Unlock()
	next := &config.Config{Selected: "test", Profiles: map[string]domain.Profile{}}
	for _, name := range []string{"test", "Lab", "lab", "State.DB", "agent.sock", "agent.pid"} {
		next.Profiles[name] = domain.Profile{Name: name}
	}
	c.reloadAccounts(next, map[string]bool{"test": true})
	seen := map[string]bool{}
	for name := range next.Profiles {
		f, err := c.account(name)
		if err != nil || seen[f.path] {
			t.Fatalf("profile %s: %#v, %v", name, f, err)
		}
		seen[f.path] = true
	}
	f, _ := c.account("test")
	v, _ := f.current()
	before.observe(domain.AccountObservation{VPN: "authenticated", Platform: "authenticated", CampusID: "101"}, true)
	current, _ := f.current()
	if f == old || v.Epoch == old.value.Epoch || current.Platform != "unknown" || !f.retryAfter.Equal(oldDeadline) {
		t.Fatal("reload lost epoch or throttle boundary", v, current)
	}
}
