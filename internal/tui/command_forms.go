package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"slices"
	"strings"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"github.com/google/uuid"
)

type FormFlag struct{ Name, Help, Type string }
type CommandForm struct {
	Path                    []string
	Summary, Usage          string
	Flags                   []FormFlag
	Query, Terminal, Export bool
}

// contextAction opens a single operation with the selected object's identity
// fixed. The surrounding object page owns discovery; no command tree is shown.
func (w *workspace) contextAction(title, detail string, path []string, bindings ...string) row {
	display := cmdline(append(slices.Clone(path), bindings...)...)
	return navLink("action/"+strings.Join(path, "/"), title, detail, display, func() {
		op, ok := formOperations[strings.Join(path, " ")]
		if !ok {
			w.notice = "此版本未提供该操作"
			return
		}
		w.boundCommandInputs(op.form(path), title, bindings)
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
	positions := strings.Fields(f.Usage)
	if len(positions) > 0 {
		positions = slices.DeleteFunc(positions[1:], func(s string) bool { return strings.Trim(s, "[]") == "--" })
	}
	variadic := len(positions) > 0 && strings.Contains(positions[len(positions)-1], "...")
	labelFor := func(i int) string {
		if len(positions) == 0 {
			return fmt.Sprintf("输入 %d", i+1)
		}
		name := strings.Trim(positions[min(i, len(positions)-1)], "[].")
		switch name {
		case "INSTANCE", "INSTANCE_ID":
			return "实例"
		case "JOB", "JOB_ID":
			return "作业"
		case "PORT":
			return "端口"
		case "NAME", "NEW_NAME":
			return "名称"
		case "PATH":
			return "存储路径"
		case "LOCAL":
			return "本机路径"
		case "REMOTE":
			return "远程路径"
		case "COMMAND":
			return "运行的命令"
		case "ARG":
			return "命令参数"
		default:
			return name
		}
	}
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
		if len(f.Path) == 1 && (f.Path[0] == "exec" || f.Path[0] == "ssh") && len(args) > 0 {
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
		v := content{}
		for _, binding := range []struct{ flag, label string }{
			{"node", "节点"}, {"store", "存储空间"}, {"group-id", "项目"}, {"cluster", "集群"}, {"spec-id", "模板"}, {"from", "来源实例"},
		} {
			if locked[binding.flag] && vals[binding.flag] != "" {
				v.facts = append(v.facts, binding.label+"　"+vals[binding.flag])
			}
		}
		for i, arg := range args {
			if i < fixed {
				v.facts = append(v.facts, labelFor(i)+"　"+arg)
				continue
			}
			label := labelFor(i)
			v.rows = append(v.rows, link(fmt.Sprintf("arg-%d", i), label, arg, cmdline(argv()...), func() { w.edit(label, arg, func(s string) error { args[i] = s; return nil }) }))
		}
		if f.Usage == "" || len(args) < len(positions) || variadic {
			label := labelFor(len(args))
			v.rows = append(v.rows, link("arg", "填写"+label, "", cmdline(argv()...), func() {
				w.edit(label, "", func(s string) error {
					if s == "" {
						return fmt.Errorf("请填写%s", label)
					}
					args = append(args, s)
					return nil
				})
			}))
		}
		if len(args) > fixed {
			v.rows = append(v.rows, link("pop", "移除"+labelFor(len(args)-1), args[len(args)-1], cmdline(argv()...), func() { args = args[:len(args)-1] }))
		}
		if len(f.Flags) > len(locked) {
			v.rows = append(v.rows, navLink("flags", "更多选项", "", cmdline(argv()...), func() {
				w.local("命令选项", cmdline(argv()...), func() content {
					cv := content{empty: "此命令没有独立选项。"}
					for _, flag := range f.Flags {
						if w.Cached && (flag.Name == "cached" || flag.Name == "refresh") {
							continue
						}
						val, chosen := vals[flag.Name]
						if locked[flag.Name] {
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
		}
		button := "执行查询"
		if !f.Query {
			button = "预览操作"
		}
		if f.Terminal || f.Export {
			button = "打开终端操作"
		}
		v.rows = append(v.rows, link("go", button, "", cmdline(argv()...), func() {
			if f.Export && (vals["file"] == "" || vals["file"] == "-") {
				w.notice = "请在选项中选择一个新私有文件"
				return
			}
			if f.Terminal || f.Export {
				w.terminalReview(title, f.Summary, argv(), TerminalAction{Name: strings.Join(f.Path, " "), Args: slices.Clone(args), Options: maps.Clone(vals)})
				return
			}
			request, err := formParams(f, args, vals)
			if err != nil {
				w.notice = err.Error()
				return
			}
			request.command = argv()
			w.reviewForm(request, !f.Query, title)
		}))
		return v
	})
}
func (w *workspace) reviewForm(request formRequest, preview bool, title string, facts ...string) {
	command := cmdline(request.command...)
	p := &page{title: title, command: command, queryMethod: request.method, needsSchool: true}
	requestKey := uuid.NewString()
	p.load = func(ctx context.Context) (func() content, error) {
		binding := ""
		if request.method == "compute.remote.checkout" {
			binding = "quote"
		}
		if request.method == "compute.admin.update" {
			binding = "revision"
		}
		raw, err := w.callForm(ctx, request, preview, requestKey)
		if err != nil {
			return nil, err
		}
		out := formText(raw)
		submit := request
		submit.params = maps.Clone(request.params)
		summary := facts
		canSubmit := true
		if preview && request.method == "instance.clone" {
			var quote domain.InstanceCreatePreview
			if err := json.Unmarshal(raw, &quote); err != nil || quote.PointsPerDay == "" {
				return nil, domain.E("COST_ESTIMATE_UNAVAILABLE", "学校尚未返回可核对的日价")
			}
			submit.params["max_points_per_day"] = quote.PointsPerDay
			summary = append(summary, "规格 "+string(quote.SpecID), "学校估价  "+quote.PointsPerDay+" 积分/天", quote.BillingStart)
		}
		if preview && binding != "" {
			var doc struct {
				Quote, Revision string
				Details         platform.RemoteQuote
				ExpiresAt       string `json:"expires_at"`
			}
			if json.Unmarshal(raw, &doc) != nil {
				return nil, fmt.Errorf("控制器未返回可验证的预览")
			}
			token := doc.Revision
			if binding == "quote" {
				token = doc.Quote
				q := doc.Details
				summary = append(summary, "规格　"+q.SpecName+" · "+q.SpecID)
				if q.Intent.Name != "" {
					summary = append(summary, "实例名称　"+q.Intent.Name)
				} else if q.Intent.Action == "renew" {
					summary = append(summary, "续期实例　"+q.Intent.Target)
				}
				canSubmit = q.Unit != "" && q.DurationUnit != "" && q.UnitPrice != "" && q.Total != "" && q.Intent.Units > 0
				if canSubmit {
					summary = append(summary, "单价　"+string(q.UnitPrice)+" "+q.Unit+" / "+q.DurationUnit,
						fmt.Sprintf("周期　%d %s", q.Intent.Units, q.DurationUnit), "总额　"+string(q.Total)+" "+q.Unit)
				} else {
					summary = append(summary, "学校报价尚缺单价、总额或计费单位，请返回后重新获取。")
				}
				if doc.ExpiresAt != "" {
					summary = append(summary, "报价有效至　"+portalTime(doc.ExpiresAt))
				}
			}
			if token == "" {
				return nil, fmt.Errorf("预览缺少确认版本")
			}
			submit.params[binding] = token
		}
		return func() content {
			v := w.textContent(out)
			if len(summary) > 0 {
				v = w.textContent(strings.Join(summary, "\n"))
			}
			if preview {
				back := link("back", "返回编辑", "", "", w.back)
				back.verb = "返回编辑"
				if !canSubmit {
					v.rows = append(v.rows, back)
					return v
				}
				confirm := link("submit", "确认"+title, "", command, func() { w.submitForm(p, requestKey, submit) })
				confirm.verb = "确认提交"
				if len(summary) > 0 {
					v.rows = append(v.rows, back, confirm, navLink("details", "查看完整预览", "", command, func() { w.textPage("完整预览", command, out) }))
				} else {
					v.rows = append([]row{back, confirm}, v.rows...)
				}
			}
			return v
		}, nil
	}
	w.push(p)
}

func (w *workspace) submitForm(p *page, key string, request formRequest) {
	if p.submitting || w.current() != p {
		return
	}
	p.submitting = true
	go func() {
		ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
		defer cancel()
		raw, err := w.callForm(ctx, request, false, key)
		out := formText(raw)
		interrupted := err != nil && (ctx.Err() != nil || uncertainFormResult(err))
		w.post(w.ctx, func() {
			p.submitting = false
			storage := request.method == "data.edit"
			checkout := request.method == "compute.remote.checkout"
			orders := func() { w.computeList("remote.order", "我的订单", "remote order", 1) }
			if err != nil {
				w.notice = whenError(err)
				// The school file API has no idempotent write receipt. A lost
				// response must return to a fresh file listing before a new edit.
				if storage && interrupted {
					w.invalidateStorage()
					p.load = nil
					p.build = func() content {
						return content{facts: []string{"学校尚未确认操作结果。"}, rows: []row{navLink("verify", "返回核对文件", "重新读取原存储位置", "", func() {
							for len(w.pages) > 1 && w.current().queryMethod != "data.list" {
								w.back()
							}
						})}}
					}
				}
				if checkout {
					switch {
					case interrupted || errorCode(err) == "ORDER_RESULT_UNCERTAIN":
						p.load = nil
						p.build = func() content {
							return content{facts: []string{"订单提交结果待核对。"}, rows: []row{navLink("orders", "查看我的订单", "", "bupt remote order", orders)}}
						}
					case errorCode(err) == "QUOTE_CHANGED" || errorCode(err) == "QUOTE_EXPIRED":
						p.build = func() content {
							return content{rows: []row{link("requote", "重新取得学校报价", "", p.command, func() { w.fetchPage(p, true) })}}
						}
					}
				}
				return
			}
			p.load = nil
			p.build = func() content {
				v := w.textContent(out)
				if checkout {
					v.rows = append([]row{navLink("orders", "查看我的订单", "核对学校订单与付款状态", "bupt remote order", orders)}, v.rows...)
				} else if request.method == "compute.quota.request" {
					v.rows = append([]row{navLink("requests", "查看我的申请", "查询学校审批进度", "bupt compute request", func() { w.computeList("request", "我的申请", "request", 1) })}, v.rows...)
				}
				return v
			}
			w.notice = "操作已完成"
			if storage {
				w.invalidateStorage()
			} else if strings.HasPrefix(request.method, "preset.") {
				w.refreshPresets()
			}
		})
	}()
}

func uncertainFormResult(err error) bool {
	var d *domain.Error
	if errors.As(err, &d) {
		return d.Code == "IO_ERROR" || d.Code == "TIMEOUT" || d.Code == "NETWORK" || d.Code == "CANCELLED" || d.Code == "CANCELED"
	}
	var transport net.Error
	return errors.As(err, &transport) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
