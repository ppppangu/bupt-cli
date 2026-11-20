package tui

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
)

func (w *workspace) files() content {
	return content{rows: []row{
		navLink("stores", "平台持久存储", "把文件保存到学校存储空间", "bupt compute storage stores", w.stores),
		navLink("transfer", "实例文件传输", "在本机和实例之间上传或下载文件", "bupt cp --help", w.transferTargets),
	}}
}
func (w *workspace) invalidateStorage() {
	for _, p := range w.pages {
		if p.queryMethod == "data.list" {
			p.stale = true
		}
	}
}

func (w *workspace) stores() { w.projectStores("") }
func (w *workspace) projectStores(group domain.ID) {
	title := "存储空间"
	if group != "" {
		title = "项目存储 · " + string(group)
	}
	w.push(queryPage(w, title, "bupt compute storage stores", "data.config", nil, true, func(stores []platform.Storage) content {
		v := content{empty: "当前账号没有可浏览的存储空间。"}
		for _, s := range stores {
			key := s.URLPath
			if key == "" {
				key = s.Name
			}
			if key == "" {
				continue
			}
			title := s.Name
			if title == "" {
				title = key
			}
			detail := "打开根目录"
			if s.IsWorkDir {
				detail += " · 工作空间"
			}
			location := platform.StoragePath{Store: key, GroupID: group}
			v.rows = append(v.rows, navLink(key, title, detail, cmdline(storageCommand("ls", location)...), func() { w.directory(location, title) }))
		}
		return v
	}))
}
func storageBindings(location platform.StoragePath, paths ...string) []string {
	return append([]string{"--store=" + location.Store, "--group-id=" + string(location.GroupID), "--"}, paths...)
}
func storageCommand(op string, location platform.StoragePath, paths ...string) []string {
	return append([]string{"compute", "storage", op}, storageBindings(location, paths...)...)
}
func (w *workspace) directory(dir platform.StoragePath, title string) {
	cmd := cmdline(storageCommand("ls", dir, dir.Path)...)
	p := queryPage(w, title, cmd, "data.list", dir, true, func(files []platform.File) content {
		v := content{facts: []string{dir.Store + " / " + dir.Path}}
		if dir.GroupID != "" {
			v.facts = append(v.facts, "项目 "+string(dir.GroupID))
		}
		files = slices.Clone(files)
		slices.SortStableFunc(files, func(a, b platform.File) int {
			if a.IsDir != b.IsDir {
				if a.IsDir {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Name, b.Name)
		})
		for _, f := range files {
			if !validFileName(f.Name) {
				continue
			}
			child := dir
			child.Path = path.Join(dir.Path, f.Name)
			if f.IsDir {
				v.rows = append(v.rows, navLink(f.Name, f.Name+"/", "目录", cmdline(storageCommand("ls", child, child.Path)...), func() { w.directory(child, f.Name) }))
				continue
			}
			size := fmt.Sprintf("%d B", f.Size)
			if f.Modified != "" {
				size += " · " + computeTime(f.Modified)
			}
			v.rows = append(v.rows, navLink(f.Name, f.Name, size, cmdline(storageCommand("pull", child, child.Path, "LOCAL")...), func() { w.fileDetails(child, f) }))
		}
		if len(v.rows) == 0 {
			v.facts = append(v.facts, "此目录为空。")
		}
		if !w.Cached {
			v.rows = append(v.rows,
				link("action/mkdir", "新建目录…", "在当前目录中创建子目录", cmdline(storageCommand("mkdir", dir)...), func() { w.createDirectory(dir) }),
				link("action/push", "上传文件…", "选择本地文件和目标文件名", cmdline(storageCommand("push", dir)...), func() { w.uploadFile(dir) }),
			)
			if dir.Path != "" {
				v.rows = append(v.rows, w.storageActions(dir)...)
			}
		}
		return v
	})
	w.push(p)
}
func (w *workspace) fileDetails(location platform.StoragePath, f platform.File) {
	cmd := cmdline(storageCommand("pull", location, location.Path, "LOCAL")...)
	show := func(f platform.File) content {
		c := content{facts: []string{location.Store + " / " + location.Path, fmt.Sprintf("大小　%d B", f.Size)}}
		if f.Modified != "" {
			c.facts = append(c.facts, "修改时间　"+computeTime(f.Modified))
		}
		if !w.Cached {
			c.rows = append(c.rows, link("get", "下载到本机…", "指定本地文件路径", cmd, func() {
				w.edit("本地目标文件", f.Name, func(dest string) error {
					if strings.TrimSpace(dest) == "" || dest == "-" {
						return fmt.Errorf("请输入文件路径")
					}
					w.terminalReview("下载文件", location.Path+" → "+dest, storageCommand("pull", location, location.Path, dest), TerminalAction{Name: "compute storage pull", Args: []string{location.Path, dest}, Options: map[string]string{"store": location.Store, "group-id": string(location.GroupID)}})
					return nil
				})
			}))
			c.rows = append(c.rows, w.storageActions(location)...)
		}
		return c
	}
	parent := location
	parent.Path = path.Dir(location.Path)
	if parent.Path == "." {
		parent.Path = ""
	}
	p := queryPage(w, f.Name, cmd, "data.list", parent, true, func(files []platform.File) content {
		for _, fresh := range files {
			if fresh.Name == f.Name && !fresh.IsDir {
				return show(fresh)
			}
		}
		return content{empty: "文件已移动或移除，请返回目录重新选择。"}
	})
	p.build = func() content { return show(f) }
	w.push(p)
}
func (w *workspace) createDirectory(dir platform.StoragePath) {
	w.edit("新目录名", "", func(name string) error {
		if !validFileName(name) {
			return fmt.Errorf("请输入单个目录名称")
		}
		w.contextAction("新建目录", "", []string{"compute", "storage", "mkdir"}, storageBindings(dir, path.Join(dir.Path, name))...).open()
		return nil
	})
}
func (w *workspace) uploadFile(dir platform.StoragePath) {
	w.edit("本机文件路径", "", func(local string) error {
		if strings.TrimSpace(local) == "" || local == "-" {
			return fmt.Errorf("请输入文件路径")
		}
		w.edit("存储中的文件名", filepath.Base(local), func(name string) error {
			if !validFileName(name) {
				return fmt.Errorf("请输入单个文件名")
			}
			w.contextAction("上传文件", "", []string{"compute", "storage", "push"}, storageBindings(dir, local, path.Join(dir.Path, name))...).open()
			return nil
		})
		return nil
	})
}

func validFileName(name string) bool {
	return strings.TrimSpace(name) != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n")
}

func (w *workspace) storageActions(location platform.StoragePath) []row {
	return []row{
		w.contextAction("重命名…", "修改此文件或目录的名称", []string{"compute", "storage", "rename"}, storageBindings(location, location.Path)...),
		w.contextAction("删除…", "输入原名称确认删除", []string{"compute", "storage", "rm"}, storageBindings(location, location.Path)...),
	}
}

func (w *workspace) transferTargets() {
	w.local("实例文件传输", "bupt cp --help", func() content {
		v := content{empty: "暂无就绪的 SSH 路由。启动实例并准备 SSH 后可传输文件。"}
		for _, route := range w.report.Routes {
			if route.State != "ready" {
				continue
			}
			id := string(route.InstanceID)
			node := string(route.NodeID)
			v.rows = append(v.rows, navLink(id+"/"+node, route.Name, "节点 "+node+" · "+label(route.Kind), cmdline("cp", "--node", node, "LOCAL", id+":PATH"), func() {
				w.local(route.Name, "bupt cp --help", func() content {
					return content{rows: []row{
						link("push", "上传文件", "本机 → 实例", cmdline("cp", "--node", node, "LOCAL", id+":PATH"), func() { w.copyForm(id, node, true, nil) }),
						link("pull", "下载文件", "实例 → 本机", cmdline("cp", "--node", node, id+":PATH", "LOCAL"), func() { w.copyForm(id, node, false, nil) }),
					}}
				})
			}))
		}
		return v
	})
}
func (w *workspace) uploadToInstance(i domain.Instance) {
	origin := w.current()
	options := func() content {
		v := content{empty: "当前没有就绪节点，请返回实例页准备 SSH。"}
		current, ok := w.instance(i.ID)
		if !ok || w.Cached {
			return v
		}
		run := w.instanceRun(current)
		for _, route := range w.report.Routes {
			if route.InstanceID != i.ID {
				continue
			}
			n, ok := w.verifiedNode(current, route)
			if !ok {
				continue
			}
			name := n.Name
			if name == "" {
				name = string(n.ID)
			}
			detail := "SSH 就绪"
			if run != nil && run.NodeID == n.ID {
				detail += " · 本次作业节点"
			}
			v.rows = append(v.rows, link(string(n.ID), name, detail, "", func() {
				w.copyForm(string(i.ID), string(n.ID), true, func(dest string) {
					w.uploadedToInstance(origin, i.ID, n.ID, name, dest)
				})
			}))
		}
		return v
	}
	if choices := options(); len(choices.rows) == 1 {
		choices.rows[0].open()
		return
	}
	w.local("选择接收代码的节点", "bupt cp --help", options)
	if run := w.instanceRun(i); run != nil && run.NodeID != "" {
		w.current().key = string(run.NodeID)
	}
}

func (w *workspace) uploadedToInstance(origin *page, instance, node domain.ID, name, path string) {
	for len(w.pages) > 1 && w.current() != origin {
		w.back()
	}
	if origin.filter != "" {
		w.clearFilter(origin)
	}
	origin.key = "ssh/" + string(node)
	w.notice = "已上传到 " + name + " · " + path
	current, ok := w.instance(instance)
	if !ok {
		return
	}
	run := w.instanceRun(current)
	switch {
	case run != nil && run.NodeID != "" && run.NodeID != node:
		w.notice += " · 后台作业使用节点 " + string(run.NodeID)
	case current.Managed && current.State == "running":
		origin.key = "job"
		w.notice += " · 可提交后台作业"
	case current.State == "running":
		origin.key = "start"
		w.notice += " · 设置运行期限后可提交作业"
	}
}

func (w *workspace) copyForm(id, node string, upload bool, after func(string)) {
	if w.Cached {
		w.notice = "当前为缓存浏览模式"
		return
	}
	fromLabel, toLabel := "实例源文件", "本机目标文件"
	if upload {
		fromLabel, toLabel = "本机源文件", "实例目标文件"
	}
	w.edit(fromLabel, "", func(source string) error {
		if source == "" || upload && source == "-" {
			return fmt.Errorf("请填写%s路径", fromLabel)
		}
		w.edit(toLabel, "", func(destination string) error {
			if destination == "" || !upload && destination == "-" {
				return fmt.Errorf("请填写%s路径", toLabel)
			}
			src, dst := id+":"+source, destination
			if upload {
				src, dst = source, id+":"+destination
			}
			args := []string{"cp", "--node", node, "--", src, dst}
			w.terminalConfirmation("传输文件", src+" → "+dst, args, func() {
				if w.foreground(TerminalAction{Name: "cp", Args: []string{src, dst}, Options: map[string]string{"node": node}}) && after != nil {
					after(destination)
				}
			})
			return nil
		})
		return nil
	})
}
