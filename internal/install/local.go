package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
	"github.com/BurntSushi/toml"
)

type device struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PublicKey    string `json:"public_key"`
	IdentityFile string `json:"identity_file"`
}
type serviceRecord struct {
	Arguments    []string `json:"arguments,omitempty"`
	Manager      string   `json:"manager"`
	Name         string   `json:"name"`
	Path         string   `json:"path,omitempty"`
	PID          int      `json:"pid,omitempty"`
	Executable   string   `json:"executable"`
	Config       string   `json:"config"`
	SetupPending bool     `json:"setup_pending,omitempty"`
}

func (h *handler) setup(ctx context.Context, cfg *config.Config, method string, p map[string]any, in io.Reader, out, errOut io.Writer) (any, error) {
	profile, err := cfg.SelectedProfile()
	if err != nil {
		return nil, err
	}
	mode := paramString(p, "mode")
	if mode == "" {
		if profile.Controller != "" && profile.Controller != "local" || paramString(p, "address") != "" {
			mode = "remote"
		} else {
			mode = "local"
		}
	}
	if mode != "local" && mode != "remote" {
		return nil, domain.E("INVALID_MODE", "controller mode must be local or remote")
	}
	if mode == "remote" {
		return h.setupRemote(ctx, cfg, p, errOut)
	}
	if paramBool(p, "relay_install", false) {
		remoteParams := make(map[string]any, len(p))
		for key, value := range p {
			remoteParams[key] = value
		}
		delete(remoteParams, "foreground")
		remoteResult, err := h.setupRemote(ctx, cfg, remoteParams, errOut)
		if err != nil {
			return nil, err
		}
		if paramBool(p, "dry_run", false) {
			return map[string]any{"mode": "local", "relay_install": remoteResult, "dry_run": true}, nil
		}
		remoteMap := remoteResult.(map[string]any)
		updated := *cfg
		updated.Profiles = cloneProfiles(cfg.Profiles)
		updated.Profiles[cfg.Selected] = remoteMap["profile"].(domain.Profile)
		localParams := make(map[string]any, len(p))
		for key, value := range p {
			localParams[key] = value
		}
		for _, key := range []string{"relay_install", "relay_address", "relay_user", "relay_identity_file", "relay_host_key"} {
			delete(localParams, key)
		}
		localParams["mode"] = "local"
		result, err := h.setup(ctx, &updated, method, localParams, in, out, errOut)
		if err != nil {
			return result, fmt.Errorf("relay was installed; local controller setup failed: %w", err)
		}
		result.(map[string]any)["relay_install"] = remoteResult
		return result, nil
	}
	install := paramBool(p, "install", true)
	firstUse := method == "workspace.start"
	start := paramBool(p, "start", true)
	foreground := paramBool(p, "foreground", false)
	if foreground {
		install = false
	}
	name := first(paramString(p, "service_name"), "bupt-agent")
	if !safeName.MatchString(name) {
		return nil, domain.E("INVALID_SERVICE_NAME", "service name must be a safe identifier")
	}
	keyPath, err := h.path(first(paramString(p, "master_key_file"), filepath.Join(cfg.Paths.DataDir, "master.key")))
	if err != nil {
		return nil, err
	}
	if paramBool(p, "dry_run", false) {
		return map[string]any{"operation": method, "mode": mode, "config": cfg.Paths.ConfigFile, "data_dir": cfg.Paths.DataDir, "state_dir": cfg.Paths.StateDir, "master_key_file": keyPath, "install": install, "start": start, "foreground": foreground, "dry_run": true}, nil
	}
	if install && h.goos != "darwin" && h.goos != "linux" {
		return nil, domain.E("UNSUPPORTED_SERVICE_MANAGER", "use --foreground or --install=false on this operating system")
	}
	for _, dir := range []string{cfg.Paths.ConfigDir, cfg.Paths.DataDir, cfg.Paths.StateDir, filepath.Join(cfg.Paths.DataDir, "keys")} {
		if err = ensurePrivateDir(dir); err != nil {
			return nil, err
		}
	}
	if info, e := os.Lstat(keyPath); errors.Is(e, os.ErrNotExist) {
		if firstUse {
			if _, e := os.Lstat(filepath.Join(cfg.Paths.DataDir, "credentials.db")); !errors.Is(e, os.ErrNotExist) {
				return nil, domain.E("MASTER_KEY_REQUIRED", "已有加密凭据库，请恢复原主密钥后重试")
			}
		}
		if err = state.CreateMasterKey(keyPath); err != nil {
			return nil, err
		}
	} else if e != nil {
		return nil, e
	} else if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, domain.E("INSECURE_MASTER_KEY", "master key must be a regular file with mode 0600 or 0400")
	}
	if err = validateMasterFile(keyPath); err != nil {
		return nil, err
	}
	d, err := h.ensureDevice(cfg)
	if err != nil {
		return nil, err
	}
	profile.Controller = "local"
	profile.ControllerSocket, err = h.path(first(profile.ControllerSocket, cfg.Paths.Socket))
	if err != nil {
		return nil, err
	}
	profile.ControllerIdentityFile = ""
	profile.ControllerHostKey = ""
	profile.ControllerUser = ""
	if v := paramString(p, "socket"); v != "" {
		profile.ControllerSocket, err = h.path(v)
		if err != nil {
			return nil, err
		}
	}
	if err = applyRelayParams(&profile, p); err != nil {
		return nil, err
	}
	backups := []string{}
	backup, err := h.saveProfile(cfg, profile, firstUse)
	if err != nil {
		return nil, err
	}
	addBackup(&backups, backup)
	updated := *cfg
	updated.Profiles = cloneProfiles(cfg.Profiles)
	updated.Profiles[cfg.Selected] = profile
	args := agentArgs(&updated, keyPath)
	result := map[string]any{"mode": "local", "config": cfg.Paths.ConfigFile, "data_dir": cfg.Paths.DataDir, "state_dir": cfg.Paths.StateDir, "master_key_file": keyPath, "identity_file": d.IdentityFile, "client_id": d.ID, "started": false, "installed": false, "backups": backups, "foreground_command": append([]string{h.executable}, args...)}
	if firstUse {
		result["public_key"] = d.PublicKey
	}
	if foreground {
		if !start {
			return result, nil
		}
		fmt.Fprintln(errOut, "Running the local controller in the foreground; Ctrl-C stops this process.")
		err = h.run(ctx, h.executable, args, in, out, errOut)
		result["started"] = err == nil
		return result, err
	}
	record := serviceRecord{Executable: h.executable, Config: cfg.Paths.ConfigFile, Name: name}
	if install {
		record, backup, err = h.installUserService(ctx, &updated, keyPath, name, start, errOut)
		if err != nil {
			return nil, err
		}
		addBackup(&backups, backup)
		result["installed"] = true
		result["service_file"] = record.Path
		result["service_manager"] = record.Manager
	} else if start {
		var status any
		if e := h.rpc(ctx, &updated, "agent.status", map[string]any{}, &status); e == nil {
			result["already_running"] = true
		} else {
			pid, e := h.start(h.executable, args, filepath.Join(cfg.Paths.StateDir, "agent.log"))
			if e != nil {
				return nil, e
			}
			record.Manager = "process"
			record.Arguments = args
			record.PID = pid
			result["pid"] = pid
			result["log"] = filepath.Join(cfg.Paths.StateDir, "agent.log")
		}
	}
	if record.Manager != "" {
		data, _ := json.MarshalIndent(record, "", "  ")
		recordPath := filepath.Join(cfg.Paths.StateDir, "service.json")
		backup, err = h.replaceFile(recordPath, append(data, '\n'), 0600)
		if err != nil {
			return nil, err
		}
		addBackup(&backups, backup)
	}
	result["backups"] = backups
	result["recovery"] = localRecovery(record, &updated, keyPath)
	if start {
		if err = h.waitReady(ctx, &updated); err != nil {
			return result, err
		}
		result["started"] = true
		var enrollment any
		if err = h.rpc(ctx, &updated, "key.import", map[string]any{"name": "device", "public_key": d.PublicKey}, &enrollment); err != nil {
			return result, fmt.Errorf("controller started, but device key enrollment failed: %w", err)
		}
		result["key"] = enrollment
	}
	return result, nil
}

func cloneProfiles(profiles map[string]domain.Profile) map[string]domain.Profile {
	out := make(map[string]domain.Profile, len(profiles))
	for k, v := range profiles {
		out[k] = v
	}
	return out
}

func validateMasterFile(path string) error {
	data, err := readRegular(path, 4096)
	if err != nil {
		return err
	}
	defer clear(data)
	if len(data) == 32 {
		return nil
	}
	text := strings.TrimSpace(string(data))
	for _, decode := range []func(string) ([]byte, error){base64.StdEncoding.DecodeString, base64.RawStdEncoding.DecodeString, hex.DecodeString} {
		decoded, err := decode(text)
		valid := err == nil && len(decoded) == 32
		clear(decoded)
		if valid {
			return nil
		}
	}
	return domain.E("INVALID_MASTER_KEY", "master key must be 32 random bytes or their base64/hex encoding")
}

func applyRelayParams(profile *domain.Profile, p map[string]any) error {
	if v := paramString(p, "relay_address"); v != "" {
		profile.Relay.Address = v
	}
	if v := paramString(p, "relay_user"); v != "" {
		profile.Relay.User = v
	}
	if v := paramString(p, "relay_identity_file"); v != "" {
		profile.Relay.IdentityFile = v
	}
	if v := paramString(p, "relay_host_key"); v != "" {
		profile.Relay.HostKey = v
	}
	if v := paramString(p, "instance_address"); v != "" {
		profile.Relay.InstanceAddress = v
	}
	profile.Relay.PortMin = paramInt(p, "port_min", profile.Relay.PortMin)
	if profile.Relay.PortMin == 0 {
		profile.Relay.PortMin = 24000
	}
	profile.Relay.PortMax = paramInt(p, "port_max", profile.Relay.PortMax)
	if profile.Relay.PortMax == 0 {
		profile.Relay.PortMax = 24999
	}
	return config.ValidateProfile(*profile)
}

func (h *handler) ensureDevice(cfg *config.Config) (device, error) {
	keyPath := filepath.Join(cfg.Paths.DataDir, "keys", "device_ed25519")
	if err := ensurePrivateDir(filepath.Dir(keyPath)); err != nil {
		return device{}, err
	}
	public, err := connect.EnsureIdentity(keyPath, "bupt device")
	if err != nil {
		return device{}, err
	}
	sum := sha256.Sum256([]byte(public))
	id := "device-" + hex.EncodeToString(sum[:12])
	d := device{ID: id, Name: "device", PublicKey: public, IdentityFile: keyPath}
	data, _ := json.MarshalIndent(d, "", "  ")
	if _, err = h.replaceFile(filepath.Join(cfg.Paths.DataDir, "device.json"), append(data, '\n'), 0600); err != nil {
		return device{}, err
	}
	return d, nil
}

func agentArgs(cfg *config.Config, keyPath string) []string {
	return []string{"agent", "run", "--no-project", "--config", cfg.Paths.ConfigFile, "--data-dir", cfg.Paths.DataDir, "--state-dir", cfg.Paths.StateDir, "--socket", cfg.Profiles[cfg.Selected].ControllerSocket, "--profile", cfg.Selected, "--master-key-file", keyPath}
}

func profileMap(p domain.Profile) map[string]any {
	return map[string]any{"platform_url": p.PlatformURL, "gateway_url": p.GatewayURL, "username": p.Username, "expected_uid": string(p.ExpectedUID), "controller": p.Controller, "controller_user": p.ControllerUser, "controller_identity_file": p.ControllerIdentityFile, "controller_host_key": p.ControllerHostKey, "controller_socket": p.ControllerSocket, "http_proxy": p.HTTPProxy, "relay": map[string]any{"address": p.Relay.Address, "user": p.Relay.User, "identity_file": p.Relay.IdentityFile, "known_hosts_file": p.Relay.KnownHostsFile, "host_key": p.Relay.HostKey, "instance_address": p.Relay.InstanceAddress, "port_min": p.Relay.PortMin, "port_max": p.Relay.PortMax}}
}

func (h *handler) saveProfile(cfg *config.Config, p domain.Profile, preserve bool) (string, error) {
	doc := map[string]any{}
	data, err := readRegular(cfg.Paths.ConfigFile, 1<<20)
	if err == nil {
		if preserve {
			return "", nil
		}
		if _, err = toml.Decode(string(data), &doc); err != nil {
			return "", domain.E("INVALID_CONFIG", "cannot parse existing configuration")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if _, ok := doc["version"]; !ok {
		doc["version"] = 1
	}
	if _, ok := doc["default_profile"]; !ok {
		doc["default_profile"] = cfg.Selected
	}
	profiles, ok := doc["profiles"].(map[string]any)
	if !ok {
		profiles = map[string]any{}
		doc["profiles"] = profiles
	}
	old, _ := profiles[p.Name].(map[string]any)
	if old == nil {
		old = map[string]any{}
	}
	for k, v := range profileMap(p) {
		old[k] = v
	}
	// Empty controller values must clear a previously configured remote endpoint.
	for k, v := range map[string]string{"controller": p.Controller, "controller_user": p.ControllerUser, "controller_identity_file": p.ControllerIdentityFile, "controller_host_key": p.ControllerHostKey, "controller_socket": p.ControllerSocket} {
		old[k] = v
	}
	profiles[p.Name] = old
	if _, ok := doc["policy"]; !ok {
		doc["policy"] = map[string]any{"cleanup": "release", "stop_grace": "60s", "force_stop": false, "auto_renew": false}
	}
	var b bytes.Buffer
	if err = toml.NewEncoder(&b).Encode(doc); err != nil {
		return "", err
	}
	if preserve {
		return "", writeNew(cfg.Paths.ConfigFile, b.Bytes(), 0600)
	}
	return h.replaceFile(cfg.Paths.ConfigFile, b.Bytes(), 0600)
}

func (h *handler) waitReady(ctx context.Context, cfg *config.Config) error {
	waitCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for {
		var result any
		if err := h.rpc(waitCtx, cfg, "agent.status", map[string]any{}, &result); err == nil {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return domain.E("AGENT_START_FAILED", "controller did not become ready; inspect its service status or the private agent.log")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func startDetached(binary string, args []string, logPath string) (int, error) {
	if info, err := os.Lstat(logPath); err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return 0, domain.E("UNSAFE_LOG_FILE", "agent log must be a private regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	cmd := exec.Command(binary, args...)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	go func() { _ = cmd.Wait() }()
	return pid, nil
}

func (h *handler) stop(ctx context.Context, cfg *config.Config, p map[string]any, errOut io.Writer) (any, error) {
	if paramBool(p, "dry_run", false) {
		return map[string]any{"operation": "agent.stop", "profile": cfg.Selected, "dry_run": true}, nil
	}
	profile, err := cfg.SelectedProfile()
	if err != nil {
		return nil, err
	}
	if profile.Controller != "" && profile.Controller != "local" {
		var result any
		err = h.rpc(ctx, cfg, "agent.stop", map[string]any{}, &result)
		return result, err
	}
	recordPath := filepath.Join(cfg.Paths.StateDir, "service.json")
	data, err := readRegular(recordPath, 64<<10)
	var record serviceRecord
	if err == nil {
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, domain.E("INVALID_SERVICE_RECORD", "cannot parse local service record")
		}
		if record.Config != cfg.Paths.ConfigFile {
			return nil, domain.E("SERVICE_CONFIG_MISMATCH", "service record belongs to another configuration")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	switch record.Manager {
	case "launchd":
		if !safeName.MatchString(record.Name) {
			return nil, domain.E("INVALID_SERVICE_RECORD", "invalid launchd label")
		}
		err = h.run(ctx, "launchctl", []string{"bootout", "gui/" + strconv.Itoa(h.uid) + "/" + record.Name}, nil, io.Discard, errOut)
	case "systemd-user":
		if !safeName.MatchString(record.Name) {
			return nil, domain.E("INVALID_SERVICE_RECORD", "invalid systemd unit name")
		}
		err = h.run(ctx, "systemctl", []string{"--user", "stop", record.Name + ".service"}, nil, io.Discard, errOut)
	default:
		var result any
		err = h.rpc(ctx, cfg, "agent.stop", map[string]any{}, &result)
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"stopped": true, "profile": cfg.Selected, "routes_retained": true, "data_retained": true}, nil
}

func localRecovery(r serviceRecord, cfg *config.Config, keyPath string) map[string]any {
	stop := []string{
		r.Executable, "--no-project", "--config", cfg.Paths.ConfigFile,
		"--state-dir", cfg.Paths.StateDir, "--data-dir", cfg.Paths.DataDir,
		"--profile", cfg.Selected, "--controller", "local",
		"--socket", cfg.Profiles[cfg.Selected].ControllerSocket, "agent", "stop",
	}
	info := map[string]any{
		"stop":     stop,
		"preserve": []string{cfg.Paths.ConfigFile, cfg.Paths.DataDir, cfg.Paths.StateDir, keyPath},
		"note":     "Stopping or uninstalling the controller retains credentials, cached routes and instance tunnels. Copy the master key with encrypted data for recovery.",
	}
	switch r.Manager {
	case "launchd":
		info["uninstall"] = []string{"run the stop command in recovery.stop", "remove the managed launchd file at " + r.Path}
	case "systemd-user":
		info["uninstall"] = []string{"systemctl --user disable --now " + r.Name + ".service", "remove the managed unit: " + r.Path, "systemctl --user daemon-reload"}
	default:
		info["uninstall"] = []string{"run the stop command in recovery.stop", "remove the bupt executable only after preserving data"}
	}
	return info
}

func quoteShell(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
