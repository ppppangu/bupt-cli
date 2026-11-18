package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
)

type FormFlag struct{ Name, Help, Type string }
type CommandForm struct {
	Path                              []string
	Title, Summary, Usage             string
	Flags                             []FormFlag
	Children                          []CommandForm
	Runnable, Query, Terminal, Export bool
}

// contextAction opens a single operation with the selected object's identity
// fixed. The surrounding object page owns discovery; no command tree is shown.
func (w *workspace) contextAction(title, detail string, path []string, bindings ...string) row {
	display := cmdline(append(slices.Clone(path), bindings...)...)
	return link("action/"+strings.Join(path, "/"), title, detail, display, func() {
		form, ok := w.Commands[path[0]]
		for _, part := range path[1:] {
			i := slices.IndexFunc(form.Children, func(child CommandForm) bool { return child.Path[len(child.Path)-1] == part })
			if i < 0 {
				ok = false
				break
			}
			form = form.Children[i]
		}
		if !ok || !form.Runnable {
			w.notice = "此版本未提供该操作"
			return
		}
		w.boundCommandInputs(form, title, bindings)
	})
}

func (w *workspace) boundCommandInputs(f CommandForm, title string, bindings []string) {
	var args []string
	vals, locked := map[string]string{}, map[string]bool{}
	literal := false
	for _, s := range bindings {
		if s == "--" && !literal {
			literal = true
			continue
		}
		if strings.HasPrefix(s, "--") && !literal {
			name, v, ok := strings.Cut(strings.TrimPrefix(s, "--"), "=")
			if !ok || !slices.ContainsFunc(f.Flags, func(flag FormFlag) bool { return flag.Name == name }) {
				w.notice = "无法绑定当前对象选项"
				return
			}
			vals[name], locked[name] = v, true
		} else {
			args = append(args, s)
		}
	}
	fixed := len(args)
	if w.Cached {
		hasCached := slices.ContainsFunc(f.Flags, func(flag FormFlag) bool { return flag.Name == "cached" })
		if !f.Query || !hasCached {
			w.notice = "此操作需要在线工作区"
			return
		}
		vals["cached"] = "true"
	}
	argv := func() []string {
		out := slices.Clone(f.Path)
		for _, flag := range f.Flags {
			if val, ok := vals[flag.Name]; ok {
				out = append(out, "--"+flag.Name+"="+val)
			}
		}
		// SSH and exec place the separator between the instance and program.
		if (slices.Equal(f.Path, []string{"compute", "exec"}) || slices.Equal(f.Path, []string{"compute", "ssh"})) && len(args) > 0 {
			out = append(out, args[0])
			if len(args) > 1 {
				out = append(out, "--")
				out = append(out, args[1:]...)
			}
			return out
		}
		if len(args) > 0 {
			out = append(out, "--")
		}
		return append(out, args...)
	}
	w.local(title, cmdline(argv()...), func() content {
		v := content{facts: []string{f.Summary, "用法  " + f.Usage}}
		for i, arg := range args {
			if i < fixed {
				v.rows = append(v.rows, info(fmt.Sprintf("目标参数 %d", i+1), arg))
				continue
			}
			v.rows = append(v.rows, link(fmt.Sprintf("arg-%d", i), fmt.Sprintf("参数 %d", i+1), arg, cmdline(argv()...), func() { w.edit("参数值", arg, func(s string) error { args[i] = s; return nil }) }))
		}
		v.rows = append(v.rows, link("arg", "添加一个参数", "含空格的路径可直接输入", cmdline(argv()...), func() {
			w.edit("参数值", "", func(s string) error {
				if s == "" {
					return fmt.Errorf("请输入参数")
				}
				args = append(args, s)
				return nil
			})
		}))
		if len(args) > fixed {
			v.rows = append(v.rows, link("pop", "移除末尾参数", args[len(args)-1], cmdline(argv()...), func() { args = args[:len(args)-1] }))
		}
		v.rows = append(v.rows, link("flags", "选项", fmt.Sprintf("已设置 %d 项", len(vals)), cmdline(argv()...), func() {
			w.local("命令选项", cmdline(argv()...), func() content {
				cv := content{empty: "此命令没有独立选项。"}
				for _, flag := range f.Flags {
					if w.Cached && (flag.Name == "cached" || flag.Name == "refresh") {
						continue
					}
					val, chosen := vals[flag.Name]
					if locked[flag.Name] {
						cv.rows = append(cv.rows, info("--"+flag.Name, val))
						continue
					}
					detail := flag.Help
					if chosen {
						detail = val + " · " + detail
					}
					cv.rows = append(cv.rows, link(flag.Name, "--"+flag.Name, detail, cmdline(argv()...), func() {
						if flag.Type == "bool" {
							if !chosen {
								vals[flag.Name] = "true"
							} else if val == "true" {
								vals[flag.Name] = "false"
							} else {
								delete(vals, flag.Name)
							}
							return
						}
						w.edit("--"+flag.Name, val, func(s string) error {
							if s == "" {
								delete(vals, flag.Name)
							} else {
								vals[flag.Name] = s
							}
							return nil
						})
					}))
				}
				return cv
			})
		}))
		button := "执行查询"
		if !f.Query {
			button = "预览操作"
		}
		if f.Terminal || f.Export {
			button = "打开终端操作"
		}
		v.rows = append(v.rows, link("go", button, "", cmdline(argv()...), func() {
			if f.Terminal {
				w.terminalReview(title, f.Summary, argv())
				return
			}
			if f.Export && (vals["output"] == "" || vals["output"] == "-") {
				w.notice = "请在选项中选择一个新私有文件"
				return
			}
			w.runForm(argv(), !f.Query, title)
		}))
		return v
	})
}
func (w *workspace) runForm(args []string, preview bool, title string) {
	if w.Capture == nil {
		return
	}
	command := cmdline(args...)
	p := &page{title: title, command: command}
	requestKey := "--idempotency-key=" + uuid.NewString()
	p.load = func(ctx context.Context) (func() content, error) {
		call := slices.Clone(args)
		binding := ""
		if preview {
			call = append([]string{"--dry-run", requestKey}, call...)
			if len(args) >= 4 && args[0] == "compute" {
				if args[1] == "remote" && args[2] == "order" && (args[3] == "create" || args[3] == "renew") {
					binding = "quote"
				}
				if args[1] == "admin" && args[2] == "quota" && args[3] == "set" {
					binding = "revision"
				}
				if binding != "" {
					call = append([]string{"--json"}, call...)
				}
			}
		}
		out, e := w.Capture(ctx, call)
		if e != nil {
			return nil, e
		}
		submitArgs := slices.Clone(args)
		fact := ""
		if binding != "" {
			var doc map[string]any
			if json.Unmarshal([]byte(out), &doc) != nil {
				return nil, fmt.Errorf("控制器未返回可验证的预览")
			}
			token, _ := doc[binding].(string)
			if token == "" {
				return nil, fmt.Errorf("预览缺少确认版本")
			}
			tail := args[4:]
			boundary := slices.Index(tail, "--")
			if boundary < 0 {
				boundary = len(tail)
			}
			flags := slices.DeleteFunc(slices.Clone(tail[:boundary]), func(arg string) bool { return strings.HasPrefix(arg, "--"+binding+"=") })
			submitArgs = append(slices.Clone(args[:4]), "--"+binding+"="+token)
			submitArgs = append(submitArgs, flags...)
			submitArgs = append(submitArgs, tail[boundary:]...)
			if details := object(doc["details"]); details != nil {
				fact = "报价 " + value(details["total"]) + " " + value(details["unit"]) + " · 周期单位 " + value(details["duration_unit"])
			}
		}
		return func() content {
			v := w.textContent(out)
			if fact != "" {
				v.facts = append(v.facts, fact)
			}
			if preview {
				v.rows = append([]row{link("back", "返回编辑", "", "", w.back), link("submit", "确认执行", title, cmdline(submitArgs...), func() {
					if p.submitting {
						return
					}
					p.submitting = true
					go func() {
						ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
						defer cancel()
						out, e := w.Capture(ctx, append([]string{requestKey}, submitArgs...))
						w.post(w.ctx, func() {
							p.submitting = false
							if e != nil {
								w.notice = whenError(e)
								return
							}
							p.load = nil
							p.build = func() content { return w.textContent(out) }
							w.notice = "操作已完成"
							if len(args) > 2 && args[0] == "compute" {
								switch args[1] {
								case "preset":
									w.refreshPresets()
								case "storage":
									w.invalidateStorage()
								}
							}
						})
					}()
				})}, v.rows...)
			}
			return v
		}, nil
	}
	w.push(p)
}
