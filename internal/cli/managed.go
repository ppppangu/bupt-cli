package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

// Submit first, then wait by durable ID. Disconnecting the CLI never loses
// the handle to a resource or repeats the allocation request.
func (a *app) managedCall(cmd *cobra.Command, method string, p map[string]any, condition string) (any, error) {
	wait := true
	if value, ok := p["wait"].(bool); ok {
		wait = value
	}
	if value, _ := p["no_wait"].(bool); value {
		wait = false
	}
	p["wait"] = false
	var result any
	if e := a.rpc(cmd.Context(), method, p, &result); e != nil {
		return result, e
	}
	if a.dryRun || !wait {
		return result, nil
	}
	id := resultID(result)
	if id == "" {
		return result, domain.E("PROTOCOL_ERROR", "operation returned no run identifier")
	}
	if a.humanOutput() {
		fmt.Fprintf(a.options.Err, "Run %s; waiting for %s (Ctrl-C stops waiting).\n", id, condition)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var run domain.Run
	if e := a.rpc(ctx, "run.wait", map[string]any{"target": id, "condition": condition}, &run); e != nil {
		return result, runWaitError(e, id)
	}
	return run, nil
}
func runWaitError(err error, id string) error {
	var de *domain.Error
	if errors.As(err, &de) {
		copy := *de
		copy.RunID = id
		return &copy
	}
	code := "RUN_PENDING"
	if errors.Is(err, context.Canceled) {
		code = "INTERRUPTED"
	}
	return &domain.Error{Code: code, Message: "waiting ended; the controller retains the run and its cleanup deadline", RunID: id, Retryable: true}
}
func (a *app) waitCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "wait RUN", Short: "Resume waiting for a tracked run without allocating again", Args: exactArgs(1)}
	cmd.Flags().String("for", "ready", "Wait for ready or finished")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		condition, _ := cmd.Flags().GetString("for")
		if condition != "ready" && condition != "finished" {
			return usage("--for must be ready or finished")
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		var run domain.Run
		if e := a.rpc(ctx, "run.wait", map[string]any{"target": args[0], "condition": condition}, &run); e != nil {
			return runWaitError(e, args[0])
		}
		return a.emitRun(run)
	}
	return cmd
}
func (a *app) emitRun(value any) error {
	if a.json || a.jsonl || a.ids {
		return a.emit(value)
	}
	raw, _ := json.Marshal(value)
	var run domain.Run
	if json.Unmarshal(raw, &run) != nil || run.ID == "" || run.State == "" {
		return a.emit(value)
	}
	result := map[string]any{"id": run.ID, "name": run.Name, "instance_id": run.InstanceID, "state": run.State, "deadline": run.Deadline, "cleanup": run.Policy.Cleanup}
	if run.LastError != "" {
		result["last_error"] = run.LastError
	}
	return a.emit(result)
}
func withHint(original *domain.Error) *domain.Error {
	d := *original
	if d.Hint != "" {
		return &d
	}
	switch d.Code {
	case "CONTROLLER_UNAVAILABLE", "CONTROLLER_CONNECT", "AGENT_NOT_RUNNING":
		d.Hint = "bupt agent status 或 bupt init"
	case "GATEWAY_AUTH_EXPIRED", "AUTH_REQUIRED", "AUTH_EXPIRED", "PLATFORM_AUTH_EXPIRED", "SSO_REQUIRED", "SSO_VERIFICATION_PENDING":
		d.Hint = "bupt login"
	case "CONSOLE_LOGIN_REQUIRED", "CONSOLE_LOGIN_FAILED":
		d.Hint = "bupt auth access"
	case "SSH_NOT_PREPARED":
		d.Hint = "bupt compute instance prepare-ssh INSTANCE"
	case "RELAY_REQUIRED", "SSH_NOT_READY", "SSH_DIAL_FAILED":
		d.Hint = "bupt doctor ssh"
	case "SPEC_REQUIRED", "SPEC_NOT_FOUND", "AMBIGUOUS_SPEC":
		d.Hint = "bupt compute catalog ls 或 bupt compute preset ls"
	case "RUN_LIMIT_REQUIRED", "LIMIT_REQUIRED":
		d.Hint = "使用 --max-runtime 2h，或选用已设置时限的预设"
	case "COST_AUTH_REQUIRED", "COST_ESTIMATE_UNAVAILABLE", "COST_LIMIT_EXCEEDED", "COST_SELECTION_CHANGED":
		d.Hint = "给原命令加 --dry-run，重新核对学校估价"
	}
	if d.RunID != "" {
		next := "bupt compute instance wait --operation " + d.RunID
		if d.Hint != "" {
			d.Hint += "。"
		}
		d.Hint += next
	}
	if d.TaskID != "" && d.RunID == "" {
		if d.Hint != "" {
			d.Hint += "。"
		}
		d.Hint += "bupt compute task show " + d.TaskID
	}
	return &d
}
