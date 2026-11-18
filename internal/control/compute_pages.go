package control

import (
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"context"
	"encoding/json"
	"strings"
	"time"
)

func (c *Controller) computeRead(ctx context.Context, profile string, p Params) (any, error) {
	section := p.String("section")
	if strings.HasPrefix(section, "admin") && p.Bool("cached") {
		return nil, domain.E("NO_CACHE", "后台操作需要当前权限核验。")
	}
	if section == "admin.request" {
		a, e := c.api(profile)
		if e != nil {
			return nil, e
		}
		s, e := c.snapshot(ctx, profile)
		if e != nil {
			return nil, e
		}
		checker, ok := a.(interface {
			VerifyComputeAdmin(context.Context, domain.Identity) error
		})
		if !ok {
			return nil, domain.E("ADMIN_SCOPE_REQUIRED", "需要学校管理员授权。")
		}
		if e = checker.VerifyComputeAdmin(ctx, s.Identity); e != nil {
			return nil, e
		}
		return s.Renewals, nil
	}
	if section == "project" || section == "project.show" || section == "request" {
		var s domain.Snapshot
		var e error
		if p.Bool("cached") {
			e = c.Store.Get("snapshots", profile, &s)
		} else {
			s, e = c.snapshot(ctx, profile)
		}
		if e != nil {
			return nil, e
		}
		if section == "project" {
			return s.Identity.Memberships, nil
		}
		if section == "project.show" {
			id := p.ID("target")
			for _, role := range s.Identity.Memberships {
				if id != role.GroupID {
					continue
				}
				var instances []domain.Instance
				for _, i := range s.Instances {
					if i.GroupID == id {
						instances = append(instances, i)
					}
				}
				return map[string]any{"membership": role, "instances": instances, "members": s.ProjectMembers[string(id)]}, nil
			}
			return nil, domain.E("FORBIDDEN", "当前账号没有该项目的已验证成员记录。")
		}
		own := map[domain.ID]bool{}
		for _, v := range s.Instances {
			if v.UserID == s.Identity.UID || v.OwnerID == s.Identity.UID {
				own[v.ID] = true
			}
		}
		requests := []domain.Renewal{}
		for _, v := range s.Renewals {
			if v.OwnerID == s.Identity.UID || v.OwnerID == "" && own[v.TargetID] {
				requests = append(requests, v)
			}
		}
		return requests, nil
	}
	if section == "capabilities" {
		f, e := c.computeFeatures(ctx, profile, !p.Bool("cached"), p.Bool("refresh"))
		if f == nil && e == nil {
			return nil, domain.E("NO_CACHE", "尚无学校功能配置快照。")
		}
		return f, e
	}
	if p.Bool("cached") {
		return nil, domain.E("NO_CACHE", "此栏目需要学校实时数据。")
	}
	a, e := c.api(profile)
	if e != nil {
		return nil, e
	}
	if native, ok := a.(*platform.Client); ok {
		if !strings.HasPrefix(section, "admin") {
			feature := ""
			switch {
			case section == "cluster" || section == "partition" || section == "cluster.show" || section == "job" || strings.HasPrefix(section, "job."):
				feature = "hpc"
			case strings.HasPrefix(section, "llm."):
				feature = "aiModel"
			case strings.HasPrefix(section, "remote."):
				feature = "remoteApp"
			case strings.HasPrefix(section, "billing"):
				feature = "billing"
			}
			if feature != "" {
				flags, _ := c.computeFeatures(ctx, profile, true, false)
				if enabled, known := flags[feature]; known && !enabled {
					return nil, domain.E("FEATURE_DISABLED", "学校配置未启用该栏目。")
				}
			}
		}
		switch section {
		case "remote.catalog.show", "remote.instance.show", "remote.order.show", "notice.show", "dataset.show", "job.show", "job.template.show", "job.request.show", "cluster.show", "partition":
			return native.SchedulerDetail(ctx, section, p.String("cluster"), p.String("target"))
		case "job.log":
			return native.SchedulerLog(ctx, p.String("cluster"), p.String("target"), p.String("stream"))
		case "admin.limits":
			return native.AdminLimits(ctx, p.String("target"))
		}

	}
	service, ok := a.(interface {
		ComputePage(context.Context, string, int, int, string) (platform.ComputePage, error)
	})
	if !ok {
		return nil, domain.E("UNSUPPORTED", "控制器缺少算力页面适配器。")
	}
	page, limit := int(p.Int("page")), int(p.Int("limit"))
	if !p.Has("page") {
		page = 1
	}
	if !p.Has("limit") {
		limit = 25
	}
	return service.ComputePage(ctx, section, page, limit, p.String("group_id"))
}

func (c *Controller) computeFeatures(ctx context.Context, profile string, online, refresh bool) (map[string]bool, error) {
	ns := c.portalNamespace(profile) + "features"
	type cached struct {
		Flags map[string]bool
		At    time.Time
	}
	if c.Vault == nil {
		return nil, nil
	}
	defer c.lockLogin("features/" + profile)()
	var prev cached
	e := c.Vault.Get(ns, &prev)
	if !online || e == nil && !refresh && time.Since(prev.At) < time.Hour {
		return prev.Flags, nil
	}
	a, e := c.api(profile)
	if e != nil {
		return nil, e
	}
	source, ok := a.(interface {
		ComputeCapabilities(context.Context) (map[string]bool, error)
	})
	if !ok {
		return nil, nil
	}
	flags, e := source.ComputeCapabilities(ctx)
	if e != nil {
		return prev.Flags, e
	}
	if e = c.Vault.Put(ns, cached{Flags: flags, At: time.Now().UTC()}); e != nil {
		return nil, e
	}
	return flags, nil
}
func (c *Controller) adminQuota(ctx context.Context, profile string, p Params) (any, error) {
	defer c.lockLogin(profile)()
	a, e := c.api(profile)
	if e != nil {
		return nil, e
	}
	native, ok := a.(*platform.Client)
	if !ok {
		return nil, domain.E("UNSUPPORTED", "控制器没有管理员配额接口。")
	}
	v, e := native.UpdateAdminLimit(ctx, p.String("cluster"), p.String("account"), p.String("field"), json.Number(p.String("value")), p.String("revision"), p.Bool("dry_run"))
	if e == nil && !p.Bool("dry_run") {
		c.Audit("compute.admin.quota", profile, p.String("account"), "", "", "管理员已更新选定的集群限额。")
	}
	return v, e
}
