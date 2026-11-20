package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"bupt.local/cli/internal/client"
	"bupt.local/cli/internal/domain"
)

type formOperation struct {
	method, section string
	usage, fields   string
	query, terminal bool
}

// Each form belongs to the workflow that exposes it and binds its own object
// IDs. CLI options and Cobra metadata have no role in selecting its fields.
var formOperations = map[string]formOperation{
	"compute features":           {method: "compute.read", section: "capabilities", query: true, fields: "cached:bool"},
	"compute instance events":    {method: "instance.events", usage: "INSTANCE", fields: "limit:int", query: true},
	"compute instance wait":      {method: "instance.wait", usage: "INSTANCE", fields: "state node", terminal: true},
	"compute instance adopt":     {method: "instance.adopt", usage: "INSTANCE", fields: "name max-runtime until cleanup stop-grace renew:bool force-stop:bool"},
	"compute instance clone":     {method: "instance.clone", usage: "INSTANCE NAME", fields: "max-runtime until cleanup cpu gpu memory disk node"},
	"compute instance resize":    {method: "instance.resize", usage: "INSTANCE", fields: "node cpu gpu memory disk size-gb:int"},
	"compute service add":        {method: "service.add", usage: "INSTANCE NAME", fields: "node bind host port:int"},
	"compute service delete":     {method: "service.delete", usage: "INSTANCE SERVICE", fields: "node"},
	"compute storage mkdir":      {method: "data.edit", section: "mkdir", usage: "PATH", fields: "store group-id"},
	"compute storage rename":     {method: "data.edit", section: "rename", usage: "PATH NEW_NAME", fields: "store group-id"},
	"compute storage rm":         {method: "data.edit", section: "remove", usage: "PATH", fields: "store group-id confirm"},
	"compute storage push":       {method: "data.upload", usage: "LOCAL REMOTE", fields: "store group-id overwrite:bool resume:bool", terminal: true},
	"compute catalog estimate":   {method: "spec.estimate", usage: "", fields: "spec-id cpu gpu memory disk", query: true},
	"compute preset set":         {method: "preset.set", usage: "NAME", fields: "from spec spec-id max-runtime until cpu gpu memory disk cleanup force-stop:bool renew:bool project:bool", terminal: false},
	"compute preset use":         {method: "preset.use", usage: "NAME", fields: "project:bool"},
	"compute preset rm":          {method: "preset.rm", usage: "NAME", fields: "project:bool"},
	"compute job attach":         {method: "job.attach", usage: "JOB", terminal: true},
	"compute job wait":           {method: "job.wait", usage: "JOB", terminal: true},
	"compute job update":         {method: "job.update", usage: "JOB", fields: "max-runtime until cleanup force-stop:bool renew:bool stop-now:bool stop-grace"},
	"compute lease wait":         {method: "run.wait", usage: "RUN", fields: "for", terminal: true},
	"compute task wait":          {method: "task.wait", usage: "OPERATION", terminal: true},
	"compute quota request":      {method: "compute.quota.request", usage: "QUOTA_ID", fields: "gpu cpu memory instances disk file-storage reason wait:bool"},
	"compute scheduler job logs": {method: "compute.read", section: "job.log", usage: "JOB", fields: "cluster stream", query: true},
	"cluster partition":          {method: "compute.read", section: "partition", usage: "CLUSTER", query: true},
	"remote order create":        {method: "compute.remote.checkout", section: "create", usage: "NAME", fields: "spec units:int quote"},
	"remote order renew":         {method: "compute.remote.checkout", section: "renew", usage: "INSTANCE", fields: "units:int quote"},
	"remote order cancel":        {method: "compute.remote.order", section: "cancel", usage: "ORDER", fields: "confirm"},
	"remote order payment-code":  {method: "compute.remote.order", section: "payment-code", usage: "ORDER", query: true},
	"admin quota show":           {method: "compute.read", section: "admin.limits", usage: "CLUSTER", query: true},
	"admin quota set":            {method: "compute.admin.update", usage: "CLUSTER ACCOUNT", fields: "quota-field value revision"},
	"admin approve":              {method: "compute.admin.approve", usage: "REQUEST", fields: "confirm reason wait:bool"},
	"admin reject":               {method: "compute.admin.reject", usage: "REQUEST", fields: "confirm reason wait:bool"},
	"ssh":                        {method: "ssh", usage: "INSTANCE COMMAND ARG...", fields: "node session tty:bool no-tty:bool reconnect:bool", terminal: true},
	"exec":                       {method: "exec", usage: "INSTANCE COMMAND ARG...", fields: "node", terminal: true},
	"console":                    {method: "console", usage: "INSTANCE", fields: "node", terminal: true},
	"forward":                    {method: "forward", usage: "INSTANCE PORT", fields: "node local remote-host", terminal: true},
	"sftp":                       {method: "sftp", usage: "INSTANCE", fields: "node", terminal: true},
}

type formRequest struct {
	method  string
	params  map[string]any
	command []string
	disk    *domain.GrowDiskRequest
}

func (f formOperation) form(path []string) CommandForm {
	form := CommandForm{Path: path, Usage: "action " + f.usage, Query: f.query, Terminal: f.terminal}
	for _, field := range strings.Fields(f.fields) {
		name, kind, _ := strings.Cut(field, ":")
		form.Flags = append(form.Flags, FormFlag{Name: name, Type: kind})
	}
	return form
}

func formParams(f CommandForm, args []string, values map[string]string) (formRequest, error) {
	op, ok := formOperations[strings.Join(f.Path, " ")]
	if !ok {
		return formRequest{}, domain.E("UNSUPPORTED", "此工作流未提供该操作")
	}
	fields := strings.Fields(op.usage)
	required := len(fields)
	if op.method == "ssh" {
		required = 1
	}
	if op.method == "exec" {
		required = 2
	}
	if len(args) < required {
		return formRequest{}, fmt.Errorf("请填写 %s", strings.Join(fields[len(args):required], "、"))
	}
	if op.method == "instance.resize" && values["size-gb"] != "" {
		size, err := strconv.ParseInt(values["size-gb"], 10, 64)
		if err != nil || size <= 0 || values["cpu"] != "" || values["gpu"] != "" || values["memory"] != "" || values["disk"] != "" {
			return formRequest{}, fmt.Errorf("请单独填写有效的磁盘容量")
		}
		noWait := false
		return formRequest{method: "instance.disk-grow", disk: &domain.GrowDiskRequest{
			InstanceTaskRequest: domain.InstanceTaskRequest{OperationOptions: domain.OperationOptions{Wait: &noWait}, Target: args[0], Node: domain.ID(values["node"])},
			TargetCapacityGB:    size,
		}}, nil
	}
	p := map[string]any{"wait": false}
	if len(args) > 0 {
		p["target"] = args[0]
	}
	for _, flag := range f.Flags {
		raw, ok := values[flag.Name]
		if !ok {
			continue
		}
		key := strings.ReplaceAll(flag.Name, "-", "_")
		switch flag.Type {
		case "int":
			n, err := strconv.Atoi(raw)
			if err != nil {
				return formRequest{}, fmt.Errorf("%s 需要整数", flag.Name)
			}
			p[key] = n
		case "bool":
			p[key] = raw == "true"
		default:
			p[key] = raw
		}
	}
	switch op.method {
	case "compute.read":
		p["section"] = op.section
	case "data.edit":
		p["action"], p["path"] = op.section, args[0]
		if len(args) > 1 {
			p["new_name"] = args[1]
		}
	case "compute.remote.checkout":
		p["action"] = op.section
		if op.section == "create" {
			p["name"], p["target"] = args[0], p["spec"]
		}
	case "compute.remote.order":
		p["action"] = op.section
	case "compute.admin.update":
		p["cluster"], p["account"], p["field"] = args[0], args[1], p["quota_field"]
	case "compute.quota.request":
		for flag, field := range map[string]string{"instances": "appCount", "disk": "blockStorage", "file_storage": "fileStorage"} {
			if val, exists := p[flag]; exists {
				p[field] = val
			}
		}
	case "instance.clone":
		p["name"] = args[1]
	case "service.add":
		p["name"] = args[1]
		if p["bind"] == nil {
			p["bind"] = "127.0.0.1"
		}
	case "service.delete":
		p["service_id"] = args[1]
	case "instance.wait", "run.wait":
		if p["for"] != nil {
			p["condition"] = p["for"]
		}
	}
	if op.method == "instance.resize" || op.method == "instance.clone" || op.method == "spec.estimate" || op.method == "preset.set" {
		resources := map[string]any{}
		for _, key := range []string{"cpu", "gpu", "memory", "disk"} {
			if v, ok := p[key]; ok {
				resources[key] = v
			}
		}
		if len(resources) > 0 {
			p["resources"] = resources
		}
	}
	if op.method == "instance.clone" || op.method == "instance.adopt" || op.method == "job.update" {
		policy := map[string]any{}
		for _, key := range []string{"max_runtime", "until", "cleanup", "stop_grace", "force_stop"} {
			if value, exists := p[key]; exists {
				policy[key] = value
			}
		}
		if value, exists := p["renew"]; exists {
			policy["auto_renew"] = value
		}
		if len(policy) > 0 {
			p["policy"] = policy
		}
	}
	return formRequest{method: op.method, params: p}, nil
}

func (w *workspace) callForm(ctx context.Context, r formRequest, preview bool, key string) (json.RawMessage, error) {
	if r.disk != nil {
		request := *r.disk
		request.DryRun, request.IdempotencyKey = preview, key
		var reply json.RawMessage
		err := w.Backend.Call(ctx, r.method, request, &reply)
		return reply, err
	}
	params := maps.Clone(r.params)
	if key != "" {
		params["idempotency_key"] = key
	}
	if strings.HasPrefix(r.method, "preset.") {
		if w.PresetAction == nil {
			return nil, domain.E("UNSUPPORTED", "此终端无法更改预设")
		}
		return w.PresetAction(ctx, r.method, params, preview)
	}
	var reply json.RawMessage
	err := client.CallOperation(ctx, w.Backend, r.method, params, preview, &reply)
	return reply, err
}

func formText(raw json.RawMessage) string {
	var view any
	if data, err := readComputeJSON(raw); err == nil {
		view = data
	}
	if view == nil {
		return string(raw)
	}
	b, err := json.MarshalIndent(view, "", "  ")
	if err != nil {
		return string(raw)
	}
	const limit = 128 << 10
	if len(b) > limit {
		return string(b[:limit]) + "\n… 查看等效 CLI 命令以取得完整结果"
	}
	return string(b)
}
