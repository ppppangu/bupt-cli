package tui

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"bupt.local/cli/internal/platform"
)

func (w *workspace) files() content {
	return content{rows: []row{
		link("stores", "平台持久存储", "浏览存储空间、目录和文件", "bupt data stores", w.stores),
		link("transfer", "实例文件传输", "选择已准备 SSH 的实例", "bupt cp --help", w.transferTargets),
	}}
}
func (w *workspace) stores() {
	w.push(queryPage(w, "存储空间", "bupt data stores", "data.config", nil, true, func(stores []platform.Storage) content {
		v := content{empty: "当前账号没有可浏览的存储空间。"}
		for _, s := range stores {
			key := s.URLPath
			if key == "" {
				key = s.Name
			}
			title := s.Name
			if title == "" {
				title = key
			}
			detail := "打开根目录"
			if s.IsWorkDir {
				detail += " · 工作空间"
			}
			v.rows = append(v.rows, link(key, title, detail, cmdline("data", "ls", "--store", key), func() { w.directory(key, "", title) }))
		}
		return v
	}))
}
func (w *workspace) directory(store, dir, title string) {
	cmd := cmdline("data", "ls", "--store", store, "--", dir)
	w.push(queryPage(w, title, cmd, "data.list", map[string]any{"store": store, "path": dir}, true, func(files []platform.File) content {
		v := content{facts: []string{store + " / " + dir}, empty: "此目录为空。"}
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
			if f.Name == "" || f.Name == "." || f.Name == ".." || path.Base(f.Name) != f.Name {
				continue
			}
			child := path.Join(dir, f.Name)
			if f.IsDir {
				v.rows = append(v.rows, link(f.Name, f.Name+"/", "目录", cmdline("data", "ls", "--store", store, "--", child), func() { w.directory(store, child, f.Name) }))
				continue
			}
			v.rows = append(v.rows, link(f.Name, f.Name, fmt.Sprintf("%d B · %s", f.Size, f.Modified), cmdline("data", "pull", "--store", store, "--", child, "LOCAL"), func() {
				w.local(f.Name, cmd, func() content {
					return content{rows: []row{info("文件", child), info("大小", fmt.Sprintf("%d B", f.Size)), info("修改时间", f.Modified), link("get", "下载到本机…", "指定本地文件路径", cmdline("data", "pull", "--store", store, "--", child, "LOCAL"), func() {
						w.edit("本地目标文件", f.Name, func(dest string) error {
							if strings.TrimSpace(dest) == "" || dest == "-" {
								return fmt.Errorf("请输入文件路径")
							}
							w.terminalReview("下载文件", child+" → "+dest, []string{"data", "pull", "--store", store, "--", child, dest})
							return nil
						})
					})}}
				})
			}))
		}
		return v
	}))
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
			v.rows = append(v.rows, link(id+"/"+node, route.Name, "节点 "+node+" · "+label(route.Kind), cmdline("cp", "--node", node, "LOCAL", id+":PATH"), func() {
				w.local(route.Name, "bupt cp --help", func() content {
					return content{rows: []row{
						link("push", "上传文件", "本机 → 实例", cmdline("cp", "--node", node, "LOCAL", id+":PATH"), func() { w.copyForm(id, node, true) }),
						link("pull", "下载文件", "实例 → 本机", cmdline("cp", "--node", node, id+":PATH", "LOCAL"), func() { w.copyForm(id, node, false) }),
					}}
				})
			}))
		}
		return v
	})
}
func (w *workspace) copyForm(id, node string, upload bool) {
	if w.Cached {
		w.notice = "当前为缓存浏览模式"
		return
	}
	w.edit("本机文件路径", "", func(local string) error {
		if local == "" || local == "-" {
			return fmt.Errorf("请输入本机文件路径")
		}
		w.edit("实例文件路径", "", func(remote string) error {
			if remote == "" {
				return fmt.Errorf("请输入实例文件路径")
			}
			src, dst := id+":"+remote, local
			if upload {
				src, dst = dst, src
			}
			w.terminalReview("传输文件", src+" → "+dst, []string{"cp", "--node", node, "--", src, dst})
			return nil
		})
		return nil
	})
}
