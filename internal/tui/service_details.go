package tui

import (
	"fmt"
	"strings"

	"bupt.local/cli/internal/domain"
)

func (w *workspace) portalNews(v *content, page domain.PortalPage) {
	items, _ := page.Data["items"].([]any)
	for _, obj := range items {
		article := object(obj)
		id := value(article["id"])
		if id == "" {
			continue
		}
		title := value(article["title"])
		status := "未读"
		if article["read"] == true {
			status = "已读"
		}
		cmd := cmdline("news", "show", id)
		v.rows = append(v.rows, link(id, title, status+" · "+value(article["publishedAt"]), cmd, func() { w.portalPage("news.show", title, cmd, map[string]any{"target": id}) }))
	}
	if next := value(page.Data["nextCursor"]); next != "" {
		v.rows = append(v.rows, link("next", "下一页", "", cmdline("news", "--cursor", next), func() {
			w.portalPage("news", "平台动态", cmdline("news", "--cursor", next), map[string]any{"cursor": next})
		}))
	}
	if !w.Cached && len(items) > 0 {
		v.rows = append(v.rows, link("all", "全部标记已读", "", "bupt news read all", func() {
			w.portalConfirm("全部标记已读", []string{"news", "read", "all"}, map[string]any{"action": "news.read", "target": "all"}, func() { w.portalPage("news", "平台动态", "bupt news", nil) })
		}))
	}
}
func (w *workspace) article(v *content, cmd string, p domain.PortalPage) {
	item := p.Data
	if m := object(item["item"]); m != nil {
		item = m
	}
	title, id := value(item["title"]), value(item["id"])
	v.facts = append(v.facts, title, value(item["publishedAt"]))
	text := value(item["body"])
	if text == "" {
		text = value(item["content"])
	}
	if text == "" {
		text = value(item["markdown"])
	}
	if text != "" {
		v.rows = append(v.rows, link("body", "阅读全文", "", cmd, func() { w.textPage(title, cmd, text) }))
	} else {
		v.rows = append(v.rows, link("detail", "动态详情", "", cmd, func() { w.objectPage(title, cmd, item) }))
	}
	if !w.Cached && id != "" {
		args := []string{"news", "read", id}
		v.rows = append(v.rows, link("read", "标记为已读", "", cmdline(args...), func() {
			w.portalConfirm("标记为已读", args, map[string]any{"action": "news.read", "target": id}, func() { w.portalPage("news", "平台动态", "bupt news", nil) })
		}))
	}
}
func (w *workspace) agreement(v *content, p domain.PortalPage) {
	data := p.Data
	if m := object(data["agreement"]); m != nil {
		data = m
	}
	id := value(data["id"])
	title := value(data["title"])
	if title == "" {
		title = "服务协议"
	}
	v.rows = append(v.rows, link("text", "阅读"+title, "版本 "+id, "bupt agreement", func() { w.objectPage(title, "bupt agreement", data) }))
	if !w.Cached && p.Account != nil && p.Account.ConsentRequired && id != "" {
		args := []string{"agreement", "accept", id, "--confirm", id}
		v.rows = append(v.rows, link("accept", "接受此版本", id, cmdline(args...), func() {
			w.portalConfirm("接受协议 "+id, args, map[string]any{"action": "agreement.accept", "target": id, "confirm": id}, func() { w.portalPage("agreement", "服务协议", "bupt agreement", nil) })
		}))
	}
}
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
	if i.State == "running" {
		action("stop", "仅停止", "保留磁盘，停止后的预留状态由学校回执确认。")
	}
	if i.State == "stopped" && i.Reserved {
		action("release", "释放预留", "归还计算额度，保留可恢复的实例磁盘。")
	}
	if i.State == "stopped" && !i.Reserved {
		v.rows = append(v.rows, link("restore", "恢复预留", "选择有限时长，保留原磁盘", cmdline("compute", "instance", "restore", id), func() {
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
		v := content{empty: "此节点没有已记录的快照。"}
		for _, snap := range n.Snapshots {
			sid := value(snap["eid"])
			if sid == "" {
				sid = value(snap["id"])
			}
			name := value(snap["name"])
			v.rows = append(v.rows, link(sid, name, value(snap["state"]), cmdline(args...), func() {
				w.local(name, cmdline(args...), func() content {
					cv := content{facts: []string{fmt.Sprintf("实例 %s · 节点 %s · 快照 %s", id, node, sid)}}
					if !w.Cached {
						for _, act := range []string{"restore", "delete"} {
							op := act
							label := map[string]string{"restore": "从快照恢复", "delete": "删除快照"}[op]
							a := []string{"compute", "snapshot", op, id, sid, "--node", node, "--no-wait"}
							cv.rows = append(cv.rows, link(op, label, name, cmdline(a...), func() {
								w.review(change{title: label, method: "snapshot." + op, args: a, params: map[string]any{"target": id, "node": node, "snapshot_id": sid}, facts: []string{name + " · " + sid}, deleteName: name, task: true})
							}))
						}
					}
					return cv
				})
			}))
		}
		if !w.Cached {
			v.rows = append(v.rows, link("new", "创建节点快照", "", "bupt compute snapshot create", func() {
				w.edit("快照名称", "", func(name string) error {
					if strings.TrimSpace(name) == "" {
						return fmt.Errorf("请输入快照名称")
					}
					a := []string{"compute", "snapshot", "create", id, name, "--node", node, "--no-wait"}
					w.review(change{title: "创建快照", method: "snapshot.create", args: a, params: map[string]any{"target": id, "node": node, "name": name}, facts: []string{name, "实例 " + id + " · 节点 " + node}, task: true})
					return nil
				})
			}))
		}
		return v
	})
}
