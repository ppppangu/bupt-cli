package cli

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

func (a *app) settingsFile(cmd *cobra.Command) (string, error) {
	project, _ := cmd.Flags().GetBool("project")
	if !project {
		return a.cfg.Paths.ConfigFile, nil
	}
	if a.cfg.Paths.ProjectFile != "" {
		return a.cfg.Paths.ProjectFile, nil
	}
	if a.projectFile != "" {
		return a.projectFile, nil
	}
	path, e := filepath.Abs(".bupt.toml")
	return path, e
}
func presetName(s string) bool { return config.ValidProfileName(s) && !strings.Contains(s, ".") }
func (a *app) presetCommands() *cobra.Command {
	group := &cobra.Command{Use: "preset", Short: "Save resource choices and finite runtime defaults"}
	list := &cobra.Command{Use: "ls", Short: "List resource presets", Args: exactArgs(0), RunE: func(_ *cobra.Command, _ []string) error {
		names := []string{}
		for name := range a.cfg.Presets {
			names = append(names, name)
		}
		sort.Strings(names)
		rows := []map[string]any{}
		for _, name := range names {
			p := a.cfg.Presets[name]
			rows = append(rows, map[string]any{"name": name, "spec_id": p.SpecID, "default": name == a.cfg.DefaultPreset, "max_runtime": p.Policy.MaxRuntime, "auto_renew": p.Policy.AutoRenew, "cleanup": p.Policy.Cleanup})
		}
		return a.emit(rows)
	}}
	show := &cobra.Command{Use: "show NAME", Short: "Inspect a resolved preset", Args: exactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		p, e := a.cfg.Preset(args[0])
		if e != nil {
			return e
		}
		return a.emit(p)
	}}
	use := &cobra.Command{Use: "use NAME", Short: "Choose the default preset for future runs; use - to clear", Args: exactArgs(1)}
	use.Flags().Bool("project", false, "Apply in the nearest project, or create .bupt.toml here")
	use.RunE = func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if name == "-" {
			name = ""
		} else {
			if _, e := a.cfg.Preset(name); e != nil {
				return e
			}
		}
		path, e := a.settingsFile(cmd)
		if e != nil {
			return e
		}
		if !a.dryRun {
			if e = config.SetFile(path, "default_preset", name); e != nil {
				return e
			}
		}
		return a.emit(map[string]any{"name": name, "default": true, "file": path, "dry_run": a.dryRun})
	}
	remove := &cobra.Command{Use: "rm NAME", Short: "Remove a preset from one configuration file", Args: exactArgs(1)}
	remove.Flags().Bool("project", false, "Remove from the project configuration")
	remove.RunE = func(cmd *cobra.Command, args []string) error {
		if !presetName(args[0]) {
			return usage("preset name must be a simple identifier without dots")
		}
		if args[0] == a.cfg.DefaultPreset {
			return domain.E("PRESET_IN_USE", "choose another default with bupt compute preset use, or clear it with bupt compute preset use -")
		}
		path, e := a.settingsFile(cmd)
		if e != nil {
			return e
		}
		if !a.dryRun {
			if e = config.UnsetFile(path, "presets."+args[0]); e != nil {
				return e
			}
		}
		return a.emit(map[string]any{"name": args[0], "removed": true, "file": path, "dry_run": a.dryRun})
	}
	set := &cobra.Command{Use: "set NAME", Short: "Save or update a verified template or instance preset", Args: exactArgs(1), Annotations: map[string]string{"ignore_default_preset": "true"}}
	addResourceFlags(set)
	set.Flags().MarkHidden("preset")
	set.Flags().String("from", "", "Copy resource choices from an owned instance")
	set.Flags().Bool("project", false, "Write to the project configuration")
	set.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.Flags().Changed("preset") {
			return usage("use --from INSTANCE or --spec TEMPLATE to define a preset")
		}
		name := args[0]
		if !presetName(name) {
			return usage("preset name must be a simple identifier without dots")
		}
		p, e := a.resourceParams(cmd, false)
		if e != nil {
			return e
		}
		existing, has := a.cfg.Presets[name]
		from, _ := cmd.Flags().GetString("from")
		if from != "" {
			p["target"] = from
		}
		if _, ok := p["spec_id"]; !ok && from == "" && has {
			p["spec_id"] = existing.SpecID
		}
		if has {
			for key, value := range map[string]any{"group_id": existing.GroupID, "bill_id": existing.BillID, "usage": existing.Usage, "domain": existing.Domain} {
				if _, ok := p[key]; !ok {
					p[key] = value
				}
			}
			if _, ok := p["constraints"]; !ok {
				if _, overridden := p["resources"]; !overridden && from == "" {
					p["constraints"] = existing.Constraints
				}
			}
		}
		var resolved domain.Preset
		if e = a.rpc(cmd.Context(), "preset.resolve", p, &resolved); e != nil {
			return e
		}
		policy := a.cfg.Policy
		if has {
			policy = existing.Policy
		}
		if anyPolicyChanged(cmd) {
			patch, e := policyPatch(cmd)
			if e != nil {
				return e
			}
			raw, _ := json.Marshal(patch)
			if e = json.Unmarshal(raw, &policy); e != nil {
				return e
			}
			if cmd.Flags().Changed("max-runtime") {
				policy.Until = domain.RunPolicy{}.Until
			}
			if cmd.Flags().Changed("until") {
				policy.MaxRuntime = ""
			}
		}
		if e = config.ValidatePolicy(policy, false); e != nil {
			return e
		}
		resolved.Policy = policy
		resolved.Name = name
		path, e := a.settingsFile(cmd)
		if e != nil {
			return e
		}
		if !a.dryRun {
			raw, _ := json.Marshal(resolved)
			var value map[string]any
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.UseNumber()
			if e = decoder.Decode(&value); e != nil {
				return e
			}
			if e = config.SetFile(path, "presets."+name, value); e != nil {
				return e
			}
		}
		return a.emit(map[string]any{"name": name, "spec_id": resolved.SpecID, "policy": resolved.Policy, "constraints": resolved.Constraints, "file": path, "dry_run": a.dryRun})
	}
	group.AddCommand(list, show, set, use, remove)
	return group
}
