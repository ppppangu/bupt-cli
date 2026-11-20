package cli

import (
	"context"
	"encoding/json"
	"io"
	"strings"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/tui"
	"github.com/spf13/cobra"
)

func (a *app) runTerminalAction(ctx context.Context, request tui.TerminalAction, timed bool) error {
	var cmd *cobra.Command
	switch request.Name {
	case "login":
		cmd = a.loginCommand()
	case "logout":
		cmd = a.logoutCommand()
	case "auth access":
		cmd = a.authAccessCommand()
	case "ssh":
		cmd = a.sshCommand()
	case "compute instance prepare-ssh":
		cmd = a.sshRepairCommand()
	case "exec":
		cmd = a.execCommand()
	case "cp":
		cmd = a.cpCommand()
	case "console":
		cmd = a.consoleCommand()
	case "forward":
		cmd = a.forwardCommand()
	case "sftp":
		cmd = a.sftpCommand()
	case "api key export":
		cmd = a.exportPortalKey()
	case "compute task wait":
		cmd, _, _ = a.taskCommands().Find([]string{"wait"})
	case "compute instance wait":
		cmd = &cobra.Command{RunE: a.waitInstance}
		cmd.Flags().String("state", "ssh-ready", "")
		cmd.Flags().String("node", "", "")
		cmd.Flags().String("operation", "", "")
	case "compute lease wait":
		cmd = a.waitCommand()
	case "compute job wait", "compute job attach":
		cmd, _, _ = a.jobCommands().Find(strings.Fields(request.Name)[2:])
	case "compute storage push", "compute storage pull":
		cmd, _, _ = a.dataCommands().Find(strings.Fields(request.Name)[2:])
	default:
		return domain.E("UNSUPPORTED", "该终端操作不可用")
	}
	if timed {
		cmd.Flags().Duration("timeout", a.timeout, "")
		_ = cmd.Flags().Set("timeout", a.timeout.String())
	}
	// These handlers share terminal I/O and validation with the CLI. The TUI
	// gives them an already selected action and bound fields.
	return invokeTerminalHandler(ctx, cmd, request.Args, request.Options)
}

func invokeTerminalHandler(ctx context.Context, cmd *cobra.Command, args []string, options map[string]string) error {
	cmd.SetContext(ctx)
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations["terminal_action"] = "true"
	for key, value := range options {
		if err := cmd.Flags().Set(key, value); err != nil {
			return err
		}
	}
	if cmd.Args != nil {
		if err := cmd.Args(cmd, args); err != nil {
			return err
		}
	}
	return cmd.RunE(cmd, args)
}

func (a *app) applyPresetForm(ctx context.Context, method string, params map[string]any, dryRun bool) (json.RawMessage, error) {
	sub := strings.TrimPrefix(method, "preset.")
	options := map[string]string{}
	keys := []string{"from", "spec", "spec_id", "max_runtime", "until", "cpu", "gpu", "memory", "disk", "cleanup", "force_stop", "renew", "project"}
	for _, key := range keys {
		if v, ok := params[key]; ok {
			options[strings.ReplaceAll(key, "_", "-")] = scalar(v)
		}
	}
	local := *a
	local.dryRun = dryRun
	local.options.Out = io.Discard
	var values []any
	local.transferResults = &values
	cmd, _, _ := local.presetCommands().Find([]string{sub})
	target, _ := params["target"].(string)
	if err := invokeTerminalHandler(ctx, cmd, []string{target}, options); err != nil {
		return nil, err
	}
	if len(values) == 1 {
		return json.Marshal(values[0])
	}
	return json.Marshal(values)
}
