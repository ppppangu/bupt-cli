package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"bupt.local/cli/internal/client"
	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

type ClientIdentity struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	PublicKey string   `json:"public_key"`
	Profiles  []string `json:"profiles,omitempty"`
	Owner     bool     `json:"owner"`
	ReadOnly  bool     `json:"read_only,omitempty"`
	Revoked   bool     `json:"revoked,omitempty"`
}
type DeviceKey struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Profile   string    `json:"profile"`
	PublicKey string    `json:"public_key"`
	CreatedAt time.Time `json:"created_at"`
	Revoked   bool      `json:"revoked,omitempty"`
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,250}$`)

func (c *Controller) Authorize(id, method, p string) error {
	if !config.ValidProfileName(p) {
		return domain.E("INVALID_PROFILE", "profile identifier is invalid")
	}
	if id == "" {
		return nil
	}
	var identity ClientIdentity
	if c.Store.Get("clients", id, &identity) != nil || identity.Revoked {
		return domain.E("CLIENT_REVOKED", "controller client is not registered")
	}
	allowed := identity.Owner
	for _, v := range identity.Profiles {
		if v == p {
			allowed = true
		}
	}
	if !allowed {
		return domain.E("PROFILE_FORBIDDEN", "client is not allowed to manage this profile")
	}
	if !identity.Owner && (strings.HasPrefix(method, "compute.admin") || method == "portal.change" || method == "portal.export" || strings.HasPrefix(method, "client.") || method == "agent.stop" || method == "agent.reload" || method == "auth.login" || method == "auth.access" || method == "auth.start" || method == "auth.poll" || method == "auth.sso.submit" || method == "auth.probe" || method == "auth.setup" || method == "auth.logout" || strings.HasPrefix(method, "auth.gateway.")) {
		return domain.E("OWNER_REQUIRED", "this operation requires the controller owner")
	}
	if identity.ReadOnly {
		switch method {
		case "compute.read", "portal.read", "agent.status", "status.get", "resources.overview", "auth.status", "instance.list", "instance.inspect", "spec.list", "spec.show", "spec.estimate", "preset.resolve", "quota.get", "job.list", "job.get", "lease.status", "task.list", "task.get", "audit.list", "doctor":
			return nil
		}
		return domain.E("READ_ONLY_CLIENT", "client is read-only")
	}
	return nil
}
func (c *Controller) RegisterClient(v ClientIdentity) (ClientIdentity, error) {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.ID == "local" {
		return v, domain.E("INVALID_CLIENT", "local is reserved for controller-internal operations")
	}
	if !safeID.MatchString(v.ID) {
		return v, domain.E("INVALID_CLIENT", "invalid client identifier")
	}
	k, e := connect.CanonicalPublicKey(v.PublicKey)
	if e != nil {
		return v, e
	}
	v.PublicKey = k
	if e = c.Store.Put("clients", v.ID, v); e != nil {
		return v, e
	}
	if c.opts.AuthorizedKeys != "" {
		e = c.writeAuthorizedKeys()
	}
	return v, e
}
func (c *Controller) writeAuthorizedKeys() error {
	c.keysMu.Lock()
	defer c.keysMu.Unlock()
	if c.opts.AuthorizedKeys == "" {
		return domain.E("RELAY_NOT_ENABLED", "this controller is not configured as an SSH relay")
	}
	var clients []ClientIdentity
	if e := c.Store.List("clients", &clients); e != nil {
		return e
	}
	leases, e := c.Store.ListPortLeases("")
	if e != nil {
		return e
	}
	exe := c.opts.Executable
	if exe == "" {
		exe = "/usr/local/bin/bupt"
	}
	if strings.ContainsAny(exe, "\n\r\x00\t") {
		return domain.E("INVALID_EXECUTABLE", "invalid gateway executable path")
	}
	lines := []string{}
	for _, cl := range clients {
		if cl.Revoked {
			continue
		}
		if !safeID.MatchString(cl.ID) {
			return domain.E("INVALID_CLIENT", "invalid persisted client ID")
		}
		cmd := connect.QuotePOSIX(exe) + " agent gateway --client " + connect.QuotePOSIX(cl.ID) + " --socket " + connect.QuotePOSIX(filepath.Join(c.Store.Dir(), "agent.sock"))
		if c.opts.Socket != "" {
			cmd = connect.QuotePOSIX(exe) + " agent gateway --client " + connect.QuotePOSIX(cl.ID) + " --socket " + connect.QuotePOSIX(c.opts.Socket)
		}
		if strings.ContainsAny(cmd, "\n\r\x00\t") {
			return domain.E("INVALID_PATH", "relay socket path cannot be encoded in authorized_keys")
		}
		lines = append(lines, fmt.Sprintf("restrict,command=%q %s bupt-client-%s", cmd, cl.PublicKey, cl.ID))
	}
	for _, l := range leases {
		if l.PublicKey == "" || strings.HasPrefix(l.State, "revoked") {
			continue
		}
		if l.Port < 1024 || l.Port > 65535 {
			return domain.E("INVALID_PORT", "invalid persisted relay port")
		}
		key, e := connect.CanonicalPublicKey(l.PublicKey)
		if e != nil {
			return e
		}
		lines = append(lines, fmt.Sprintf("restrict,port-forwarding,permitlisten=\"127.0.0.1:%d\",permitopen=\"127.0.0.1:%d\",command=\"/usr/sbin/nologin\" %s bupt-tunnel-%s", l.Port, l.Port, key, l.Key))
	}
	sort.Strings(lines)
	start := "# BEGIN BUPT MANAGED"
	end := "# END BUPT MANAGED"
	old, _ := os.ReadFile(c.opts.AuthorizedKeys)
	text := string(old)
	begin := strings.Index(text, start)
	finish := strings.Index(text, end)
	if (begin >= 0) != (finish >= 0) || (begin >= 0 && finish < begin) {
		return domain.E("AUTH_FILE_CONFLICT", "authorized_keys managed markers are inconsistent")
	}
	block := start + "\n" + strings.Join(lines, "\n") + "\n" + end + "\n"
	if begin >= 0 {
		text = text[:begin] + block + strings.TrimPrefix(text[finish+len(end):], "\n")
	} else {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += block
	}
	directory := filepath.Dir(c.opts.AuthorizedKeys)
	if e = os.MkdirAll(directory, 0700); e != nil {
		return e
	}
	if string(old) != text && len(old) > 0 {
		backup := c.opts.AuthorizedKeys + ".before-" + time.Now().UTC().Format("20060102T150405.000000000")
		if e = os.WriteFile(backup, old, 0600); e != nil {
			return e
		}
	}
	f, e := os.CreateTemp(directory, ".bupt-authorized-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0600); e == nil {
		_, e = f.WriteString(text)
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Rename(name, c.opts.AuthorizedKeys)
}
func portAvailable(port int) bool {
	l, e := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if e != nil {
		return false
	}
	l.Close()
	return true
}
func (c *Controller) RelayReserve(owner, p string, instance, node domain.ID) (domain.PortLease, error) {
	if c.opts.AuthorizedKeys == "" {
		return domain.PortLease{}, domain.E("RELAY_NOT_ENABLED", "relay registration is not configured")
	}
	if owner == "" {
		owner = "local"
	}
	minPort, maxPort := 24000, 24999
	if prof, ok := c.Configuration().Profiles[p]; ok {
		if prof.Relay.PortMin > 0 {
			minPort = prof.Relay.PortMin
		}
		if prof.Relay.PortMax > 0 {
			maxPort = prof.Relay.PortMax
		}
	}
	key := uuid.NewSHA1(uuid.NameSpaceURL, []byte(owner+"/"+p+"/"+string(instance)+"/"+string(node))).String()
	return c.Store.AllocatePort(domain.PortLease{Key: key, Owner: owner, Profile: p, InstanceID: instance, NodeID: node}, minPort, maxPort, portAvailable)
}
func (c *Controller) RelayEnroll(owner, key, pub string) (domain.PortLease, error) {
	l, e := c.Store.GetPortLease(key)
	if e != nil {
		return l, e
	}
	if owner == "" {
		owner = "local"
	}
	if l.Owner != owner {
		return l, domain.E("LEASE_FORBIDDEN", "relay lease belongs to another controller")
	}
	pub, e = connect.CanonicalPublicKey(pub)
	if e != nil {
		return l, e
	}
	l.PublicKey = pub
	l.State = "enrolled"
	l.UpdatedAt = time.Now().UTC()
	if e = c.Store.PutPortLease(l); e != nil {
		return l, e
	}
	e = c.writeAuthorizedKeys()
	return l, e
}
func (c *Controller) RelayRevoke(owner, key string) (domain.PortLease, error) {
	l, e := c.Store.GetPortLease(key)
	if e != nil {
		return l, e
	}
	if owner == "" {
		owner = "local"
	}
	if l.Owner != owner {
		return l, domain.E("LEASE_FORBIDDEN", "relay lease belongs to another controller")
	}
	l.State = "revoked_pending_close"
	if e = c.Store.PutPortLease(l); e != nil {
		return l, e
	}
	if e = c.writeAuthorizedKeys(); e != nil {
		return l, e
	}
	if portAvailable(l.Port) {
		l.State = "revoked"
		_ = c.Store.ReleasePort(l.Key)
	}
	return l, nil
}
func (c *Controller) RelayAddress(owner, key string) (string, error) {
	l, e := c.Store.GetPortLease(key)
	if e != nil {
		return "", e
	}
	if owner == "" {
		owner = "local"
	}
	if l.Owner != owner {
		return "", domain.E("LEASE_FORBIDDEN", "relay lease belongs to another controller")
	}
	if l.State != "enrolled" && l.State != "active" {
		return "", domain.E("TUNNEL_NOT_READY", "relay lease is not active")
	}
	return fmt.Sprintf("127.0.0.1:%d", l.Port), nil
}
func (c *Controller) relayCall(ctx context.Context, p domain.Profile, method string, params any, out any) error {
	if c.opts.AuthorizedKeys != "" && p.Relay.IdentityFile == "" {
		raw, _ := json.Marshal(params)
		q, _ := Decode(raw)
		var v any
		var e error
		switch method {
		case "relay.reserve":
			v, e = c.RelayReserve("local", p.Name, q.ID("instance_id"), q.ID("node_id"))
		case "relay.enroll":
			v, e = c.RelayEnroll("local", q.String("key"), q.String("public_key"))
		case "relay.revoke":
			v, e = c.RelayRevoke("local", q.String("key"))
		default:
			return domain.E("UNKNOWN_METHOD", "unknown relay operation")
		}
		if e != nil {
			return e
		}
		b, _ := json.Marshal(v)
		return json.Unmarshal(b, out)
	}
	return client.GenericRelayRPC(ctx, p.Relay, domain.RPCRequest{ID: uuid.NewString(), Method: method, Profile: p.Name, Params: domain.Result(params)}, out)
}
func ReadBootstrapClient(path string) (ClientIdentity, error) {
	var v ClientIdentity
	f, e := os.Open(path)
	if e != nil {
		return v, e
	}
	defer f.Close()
	d := json.NewDecoder(bufio.NewReader(f))
	d.DisallowUnknownFields()
	e = d.Decode(&v)
	return v, e
}
