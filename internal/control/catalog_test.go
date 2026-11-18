package control

import (
	"bupt.local/cli/internal/domain"
	"context"
	"testing"
)

func TestResourceDirectoryKeepsQuotaAndCatalogueFreshnessIndependent(t *testing.T) {
	c, api, _ := lifecycleController(t)
	a := &statusPlatform{lifecyclePlatform: api, authenticated: true, catalog: []domain.Spec{
		{ID: "100", Name: "Ubuntu", Version: "22", Options: map[string]any{"poolName": "gpu", "poolDisplayName": "GPU 池", "resourceSummary": `{"gpu":10,"memory":6144}`}},
		{ID: "101", Name: "Jupyter", Version: "3", Options: map[string]any{"poolName": "gpu", "poolDisplayName": "GPU 池"}}, {ID: "102", Name: "CPU"},
	}}
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return a, nil }
	r, e := c.Resources(context.Background(), "test", Params{})
	if e != nil || r.CatalogSource != "live" || len(r.Pools) != 2 || r.CatalogAt == nil {
		t.Fatal(r, e)
	}
	var count int
	for _, p := range r.Pools {
		if p.ID == "gpu" {
			count = len(p.Templates)
		}
	}
	if count != 2 {
		t.Fatal(r)
	}
	a.authenticated = false
	a.catalog = nil
	r, e = c.Resources(context.Background(), "test", Params{})
	if e != nil || r.QuotaSource != "cached" || r.CatalogSource != "cached" || len(r.Pools) != 2 {
		t.Fatal(r, e)
	}
}
