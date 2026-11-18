package tui

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

func (w *workspace) resources() content {
	v := content{}
	v.rows = append(v.rows, navLink("quota", "我的配额", "已用量、上限和单位", "bupt quota", w.quotas),
		navLink("spec", "资源池与模板", "比较资源池、GPU、CPU 和内存", "bupt resources", w.catalog),
		navLink("preset", "我的预设", fmt.Sprintf("%d 个 · 默认 %s", len(w.Config.Presets), label(w.Config.DefaultPreset)), "bupt preset ls", w.presets))
	return v
}
func (w *workspace) quotas() {
	w.local("我的配额", "bupt quota", func() content {
		v := content{facts: []string{"资源                       已用 / 上限"}, empty: "平台未提供这组配额数据。"}
		for _, m := range w.report.Capacity {
			v.rows = append(v.rows, row{key: m.Scope + "/" + m.Resource, title: m.Resource, detail: m.Amounts() + " · " + m.Scope})
		}
		if !w.Cached {
			v.rows = append(v.rows, link("request", "申请调整额度…", "选择个人算力或存储额度", "bupt compute quota request", w.quotaRequests))
		}
		return v
	})
}

func (w *workspace) quotaRequests() {
	w.push(queryPage(w, "选择申请额度", "bupt compute quota", "quota.get", nil, true, func(quotas map[string]any) content {
		v := content{empty: "没有可申请调整的额度。"}
		for _, key := range orderedKeys(quotas) {
			kind, id, ok := strings.Cut(key, ":")
			quota, valid := quotas[key].(map[string]any)
			if !ok || !valid || (kind != "UserQuota" && kind != "UserStorageQuota") {
				continue
			}
			if _, err := strconv.ParseUint(id, 10, 64); err != nil {
				continue
			}
			title, detail := "算力额度", "CPU、GPU、内存和实例数量"
			scope, _ := quota["domain"].(string)
			if kind == "UserStorageQuota" {
				title, detail = "存储额度", "块存储与文件空间"
				scope, _ = quota["storageName"].(string)
			}
			if scope != "" {
				title += " · " + scope
			}
			v.rows = append(v.rows, navLink(key, title, detail, cmdline("compute", "quota", "request", id), func() {
				w.quotaForm(key, title, quota)
			}))
		}
		return v
	}))
}
func (w *workspace) catalog()        { w.templateList(false) }
func (w *workspace) chooseTemplate() { w.templateList(true) }

func (w *workspace) templateList(create bool) {
	title := "资源目录"
	if create {
		title = "选择实例模板"
	}
	p := queryPage(w, title, "bupt compute catalog", "resources.overview", map[string]any{"cached": w.Cached}, false, func(result domain.ResourceOverview) content {
		v := content{empty: "此账号暂无可见模板。", facts: slices.Clone(result.Warnings)}
		online := !w.Cached && result.CatalogSource == "live"
		if result.CatalogSource == "unavailable" {
			v.empty = "还没有可用的模板目录。"
		} else if !online {
			v.facts = append(v.facts, "已保存的模板目录 · 完整规格和创建需要在线读取")
		}
		for _, pool := range result.Pools {
			if len(pool.Templates) != 0 {
				v.rows = append(v.rows, row{key: "pool/" + pool.ID, title: pool.Name})
			}
			for _, t := range pool.Templates {
				name := strings.TrimSpace(t.Name + " " + t.Version)
				summary := resourceSummary(t.Resources)
				cmd := cmdline("compute", "catalog", "show", string(t.ID))
				r := navLink("spec/"+string(t.ID), name, summary, cmd, func() {
					switch {
					case !online:
						w.textPage(name, cmd, strings.Join([]string{pool.Name, name, summary, "模板 ID " + string(t.ID)}, "\n"))
					case create:
						w.createFromTemplate(t, pool.Name)
					default:
						w.specPage(domain.Spec{ID: t.ID, Name: t.Name, Version: t.Version})
					}
				})
				if create && online {
					r.verb = "填写申请"
				}
				v.rows = append(v.rows, r)
			}
		}
		return v
	})
	w.push(p)
}
func object(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	var m map[string]any
	if text, ok := v.(string); ok {
		d := json.NewDecoder(strings.NewReader(text))
		d.UseNumber()
		_ = d.Decode(&m)
	}
	return m
}

type specGroup struct {
	NodeName    string `json:"nodeName"`
	Role        string `json:"role"`
	Constraints []struct{ Type, Value, Options string }
}

func constraintRows(v any) []row {
	var b []byte
	if s, ok := v.(string); ok {
		b = []byte(s)
	} else {
		b, _ = json.Marshal(v)
	}
	var groups map[string]specGroup
	if json.Unmarshal(b, &groups) != nil {
		return nil
	}
	var rows []row
	for _, k := range orderedKeys(groups) {
		g := groups[k]
		for _, f := range g.Constraints {
			detail := f.Value
			if f.Options != "" {
				detail += " · 可选 " + strings.ReplaceAll(f.Options, ",", " / ")
			}
			name := f.Type
			if name == "Memory" {
				name = "内存 (MiB)"
			}
			if len(groups) > 1 {
				name = g.NodeName + " " + name
			}
			rows = append(rows, row{key: k + "/" + f.Type, title: name, detail: detail})
		}
	}
	return rows
}
func (w *workspace) specPage(s domain.Spec) {
	cmd := cmdline("spec", "show", string(s.ID))
	selectedID := s.ID
	p := queryPage(w, s.Name, cmd, "spec.show", map[string]any{"target": s.ID}, true, func(s domain.Spec) content {
		if s.ID != selectedID {
			return content{empty: "模板已变更，请返回资源目录重新选择。"}
		}
		v := content{facts: []string{strings.TrimSpace(s.Name+" "+s.Version) + "   ·   " + value(s.Options["poolDisplayName"]), resourceSummary(object(s.Options["resourceSummary"]))}}
		v.rows = []row{navLink("create", "用此模板创建…", "下一步填写名称和资源", cmdline("instance", "create", "NAME", "--spec-id", string(s.ID)), func() { w.createForm(s, nil) }),
			link("estimate", "估算模板默认价格", "读取平台估价", cmdline("estimate", "--spec-id", string(s.ID)), func() { w.estimate(&draft{spec: s}) })}
		v.rows = append(v.rows,
			w.contextAction("自定义资源估价", "按所选 CPU、GPU 和内存查询价格", []string{"compute", "catalog", "estimate"}, "--spec-id="+string(s.ID)),
			w.contextAction("保存模板预设", "为常用配置命名", []string{"compute", "preset", "set"}, "--spec-id="+string(s.ID)),
		)
		v.rows = append(v.rows, constraintRows(s.Options["resourceConstraints"])...)
		if d := seconds(s.Options["expDuration"]); d != "" {
			v.rows = append(v.rows, info("平台默认时长", d))
		}
		v.rows = append(v.rows, info("模板 ID", string(s.ID)))
		if s.Description != "" {
			v.rows = append(v.rows, navLink("desc", "说明", s.Description, cmd, func() { w.textPage("模板说明", cmd, s.Description) }))
		}
		return v
	})
	w.push(p)
}
func seconds(v any) string {
	d, e := time.ParseDuration(value(v) + "s")
	if e != nil {
		return ""
	}
	return d.String()
}
func (w *workspace) estimate(d *draft) {
	params := map[string]any{"spec_id": d.spec.ID}
	args := []string{"estimate", "--spec-id", string(d.spec.ID)}
	if d.preset != nil {
		params["preset"] = d.preset.Name
		params["preset_snapshot"] = *d.preset
		args = []string{"estimate", "--preset", d.preset.Name}
	}
	if len(d.resources) > 0 {
		params["resources"] = maps.Clone(d.resources)
		for _, k := range orderedKeys(d.resources) {
			args = append(args, "--"+k, d.resources[k])
		}
	}
	d.price = ""
	w.push(queryPage(w, "学校估价", cmdline(args...), "spec.estimate", params, true, func(raw json.RawMessage) content {
		result, err := readComputeJSON(raw)
		if err != nil || object(result) == nil {
			return content{empty: "学校尚未返回可读的估价。"}
		}
		data := object(result)
		v := content{empty: "学校尚未返回估价。"}
		for _, k := range orderedKeys(data) {
			name, val := k, value(data[k])
			if k == "points_per_day" {
				name = "积分 / 天"
				if val == "" {
					val = "待学校确认"
				}
				if n, err := strconv.ParseFloat(val, 64); err == nil && n >= 0 && !math.IsInf(n, 0) {
					d.price = val
				}
			}
			v.rows = append(v.rows, info(name, val))
		}
		return v
	}))
}
func (w *workspace) refreshPresets() {
	if w.ReloadPresets == nil {
		return
	}
	fresh, err := w.ReloadPresets()
	if err != nil {
		w.notice = "读取预设失败 · " + err.Error()
		return
	}
	current := *w.Config
	current.Presets = fresh.Presets
	current.DefaultPreset = fresh.DefaultPreset
	w.Config = &current
}

func (w *workspace) presets() {
	w.local("我的预设", "bupt preset ls", func() content {
		v := content{empty: "还没有预设。可用 bupt preset set NAME --from INSTANCE 保存常用配置。"}
		if !w.Cached {
			v.rows = append(v.rows, w.contextAction("新建资源预设", "选择模板或实例作为来源", []string{"compute", "preset", "set"}))
		}
		for _, name := range orderedKeys(w.Config.Presets) {
			p := w.Config.Presets[name]
			title := name
			if name == w.Config.DefaultPreset {
				title += " · 默认"
			}
			v.rows = append(v.rows, navLink(name, title, policyText(p.Policy), cmdline("preset", "show", name), func() {
				w.local(name, cmdline("preset", "show", name), func() content {
					p, exists := w.Config.Presets[name]
					if !exists {
						return content{empty: "此预设已移除。"}
					}
					c := content{facts: []string{"模板 " + string(p.SpecID), policyText(p.Policy)}}
					if !w.Cached {
						c.rows = append(c.rows, navLink("create", "按预设创建…", "预览后提交", cmdline("instance", "create", "NAME", "--preset", name), func() { w.createForm(domain.Spec{ID: p.SpecID}, &p) }),
							link("estimate", "估算费用", "读取预设资源的价格", cmdline("estimate", "--preset", name), func() { w.estimate(&draft{spec: domain.Spec{ID: p.SpecID}, preset: &p}) }))
						c.rows = append(c.rows, w.contextAction("编辑预设", "调整资源与托管策略", []string{"compute", "preset", "set"}, name))
						if w.Config.DefaultPreset == name {
							c.rows = append(c.rows, w.contextAction("取消默认预设", "新建实例时不再自动使用此预设", []string{"compute", "preset", "use"}, "-"))
						} else {
							c.rows = append(c.rows, w.contextAction("设为默认", "新建实例优先使用此配置", []string{"compute", "preset", "use"}, name),
								w.contextAction("移除预设", "从选定的配置文件删除", []string{"compute", "preset", "rm"}, name))
						}
					}
					for _, group := range orderedKeys(p.Constraints) {
						b, _ := json.Marshal(p.Constraints[group])
						var fields []struct{ Type, Value string }
						if json.Unmarshal(b, &fields) == nil {
							for _, f := range fields {
								c.rows = append(c.rows, row{key: group + "/" + f.Type, title: f.Type, detail: f.Value})
							}
						}
					}
					return c
				})
			}))
		}
		return v
	})
	w.refreshPresets()
}
func policyText(p domain.RunPolicy) string {
	limit := p.MaxRuntime
	if !p.Until.IsZero() {
		limit = moment(p.Until)
	}
	if limit == "" {
		limit = "待指定"
	}
	return limit + " · " + label(p.Cleanup) + " · 自动续期 " + yes(p.AutoRenew)
}

func newestFirst(runs []domain.Run) []domain.Run {
	ordered := slices.Clone(runs)
	slices.SortStableFunc(ordered, func(a, b domain.Run) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return ordered
}
