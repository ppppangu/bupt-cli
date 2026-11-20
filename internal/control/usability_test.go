package control

import (
	"context"
	"errors"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
)

type statusPlatform struct {
	*lifecyclePlatform
	authenticated bool
	catalog       []domain.Spec
}

func (a *statusPlatform) ProbeSession(context.Context, string) (platform.SessionProbe, error) {
	s := "expired"
	if a.authenticated {
		s = "authenticated"
	}
	return platform.SessionProbe{Gateway: platform.SessionLayerProbe{State: s}, Platform: platform.SessionLayerProbe{State: s}}, nil
}
func (a *statusPlatform) Specs(context.Context) ([]domain.Spec, error) { return a.catalog, nil }

func TestStatusUsesCacheWhenLoginExpiresAndNeverSyncs(t *testing.T) {
	c, a, _ := lifecycleController(t)
	r := fixtureActiveRun(t, c, a, false)
	if e := c.Store.Put("snapshots", "test", a.snap); e != nil {
		t.Fatal(e)
	}
	fake := &statusPlatform{lifecyclePlatform: a}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return fake, nil }
	report, e := c.Status(context.Background(), "test", Params{})
	if e != nil {
		t.Fatal(e)
	}
	if report.Source != "cached" || report.Platform != "expired" || len(report.Instances) != 1 || report.Instances[0].RunID != r.ID {
		t.Fatal(report)
	}
	if a.syncCalls != 0 {
		t.Fatal("status tried to refresh/login behind an expired session")
	}
}

func TestWaitStoppedRunCannotReportSSHReady(t *testing.T) {
	c, _, _ := lifecycleController(t)
	r := domain.Run{ID: "ended", Profile: "test", State: "stopped"}
	if e := c.Store.PutRun(r); e != nil {
		t.Fatal(e)
	}
	_, e := c.waitManaged(context.Background(), r.ID, false)
	var de *domain.Error
	if !errors.As(e, &de) || de.Code != "RUN_ENDED" || de.RunID != r.ID {
		t.Fatal(e)
	}
	if _, e = c.waitManaged(context.Background(), r.ID, true); e != nil {
		t.Fatal(e)
	}
}

func TestStoppedInstanceOverridesCachedReadySSHState(t *testing.T) {
	ctx := t.Context()
	c, a, _ := lifecycleController(t)
	fixtureActiveRun(t, c, a, false)
	a.mu.Lock()
	a.snap.Instances[0].State = "stopped"
	a.mu.Unlock()

	instances, err := c.Instances(ctx, "test", false)
	if err != nil || len(instances) != 1 || instances[0].SSHState != "stopped" {
		t.Fatalf("instance list: %+v, %v", instances, err)
	}
	result, err := c.inspectInstance(ctx, "test", lifecycleParams(map[string]any{"target": "20"}))
	if instance, ok := result.(domain.Instance); err != nil || !ok || instance.SSHState != "stopped" {
		t.Fatalf("instance detail: %+v, %v", result, err)
	}
	report, err := c.Status(ctx, "test", lifecycleParams(map[string]any{"cached": true}))
	if err != nil || len(report.Instances) != 1 || report.Instances[0].SSHState != "stopped" {
		t.Fatalf("cached status: %+v, %v", report.Instances, err)
	}
	route, err := c.CachedRoute("test", "20", "40")
	if err != nil || route.State != "ready" {
		t.Fatalf("route metadata: %+v, %v", route, err)
	}
}

func TestWaitActionErrorRetainsDurableRun(t *testing.T) {
	c, _, _ := lifecycleController(t)
	r := domain.Run{ID: "needs-password", Profile: "test", State: "preparing", LastError: "CONSOLE_LOGIN_REQUIRED", Deadline: time.Now().Add(time.Hour)}
	if e := c.Store.PutRun(r); e != nil {
		t.Fatal(e)
	}
	_, e := c.waitManaged(context.Background(), r.ID, false)
	var de *domain.Error
	if !errors.As(e, &de) || de.RunID != r.ID || de.Code != r.LastError {
		t.Fatal(e)
	}
	saved, e := c.Store.GetRun(r.ID)
	if e != nil || saved.State != "preparing" {
		t.Fatal(saved, e)
	}
}

func TestTemplateNameAmbiguityRequiresExplicitVersion(t *testing.T) {
	c, a, _ := lifecycleController(t)
	fake := &statusPlatform{lifecyclePlatform: a, catalog: []domain.Spec{{ID: "9", Name: "Ubuntu", Version: "22.04"}, {ID: "10", Name: "Ubuntu", Version: "24.04"}}}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return fake, nil }
	if _, e := c.ResolveSpec(context.Background(), "test", "Ubuntu"); e == nil {
		t.Fatal("ambiguous template was silently chosen")
	}
	if _, e := c.ResolveSpec(context.Background(), "test", "Ubuntu@24.04"); e != nil {
		t.Fatal(e)
	}
}

func TestRestartReleasedInstanceByLogicalNameDoesNotCreateAnother(t *testing.T) {
	c, a, _ := lifecycleController(t)
	first := fixtureActiveRun(t, c, a, false)
	a.mu.Lock()
	a.snap.Instances[0].State = "stopped"
	a.snap.Instances[0].Reserved = false
	a.mu.Unlock()
	if e := c.Store.UpdateRun(first.ID, func(r *domain.Run) error {
		r.State = "stopped"
		r.Phase = "terminal"
		r.CleanupState = "released"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	next, e := c.prepareManagedRun(context.Background(), "test", "up", lifecycleParams(map[string]any{"name": first.Name, "max_runtime": "15m", "no_ssh": true}), false, nil)
	if e != nil {
		t.Fatal(e)
	}
	if next.ID == first.ID || next.InstanceID != first.InstanceID || next.CreatedInstance {
		t.Fatal(next)
	}
}

func TestJobNameDoesNotReplaceInstanceAlias(t *testing.T) {
	c, a, _ := lifecycleController(t)
	first := fixtureActiveRun(t, c, a, false)
	next, e := c.prepareManagedRun(context.Background(), "test", "up", lifecycleParams(map[string]any{"target": first.Name, "name": "training-job", "command": []string{"true"}, "max_runtime": "1h", "no_ssh": true}), true, nil)
	if e != nil || next.Name != first.Name || next.JobName != "training-job" {
		t.Fatal(next, e)
	}
}

func TestJobCanInheritActiveDeadlineWithoutFreshRuntimeFlag(t *testing.T) {
	c, a, _ := lifecycleController(t)
	first := fixtureActiveRun(t, c, a, false)
	next, e := c.prepareManagedRun(context.Background(), "test", "up", lifecycleParams(map[string]any{"target": first.Name, "command": []string{"true"}, "inherit_policy": true, "no_ssh": true}), true, nil)
	if e != nil || !next.Deadline.Equal(first.Deadline) || next.Policy != first.Policy {
		t.Fatal(next, e)
	}
}
