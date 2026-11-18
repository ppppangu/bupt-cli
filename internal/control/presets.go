package control

import (
	"bupt.local/cli/internal/domain"
	"context"
)

// ResolvePreset validates resource choices without allocating an instance.
func (c *Controller) ResolvePreset(ctx context.Context, profile string, p Params) (domain.Preset, error) {
	var current *domain.Node
	var source domain.Instance
	selector := p.String("spec_id")
	if target := p.String("target"); target != "" {
		snapshot, e := c.snapshot(ctx, profile)
		if e != nil {
			return domain.Preset{}, e
		}
		source, e = Resolve(snapshot, target)
		if e != nil {
			return domain.Preset{}, e
		}
		if e = Owned(snapshot, source); e != nil {
			return domain.Preset{}, e
		}
		if len(source.Nodes) != 1 {
			return domain.Preset{}, domain.E("NODE_LAYOUT_UNSUPPORTED", "saving an instance preset requires one node; use a template and exact constraints for multi-node instances")
		}
		current = &source.Nodes[0]
		if selector == "" {
			selector = string(source.SpecID)
		}
	}
	spec, e := c.ResolveSpec(ctx, profile, selector)
	if e != nil {
		return domain.Preset{}, e
	}
	exact, e := requestedResourceMap(p, "constraints")
	if e != nil {
		return domain.Preset{}, e
	}
	resources, e := requestedResourceMap(p, "resources")
	if e != nil {
		return domain.Preset{}, e
	}
	constraints, e := resourcesForSpec(spec, exact, resources, current)
	if e != nil {
		return domain.Preset{}, e
	}
	preset := domain.Preset{SpecID: spec.ID, Constraints: constraints, Domain: scalarResource(spec.Options["domain"]), GroupID: source.GroupID}
	if p.Has("group_id") {
		preset.GroupID = p.ID("group_id")
	}
	if p.Has("bill_id") {
		preset.BillID = p.ID("bill_id")
	}
	if p.Has("domain") {
		preset.Domain = p.String("domain")
	}
	preset.Usage = p.String("usage")
	return preset, nil
}
