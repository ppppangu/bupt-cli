package tui

import (
	"fmt"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
)

// The school publishes different writable fields on each personal quota.
// A blank input leaves the existing limit in place; zero is an explicit value.
func (w *workspace) quotaForm(key, title string, quota map[string]any) {
	kind, id, _ := strings.Cut(key, ":")
	limits := map[string]*float64{}
	for _, cap := range domain.CapacityFromQuota(map[string]any{key: quota}) {
		limits[cap.Resource] = cap.Limit
	}
	type field struct{ flag, title, unit, previous, input string }
	var fields []field
	for _, spec := range []struct{ prop, flag, title, unit string }{
		{"gpu", "gpu", "GPU", "卡"}, {"cpu", "cpu", "CPU", "核"}, {"memory", "memory", "内存", "GiB"},
		{"appCount", "instances", "实例", "个"}, {"blockStorage", "disk", "块存储", "GiB"}, {"fileStorage", "file-storage", "文件存储", "GiB"},
	} {
		prop := spec.prop
		if kind == "UserStorageQuota" {
			if spec.flag != "disk" && spec.flag != "file-storage" {
				continue
			}
			prop += "QuotaInMb"
		}
		if _, present := quota[prop]; present {
			fields = append(fields, field{flag: spec.flag, title: spec.title, unit: spec.unit, previous: ntext(limits[spec.title])})
		}
	}
	reason := ""
	pending := false
	w.local("申请调整额度", cmdline("compute", "quota", "request", id), func() content {
		v := content{facts: []string{title + " · 额度 " + id}, empty: "学校未公布可调整的资源。"}
		if len(fields) == 0 || w.Cached {
			return v
		}
		v.facts = append(v.facts, "填写要修改的目标上限，至少一项。")
		args := []string{"compute", "quota", "request"}
		facts := []string{title + " · 额度 " + id}
		for i := range fields {
			f := &fields[i]
			r := w.field(f.flag, f.title+"（"+f.unit+"）", &f.input, "当前上限 "+f.previous+" "+f.unit)
			r.verb = "填写上限"
			v.rows = append(v.rows, r)
			if f.input != "" {
				newValue := f.input
				if f.unit == "GiB" {
					if _, err := strconv.ParseFloat(newValue, 64); err == nil {
						newValue += "GiB"
					}
				}
				args = append(args, "--"+f.flag+"="+newValue)
				target := newValue
				if f.unit != "GiB" {
					target += " " + f.unit
				}
				facts = append(facts, f.title+"　"+f.previous+" "+f.unit+" → "+target)
			}
		}
		hasChange := len(args) > 3
		v.rows = append(v.rows, w.field("reason", "申请理由（必填）", &reason, "例如 课程训练需要更多显存"))
		mode := "收到学校回执后返回"
		if pending {
			mode = "提交后立即返回"
		}
		if pending {
			args = append(args, "--no-wait")
		}
		args = append(args, "--reason="+reason, "--", id)
		goReview := link("go", "核对额度申请", "", cmdline(args...), func() {
			if !hasChange || reason == "" {
				w.notice = "请填写至少一项目标上限和申请理由。"
				return
			}
			w.runForm(args, true, "提交额度申请", append(facts, "申请理由　"+reason)...)
		})
		goReview.verb = "核对申请"
		v.rows = append(v.rows, goReview)
		v.rows = append(v.rows, link("wait", "提交后", mode, "", func() { pending = !pending }))
		return v
	})
}

func (w *workspace) remoteOrderForm(renew bool, id, name, unit string) {
	action, title, target := "create", "新建远程算力订单", "规格"
	if renew {
		action, title, target = "renew", "续期远程实例", "实例"
	}
	units, instance := "1", ""
	command := []string{"compute", "remote", "order", action}
	w.local(title, cmdline(command...), func() content {
		objectName := id
		if name != "" && name != id {
			objectName = name + " · " + id
		}
		v := content{facts: []string{target + "　" + objectName}}
		if w.Cached {
			return v
		}
		args := append([]string{}, command...)
		if !renew {
			v.rows = append(v.rows, w.field("name", "实例名称（必填）", &instance, "输入新实例名称"))
			args = append(args, "--spec="+id)
		}
		count := link("units", "计费周期数", units, "", func() {
			w.edit("计费周期数（正整数）", units, func(s string) error {
				n, err := strconv.Atoi(strings.TrimSpace(s))
				if err != nil || n <= 0 {
					return fmt.Errorf("请填写正整数周期数")
				}
				units = strconv.Itoa(n)
				return nil
			})
		})
		count.verb = "填写周期数"
		v.rows = append(v.rows, count)
		if unit != "" {
			v.facts = append(v.facts, "计费周期单位　"+unit)
		}
		selected := id
		if !renew {
			selected = instance
		}
		args = append(args, "--units="+units, "--", selected)
		r := link("go", "查看学校报价", "核对单价、周期和总额后可确认下单", cmdline(args...), func() {
			if !renew && instance == "" {
				w.notice = "请先填写实例名称。"
				return
			}
			w.runForm(args, true, title)
		})
		r.verb = "查看报价"
		v.rows = append(v.rows, r)
		return v
	})
}
