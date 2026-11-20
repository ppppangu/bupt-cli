package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/client"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/wire"
)

func TestCreateCostAuthorizationAndAllocateOnly(t *testing.T) {
	c, school, _ := lifecycleController(t)
	ctx := t.Context()
	p := lifecycleParams(map[string]any{"name": "lab", "spec_id": "9", "max_runtime": "20m", "allocate_only": true, "wait": false, "idempotency_key": "create"})
	for _, check := range []struct{ rate, code string }{{"", "COST_AUTH_REQUIRED"}, {"not-a-rate", "COST_AUTH_REQUIRED"}, {"1.2499999999999999999", "COST_LIMIT_EXCEEDED"}} {
		p.Set("max_points_per_day", check.rate)
		if _, err := c.ManagedOperation(ctx, "test", "create", p); managedCode(err) != check.code {
			t.Fatalf("rate %q: %v", check.rate, err)
		}
	}
	runs, err := c.Store.ListRuns("test")
	if err != nil || len(runs) != 0 || school.count("CreateAppTask") != 0 {
		t.Fatal(runs, err)
	}
	p.Set("max_points_per_day", "1.25")
	out, err := c.ManagedOperation(ctx, "test", "create", p)
	if err != nil {
		t.Fatal(err)
	}
	r := lifecycleWait(t, c, out.(domain.Run).ID, func(r domain.Run) bool { return r.State == "allocated" })
	retry, err := c.ManagedOperation(ctx, "test", "create", p)
	if err != nil || retry.(domain.Run).ID != r.ID || school.count("CreateAppTask") != 1 || school.count("AppReserveTask") != 1 || school.count("StartAppTask") != 0 {
		t.Fatal(retry, err)
	}
}

func TestAllocatedInstanceStartReturnsSameReceiptAfterRunning(t *testing.T) {
	c, school, _ := lifecycleController(t)
	ctx := t.Context()
	create := lifecycleParams(map[string]any{
		"name": "lab", "spec_id": "9", "max_runtime": "20m", "allocate_only": true,
		"max_points_per_day": "1.25", "wait": false, "idempotency_key": "allocation",
	})
	out, err := c.ManagedOperation(ctx, "test", "create", create)
	if err != nil {
		t.Fatal(err)
	}
	cycle := lifecycleWait(t, c, out.(domain.Run).ID, func(r domain.Run) bool { return r.State == "allocated" })
	start := lifecycleParams(map[string]any{"target": "lab", "idempotency_key": "start-allocated", "wait": false, "no_ssh": true})
	out, err = c.ManagedOperation(ctx, "test", "start", start)
	if err != nil || out.(domain.Run).ID != cycle.ID {
		t.Fatal(out, err)
	}
	lifecycleWait(t, c, cycle.ID, func(r domain.Run) bool { return r.State == "ready" })
	out, err = c.ManagedOperation(ctx, "test", "start", start)
	if err != nil || out.(domain.Run).ID != cycle.ID || school.count("StartAppTask") != 1 {
		t.Fatal(out, err, school.count("StartAppTask"))
	}
	start.Set("node", "changed")
	if _, err := c.ManagedOperation(ctx, "test", "start", start); managedCode(err) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(err)
	}
}

func TestAllocationRechecksAuthorizedRateBeforeCreating(t *testing.T) {
	c, school, _ := lifecycleController(t)
	ctx := t.Context()
	p := lifecycleParams(map[string]any{
		"name": "lab", "spec_id": "9", "max_runtime": "20m", "idempotency_key": "price-change",
		"max_points_per_day": "1.25",
	})
	run, err := c.prepareManagedRun(ctx, "test", "create", p, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	school.mu.Lock()
	school.price = "1.250000000000000001"
	school.mu.Unlock()
	if _, err := c.advanceRun(ctx, run.ID); managedCode(err) != "COST_LIMIT_EXCEEDED" {
		t.Fatal(err)
	}
	final, err := c.Store.GetRun(run.ID)
	if err != nil || final.State != "failed" || school.count("CreateAppTask") != 0 {
		t.Fatal(final, err)
	}
	p.Set("idempotency_key", "unapproved-job")
	p.Set("command", []string{"true"})
	delete(p, "max_points_per_day")
	if _, err := c.RunJob(ctx, "test", p); managedCode(err) != "COST_AUTH_REQUIRED" || school.count("CreateAppTask") != 0 {
		t.Fatal(err)
	}
}

func TestSocketDryRunDoesNotStopAgentOrOpenStream(t *testing.T) {
	c, _, _ := lifecycleController(t)
	call := func(method string, dry bool) domain.RPCResponse {
		t.Helper()
		server, peer := net.Pipe()
		defer peer.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer server.Close()
			c.serveConnection(t.Context(), server)
		}()
		_ = peer.SetDeadline(time.Now().Add(3 * time.Second))
		req := domain.RPCRequest{ID: "preview-test", Method: method, Profile: "test", Params: domain.Result(map[string]any{"dry_run": dry})}
		if err := wire.WriteJSON(peer, req); err != nil {
			t.Fatal(err)
		}
		var reply domain.RPCResponse
		if err := wire.ReadJSON(bufio.NewReader(peer), &reply); err != nil {
			t.Fatal(err)
		}
		<-done
		return reply
	}
	if got := call("agent.stop", true); got.Error != nil || !bytes.Contains(got.Result, []byte(`"dry_run":true`)) || c.ctx.Err() != nil {
		t.Fatal(got, c.ctx.Err())
	}
	if got := call("connect.upload", true); got.Error == nil || got.Error.Code != "PREVIEW_UNSUPPORTED" {
		t.Fatal(got)
	}
	if got := call("agent.stop", false); got.Error != nil || c.ctx.Err() == nil {
		t.Fatal(got, c.ctx.Err())
	}
}

func TestCachedInstancesDoNotSynchronizeWithSchool(t *testing.T) {
	c, school, _ := lifecycleController(t)
	ctx := t.Context()
	if _, err := c.Instances(ctx, "test", true); managedCode(err) != "NO_CACHE" {
		t.Fatal(err)
	}
	if _, err := c.Instances(ctx, "test", false); err != nil {
		t.Fatal(err)
	}
	school.mu.Lock()
	before := school.syncCalls
	school.authExpired = true
	school.mu.Unlock()
	if _, err := c.Instances(ctx, "test", true); err != nil {
		t.Fatal(err)
	}
	school.mu.Lock()
	defer school.mu.Unlock()
	if school.syncCalls != before {
		t.Fatal("cached instance list called the school")
	}
}

func TestCancelQueueWaitReturnsAfterSchoolStateChanges(t *testing.T) {
	c, school, _ := lifecycleController(t)
	run := fixtureActiveRun(t, c, school, false)
	school.mu.Lock()
	school.snap.Instances[0].State = "reserve_queuing"
	school.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	p := lifecycleParams(map[string]any{"target": "20", "idempotency_key": "cancel-queue"})
	out, err := c.manualInstance(ctx, "test", "cancel-queue", p)
	if err != nil {
		t.Fatal(err)
	}
	id := out.(domain.TaskRecord).ID
	if _, err := c.waitOperation(ctx, "test", id); err != nil {
		t.Fatal(err)
	}
	final := lifecycleWait(t, c, run.ID, func(r domain.Run) bool { return managedRunTerminal(r.State) })
	if final.CleanupState != "manual_complete" || school.count("CancelAppReserveQueuingTask") != 1 || school.count("StartAppTask") != 0 {
		t.Fatal(final)
	}
}

func TestCreateWaitAndSSHThroughUnixSocketAndPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("requires a built CLI and a PTY")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the PTY harness")
	}
	c, school, _ := lifecycleController(t)
	sshd := newStreamTestSSH(t)
	route := sshd.route()
	school.sshFixture = &route
	dir, err := os.MkdirTemp("", "bupt-pty-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := *c.Configuration()
	cfg.Paths.DataDir = dir
	c.configuration.Store(&cfg)
	private, err := os.ReadFile(sshd.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "keys"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "test-automation"), private, 0600); err != nil {
		t.Fatal(err)
	}
	clear(private)
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("version = 1\ndefault_profile='test'\n[profiles.test]\nplatform_url='https://school.invalid'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "agent.sock")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	serving := make(chan error, 1)
	go func() { serving <- c.Serve(ctx, socket) }()
	defer func() { cancel(); <-serving }()
	for {
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
	bin := filepath.Join(dir, "bupt")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/bupt")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("%s\n%s", err, output)
	}
	harness := exec.CommandContext(ctx, python, "-c", ptyLifecycleScript, bin, path, socket, dir)
	harness.Env = append(os.Environ(), "TERM=xterm")
	if output, err := harness.CombinedOutput(); err != nil {
		t.Fatalf("%s\n%s", err, output)
	} else {
		t.Log(string(output))
	}
	if school.count("CreateAppTask") != 1 || school.count("StartAppTask") != 1 || school.count("AppReserveTask") != 1 {
		t.Fatal("creation was replayed or incomplete")
	}

	transport := client.Client{Config: &cfg, Profile: domain.Profile{Name: "test", ControllerSocket: socket}}
	var receipt struct {
		ID        string    `json:"id"`
		RestartID string    `json:"restart_id"`
		Deadline  time.Time `json:"deadline"`
	}
	original, err := c.GetRun("test", "lab")
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.Call(ctx, "instance.restart", map[string]any{"target": "lab", "wait": false, "idempotency_key": "restart-lab"}, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.RestartID == "" || !receipt.Deadline.Equal(original.Deadline) {
		t.Fatal(receipt)
	}
	var restarted domain.Run
	if err := transport.Call(ctx, "instance.wait", map[string]any{"operation": receipt.RestartID}, &restarted); err != nil {
		t.Fatal(err)
	}
	if restarted.State != "ready" || restarted.InstanceID != original.InstanceID || !restarted.Deadline.Equal(original.Deadline) || school.count("StopAppTask") != 1 || school.count("StartAppTask") != 2 {
		t.Fatal(restarted)
	}

	school.mu.Lock()
	school.authExpired = true
	school.mu.Unlock()
	var stdout, stderr bytes.Buffer
	code, err := transport.Session(ctx, "lab", connect.SessionOptions{Command: []string{"exit", "37"}}, nil, &stdout, &stderr, nil, nil)
	if err != nil || code != 37 || stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatal(code, stdout.String(), stderr.String(), err)
	}
	school.mu.Lock()
	school.authExpired = false
	school.mu.Unlock()
	operate := func(method, key, confirmation string) domain.TaskRecord {
		t.Helper()
		args := map[string]any{"target": "lab", "wait": false, "idempotency_key": key}
		if confirmation != "" {
			args["confirm"] = confirmation
		}
		var task domain.TaskRecord
		if err := transport.Call(ctx, method, args, &task); err != nil {
			t.Fatal(method, err)
		}
		if task.ID == "" {
			t.Fatal(method, "missing receipt")
		}
		if err := transport.Call(ctx, "instance.wait", map[string]any{"operation": task.ID}, &task); err != nil {
			t.Fatal(method, err)
		}
		return task
	}
	operate("instance.stop", "stop-lab", "")
	stopped, err := school.Sync(ctx)
	if err != nil || len(stopped.Instances) != 1 || stopped.Instances[0].State != "stopped" || !stopped.Instances[0].Reserved || school.count("ForceStopAppTask") != 0 || school.count("AppUnreserveTask") != 0 {
		t.Fatal(stopped.Instances, err)
	}
	lifecycleWait(t, c, restarted.ID, func(r domain.Run) bool { return managedRunTerminal(r.State) })
	operate("instance.release", "release-lab", "")
	var restored domain.Run
	if err := transport.Call(ctx, "instance.restore", map[string]any{"target": "lab", "max_runtime": "20m", "max_points_per_day": "1.25", "wait": false, "idempotency_key": "restore-lab"}, &restored); err != nil {
		t.Fatal(err)
	}
	if err := transport.Call(ctx, "instance.wait", map[string]any{"operation": restored.ID}, &restored); err != nil || restored.InstanceID != original.InstanceID || restored.State != "allocated" {
		t.Fatal(restored, err)
	}
	operate("instance.release", "release-restored-lab", "")
	var removed domain.TaskRecord
	if err := transport.Call(ctx, "instance.delete", map[string]any{"target": "lab", "confirm": "999", "wait": false}, &removed); managedCode(err) != "CONFIRM_REQUIRED" || school.count("RemoveAppTask") != 0 {
		t.Fatal(err)
	}
	operate("instance.delete", "delete-lab", "20")
	if school.count("RemoveAppTask") != 1 {
		t.Fatal("delete count")
	}

}

const ptyLifecycleScript = `
import errno, json, os, pty, select, subprocess, sys, time
bin, cfg, sock, work = sys.argv[1:]
base = [bin, "--no-project", "--config", cfg, "--socket", sock, "--timeout", "12s"]
env = {k:v for k,v in os.environ.items() if not k.startswith("BUPT_")}
env["HOME"] = work
def cli(*args):
    return subprocess.run(base + list(args), env=env, cwd=work, capture_output=True, check=True, text=True)
def terminal(args, steps):
    master, slave = pty.openpty()
    p = subprocess.Popen(base + args, env=env, cwd=work, stdin=slave, stdout=slave, stderr=slave)
    os.close(slave)
    output = bytearray()
    try:
        end = time.monotonic() + 18
        while time.monotonic() < end:
            if not select.select([master], [], [], 0.25)[0]:
                continue
            try:
                part = os.read(master, 8192)
            except OSError as e:
                if e.errno == errno.EIO:
                    break
                raise
            if not part:
                break
            output.extend(part)
            while steps and steps[0][0].encode() in output:
                _, reply = steps.pop(0)
                os.write(master, reply)
        return p.wait(timeout=2), output.decode("utf-8", errors="replace")
    finally:
        if p.poll() is None:
            p.kill()
            p.wait()
        os.close(master)
catalog = json.loads(cli("compute", "catalog", "--output", "json").stdout)
assert catalog["pools"][0]["templates"][0]["id"] == "9", catalog
code, out = terminal(["compute","instance","create","lab","--spec","9"], [("运行上限", b"20m\n"), ("确认提交", b"n\n")])
assert code == 130 and "操作 ID" not in out, out
code, out = terminal(["compute","instance","create","lab","--spec","9"], [("运行上限", b"20m\n"), ("确认提交", b"y\n")])
assert code == 0 and "操作 ID" in out and "1.25" in out and "预留" in out, out
ready = cli("compute", "instance", "wait", "lab", "--output", "json")
assert ready.stderr == "" and json.loads(ready.stdout)["ssh_state"] == "ready", ready
code, out = terminal(["ssh","lab"], [])
assert code == 0 and "SSH 认证成功" in out and "offline shell" in out, out
print("PTY catalog → cancel → create → wait → ssh: passed")
`

func (a *lifecyclePlatform) Estimate(context.Context, domain.ID, map[string]any) (map[string]any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	price := a.price
	if price == "" {
		price = "1.25"
	}
	return map[string]any{"points_per_day": json.Number(price)}, nil
}
func (a *lifecyclePlatform) Specs(context.Context) ([]domain.Spec, error) {
	return []domain.Spec{{ID: "9", Name: "gpu-a"}}, nil
}
func (a *lifecyclePlatform) AuthStatus(context.Context) domain.AuthStatus {
	return domain.AuthStatus{Gateway: "authenticated", Platform: "authenticated"}
}
func (a *lifecyclePlatform) Console(context.Context, domain.ID) (io.ReadWriteCloser, error) {
	if a.sshFixture == nil {
		return nil, domain.E("CONSOLE_UNAVAILABLE", "fixture")
	}
	client, peer := net.Pipe()
	route := *a.sshFixture
	go func() {
		defer peer.Close()
		reader := bufio.NewReader(peer)
		if _, err := reader.ReadByte(); err != nil {
			return
		}
		if _, err := io.WriteString(peer, "fixture$ "); err != nil {
			return
		}
		first, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		id := strings.TrimSpace(first)
		id = strings.TrimSuffix(strings.TrimPrefix(id, "python3 - <<'__BUPT_"), "__'")
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if strings.TrimSpace(line) == "__BUPT_"+id+"__" {
				break
			}
		}
		result := connect.BootstrapResult{InstanceID: "20", NodeID: "40", Username: route.Username, Home: route.Home, HostKey: route.HostKey, TunnelPublicKey: route.HostKey}
		encoded := base64.StdEncoding.EncodeToString(domain.Result(map[string]any{"ok": true, "result": result}))
		_, _ = io.WriteString(peer, "BUPT_RESULT_"+id+":"+encoded+":BUPT_END_"+id)
	}()
	return client, nil
}
