package control

import (
	"context"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
)

func numericSpecSelector(s string) bool {
	n, e := strconv.ParseUint(s, 10, 64)
	return e == nil && n > 0
}

// ResolveSpec accepts a concrete ID or an exact unambiguous name[@version].
// Names are resolved before persisting an allocation intent, never on a retry.
func (c *Controller) ResolveSpec(ctx context.Context, profile, selector string) (domain.Spec, error) {
	if selector == "" {
		return domain.Spec{}, domain.E("SPEC_REQUIRED", "select a template with --spec or a preset")
	}
	api, e := c.api(profile)
	if e != nil {
		return domain.Spec{}, e
	}
	if numericSpecSelector(selector) {
		return api.Spec(ctx, domain.ID(selector))
	}
	catalog, e := api.Specs(ctx)
	if e != nil {
		return domain.Spec{}, e
	}
	matches := []domain.Spec{}
	for _, s := range catalog {
		if s.Name == selector {
			matches = append(matches, s)
		}
	}
	if len(matches) == 0 {
		if name, version, ok := strings.Cut(selector, "@"); ok {
			for _, s := range catalog {
				if s.Name == name && s.Version == version {
					matches = append(matches, s)
				}
			}
		}
	}
	if len(matches) == 0 {
		return domain.Spec{}, domain.E("SPEC_NOT_FOUND", "template name was not found; use bupt spec ls")
	}
	if len(matches) != 1 {
		return domain.Spec{}, domain.E("AMBIGUOUS_SPEC", "template name matches multiple versions; use NAME@VERSION or an ID")
	}
	return api.Spec(ctx, matches[0].ID)
}
