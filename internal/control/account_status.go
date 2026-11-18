package control

import (
	"encoding/base32"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

const accountTTL = 90 * time.Second
const accountProbeInterval = time.Minute

// Only the controller writes this private projection. Filesystem notifications
// are hints to reread its latest complete revision, never authorization inputs.
type accountFeeds struct {
	mu        sync.Mutex
	closed    bool
	byProfile map[string]*accountFeed
}

type accountFeed struct {
	mu                                sync.Mutex
	path                              string
	value                             domain.AccountStatus
	direct                            bool
	generation, scope                 uint64
	issued, lastInstances, lastPortal uint64
	signedOut, dirty, closed          bool
	expiry                            *time.Timer
	live                              int
	stop                              func()
	nextProbe, retryAfter             time.Time
	network                           string
	networkChecked                    time.Time
	inFlight                          chan struct{}
	work                              sync.WaitGroup
}

func (c *Controller) account(profile string) (*accountFeed, error) {
	if !config.ValidProfileName(profile) {
		return nil, domain.E("INVALID_PROFILE", "invalid account profile")
	}
	a := &c.accounts
	a.mu.Lock()
	defer a.mu.Unlock()
	return c.accountLocked(profile)
}

func (c *Controller) accountLocked(profile string) (*accountFeed, error) {
	a := &c.accounts
	cfg, ok := c.Configuration().Profiles[profile]
	if !ok {
		return nil, domain.E("PROFILE_NOT_FOUND", "unknown profile")
	}
	if a.closed {
		return nil, domain.E("AGENT_STOPPING", "controller is stopping")
	}
	if f := a.byProfile[profile]; f != nil {
		return f, nil
	}
	dir := c.accountDirectory(c.Configuration(), profile)
	if e := os.Mkdir(dir, 0700); e != nil && !errors.Is(e, os.ErrExist) {
		return nil, e
	}
	st, e := os.Lstat(dir)
	if e != nil {
		return nil, e
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return nil, domain.E("INSECURE_STATUS_DIRECTORY", "account status requires a private directory")
	}
	vpn := "unknown"
	if cfg.GatewayURL == "" {
		vpn = "not_configured"
	}
	f := &accountFeed{direct: cfg.GatewayURL == "", path: filepath.Join(dir, "status.json"), dirty: true, value: domain.AccountStatus{Epoch: uuid.NewString(), Revision: 1, Profile: profile, VPN: vpn, Platform: "unknown"}}
	if err := f.flushLocked(); err != nil {
		return nil, err
	}
	if a.byProfile == nil {
		a.byProfile = make(map[string]*accountFeed)
	}
	a.byProfile[profile] = f
	return f, nil
}

// Existing profile names can collide with controller files or with one
// another on case-insensitive volumes. The escaped component cannot itself
// be a valid profile name; lowercase base32 makes the mapping reversible.
func (c *Controller) accountDirectory(cfg *config.Config, profile string) string {
	dir := filepath.Join(c.Store.Dir(), profile)
	escaped := strings.EqualFold(dir, cfg.Paths.Socket) || strings.EqualFold(dir, c.opts.Socket)
	for _, name := range []string{"state.db", "agent.pid", "agent.sock", "agent.log", "service.json", "captchas"} {
		escaped = escaped || strings.EqualFold(profile, name)
	}
	for other := range cfg.Profiles {
		if other != profile && strings.EqualFold(other, profile) {
			escaped = true
			break
		}
	}
	if escaped {
		name := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(profile))
		return filepath.Join(c.Store.Dir(), "@"+strings.ToLower(name))
	}
	return dir
}

func (f *accountFeed) flushLocked() error {
	if !f.dirty {
		return nil
	}
	data, err := json.MarshalIndent(f.value, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(f.path)
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return domain.E("INSECURE_STATUS_DIRECTORY", "account status directory is not private")
	}
	tmp, err := os.CreateTemp(dir, ".status-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	closed := tmp.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	if err = os.Rename(tmp.Name(), f.path); err != nil {
		return err
	}
	f.dirty = false
	return nil
}

func (f *accountFeed) setLocked(next domain.AccountStatus) {
	next = next.Effective(time.Now())
	if next.Running == nil {
		next.RunningValidUntil = time.Time{}
	}
	if !reflect.DeepEqual(f.value, next) {
		next.Revision++
		f.value = next
		f.dirty = true
	}
	_ = f.flushLocked() // a failed publication stays dirty until the next read or update
}

func (f *accountFeed) current() (domain.AccountStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return domain.AccountStatus{}, domain.E("PROFILE_RELOADED", "controller account subscription has closed")
	}
	f.setLocked(f.value.Effective(time.Now()))
	return f.value, f.flushLocked()
}

// A ticket fences both a slow read and a replaced profile/controller. It is
// captured before network I/O; a newer login or logout invalidates old reads.
type accountTicket struct {
	feed                        *accountFeed
	generation, scope, sequence uint64
}

func (c *Controller) accountTicket(profile string) accountTicket {
	f, e := c.account(profile)
	if e != nil {
		return accountTicket{}
	}
	return f.ticket()
}
func (f *accountFeed) ticket() accountTicket {
	if f == nil {
		return accountTicket{}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ticketLocked()
}
func (f *accountFeed) ticketLocked() accountTicket {
	f.issued++
	return accountTicket{f, f.generation, f.scope, f.issued}
}

// Reduce a partial observation. A gateway denial dominates the projected
// platform state, and identity-bound fields live only with a verified subject.
func reduceAccount(old domain.AccountStatus, o domain.AccountObservation, now time.Time) domain.AccountStatus {
	v := old.Effective(now)
	if o.VPN != "" {
		v.VPN = o.VPN
	}
	if o.Platform != "" {
		v.Platform = o.Platform
		id := domain.CampusNumber(o.CampusID)
		if id == "" || v.CampusID != id {
			v.Running = nil
		}
		v.CampusID, v.Name, v.Balance = id, o.Name, o.Balance
		v.ConsentRequired = o.ConsentRequired
	}
	switch v.VPN {
	case "authenticated":
		if v.Platform == "blocked_by_gateway" {
			v.Platform = "unknown"
		}
	case "expired":
		v.Platform = "blocked_by_gateway"
	case "unknown":
		if v.Platform == "authenticated" {
			v.Platform = "unknown"
		}
	}
	if v.Platform != "authenticated" {
		v.Name, v.CampusID, v.Balance, v.Running = "", "", nil, nil
		v.ConsentRequired = false
	}
	if v.VPN == "authenticated" || v.Platform == "authenticated" {
		// A partial check cannot extend the lifetime of an unobserved layer.
		untouched := (o.VPN == "" && v.VPN == "authenticated") || (o.Platform == "" && v.Platform == "authenticated")
		if !untouched {
			v.ValidUntil = now.Add(accountTTL)
		}
	} else {
		v.ValidUntil = time.Time{}
	}
	return v
}

func (t accountTicket) observe(o domain.AccountObservation, interactive bool) {
	f := t.feed
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || t.scope != f.scope || (!interactive && (t.generation != f.generation || f.signedOut)) {
		return
	}
	if !interactive && o.Platform == "authenticated" && t.sequence < f.lastPortal {
		return
	}
	if o.Platform == "authenticated" {
		f.lastPortal = t.sequence
	}
	if interactive {
		f.signedOut = false
	}
	next := reduceAccount(f.value, o, time.Now().UTC())
	// Credentials, network changes and negative evidence fence outstanding
	// reads. Refreshing the same subject cannot suppress a concurrent denial.
	if interactive || next.Platform != "authenticated" || f.value.CampusID != "" && next.CampusID != f.value.CampusID {
		f.generation++
	}
	f.setLocked(next)
	f.scheduleExpiryLocked()
}
func (f *accountFeed) scheduleExpiryLocked() {
	if f.expiry != nil {
		f.expiry.Stop()
		f.expiry = nil
	}
	deadline := f.value.NextExpiry()
	if !deadline.IsZero() {
		f.expiry = time.AfterFunc(time.Until(deadline), func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			if !f.closed {
				f.setLocked(f.value.Effective(time.Now()))
				f.scheduleExpiryLocked()
			}
		})
	}
}

func (t accountTicket) observePortal(a domain.PortalAccount, interactive bool) {
	if t.feed == nil {
		return
	}
	vpn := "authenticated"
	if t.feed.direct {
		vpn = "not_configured"
	}
	t.observe(domain.AccountObservation{VPN: vpn, Platform: "authenticated", CampusID: a.SID, Name: a.Name, Balance: a.Balance, ConsentRequired: a.ConsentRequired}, interactive)
}

// A completed login can renew the compute ticket while the portal remains
// authenticated. Publish this event separately from periodic observations.
func (t accountTicket) loginCompleted() {
	f := t.feed
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || f.signedOut || t.scope != f.scope {
		return
	}
	v := f.value
	v.LoginRevision++
	f.setLocked(v)
}
func (t accountTicket) failure(e error, portal bool) {
	switch managedCode(e) {
	case "GATEWAY_AUTH_EXPIRED":
		t.observe(domain.AccountObservation{VPN: "expired", Platform: "blocked_by_gateway"}, false)
	case "AUTH_EXPIRED", "AUTH_REQUIRED":
		if portal {
			t.observe(domain.AccountObservation{Platform: "expired"}, false)
		}
	case "IDENTITY_MISMATCH":
		t.observe(domain.AccountObservation{Platform: "unknown"}, false)
	}
}

func (t accountTicket) instances(s domain.Snapshot) {
	f := t.feed
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.value.Effective(time.Now())
	if f.closed || f.generation != t.generation || t.sequence <= f.lastInstances || v.Platform != "authenticated" || v.CampusID == "" || v.CampusID != domain.CampusNumber(s.Identity.CampusID) || s.Identity.UID == "" {
		return
	}
	n := 0
	for _, i := range s.Instances {
		if i.State == "running" && (i.OwnerID == s.Identity.UID || i.OwnerID == "" && i.UserID == s.Identity.UID) {
			n++
		}
	}
	f.lastInstances = t.sequence
	v.Running = &n
	v.RunningValidUntil = time.Now().UTC().Add(accountTTL)
	f.setLocked(v)
	f.scheduleExpiryLocked()
}
func (c *Controller) accountLogout(profile string) {
	f, e := c.account(profile)
	if e != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generation++
	f.scope++
	f.signedOut = true
	vpn := "expired"
	if c.Configuration().Profiles[profile].GatewayURL == "" {
		vpn = "not_configured"
	}
	v := reduceAccount(f.value, domain.AccountObservation{VPN: vpn, Platform: "expired"}, time.Now())
	v.Platform = "expired"
	f.setLocked(v)
	f.scheduleExpiryLocked()
}
func (f *accountFeed) close() {
	f.mu.Lock()
	f.closed = true
	f.generation++
	if f.stop != nil {
		f.stop()
	}
	if f.expiry != nil {
		f.expiry.Stop()
	}
	v := f.value
	v.Platform = "unknown"
	if v.VPN != "not_configured" {
		v.VPN = "unknown"
	}
	v.ValidUntil = time.Time{}
	v.Balance, v.Running, v.Name, v.CampusID = nil, nil, "", ""
	v.ConsentRequired = false
	f.setLocked(v)
	f.mu.Unlock()
	f.work.Wait()
}
func (c *Controller) closeAccounts() {
	a := &c.accounts
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	for _, f := range a.byProfile {
		f.close()
	}
}
func (c *Controller) reloadAccounts(next *config.Config, changed map[string]bool) {
	a := &c.accounts
	a.mu.Lock()
	defer a.mu.Unlock()
	type limits struct{ next, retry time.Time }
	budgets := make(map[string]limits)
	for p, f := range a.byProfile {
		if !changed[p] && filepath.Dir(f.path) == c.accountDirectory(next, p) {
			continue
		}
		f.close()
		budgets[p] = limits{f.nextProbe, f.retryAfter}
		delete(a.byProfile, p)
	}
	c.mu.Lock()
	for name := range changed {
		if api := c.apis[name]; api != nil {
			_ = api.Close()
			delete(c.apis, name)
		}
	}
	c.configuration.Store(next)
	c.mu.Unlock()
	for p := range next.Profiles {
		f, err := c.accountLocked(p)
		if budget, ok := budgets[p]; err == nil && ok {
			f.nextProbe, f.retryAfter = budget.next, budget.retry
		}
	}
}
