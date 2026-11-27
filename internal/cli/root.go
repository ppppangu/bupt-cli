// Package cli implements the human and machine interfaces. It never authenticates
// before opening an already provisioned SSH route.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/feedback"
	"github.com/spf13/cobra"
)

type Backend interface {
	Call(context.Context, string, any, any) error
	Session(context.Context, string, connect.SessionOptions, io.Reader, io.Writer, io.Writer, <-chan connect.WindowSize, <-chan string) (int, error)
	Proxy(context.Context, string, io.Reader, io.Writer) error
	Stream(context.Context, string, any, io.Reader, io.Writer) (map[string]any, error)
}

type Hooks struct {
	NewClient func(*config.Config, string) (Backend, error)
	Local     func(context.Context, *config.Config, string, map[string]any, io.Reader, io.Writer, io.Writer) (any, error)
}

type nodeSessionBackend interface {
	SessionNode(context.Context, string, string, connect.SessionOptions, io.Reader, io.Writer, io.Writer, <-chan connect.WindowSize, <-chan string) (int, error)
}
type nodeProxyBackend interface {
	ProxyNode(context.Context, string, string, io.Reader, io.Writer) error
}

type Options struct {
	In            io.Reader
	Out, Err      io.Writer
	Hooks         Hooks
	Version       string
	ConfigOptions config.Options
	LoadConfig    func(config.Options) (*config.Config, error)
	// Optional interaction hooks keep tests independent of a real terminal/UI.
	Interactive      func() bool
	ReadSecret       func(string) (string, error)
	OpenImage        func(context.Context, string) error
	StderrIsTerminal func() bool
}

type UsageError struct{ Err error }

func (e *UsageError) Error() string          { return e.Err.Error() }
func (e *UsageError) Unwrap() error          { return e.Err }
func usage(format string, args ...any) error { return &UsageError{fmt.Errorf(format, args...)} }

// ExitError transports a remote command's status without printing a fabricated
// local error message or conflating it with an authentication failure.
type ExitError struct {
	Code       int
	Diagnostic *domain.Error
}

func (e *ExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

type app struct {
	options                                                                 Options
	root                                                                    *cobra.Command
	configFile, projectFile, profile, controller, socket, stateDir, dataDir string
	json, jsonl, ids, noColor, nonInteractive, dryRun, noProject            bool
	timeout                                                                 time.Duration
	format, field, idempotencyKey                                           string
	cfg                                                                     *config.Config
	backend                                                                 Backend
	transferResults                                                         *[]any
}

func NewRoot(options Options) *cobra.Command {
	if options.In == nil {
		options.In = os.Stdin
	}
	if options.Out == nil {
		options.Out = os.Stdout
	}
	if options.Err == nil {
		options.Err = os.Stderr
	}
	if options.LoadConfig == nil {
		options.LoadConfig = config.Load
	}
	if options.Version == "" {
		options.Version = "dev"
	}
	a := &app{options: options}
	root := &cobra.Command{
		Use: "bupt", Short: "Manage BUPT compute instances, jobs, and SSH connections",
		Long:         "A Unix-style client for BUPT compute. Local controllers are the default; remote controllers are optional. Existing SSH routes remain usable when school authentication expires.",
		SilenceUsage: true, SilenceErrors: true, Version: options.Version,
	}
	a.root = root
	root.SetIn(options.In)
	root.SetOut(options.Out)
	root.SetErr(options.Err)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return &UsageError{err} })
	f := root.PersistentFlags()
	f.StringVar(&a.configFile, "config", "", "Global configuration file")
	f.StringVar(&a.projectFile, "project-config", "", "Project configuration file")
	f.StringVarP(&a.profile, "profile", "p", "", "Account profile")
	f.StringVar(&a.controller, "controller", "", "Controller: local or an ssh:// endpoint")
	f.StringVar(&a.socket, "socket", "", "Local controller socket")
	f.StringVar(&a.stateDir, "state-dir", "", "Override state directory")
	f.StringVar(&a.dataDir, "data-dir", "", "Override data directory")
	f.BoolVar(&a.json, "json", false, "Write a single JSON value to stdout")
	f.BoolVar(&a.jsonl, "jsonl", false, "Write newline-delimited JSON records")
	f.StringVar(&a.format, "output", "", "输出格式 table、json、jsonl 或 id")
	f.StringVar(&a.field, "field", "", "输出一个受支持的标量字段")
	f.BoolVar(&a.noColor, "no-color", false, "Disable color and inline images")
	f.BoolVar(&a.nonInteractive, "non-interactive", false, "Never prompt for input")
	f.BoolVar(&a.dryRun, "dry-run", false, "Validate and describe changes without applying them")
	f.BoolVar(&a.noProject, "no-project", false, "Ignore project configuration")
	f.DurationVar(&a.timeout, "timeout", 10*time.Minute, "Time limit for an RPC or explicit wait; SSH sessions have no default limit")
	f.StringVar(&a.idempotencyKey, "idempotency-key", "", "Stable request key for safe retries")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if cmd.Name() == cobra.ShellCompRequestCmd {
			return nil
		}
		if agentLoginRequested(cmd) && a.format == "" && !a.jsonl {
			a.json = true
		}
		// Treat incompatible options as usage errors, including in machine mode.
		if err := cmd.ValidateFlagGroups(); err != nil {
			return &UsageError{err}
		}
		selected := 0
		for _, name := range []string{"output", "json", "jsonl", "field"} {
			if cmd.Flags().Changed(name) {
				selected++
			}
		}
		if selected > 1 {
			return usage("任选一个 --output、--json、--jsonl 或 --field")
		}
		switch a.format {
		case "", "table":
		case "json":
			a.json = true
		case "jsonl":
			a.jsonl = true
		case "id":
			a.ids = true
		default:
			return usage("--output 支持 table、json、jsonl 或 id")
		}
		if cmd.Flags().Changed("field") && (cmd.CommandPath() != "bupt api usage" || a.field != "balance") {
			return usage("字段选择当前支持 bupt api usage --field balance")
		}
		if a.ids && !supportsID(cmd) {
			return usage("%s 没有稳定 ID 输出", cmd.CommandPath())
		}
		if a.timeout <= 0 {
			return usage("--timeout must be positive")
		}
		if cmd == root {
			return nil
		}
		if cmd.Annotations["skip_config"] == "true" {
			return nil
		}
		return a.loadConfig(cmd)
	}
	root.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
	compute, advanced := a.computeCommand()
	keys := a.keyCommands()
	clients := a.clientCommands()
	clients.AddCommand(keys)
	root.AddCommand(a.loginCommand(), a.logoutCommand(), a.tuiCommand(), a.apiCommand(), compute,
		a.sshCommand(), a.execCommand(), a.cpCommand(), a.forwardCommand(), a.usageCommand(), a.doctorCommand(), a.feedbackCommand(), a.configCommands(),
		a.completionCommand(), &cobra.Command{Use: "version", Short: "显示版本", Args: exactArgs(0), Annotations: map[string]string{"skip_config": "true"}, RunE: func(cmd *cobra.Command, _ []string) error {
			if a.json || a.jsonl {
				return a.emit(map[string]any{"version": options.Version})
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s version %s\n", root.Name(), options.Version)
			return err
		}})
	root.AddCommand(advanced...)
	for _, cmd := range append(a.portalCommonCommands(), a.authCommands(), a.profileCommands(), a.agentCommands(), a.sshConfigCommands(),
		a.consoleCommand(), a.proxyCommand(), a.sftpCommand(), a.initCommand(), clients, a.statusCommand()) {
		markAdvanced(cmd)
		root.AddCommand(cmd)
	}
	a.installDiscovery(root)
	a.installObjectCompletion(root)
	return root
}

func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, hooks Hooks) int {
	return ExecuteOptions(ctx, args, Options{In: in, Out: out, Err: errOut, Hooks: hooks})
}
func ExecuteOptions(ctx context.Context, args []string, opts Options) int {
	root := NewRoot(opts)
	root.SetArgs(args)
	command, err := root.ExecuteContextC(ctx)
	if err == nil {
		return 0
	}
	var exit *ExitError
	code := 1
	if errors.As(err, &exit) {
		if exit.Code < 0 || exit.Code > 255 {
			return 1
		}
		if exit.Diagnostic == nil {
			return exit.Code
		}
		code, err = exit.Code, exit.Diagnostic
	}
	var ue *UsageError
	if errors.As(err, &ue) || strings.HasPrefix(err.Error(), "unknown command ") {
		code = 2
	}
	if errors.Is(err, context.Canceled) {
		code = 130
	}
	w := root.ErrOrStderr()
	machine, _ := root.PersistentFlags().GetBool("json")
	lines, _ := root.PersistentFlags().GetBool("jsonl")
	var de *domain.Error
	if !errors.As(err, &de) {
		de = &domain.Error{Code: "ERROR", Message: err.Error()}
		if code == 2 {
			de.Code = "USAGE"
		}
		if code == 130 {
			de.Code = "INTERRUPTED"
		}
		if errors.Is(err, context.DeadlineExceeded) {
			de.Code = "TIMEOUT"
			de.Retryable = true
		}
	}
	de = withHint(de)
	if de.Code == "INTERRUPTED" {
		code = 130
	}
	format, _ := root.PersistentFlags().GetString("output")
	if machine || lines || format == "json" || format == "jsonl" || machineRequested(command, args) {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": de})
	} else {
		fmt.Fprintf(w, "BUPT[%s] %s\n", de.Code, de.Message)
		if de.RunID != "" {
			fmt.Fprintln(w, "Run:", de.RunID)
		}
		if de.TaskID != "" {
			fmt.Fprintln(w, "Task:", de.TaskID)
		}
		if de.RequestID != "" {
			fmt.Fprintln(w, "请求编号", scalar(de.RequestID))
		}
		if de.IdempotencyKey != "" {
			fmt.Fprintln(w, "重试使用相同请求键 --idempotency-key", shellArg(de.IdempotencyKey))
		}
		if !root.PersistentFlags().Changed("field") && !root.PersistentFlags().Changed("non-interactive") && terminalWriter(w, opts.StderrIsTerminal) && isTerminal(root.OutOrStdout()) {
			if de.Hint != "" {
				fmt.Fprintln(w, "Next:", de.Hint)
			}
			if command != nil && command.Name() != "feedback" && code != 130 && (code == 1 || command.Name() == "tui") {
				i := feedback.FromError(command.CommandPath(), de)
				hint := "bupt feedback --template --at " + shellArg(i.Time.Format(time.RFC3339))
				if i.Operation != "" {
					hint += " --operation " + shellArg(i.Operation)
				}
				for _, arg := range []struct{ flag, value string }{
					{"code", i.Code}, {"request-id", i.RequestID}, {"run-id", i.RunID}, {"task-id", i.TaskID},
				} {
					if arg.value != "" {
						hint += " --" + arg.flag + " " + shellArg(arg.value)
					}
				}
				fmt.Fprintln(w, "脱敏诊断  bupt doctor --report")
				fmt.Fprintln(w, "反馈模板 ", hint)
			}
		}
	}
	return code
}

func machineRequested(cmd *cobra.Command, args []string) bool {
	// Flag parsing can stop at an unknown option before reaching --agent.
	agent := false
	table := false
	for i, arg := range args {
		switch arg {
		case "--":
			return agent && !table
		case "--agent", "--agent=true":
			agent = cmd != nil && cmd.Flags().Lookup("agent") != nil
		case "--agent=false":
			agent = false
		case "--output=table":
			table = true
		case "--json", "--json=true", "--jsonl", "--jsonl=true", "--output=json", "--output=jsonl":
			return true
		case "--output":
			table = i+1 < len(args) && args[i+1] == "table"
			if i+1 < len(args) && (args[i+1] == "json" || args[i+1] == "jsonl") {
				return true
			}
		}
	}
	return agent && !table
}

func (a *app) loadConfig(cmd *cobra.Command) error {
	o := a.options.ConfigOptions
	o.AllowMissingConfig = cmd.Name() == "init"
	if a.configFile != "" {
		o.ConfigFile = a.configFile
	}
	if a.projectFile != "" {
		o.ProjectFile = a.projectFile
	}
	if a.profile != "" {
		o.Profile = a.profile
	}
	if a.stateDir != "" {
		o.StateDir = a.stateDir
	}
	if a.dataDir != "" {
		o.DataDir = a.dataDir
	}
	o.NoProject = o.NoProject || a.noProject
	var err error
	a.cfg, err = a.options.LoadConfig(o)
	if err != nil {
		return err
	}
	if a.controller != "" || a.socket != "" {
		if o.Overrides == nil {
			o.Overrides = map[string]any{}
		}
		if a.controller != "" {
			o.Overrides["profiles."+a.cfg.Selected+".controller"] = a.controller
		}
		if a.socket != "" {
			o.Overrides["profiles."+a.cfg.Selected+".controller_socket"] = a.socket
		}
		a.cfg, err = a.options.LoadConfig(o)
		if err != nil {
			return err
		}
	}
	return nil
}
func (a *app) getBackend() (Backend, error) {
	if a.backend != nil {
		return a.backend, nil
	}
	if a.options.Hooks.NewClient == nil {
		return nil, domain.E("CONTROLLER_UNAVAILABLE", "controller transport is not configured")
	}
	b, err := a.options.Hooks.NewClient(a.cfg, a.cfg.Selected)
	if err != nil {
		return nil, err
	}
	a.backend = b
	return b, nil
}
func (a *app) rpc(ctx context.Context, method string, params map[string]any, out any) error {
	if params == nil {
		params = map[string]any{}
	}
	if a.dryRun {
		params["dry_run"] = true
	}
	if a.idempotencyKey != "" {
		params["idempotency_key"] = a.idempotencyKey
	}
	return a.rpcRequest(ctx, method, params, out)
}

func (a *app) rpcRequest(ctx context.Context, method string, request any, out any) error {
	b, err := a.getBackend()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	return b.Call(ctx, method, request, out)
}
func (a *app) callAndPrint(cmd *cobra.Command, method string, params map[string]any) error {
	var result json.RawMessage
	if err := a.rpc(cmd.Context(), method, params, &result); err != nil {
		return err
	}
	return a.emit(result)
}
func (a *app) local(cmd *cobra.Command, method string, params map[string]any) error {
	if a.options.Hooks.Local == nil {
		return domain.E("LOCAL_OPERATION_UNAVAILABLE", "this installation does not provide "+method)
	}
	if params == nil {
		params = map[string]any{}
	}
	if a.dryRun {
		params["dry_run"] = true
	}
	v, err := a.options.Hooks.Local(cmd.Context(), a.cfg, method, params, a.options.In, a.options.Out, a.options.Err)
	if err != nil {
		return err
	}
	if v != nil {
		return a.emit(v)
	}
	return nil
}
func exactArgs(n int) cobra.PositionalArgs { return rangeArgs(n, n) }
func rangeArgs(min, max int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) < min || max >= 0 && len(args) > max {
			return usage("expected %d..%d positional arguments, got %d", min, max, len(args))
		}
		return nil
	}
}
func targetParams(args []string) map[string]any { return map[string]any{"target": args[0]} }
func (a *app) simpleRPC(use, short, method string, min, max int, params func([]string) map[string]any) *cobra.Command {
	return &cobra.Command{Use: use, Short: short, Args: rangeArgs(min, max), RunE: func(cmd *cobra.Command, args []string) error {
		p := map[string]any{}
		if params != nil {
			p = params(args)
		}
		copyChangedFlags(cmd, p)
		return a.callAndPrint(cmd, method, p)
	}}
}

func copyChangedFlags(cmd *cobra.Command, p map[string]any) {
	for _, name := range []string{"cached", "state", "filter", "columns", "node", "group-id", "bill-id", "reason", "force", "wait", "no-wait", "overwrite", "recursive", "stop-now", "store", "path", "stream", "follow", "limit", "after", "mode", "public-key-file", "identity-file", "host", "bind", "port", "local", "remote-host", "snapshot-id", "name", "new-size-gb", "cpu", "memory", "gpu", "disk", "spec-id", "duration", "usage", "email", "domain"} {
		f := cmd.Flags().Lookup(name)
		if f == nil || !f.Changed {
			continue
		}
		key := strings.ReplaceAll(name, "-", "_")
		switch f.Value.Type() {
		case "bool":
			v, _ := cmd.Flags().GetBool(name)
			p[key] = v
		case "int":
			v, _ := cmd.Flags().GetInt(name)
			p[key] = v
		case "int64":
			v, _ := cmd.Flags().GetInt64(name)
			p[key] = v
		case "stringSlice":
			v, _ := cmd.Flags().GetStringSlice(name)
			p[key] = v
		default:
			p[key] = f.Value.String()
		}
	}
}
