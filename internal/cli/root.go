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
type ExitError struct{ Code int }

func (e *ExitError) Error() string {
	return fmt.Sprintf("remote command exited with status %d", e.Code)
}

type app struct {
	options                                                                 Options
	root                                                                    *cobra.Command
	configFile, projectFile, profile, controller, socket, stateDir, dataDir string
	json, jsonl, quiet, noColor, nonInteractive, dryRun, noProject          bool
	timeout                                                                 time.Duration
	idempotencyKey                                                          string
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
	f.BoolVarP(&a.quiet, "quiet", "q", false, "Write identifiers or scalar values only")
	f.BoolVar(&a.noColor, "no-color", false, "Disable color and inline images")
	f.BoolVar(&a.nonInteractive, "non-interactive", false, "Never prompt for input")
	f.BoolVar(&a.dryRun, "dry-run", false, "Validate and describe changes without applying them")
	f.BoolVar(&a.noProject, "no-project", false, "Ignore project configuration")
	f.DurationVar(&a.timeout, "timeout", 10*time.Minute, "Time limit for an RPC or explicit wait; SSH sessions have no default limit")
	f.StringVar(&a.idempotencyKey, "idempotency-key", "", "Stable request key for safe retries")
	root.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if a.json && a.jsonl || a.quiet && (a.json || a.jsonl) {
			return usage("choose only one of --json, --jsonl, or --quiet")
		}
		if a.timeout <= 0 {
			return usage("--timeout must be positive")
		}
		if cmd == root && !a.json && !a.jsonl && !a.quiet {
			return nil
		}
		if cmd.Annotations["skip_config"] == "true" {
			return nil
		}
		return a.loadConfig(cmd)
	}
	root.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.json || a.jsonl || a.quiet {
			return a.statusCommand().RunE(cmd, nil)
		}
		return cmd.Help()
	}
	root.AddCommand(a.authCommands(), a.configCommands(), a.profileCommands(), a.specCommands(), a.instanceCommands(), a.jobCommands(), a.leaseCommands(), a.dataCommands(), a.snapshotCommands(), a.serviceCommands(), a.keyCommands(), a.agentCommands(), a.sshConfigCommands())
	root.AddCommand(a.upCommand(), a.downCommand(), a.runCommand(), a.sshCommand(), a.execCommand(), a.consoleCommand(), a.proxyCommand(), a.forwardCommand(), a.cpCommand(), a.sftpCommand(), a.resizeCommand(), a.cloneCommand(), a.initCommand())
	root.AddCommand(a.simpleRPC("quota", "Show allocation and billing limits", "quota.get", 0, 0, nil))
	root.AddCommand(a.estimateCommand())
	root.AddCommand(a.doctorCommand())
	ls := a.simpleRPC("ls", "List instances", "instance.list", 0, 0, nil)
	addListFlags(ls)
	root.AddCommand(ls)
	root.AddCommand(a.simpleRPC("inspect INSTANCE", "Inspect an instance", "instance.inspect", 1, 1, targetParams))
	root.AddCommand(a.taskCommands(), a.auditCommands())
	root.AddCommand(a.clientCommands())
	root.AddCommand(&cobra.Command{Use: "version", Short: "Show version", Args: exactArgs(0), Annotations: map[string]string{"skip_config": "true"}, RunE: func(cmd *cobra.Command, _ []string) error { return a.emit(map[string]any{"version": options.Version}) }})
	root.AddCommand(a.completionCommand(), a.loginCommand(), a.statusCommand(), a.presetCommands(), a.waitCommand(), a.tuiCommand(), a.resourcesCommand())
	root.AddCommand(a.apiCommand(), a.computeCommand())
	root.AddCommand(a.portalCommonCommands()...)
	installDiscovery(root)
	return root
}

func Execute(ctx context.Context, args []string, in io.Reader, out, errOut io.Writer, hooks Hooks) int {
	return ExecuteOptions(ctx, args, Options{In: in, Out: out, Err: errOut, Hooks: hooks})
}
func ExecuteOptions(ctx context.Context, args []string, opts Options) int {
	root := NewRoot(opts)
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return 0
	}
	var exit *ExitError
	if errors.As(err, &exit) {
		if exit.Code < 0 || exit.Code > 255 {
			return 1
		}
		return exit.Code
	}
	code := 1
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
	if machine || lines {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": de})
	} else {
		fmt.Fprintln(w, de.Error())
		if de.RunID != "" {
			fmt.Fprintln(w, "Run:", de.RunID)
		}
		if de.TaskID != "" {
			fmt.Fprintln(w, "Task:", de.TaskID)
		}
		if de.Hint != "" {
			fmt.Fprintln(w, "Next:", de.Hint)
		}
	}
	return code
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
	b, err := a.getBackend()
	if err != nil {
		return err
	}
	if params == nil {
		params = map[string]any{}
	}
	if a.dryRun {
		params["dry_run"] = true
	}
	if a.idempotencyKey != "" {
		params["idempotency_key"] = a.idempotencyKey
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	return b.Call(ctx, method, params, out)
}
func (a *app) callAndPrint(cmd *cobra.Command, method string, params map[string]any) error {
	var result any
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
