package domain

import "time"

// Mutation requests describe controller intent, independently of CLI flags or
// terminal widgets. They do not contain transport credentials or a client ID;
// the selected Client and authenticated gateway supply that authority.

type CleanupMode string

const (
	CleanupNone    CleanupMode = "none"
	CleanupStop    CleanupMode = "stop"
	CleanupRelease CleanupMode = "release"
	CleanupDelete  CleanupMode = "delete"
)

// OperationOptions retains the difference between the controller default and
// an explicit false. UI callers normally set Wait to false and track the returned
// Run/TaskRecord ID. Reuse IdempotencyKey only for the same logical request.
type OperationOptions struct {
	Wait           *bool  `json:"wait,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	DryRun         bool   `json:"dry_run,omitempty"`
}

// RunPolicyPatch changes only selected policy settings. A nil pointer inherits
// the current value; a pointer to false disables an enabled setting.
// Deadline selection is represented separately by PolicySelection so switching
// between max_runtime and until uses the controller's existing RPC semantics.
type RunPolicyPatch struct {
	AutoRenew *bool        `json:"auto_renew,omitempty"`
	Cleanup   *CleanupMode `json:"cleanup,omitempty"`
	StopGrace *string      `json:"stop_grace,omitempty"`
	ForceStop *bool        `json:"force_stop,omitempty"`
}

// PolicySelection chooses at most one finite limit. Durations use Go duration
// strings (e.g. "2h"); Until is encoded as RFC3339. The controller validates the
// limit, resolves defaults and snapshots the resulting RunPolicy.
type PolicySelection struct {
	MaxRuntime *string         `json:"max_runtime,omitempty"`
	Until      *time.Time      `json:"until,omitempty"`
	Policy     *RunPolicyPatch `json:"policy,omitempty"`
}

// ResourceSelection uses exact textual quantities, avoiding floating-point
// rounding. Template-specific multi-node constraints remain an explicit map in
// TemplateSelection/ResizeInstanceRequest and are validated by the controller.
type ResourceSelection struct {
	CPU     string `json:"cpu,omitempty"`
	Memory  string `json:"memory,omitempty"`
	GPU     string `json:"gpu,omitempty"`
	Disk    string `json:"disk,omitempty"`
	BindCPU *bool  `json:"bindCpu,omitempty"`
	CPUArch string `json:"cpuArch,omitempty"`
}

type TemplateSelection struct {
	Preset string `json:"preset,omitempty"`
	// Send the resolved snapshot together with Preset when client/project
	// configuration supplied that preset; it contains no connection settings.
	PresetSnapshot *Preset            `json:"preset_snapshot,omitempty"`
	SpecID         ID                 `json:"spec_id,omitempty"`
	GroupID        ID                 `json:"group_id,omitempty"`
	BillID         ID                 `json:"bill_id,omitempty"`
	Resources      *ResourceSelection `json:"resources,omitempty"`
	Constraints    map[string]any     `json:"constraints,omitempty"`
	Email          string             `json:"email,omitempty"`
	Usage          string             `json:"usage,omitempty"`
	Domain         string             `json:"domain,omitempty"`
}

// ManagedRequest is used by up and managed create/start/restore/adopt/clone.
// Target accepts an instance ID or unambiguous name; for clone it is the source.
// An existing unmanaged instance is never adopted merely because Target is set:
// use Adopt=true or the explicit AdoptInstance operation.
type ManagedRequest struct {
	AllocateOnly    bool   `json:"allocate_only,omitempty"`
	MaxPointsPerDay string `json:"max_points_per_day,omitempty"`
	OperationOptions
	PolicySelection
	TemplateSelection
	Target          string            `json:"target,omitempty"`
	Name            string            `json:"name,omitempty"`
	Node            ID                `json:"node,omitempty"`
	Adopt           *bool             `json:"adopt,omitempty"`
	NoSSH           *bool             `json:"no_ssh,omitempty"`
	DurationSeconds int64             `json:"duration,omitempty"`
	ConfigSources   map[string]string `json:"config_sources,omitempty"`
}

// RunJobRequest carries argv without shell parsing. Env and command arguments
// may contain secrets: keep the input transient and never log/cache it as a UI
// snapshot. The controller stores its private run intent in the encrypted vault.
type RunJobRequest struct {
	ManagedRequest
	Command []string          `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
	WorkDir string            `json:"work_dir,omitempty"`
	Detach  *bool             `json:"detach,omitempty"`
}

type DownRequest struct {
	Target  string       `json:"target"`
	Wait    *bool        `json:"wait,omitempty"`
	Cleanup *CleanupMode `json:"cleanup,omitempty"`
}

// PolicyUpdateRequest edits an existing run explicitly. Omit all unrelated
// fields; use Revision from the displayed Run to detect a concurrent edit.
type PolicyUpdateRequest struct {
	PolicySelection
	Target   string `json:"target"`
	Revision *int   `json:"revision,omitempty"`
	StopNow  *bool  `json:"stop_now,omitempty"`
}

// InstanceTaskRequest is a primitive operation, not a new managed run. Its
// receipt is TaskRecord; managed allocation operations return Run instead.
type InstanceTaskRequest struct {
	OperationOptions
	Target  string `json:"target"`
	Node    ID     `json:"node,omitempty"`
	Confirm ID     `json:"confirm,omitempty"`
}

type StopInstanceRequest struct {
	InstanceTaskRequest
	Force *bool `json:"force,omitempty"`
}
type ResizeInstanceRequest struct {
	InstanceTaskRequest
	Resources   *ResourceSelection `json:"resources,omitempty"`
	Constraints map[string]any     `json:"constraints,omitempty"`
}
type GrowDiskRequest struct {
	InstanceTaskRequest
	TargetCapacityGB int64 `json:"size_gb"`
}
type CreateSnapshotRequest struct {
	InstanceTaskRequest
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}
type SnapshotRequest struct {
	InstanceTaskRequest
	SnapshotID ID `json:"snapshot_id"`
}
type AddServiceRequest struct {
	InstanceTaskRequest
	Name string `json:"name"`
	Port int    `json:"port"`
	Bind string `json:"bind,omitempty"`
}
type DeleteServiceRequest struct {
	InstanceTaskRequest
	ServiceID string `json:"service_id"`
}
type RenewLeaseRequest struct {
	OperationOptions
	Target   string `json:"target"`
	Duration string `json:"duration"`
	Reason   string `json:"reason,omitempty"`
}

type ReloadResult struct {
	Reloaded             bool     `json:"reloaded"`
	Profiles             []string `json:"profiles"`
	RunPoliciesPreserved bool     `json:"run_policies_preserved"`
}

// InstanceCreatePreview carries the resource and billing facts approved by both clients.
type InstanceCreatePreview struct {
	DryRun              bool                            `json:"dry_run"`
	Run                 *Run                            `json:"run"`
	SpecID              ID                              `json:"spec_id"`
	PointsPerDay        string                          `json:"points_per_day"`
	BillingStart        string                          `json:"billing_start"`
	AllocateOnly        bool                            `json:"allocate_only"`
	ResourceConstraints map[string][]ResourceConstraint `json:"resource_constraints"`
}

// ResourceConstraint is a normalized resource value from the school template.
type ResourceConstraint struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}
