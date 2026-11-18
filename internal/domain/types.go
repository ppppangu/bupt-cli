package domain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"
)

// IDs are strings at the CLI boundary so consumers never lose integer precision.
type ID string

func (i *ID) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		*i = ""
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*i = ID(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	if _, err := strconv.ParseUint(n.String(), 10, 64); err != nil {
		return err
	}
	*i = ID(n.String())
	return nil
}
func (i ID) Number() json.Number {
	if i == "" {
		return json.Number("0")
	}
	return json.Number(i)
}

type Error struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
	TaskID    string `json:"task_id,omitempty"`
	RunID     string `json:"run_id,omitempty"`
	Hint      string `json:"hint,omitempty"`
}

func (e *Error) Error() string     { return e.Code + ": " + e.Message }
func E(code, message string) error { return &Error{Code: code, Message: message} }

type RelayConfig struct {
	Address         string `json:"address" toml:"address"`
	User            string `json:"user" toml:"user"`
	IdentityFile    string `json:"identity_file,omitempty" toml:"identity_file"`
	KnownHostsFile  string `json:"known_hosts_file,omitempty" toml:"known_hosts_file"`
	HostKey         string `json:"host_key,omitempty" toml:"host_key"`
	InstanceAddress string `json:"instance_address,omitempty" toml:"instance_address"`
	PortMin         int    `json:"port_min,omitempty" toml:"port_min"`
	PortMax         int    `json:"port_max,omitempty" toml:"port_max"`
}
type Profile struct {
	PortalURL              string      `json:"portal_url,omitempty" toml:"portal_url"`
	Name                   string      `json:"name" toml:"-"`
	PlatformURL            string      `json:"platform_url" toml:"platform_url"`
	GatewayURL             string      `json:"gateway_url,omitempty" toml:"gateway_url"`
	Username               string      `json:"username,omitempty" toml:"username"`
	ExpectedUID            ID          `json:"expected_uid,omitempty" toml:"expected_uid"`
	Controller             string      `json:"controller,omitempty" toml:"controller"`
	ControllerUser         string      `json:"controller_user,omitempty" toml:"controller_user"`
	ControllerIdentityFile string      `json:"controller_identity_file,omitempty" toml:"controller_identity_file"`
	ControllerHostKey      string      `json:"controller_host_key,omitempty" toml:"controller_host_key"`
	ControllerSocket       string      `json:"controller_socket,omitempty" toml:"controller_socket"`
	HTTPProxy              string      `json:"http_proxy,omitempty" toml:"http_proxy"`
	Relay                  RelayConfig `json:"relay" toml:"relay"`
}
type Identity struct {
	CampusID    string              `json:"campus_id,omitempty"`
	Memberships []ProjectMembership `json:"memberships,omitempty"`
	UID         ID                  `json:"uid"`
	EID         ID                  `json:"eid,omitempty"`
	Username    string              `json:"username"`
	DisplayName string              `json:"display_name,omitempty"`
	Email       string              `json:"email,omitempty"`
	Groups      []ID                `json:"groups,omitempty"`
}
type ProjectMembership struct {
	GroupID ID     `json:"group_id"`
	Role    string `json:"role"`
	State   string `json:"state,omitempty"`
}

const CampusPasswordKind = "bupt_sso_password"
const EnterCampusPassword = "enter_sso_password"

type AuthChallenge struct {
	CaptchaImage   string    `json:"captcha_image,omitempty"`
	Stage          string    `json:"stage,omitempty"`
	RequiredAction string    `json:"required_action,omitempty"`
	PollAfterMS    int       `json:"poll_after_ms,omitempty"`
	Kind           string    `json:"kind"`
	ID             string    `json:"id,omitempty"`
	URL            string    `json:"url,omitempty"`
	ExpiresAt      time.Time `json:"expires_at,omitempty"`
	State          string    `json:"state"`
	Message        string    `json:"message,omitempty"`
}
type AuthStatus struct {
	Gateway        string    `json:"gateway"`
	Platform       string    `json:"platform"`
	Identity       *Identity `json:"identity,omitempty"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
	RequiresAction string    `json:"requires_action,omitempty"`
}
type Service struct {
	ID        ID     `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	UIType    string `json:"ui_type,omitempty"`
	Healthy   bool   `json:"healthy"`
	Host      string `json:"host,omitempty"`
	Port      int    `json:"port,omitempty"`
	InnerHost string `json:"inner_host,omitempty"`
	InnerPort int    `json:"inner_port,omitempty"`
	Builtin   bool   `json:"builtin,omitempty"`
}
type Node struct {
	ID        ID               `json:"id"`
	Name      string           `json:"name"`
	Username  string           `json:"username,omitempty"`
	Type      string           `json:"type,omitempty"`
	Role      string           `json:"role,omitempty"`
	State     string           `json:"state,omitempty"`
	GroupID   ID               `json:"group_id,omitempty"`
	Index     ID               `json:"index,omitempty"`
	Services  []Service        `json:"services,omitempty"`
	Resources map[string]any   `json:"resources,omitempty"`
	Storages  []map[string]any `json:"storages,omitempty"`
	Snapshots []map[string]any `json:"snapshots,omitempty"`
}
type Instance struct {
	OperateTaskID   string         `json:"operate_task_id,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
	ID              ID             `json:"id"`
	Name            string         `json:"name"`
	OwnerID         ID             `json:"owner_id,omitempty"`
	UserID          ID             `json:"user_id,omitempty"`
	GroupID         ID             `json:"group_id,omitempty"`
	SpecID          ID             `json:"spec_id,omitempty"`
	Domain          string         `json:"domain,omitempty"`
	Index           ID             `json:"index,omitempty"`
	State           string         `json:"state"`
	Health          string         `json:"health,omitempty"`
	Reserved        bool           `json:"reserved"`
	ReserveType     string         `json:"reserve_type,omitempty"`
	ExpiresAt       time.Time      `json:"expires_at,omitempty"`
	DurationSeconds int64          `json:"duration_seconds,omitempty"`
	CreatedAt       time.Time      `json:"created_at,omitempty"`
	Nodes           []Node         `json:"nodes,omitempty"`
	Managed         bool           `json:"managed,omitempty"`
	RunID           string         `json:"run_id,omitempty"`
	SSHState        string         `json:"ssh_state,omitempty"`
}
type Spec struct {
	ID          ID             `json:"id"`
	Name        string         `json:"name"`
	Version     string         `json:"version,omitempty"`
	Description string         `json:"description,omitempty"`
	Options     map[string]any `json:"options,omitempty"`
}
type Renewal struct {
	OwnerID          ID        `json:"owner_id,omitempty"`
	ID               ID        `json:"id"`
	TargetID         ID        `json:"target_id"`
	Type             string    `json:"type"`
	State            string    `json:"state"`
	RequestedSeconds int64     `json:"requested_seconds,omitempty"`
	CreatedAt        time.Time `json:"created_at,omitempty"`
}
type TaskRequest struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Payload  map[string]any `json:"payload"`
	UserID   ID             `json:"user_id"`
	TargetID ID             `json:"target_id,omitempty"`
}
type TaskResult struct {
	ID         string         `json:"id"`
	State      string         `json:"state"`
	Code       int            `json:"code,omitempty"`
	Message    string         `json:"message,omitempty"`
	InstanceID ID             `json:"instance_id,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}
type PlatformEvent struct {
	Index         int64          `json:"index"`
	PreviousIndex int64          `json:"previous_index,omitempty"`
	Type          string         `json:"type"`
	TaskID        string         `json:"task_id,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
}
type ProjectMember struct {
	UID   ID     `json:"uid"`
	Name  string `json:"name,omitempty"`
	Role  string `json:"role"`
	State string `json:"state,omitempty"`
}
type Snapshot struct {
	ProjectMembers map[string][]ProjectMember `json:"project_members,omitempty"`
	Index          int64                      `json:"index"`
	Identity       Identity                   `json:"identity"`
	Instances      []Instance                 `json:"instances"`
	Tasks          []TaskResult               `json:"tasks,omitempty"`
	Renewals       []Renewal                  `json:"renewals,omitempty"`
	Quota          map[string]any             `json:"quota,omitempty"`
	Version        string                     `json:"version,omitempty"`
}
type PlatformConfig struct {
	Features map[string]bool `json:"features,omitempty"`
	App      map[string]any  `json:"app,omitempty"`
	Meter    map[string]any  `json:"meter,omitempty"`
	Version  string          `json:"version,omitempty"`
}
type SecretStore interface {
	Get(name string, out any) error
	Put(name string, value any) error
	Delete(name string) error
}

// The adapter holds sessions; connection routes are stored independently from them.
type PlatformAPI interface {
	WhoAmI(context.Context) (Identity, error)
	Login(context.Context, string, string) (Identity, error)
	AuthStatus(context.Context) AuthStatus
	GatewayStart(context.Context) (AuthChallenge, error)
	GatewayPoll(context.Context) (AuthChallenge, error)
	Logout(context.Context) error
	SetupPassword(context.Context, string, string) error
	Config(context.Context) (PlatformConfig, error)
	Specs(context.Context) ([]Spec, error)
	Spec(context.Context, ID) (Spec, error)
	Estimate(context.Context, ID, map[string]any) (map[string]any, error)
	Sync(context.Context) (Snapshot, error)
	Submit(context.Context, TaskRequest) (TaskResult, error)
	Console(context.Context, ID) (io.ReadWriteCloser, error)
	Close() error
}
type RunPolicy struct {
	MaxRuntime string    `json:"max_runtime,omitempty" toml:"max_runtime"`
	Until      time.Time `json:"until,omitempty" toml:"until"`
	AutoRenew  bool      `json:"auto_renew" toml:"auto_renew"`
	Cleanup    string    `json:"cleanup" toml:"cleanup"`
	StopGrace  string    `json:"stop_grace" toml:"stop_grace"`
	ForceStop  bool      `json:"force_stop" toml:"force_stop"`
}

func (p RunPolicy) Deadline(start time.Time) (time.Time, error) {
	if !p.Until.IsZero() && p.MaxRuntime != "" {
		return time.Time{}, E("INVALID_POLICY", "use max_runtime or until, not both")
	}
	if !p.Until.IsZero() {
		if !p.Until.After(start) {
			return time.Time{}, E("INVALID_POLICY", "deadline must be in the future")
		}
		return p.Until, nil
	}
	if p.MaxRuntime == "" {
		return time.Time{}, E("LIMIT_REQUIRED", "a run must have max_runtime or until")
	}
	d, e := time.ParseDuration(p.MaxRuntime)
	if e != nil || d <= 0 {
		return time.Time{}, E("INVALID_POLICY", "max_runtime must be a positive duration")
	}
	return start.Add(d), nil
}

type Preset struct {
	SpecID      ID             `json:"spec_id" toml:"spec_id"`
	Name        string         `json:"name,omitempty" toml:"name"`
	GroupID     ID             `json:"group_id,omitempty" toml:"group_id"`
	BillID      ID             `json:"bill_id,omitempty" toml:"bill_id"`
	Usage       string         `json:"usage,omitempty" toml:"usage"`
	Domain      string         `json:"domain,omitempty" toml:"domain"`
	Constraints map[string]any `json:"constraints,omitempty" toml:"constraints"`
	Policy      RunPolicy      `json:"policy" toml:"policy"`
}
type Run struct {
	JobName                 string            `json:"job_name,omitempty"`
	Kind                    string            `json:"kind,omitempty"`
	Phase                   string            `json:"phase,omitempty"`
	IntentHash              string            `json:"intent_hash,omitempty"`
	StopRequestedAt         time.Time         `json:"stop_requested_at,omitempty"`
	JobState                string            `json:"job_state,omitempty"`
	JobStartedAt            time.Time         `json:"job_started_at,omitempty"`
	RenewalTaskID           string            `json:"renewal_task_id,omitempty"`
	RenewalRequestID        ID                `json:"renewal_request_id,omitempty"`
	RenewalBaseExpiry       time.Time         `json:"renewal_base_expiry,omitempty"`
	RenewalRequestedSeconds int64             `json:"renewal_requested_seconds,omitempty"`
	LastRenewalAttempt      time.Time         `json:"last_renewal_attempt,omitempty"`
	TerminalReason          string            `json:"terminal_reason,omitempty"`
	ID                      string            `json:"id"`
	Profile                 string            `json:"profile"`
	Name                    string            `json:"name"`
	InstanceID              ID                `json:"instance_id,omitempty"`
	NodeID                  ID                `json:"node_id,omitempty"`
	State                   string            `json:"state"`
	CreatedAt               time.Time         `json:"created_at"`
	UpdatedAt               time.Time         `json:"updated_at"`
	Deadline                time.Time         `json:"deadline"`
	Policy                  RunPolicy         `json:"policy"`
	PolicyRevision          int               `json:"policy_revision"`
	Command                 []string          `json:"command,omitempty"`
	WorkDir                 string            `json:"work_dir,omitempty"`
	RemotePID               int               `json:"remote_pid,omitempty"`
	ExitCode                *int              `json:"exit_code,omitempty"`
	CleanupState            string            `json:"cleanup_state,omitempty"`
	AuthState               string            `json:"auth_state,omitempty"`
	RenewalState            string            `json:"renewal_state,omitempty"`
	LastError               string            `json:"last_error,omitempty"`
	CreatedInstance         bool              `json:"created_instance"`
	CancelRequested         bool              `json:"cancel_requested,omitempty"`
	LastTaskID              string            `json:"last_task_id,omitempty"`
	ConfigSources           map[string]string `json:"config_sources,omitempty"`
}
type TaskRecord struct {
	ID         string      `json:"id"`
	Profile    string      `json:"profile"`
	Operation  string      `json:"operation"`
	Request    TaskRequest `json:"request"`
	Hash       string      `json:"hash"`
	IntentHash string      `json:"intent_hash,omitempty"`
	ParentID   string      `json:"parent_id,omitempty"`
	AttemptIDs []string    `json:"attempt_ids,omitempty"`
	State      string      `json:"state"`
	Result     *TaskResult `json:"result,omitempty"`
	CreatedAt  time.Time   `json:"created_at"`
	UpdatedAt  time.Time   `json:"updated_at"`
}
type Route struct {
	Profile       string      `json:"profile"`
	InstanceID    ID          `json:"instance_id"`
	NodeID        ID          `json:"node_id"`
	Username      string      `json:"username"`
	Kind          string      `json:"kind"`
	Host          string      `json:"host"`
	Port          int         `json:"port"`
	HostKey       string      `json:"host_key"`
	IdentityFile  string      `json:"identity_file,omitempty"`
	Relay         RelayConfig `json:"relay,omitempty"`
	VerifiedAt    time.Time   `json:"verified_at"`
	State         string      `json:"state"`
	Generation    string      `json:"generation,omitempty"`
	Home          string      `json:"home,omitempty"`
	RelayLeaseKey string      `json:"relay_lease_key,omitempty"`
	Name          string      `json:"name,omitempty"`
}
type PortLease struct {
	Key        string    `json:"key"`
	Owner      string    `json:"owner"`
	Profile    string    `json:"profile"`
	InstanceID ID        `json:"instance_id"`
	NodeID     ID        `json:"node_id"`
	Port       int       `json:"port"`
	PublicKey  string    `json:"public_key,omitempty"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type AuditEvent struct {
	Time    time.Time      `json:"time"`
	Kind    string         `json:"kind"`
	Profile string         `json:"profile,omitempty"`
	Target  string         `json:"target,omitempty"`
	TaskID  string         `json:"task_id,omitempty"`
	RunID   string         `json:"run_id,omitempty"`
	Message string         `json:"message,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}
type RPCRequest struct {
	ID       string          `json:"id,omitempty"`
	Method   string          `json:"method"`
	Profile  string          `json:"profile,omitempty"`
	ClientID string          `json:"client_id,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
}
type RPCResponse struct {
	ID     string          `json:"id,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

func Result(v any) json.RawMessage {
	b, e := json.Marshal(v)
	if e != nil {
		return json.RawMessage(fmt.Sprintf("{\"error\":%q}", e.Error()))
	}
	return b
}
