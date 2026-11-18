package install

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/config"
)

func (h *handler) installUserService(ctx context.Context, cfg *config.Config, keyPath, name string, start bool, errOut io.Writer) (serviceRecord, string, error) {
	record := serviceRecord{Name: name, Executable: h.executable, Config: cfg.Paths.ConfigFile}
	var contents []byte
	if h.goos == "darwin" {
		record.Manager = "launchd"
		record.Name = "cn.edu.bupt.cli." + name
		record.Path = filepath.Join(h.home, "Library", "LaunchAgents", record.Name+".plist")
		contents = launchdUnit(record.Name, h.executable, agentArgs(cfg, keyPath), filepath.Join(cfg.Paths.StateDir, "agent.log"))
	} else {
		record.Manager = "systemd-user"
		base := h.userConfigHome
		if base == "" {
			base = filepath.Join(h.home, ".config")
		}
		record.Path = filepath.Join(base, "systemd", "user", name+".service")
		contents = userUnit(h.executable, agentArgs(cfg, keyPath))
	}
	backup, err := h.replaceFile(record.Path, contents, 0600)
	if err != nil {
		return record, backup, err
	}
	if h.goos == "darwin" {
		if !start {
			return record, backup, nil
		}
		err = h.reloadLaunchd(ctx, record, backup)
		return record, backup, err
	}
	if err = h.run(ctx, "systemctl", []string{"--user", "daemon-reload"}, nil, io.Discard, errOut); err != nil {
		return record, backup, err
	}
	args := []string{"--user", "enable"}
	if start {
		args = append(args, "--now")
	}
	args = append(args, name+".service")
	err = h.run(ctx, "systemctl", args, nil, io.Discard, errOut)
	if err == nil && start {
		err = h.run(ctx, "systemctl", []string{"--user", "restart", name + ".service"}, nil, io.Discard, errOut)
	}
	return record, backup, err
}

const launchdReloadTimeout = 10 * time.Second
const launchdRetryInterval = 200 * time.Millisecond

// A successful bootout may precede removal from launchd's service table. Even
// after print reports absence, bootstrap can briefly return EIO while removal
// finishes. Observe both transitions within one deadline; controller RPC
// readiness is checked separately by setup after this registration succeeds.
func (h *handler) reloadLaunchd(ctx context.Context, record serviceRecord, backup string) error {
	ctx, cancel := context.WithTimeout(ctx, launchdReloadTimeout)
	defer cancel()
	domain := "gui/" + strconv.Itoa(h.uid)
	target := domain + "/" + record.Name
	pause := h.wait
	if pause == nil {
		pause = waitContext
	}
	run := func(args ...string) (string, error) {
		var stderr bytes.Buffer
		err := h.run(ctx, "launchctl", args, nil, io.Discard, &boundedWriter{w: &stderr, left: 16 << 10})
		return launchdDiagnostic(stderr.String()), err
	}
	fail := func(stage string, cause error, detail string) error {
		message := "launchd " + stage + " failed for " + record.Name
		if detail != "" {
			message += ": " + detail
		}
		message += "; plist retained at " + record.Path
		message += "; inspect with launchctl print " + quoteShell(target)
		message += "; if the service is absent, recover as the same user with launchctl bootstrap " + quoteShell(domain) + " " + quoteShell(record.Path)
		if backup != "" {
			message += "; previous plist backup: " + backup
		}
		return fmt.Errorf("%s: %w", message, cause)
	}
	if err := ctx.Err(); err != nil {
		return fail("reload", err, "")
	}
	detail, err := run("print", target)
	if ctx.Err() != nil {
		return fail("inspection", ctx.Err(), detail)
	}
	if err == nil {
		detail, err = run("bootout", target)
		if ctx.Err() != nil {
			return fail("bootout", ctx.Err(), detail)
		}
		if err != nil {
			return fail("bootout", err, detail)
		}
		for {
			detail, err = run("print", target)
			if ctx.Err() != nil {
				return fail("removal", ctx.Err(), detail)
			}
			if err != nil {
				break
			}
			if err = pause(ctx, launchdRetryInterval); err != nil {
				return fail("removal", err, "service is still registered after bootout")
			}
		}
	}
	for {
		detail, err = run("bootstrap", domain, record.Path)
		if ctx.Err() != nil {
			return fail("bootstrap", ctx.Err(), detail)
		}
		if err == nil {
			for {
				detail, err = run("print", target)
				if ctx.Err() != nil {
					return fail("registration", ctx.Err(), detail)
				}
				if err == nil {
					return nil
				}
				if err = pause(ctx, launchdRetryInterval); err != nil {
					return fail("registration", err, detail)
				}
			}
		}
		if !transientLaunchdBootstrap(err, detail) {
			return fail("bootstrap", err, detail)
		}
		// Some EIO responses accompany a successful registration. Avoid a second
		// bootstrap in that case, which would itself fail because it is loaded.
		if _, probeErr := run("print", target); probeErr == nil {
			return nil
		}
		if waitErr := pause(ctx, launchdRetryInterval); waitErr != nil {
			return fail("bootstrap", waitErr, detail)
		}
	}
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func transientLaunchdBootstrap(err error, detail string) bool {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return exit.ExitCode() == 5
	}
	text := strings.ToLower(detail + " " + err.Error())
	return strings.Contains(text, "input/output error") || strings.Contains(text, "exit status 5")
}

func launchdDiagnostic(text string) string {
	lines := []string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		// launchctl's generic EIO advice suggests using root. This installer is a
		// user agent; report the real error with a user-domain recovery instead.
		if line == "" || strings.Contains(strings.ToLower(line), "re-running the command as root") {
			continue
		}
		lines = append(lines, line)
	}
	result := strings.Join(lines, "; ")
	if len(result) > 2048 {
		result = result[:2048] + "..."
	}
	return result
}

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func launchdUnit(label, binary string, args []string, log string) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n")
	fmt.Fprintf(&b, "<key>Label</key><string>%s</string>\n<key>ProgramArguments</key><array>\n", xmlText(label))
	for _, arg := range append([]string{binary}, args...) {
		fmt.Fprintf(&b, "<string>%s</string>\n", xmlText(arg))
	}
	b.WriteString("</array>\n<key>RunAtLoad</key><true/>\n<key>KeepAlive</key><true/>\n<key>ThrottleInterval</key><integer>5</integer>\n<key>Umask</key><integer>63</integer>\n")
	fmt.Fprintf(&b, "<key>StandardOutPath</key><string>%s</string>\n<key>StandardErrorPath</key><string>%s</string>\n", xmlText(log), xmlText(log))
	b.WriteString("</dict></plist>\n")
	return []byte(b.String())
}

// systemd parses ExecStart directly (not through a shell). Double percent/dollar
// signs suppress specifier/environment expansion in user-controlled paths.
func unitArg(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	s = strings.ReplaceAll(s, "$", "$$")
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return "\"" + s + "\""
}
func unitCommand(binary string, args []string) string {
	parts := []string{unitArg(binary)}
	for _, arg := range args {
		parts = append(parts, unitArg(arg))
	}
	return strings.Join(parts, " ")
}
func userUnit(binary string, args []string) []byte {
	return []byte("# Managed by bupt; previous contents are backed up on replacement.\n[Unit]\nDescription=BUPT personal compute controller\nAfter=network.target\n\n[Service]\nType=simple\nExecStart=" + unitCommand(binary, args) + "\nRestart=on-failure\nRestartSec=5s\nTimeoutStopSec=15s\nUMask=0077\nNoNewPrivileges=true\n\n[Install]\nWantedBy=default.target\n")
}
func remoteUnit(serviceName string) []byte {
	args := []string{"agent", "run", "--no-project", "--config", "/etc/bupt/config.toml", "--data-dir", "/var/lib/bupt/data", "--state-dir", "/var/lib/bupt/state", "--socket", "/var/lib/bupt/state/agent.sock", "--master-key-file", "/var/lib/bupt/master.key", "--authorized-keys", "/var/lib/bupt/.ssh/authorized_keys", "--executable", "/usr/local/bin/bupt", "--bootstrap-client", "/etc/bupt/bootstrap-client.json"}
	return []byte("# Managed by bupt installer v1\n[Unit]\nDescription=BUPT compute controller\nWants=network-online.target\nAfter=network-online.target\n\n[Service]\nType=simple\nUser=bupt\nGroup=bupt\nWorkingDirectory=/var/lib/bupt\nExecStart=" + unitCommand("/usr/local/bin/bupt", args) + "\nRestart=on-failure\nRestartSec=5s\nTimeoutStopSec=15s\nUMask=0077\nNoNewPrivileges=true\nPrivateTmp=true\nProtectSystem=strict\nProtectHome=true\nReadWritePaths=/var/lib/bupt\n\n[Install]\nWantedBy=multi-user.target\n")
}
