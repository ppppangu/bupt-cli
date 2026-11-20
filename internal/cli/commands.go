package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

func addListFlags(cmd *cobra.Command) {
	cmd.Flags().String("state", "", "Filter by state")
	cmd.Flags().StringSlice("filter", nil, "Filter expression (repeatable)")
	cmd.Flags().StringSlice("columns", nil, "Select output fields")
}
func addTaskFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("wait", false, "等待操作完成")
}
func addResourceFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("preset", "", "Named resource preset")
	f.String("spec-id", "", "Application template ID")
	f.String("spec", "", "Template ID or exact NAME[@VERSION]")
	f.String("constraints", "", "Exact platform resource constraints as a JSON object")
	f.String("cpu", "", "CPU allocation")
	f.String("memory", "", "Memory allocation, e.g. 6GiB")
	f.String("gpu", "", "GPU allocation supported by the template")
	f.String("disk", "", "Disk allocation, e.g. 50GiB")
	f.String("group-id", "", "Project/group ID")
	f.String("bill-id", "", "Billing account ID")
	f.String("usage", "", "Resource usage description")
	f.String("email", "", "Account email if required by the platform")
	f.String("domain", "", "Platform resource domain")
	addPolicyFlags(cmd)
}
func addPolicyFlags(cmd *cobra.Command) {
	f := cmd.Flags()
	f.String("max-runtime", "", "Finite maximum duration, including preparation")
	f.String("until", "", "Absolute deadline in RFC3339 format")
	f.Bool("renew", false, "Automatically request renewal within the explicit limit")
	f.Bool("no-renew", false, "Disable future renewal requests")
	f.String("cleanup", "", "none, stop, release, or delete")
	f.String("stop-grace", "", "Grace period before forced stop")
	f.Bool("force-stop", false, "Allow forced stop after the grace period")
}
func (a *app) policy(cmd *cobra.Command, preset string, required bool) (domain.RunPolicy, error) {
	p := a.cfg.Policy
	if preset != "" {
		pr, e := a.cfg.Preset(preset)
		if e != nil {
			return p, e
		}
		p = pr.Policy
	}
	p.ForceStop = false
	f := cmd.Flags()
	changed := func(n string) bool { v := f.Lookup(n); return v != nil && v.Changed }
	if changed("max-runtime") {
		p.MaxRuntime, _ = f.GetString("max-runtime")
		p.Until = time.Time{}
	}
	if changed("until") {
		s, _ := f.GetString("until")
		v, e := time.Parse(time.RFC3339, s)
		if e != nil {
			return p, usage("--until must be RFC3339 with a timezone")
		}
		p.Until = v
		p.MaxRuntime = ""
	}
	if changed("until") && changed("max-runtime") {
		return p, usage("choose --until or --max-runtime")
	}
	if changed("renew") {
		p.AutoRenew, _ = f.GetBool("renew")
	}
	if changed("no-renew") {
		disabled, _ := f.GetBool("no-renew")
		if disabled {
			p.AutoRenew = false
		}
	}
	if changed("renew") && changed("no-renew") {
		return p, usage("choose --renew or --no-renew")
	}
	if changed("cleanup") {
		p.Cleanup, _ = f.GetString("cleanup")
	}
	if changed("stop-grace") {
		p.StopGrace, _ = f.GetString("stop-grace")
	}
	if changed("force-stop") {
		p.ForceStop, _ = f.GetBool("force-stop")
	}
	if e := config.ValidatePolicy(p, required); e != nil {
		return p, e
	}
	if required && !p.Until.IsZero() && !p.Until.After(time.Now()) {
		return p, usage("--until must be in the future when starting a managed run")
	}
	return p, nil
}
func (a *app) resourceParams(cmd *cobra.Command, required bool) (map[string]any, error) {
	p := map[string]any{}
	preset, _ := cmd.Flags().GetString("preset")
	if preset == "" && cmd.Flags().Lookup("preset") != nil && !cmd.Flags().Changed("preset") && !cmd.Flags().Changed("spec") && !cmd.Flags().Changed("spec-id") && cmd.Annotations["ignore_default_preset"] != "true" {
		preset = a.cfg.DefaultPreset
	}
	if cmd.Flags().Changed("spec") && cmd.Flags().Changed("spec-id") {
		return nil, usage("choose --spec or --spec-id")
	}
	if preset != "" {
		p["preset"] = preset
		resolved, e := a.cfg.Preset(preset)
		if e != nil {
			return nil, e
		}
		p["preset_snapshot"] = resolved
	}
	copyChangedFlags(cmd, p)
	if cmd.Flags().Changed("spec") {
		p["spec_id"], _ = cmd.Flags().GetString("spec")
	}
	resources := map[string]any{}
	for _, key := range []string{"cpu", "memory", "gpu", "disk"} {
		if v, ok := p[key]; ok {
			resources[key] = v
		}
	}
	if len(resources) > 0 {
		p["resources"] = resources
	}
	if f := cmd.Flags().Lookup("constraints"); f != nil && f.Changed {
		raw, _ := cmd.Flags().GetString("constraints")
		var constraints map[string]any
		d := json.NewDecoder(strings.NewReader(raw))
		d.UseNumber()
		if e := d.Decode(&constraints); e != nil || constraints == nil || !json.Valid([]byte(raw)) {
			return nil, usage("--constraints must be a JSON object")
		}
		p["constraints"] = constraints
	}
	policy, e := a.policy(cmd, preset, required)
	if e != nil {
		return nil, e
	}
	if required || policy.MaxRuntime != "" || !policy.Until.IsZero() || anyPolicyChanged(cmd) {
		p["policy"] = policy
	}
	sources := map[string]string{}
	for key, value := range a.cfg.Sources {
		sources[key] = value
	}
	for _, name := range []string{"max-runtime", "until", "renew", "no-renew", "cleanup", "stop-grace", "force-stop"} {
		if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
			key := strings.ReplaceAll(name, "-", "_")
			if name == "renew" || name == "no-renew" {
				key = "auto_renew"
			}
			sources["policy."+key] = "flag:--" + name
		}
	}
	p["config_sources"] = sources
	if cmd.Flags().Lookup("wait") != nil {
		p["wait"] = waitSelected(cmd)
	}
	return p, nil
}
func anyPolicyChanged(c *cobra.Command) bool {
	for _, n := range []string{"max-runtime", "until", "renew", "no-renew", "cleanup", "stop-grace", "force-stop"} {
		if f := c.Flags().Lookup(n); f != nil && f.Changed {
			return true
		}
	}
	return false
}

func policyPatch(cmd *cobra.Command) (map[string]any, error) {
	if !anyPolicyChanged(cmd) {
		return nil, usage("provide a policy change such as --max-runtime, --until, --no-renew, or --cleanup")
	}
	if cmd.Flags().Changed("max-runtime") && cmd.Flags().Changed("until") {
		return nil, usage("choose --until or --max-runtime")
	}
	if cmd.Flags().Changed("renew") && cmd.Flags().Changed("no-renew") {
		return nil, usage("choose --renew or --no-renew")
	}
	patch := map[string]any{}
	for _, n := range []string{"max-runtime", "until", "renew", "no-renew", "cleanup", "stop-grace", "force-stop"} {
		if !cmd.Flags().Changed(n) {
			continue
		}
		key := strings.ReplaceAll(n, "-", "_")
		switch n {
		case "renew":
			v, _ := cmd.Flags().GetBool(n)
			patch["auto_renew"] = v
		case "no-renew":
			v, _ := cmd.Flags().GetBool(n)
			if v {
				patch["auto_renew"] = false
			}
		case "force-stop":
			v, _ := cmd.Flags().GetBool(n)
			patch[key] = v
		case "until":
			v, _ := cmd.Flags().GetString(n)
			if _, e := time.Parse(time.RFC3339, v); e != nil {
				return nil, usage("--until must be RFC3339 with a timezone")
			}
			patch[key] = v
			patch["max_runtime"] = ""
		default:
			v, _ := cmd.Flags().GetString(n)
			if n == "max-runtime" {
				if d, e := time.ParseDuration(v); e != nil || d <= 0 {
					return nil, usage("--max-runtime must be a positive duration")
				}
				patch["until"] = nil
			}
			if n == "stop-grace" {
				if d, e := time.ParseDuration(v); e != nil || d < 0 {
					return nil, usage("--stop-grace must be a nonnegative duration")
				}
			}
			if n == "cleanup" && v != "none" && v != "stop" && v != "release" && v != "delete" {
				return nil, usage("--cleanup must be none, stop, release, or delete")
			}
			patch[key] = v
		}
	}
	if len(patch) == 0 {
		return nil, usage("provide an effective policy change")
	}
	return patch, nil
}

func (a *app) estimateCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "estimate", Short: "Estimate platform charges without allocating resources", Args: exactArgs(0)}
	addResourceFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		p, e := a.resourceParams(cmd, false)
		if e != nil {
			return e
		}
		return a.callAndPrint(cmd, "spec.estimate", p)
	}
	return cmd
}
func (a *app) instanceCommands() *cobra.Command {
	g := &cobra.Command{Use: "instance", Short: "Manage instance lifecycle primitives"}
	list := a.simpleRPC("ls", "List instances", "instance.list", 0, 0, nil)

	addListFlags(list)
	g.AddCommand(list)
	g.AddCommand(a.simpleRPC("inspect INSTANCE", "Inspect an instance", "instance.inspect", 1, 1, targetParams))
	g.AddCommand(a.createInstanceCommand())
	adopt := &cobra.Command{Use: "adopt INSTANCE", Short: "Explicitly put an existing instance under lifecycle management", Args: exactArgs(1)}
	adopt.Flags().String("name", "", "Logical name")
	addPolicyFlags(adopt)
	adopt.RunE = func(cmd *cobra.Command, args []string) error {
		p, e := a.resourceParams(cmd, true)
		if e != nil {
			return e
		}
		p["target"] = args[0]
		return a.callAndPrint(cmd, "instance.adopt", p)
	}
	g.AddCommand(adopt)
	for op, label := range map[string]string{
		"start": "启动已有停机实例", "restart": "温和重启当前实例", "stop": "停机并保留预留和磁盘",
		"release": "归还已停机实例的算力，磁盘按回收策略保留", "restore": "为已有实例重新分配算力，随后可启动并连接 SSH",
		"delete": "永久删除已释放的实例和本机磁盘", "cancel-queue": "取消排队申请",
	} {
		cmd := &cobra.Command{Use: op + " INSTANCE", Short: label, Args: exactArgs(1)}
		addTaskFlags(cmd)
		switch op {
		case "start", "restore", "restart":
			addPolicyFlags(cmd)
			cmd.Flags().String("node", "", "选择 SSH 节点")
			if op == "restore" {
				cmd.Flags().String("max-points-per-day", "", "授权分配算力的积分日价上限")
			}
		case "delete":
			cmd.Flags().String("confirm", "", "核对后输入实例 ID")
		case "stop":
			cmd.Flags().Bool("force", false, "立即强制停机")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			p := targetParams(args)
			copyChangedFlags(cmd, p)
			switch op {
			case "start", "restart", "restore":
				pol, err := a.policy(cmd, "", op == "restore")
				if err != nil {
					return err
				}
				p["policy"], p["config_sources"] = pol, a.cfg.Sources
				if op == "restore" {
					if err := a.authorizeRestore(cmd, p); err != nil {
						return err
					}
				}
			case "delete":
				p["confirm"], _ = cmd.Flags().GetString("confirm")
				if !a.dryRun && p["confirm"] == "" {
					return usage("需要 --confirm INSTANCE_ID")
				}
			}
			return a.submitInstance(cmd, "instance."+op, p)
		}
		g.AddCommand(cmd)
	}
	wait := &cobra.Command{Use: "wait [INSTANCE]", Short: "等待实例 SSH 就绪或跟踪指定回执", Args: rangeArgs(0, 1)}
	wait.Flags().String("state", "ssh-ready", "等待指定平台状态")
	completeValues(wait, "state", "ssh-ready", "pending_approval", "approved", "queuing", "reserve_queuing", "starting", "running", "stopping", "stopped", "released")
	wait.Flags().String("node", "", "选择 SSH 节点")
	wait.Flags().String("operation", "", "等待指定操作 ID")
	wait.RunE = a.waitInstance
	g.AddCommand(wait)
	events := a.simpleRPC("events INSTANCE", "查看学校可见回执与控制器记录", "instance.events", 1, 1, targetParams)
	events.Flags().Int("limit", 100, "Maximum event count")
	events.Flags().Bool("cached", false, "仅查看缓存的学校回执和本地记录")
	g.AddCommand(events)
	return g
}

func (a *app) leaseCommands() *cobra.Command {
	g := &cobra.Command{Use: "lease", Short: "Inspect or update finite runtime and renewal policy"}
	g.AddCommand(a.simpleRPC("status INSTANCE", "Show requested, approved, and maximum expiry", "lease.status", 1, 1, targetParams))
	set := &cobra.Command{Use: "set INSTANCE", Short: "Update the current run policy without resetting elapsed time", Args: exactArgs(1)}
	addPolicyFlags(set)
	set.Flags().Bool("stop-now", false, "Explicitly permit immediate cleanup when the new cap has elapsed")
	set.RunE = func(cmd *cobra.Command, args []string) error {
		patch, e := policyPatch(cmd)
		if e != nil {
			return e
		}
		p := targetParams(args)
		p["policy"] = patch
		stop, _ := cmd.Flags().GetBool("stop-now")
		p["stop_now"] = stop
		return a.callAndPrint(cmd, "lease.set", p)
	}
	renew := a.simpleRPC("renew INSTANCE", "Submit and track a normal-user renewal request", "lease.renew", 1, 1, targetParams)
	renew.Flags().String("duration", "", "Requested additional duration")
	renew.Flags().String("reason", "", "Reason sent to the platform approver")
	end := &cobra.Command{Use: "end INSTANCE", Short: "按指定策略结束托管周期", Args: exactArgs(1)}
	end.Flags().String("cleanup", "", "none、stop、release 或 delete")
	addTaskFlags(end)
	end.RunE = func(cmd *cobra.Command, args []string) error {
		mode, _ := cmd.Flags().GetString("cleanup")
		switch mode {
		case "none", "stop", "release", "delete":
		default:
			return usage("需要指定 --cleanup none、stop、release 或 delete")
		}
		result, err := a.managedCall(cmd, "down", map[string]any{"target": args[0], "cleanup": mode, "wait": cmd.Flags().Changed("wait") && waitSelected(cmd)}, "finished")
		if err != nil {
			return err
		}
		return a.emitRun(result)
	}
	wait := a.waitCommand()
	wait.Short = "等待托管周期就绪或结束"
	list := a.simpleRPC("ls", "列出托管周期", "run.list", 0, 0, nil)
	addListFlags(list)
	g.AddCommand(list, set, renew, end, wait)
	return g
}
func (a *app) resizeCommand() *cobra.Command {
	cmd := a.simpleRPC("resize INSTANCE", "Change template-permitted resources after stop and release", "instance.resize", 1, 1, targetParams)
	cmd.Flags().String("node", "", "Node selector")
	cmd.Flags().String("cpu", "", "CPU allocation")
	cmd.Flags().String("gpu", "", "GPU constraint")
	cmd.Flags().String("memory", "", "Memory size")
	cmd.Flags().String("disk", "", "Block storage constraint")
	cmd.Flags().Int64("size-gb", 0, "Grow a QEMU disk to this total GB capacity")
	addTaskFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		p := targetParams(args)
		copyChangedFlags(cmd, p)
		resources := map[string]any{}
		for _, key := range []string{"cpu", "gpu", "memory", "disk"} {
			if v, ok := p[key]; ok {
				delete(p, key)
				if key == "disk" {
					key = "accelDisk"
				}
				resources[key] = v
			}
		}
		if cmd.Flags().Changed("size-gb") {
			if len(resources) > 0 {
				return usage("--size-gb cannot be combined with resource constraints")
			}
			v, _ := cmd.Flags().GetInt64("size-gb")
			if v <= 0 {
				return usage("--size-gb must be positive")
			}
			node, _ := cmd.Flags().GetString("node")
			settings := domain.OperationOptions{DryRun: a.dryRun, IdempotencyKey: a.idempotencyKey}
			if cmd.Flags().Changed("wait") {
				wait := waitSelected(cmd)
				settings.Wait = &wait
			}
			request := domain.GrowDiskRequest{
				InstanceTaskRequest: domain.InstanceTaskRequest{Target: args[0], Node: domain.ID(node), OperationOptions: settings},
				TargetCapacityGB:    v,
			}
			var result json.RawMessage
			if err := a.rpcRequest(cmd.Context(), "instance.disk-grow", request, &result); err != nil {
				return err
			}
			return a.emit(result)
		}
		if len(resources) == 0 {
			return usage("provide a resource change or --size-gb")
		}
		p["resources"] = resources
		return a.callAndPrint(cmd, "instance.resize", p)
	}
	return cmd
}
func (a *app) cloneCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "clone SOURCE NAME", Short: "Create a separately managed clone when supported", Args: exactArgs(2)}
	addResourceFlags(cmd)
	addTaskFlags(cmd)
	rate := cmd.Flags().String("max-points-per-day", "", "克隆实例时授权的积分日价上限")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if !a.dryRun && *rate == "" {
			return usage("请先用 --dry-run 估价，再指定 --max-points-per-day RATE")
		}
		p, e := a.resourceParams(cmd, true)
		if e != nil {
			return e
		}
		p["target"] = args[0]
		p["name"] = args[1]
		if *rate != "" {
			p["max_points_per_day"] = *rate
		}
		return a.callAndPrint(cmd, "instance.clone", p)
	}
	return cmd
}
func (a *app) snapshotCommands() *cobra.Command {
	g := &cobra.Command{Use: "snapshot", Short: "Manage snapshots only where the platform supports them"}
	for _, op := range []string{"ls", "create", "restore", "delete"} {
		op := op
		use := op + " INSTANCE"
		min, max := 1, 1
		if op == "create" {
			use += " NAME"
			min, max = 2, 2
		}
		if op == "restore" || op == "delete" {
			use += " SNAPSHOT"
			min, max = 2, 2
		}
		method := op
		if op == "ls" {
			method = "list"
		}
		cmd := a.simpleRPC(use, fmt.Sprintf("%s instance snapshots", op), "snapshot."+method, min, max, func(args []string) map[string]any {
			p := targetParams(args)
			if len(args) > 1 {
				if op == "create" {
					p["name"] = args[1]
				} else {
					p["snapshot_id"] = args[1]
				}
			}
			return p
		})
		cmd.Flags().String("node", "", "Node selector")
		if op == "create" {
			cmd.Flags().String("description", "", "Snapshot description")
			cmd.RunE = func(c *cobra.Command, args []string) error {
				p := targetParams(args)
				p["name"] = args[1]
				v, _ := c.Flags().GetString("description")
				p["description"] = v
				copyChangedFlags(c, p)
				return a.callAndPrint(c, "snapshot.create", p)
			}
		}
		if op != "ls" {
			addTaskFlags(cmd)
		}
		g.AddCommand(cmd)
	}
	return g
}
func (a *app) serviceCommands() *cobra.Command {
	g := &cobra.Command{Use: "service", Short: "Inspect and manage non-builtin service mappings"}
	g.AddCommand(a.simpleRPC("ls INSTANCE", "List instance service mappings", "service.list", 1, 1, targetParams))
	add := a.simpleRPC("add INSTANCE NAME", "Create a platform TCP mapping; changes may need a restart", "service.add", 2, 2, func(args []string) map[string]any { return map[string]any{"target": args[0], "name": args[1]} })
	add.Flags().String("node", "", "Node selector")
	add.Flags().String("bind", "127.0.0.1", "Address inside the instance")
	add.Flags().Int("port", 0, "Port inside the instance")
	addTaskFlags(add)
	add.RunE = func(cmd *cobra.Command, args []string) error {
		port, _ := cmd.Flags().GetInt("port")
		if port < 1 || port > 65535 {
			return usage("--port must be between 1 and 65535")
		}
		host, _ := cmd.Flags().GetString("bind")
		p := map[string]any{"target": args[0], "name": args[1], "bind": host, "port": port}
		copyChangedFlags(cmd, p)
		return a.callAndPrint(cmd, "service.add", p)
	}
	del := a.simpleRPC("delete INSTANCE SERVICE", "Delete a non-builtin service mapping", "service.delete", 2, 2, func(args []string) map[string]any { return map[string]any{"target": args[0], "service_id": args[1]} })
	del.Flags().String("node", "", "Node selector")
	addTaskFlags(del)
	g.AddCommand(add, del)
	return g
}
func (a *app) taskCommands() *cobra.Command {
	g := &cobra.Command{Use: "task", Short: "Inspect durable platform operation receipts"}
	list := a.simpleRPC("ls", "List recorded operations", "task.list", 0, 0, nil)

	g.AddCommand(list, a.simpleRPC("show TASK", "Show an operation receipt", "task.get", 1, 1, targetParams), a.simpleRPC("wait TASK", "Wait for a recorded operation", "task.wait", 1, 1, targetParams))
	return g
}
func (a *app) auditCommands() *cobra.Command {
	g := &cobra.Command{Use: "audit", Short: "Read sanitized controller audit events"}
	list := a.simpleRPC("ls", "List audit events", "audit.list", 0, 0, nil)
	list.Flags().Int("limit", 100, "Maximum events")
	list.Flags().String("after", "", "Only events after this RFC3339 time")
	g.AddCommand(list)
	return g
}
func (a *app) doctorCommand() *cobra.Command {
	cmd := a.simpleRPC("doctor [AREA]", "Diagnose configuration, authentication, network, and SSH independently", "doctor", 0, 1, func(args []string) map[string]any {
		if len(args) > 0 {
			return map[string]any{"area": args[0]}
		}
		return nil
	})
	cmd.Flags().Bool("offline", false, "Check local configuration without contacting the controller")
	cmd.Flags().Bool("report", false, "生成可附加到反馈邮件的脱敏诊断")
	cmd.Annotations = map[string]string{"skip_config": "true"}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		offline, _ := cmd.Flags().GetBool("offline")
		report, _ := cmd.Flags().GetBool("report")
		if report {
			if len(args) != 0 {
				return usage("bupt doctor --report 可加 --offline")
			}
			r := a.supportReport(cmd, offline)
			if a.json || a.jsonl {
				return a.emit(r)
			}
			_, err := fmt.Fprint(a.options.Out, r.Text())
			return err
		}
		if err := a.loadConfig(cmd); err != nil {
			return err
		}
		if offline {
			p, e := a.cfg.SelectedProfile()
			if e != nil {
				return e
			}
			return a.emit(map[string]any{"profile": p.Name, "controller": p.Controller, "configuration": "valid", "note": "School authentication is not required to use an existing SSH route."})
		}
		p := map[string]any{}
		if len(args) > 0 {
			p["area"] = args[0]
		}
		return a.callAndPrint(cmd, "doctor", p)
	}
	return cmd
}
