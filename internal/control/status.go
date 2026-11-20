package control

import (
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"context"
	"time"
)

type observedAuth struct {
	Gateway, Platform, Reason string
	At                        time.Time
}

func (c *Controller) Status(ctx context.Context, profile string, p Params) (domain.StatusReport, error) {
	result := domain.StatusReport{Profile: profile, Controller: "running", Gateway: "not_checked", Platform: "not_checked", CheckedAt: time.Now().UTC(), Source: "cached", Instances: []domain.Instance{}, Runs: []domain.Run{}, Routes: []domain.Route{}}
	result.ComputeFeatures, _ = c.computeFeatures(ctx, profile, p.Bool("compute") && !p.Bool("cached"), false)
	result.API.State = "not_checked"
	if c.Vault != nil {
		if i, err := c.portalIndex(c.portalNamespace(profile)); err == nil && i.Summary.State != "" {
			result.API = i.Summary
		}
	}
	var snapshot domain.Snapshot
	result.SnapshotAvailable = c.Store.Get("snapshots", profile, &snapshot) == nil
	authKey := c.portalNamespace(profile) + "compute-status"
	if p.Bool("cached") && c.Vault != nil {
		var last observedAuth
		if c.Vault.Get(authKey, &last) == nil {
			result.Gateway = last.Gateway
			result.Platform = last.Platform
			result.AuthReason = last.Reason
			result.CheckedAt = last.At
		}
	}
	if !p.Bool("cached") {
		ticket := c.accountTicket(profile)
		api, e := c.api(profile)
		if e != nil {
			return result, e
		}
		probe, ok := api.(interface {
			ProbeSession(context.Context, string) (platform.SessionProbe, error)
		})
		if ok {
			auth, e := probe.ProbeSession(ctx, "")
			if e != nil {
				result.AuthReason = managedCode(e)
			} else {
				result.Gateway = auth.Gateway.State
				result.Platform = auth.Platform.State
				if result.Platform != "authenticated" {
					result.AuthReason = auth.Platform.Reason
					if auth.Gateway.State == "expired" {
						result.AuthReason = auth.Gateway.Reason
					}
				}
			}
		} else {
			auth := api.AuthStatus(ctx)
			result.Gateway = auth.Gateway
			result.Platform = auth.Platform
			result.AuthReason = auth.RequiresAction
		}
		if result.Gateway == "expired" {
			ticket.observe(domain.AccountObservation{VPN: "expired"}, false)
		}
		if c.Vault != nil {
			_ = c.Vault.Put(authKey, observedAuth{result.Gateway, result.Platform, result.AuthReason, result.CheckedAt})
		}
		if result.Platform == "authenticated" {
			// Status never logs in. A stale session falls back to its last known view.
			fresh, e := api.Sync(ctx)
			if e != nil {
				result.Warnings = append(result.Warnings, managedCode(e))
			} else {
				snapshot = fresh
				result.Source = "live"
				result.SnapshotAvailable = true
				_ = c.Store.Put("snapshots", profile, fresh)
				ticket.instances(fresh)
			}
		}
	}
	result.Capacity = domain.CapacityFromQuota(snapshot.Quota)
	if p.String("target") == "" {
		var cache catalogSnapshot
		if c.Store.Get("resource_catalogs", profile, &cache) == nil {
			summary := domain.CatalogCounts{Pools: len(cache.Pools), ObservedAt: cache.At}
			for _, pool := range cache.Pools {
				summary.Templates += len(pool.Templates)
			}
			result.Catalog = &summary
		}
	}
	if snapshot.Instances != nil {
		result.Instances = snapshot.Instances
	}
	runs, e := c.Store.ListRuns(profile)
	if e != nil {
		return result, e
	}
	result.Runs = runs
	routes, e := c.Store.ListRoutes(profile)
	if e != nil {
		return result, e
	}
	result.Routes = routes
	tasks, e := c.Store.ListTasks(profile)
	if e != nil {
		return result, e
	}
	for _, task := range tasks {
		if !terminalTask(task.State) {
			result.PendingTasks++
		}
	}
	if target := p.String("target"); target != "" {
		ids := map[domain.ID]bool{}
		runIDs := map[string]bool{}
		exactRun := false
		for _, i := range result.Instances {
			if string(i.ID) == target || i.Name == target {
				ids[i.ID] = true
			}
		}
		for _, r := range result.Runs {
			if r.ID == target {
				exactRun = true
			}
			if r.ID == target || r.Name == target || string(r.InstanceID) == target {
				runIDs[r.ID] = true
				if r.InstanceID != "" {
					ids[r.InstanceID] = true
				}
			}
		}
		for _, r := range result.Routes {
			if string(r.InstanceID) == target || r.Name == target {
				ids[r.InstanceID] = true
			}
		}
		if len(ids) > 1 {
			return result, domain.E("AMBIGUOUS_TARGET", "name refers to several instances; use an ID")
		}
		if len(ids) == 0 && len(runIDs) == 0 {
			return result, domain.E("NOT_FOUND", "target not found in this profile")
		}
		instances := []domain.Instance{}
		for _, i := range result.Instances {
			if ids[i.ID] {
				instances = append(instances, i)
			}
		}
		result.Instances = instances
		filteredRuns := []domain.Run{}
		for _, r := range result.Runs {
			if (exactRun && r.ID == target) || (!exactRun && (ids[r.InstanceID] || runIDs[r.ID])) {
				filteredRuns = append(filteredRuns, r)
			}
		}
		result.Runs = filteredRuns
		filteredRoutes := []domain.Route{}
		for _, r := range result.Routes {
			if ids[r.InstanceID] {
				filteredRoutes = append(filteredRoutes, r)
			}
		}
		result.Routes = filteredRoutes
		result.PendingTasks = 0
		scopedTasks := map[string]bool{}
		for _, run := range filteredRuns {
			scopedTasks[run.LastTaskID] = true
			scopedTasks[run.RenewalTaskID] = true
		}
		for _, task := range tasks {
			if !terminalTask(task.State) && (scopedTasks[task.ID] || !exactRun && ids[task.Request.TargetID]) {
				result.PendingTasks++
			}
		}

	}
	for idx := range result.Instances {
		result.Instances[idx].SSHState = instanceSSHState(result.Instances[idx], result.Routes)
		for _, r := range result.Runs {
			if r.InstanceID == result.Instances[idx].ID && !managedRunTerminal(r.State) {
				result.Instances[idx].Managed = true
				result.Instances[idx].RunID = r.ID
			}
		}
	}
	return result, nil
}
