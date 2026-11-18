package control

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
)

type catalogSnapshot struct {
	At    time.Time
	Pools []domain.ResourcePool
}

// Resources puts personal quota and visible template pools together. The
// catalogue is fetched only when requested and has its own freshness marker.
func (c *Controller) Resources(ctx context.Context, profile string, p Params) (domain.ResourceOverview, error) {
	status, e := c.Status(ctx, profile, Params{"cached": domain.Result(p.Bool("cached"))})
	if e != nil {
		return domain.ResourceOverview{}, e
	}
	result := domain.ResourceOverview{Profile: profile, Capacity: status.Capacity, QuotaSource: status.Source, CatalogSource: "unavailable", Warnings: status.Warnings}
	if !status.SnapshotAvailable {
		result.QuotaSource = "unavailable"
	}
	var cache catalogSnapshot
	err := c.Store.Get("resource_catalogs", profile, &cache)
	if err == nil {
		result.CatalogSource = "cached"
	} else if !state.IsNotFound(err) {
		return result, err
	}
	if !p.Bool("cached") {
		if status.Platform == "authenticated" {
			api, e := c.api(profile)
			if e != nil {
				return result, e
			}
			specs, e := api.Specs(ctx)
			if e != nil {
				result.Warnings = append(result.Warnings, managedCode(e))
			} else {
				cache = catalogSnapshot{At: time.Now().UTC(), Pools: groupSpecs(specs)}
				result.CatalogSource = "live"
				if e = c.Store.Put("resource_catalogs", profile, cache); e != nil {
					result.Warnings = append(result.Warnings, "CATALOG_CACHE_WRITE_FAILED")
				}
			}
		} else {
			reason := status.AuthReason
			if reason == "" {
				reason = status.Platform
			}
			result.Warnings = append(result.Warnings, reason)
		}
	}
	result.Pools = cache.Pools
	if !cache.At.IsZero() {
		result.CatalogAt = &cache.At
	}
	return result, nil
}
func groupSpecs(specs []domain.Spec) []domain.ResourcePool {
	pools := map[string]*domain.ResourcePool{}
	for _, s := range specs {
		if disabled, ok := s.Options["disabled"].(bool); ok && disabled {
			continue
		}
		id, _ := s.Options["poolName"].(string)
		name, _ := s.Options["poolDisplayName"].(string)
		if id == "" {
			id = name
		}
		if name == "" {
			name = id
		}
		if name == "" {
			name = "未标注资源池"
		}
		pool := pools[id]
		if pool == nil {
			pool = &domain.ResourcePool{ID: id, Name: name}
			pools[id] = pool
		}
		summary, _ := s.Options["resourceSummary"].(map[string]any)
		if raw, ok := s.Options["resourceSummary"].(string); ok {
			d := json.NewDecoder(strings.NewReader(raw))
			d.UseNumber()
			_ = d.Decode(&summary)
		}
		values := map[string]any{}
		for _, k := range []string{"cpu", "gpu", "memory", "blockStorage"} {
			if v, ok := summary[k]; ok {
				values[k] = v
			}
		}
		var auto *bool
		if yes, ok := s.Options["autoApprove"].(bool); ok {
			auto = &yes
		}
		pool.Templates = append(pool.Templates, domain.TemplateSummary{AutoApprove: auto, ID: s.ID, Name: s.Name, Version: s.Version, Resources: values})
	}
	out := make([]domain.ResourcePool, 0, len(pools))
	for _, pool := range pools {
		slices.SortFunc(pool.Templates, func(a, b domain.TemplateSummary) int {
			if n := strings.Compare(a.Name, b.Name); n != 0 {
				return n
			}
			if n := strings.Compare(a.Version, b.Version); n != 0 {
				return n
			}
			return strings.Compare(string(a.ID), string(b.ID))
		})
		out = append(out, *pool)
	}
	slices.SortFunc(out, func(a, b domain.ResourcePool) int {
		if n := strings.Compare(a.Name, b.Name); n != 0 {
			return n
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}
