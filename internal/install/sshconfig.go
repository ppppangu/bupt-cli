package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
)

type enrollment struct {
	Installed []json.RawMessage `json:"installed"`
	Pending   []json.RawMessage `json:"pending"`
}

func (h *handler) sshConfig(ctx context.Context, cfg *config.Config, method string, p map[string]any, out io.Writer) (any, error) {
	target := paramString(p, "target")
	requestedAlias := paramString(p, "alias")
	if requestedAlias != "" && (!safeName.MatchString(requestedAlias) || len(requestedAlias) > 255 || target == "") {
		return nil, domain.E("INVALID_ALIAS", "--alias requires one instance and a safe SSH host name")
	}
	output := paramString(p, "output")
	if output != "" {
		var err error
		output, err = h.path(output)
		if err != nil {
			return nil, err
		}
	}
	if method == "ssh-config.install" && output == "" {
		output = filepath.Join(h.home, ".ssh", "bupt", cfg.Selected+".conf")
	}
	if paramBool(p, "dry_run", false) {
		return map[string]any{"operation": method, "target": target, "output": output, "dry_run": true}, nil
	}
	routes := []domain.Route{}
	if target != "" {
		var route domain.Route
		if err := h.rpc(ctx, cfg, "route.get", map[string]any{"target": target, "node": paramString(p, "node"), "repair": false}, &route); err != nil {
			return nil, err
		}
		routes = append(routes, route)
	} else {
		if err := h.rpc(ctx, cfg, "route.list", map[string]any{}, &routes); err != nil {
			return nil, err
		}
	}
	if len(routes) == 0 {
		return nil, domain.E("ROUTE_NOT_FOUND", "no cached SSH routes exist; prepare or start an instance first")
	}
	if err := ensurePrivateDir(cfg.Paths.DataDir); err != nil {
		return nil, err
	}
	d, err := h.ensureDevice(cfg)
	if err != nil {
		return nil, err
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].InstanceID != routes[j].InstanceID {
			return routes[i].InstanceID < routes[j].InstanceID
		}
		return routes[i].NodeID < routes[j].NodeID
	})
	seen := map[string]bool{}
	knownHosts := filepath.Join(cfg.Paths.DataDir, "ssh_known_hosts")
	blocks := map[string]string{}
	hostKeys := map[string]string{}
	aliases := []string{}
	for _, route := range routes {
		if route.Profile != "" && route.Profile != cfg.Selected {
			return nil, domain.E("PROFILE_MISMATCH", "cached route belongs to another profile")
		}
		routeID := string(route.InstanceID) + ":" + string(route.NodeID)
		if seen[routeID] {
			continue
		}
		seen[routeID] = true
		if !safeName.MatchString(string(route.InstanceID)) || !safeName.MatchString(route.Username) || (route.NodeID != "" && !safeName.MatchString(string(route.NodeID))) {
			return nil, domain.E("INVALID_ROUTE", "cached route has an invalid instance ID or SSH username")
		}
		public, err := connect.CanonicalPublicKey(route.HostKey)
		if err != nil {
			return nil, domain.E("HOST_KEY_REQUIRED", "cached SSH route must contain its verified host public key")
		}
		var enrollment enrollment
		if err = h.rpc(ctx, cfg, "key.import", map[string]any{"name": "device", "public_key": d.PublicKey, "target": string(route.InstanceID), "node": string(route.NodeID)}, &enrollment); err != nil {
			return nil, err
		}
		if len(enrollment.Installed) == 0 || len(enrollment.Pending) > 0 {
			return nil, domain.E("KEY_NOT_INSTALLED", "device public key is not yet installed on this instance; restore its cached SSH route and retry")
		}
		alias := "bupt-" + cfg.Selected + "-" + string(route.InstanceID)
		if route.NodeID != "" {
			alias += "-" + string(route.NodeID)
		}
		if requestedAlias != "" {
			alias = requestedAlias
		}
		if len(alias) > 255 {
			return nil, domain.E("INVALID_ALIAS", "profile and instance ID exceed the SSH alias limit")
		}
		id := cfg.Selected + ":" + routeID
		blocks[id] = sshBlock(h.executable, cfg, route, d.IdentityFile, knownHosts, alias, id)
		hostKeys[alias] = public
		aliases = append(aliases, alias)
	}
	backups := []string{}
	hosts, err := mergeKnownHosts(knownHosts, hostKeys)
	if err != nil {
		return nil, err
	}
	backup, err := h.replaceFile(knownHosts, []byte(hosts), 0600)
	if err != nil {
		return nil, err
	}
	addBackup(&backups, backup)
	text := renderBlocks(blocks)
	if output != "" {
		if target != "" {
			text, err = mergeSSHBlocks(output, blocks)
			if err != nil {
				return nil, err
			}
		}
		backup, err = h.replaceFile(output, []byte(text), 0600)
		if err != nil {
			return nil, err
		}
		addBackup(&backups, backup)
	}
	if method == "ssh-config.export" && output == "" {
		_, err = io.WriteString(out, text)
		return nil, err
	}
	result := map[string]any{"path": output, "aliases": aliases, "identity_file": d.IdentityFile, "known_hosts": knownHosts, "backups": backups}
	if method == "ssh-config.install" {
		sshDir := filepath.Join(h.home, ".ssh")
		if err = ensurePrivateDir(sshDir); err != nil {
			return nil, err
		}
		sshConfig := filepath.Join(sshDir, "config")
		previous, err := readRegular(sshConfig, 4<<20)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		include := "Include " + sshQuote(output)
		updated := insertInclude(string(previous), include)
		backup, err = h.replaceFile(sshConfig, []byte(updated), 0600)
		if err != nil {
			return nil, err
		}
		addBackup(&backups, backup)
		result["ssh_config"] = sshConfig
		result["backups"] = backups
		result["recovery"] = map[string]any{"uninstall": "remove only the added Include line from " + sshConfig + " and the managed file " + output, "restore_backups": backups, "keys_retained": true}
	}
	return result, nil
}

func sshQuote(s string) string {
	return "\"" + strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "%", "%%").Replace(s) + "\""
}
func proxyArg(s string) string { return quoteShell(strings.ReplaceAll(s, "%", "%%")) }
func sshBlock(executable string, cfg *config.Config, route domain.Route, identity, knownHosts, alias, id string) string {
	args := []string{executable, "--no-project", "--config", cfg.Paths.ConfigFile, "--data-dir", cfg.Paths.DataDir, "--state-dir", cfg.Paths.StateDir, "--profile", cfg.Selected, "proxy", string(route.InstanceID)}
	if route.NodeID != "" {
		args = append(args, "--node", string(route.NodeID))
	}
	for i := range args {
		args[i] = proxyArg(args[i])
	}
	return fmt.Sprintf("# bupt BEGIN %s\nHost %s\n    HostName %s\n    User %s\n    Port 22\n    IdentityFile %s\n    IdentitiesOnly yes\n    ProxyCommand %s\n    HostKeyAlias %s\n    UserKnownHostsFile %s\n    StrictHostKeyChecking yes\n    CheckHostIP no\n    ServerAliveInterval 30\n    ServerAliveCountMax 3\n# bupt END %s\n", id, alias, alias, route.Username, sshQuote(identity), strings.Join(args, " "), alias, sshQuote(knownHosts), id)
}
func renderBlocks(blocks map[string]string) string {
	ids := make([]string, 0, len(blocks))
	for id := range blocks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	b.WriteString("# Managed BUPT SSH routes. Device keys and cached routes survive school login expiry.\n")
	for _, id := range ids {
		b.WriteByte('\n')
		b.WriteString(blocks[id])
	}
	return b.String()
}
func mergeSSHBlocks(path string, replacements map[string]string) (string, error) {
	data, err := readRegular(path, 4<<20)
	if errors.Is(err, os.ErrNotExist) {
		return renderBlocks(replacements), nil
	}
	if err != nil {
		return "", err
	}
	blocks := map[string]string{}
	lines := strings.Split(string(data), "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(lines[i], "# bupt BEGIN ") {
			if strings.TrimSpace(lines[i]) != "" && !strings.HasPrefix(lines[i], "#") {
				return "", domain.E("UNMANAGED_SSH_CONFIG", "output file contains configuration outside managed BUPT blocks")
			}
			continue
		}
		id := strings.TrimPrefix(lines[i], "# bupt BEGIN ")
		start := i
		i++
		for i < len(lines) && lines[i] != "# bupt END "+id {
			i++
		}
		if i == len(lines) {
			return "", domain.E("INVALID_SSH_CONFIG", "managed SSH block is incomplete")
		}
		blocks[id] = strings.Join(lines[start:i+1], "\n") + "\n"
	}
	for id, text := range replacements {
		blocks[id] = text
	}
	return renderBlocks(blocks), nil
}
func mergeKnownHosts(path string, replacements map[string]string) (string, error) {
	data, err := readRegular(path, 4<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		parts := strings.Fields(line)
		if len(parts) == 0 {
			continue
		}
		if _, ok := replacements[parts[0]]; !ok {
			kept = append(kept, line)
		}
	}
	aliases := make([]string, 0, len(replacements))
	for alias := range replacements {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		kept = append(kept, alias+" "+replacements[alias])
	}
	return strings.Join(kept, "\n") + "\n", nil
}
func insertInclude(previous, line string) string {
	// An Include placed after Host/Match inherits that scope. Keep the exact
	// managed include once at the top while preserving all user-owned content.
	var kept []string
	for _, existing := range strings.Split(previous, "\n") {
		if strings.TrimSpace(existing) != line {
			kept = append(kept, existing)
		}
	}
	return line + "\n" + strings.Join(kept, "\n")
}
