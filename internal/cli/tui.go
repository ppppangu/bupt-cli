package cli

import (
	"bytes"
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
	"bupt.local/cli/internal/tui"
	"github.com/spf13/cobra"
)

func (a *app) tuiTerminal() bool {
	return isTerminal(a.options.In) && isTerminal(a.options.Out) && os.Getenv("TERM") != "dumb"
}
func (a *app) tuiCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "tui [AREA]", Short: "Open the interactive platform overview", Args: rangeArgs(0, 1)}
	cached := cmd.Flags().Bool("cached", false, "Browse saved status and controller records without contacting the school")
	for _, area := range navigation.Areas {
		cmd.ValidArgs = append(cmd.ValidArgs, area.ID)
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		area := ""
		if len(args) > 0 {
			area = args[0]
			if _, ok := navigation.Find(area); !ok {
				return usage("unknown area %q; use bupt help", area)
			}
		}
		return a.openTUI(cmd, *cached, area)
	}
	return cmd
}
func (a *app) openTUI(cmd *cobra.Command, cached bool, area string) error {
	if a.json || a.jsonl || a.quiet || a.nonInteractive {
		return usage("tui requires an interactive terminal; use bupt status --json for scripts")
	}
	if a.dryRun || a.idempotencyKey != "" {
		return usage("use --dry-run or --idempotency-key on an individual command; the TUI previews changes before submission")
	}
	if !a.tuiTerminal() {
		return domain.E("TTY_REQUIRED", "bupt tui 需要交互终端；可用 bupt status 或 bupt help 查看平台")
	}
	b, err := a.getBackend()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After temporarily releasing the screen, use the same terminal handlers
	// (QR, SSH, password input and file copies) and the same selected account.
	foreground := func(ctx context.Context, args []string) error {
		opts := a.options
		opts.LoadConfig = func(config.Options) (*config.Config, error) { return a.cfg, nil }
		opts.Hooks.NewClient = func(*config.Config, string) (Backend, error) { return b, nil }
		child := NewRoot(opts)
		prefix := []string{}
		if a.noColor {
			prefix = append(prefix, "--no-color")
		}
		if cmd.Flags().Changed("timeout") {
			prefix = append(prefix, "--timeout", a.timeout.String())
		}
		child.SetArgs(append(prefix, args...))
		return child.ExecuteContext(ctx)
	}
	explain := func(name string) string {
		var text bytes.Buffer
		opts := a.options
		opts.Out = &text
		opts.Err = &text
		root := NewRoot(opts)
		root.SetArgs([]string{"help", name})
		if err := root.ExecuteContext(ctx); err != nil {
			return err.Error()
		}
		return text.String()
	}
	capture := func(ctx context.Context, args []string) (string, error) {
		opts := a.options
		opts.LoadConfig = func(config.Options) (*config.Config, error) { return a.cfg, nil }
		opts.Hooks.NewClient = func(*config.Config, string) (Backend, error) { return b, nil }
		return captureCommand(ctx, opts, args)
	}
	return tui.Run(ctx, tui.Options{Commands: a.tuiCommands(cmd), Capture: capture, InitialArea: area, Explain: explain, Config: a.cfg, Backend: b, Foreground: foreground, Cached: cached, NoColor: a.noColor || strings.TrimSpace(os.Getenv("NO_COLOR")) != "", Timeout: a.timeout})
}
