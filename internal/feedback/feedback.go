// Package feedback prepares user-reviewed mail and diagnostic reports.
package feedback

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

//go:generate python3 ../../scripts/sync_feedback_contact.py

const Email = "meidici911@gmail.com"
const Scope = "程序错误、安装与使用、模型 API、学校算力平台、配额与计费问题，以及功能建议。"
const SchoolSupport = "作者可协助排查。账号、计费、审批和权限由学校管理部门处理。"
const Review = "请先审阅，再自行发送。资源名称、日志等可按需补充脱敏片段。请勿附上密码、完整 API Key、令牌或私钥。"

type Category struct{ ID, Label, Subject string }

var categories = []Category{
	{"bug", "程序报错或异常", "程序异常"},
	{"usage", "安装、配置及使用问题", "安装使用"},
	{"platform", "学校平台及算力服务问题", "学校平台"},
	{"suggestion", "功能建议与其他反馈", "建议与反馈"},
}

func Categories() []Category { return append([]Category(nil), categories...) }

func FindCategory(id string) (Category, bool) {
	for _, category := range categories {
		if category.ID == id {
			return category, true
		}
	}
	return Category{}, false
}

// Incident is the allowlisted context. Error messages, raw arguments, resource
// names and logs do not enter it. Receipt fields accept only recognized IDs.
type Incident struct {
	Operation string
	Code      string
	RequestID string
	RunID     string
	TaskID    string
	State     string
	Time      time.Time
}

var numberedError = regexp.MustCompile(`^(?:HTTP_[1-5][0-9]{2}|(?:AUTH|TASK)_[0-9]{1,6})$`)
var knownErrors = strings.Fields(`
	ERROR USAGE TIMEOUT INTERRUPTED TTY_REQUIRED CONFIG_INVALID IO_ERROR MISSING_RECEIPT
	AGENT_NOT_RUNNING CONTROLLER_UNAVAILABLE CONTROLLER_CONNECT CONTROLLER_IO CONTROLLER_PROTOCOL
	AUTH_REQUIRED AUTH_EXPIRED GATEWAY_AUTH_EXPIRED PLATFORM_AUTH_EXPIRED SSO_REQUIRED SSO_VERIFICATION_PENDING
	AUTH_FAILED LOGIN_FAILED AUTH_REJECTED AGREEMENT_REQUIRED FORBIDDEN OWNER_REQUIRED PORTAL_CACHE_MISSING
	CONSOLE_LOGIN_REQUIRED CONSOLE_LOGIN_FAILED SSH_CONNECTION_FAILED SSH_DIAL_FAILED SSH_NOT_PREPARED
	SSH_NOT_READY SSH_TRANSPORT SSH_HOST_KEY_MISMATCH RELAY_REQUIRED RELAY_PROXY_FAILED STREAM_FAILED
	INSTANCE_NOT_RUNNING INSTANCE_NOT_STARTED INSTANCE_OPERATION_PENDING INSTANCE_TASK_FAILED RESTORE_REQUIRED
	START_FAILED RUN_ENDED RUN_PENDING TASK_PENDING CREATE_UNCERTAIN SUBMISSION_UNKNOWN
	COST_AUTH_REQUIRED COST_LIMIT_EXCEEDED COST_ESTIMATE_UNAVAILABLE COST_SELECTION_CHANGED
	MODEL_RESULT_UNKNOWN TRIAL_ALREADY_SUBMITTED MODEL_RECONCILIATION_REQUIRED MODEL_KEY_REJECTED
	MODEL_BALANCE_INSUFFICIENT MODEL_GATEWAY_ERROR MODEL_RESPONSE_LARGE MODEL_PROTOCOL
	EVENT_CONNECT EVENT_DISCONNECTED PROTOCOL PROTOCOL_ERROR NOT_FOUND INVALID_STATE
`)
var receiptID = regexp.MustCompile(`^(?:[a-f0-9]{8}-[a-f0-9]{4}-[1-8][a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}|(?:req|request)[_-][a-f0-9]{8,32})$`)
var releaseVersion = regexp.MustCompile(`^(?:dev|v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.\-]{1,64})?)$`)

func SafeCode(raw string) string {
	if raw == "" || numberedError.MatchString(raw) || slices.Contains(knownErrors, raw) {
		return raw
	}
	return "ERROR"
}

func safeReceipt(raw string) string {
	if receiptID.MatchString(raw) {
		return raw
	}
	return ""
}

func safeVersion(raw string) string {
	if releaseVersion.MatchString(raw) {
		return raw
	}
	return "未知"
}

// Operation maps trusted command names and RPC names to display text.
func Operation(raw string) string {
	switch raw = strings.TrimPrefix(raw, "bupt "); raw {
	case "connect.session", "ssh":
		return "bupt ssh"
	case "route.get", "compute instance prepare-ssh":
		return "bupt compute instance prepare-ssh"
	case "portal.trial", "api model invoke":
		return "bupt api model invoke"
	case "portal.read", "api model ls", "api model show":
		return "bupt api model"
	case "auth.login", "auth.start", "auth.poll", "auth.sso.submit", "auth.gateway.start", "auth.gateway.poll", "login":
		return "bupt login"
	case "auth.access", "auth access":
		return "bupt auth access"
	case "instance.list", "instance.inspect", "status.get", "compute instance ls", "compute instance show", "status":
		return "bupt compute instance ls"
	case "resources.overview", "spec.show", "spec.estimate", "compute catalog", "compute catalog ls":
		return "bupt compute catalog"
	case "agent.status", "agent status":
		return "bupt agent status"
	case "tui", "init", "doctor", "cp", "exec", "forward", "console", "config validate", "api usage", "compute usage":
		return "bupt " + raw
	}
	for _, action := range []string{"create", "start", "stop", "restart", "restore", "release", "delete", "wait", "cancel-queue"} {
		if raw == "instance."+action || raw == "compute instance "+action {
			return "bupt compute instance " + action
		}
	}
	return ""
}

func (i Incident) Safe() Incident {
	i.Operation = Operation(i.Operation)
	i.Code = SafeCode(i.Code)
	i.RequestID, i.RunID, i.TaskID = safeReceipt(i.RequestID), safeReceipt(i.RunID), safeReceipt(i.TaskID)
	switch i.State {
	case "preparing", "starting", "running", "stopping", "stopped", "failed", "rejected", "aborted", "need_repeat", "uncertain", "restart_failed", "cleanup_pending":
	default:
		i.State = ""
	}
	return i
}

func FromError(operation string, err error) Incident {
	i := Incident{Operation: operation, Code: "ERROR", Time: time.Now()}
	var detail *domain.Error
	switch {
	case errors.As(err, &detail):
		i.Code, i.RequestID, i.RunID, i.TaskID = detail.Code, detail.RequestID, detail.RunID, detail.TaskID
	case errors.Is(err, context.DeadlineExceeded):
		i.Code = "TIMEOUT"
	case errors.Is(err, context.Canceled):
		i.Code = "INTERRUPTED"
	}
	return i.Safe()
}

func (i Incident) Category() Category {
	switch {
	case strings.Contains(i.Code, "AUTH"), strings.HasPrefix(i.Code, "MODEL_"), i.Operation == "bupt login",
		strings.HasPrefix(i.Code, "HTTP_"), i.Code == "AGREEMENT_REQUIRED":
		return categories[2]
	case i.Code == "USAGE", i.Code == "TTY_REQUIRED", i.Operation == "bupt init", i.Operation == "bupt config validate",
		i.Code == "AGENT_NOT_RUNNING", i.Code == "CONTROLLER_UNAVAILABLE":
		return categories[1]
	default:
		return categories[0]
	}
}

func (i Incident) Title() string {
	switch i.Operation {
	case "bupt ssh", "bupt compute instance prepare-ssh":
		return "SSH 连接失败"
	case "bupt api model invoke":
		return "模型调用失败"
	case "bupt compute instance start":
		return "实例启动失败"
	case "bupt login":
		return "学校登录遇到问题"
	}
	if i.Code != "" {
		return i.Code
	}
	return ""
}

func (i Incident) BillingUnknown() bool {
	return i.Code == "MODEL_RESULT_UNKNOWN" || i.Code == "MODEL_RECONCILIATION_REQUIRED" ||
		i.Code == "TRIAL_ALREADY_SUBMITTED" || i.Code == "SUBMISSION_UNKNOWN" || i.State == "uncertain"
}

func (i Incident) Lines() []string {
	i = i.Safe()
	var lines []string
	for _, field := range []struct{ label, value string }{
		{"操作", i.Operation}, {"错误码", i.Code}, {"请求编号", i.RequestID},
		{"操作编号", i.RunID}, {"任务编号", i.TaskID}, {"状态", i.State},
	} {
		if field.value != "" {
			lines = append(lines, field.label+"："+field.value)
		}
	}
	if !i.Time.IsZero() {
		lines = append(lines, "发生时间："+i.Time.Format("2006-01-02 15:04 -07:00"))
	}
	if i.BillingUnknown() {
		lines = append(lines, "结果或扣费待核查，请先查看用量或原操作回执。")
	}
	return lines
}

type Report struct {
	Version       string    `json:"version"`
	GOOS          string    `json:"os"`
	GOARCH        string    `json:"arch"`
	Configuration string    `json:"configuration"`
	Controller    string    `json:"controller"`
	ErrorCode     string    `json:"error_code,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
}

type StatusCall func(context.Context, string, any, any) error

// CheckController asks only whether the controller answers. The reply is not
// copied into a report, even when the server sends additional fields.
func CheckController(ctx context.Context, call StatusCall) error {
	if call == nil {
		return domain.E("CONTROLLER_UNAVAILABLE", "controller not available")
	}
	var status struct {
		State string `json:"state"`
	}
	if err := call(ctx, "agent.status", nil, &status); err != nil {
		return err
	}
	if status.State != "running" {
		return domain.E("CONTROLLER_UNAVAILABLE", "controller not running")
	}
	return nil
}

func Collect(ctx context.Context, version string, configErr error, check func(context.Context) error) Report {
	r := Report{Version: safeVersion(version), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Configuration: "valid", Controller: "not_checked", CheckedAt: time.Now()}
	if configErr != nil {
		r.Configuration, r.ErrorCode = "invalid", "CONFIG_INVALID"
		return r
	}
	if check != nil {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		r.Controller = "reachable"
		if err := check(ctx); err != nil {
			r.Controller, r.ErrorCode = "unreachable", FromError("", err).Code
		}
	}
	return r
}

func (r Report) Text() string {
	config := map[string]string{"valid": "有效", "invalid": "无法读取或验证"}[r.Configuration]
	controller := map[string]string{"not_checked": "未检查", "reachable": "可连接", "unreachable": "无法连接"}[r.Controller]
	text := fmt.Sprintf("bupt 脱敏诊断\n版本：%s\n系统：%s/%s\n配置：%s\n控制器：%s\n检查时间：%s\n",
		r.Version, r.GOOS, r.GOARCH, config, controller, r.CheckedAt.Format("2006-01-02 15:04 -07:00"))
	if r.ErrorCode != "" {
		text += "诊断错误码：" + r.ErrorCode + "\n"
	}
	return text
}

type Draft struct {
	Category Category
	Incident Incident
	Version  string
	Report   *Report
}

func (d Draft) Text() string {
	i := d.Incident.Safe()
	title := i.Title()
	if title == "" {
		title = d.Category.Label
	}
	lines := []string{"收件人：" + Email, "主题：[bupt][" + d.Category.Subject + "] " + title, "",
		"问题描述：", "（请补充现象、操作步骤和预期结果）", "", "版本：" + safeVersion(d.Version)}
	lines = append(lines, i.Lines()...)
	if i.Time.IsZero() {
		lines = append(lines, "发生时间：（请补充）")
	}
	if d.Report != nil {
		lines = append(lines, "", strings.TrimSpace(d.Report.Text()))
	}
	return strings.Join(lines, "\n") + "\n"
}
