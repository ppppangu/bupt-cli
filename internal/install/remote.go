package install

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
	"github.com/BurntSushi/toml"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type remote interface {
	Run(context.Context, string, io.Reader) ([]byte, error)
	Upload(context.Context, string, io.Reader, os.FileMode) error
	Close() error
}

type sshRemote struct {
	ssh  *ssh.Client
	sftp *sftp.Client
}

func dialRemote(ctx context.Context, relay domain.RelayConfig) (remote, error) {
	c, err := connect.DialRelay(ctx, relay)
	if err != nil {
		return nil, err
	}
	f, err := sftp.NewClient(c, uploadClientOptions()...)
	if err != nil {
		c.Close()
		return nil, err
	}
	return &sshRemote{ssh: c, sftp: f}, nil
}

func uploadClientOptions() []sftp.ClientOption {
	return []sftp.ClientOption{sftp.UseConcurrentWrites(true)}
}
func (r *sshRemote) Close() error { _ = r.sftp.Close(); return r.ssh.Close() }
func (r *sshRemote) Run(ctx context.Context, command string, in io.Reader) ([]byte, error) {
	s, err := r.ssh.NewSession()
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	var stdout, stderr bytes.Buffer
	s.Stdin = in
	s.Stdout = &boundedWriter{w: &stdout, left: 1 << 20}
	s.Stderr = &boundedWriter{w: &stderr, left: 1 << 20}
	if err = s.Run(command); err != nil {
		return nil, fmt.Errorf("remote setup failed: %s (%w)", state.RedactText(strings.TrimSpace(stderr.String())), err)
	}
	return stdout.Bytes(), nil
}
func (r *sshRemote) Upload(ctx context.Context, path string, in io.Reader, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Closing a File waits for its write mutex, so it cannot interrupt an
	// in-flight concurrent transfer. Cancel its SFTP channel instead.
	stop := context.AfterFunc(ctx, func() { _ = r.sftp.Close() })
	defer stop()
	f, err := r.sftp.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	if err = f.Chmod(mode); err == nil {
		// Direct ReadFrom retains os.File.Stat/bytes.Reader.Len, which the
		// library uses to pipeline writes. io.Copy may hide this size metadata
		// behind a source WriterTo adapter and serialize the whole upload.
		_, err = f.ReadFrom(in)
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// An incomplete file remains only in the private staging directory. Setup
	// never executes it: every upload must succeed and its SHA-256 must match.
	return err
}

type boundedWriter struct {
	w    io.Writer
	left int
}

func (w *boundedWriter) Write(b []byte) (int, error) {
	if len(b) > w.left {
		return 0, domain.E("OUTPUT_LIMIT", "remote setup output exceeded its limit")
	}
	n, err := w.w.Write(b)
	w.left -= n
	return n, err
}

func linuxBinary(path string) (string, string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 256<<20 {
		return "", "", domain.E("INVALID_AGENT_BINARY", "agent binary must be a regular Linux executable under 256 MiB")
	}
	f, err := elf.Open(path)
	if err != nil {
		return "", "", domain.E("INVALID_AGENT_BINARY", "remote installation requires a Linux ELF binary; supply --agent-binary or --binary")
	}
	defer f.Close()
	if f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN {
		return "", "", domain.E("INVALID_AGENT_BINARY", "ELF input is not an executable")
	}
	arch := ""
	switch f.Machine {
	case elf.EM_X86_64:
		arch = "x86_64"
	case elf.EM_AARCH64:
		arch = "aarch64"
	default:
		return "", "", domain.E("UNSUPPORTED_AGENT_ARCH", "remote installer supports Linux amd64 and arm64")
	}
	if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
		return "", "", domain.E("INVALID_AGENT_BINARY", "ELF target operating system is not Linux")
	}
	if bi, e := buildinfo.ReadFile(path); e == nil {
		for _, setting := range bi.Settings {
			if setting.Key == "GOOS" && setting.Value != "linux" {
				return "", "", domain.E("INVALID_AGENT_BINARY", "Go binary was not built for Linux")
			}
		}
	}
	input, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer input.Close()
	sum := sha256.New()
	if _, err = io.Copy(sum, input); err != nil {
		return "", "", err
	}
	return arch, hex.EncodeToString(sum.Sum(nil)), nil
}

var stagePathRE = regexp.MustCompile(`^/tmp/bupt-install\.[A-Za-z0-9]+$`)

func (h *handler) setupRemote(ctx context.Context, cfg *config.Config, p map[string]any, errOut io.Writer) (any, error) {
	profile, err := cfg.SelectedProfile()
	if err != nil {
		return nil, err
	}
	address := first(paramString(p, "address"), paramString(p, "relay_address"), profile.Relay.Address)
	if address == "" && profile.Controller != "local" {
		address = profile.Controller
	}
	adminUser := first(paramString(p, "user"), paramString(p, "relay_user"))
	adminKey := first(paramString(p, "identity_file"), paramString(p, "relay_identity_file"))
	if address == "" || adminUser == "" || adminKey == "" {
		return nil, domain.E("ADMIN_CONNECTION_REQUIRED", "remote setup requires an explicit administrator address, user and identity file")
	}
	address, adminUser, err = connect.ParseSSHAddress(address, adminUser)
	if err != nil {
		return nil, err
	}
	adminKey, err = h.path(adminKey)
	if err != nil {
		return nil, err
	}
	hostKey := first(paramString(p, "host_key"), paramString(p, "relay_host_key"), profile.ControllerHostKey, profile.Relay.HostKey)
	hostKey, err = connect.CanonicalPublicKey(hostKey)
	if err != nil {
		return nil, domain.E("HOST_KEY_REQUIRED", "remote setup requires the verified SSH host public key, including key type and base64 data")
	}
	binaryPath := first(paramString(p, "agent_binary"), paramString(p, "binary"))
	if binaryPath == "" {
		return nil, domain.E("AGENT_BINARY_REQUIRED", "supply a matching Linux binary with --agent-binary or --binary")
	}
	binaryPath, err = h.path(binaryPath)
	if err != nil {
		return nil, err
	}
	arch, digest, err := linuxBinary(binaryPath)
	if err != nil {
		return nil, err
	}
	serviceName := first(paramString(p, "service_name"), "bupt-agent")
	if !safeName.MatchString(serviceName) {
		return nil, domain.E("INVALID_SERVICE_NAME", "service name must be a safe identifier")
	}
	start := paramBool(p, "start", true)
	if paramBool(p, "foreground", false) {
		return nil, domain.E("INVALID_REMOTE_MODE", "remote installation is managed by systemd; foreground applies to a local controller")
	}
	if err = applyRelayParams(&profile, p); err != nil {
		return nil, err
	}
	if paramBool(p, "dry_run", false) {
		return map[string]any{"operation": "agent.install", "mode": "remote", "address": address, "admin_user": adminUser, "binary": binaryPath, "architecture": arch, "sha256": digest, "service": serviceName, "start": start, "account": "bupt", "socket": "/var/lib/bupt/state/agent.sock", "dry_run": true}, nil
	}
	for _, dir := range []string{cfg.Paths.ConfigDir, cfg.Paths.DataDir, cfg.Paths.StateDir} {
		if err = ensurePrivateDir(dir); err != nil {
			return nil, err
		}
	}
	d, err := h.ensureDevice(cfg)
	if err != nil {
		return nil, err
	}
	relay := domain.RelayConfig{Address: address, User: adminUser, IdentityFile: adminKey, HostKey: hostKey}
	r, err := h.dial(ctx, relay)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	preflight, err := r.Run(ctx, "sh -s", strings.NewReader(remotePreflight))
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(preflight)) != arch {
		return nil, domain.E("AGENT_ARCH_MISMATCH", "Linux binary architecture does not match the remote host")
	}
	stageBytes, err := r.Run(ctx, "sh -s", strings.NewReader("set -eu\numask 077\nmktemp -d /tmp/bupt-install.XXXXXXXXXX\n"))
	if err != nil {
		return nil, err
	}
	stage := strings.TrimSpace(string(stageBytes))
	if !stagePathRE.MatchString(stage) {
		return nil, domain.E("INVALID_REMOTE_STAGE", "remote staging directory was not recognized")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = r.Run(cleanupCtx, "rm -rf -- "+quoteShell(stage), nil)
	}()
	remoteProfile := profile
	remoteProfile.Controller = "local"
	remoteProfile.ControllerUser = ""
	remoteProfile.ControllerIdentityFile = ""
	remoteProfile.ControllerHostKey = ""
	remoteProfile.ControllerSocket = "/var/lib/bupt/state/agent.sock"
	remoteProfile.HTTPProxy = ""
	remoteProfile.Relay.Address = address
	remoteProfile.Relay.User = "bupt"
	remoteProfile.Relay.IdentityFile = ""
	remoteProfile.Relay.KnownHostsFile = ""
	remoteProfile.Relay.HostKey = hostKey
	remoteProfile.Relay.InstanceAddress = first(paramString(p, "instance_address"), profile.Relay.InstanceAddress, address)
	var remoteConfig bytes.Buffer
	configDocument := map[string]any{"version": 1, "default_profile": cfg.Selected, "profiles": map[string]any{cfg.Selected: profileMap(remoteProfile)}, "policy": map[string]any{"cleanup": "release", "stop_grace": "60s", "force_stop": true, "auto_renew": false}}
	if err = toml.NewEncoder(&remoteConfig).Encode(configDocument); err != nil {
		return nil, err
	}
	bootstrap, _ := json.Marshal(map[string]any{"id": d.ID, "name": d.Name, "public_key": d.PublicKey, "owner": true, "profiles": []string{cfg.Selected}})
	authorized := fmt.Sprintf("restrict,command=\"/usr/local/bin/bupt agent gateway --client %s --socket /var/lib/bupt/state/agent.sock\" %s\n", d.ID, d.PublicKey)
	for _, file := range []struct {
		name string
		data []byte
	}{{"config.toml", remoteConfig.Bytes()}, {"bootstrap-client.json", bootstrap}, {"operator-key", []byte(authorized)}, {"bupt-agent.service", remoteUnit(serviceName)}} {
		if err = r.Upload(ctx, stage+"/"+file.name, bytes.NewReader(file.data), 0600); err != nil {
			return nil, err
		}
	}
	binary, err := os.Open(binaryPath)
	if err != nil {
		return nil, err
	}
	err = r.Upload(ctx, stage+"/bupt", binary, 0600)
	_ = binary.Close()
	if err != nil {
		return nil, err
	}
	stamp := h.now().UTC().Format("20060102T150405.000000000")
	script := remoteInstallScript(stage, stamp, digest, serviceName, start)
	command := "sudo -n sh -s"
	if adminUser == "root" {
		command = "sh -s"
	}
	if _, err = r.Run(ctx, command, strings.NewReader(script)); err != nil {
		return nil, err
	}
	profile.Controller = "ssh://" + address
	profile.ControllerUser = "bupt"
	profile.ControllerIdentityFile = d.IdentityFile
	profile.ControllerHostKey = hostKey
	profile.ControllerSocket = ""
	profile.Relay = remoteProfile.Relay
	profile.Relay.IdentityFile = d.IdentityFile
	updated := *cfg
	updated.Profiles = cloneProfiles(cfg.Profiles)
	updated.Profiles[cfg.Selected] = profile
	if start {
		if err = h.waitReady(ctx, &updated); err != nil {
			return nil, err
		}
	}
	backup, err := h.saveProfile(cfg, profile, false)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"mode": "remote", "profile": profile, "address": address, "account": "bupt", "client_id": d.ID, "identity_file": d.IdentityFile, "config": cfg.Paths.ConfigFile, "service": serviceName, "started": start, "remote_backup": "/etc/bupt/backups/" + stamp, "binary_sha256": digest, "recovery": map[string]any{"stop": "sudo systemctl stop " + serviceName + ".service", "uninstall": []string{"sudo systemctl disable --now " + serviceName + ".service", "preserve /var/lib/bupt and /etc/bupt (including the master key and encrypted data)", "remove only the managed unit and /usr/local/bin/bupt, then systemctl daemon-reload"}, "restore": "restore backed-up files from /etc/bupt/backups/" + stamp + " and restart the service; retain /var/lib/bupt/master.key together with data"}}
	if backup != "" {
		result["backup"] = backup
	}
	if start {
		var enrollment any
		if err = h.rpc(ctx, &updated, "key.import", map[string]any{"name": "device", "public_key": d.PublicKey}, &enrollment); err != nil {
			return result, err
		}
		result["key"] = enrollment
	}
	return result, nil
}

const remotePreflight = `set -eu
[ "$(uname -s)" = Linux ] || { echo 'remote controller requires Linux' >&2; exit 1; }
command -v systemctl >/dev/null
command -v sha256sum >/dev/null
command -v getent >/dev/null
if [ "$(id -u)" != 0 ]; then sudo -n true; fi
uname -m
`

func remoteInstallScript(stage, stamp, digest, service string, start bool) string {
	header := "set -eu\numask 077\nstage=" + quoteShell(stage) + "\nbackup=" + quoteShell("/etc/bupt/backups/"+stamp) + "\nexpected_digest=" + quoteShell(digest) + "\nservice=" + quoteShell(service) + "\n"
	end := "systemctl daemon-reload\nsystemctl enable \"$service.service\"\n"
	if start {
		end += "systemctl restart \"$service.service\"\nsystemctl is-active --quiet \"$service.service\"\n"
	}
	return header + remoteInstallBody + end
}

// All dynamic values are single-quoted inputs. The body modifies only the bupt
// account, its own private data/config, binary and one explicitly named unit.
const remoteInstallBody = `[ "$(id -u)" = 0 ] || { echo 'root setup privilege is required' >&2; exit 1; }
test -d "$stage" && test ! -L "$stage"
printf '%s  %s\n' "$expected_digest" "$stage/bupt" | sha256sum -c - >/dev/null
managed=no
if [ -e /etc/bupt/managed-v1 ]; then
  test -f /etc/bupt/managed-v1 && test ! -L /etc/bupt/managed-v1
  test "$(stat -c %u /etc/bupt/managed-v1)" = 0
  test "$(cat /etc/bupt/managed-v1)" = "bupt-installer-v1:$service" || { echo 'another bupt service already owns this installation' >&2; exit 1; }
  managed=yes
fi
if getent passwd bupt >/dev/null; then
  [ "$managed" = yes ] || { echo 'refusing an existing, unmanaged bupt account' >&2; exit 1; }
  [ "$(getent passwd bupt | cut -d: -f6)" = /var/lib/bupt ]
  [ "$(getent passwd bupt | cut -d: -f7)" = /bin/sh ]
  [ "$(id -u bupt)" != 0 ]
  [ "$(id -gn bupt)" = bupt ]
  [ "$(id -Gn bupt)" = bupt ] || { echo 'managed account has unexpected supplementary groups' >&2; exit 1; }
else
  [ "$managed" = no ] || { echo 'managed account is missing; inspect recovery data first' >&2; exit 1; }
  for owned in /var/lib/bupt /etc/bupt /usr/local/bin/bupt "/etc/systemd/system/$service.service"; do
    [ ! -e "$owned" ] && [ ! -L "$owned" ] || { echo 'refusing existing unmanaged installation paths' >&2; exit 1; }
  done
  useradd --system --user-group --create-home --home-dir /var/lib/bupt --shell /bin/sh bupt
fi
[ ! -e "/etc/systemd/system/$service.service.d" ] && [ ! -e "/run/systemd/system/$service.service.d" ] || { echo 'existing systemd drop-ins require inspection before installation' >&2; exit 1; }
for dir in /var/lib/bupt /var/lib/bupt/data /var/lib/bupt/state /var/lib/bupt/.ssh; do
  [ ! -L "$dir" ] || { echo 'refusing symlink data directories' >&2; exit 1; }
  install -d -m 0700 -o bupt -g bupt "$dir"
done
for dir in /etc/bupt /etc/bupt/backups; do
  [ ! -L "$dir" ] || { echo 'refusing symlink configuration directories' >&2; exit 1; }
done
install -d -m 0755 -o root -g root /etc/bupt
install -d -m 0700 -o root -g root /etc/bupt/backups "$backup"
printf 'bupt-installer-v1:%s\n' "$service" > /etc/bupt/managed-v1
chmod 0600 /etc/bupt/managed-v1
backup_file() {
  if [ -e "$1" ] || [ -L "$1" ]; then
    [ -f "$1" ] && [ ! -L "$1" ] || { echo 'refusing symlink or non-file installation target' >&2; exit 1; }
    cp -p -- "$1" "$backup/$2"
    chmod 0600 "$backup/$2"
  fi
}
backup_file /usr/local/bin/bupt bupt
backup_file /etc/bupt/config.toml config.toml
backup_file /etc/bupt/bootstrap-client.json bootstrap-client.json
backup_file "/etc/systemd/system/$service.service" service.unit
backup_file /var/lib/bupt/.ssh/authorized_keys authorized_keys
if [ -e /var/lib/bupt/master.key ] || [ -L /var/lib/bupt/master.key ]; then
  test -f /var/lib/bupt/master.key && test ! -L /var/lib/bupt/master.key
  test "$(stat -c %a /var/lib/bupt/master.key)" = 600
  test "$(stat -c %u /var/lib/bupt/master.key)" = "$(id -u bupt)"
  test "$(wc -c < /var/lib/bupt/master.key)" -eq 32
else
  dd if=/dev/urandom of=/var/lib/bupt/master.key bs=32 count=1 status=none
  chmod 0600 /var/lib/bupt/master.key
  chown bupt:bupt /var/lib/bupt/master.key
fi
[ ! -e /usr/local/bin/bupt.new ] && [ ! -L /usr/local/bin/bupt.new ] || { echo 'stale binary staging path requires inspection' >&2; exit 1; }
install -m 0755 -o root -g root "$stage/bupt" /usr/local/bin/bupt.new
mv -f /usr/local/bin/bupt.new /usr/local/bin/bupt
install -m 0644 -o root -g root "$stage/config.toml" /etc/bupt/config.toml
install -m 0644 -o root -g root "$stage/bootstrap-client.json" /etc/bupt/bootstrap-client.json
install -m 0644 -o root -g root "$stage/bupt-agent.service" "/etc/systemd/system/$service.service"
touch /var/lib/bupt/.ssh/authorized_keys
if [ ! -s /var/lib/bupt/.ssh/authorized_keys ]; then
  {
    printf '# BEGIN BUPT MANAGED\n'
    cat "$stage/operator-key"
    printf '# END BUPT MANAGED\n'
  } > /var/lib/bupt/.ssh/authorized_keys
else
  grep -Fqx '# BEGIN BUPT MANAGED' /var/lib/bupt/.ssh/authorized_keys && grep -Fqx '# END BUPT MANAGED' /var/lib/bupt/.ssh/authorized_keys || { echo 'existing authorized keys have no managed block; inspect before replacing' >&2; exit 1; }
fi
chown bupt:bupt /var/lib/bupt/.ssh/authorized_keys
chmod 0600 /var/lib/bupt/.ssh/authorized_keys
`
