package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/wire"
)

type operationReply struct {
	result json.RawMessage
	err    *domain.Error
}

func operationPeer(t *testing.T, reply operationReply) (*Client, <-chan domain.RPCRequest) {
	t.Helper()
	c, listener := localFixture(t)
	c.Profile.Name = "selected-account"
	requests := make(chan domain.RPCRequest, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		var request domain.RPCRequest
		if wire.ReadJSON(bufio.NewReader(conn), &request) != nil {
			return
		}
		requests <- request
		_ = wire.WriteJSON(conn, domain.RPCResponse{ID: request.ID, Result: reply.result, Error: reply.err})
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("fake RPC server did not finish")
		}
	})
	return c, requests
}
func mutationRequest(t *testing.T, requests <-chan domain.RPCRequest, method string) map[string]json.RawMessage {
	t.Helper()
	select {
	case request := <-requests:
		if request.Method != method || request.Profile != "selected-account" || request.ClientID != "" || request.ID == "" {
			t.Fatalf("wrong controller envelope: %#v", request)
		}
		var params map[string]json.RawMessage
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatal(err)
		}
		if _, ok := params["profile"]; ok {
			t.Fatal("request overrides selected controller authority")
		}
		return params
	case <-time.After(time.Second):
		t.Fatal("no RPC request received")
		return nil
	}
}
func valuePointer[T any](value T) *T { return &value }

func TestManagedMutationReceiptsAndControllerAuthority(t *testing.T) {
	request := domain.ManagedRequest{OperationOptions: domain.OperationOptions{Wait: valuePointer(false), IdempotencyKey: "creation-intent"}, PolicySelection: domain.PolicySelection{MaxRuntime: valuePointer("2h")}, Target: "instance-name", Name: "new-name"}
	cases := []struct {
		name   string
		invoke func(*Client) (domain.Run, error)
	}{
		{"up", func(c *Client) (domain.Run, error) { return c.Up(context.Background(), request) }},
		{"instance.create", func(c *Client) (domain.Run, error) { return c.CreateInstance(context.Background(), request) }},
		{"instance.start", func(c *Client) (domain.Run, error) { return c.StartInstance(context.Background(), request) }},
		{"instance.restore", func(c *Client) (domain.Run, error) { return c.RestoreInstance(context.Background(), request) }},
		{"instance.adopt", func(c *Client) (domain.Run, error) { return c.AdoptInstance(context.Background(), request) }},
		{"instance.clone", func(c *Client) (domain.Run, error) { return c.CloneInstance(context.Background(), request) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			c, requests := operationPeer(t, operationReply{result: json.RawMessage(`{"id":"run-1","profile":"selected-account","instance_id":9007199254740993,"state":"preparing","policy_revision":3}`)})
			run, err := test.invoke(c)
			if err != nil {
				t.Fatal(err)
			}
			if run.ID != "run-1" || run.InstanceID != "9007199254740993" || run.PolicyRevision != 3 {
				t.Fatalf("wrong managed receipt: %#v", run)
			}
			params := mutationRequest(t, requests, test.name)
			if string(params["wait"]) != "false" || string(params["idempotency_key"]) != `"creation-intent"` || string(params["max_runtime"]) != `"2h"` {
				t.Fatal(params)
			}
			if _, ok := params["adopt"]; ok {
				t.Fatal("SDK silently authorized adoption")
			}
			if _, ok := params["dry_run"]; ok {
				t.Fatal("typed mutation was turned into a preview")
			}
		})
	}
}

func TestManagedRequestPreservesExplicitFalseAndPresetSnapshot(t *testing.T) {
	c, requests := operationPeer(t, operationReply{result: json.RawMessage(`{"id":"run-2","state":"preparing"}`)})
	snapshot := domain.Preset{SpecID: "9007199254740997", Name: "personal", Policy: domain.RunPolicy{MaxRuntime: "4h", AutoRenew: true, Cleanup: "release", StopGrace: "60s", ForceStop: true}, Constraints: map[string]any{"count": json.Number("9007199254740999")}}
	request := domain.ManagedRequest{
		OperationOptions:  domain.OperationOptions{Wait: valuePointer(false)},
		PolicySelection:   domain.PolicySelection{Policy: &domain.RunPolicyPatch{AutoRenew: valuePointer(false), ForceStop: valuePointer(false)}},
		TemplateSelection: domain.TemplateSelection{Preset: "personal", PresetSnapshot: &snapshot, SpecID: "9007199254740997", Resources: &domain.ResourceSelection{CPU: "1.5", Memory: "6GiB", BindCPU: valuePointer(false)}},
		Target:            "existing", Adopt: valuePointer(false), NoSSH: valuePointer(false),
		ConfigSources: map[string]string{"policy.auto_renew": "ui:explicit"},
	}
	if _, err := c.Up(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	params := mutationRequest(t, requests, "up")
	if string(params["adopt"]) != "false" || string(params["no_ssh"]) != "false" || string(params["spec_id"]) != `"9007199254740997"` {
		t.Fatal(params)
	}
	var policy map[string]json.RawMessage
	_ = json.Unmarshal(params["policy"], &policy)
	if len(policy) != 2 || string(policy["auto_renew"]) != "false" || string(policy["force_stop"]) != "false" {
		t.Fatalf("unset policy fields were written: %s", params["policy"])
	}
	var resources map[string]json.RawMessage
	_ = json.Unmarshal(params["resources"], &resources)
	if string(resources["bindCpu"]) != "false" || string(resources["memory"]) != `"6GiB"` {
		t.Fatal(resources)
	}
	if !strings.Contains(string(params["preset_snapshot"]), `9007199254740999`) {
		t.Fatal("preset numeric precision was lost")
	}
	if string(params["config_sources"]) != `{"policy.auto_renew":"ui:explicit"}` {
		t.Fatal("lost frontend configuration provenance")
	}
}

func TestRunJobKeepsArgvWorkdirAndEnvInRPCOnly(t *testing.T) {
	c, requests := operationPeer(t, operationReply{result: json.RawMessage(`{"id":"job-1","kind":"job","state":"preparing"}`)})
	request := domain.RunJobRequest{
		ManagedRequest: domain.ManagedRequest{OperationOptions: domain.OperationOptions{Wait: valuePointer(false)}, PolicySelection: domain.PolicySelection{MaxRuntime: valuePointer("1h")}, Target: "already-managed"},
		Command:        []string{"python3", "job.py", "literal ; $(no-shell)"}, Env: map[string]string{"JOB_INPUT": "transient-fixture"}, WorkDir: "work dir", Detach: valuePointer(true),
	}
	run, err := c.RunJob(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != "job-1" || run.Kind != "job" || len(run.Command) != 0 {
		t.Fatal(run)
	}
	params := mutationRequest(t, requests, "job.run")
	var argv []string
	if err = json.Unmarshal(params["command"], &argv); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(argv, request.Command) || string(params["work_dir"]) != `"work dir"` || string(params["detach"]) != "true" {
		t.Fatal(params)
	}
	if _, ok := params["workdir"]; ok {
		t.Fatal("SDK used CLI-specific workdir spelling")
	}
}

func TestPolicyUpdatesKeepOnlyExplicitChangesAndRevision(t *testing.T) {
	until := time.Date(2030, 1, 2, 3, 4, 5, 0, time.FixedZone("CST", 8*3600))
	for _, absolute := range []bool{false, true} {
		name := "max_runtime"
		if absolute {
			name = "until"
		}
		t.Run(name, func(t *testing.T) {
			c, requests := operationPeer(t, operationReply{result: json.RawMessage(`{"id":"run-1","policy_revision":8,"policy":{"auto_renew":false,"force_stop":false}}`)})
			request := domain.PolicyUpdateRequest{Target: "run-1", Revision: valuePointer(7), PolicySelection: domain.PolicySelection{Policy: &domain.RunPolicyPatch{AutoRenew: valuePointer(false), ForceStop: valuePointer(false)}}}
			if absolute {
				request.Until = &until
			} else {
				request.MaxRuntime = valuePointer("3h")
			}
			run, err := c.UpdateRunPolicy(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if run.PolicyRevision != 8 || run.Policy.AutoRenew || run.Policy.ForceStop {
				t.Fatal(run)
			}
			params := mutationRequest(t, requests, "lease.set")
			if string(params["revision"]) != "7" {
				t.Fatal(params)
			}
			var policy map[string]json.RawMessage
			_ = json.Unmarshal(params["policy"], &policy)
			if len(policy) != 2 {
				t.Fatalf("policy update replaced untouched settings: %s", params["policy"])
			}
			if _, ok := policy["max_runtime"]; ok {
				t.Fatal("duration must be top-level so controller clears the old until mode")
			}
			if absolute {
				if string(params["until"]) != `"2030-01-02T03:04:05+08:00"` {
					t.Fatal(params)
				}
				if _, ok := params["max_runtime"]; ok {
					t.Fatal("sent competing limit modes")
				}
			} else {
				if string(params["max_runtime"]) != `"3h"` {
					t.Fatal(params)
				}
				if _, ok := params["until"]; ok {
					t.Fatal("sent an unrequested absolute deadline")
				}
			}
		})
	}
}

func TestDownAndCancelReturnRunsWithoutInventingCleanup(t *testing.T) {
	for _, method := range []string{"down", "job.cancel"} {
		t.Run(method, func(t *testing.T) {
			c, requests := operationPeer(t, operationReply{result: json.RawMessage(`{"id":"run-1","state":"cleanup_pending","cleanup_state":"pending"}`)})
			request := domain.DownRequest{Target: "run-1", Wait: valuePointer(false)}
			var run domain.Run
			var err error
			if method == "down" {
				run, err = c.Down(context.Background(), request)
			} else {
				run, err = c.CancelJob(context.Background(), request)
			}
			if err != nil {
				t.Fatal(err)
			}
			if run.CleanupState != "pending" {
				t.Fatal(run)
			}
			params := mutationRequest(t, requests, method)
			if _, ok := params["cleanup"]; ok {
				t.Fatal("SDK replaced the run's cleanup policy")
			}
			if len(params) != 2 {
				t.Fatal(params)
			}
		})
	}
}

func TestPrimitiveMutationsReturnTaskReceipts(t *testing.T) {
	base := domain.InstanceTaskRequest{OperationOptions: domain.OperationOptions{Wait: valuePointer(false), IdempotencyKey: "task-intent"}, Target: "instance-1", Node: "9007199254740997"}
	cases := []struct {
		method      string
		invoke      func(*Client) (domain.TaskRecord, error)
		field, want string
	}{
		{"instance.stop", func(c *Client) (domain.TaskRecord, error) {
			return c.StopInstance(context.Background(), domain.StopInstanceRequest{InstanceTaskRequest: base, Force: valuePointer(false)})
		}, "force", "false"},
		{"instance.release", func(c *Client) (domain.TaskRecord, error) { return c.ReleaseInstance(context.Background(), base) }, "node", `"9007199254740997"`},
		{"instance.delete", func(c *Client) (domain.TaskRecord, error) { return c.DeleteInstance(context.Background(), base) }, "target", `"instance-1"`},
		{"instance.cancel-queue", func(c *Client) (domain.TaskRecord, error) { return c.CancelQueue(context.Background(), base) }, "target", `"instance-1"`},
		{"instance.resize", func(c *Client) (domain.TaskRecord, error) {
			return c.ResizeInstance(context.Background(), domain.ResizeInstanceRequest{InstanceTaskRequest: base, Resources: &domain.ResourceSelection{Memory: "8GiB"}})
		}, "resources", `{"memory":"8GiB"}`},
		{"instance.disk-grow", func(c *Client) (domain.TaskRecord, error) {
			return c.GrowDisk(context.Background(), domain.GrowDiskRequest{InstanceTaskRequest: base, SizeGB: 100})
		}, "size_gb", "100"},
		{"snapshot.create", func(c *Client) (domain.TaskRecord, error) {
			return c.CreateSnapshot(context.Background(), domain.CreateSnapshotRequest{InstanceTaskRequest: base, Name: "checkpoint", Description: "before upgrade"})
		}, "description", `"before upgrade"`},
		{"snapshot.restore", func(c *Client) (domain.TaskRecord, error) {
			return c.RestoreSnapshot(context.Background(), domain.SnapshotRequest{InstanceTaskRequest: base, SnapshotID: "9007199254740999"})
		}, "snapshot_id", `"9007199254740999"`},
		{"snapshot.delete", func(c *Client) (domain.TaskRecord, error) {
			return c.DeleteSnapshot(context.Background(), domain.SnapshotRequest{InstanceTaskRequest: base, SnapshotID: "9007199254740999"})
		}, "snapshot_id", `"9007199254740999"`},
		{"service.add", func(c *Client) (domain.TaskRecord, error) {
			return c.AddService(context.Background(), domain.AddServiceRequest{InstanceTaskRequest: base, Name: "app", Port: 8080, Bind: "127.0.0.1"})
		}, "port", "8080"},
		{"service.delete", func(c *Client) (domain.TaskRecord, error) {
			return c.DeleteService(context.Background(), domain.DeleteServiceRequest{InstanceTaskRequest: base, ServiceID: "app"})
		}, "service_id", `"app"`},
		{"lease.renew", func(c *Client) (domain.TaskRecord, error) {
			return c.RenewLease(context.Background(), domain.RenewLeaseRequest{OperationOptions: base.OperationOptions, Target: "run-1", Duration: "1h", Reason: "finish job"})
		}, "duration", `"1h"`},
	}
	for _, test := range cases {
		t.Run(test.method, func(t *testing.T) {
			c, requests := operationPeer(t, operationReply{result: json.RawMessage(`{"id":"task-1","profile":"selected-account","operation":"fixture","state":"prepared","request":{"id":"task-1","type":"Task","user_id":9007199254740993}}`)})
			task, err := test.invoke(c)
			if err != nil {
				t.Fatal(err)
			}
			if task.ID != "task-1" || task.Request.UserID != "9007199254740993" || task.State != "prepared" {
				t.Fatalf("wrong primitive receipt: %#v", task)
			}
			params := mutationRequest(t, requests, test.method)
			if string(params[test.field]) != test.want || string(params["idempotency_key"]) != `"task-intent"` || string(params["wait"]) != "false" {
				t.Fatal(params)
			}
		})
	}
}

func TestMutationPropagatesStructuredErrorAndDoesNotRetry(t *testing.T) {
	serverError := &domain.Error{Code: "TASK_PENDING", Message: "tracked by controller", TaskID: "task-unknown", Retryable: true}
	c, requests := operationPeer(t, operationReply{err: serverError})
	_, err := c.StopInstance(context.Background(), domain.StopInstanceRequest{InstanceTaskRequest: domain.InstanceTaskRequest{Target: "instance-1"}})
	var result *domain.Error
	if !errors.As(err, &result) || !reflect.DeepEqual(result, serverError) {
		t.Fatal(err)
	}
	mutationRequest(t, requests, "instance.stop")
}

func TestPreviewReturnsActualSchemaAndCopiesRequest(t *testing.T) {
	c, requests := operationPeer(t, operationReply{result: json.RawMessage(`{"dry_run":true,"operation":"delete","request":{"target":9007199254740997},"future_field":{"value":1}}`)})
	request := map[string]any{"target": "instance-1", "dry_run": false, "constraints": map[string]any{"count": json.Number("9007199254740999")}}
	result, err := c.Preview(context.Background(), "instance.delete", request)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(result) || !strings.Contains(string(result), `"future_field"`) || !strings.Contains(string(result), "9007199254740997") {
		t.Fatal(string(result))
	}
	params := mutationRequest(t, requests, "instance.delete")
	if string(params["dry_run"]) != "true" || !strings.Contains(string(params["constraints"]), "9007199254740999") {
		t.Fatal(params)
	}
	if request["dry_run"] != false {
		t.Fatal("preview mutated caller-owned request")
	}
}

func TestSDKRejectsInvalidEncodingAndUnsafePreviewBeforeRPC(t *testing.T) {
	c := &Client{} // Any attempted transport use would panic or fail differently.
	_, err := c.Up(context.Background(), domain.ManagedRequest{TemplateSelection: domain.TemplateSelection{Constraints: map[string]any{"sensitive-field": make(chan int)}}})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "INVALID_REQUEST" || strings.Contains(err.Error(), "sensitive-field") {
		t.Fatal(err)
	}
	for _, method := range []string{"connect.session", "auth.start", "unknown"} {
		_, err = c.Preview(context.Background(), method, map[string]any{})
		if !errors.As(err, &de) || de.Code != "PREVIEW_UNSUPPORTED" {
			t.Fatal(err)
		}
	}
	_, err = c.Preview(context.Background(), "up", []string{"not an object"})
	if !errors.As(err, &de) || de.Code != "INVALID_REQUEST" {
		t.Fatal(err)
	}
}

func TestReloadConfigurationHasNoProfileMutationPayload(t *testing.T) {
	c, requests := operationPeer(t, operationReply{result: json.RawMessage(`{"reloaded":true,"profiles":["selected-account"],"run_policies_preserved":true}`)})
	result, err := c.ReloadConfiguration(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reloaded || !result.RunPoliciesPreserved || len(result.Profiles) != 1 {
		t.Fatal(result)
	}
	if params := mutationRequest(t, requests, "agent.reload"); len(params) != 0 {
		t.Fatal(params)
	}
}

func TestTypedMutationCancellationOnlyClosesItsWait(t *testing.T) {
	c, listener := localFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan struct{})
	requests := make(chan domain.RPCRequest, 1)
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var request domain.RPCRequest
		if wire.ReadJSON(bufio.NewReader(conn), &request) != nil {
			return
		}
		requests <- request
		cancel()
		_, _ = io.Copy(io.Discard, conn)
	}()
	_, err := c.Up(ctx, domain.ManagedRequest{Name: "job"})
	if err == nil {
		t.Fatal("expected interrupted wait")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SDK left the request connection open")
	}
	select {
	case request := <-requests:
		if request.Method != "up" {
			t.Fatal(request)
		}
	default:
		t.Fatal("request did not reach the controller")
	}
	// No cancellation mutation is sent when a view/wait context closes.
	if unix, ok := listener.(*net.UnixListener); ok {
		_ = unix.SetDeadline(time.Now().Add(20 * time.Millisecond))
		if conn, err := listener.Accept(); err == nil {
			conn.Close()
			t.Fatal("SDK silently sent a lifecycle cancellation")
		}
	}
}
