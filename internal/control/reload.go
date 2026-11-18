package control

import (
	"context"
	"sort"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
)

// ReloadConfiguration reads only the controller's trusted global file. Live
// policies remain immutable run snapshots; endpoint changes require idle runs.
func (c *Controller) ReloadConfiguration(ctx context.Context) (any, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	c.intentMu.Lock()
	defer c.intentMu.Unlock()
	old := c.Configuration()
	next, e := config.Load(config.Options{ConfigFile: old.Paths.ConfigFile, ConfigDir: old.Paths.ConfigDir, DataDir: old.Paths.DataDir, StateDir: old.Paths.StateDir, Profile: old.Selected, NoProject: true})
	if e != nil {
		return nil, e
	}
	changed := map[string]bool{}
	for name, p := range old.Profiles {
		if q, ok := next.Profiles[name]; !ok || q != p {
			changed[name] = true
		}
	}
	runs, e := c.Store.ListRuns("")
	if e != nil {
		return nil, e
	}
	for _, r := range runs {
		if changed[r.Profile] && !managedRunTerminal(r.State) {
			return nil, domain.E("PROFILE_BUSY", "a changed profile still has a managed run; finish it before replacing its connection settings")
		}
	}
	tasks, e := c.Store.ListTasks("")
	if e != nil {
		return nil, e
	}
	for _, t := range tasks {
		if changed[t.Profile] && !terminalTask(t.State) {
			return nil, domain.E("PROFILE_BUSY", "a changed profile still has a pending platform task")
		}
	}
	// Complete pending account transactions against their original namespace.
	// Stable ordering also supports replacing multiple profiles at once.
	changedNames := make([]string, 0, len(changed))
	for name := range changed {
		changedNames = append(changedNames, name)
	}
	sort.Strings(changedNames)
	for _, name := range changedNames {
		defer c.lockLogin(name)()
	}
	c.reloadAccounts(next, changed)
	profiles := []string{}
	for name := range next.Profiles {
		profiles = append(profiles, name)
	}
	sort.Strings(profiles)
	c.Audit("config.reloaded", "", "", "", "", "trusted controller configuration reloaded; existing run policies retained")
	return map[string]any{"reloaded": true, "profiles": profiles, "run_policies_preserved": true}, nil
}
