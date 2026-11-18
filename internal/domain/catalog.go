package domain

import "time"

type TemplateSummary struct {
	AutoApprove *bool          `json:"auto_approve,omitempty"`
	ID          ID             `json:"id"`
	Name        string         `json:"name"`
	Version     string         `json:"version,omitempty"`
	Resources   map[string]any `json:"resources,omitempty"`
}
type ResourcePool struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Templates []TemplateSummary `json:"templates"`
}
type ResourceOverview struct {
	Profile       string         `json:"profile"`
	Capacity      []Capacity     `json:"capacity"`
	QuotaSource   string         `json:"quota_source"`
	Pools         []ResourcePool `json:"pools"`
	CatalogSource string         `json:"catalog_source"`
	CatalogAt     *time.Time     `json:"catalog_at,omitempty"`
	Warnings      []string       `json:"warnings,omitempty"`
}
