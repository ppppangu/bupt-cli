package cli

import (
	"fmt"
	"github.com/spf13/pflag"
	"io"
	"slices"
	"strings"

	"bupt.local/cli/internal/navigation"
	"github.com/spf13/cobra"
)

// The root prints the short command index. The full index is generated from
// Cobra so that new flags and commands remain discoverable.
func installDiscovery(root *cobra.Command) {
	native := root.HelpFunc()
	defaults := map[string]string{"instance": "ls", "spec": "ls", "preset": "ls", "job": "ls", "data": "stores", "auth": "status", "task": "ls"}
	for name, sub := range defaults {
		group, _, _ := root.Find([]string{name})
		child, _, _ := group.Find([]string{sub})
		group.Flags().AddFlagSet(child.LocalNonPersistentFlags())
		group.Args = exactArgs(0)
		group.RunE = child.RunE
	}
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		if cmd != root {
			briefCommand(cmd, defaults[cmd.Name()])
			return
		}
		w := cmd.OutOrStdout()
		fmt.Fprintln(w, "bupt   北邮算力平台")
		fmt.Fprintln(w, "\n用法  bupt <命令> [选项]")
		fmt.Fprintln(w, "\n平台    bupt login     bupt logout     bupt status     bupt account     bupt tui")
		fmt.Fprintln(w, "\n按用途浏览")
		fmt.Fprintln(w, "  bupt api             大模型 API 服务")
		fmt.Fprintln(w, "    model              可调用模型、ID、能力、上下文、价格、健康状态")
		fmt.Fprintln(w, "    usage              余额与用量")
		fmt.Fprintln(w, "    key                密钥与模型范围")
		fmt.Fprintln(w, "  bupt compute         裸算力服务")
		fmt.Fprintln(w, "    catalog            资源池、模板和可申请规格")
		fmt.Fprintln(w, "    instance           实例、预留、期限与节点")
		fmt.Fprintln(w, "    quota              账号与存储配额")
		fmt.Fprintln(w, "\nbupt help api 或 bupt help compute 查看业务命令。bupt help resources 查看模板相关快捷命令。")
		fmt.Fprintln(w, "\nbupt help --all  查看完整命令树和全局选项")
	})
	help := &cobra.Command{Use: "help [TOPIC|COMMAND...]", Short: "Browse commands by purpose or show the full command tree", Annotations: map[string]string{"skip_config": "true"}}
	all := help.Flags().BoolP("all", "a", false, "Show the complete command tree and global options")
	help.RunE = func(cmd *cobra.Command, args []string) error {
		w := cmd.OutOrStdout()
		if *all && len(args) == 0 {
			for _, area := range navigation.Areas {
				printArea(w, root, area, true)
			}
			fmt.Fprintln(w, "其他命令")
			for _, c := range root.Commands() {
				if c.Hidden {
					continue
				}
				known := false
				for _, area := range navigation.Areas {
					known = known || slices.Contains(area.Commands, c.Name())
				}
				if !known {
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
			return nil
		}
		return found.Help()
	}
	root.SetHelpCommand(help)
}
func printArea(w io.Writer, root *cobra.Command, area navigation.Area, full bool) {
	fmt.Fprintf(w, "\n%s · %s\n", area.Name, area.Summary)
	for _, name := range area.Commands {
		cmd, _, err := root.Find([]string{name})
		if err == nil && cmd != root {
			printBranch(w, cmd, "  ", full)
		}
	}
}
func printBranch(w io.Writer, cmd *cobra.Command, indent string, recursive bool) {
	fmt.Fprintf(w, "%s%-28s %s\n", indent, strings.TrimPrefix(cmd.CommandPath(), "bupt "), cmd.Short)
	if recursive {
		for _, child := range cmd.Commands() {
			if !child.Hidden {
				printBranch(w, child, indent+"  ", true)
			}
		}
	}
}

// Most people choosing a template or starting a session need a handful of
// options. The full Cobra help remains available for every command path.
var commonOptions = map[string][]string{
	"bupt up":              {"adopt", "preset", "spec", "max-runtime", "ssh", "no-wait"},
	"bupt down":            {"cleanup", "no-wait"},
	"bupt run":             {"instance", "preset", "name", "max-runtime", "detach"},
	"bupt job run":         {"instance", "preset", "name", "max-runtime", "detach"},
	"bupt instance create": {"spec", "preset", "gpu", "max-runtime", "no-wait"},
	"bupt preset set":      {"from", "spec", "max-runtime", "project"},
	"bupt init":            {"mode", "foreground", "install"},
	"bupt login":           {"sso-password", "username", "wait", "no-wait", "qr-file"},
	"bupt auth login":      {"sso-password", "username", "wait", "no-wait", "qr-file"},
}

func briefCommand(cmd *cobra.Command, defaultChild string) {
	w := cmd.OutOrStdout()
	fmt.Fprintln(w, cmd.Short)
	fmt.Fprintln(w, "\n用法  "+cmd.UseLine())
	if defaultChild != "" && cmd.Parent() == cmd.Root() {
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
