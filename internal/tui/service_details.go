package tui

import (
	"fmt"
	"slices"
	"strings"

	"bupt.local/cli/internal/domain"
)

func (w *workspace) lifecycleRows(v *content, i domain.Instance) {
	if w.Cached {
		return
	}
	id := string(i.ID)
	action := func(key, title, detail string) {
		args := []string{"compute", "instance", key, id, "--no-wait"}
		v.rows = append(v.rows, link(key, title, detail, cmdline(args...), func() {
			w.review(change{title: title, method: "instance." + key, args: args, params: map[string]any{"target": id}, facts: []string{i.Name + " · " + id, detail}, task: true})
		}))
	}
	if i.State == "running" || i.Reserved || i.Managed {
		v.rows = append(v.rows, link("down", "停止并释放", "归还计算资源，保留实例磁盘", cmdline("down", id, "--cleanup", "release"), func() { w.endReview(i) }))
	}
	if i.State == "running" {
		action("stop", "仅停止运行", "保留磁盘，资源是否仍预留以学校回执为准。")
	}
	if i.State == "stopped" && i.Reserved {
		action("release", "释放预留", "归还计算额度，保留可恢复的实例磁盘。")
	}
	if i.State == "stopped" && !i.Reserved {
		v.rows = append(v.rows, navLink("restore", "恢复预留", "选择有限时长，保留原磁盘", cmdline("compute", "instance", "restore", id), func() {
			p := w.Config.Policy
			if preset, ok := w.Config.Presets[w.Config.DefaultPreset]; ok {
				p = preset.Policy
			}
			w.resourceForm("恢复预留", &draft{instance: &i, restore: true, name: w.instanceName(i), policy: p, limit: p.MaxRuntime})
		}))
	}
	if strings.Contains(i.State, "queu") {
		action("cancel-queue", "取消排队", "撤回当前正在等待的资源申请。")
	}
}
func (w *workspace) nodeSnapshots(i domain.Instance, n domain.Node) {
	id, node := string(i.ID), string(n.ID)
	args := []string{"compute", "snapshot", "ls", id, "--node", node}
	w.local("节点快照", cmdline(args...), func() content {
		current, nodeData, found := w.node(i.ID, n.ID)
		if !found {
			return content{empty: "当前实例中已找不到此节点。"}
		}
		v := content{empty: "此节点没有已记录的快照。"}
		for _, snap := range nodeData.Snapshots {
			sid := snapshotID(snap)
			if sid == "" {
				continue
			}
			name := value(snap["name"])
			v.rows = append(v.rows, navLink(sid, name, value(snap["state"]), cmdline(args...), func() {
				w.snapshotPage(i.ID, n.ID, sid, name)
			}))
		}
		if !w.Cached && canChangeSnapshots(current, nodeData) {
			v.rows = append(v.rows, link("new", "创建节点快照", "", "bupt compute snapshot create", func() {
				w.edit("快照名称", "", func(name string) error {
					if strings.TrimSpace(name) == "" {
						return fmt.Errorf("请输入快照名称")
					}
					a := []string{"compute", "snapshot", "create", "--node", node, "--no-wait", "--", id, name}
					w.review(change{title: "创建快照", method: "snapshot.create", args: a, params: map[string]any{"target": id, "node": node, "name": name}, facts: []string{name, "实例 " + id + " · 节点 " + node}, task: true})
					return nil
				})
			}))
		}
		return v
	})
}

func snapshotID(record map[string]any) string {
	if id := value(record["eid"]); id != "" {
		return id
	}
	return value(record["id"])
}

func (w *workspace) snapshotPage(instanceID, nodeID domain.ID, sid, name string) {
	id, node := string(instanceID), string(nodeID)
	w.local(name, cmdline("compute", "snapshot", "ls", id, "--node", node), func() content {
		i, n, ok := w.node(instanceID, nodeID)
		if !ok {
			return content{empty: "当前实例中已找不到此节点。"}
		}
		index := slices.IndexFunc(n.Snapshots, func(s map[string]any) bool { return snapshotID(s) == sid })
		if index < 0 {
			return content{empty: "此快照已移除。"}
		}
		name := value(n.Snapshots[index]["name"])
		v := content{facts: []string{name, fmt.Sprintf("实例 %s · 节点 %s · 快照 %s", id, node, sid)}}
		if w.Cached || name == "" || !canChangeSnapshots(i, n) {
			return v
		}
		for _, a := range []struct{ op, title string }{{"restore", "从快照恢复"}, {"delete", "删除快照"}} {
			args := []string{"compute", "snapshot", a.op, "--node", node, "--no-wait", "--", id, sid}
			v.rows = append(v.rows, link(a.op, a.title, name, cmdline(args...), func() {
				w.review(change{title: a.title, method: "snapshot." + a.op, args: args, params: map[string]any{"target": id, "node": node, "snapshot_id": sid}, facts: []string{name + " · " + sid}, deleteName: name, task: true})
			}))
		}
		return v
	})
}

func canChangeSnapshots(i domain.Instance, n domain.Node) bool {
	return n.Type == "qemu" && (i.State == "stopped" || i.State == "approved")
}
