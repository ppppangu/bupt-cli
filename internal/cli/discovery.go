package cli

import (
	"fmt"
	"io"
	"slices"
	"strings"

	"bupt.local/cli/internal/navigation"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func completeValues(cmd *cobra.Command, flag string, values ...string) {
	_ = cmd.RegisterFlagCompletionFunc(flag, func(_ *cobra.Command, _ []string, prefix string) ([]string, cobra.ShellCompDirective) {
		var matches []string
		for _, value := range values {
			if strings.HasPrefix(value, prefix) {
				matches = append(matches, value)
			}
		}
		return matches, cobra.ShellCompDirectiveNoFileComp
	})
}

// The root prints the short command index. The full index is generated from
// Cobra so that new flags and commands remain discoverable.
func installDiscovery(root *cobra.Command) {
	completeValues(root, "output", "table", "json", "jsonl", "id")
	completeValues(root, "field", "balance")
	native := root.HelpFunc()
	for path, sub := range map[string]string{"auth": "status", "compute preset": "ls", "compute task": "ls"} {
		if group, remaining, _ := root.Find(strings.Fields(path)); group != nil && group != root && len(remaining) == 0 {
			defaultTo(group, sub)
		}
	}
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd != root {
			briefCommand(cmd, cmd.Annotations["default_child"])
			return
		}
		w := cmd.OutOrStdout()
		fmt.Fprintln(w, "bupt   北邮算力平台")
		fmt.Fprintln(w, "\n用法  bupt <命令> [选项]\n\n按用途浏览")
		fmt.Fprintln(w, "  login / logout      登录与退出学校账号\n  tui                 打开交互工作台")
		for _, c := range root.Commands() {
			if !c.Hidden && !slices.Contains([]string{"login", "logout", "tui", "help", "version", "completion"}, c.Name()) {
				fmt.Fprintf(w, "  bupt %-14s %s\n", c.Name(), c.Short)
			}
		}
		fmt.Fprintln(w, "\nbupt help COMMAND  查看命令和选项\nbupt help tutorial  阅读教程\nbupt help --all  查看完整命令树")

	})
	help := &cobra.Command{Use: "help [TOPIC|COMMAND...]", Short: "Browse commands by purpose or show the full command tree", Annotations: map[string]string{"skip_config": "true"}}
	all := help.Flags().BoolP("all", "a", false, "Show the complete command tree and global options")
	help.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
		if len(args) > 0 && args[0] == "tutorial" {
			if len(args) > 2 {
				return usage("bupt help tutorial [TOPIC]")
			}
			if len(args) == 2 {
				for _, l := range navigation.Lessons {
					if l.ID == args[1] {
						fmt.Fprintf(w, "%s\n\n%s\n", l.Title, l.Text)
						return nil
					}
				}
				return usage("unknown tutorial topic %q", args[1])
			}
			fmt.Fprintln(w, "入门与帮助\n\n用法  bupt help tutorial TOPIC")
			for _, l := range navigation.Lessons {
				fmt.Fprintf(w, "  %-8s %s\n", l.ID, l.Title)
			}
			fmt.Fprintln(w, "\nbupt help --all  完整命令树")
			return nil
		}
		if *all && len(args) == 0 {
			fmt.Fprintln(w, "完整命令树")
			for _, c := range root.Commands() {
				if discoverable(c) {
					printBranch(w, c, "  ", true)
				}
			}
			fmt.Fprint(w, "\n全局选项\n", root.PersistentFlags().FlagUsages())
			return nil
		}
		if len(args) == 0 {
			return root.Help()
		}
		if area, ok := navigation.Find(args[0]); ok && len(args) == 1 && args[0] != "api" && args[0] != "compute" && !(args[0] == "resources" && *all) {
			printArea(w, root, area, *all)
			fmt.Fprintln(w, "\n使用 bupt help COMMAND 查看子命令和选项。")
			return nil
		}
		found, remaining, e := root.Find(args)
		if e != nil || len(remaining) > 0 || found == root {
			return usage("unknown help topic or command %q", strings.Join(args, " "))
		}
		if *all {
			found.InitDefaultHelpFlag()
			native(found, nil)
			for _, child := range found.Commands() {
				if child.Hidden && discoverable(child) {
					printBranch(w, child, "  ", true)
				}
			}
			return nil
		}
		return found.Help()
	}
	root.SetHelpCommand(help)
}
func printArea(w io.Writer, root *cobra.Command, area navigation.Area, full bool) {
	fmt.Fprintf(w, "\n%s · %s\n", area.Name, area.Summary)
	for _, name := range area.Commands {
		cmd, rest, err := root.Find(strings.Fields(name))
		if err == nil && cmd != root && len(rest) == 0 && (full || !cmd.Hidden) {
			printBranch(w, cmd, "  ", full)
		}
	}
}
func printBranch(w io.Writer, cmd *cobra.Command, indent string, recursive bool) {
	fmt.Fprintf(w, "%s%-28s %s\n", indent, strings.TrimPrefix(cmd.CommandPath(), "bupt "), cmd.Short)
	if recursive {
		for _, child := range cmd.Commands() {
			if discoverable(child) {
				printBranch(w, child, indent+"  ", true)
			}
		}
	}
}

// Most people choosing a template or starting a session need a handful of
// options. The full Cobra help remains available for every command path.
var commonOptions = map[string][]string{
	"bupt feedback":                {"template", "diagnostics", "category"},
	"bupt doctor":                  {"report", "offline"},
	"bupt compute instance create": {"spec", "preset", "gpu", "max-runtime", "allocate-only", "max-points-per-day", "wait"},
	"bupt init":                    {"mode", "foreground", "install"},
	"bupt login":                   {"sso-password", "username", "wait", "no-wait", "qr-file"},
}

func markAdvanced(c *cobra.Command) {
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations["advanced"] = "true"
	c.Hidden = true
}

func advancedCommand(c *cobra.Command) *cobra.Command { markAdvanced(c); return c }

func discoverable(c *cobra.Command) bool { return !c.Hidden || c.Annotations["advanced"] == "true" }

func briefCommand(cmd *cobra.Command, defaultChild string) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, cmd.Short)
	fmt.Fprintln(w, "\n用法  "+cmd.UseLine())
	if defaultChild != "" {
		fmt.Fprintf(w, "直接运行等同于 %s %s\n", cmd.CommandPath(), defaultChild)
	}
	if cmd.HasAvailableSubCommands() {
		fmt.Fprintln(w, "\n子命令")
		for _, child := range cmd.Commands() {
			if !child.Hidden {
				fmt.Fprintf(w, "  %-17s %s\n", child.Name(), child.Short)
			}
		}
	}
	flags := pflag.NewFlagSet("", pflag.ContinueOnError)
	keep, limited := commonOptions[cmd.CommandPath()]
	cmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) {
		if !f.Hidden && f.Name != "help" && (!limited || slices.Contains(keep, f.Name)) {
			flags.AddFlag(f)
		}
	})
	if flags.HasAvailableFlags() {
		fmt.Fprint(w, "\n常用选项\n", flags.FlagUsages())
	}
	fmt.Fprintf(w, "\n完整选项  bupt help %s --all\n", strings.TrimPrefix(cmd.CommandPath(), "bupt "))
}
