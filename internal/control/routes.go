package control

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
	"golang.org/x/crypto/ssh"
)

func instanceSSHState(i domain.Instance, routes []domain.Route) string {
	if instanceStopped(i) {
		return "stopped"
	}
	result := i.SSHState
	for _, route := range routes {
		if route.InstanceID == i.ID {
			result = route.State
		}
	}
	return result
}

func (c *Controller) CachedRoute(profile, target, node string) (domain.Route, error) {
	routes, e := c.Store.ListRoutes(profile)
	if e != nil {
		return domain.Route{}, e
	}
	found := []domain.Route{}
	aliasIDs := map[domain.ID]bool{}
	runs, _ := c.Store.ListRuns(profile)
	activeAliases := map[domain.ID]bool{}
	for _, run := range runs {
		if run.Name == target && run.InstanceID != "" {
			aliasIDs[run.InstanceID] = true
			if !managedRunTerminal(run.State) {
				activeAliases[run.InstanceID] = true
			}
		}
	}
	if len(activeAliases) > 0 {
		aliasIDs = activeAliases
	}
	for _, r := range routes {
		if string(r.InstanceID) == target || r.Name == target || aliasIDs[r.InstanceID] {
			if node == "" || string(r.NodeID) == node {
				if aliasIDs[r.InstanceID] {
					r.Name = target
				}
				found = append(found, r)
			}
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	if len(found) > 1 {
		return domain.Route{}, domain.E("AMBIGUOUS_TARGET", "cached target has multiple nodes; specify a node")
	}
	return domain.Route{}, domain.E("SSH_NOT_PREPARED", "no verified SSH route; run bupt compute instance prepare-ssh while platform authentication is available")
}
func (c *Controller) automationKey(profile string) (string, string, error) {
	p := filepath.Join(c.Configuration().Paths.DataDir, "keys", profile+"-automation")
	pub, e := connect.EnsureIdentity(p, "bupt-agent-"+profile)
	return p, pub, e
}
func (c *Controller) publicKeys(profile, agent string) []string {
	out := []string{agent}
	var keys []DeviceKey
	_ = c.Store.List("keys", &keys)
	for _, k := range keys {
		if !k.Revoked && k.Profile == profile {
			out = append(out, k.PublicKey)
		}
	}
	return out
}
func findConsole(n domain.Node) (domain.ID, error) {
	for _, s := range n.Services {
		if strings.EqualFold(s.UIType, "console") || strings.EqualFold(s.Name, "console") {
			return s.ID, nil
		}
	}
	return "", domain.E("CONSOLE_UNAVAILABLE", "node has no console service")
}
func findSSH(n domain.Node) (domain.Service, error) {
	for _, s := range n.Services {
		if strings.EqualFold(s.Name, "ssh") || strings.EqualFold(s.UIType, "ssh") {
			return s, nil
		}
	}
	return domain.Service{}, domain.E("SSH_SERVICE_UNAVAILABLE", "node has no SSH service")
}
func (c *Controller) EnsureSSH(ctx context.Context, profile, target, node string, repair bool) (domain.Route, error) {
	if !repair {
		if r, e := c.CachedRoute(profile, target, node); e == nil && r.State == "ready" {
			return r, nil
		}
	}
	p, ok := c.Configuration().Profiles[profile]
	if !ok {
		return domain.Route{}, domain.E("PROFILE_NOT_FOUND", "profile is not configured")
	}
	s, e := c.snapshot(ctx, profile)
	if e != nil {
		return domain.Route{}, e
	}
	i, e := Resolve(s, target)
	if e != nil {
		return domain.Route{}, e
	}
	if e = Owned(s, i); e != nil {
		return domain.Route{}, e
	}
	if i.State != "running" {
		return domain.Route{}, domain.E("INSTANCE_NOT_RUNNING", "start the instance explicitly before preparing SSH")
	}
	n, e := SelectNode(i, node)
	if e != nil {
		return domain.Route{}, e
	}
	consoleID, e := findConsole(n)
	if e != nil {
		return domain.Route{}, e
	}
	svc, e := findSSH(n)
	if e != nil {
		return domain.Route{}, e
	}
	key, pub, e := c.automationKey(profile)
	if e != nil {
		return domain.Route{}, e
	}
	api, e := c.api(profile)
	if e != nil {
		return domain.Route{}, e
	}
	co, e := api.Console(ctx, consoleID)
	if e != nil {
		return domain.Route{}, e
	}
	defer co.Close()
	user := n.Username
	if user == "" {
		user = s.Identity.Username
	}
	var credentials struct {
		Password string `json:"password"`
	}
	if c.Vault != nil {
		if c.Vault.Get("platform/"+profile+"/instance_credentials", &credentials) != nil {
			_ = c.Vault.Get("platform/"+profile+"/credentials", &credentials)
		}
	}
	if e = AuthenticateConsole(ctx, co, user, credentials.Password); e != nil {
		return domain.Route{}, e
	}
	credentials.Password = ""
	prepared, e := connect.Prepare(ctx, co, connect.BootstrapRequest{Profile: profile, InstanceID: i.ID, NodeID: n.ID, ExpectedUsername: user, PublicKeys: c.publicKeys(profile, pub), RevokedKeys: c.revokedKeys(profile), SSHPort: svc.InnerPort})
	if e != nil {
		return domain.Route{}, e
	}
	r := domain.Route{Profile: profile, Name: i.Name, InstanceID: i.ID, NodeID: n.ID, Username: prepared.Username, HostKey: prepared.HostKey, Home: prepared.Home, IdentityFile: key, State: "preparing", Generation: string(n.ID)}
	_ = c.Store.Put("bootstraps", state.RouteKey(profile, i.ID, n.ID), prepared)
	if svc.Host != "" && svc.Port > 0 {
		r.Kind = "direct"
		r.Host = svc.Host
		r.Port = svc.Port
		probe, cancel := context.WithTimeout(ctx, 3*time.Second)
		cl, pe := connect.DialSSH(probe, r)
		cancel()
		if pe == nil {
			cl.Close()
			r.State = "ready"
			r.VerifiedAt = time.Now().UTC()
			_ = c.Store.PutRoute(r)
			c.Audit("ssh.ready", profile, string(i.ID), "", "", "verified direct SSH route")
			return r, nil
		}
	}
	if p.Relay.Address == "" {
		return r, domain.E("RELAY_REQUIRED", "SSH is not directly reachable; configure a relay (controller may remain local)")
	}
	var lease domain.PortLease
	if e = c.relayCall(ctx, p, "relay.reserve", map[string]any{"instance_id": i.ID, "node_id": n.ID}, &lease); e != nil {
		return r, e
	}
	r.Kind = "relay"
	r.Host = "127.0.0.1"
	r.Port = lease.Port
	r.Relay = p.Relay
	r.RelayLeaseKey = lease.Key
	if c.opts.AuthorizedKeys != "" && p.Relay.IdentityFile == "" {
		r.Kind = "direct"
	}
	// Retain the lease before any remote enrollment so failed setup can still
	// be diagnosed and revoked by the run's normal cleanup path.
	if e = c.Store.PutRoute(r); e != nil {
		return r, e
	}
	if e = c.relayCall(ctx, p, "relay.enroll", map[string]any{"key": lease.Key, "public_key": prepared.TunnelPublicKey}, &lease); e != nil {
		return r, e
	}
	endpoint := p.Relay.InstanceAddress
	if endpoint == "" {
		endpoint = p.Relay.Address
	}
	addr, relayUser, e := connect.ParseSSHAddress(endpoint, p.Relay.User)
	if e != nil {
		return r, e
	}
	host, portText, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portText)
	if _, e = connect.ConfigureTunnel(ctx, co, prepared, connect.TunnelConfig{RelayHost: host, RelayPort: port, RelayUser: relayUser, RelayHostKey: p.Relay.HostKey, Port: lease.Port, TargetPort: svc.InnerPort, LeaseKey: lease.Key}); e != nil {
		return r, e
	}
	if _, e = connect.StartTunnel(ctx, co, prepared); e != nil {
		return r, e
	}
	r.Kind = "relay"
	r.Host = "127.0.0.1"
	r.Port = lease.Port
	r.Relay = p.Relay
	r.RelayLeaseKey = lease.Key
	if c.opts.AuthorizedKeys != "" && p.Relay.IdentityFile == "" {
		r.Kind = "direct"
	}
	// Save the candidate for diagnosis, never claim ready until key-pinned login succeeds.
	_ = c.Store.PutRoute(r)
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		var out bytes.Buffer
		code, pe := connect.Run(probe, r, connect.SessionOptions{Command: []string{"id", "-un"}}, nil, &out, &bytes.Buffer{}, nil, nil)
		cancel()
		if pe == nil && code == 0 && strings.TrimSpace(out.String()) == r.Username {
			r.State = "ready"
			r.VerifiedAt = time.Now().UTC()
			_ = c.Store.PutRoute(r)
			c.Audit("ssh.ready", profile, string(i.ID), "", "", "verified reverse SSH route")
			return r, nil
		}
		select {
		case <-ctx.Done():
			return r, ctx.Err()
		case <-deadline.C:
			r.State = "unreachable"
			_ = c.Store.PutRoute(r)
			return r, domain.E("SSH_NOT_READY", "tunnel was prepared but a verified SSH login did not succeed; inspect doctor and tunnel log")
		case <-tick.C:
		}
	}
}
func (c *Controller) RegisterDeviceKey(ctx context.Context, profile, name, public, target string, nodes ...string) (map[string]any, error) {
	public, e := connect.CanonicalPublicKey(public)
	if e != nil {
		return nil, e
	}
	fp, e := connect.Fingerprint(public)
	if e != nil {
		return nil, e
	}
	k := DeviceKey{ID: fp, Name: name, Profile: profile, PublicKey: public, CreatedAt: time.Now().UTC()}
	if e = c.Store.Put("keys", profile+"/"+fp, k); e != nil {
		return nil, e
	}
	routes, _ := c.Store.ListRoutes(profile)
	installed := []string{}
	pending := []string{}
	if target != "" {
		node := ""
		if len(nodes) > 0 {
			node = nodes[0]
		}
		if route, e := c.CachedRoute(profile, target, node); e == nil {
			target = string(route.InstanceID)
		}
	}
	for _, r := range routes {
		if target != "" && string(r.InstanceID) != target && r.Name != target {
			continue
		}
		if len(nodes) > 0 && nodes[0] != "" && string(r.NodeID) != nodes[0] {
			continue
		}
		_, e := connect.ApplyAuthorizedKeys(ctx, r, []string{public}, nil)
		if e == nil {
			installed = append(installed, string(r.InstanceID))
		} else {
			pending = append(pending, string(r.InstanceID))
		}
	}
	if target != "" && len(installed) == 0 && len(pending) == 0 {
		pending = append(pending, target)
	}
	return map[string]any{"id": fp, "public_key": public, "installed": installed, "pending": pending}, nil
}
func (c *Controller) revokedKeys(profile string) []string {
	out := []string{}
	for _, key := range c.ClientKeys(profile) {
		if key.Revoked {
			out = append(out, key.PublicKey)
		}
	}
	return out
}
func (c *Controller) RevokeDeviceKey(ctx context.Context, profile, id string) (any, error) {
	var key DeviceKey
	if e := c.Store.Get("keys", profile+"/"+id, &key); e != nil {
		return nil, e
	}
	public, e := connect.CanonicalPublicKey(key.PublicKey)
	if e != nil {
		return nil, e
	}
	// The transport identity belongs to the controller, not to the device-key registry.
	if _, e = os.Stat(c.KeyPath(profile)); e == nil {
		signer, e := connect.LoadSigner(c.KeyPath(profile), nil)
		if e != nil {
			return nil, e
		}
		if ssh.FingerprintSHA256(signer.PublicKey()) == id {
			return nil, domain.E("CONTROLLER_KEY", "controller transport keys cannot be revoked through the device-key registry")
		}
	}
	key.Revoked = true
	if e = c.Store.Put("keys", profile+"/"+id, key); e != nil {
		return nil, e
	}
	routes, e := c.Store.ListRoutes(profile)
	if e != nil {
		return nil, e
	}
	pending, preserved := []string{}, []string{}
	for _, route := range routes {
		result, e := connect.ApplyAuthorizedKeys(ctx, route, nil, []string{public})
		if e != nil {
			pending = append(pending, string(route.InstanceID))
			continue
		}
		if len(result.PreservedUnmanagedBlobs) > 0 {
			preserved = append(preserved, string(route.InstanceID))
		}
	}
	return map[string]any{"revoked": id, "pending": pending, "preserved_unmanaged": preserved}, nil
}
func (c *Controller) CloseRoutes(ctx context.Context, profile string, id domain.ID) error {
	routes, _ := c.Store.ListRoutes(profile)
	p := c.Configuration().Profiles[profile]
	for _, r := range routes {
		if r.InstanceID != id {
			continue
		}
		var result connect.BootstrapResult
		if c.Store.Get("bootstraps", state.RouteKey(profile, r.InstanceID, r.NodeID), &result) == nil {
			program, e := connect.TunnelProgram("stop", result, connect.TunnelConfig{})
			if e == nil {
				_, _ = connect.Run(ctx, r, connect.SessionOptions{Command: []string{"python3", "-c", program}}, nil, &bytes.Buffer{}, &bytes.Buffer{}, nil, nil)
			}
		}
		if r.RelayLeaseKey != "" {
			var revoked domain.PortLease
			_ = c.relayCall(ctx, p, "relay.revoke", map[string]string{"key": r.RelayLeaseKey}, &revoked)
		}
		r.State = "stopped"
		_ = c.Store.PutRoute(r)
	}
	return nil
}
func (c *Controller) ClientKeys(profile string) []DeviceKey {
	var all []DeviceKey
	_ = c.Store.List("keys", &all)
	out := []DeviceKey{}
	for _, k := range all {
		if k.Profile == profile {
			out = append(out, k)
		}
	}
	return out
}
func (c *Controller) KeyPath(profile string) string {
	return filepath.Join(c.Configuration().Paths.DataDir, "keys", profile+"-automation")
}
