package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

func (a *app) configCommands() *cobra.Command {
	g := &cobra.Command{Use: "config", Short: "Inspect and update layered, non-secret configuration"}
	g.AddCommand(&cobra.Command{Use: "explain", Short: "Show effective values and their source layers", Args: exactArgs(0), RunE: func(_ *cobra.Command, _ []string) error { return a.emit(a.cfg.Explain()) }})
	g.AddCommand(&cobra.Command{Use: "validate", Short: "Validate resolved configuration", Args: exactArgs(0), RunE: func(_ *cobra.Command, _ []string) error {
		return a.emit(map[string]any{"valid": true, "profile": a.cfg.Selected})
	}})
	g.AddCommand(&cobra.Command{Use: "get KEY", Short: "Read a resolved configuration key", Args: exactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		v, ok := a.cfg.Get(args[0])
		if !ok {
			return domain.E("CONFIG_KEY_NOT_FOUND", args[0])
		}
		return a.emit(map[string]any{"key": args[0], "value": v, "source": a.cfg.Sources[args[0]]})
	}})
	set := &cobra.Command{Use: "set KEY VALUE", Short: "Set a non-secret configuration value (does not change running jobs)", Args: exactArgs(2)}
	set.Flags().Bool("project", false, "Write to the selected project configuration")
	set.RunE = func(cmd *cobra.Command, args []string) error {
		project, _ := cmd.Flags().GetBool("project")
		path := a.cfg.Paths.ConfigFile
		if project {
			if args[0] != "profile" && args[0] != "default_preset" && args[0] != "version" && !strings.HasPrefix(args[0], "policy.") && !strings.HasPrefix(args[0], "presets.") {
				return domain.E("UNTRUSTED_PROJECT_SETTING", "project configuration may select a profile or set policy/presets; trusted endpoints and paths belong in global configuration")
			}
			path = a.cfg.Paths.ProjectFile
			if path == "" {
				return domain.E("PROJECT_CONFIG_NOT_FOUND", "select an existing project with --project-config")
			}
		}
		var v any
		d := json.NewDecoder(strings.NewReader(args[1]))
		d.UseNumber()
		if d.Decode(&v) != nil || !json.Valid([]byte(args[1])) {
			v = args[1]
		}
		if a.dryRun {
			return a.emit(map[string]any{"operation": "config.set", "file": path, "key": args[0], "value": config.Redact(map[string]any{args[0]: v}), "dry_run": true})
		}
		if err := config.SetFile(path, args[0], v); err != nil {
			return err
		}
		return a.emit(map[string]any{"file": path, "key": args[0], "updated": true})
	}
	g.AddCommand(set)
	return g
}
func (a *app) profileCommands() *cobra.Command {
	g := &cobra.Command{Use: "profile", Short: "Manage trusted account and controller profiles"}
	g.AddCommand(&cobra.Command{Use: "ls", Short: "List configured profiles", Args: exactArgs(0), RunE: func(_ *cobra.Command, _ []string) error {
		keys := []string{}
		for n := range a.cfg.Profiles {
			keys = append(keys, n)
		}
		sort.Strings(keys)
		result := []map[string]any{}
		for _, n := range keys {
			p := a.cfg.Profiles[n]
			result = append(result, map[string]any{"name": n, "selected": n == a.cfg.Selected, "controller": p.Controller, "username": p.Username, "platform_url": p.PlatformURL})
		}
		return a.emit(result)
	}})
	g.AddCommand(&cobra.Command{Use: "show [NAME]", Short: "Show a trusted profile", Args: rangeArgs(0, 1), RunE: func(_ *cobra.Command, args []string) error {
		n := a.cfg.Selected
		if len(args) > 0 {
			n = args[0]
		}
		p, ok := a.cfg.Profiles[n]
		if !ok {
			return domain.E("PROFILE_NOT_FOUND", n)
		}
		return a.emit(p)
	}})
	g.AddCommand(&cobra.Command{Use: "use NAME", Short: "Select the default profile for future commands", Args: exactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		if _, ok := a.cfg.Profiles[args[0]]; !ok {
			return domain.E("PROFILE_NOT_FOUND", args[0])
		}
		if !a.dryRun {
			if e := config.SetFile(a.cfg.Paths.ConfigFile, "default_profile", args[0]); e != nil {
				return e
			}
		}
		return a.emit(map[string]any{"profile": args[0], "dry_run": a.dryRun})
	}})
	add := &cobra.Command{Use: "add NAME", Short: "Add a trusted platform profile", Args: exactArgs(1)}
	add.Flags().String("platform-url", "https://gpuhub.bupt.edu.cn", "Trusted platform origin")
	add.Flags().String("portal-url", "", "Trusted campus API portal origin (optional)")
	add.Flags().String("gateway-url", "https://webvpn.bupt.edu.cn", "Trusted WebVPN origin")
	add.Flags().String("username", "", "Platform username")
	add.RunE = func(cmd *cobra.Command, args []string) error {
		if strings.ContainsAny(args[0], ". /\\\t\r\n\x00") || args[0] == "" {
			return usage("profile names must not contain dots, whitespace, or path separators")
		}
		if _, ok := a.cfg.Profiles[args[0]]; ok {
			return domain.E("PROFILE_EXISTS", args[0])
		}
		platform, _ := cmd.Flags().GetString("platform-url")
		gateway, _ := cmd.Flags().GetString("gateway-url")
		user, _ := cmd.Flags().GetString("username")
		portal, _ := cmd.Flags().GetString("portal-url")
		p := domain.Profile{PortalURL: portal, Name: args[0], PlatformURL: platform, GatewayURL: gateway, Username: user, Controller: "local", Relay: domain.RelayConfig{PortMin: 24000, PortMax: 24999}}
		if err := config.ValidateProfile(p); err != nil {
			return err
		}
		value := map[string]any{"portal_url": portal, "platform_url": platform, "gateway_url": gateway, "username": user, "controller": "local", "relay": map[string]any{"port_min": 24000, "port_max": 24999}}
		if !a.dryRun {
			if e := config.SetFile(a.cfg.Paths.ConfigFile, "profiles."+args[0], value); e != nil {
				return e
			}
		}
		return a.emit(p)
	}
	g.AddCommand(add)
	return g
}
func (a *app) initCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "init", Short: "Initialize a local controller or explicitly deploy a remote controller", Args: exactArgs(0)}
	f := cmd.Flags()
	f.String("mode", "", "local or remote (default follows selected profile)")
	f.String("relay-address", "", "Relay SSH address")
	f.String("relay-user", "", "Relay account")
	f.String("relay-identity-file", "", "Existing local administrator key for initial relay setup")
	f.String("relay-host-key", "", "Verified relay host public key")
	f.String("instance-address", "", "Relay address reachable from school instances")
	f.String("agent-binary", "", "Matching Linux agent binary for a remote installation")
	f.String("master-key-file", "", "Protected master-key file")
	f.Int("port-min", 24000, "First managed relay loopback port")
	f.Int("port-max", 24999, "Last managed relay loopback port")
	f.Bool("install", true, "Install the selected user/service manager integration")
	f.Bool("start", true, "Start the controller after setup")
	f.Bool("foreground", false, "Run the controller in this terminal")
	f.Bool("relay-install", false, "Provision a remote relay while retaining the local controller")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error { return a.local(cmd, "init", localParams(cmd, a)) }
	return cmd
}
func localParams(cmd *cobra.Command, a *app) map[string]any {
	p := map[string]any{}
	for _, n := range []string{"mode", "relay-address", "relay-user", "relay-identity-file", "relay-host-key", "instance-address", "agent-binary", "master-key-file", "port-min", "port-max", "install", "start", "client", "address", "user", "identity-file", "host-key", "binary", "service-name", "foreground", "public-key-file", "name", "output", "session", "path", "overwrite", "authorized-keys", "executable", "bootstrap-client", "relay-install", "node", "alias"} {
		f := cmd.Flags().Lookup(n)
		if f == nil || !f.Changed {
			continue
		}
		k := strings.ReplaceAll(n, "-", "_")
		switch f.Value.Type() {
		case "bool":
			v, _ := cmd.Flags().GetBool(n)
			p[k] = v
		case "int":
			v, _ := cmd.Flags().GetInt(n)
			p[k] = v
		default:
			p[k] = f.Value.String()
		}
	}
	if a.socket != "" {
		p["socket"] = a.socket
	}
	if a.stateDir != "" {
		p["state_dir"] = a.stateDir
	}
	if a.dataDir != "" {
		p["data_dir"] = a.dataDir
	}
	return p
}
func (a *app) agentCommands() *cobra.Command {
	g := &cobra.Command{Use: "agent", Short: "Run and inspect a local or remote controller"}
	for _, op := range []string{"run", "status", "reload", "gateway", "install", "stop"} {
		op := op
		cmd := &cobra.Command{Use: op, Short: fmt.Sprintf("%s the controller", op), Args: exactArgs(0)}
		cmd.Flags().String("master-key-file", "", "Protected controller master key")
		cmd.Flags().String("client", "", "Registered client identity (forced command only)")
		cmd.Flags().String("address", "", "Administrator SSH address for installation")
		cmd.Flags().String("user", "", "Administrator SSH user")
		cmd.Flags().String("identity-file", "", "Administrator SSH identity file")
		cmd.Flags().String("host-key", "", "Verified SSH host public key")
		cmd.Flags().String("binary", "", "Matching agent binary to install")
		cmd.Flags().String("service-name", "bupt-agent", "Service manager unit name")
		if op == "run" {
			cmd.Flags().String("authorized-keys", "", "Dedicated controller SSH authorized_keys file")
			cmd.Flags().String("executable", "", "Absolute installed bupt executable path for forced commands")
			cmd.Flags().String("bootstrap-client", "", "Trusted initial client public key file")
		}
		if op == "install" {
			cmd.Flags().String("mode", "", "local or remote (default follows selected profile)")
			cmd.Flags().Bool("install", true, "Install the selected user/service manager integration")
			cmd.Flags().Bool("start", true, "Start the controller after setup")
			cmd.Flags().Bool("foreground", false, "Run the controller in this terminal")
			cmd.Flags().Bool("relay-install", false, "Provision a remote relay while retaining the local controller")
		}
		if op == "gateway" {
			cmd.Hidden = true
		}
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			if op == "gateway" && (a.json || a.jsonl || a.ids) {
				return usage("agent gateway requires an unformatted protocol stream")
			}
			return a.local(cmd, "agent."+op, localParams(cmd, a))
		}
		g.AddCommand(cmd)
	}
	return g
}
func (a *app) keyCommands() *cobra.Command {
	g := &cobra.Command{Use: "key", Short: "Manage dedicated client keys without replacing platform-owned keys"}
	g.AddCommand(a.simpleRPC("ls", "List public key metadata", "key.list", 0, 0, nil))
	for _, op := range []string{"generate", "import"} {
		op := op
		cmd := &cobra.Command{Use: op + " NAME", Short: op + " a dedicated local identity", Args: exactArgs(1)}
		cmd.Flags().String("identity-file", "", "Private key file (never a literal key)")
		cmd.Flags().String("public-key-file", "", "Public key file")
		cmd.Flags().String("instance", "", "Install the public key on this existing instance")
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			p := localParams(cmd, a)
			p["name"] = args[0]
			if v, _ := cmd.Flags().GetString("instance"); v != "" {
				p["target"] = v
			}
			return a.local(cmd, "key."+op, p)
		}
		g.AddCommand(cmd)
	}
	revoke := a.simpleRPC("revoke KEY", "Revoke one tool-managed key", "key.revoke", 1, 1, targetParams)
	revoke.Flags().Bool("force", false, "Allow revocation while sessions use the key")
	g.AddCommand(revoke)
	return g
}
func (a *app) sshConfigCommands() *cobra.Command {
	g := &cobra.Command{Use: "ssh-config", Short: "Export or install an optional managed OpenSSH include"}
	for _, op := range []string{"export", "install"} {
		op := op
		cmd := &cobra.Command{Use: op + " [INSTANCE]", Short: op + " OpenSSH compatibility configuration", Args: rangeArgs(0, 1)}
		cmd.Flags().String("file", "", "Output/include path")
		cmd.Flags().String("alias", "", "Short SSH host alias (requires one instance)")
		cmd.Flags().String("node", "", "Node selector for a multi-node instance")
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if output, _ := cmd.Flags().GetString("file"); op == "export" && output == "" {
				if e := a.rawOutput(); e != nil {
					return e
				}
			}
			p := localParams(cmd, a)
			if len(args) > 0 {
				p["target"] = args[0]
			}
			return a.local(cmd, "ssh-config."+op, p)
		}
		g.AddCommand(cmd)
	}
	return g
}
func (a *app) completionCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "completion SHELL", Short: "Generate shell completions", Args: exactArgs(1), Annotations: map[string]string{"skip_config": "true"}, ValidArgs: []string{"bash", "zsh", "fish"}}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			if err := a.root.GenBashCompletionV2(a.options.Out, true); err != nil {
				return err
			}
			_, err := io.WriteString(a.options.Out, bashBuiltinCompletion)
			return err
		case "zsh":
			return a.root.GenZshCompletion(a.options.Out)
		case "fish":
			return a.root.GenFishCompletion(a.options.Out, true)
		default:
			return usage("supported shells: bash, zsh, fish")
		}
	}
	return cmd
}
