package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

type FormFlag struct{ Name, Help, Type string }
type CommandForm struct {
	Path                              []string
	Title, Summary, Usage             string
	Flags                             []FormFlag
	Children                          []CommandForm
	Runnable, Query, Terminal, Export bool
}

func (w *workspace) moreCommands(service string) row {
	return link("commands", "全部管理操作", "按对象浏览命令与参数", "bupt help "+service+" --all", func() {
		f, ok := w.Commands[service]
		if ok {
			w.commandGroup(f)
		}
	})
}
func (w *workspace) commandGroup(f CommandForm) {
	if len(f.Children) == 0 {
		w.commandInputs(f)
		return
	}
	w.local(f.Title, cmdline(f.Path...), func() content {
		v := content{}
		if f.Runnable {
			v.rows = append(v.rows, link("default", "查询或填写当前命令", f.Summary, cmdline(f.Path...), func() { w.commandInputs(f) }))
		}
		for _, c := range f.Children {
			v.rows = append(v.rows, link(c.Title, c.Title, c.Summary, cmdline(c.Path...), func() { w.commandGroup(c) }))
		}
		return v
	})
}
func (w *workspace) commandInputs(f CommandForm) {
	var args []string
	vals := map[string]string{}
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
		return append(out, args...)
	}
	w.local(f.Title, cmdline(f.Path...), func() content {
		v := content{facts: []string{f.Summary, "用法  " + f.Usage}}
		for i, arg := range args {
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
		if len(args) > 0 {
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
		title := "执行查询"
		if !f.Query {
			title = "预览操作"
		}
		if f.Terminal || f.Export {
			title = "打开终端操作"
		}
		v.rows = append(v.rows, link("go", title, "", cmdline(argv()...), func() {
			for _, arg := range args {
				if arg == "--" {
					break
				}
				if strings.HasPrefix(arg, "-") && arg != "-" {
					w.notice = "请在选项页设置标志，远端参数可以放在 -- 后。"
					return
				}
			}
			if len(f.Children) > 0 && len(args) > 0 {
				w.notice = "请从子命令列表选择具体操作"
				return
			}
			if f.Terminal {
				w.terminalReview(f.Title, f.Summary, argv())
				return
			}
			if f.Export && (vals["output"] == "" || vals["output"] == "-") {
				w.notice = "请在选项中选择一个新私有文件"
				return
			}
			w.runForm(argv(), !f.Query, f.Title)
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
	p.load = func(ctx context.Context) (func() content, error) {
		call := slices.Clone(args)
		binding := ""
		if preview {
			call = append([]string{"--dry-run"}, call...)
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
			tail := slices.DeleteFunc(slices.Clone(args[4:]), func(arg string) bool { return strings.HasPrefix(arg, "--"+binding+"=") })
			submitArgs = append(slices.Clone(args[:4]), "--"+binding+"="+token)
			submitArgs = append(submitArgs, tail...)
			if details := object(doc["details"]); details != nil {
				fact = "报价 " + value(details["total"]) + " " + value(details["unit"]) + " · 周期单位 " + value(details["duration_unit"])
			}
		}
		return func() content {
			v := w.textContent(clean(out))
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
						out, e := w.Capture(ctx, submitArgs)
						w.post(w.ctx, func() {
							p.submitting = false
							if e != nil {
								w.notice = whenError(e)
								return
							}
							p.load = nil
							p.build = func() content { return w.textContent(clean(out)) }
							w.notice = "操作已完成"
						})
					}()
				})}, v.rows...)
			}
			return v
		}, nil
	}
	w.push(p)
}
