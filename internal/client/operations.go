package client

import (
	"context"
	"encoding/json"
	"maps"

	"bupt.local/cli/internal/domain"
)

// Managed lifecycle operations return Run receipts. The controller owns all
// validation, ownership checks, scheduling, idempotency and lifecycle transitions.
func (c *Client) Up(ctx context.Context, request domain.ManagedRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "up", request, &result)
	return result, err
}
func (c *Client) RunJob(ctx context.Context, request domain.RunJobRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "job.run", request, &result)
	return result, err
}
func (c *Client) CreateInstance(ctx context.Context, request domain.ManagedRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "instance.create", request, &result)
	return result, err
}
func (c *Client) StartInstance(ctx context.Context, request domain.ManagedRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "instance.start", request, &result)
	return result, err
}
func (c *Client) RestoreInstance(ctx context.Context, request domain.ManagedRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "instance.restore", request, &result)
	return result, err
}
func (c *Client) RestartInstance(ctx context.Context, request domain.ManagedRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "instance.restart", request, &result)
	return result, err
}
func (c *Client) AdoptInstance(ctx context.Context, request domain.ManagedRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "instance.adopt", request, &result)
	return result, err
}
func (c *Client) CloneInstance(ctx context.Context, request domain.ManagedRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "instance.clone", request, &result)
	return result, err
}
func (c *Client) Down(ctx context.Context, request domain.DownRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "down", request, &result)
	return result, err
}
func (c *Client) CancelJob(ctx context.Context, request domain.DownRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "job.cancel", request, &result)
	return result, err
}
func (c *Client) UpdateRunPolicy(ctx context.Context, request domain.PolicyUpdateRequest) (domain.Run, error) {
	var result domain.Run
	err := c.mutate(ctx, "lease.set", request, &result)
	return result, err
}

// Primitive changes return TaskRecord receipts. They do not masquerade as new
// managed runs; use GetTask and its stable task ID to observe completion.
func (c *Client) StopInstance(ctx context.Context, request domain.StopInstanceRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "instance.stop", request)
}
func (c *Client) ReleaseInstance(ctx context.Context, request domain.InstanceTaskRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "instance.release", request)
}
func (c *Client) DeleteInstance(ctx context.Context, request domain.InstanceTaskRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "instance.delete", request)
}
func (c *Client) CancelQueue(ctx context.Context, request domain.InstanceTaskRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "instance.cancel-queue", request)
}
func (c *Client) ResizeInstance(ctx context.Context, request domain.ResizeInstanceRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "instance.resize", request)
}
func (c *Client) GrowDisk(ctx context.Context, request domain.GrowDiskRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "instance.disk-grow", request)
}
func (c *Client) CreateSnapshot(ctx context.Context, request domain.CreateSnapshotRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "snapshot.create", request)
}
func (c *Client) RestoreSnapshot(ctx context.Context, request domain.SnapshotRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "snapshot.restore", request)
}
func (c *Client) DeleteSnapshot(ctx context.Context, request domain.SnapshotRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "snapshot.delete", request)
}
func (c *Client) AddService(ctx context.Context, request domain.AddServiceRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "service.add", request)
}
func (c *Client) DeleteService(ctx context.Context, request domain.DeleteServiceRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "service.delete", request)
}
func (c *Client) RenewLease(ctx context.Context, request domain.RenewLeaseRequest) (domain.TaskRecord, error) {
	return c.task(ctx, "lease.renew", request)
}
func (c *Client) ReloadConfiguration(ctx context.Context) (domain.ReloadResult, error) {
	var result domain.ReloadResult
	err := c.mutate(ctx, "agent.reload", struct{}{}, &result)
	return result, err
}

func (c *Client) task(ctx context.Context, method string, request any) (domain.TaskRecord, error) {
	var result domain.TaskRecord
	err := c.mutate(ctx, method, request, &result)
	return result, err
}
func (c *Client) mutate(ctx context.Context, method string, request, result any) error {
	data, err := json.Marshal(request)
	if err != nil {
		return domain.E("INVALID_REQUEST", "mutation request contains a value that cannot be encoded as JSON")
	}
	return c.Call(ctx, method, json.RawMessage(data), result)
}

// Preview preserves the controller's actual preview response. Its schemas differ
// across operations and must not be decoded as an executed Run or TaskRecord.
// Only supported mutation methods may be previewed; this prevents a stream or
// unrelated RPC from accidentally running merely because it ignores dry_run.
func (c *Client) Preview(ctx context.Context, method string, request any) (json.RawMessage, error) {
	if !previewable(method) {
		return nil, domain.E("PREVIEW_UNSUPPORTED", "this operation has no supported mutation preview")
	}
	data, err := json.Marshal(request)
	if err != nil {
		return nil, domain.E("INVALID_REQUEST", "preview request contains a value that cannot be encoded as JSON")
	}
	params := map[string]json.RawMessage{}
	if err = json.Unmarshal(data, &params); err != nil {
		return nil, domain.E("INVALID_REQUEST", "preview request must be a JSON object")
	}
	if params == nil {
		params = map[string]json.RawMessage{}
	}
	params["dry_run"] = json.RawMessage("true")
	var result json.RawMessage
	err = c.Call(ctx, method, params, &result)
	return result, err
}
func previewable(method string) bool {
	switch method {
	case "up", "job.run", "down", "job.cancel", "lease.set", "job.update", "lease.renew", "agent.reload",
		"instance.create", "instance.start", "instance.restore", "instance.adopt", "instance.clone",
		"instance.restart", "compute.quota.request", "compute.admin.approve", "compute.admin.reject", "compute.admin.update", "compute.remote.checkout", "compute.remote.order", "data.edit", "instance.stop", "instance.release", "instance.delete", "instance.cancel-queue", "instance.resize", "instance.disk-grow",
		"snapshot.create", "snapshot.restore", "snapshot.delete", "service.add", "service.delete":
		return true
	}
	return false
}

// Caller is the transport shared by terminal and screen frontends.
type Caller interface {
	Call(context.Context, string, any, any) error
}

// CallOperation applies a preview only when the controller supports it.
func CallOperation(ctx context.Context, caller Caller, method string, params map[string]any, preview bool, out any) error {
	if !preview {
		return caller.Call(ctx, method, params, out)
	}
	if !previewable(method) {
		return domain.E("PREVIEW_UNSUPPORTED", "此操作没有只读预览")
	}
	p := maps.Clone(params)
	if p == nil {
		p = map[string]any{}
	}
	p["dry_run"] = true
	return caller.Call(ctx, method, p, out)
}
