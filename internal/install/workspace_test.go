package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
)

// A controller in these tests becomes reachable only when its simulated
// service manager starts it. No test installs or loads a real user service.
func TestWorkspaceCreatesAndResumesManagedService(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		t.Run(platform, func(t *testing.T) {
			h, cfg, _ := testSetup(t)
			h.goos = platform
			if err := os.MkdirAll(cfg.Paths.ConfigDir, 0700); err != nil {
				t.Fatal(err)
			}
			configText := []byte("# Preserve the operator's exact configuration.\nversion = 1\n")
			if err := os.WriteFile(cfg.Paths.ConfigFile, configText, 0600); err != nil {
				t.Fatal(err)
			}
			action, unit, err := h.inspectWorkspace(cfg)
			if err != nil || action != WorkspaceInitialize {
				t.Fatal(action, err)
			}
			var commands []string
			ready, loaded := false, false
			var enrollments int
			h.rpc = func(_ context.Context, _ *config.Config, method string, _, _ any) error {
				if method == "key.import" {
					enrollments++
					return nil
				}
				if !ready {
					return domain.E("AGENT_NOT_RUNNING", "fixture controller is down")
				}
				return nil
			}
			h.run = func(_ context.Context, name string, args []string, _ io.Reader, out, _ io.Writer) error {
				command := name + " " + strings.Join(args, " ")
				commands = append(commands, command)
				switch {
				case slices.Contains(args, "--property=LoadState"):
					io.WriteString(out, "not-found\n")
				case slices.Contains(args, "--property=FragmentPath,DropInPaths,NeedDaemonReload"):
					io.WriteString(out, "DropInPaths=\nFragmentPath="+unit.Path+"\nNeedDaemonReload=no\n")
				case args[0] == "print":
					if !loaded {
						return errors.New("service not found")
					}
					io.WriteString(out, "\tpath = "+unit.Path+"\n")
				case args[0] == "bootstrap" || args[0] == "kickstart" || slices.Contains(args, "start") || slices.Contains(args, "--now"):
					ready, loaded = true, true
				}
				return nil
			}
			start := func(action WorkspaceAction) error {
				_, err := h.handle(t.Context(), cfg, "workspace.start", map[string]any{"action": string(action)}, nil, nil, nil)
				return err
			}
			if err = start(action); err != nil || !ready || enrollments != 1 {
				t.Fatal(err, commands, enrollments)
			}
			if disk, _ := os.ReadFile(cfg.Paths.ConfigFile); !bytes.Equal(disk, configText) {
				t.Fatal("configuration was modified")
			}
			if _, err := os.Stat(filepath.Join(cfg.Paths.StateDir, "service.json")); err != nil {
				t.Fatal(err)
			}
			initial, err := os.ReadFile(unit.Path)
			if err != nil {
				t.Fatal(err)
			}
			before := len(commands)
			if err = start(WorkspaceInitialize); err != nil || len(commands) != before || enrollments != 1 {
				t.Fatal("ready controller was reinstalled", err, commands)
			}
			ready = false
			if platform == "darwin" {
				// The workstation cutover uses a plist editor that reformats XML.
				initial = bytes.ReplaceAll(initial, []byte("><"), []byte(">\n\t<"))
				if err = os.WriteFile(unit.Path, initial, 0600); err != nil {
					t.Fatal(err)
				}
			}
			action, _, err = h.inspectWorkspace(cfg)
			if err != nil || action != WorkspaceResume {
				t.Fatal(action, err)
			}
			if err = start(WorkspaceInitialize); err == nil || len(commands) != before {
				t.Fatal("stale initial action resumed service", err, commands)
			}
			if err = start(action); err != nil || !ready || enrollments != 1 {
				t.Fatal(err, commands, enrollments)
			}
			if disk, _ := os.ReadFile(unit.Path); !bytes.Equal(disk, initial) {
				t.Fatal("resume rewrote original service")
			}
			for _, cmd := range commands {
				if strings.Contains(cmd, "bootout") || strings.Contains(cmd, "restart") || strings.Contains(cmd, "kickstart -k") {
					t.Fatal("startup displaced an existing job", cmd)
				}
			}
		})
	}
}

func TestWorkspaceProcessRequiresExplicitChoiceAndVerifiableOldPID(t *testing.T) {
	h, cfg, _ := testSetup(t)
	reachable := false
	var launched, enrolled int
	h.rpc = func(_ context.Context, _ *config.Config, method string, _, _ any) error {
		if !reachable {
			return domain.E("AGENT_NOT_RUNNING", "fixture")
		}
		if method == "key.import" {
			enrolled++
		}
		return nil
	}
	h.run = func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
		if !slices.Contains(args, "--property=LoadState") {
			t.Fatal("unexpected manager change", args)
		}
		return exec.ErrNotFound
	}
	// Use one test-owned PID for the collision check, then an exited child's
	// real PID for a known-safe restart.
	child := exec.Command("true")
	if err := child.Run(); err != nil {
		t.Fatal(err)
	}
	deadPID := child.Process.Pid
	pid := os.Getpid()
	wantArgs := agentArgs(cfg, filepath.Join(cfg.Paths.DataDir, "master.key"))
	h.start = func(executable string, args []string, log string) (int, error) {
		if executable != h.executable || !slices.Equal(args, wantArgs) || log != filepath.Join(cfg.Paths.StateDir, "agent.log") {
			t.Fatal("wrong process invocation", args)
		}
		launched++
		reachable = true
		return pid, nil
	}
	start := func(action WorkspaceAction) error {
		_, err := h.handle(t.Context(), cfg, "workspace.start", map[string]any{"action": string(action)}, nil, nil, nil)
		return err
	}
	var de *domain.Error
	if err := start(WorkspaceInitialize); !errors.As(err, &de) || de.Code != "USER_SERVICE_UNAVAILABLE" || launched != 0 {
		t.Fatal("native-service detection triggered a process", err)
	}
	if _, err := os.Stat(cfg.Paths.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("choosing fallback should be side effect free", err)
	}
	if err := start(WorkspaceProcess); err != nil || launched != 1 || enrolled != 1 {
		t.Fatal("explicit fallback failed", err, launched, enrolled)
	}
	action, record, err := h.inspectWorkspace(cfg)
	if err != nil || action != WorkspaceResumeProcess || !slices.Equal(record.Arguments, wantArgs) || record.SetupPending || record.PID != pid {
		t.Fatal(action, record, err)
	}
	reachable = false
	if err := start(action); err == nil || launched != 1 {
		t.Fatal("started over a live PID", err, launched)
	}
	original := slices.Clone(record.Arguments)
	record.Arguments = append(slices.Clone(original), "--unexpected")
	if err := h.writeWorkspaceRecord(cfg, record, false); err != nil {
		t.Fatal(err)
	}
	if err := start(action); err == nil || launched != 1 {
		t.Fatal("unsafe process argv resumed", err)
	}
	record.Arguments = original
	record.PID = deadPID
	pid = deadPID
	if err := h.writeWorkspaceRecord(cfg, record, false); err != nil {
		t.Fatal(err)
	}
	if err := start(action); err != nil || launched != 2 || enrolled != 1 {
		t.Fatal("recorded process did not resume correctly", err, launched, enrolled)
	}
}

func TestWorkspaceRejectsConflictsAndNonlocalTargets(t *testing.T) {
	for _, tc := range []string{"remote", "transient-socket", "partial", "foreign-unit", "record-mismatch", "independent", "edited-unit", "healthy-host-job", "permissions"} {
		t.Run(tc, func(t *testing.T) {
			h, cfg, _ := testSetup(t)
			record, unit := h.userService(cfg, filepath.Join(cfg.Paths.DataDir, "master.key"), "bupt-agent")
			write := func(path string, data []byte) {
				if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
					t.Fatal(e)
				}
				if e := os.WriteFile(path, data, 0600); e != nil {
					t.Fatal(e)
				}
			}
			saveRecord := func() { b, _ := json.Marshal(record); write(filepath.Join(cfg.Paths.StateDir, "service.json"), b) }
			switch tc {
			case "remote", "transient-socket":
				p := cfg.Profiles[cfg.Selected]
				if tc == "remote" {
					p.Controller = "ssh://fixture.invalid"
				} else {
					p.ControllerSocket = filepath.Join(cfg.Paths.StateDir, "custom.sock")
				}
				cfg.Profiles[cfg.Selected] = p
			case "foreign-unit":
				write(record.Path, []byte("another service\n"))
			case "partial":
				write(filepath.Join(cfg.Paths.DataDir, "credentials.db"), []byte("fixture"))
			case "record-mismatch":
				record.Config += "-other"
				saveRecord()
				write(record.Path, unit)
			case "independent":
				record.Manager = "process"
				saveRecord()
			case "edited-unit":
				saveRecord()
				write(record.Path, bytes.ReplaceAll(unit, []byte(cfg.Paths.Socket), []byte("/other/agent.sock")))
			}
			h.rpc = func(_ context.Context, _ *config.Config, _ string, _, _ any) error {
				if tc == "remote" {
					t.Fatal("remote controller was contacted")
				}
				if tc == "permissions" {
					return domain.E("CONTROLLER_CONNECT", "permission denied")
				}
				return domain.E("AGENT_NOT_RUNNING", "fixture")
			}
			h.run = func(_ context.Context, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
				if tc != "healthy-host-job" || !slices.Contains(args, "--property=LoadState") {
					t.Fatalf("unexpected service mutation %v", args)
				}
				io.WriteString(out, "loaded\n")
				return nil
			}
			_, err := h.handle(t.Context(), cfg, "workspace.start", map[string]any{"action": string(WorkspaceInitialize)}, nil, nil, nil)
			if err == nil {
				t.Fatal("unexpected setup")
			}
			if _, err := os.Stat(filepath.Join(cfg.Paths.DataDir, "master.key")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("created key material", err)
			}
		})
	}
}

func TestWorkspaceLockAndActionChecks(t *testing.T) {
	h, cfg, _ := testSetup(t)
	started, release := make(chan struct{}), make(chan struct{})
	h.rpc = func(context.Context, *config.Config, string, any, any) error {
		return domain.E("AGENT_NOT_RUNNING", "fixture")
	}
	h.run = func(_ context.Context, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
		if !slices.Contains(args, "--property=LoadState") {
			t.Fatalf("unexpected command %v", args)
		}
		close(started)
		<-release
		io.WriteString(out, "loaded\n")
		return nil
	}
	m := map[string]any{"action": string(WorkspaceInitialize)}
	done := make(chan error, 1)
	go func() { _, e := h.handle(t.Context(), cfg, "workspace.start", m, nil, nil, nil); done <- e }()
	<-started
	// A different XDG/state dir still shares the user's service manager namespace.
	otherRoot := t.TempDir()
	other, err := config.Load(config.Options{HomeDir: otherRoot, Env: map[string]string{}, NoProject: true})
	if err != nil {
		t.Fatal(err)
	}
	_, e := h.handle(t.Context(), other, "workspace.start", m, nil, nil, nil)
	close(release)
	if e == nil || !strings.Contains(e.Error(), "另一个终端") {
		t.Fatalf("start contention: %v", e)
	}
	if e = <-done; e == nil {
		t.Fatal("occupied user service should not be replaced")
	}
	if _, err := os.Stat(other.Paths.ConfigDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("contending setup wrote to the other workspace", err)
	}
}

func TestInterruptedWorkspaceResumesOnlyTheOriginalSetup(t *testing.T) {
	h, cfg, _ := testSetup(t)
	record, _ := h.userService(cfg, filepath.Join(cfg.Paths.DataDir, "master.key"), "bupt-agent")
	fragment := "FragmentPath=" + record.Path + "\n"
	properties := fragment + "DropInPaths=/etc/systemd/user/bupt-agent.service.d/custom.conf\nNeedDaemonReload=no\n"
	started := false
	h.rpc = func(_ context.Context, _ *config.Config, method string, _, _ any) error {
		if !started {
			return domain.E("AGENT_NOT_RUNNING", "fixture")
		}
		return nil
	}
	var enrollment int
	rpc := h.rpc
	h.rpc = func(ctx context.Context, cfg *config.Config, method string, p, out any) error {
		if method == "key.import" {
			enrollment++
		}
		return rpc(ctx, cfg, method, p, out)
	}
	h.run = func(_ context.Context, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
		switch {
		case slices.Contains(args, "--property=LoadState"):
			io.WriteString(out, "not-found\n")
		case slices.Contains(args, "--property=FragmentPath,DropInPaths,NeedDaemonReload"):
			io.WriteString(out, properties)
		case slices.Contains(args, "enable"):
			started = true
		}
		return nil
	}
	start := func(action WorkspaceAction) error {
		_, err := h.handle(t.Context(), cfg, "workspace.start", map[string]any{"action": string(action)}, nil, nil, nil)
		return err
	}
	if err := start(WorkspaceInitialize); err == nil || started {
		t.Fatal("a systemd drop-in altered the requested service", err)
	}
	action, pending, err := h.inspectWorkspace(cfg)
	if err != nil || action != WorkspaceResume || !pending.SetupPending {
		t.Fatal("failed initial setup not recoverable", action, err)
	}
	master := filepath.Join(cfg.Paths.DataDir, "master.key")
	key, err := os.ReadFile(master)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Paths.DataDir, "credentials.db"), []byte("existing fixture vault"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(master); err != nil {
		t.Fatal(err)
	}
	if err := start(action); err == nil {
		t.Fatal("retry replaced a missing vault master key")
	}
	if _, err := os.Stat(master); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err := os.WriteFile(master, key, 0600); err != nil {
		t.Fatal(err)
	}
	properties = fragment + "DropInPaths=\nNeedDaemonReload=yes\n"
	if err := start(action); err == nil || started {
		t.Fatal("stale effective unit was started", err)
	}
	properties = fragment + "DropInPaths=\nNeedDaemonReload=no\n"
	if err := start(action); err != nil || !started || enrollment != 1 {
		t.Fatal(err, enrollment)
	}
	_, finished, err := h.inspectWorkspace(cfg)
	if err != nil || finished.SetupPending {
		t.Fatal("pending record not committed", finished, err)
	}
	unchanged, err := os.ReadFile(master)
	if err != nil || !bytes.Equal(key, unchanged) {
		t.Fatal("retry replaced master key", err)
	}
	if err := start(action); err != nil || enrollment != 1 {
		t.Fatal("completed enrollment replayed", err, enrollment)
	}
}

// A reachable controller can outlive the enrolling terminal. Recovery finishes
// just the pending local enrollment, retaining its PID and user configuration.
func TestReachablePendingWorkspaceCompletesWithoutRestart(t *testing.T) {
	h, cfg, _ := testSetup(t)
	h.executable, _ = os.Executable()
	r := serviceRecord{Name: "bupt-agent", Manager: "process", PID: os.Getpid(), Config: cfg.Paths.ConfigFile, Executable: h.executable, Arguments: agentArgs(cfg, filepath.Join(cfg.Paths.DataDir, "master.key")), SetupPending: true}
	if err := ensurePrivateDir(cfg.Paths.StateDir); err != nil {
		t.Fatal(err)
	}
	if err := h.writeWorkspaceRecord(cfg, r, true); err != nil {
		t.Fatal(err)
	}
	// Prepare local material as the original terminal had done before launch.
	if _, err := h.setup(t.Context(), cfg, "workspace.start", map[string]any{"install": false, "start": false}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	fail := true
	h.rpc = func(_ context.Context, _ *config.Config, method string, _, _ any) error {
		if method == "key.import" && fail {
			return errors.New("interrupted")
		}
		return nil
	}
	h.run = func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("online enrollment invoked service manager")
		return nil
	}
	h.start = func(string, []string, string) (int, error) {
		t.Fatal("online enrollment launched a process")
		return -1, nil
	}
	complete := func() error {
		_, err := h.handle(t.Context(), cfg, "workspace.start", map[string]any{"action": string(WorkspaceResumeProcess)}, nil, nil, nil)
		return err
	}
	if err := complete(); err == nil {
		t.Fatal("failed enrollment marked complete")
	}
	if got := PendingWorkspace(cfg); got != WorkspaceResumeProcess {
		t.Fatal("connected window cannot resume pending enrollment", got)
	}
	fail = false
	if err := complete(); err != nil {
		t.Fatal(err)
	}
	if got := PendingWorkspace(cfg); got != "" {
		t.Fatal("completed registration still offered", got)
	}
}
