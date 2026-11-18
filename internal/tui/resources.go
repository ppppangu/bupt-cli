package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

func (w *workspace) resources() content {
	v := content{}
	v.rows = append(v.rows, link("quota", "我的配额", "已用量、上限和单位", "bupt quota", w.quotas),
		link("spec", "资源池与模板", "比较资源池、GPU、CPU 和内存", "bupt resources", w.catalog),
		link("preset", "我的预设", fmt.Sprintf("%d 个 · 默认 %s", len(w.Config.Presets), label(w.Config.DefaultPreset)), "bupt preset ls", w.presets))
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
			r := w.contextAction(title, detail+" · ID "+id, []string{"compute", "quota", "request"}, id)
			r.key = key
			v.rows = append(v.rows, r)
		}
		return v
	}))
}
func (w *workspace) catalog() {
	p := queryPage(w, "资源池与模板", "bupt resources", "resources.overview", map[string]any{"cached": w.Cached}, false, func(result domain.ResourceOverview) content {
		v := content{empty: "此账号暂无可见模板。"}
		source := "尚无记录"
		if result.CatalogSource == "cached" {
			source = "缓存"
		} else if result.CatalogSource == "live" {
			source = "实时"
		}
		if result.CatalogAt != nil {
			source += " " + moment(*result.CatalogAt)
		}
		v.facts = []string{"模板目录 · " + source}
		if len(result.Warnings) > 0 {
			v.facts = append(v.facts, strings.Join(result.Warnings, " · "))
		}
		if result.CatalogSource == "unavailable" {
			v.empty = "尚无模板目录。登录后可读取学校数据。"
		}
		for _, pool := range result.Pools {
			v.rows = append(v.rows, link(pool.ID, pool.Name, fmt.Sprintf("%d 个可见模板", len(pool.Templates)), "bupt spec ls", func() {
				w.poolPage(pool, result.CatalogSource)
			}))
		}
		return v
	})
	w.push(p)
}
func (w *workspace) poolPage(initial domain.ResourcePool, source string) {
	build := func(pool domain.ResourcePool, online bool) content {
		list := content{empty: "当前目录中没有此资源池的模板。"}
		for _, t := range pool.Templates {
			title := strings.TrimSpace(t.Name + " " + t.Version)
			summary := resourceSummary(t.Resources)
			cmd := cmdline("spec", "show", string(t.ID))
			list.rows = append(list.rows, link(string(t.ID), title, summary, cmd, func() {
				if !online {
					w.textPage(title, cmd, title+"\n"+summary+"\n模板 ID "+string(t.ID)+"\n缓存目录 · 完整规格需在线读取")
					return
				}
				w.specPage(domain.Spec{ID: t.ID, Name: t.Name, Version: t.Version})
			}))
		}
		return list
	}
	p := queryPage(w, initial.Name, "bupt spec ls", "resources.overview", map[string]any{"cached": w.Cached}, false, func(result domain.ResourceOverview) content {
		for _, pool := range result.Pools {
			if pool.ID == initial.ID {
				return build(pool, !w.Cached && result.CatalogSource == "live")
			}
		}
		return content{empty: "当前目录中没有此资源池。"}
	})
	p.build = func() content { return build(initial, !w.Cached && source == "live") }
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
	p := queryPage(w, s.Name, cmd, "spec.show", map[string]any{"target": s.ID}, true, func(s domain.Spec) content {
		v := content{facts: []string{strings.TrimSpace(s.Name+" "+s.Version) + "   ·   " + value(s.Options["poolDisplayName"]), resourceSummary(object(s.Options["resourceSummary"]))}}
		v.rows = []row{link("create", "用此模板创建…", "下一步填写名称和资源", cmdline("instance", "create", "NAME", "--spec-id", string(s.ID)), func() { w.createForm(s, nil) }),
			link("estimate", "估算模板默认价格", "读取平台估价", cmdline("estimate", "--spec-id", string(s.ID)), func() { w.estimate(s.ID, nil) })}
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
			v.rows = append(v.rows, link("desc", "说明", s.Description, cmd, func() { w.textPage("模板说明", cmd, s.Description) }))
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
func (w *workspace) estimate(id domain.ID, preset *domain.Preset) {
	params := map[string]any{"spec_id": id}
	cmd := cmdline("estimate", "--spec-id", string(id))
	if preset != nil {
		params["preset"] = preset.Name
		params["preset_snapshot"] = preset
		cmd = cmdline("estimate", "--preset", preset.Name)
	}
	w.push(queryPage(w, "费用估算", cmd, "spec.estimate", params, true, func(result map[string]any) content {
		v := content{facts: []string{"平台返回的估价，单位以字段为准。"}, empty: "平台未返回价格。"}
		for _, k := range orderedKeys(result) {
			name := k
			if k == "points_per_day" {
				name = "积分 / 天"
			}
			v.rows = append(v.rows, info(name, value(result[k])))
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
			v.rows = append(v.rows, link(name, title, policyText(p.Policy), cmdline("preset", "show", name), func() {
				w.local(name, cmdline("preset", "show", name), func() content {
					p, exists := w.Config.Presets[name]
					if !exists {
						return content{empty: "此预设已移除。"}
					}
					c := content{facts: []string{"模板 " + string(p.SpecID), policyText(p.Policy)}}
					if !w.Cached {
						c.rows = append(c.rows, link("create", "按预设创建…", "预览后提交", cmdline("instance", "create", "NAME", "--preset", name), func() { w.createForm(domain.Spec{ID: p.SpecID}, &p) }),
							link("estimate", "估算费用", "读取预设资源的价格", cmdline("estimate", "--preset", name), func() { w.estimate(p.SpecID, &p) }))
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
