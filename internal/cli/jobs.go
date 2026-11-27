package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

func (a *app) runCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "run [flags] -- COMMAND [ARG...]", Short: "Run a managed job with a finite deadline and durable cleanup policy", Args: rangeArgs(1, -1)}
	addResourceFlags(cmd)
	cmd.Flags().String("name", "", "Job name")
	cmd.Flags().String("instance", "", "Reuse an explicitly selected managed instance")
	rate := cmd.Flags().String("max-points-per-day", "", "新建实例时授权的积分日价上限")
	cmd.Flags().String("workdir", "", "Remote working directory")
	cmd.Flags().BoolP("detach", "d", false, "Return the job ID and let the controller continue managing it")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.ArgsLenAtDash() != 0 {
			return usage("use compute job run [flags] -- COMMAND [ARG...]")
		}
		instance, _ := cmd.Flags().GetString("instance")
		p, e := a.resourceParams(cmd, instance == "")
		if e != nil {
			return e
		}
		if !a.dryRun && instance == "" && *rate == "" {
			return usage("新建实例需要先用 --dry-run 估价，再指定 --max-points-per-day RATE")
		}
		p["command"] = args
		if *rate != "" {
			p["max_points_per_day"] = *rate
		}
		if instance != "" && !anyPolicyChanged(cmd) && !cmd.Flags().Changed("preset") {
			p["inherit_policy"] = true
		}
		for _, n := range []string{"name", "workdir", "instance"} {
			v, _ := cmd.Flags().GetString(n)
			if v != "" {
				key := n
				if n == "instance" {
					key = "target"
				}
				p[key] = v
			}
		}
		detach, _ := cmd.Flags().GetBool("detach")
		p["detach"] = detach
		p["wait"] = false
		var result any
		if a.dryRun {
			return a.callAndPrint(cmd, "job.run", p)
		}
		key := a.submissionKey(p)
		submitCtx, stopSubmit := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		e = a.rpc(submitCtx, "job.run", p, &result)
		stopSubmit()
		if e != nil {
			return submissionError(e, key)
		}
		id := resultID(result)
		if id == "" {
			return submissionError(domain.E("PROTOCOL_ERROR", "job.run returned no job identifier"), key)
		}
		if detach {
			return a.emit(result)
		}
		a.humanHint("作业 " + id + " 已提交")
		b, e := a.getBackend()
		if e != nil {
			return e
		}
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		osSignals := make(chan os.Signal, 1)
		signal.Notify(osSignals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(osSignals)
		cancelResult := make(chan error, 1)
		done := make(chan struct{})
		defer close(done)
		go func() {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-osSignals:
				stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer stopCancel()
				var ignored any
				cancelResult <- b.Call(stopCtx, "job.cancel", map[string]any{"target": id}, &ignored)
				cancel()
			}
		}()
		logsDone := make(chan struct{})
		if !a.ids && !a.json && !a.jsonl {
			go func() {
				defer close(logsDone)
				_, e := b.Stream(ctx, "job.logs", map[string]any{"target": id, "follow": true}, nil, a.options.Out)
				if e != nil && ctx.Err() == nil {
					fmt.Fprintln(a.options.Err, "logs:", e)
				}
			}()
		} else {
			close(logsDone)
		}
		defer func() { cancel(); <-logsDone }()
		run, e := a.waitJob(ctx, cmd, id)
		if e != nil {
			select {
			case err := <-cancelResult:
				if err != nil {
					e = &domain.Error{Code: "INTERRUPTED", Message: "取消请求未获确认，请检查作业状态", Retryable: true}
				}
			default:
			}
			return jobWaitError(e, id)
		}
		drain := time.NewTimer(5 * time.Second)
		select {
		case <-logsDone:
			if !drain.Stop() {
				<-drain.C
			}
		case <-drain.C:
		case <-ctx.Done():
			if !drain.Stop() {
				select {
				case <-drain.C:
				default:
				}
			}
		}
		cancel()
		if a.json || a.jsonl || a.ids {
			if e = a.emit(run); e != nil {
				return e
			}
		}
		return runExit(run)
	}
	return cmd
}
func jobWaitError(err error, id string) *domain.Error {
	result := domain.Error{Code: "JOB_WAIT_FAILED", Message: err.Error()}
	var problem *domain.Error
	switch {
	case errors.As(err, &problem):
		result = *problem
	case errors.Is(err, context.DeadlineExceeded):
		result.Code, result.Retryable = "TIMEOUT", true
	case errors.Is(err, context.Canceled):
		result.Code = "INTERRUPTED"
	}
	result.RunID = id
	// Preserve actionable auth/SSH hints before adding the job-specific wait.
	base := result
	base.RunID, base.TaskID = "", ""
	result.Hint = withHint(&base).Hint
	if result.Hint != "" {
		result.Hint += "。"
	}
	result.Hint += "bupt compute job wait " + shellArg(id) + " --json"
	return &result
}

func resultID(v any) string {
	b, _ := json.Marshal(v)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"id", "run_id", "job_id"} {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	for _, k := range []string{"run", "job"} {
		if x, ok := m[k]; ok {
			if s := resultID(x); s != "" {
				return s
			}
		}
	}
	return ""
}
func (a *app) jobCommands() *cobra.Command {
	g := &cobra.Command{Use: "job", Short: "Inspect durable jobs independently of the creating CLI process"}
	g.AddCommand(a.runCommand())
	list := a.simpleRPC("ls", "List jobs", "job.list", 0, 0, nil)

	addListFlags(list)
	get := a.simpleRPC("show JOB", "Show job, authentication, renewal and cleanup state", "job.get", 1, 1, targetParams)
	cancel := a.simpleRPC("cancel JOB", "Cancel a job and apply its configured cleanup policy", "job.cancel", 1, 1, targetParams)
	wait := &cobra.Command{Use: "wait JOB", Short: "Wait for job completion; interruption only stops waiting", Args: exactArgs(1)}
	wait.RunE = func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		run, e := a.waitJob(ctx, cmd, args[0])
		if e != nil {
			return e
		}
		if e = a.emit(run); e != nil {
			return e
		}
		return runExit(run)
	}
	logs := &cobra.Command{Use: "logs JOB", Short: "Read job output", Args: exactArgs(1)}
	logs.Flags().BoolP("follow", "f", false, "Follow output")
	logs.Flags().String("stream", "combined", "stdout, stderr, or combined")
	logs.RunE = func(cmd *cobra.Command, args []string) error {
		if e := a.rawOutput(); e != nil {
			return e
		}
		b, e := a.getBackend()
		if e != nil {
			return e
		}
		p := targetParams(args)
		copyChangedFlags(cmd, p)
		stream, _ := cmd.Flags().GetString("stream")
		if stream != "combined" && stream != "stdout" && stream != "stderr" {
			return usage("--stream must be stdout, stderr, or combined")
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		_, e = b.Stream(ctx, "job.logs", p, nil, a.options.Out)
		return e
	}
	attach := &cobra.Command{Use: "attach JOB", Short: "Follow a managed job's output until exit", Args: exactArgs(1)}
	attach.RunE = func(cmd *cobra.Command, args []string) error {
		if e := a.rawOutput(); e != nil {
			return e
		}
		b, e := a.getBackend()
		if e != nil {
			return e
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		_, e = b.Stream(ctx, "job.attach", targetParams(args), nil, a.options.Out)
		if e != nil {
			return e
		}
		run, e := a.waitJob(ctx, cmd, args[0])
		if e != nil {
			return e
		}
		return runExit(run)
	}
	update := &cobra.Command{Use: "update JOB", Short: "Change the policy of a running job explicitly", Args: exactArgs(1)}
	addPolicyFlags(update)
	update.RunE = func(cmd *cobra.Command, args []string) error {
		patch, e := policyPatch(cmd)
		if e != nil {
			return e
		}
		p := targetParams(args)
		p["policy"] = patch
		return a.callAndPrint(cmd, "job.update", p)
	}
	g.AddCommand(list, get, cancel, logs, advancedCommand(wait), advancedCommand(attach), advancedCommand(update))
	return g
}
func (a *app) waitJob(ctx context.Context, cmd *cobra.Command, id string) (domain.Run, error) {
	if cmd.Flags().Changed("timeout") {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	b, e := a.getBackend()
	if e != nil {
		return domain.Run{}, e
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var run domain.Run
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		e = b.Call(callCtx, "job.get", map[string]any{"target": id}, &run)
		cancel()
		if e != nil {
			return run, e
		}
		if run.LastError != "" && (run.AuthState == "action_required" || strings.HasPrefix(run.LastError, "CONSOLE_") || run.LastError == "RELAY_REQUIRED" || run.LastError == "SSH_NOT_READY") {
			return run, &domain.Error{Code: run.LastError, Message: "run remains tracked and needs action", RunID: run.ID, TaskID: run.LastTaskID, Retryable: true}
		}
		switch run.State {
		case "done", "complete", "completed", "failed", "cancelled", "canceled", "expired", "stopped":
			return run, nil
		}
		if run.ExitCode != nil && (run.CleanupState == "failed" || run.CleanupState == "cleanup_pending" || run.CleanupState == "auth_required") {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return run, ctx.Err()
		case <-ticker.C:
		}
	}
}
func runExit(run domain.Run) error {
	if run.ExitCode != nil && *run.ExitCode != 0 {
		return &ExitError{Code: *run.ExitCode}
	}
	if run.State == "expired" {
		return &ExitError{Code: 124, Diagnostic: &domain.Error{Code: "JOB_DEADLINE", Message: "作业达到运行上限", RunID: run.ID}}
	}
	if run.State == "stopped" && run.Kind == "job" && run.ExitCode == nil {
		return domain.E("JOB_INTERRUPTED", "instance stopped before the job's exit status could be confirmed")
	}
	if run.CleanupState == "failed" || run.CleanupState == "cleanup_pending" || run.CleanupState == "auth_required" {
		return &domain.Error{Code: "CLEANUP_PENDING", Message: "job exited, but resource cleanup has not completed", TaskID: run.LastTaskID, Retryable: true}
	}
	if run.State == "failed" {
		return domain.E("JOB_FAILED", run.LastError)
	}
	if run.State == "cancelled" || run.State == "canceled" {
		return &ExitError{Code: 130, Diagnostic: &domain.Error{Code: "JOB_CANCELLED", Message: "作业已取消", RunID: run.ID}}
	}
	return nil
}
