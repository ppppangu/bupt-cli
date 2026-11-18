package cli

import (
	"context"
	"io"
	"strings"

	"bupt.local/cli/internal/tui"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Build the advanced TUI forms from the same command objects and validators as
// the CLI. Arguments are passed as argv, never evaluated by a shell.
func commandForm(c *cobra.Command) tui.CommandForm {
	path := strings.Fields(strings.TrimPrefix(c.CommandPath(), "bupt "))
	f := tui.CommandForm{Path: path, Title: c.Name(), Summary: c.Short, Usage: c.Use, Runnable: c.Runnable()}
	for _, child := range c.Commands() {
		if !child.Hidden && child.Name() != "help" {
			f.Children = append(f.Children, commandForm(child))
		}
	}
	c.LocalNonPersistentFlags().VisitAll(func(flag *pflag.Flag) {
		if !flag.Hidden && flag.Name != "help" {
			f.Flags = append(f.Flags, tui.FormFlag{Name: flag.Name, Help: flag.Usage, Type: flag.Value.Type()})
		}
	})
	f.Terminal = strings.HasPrefix(strings.Join(path, " "), "compute ssh") || len(path) > 1 && (path[1] == "exec" || path[1] == "console" || path[1] == "forward" || path[1] == "sftp" || path[1] == "cp" || path[1] == "storage" && len(path) > 2 && (path[2] == "push" || path[2] == "pull"))
	f.Export = len(path) == 3 && path[0] == "api" && path[1] == "key" && path[2] == "export"
	// Browsing a page never executes its default command. All mutating leaves
	// preview first, including actions introduced in future command groups.
	name := c.Name()
	switch name {
	case "example", "ls", "list", "show", "inspect", "status", "features", "quota", "model", "key", "usage", "guide", "leaderboard", "display", "news", "account", "agreement", "catalog", "estimate", "stores", "events", "logs", "template", "cluster", "dataset", "project", "request", "job", "admin", "user", "partition", "storage", "task", "audit", "instance", "run", "history", "hpc":
		f.Query = true
	}
	if strings.HasPrefix(strings.Join(path, " "), "compute run submit") || strings.HasPrefix(strings.Join(path, " "), "compute run run") {
		f.Query = false
	}
	return f
}
func (a *app) tuiCommands(cmd *cobra.Command) map[string]tui.CommandForm {
	out := map[string]tui.CommandForm{}
	for _, n := range []string{"api", "compute"} {
		c, _, _ := cmd.Root().Find([]string{n})
		out[n] = commandForm(c)
	}
	return out
}

func boundedText(out io.Writer, n int) io.Writer { return &textLimit{dest: out, remaining: n} }

type textLimit struct {
	dest      io.Writer
	remaining int
	truncated bool
}

func (w *textLimit) Write(b []byte) (int, error) {
	n := min(len(b), w.remaining)
	if n > 0 {
		if _, e := w.dest.Write(b[:n]); e != nil {
			return 0, e
		}
		w.remaining -= n
	}
	if n < len(b) && !w.truncated {
		w.truncated = true
		_, _ = io.WriteString(w.dest, "\n… 输出已截断，可用等效 CLI 命令查看完整结果。\n")
	}
	return len(b), nil
}

func captureCommand(ctx context.Context, opts Options, args []string) (string, error) {
	var out strings.Builder
	dest := boundedText(&out, 128<<10)
	opts.Out, opts.Err = dest, dest
	opts.In = strings.NewReader("")
	opts.Interactive = func() bool { return false }
	root := NewRoot(opts)
	root.SetArgs(append([]string{"--non-interactive"}, args...))
	e := root.ExecuteContext(ctx)
	return out.String(), e
}
