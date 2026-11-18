package domain

import (
	"encoding/json"
	"time"
)

// These projections are shared by both frontends. A portal page never contains a full API key.
type PortalAccount struct {
	Role            string   `json:"role,omitempty"`
	Admin           *bool    `json:"is_admin,omitempty"`
	Permissions     []string `json:"permissions,omitempty"`
	SID             string   `json:"sid"`
	Name            string   `json:"name,omitempty"`
	College         string   `json:"college,omitempty"`
	Balance         *float64 `json:"balance_points"`
	FreeQuota       *float64 `json:"free_quota_points,omitempty"`
	ConsentRequired bool     `json:"consent_required"`
}

type PortalModel struct {
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Kind        string           `json:"kind,omitempty"`
	Type        string           `json:"type,omitempty"`
	Source      string           `json:"source,omitempty"`
	Vendor      string           `json:"vendor,omitempty"`
	Description string           `json:"description,omitempty"`
	Context     string           `json:"context,omitempty"`
	InputPrice  *float64         `json:"input_points_per_million_tokens"`
	OutputPrice *float64         `json:"output_points_per_million_tokens"`
	Health      string           `json:"health"`
	Coverage    string           `json:"coverage,omitempty"`
	SampledAt   string           `json:"last_sampled_at,omitempty"`
	LatencyMS   *float64         `json:"last_latency_ms,omitempty"`
	History     []map[string]any `json:"history,omitempty"`
}
type PortalKey struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Masked    string   `json:"masked,omitempty"`
	Models    []string `json:"models"`
	Status    string   `json:"status"`
	Used      *float64 `json:"used_points"`
	CreatedAt string   `json:"created_at,omitempty"`
	Stored    bool     `json:"stored_in_controller"`
}
type PortalPage struct {
	Section    string         `json:"section"`
	Source     string         `json:"source"`
	ObservedAt time.Time      `json:"observed_at"`
	Account    *PortalAccount `json:"account,omitempty"`
	Models     []PortalModel  `json:"models,omitempty"`
	Keys       []PortalKey    `json:"keys,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	Warnings   []string       `json:"warnings,omitempty"`
}
type PortalInput struct {
	Section string `json:"section"`
	Target  string `json:"target,omitempty"`
	Days    int    `json:"days,omitempty"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Only    string `json:"only,omitempty"`
	Order   string `json:"order,omitempty"`
}
type PortalChange struct {
	Action string   `json:"action"`
	Target string   `json:"target,omitempty"`
	Name   string   `json:"name,omitempty"`
	Models []string `json:"models,omitempty"`
	Mode   string   `json:"mode,omitempty"`
}
type PortalSummary struct {
	State     string    `json:"state"`
	CheckedAt time.Time `json:"checked_at,omitzero"`
	Balance   *float64  `json:"balance_points,omitempty"`
	Keys      *int      `json:"keys,omitempty"`
	Models    *int      `json:"models,omitempty"`
}

// Keep collection fields present on their own list pages, including empty lists.
func (p PortalPage) MarshalJSON() ([]byte, error) {
	type plain PortalPage
	v := struct {
		plain
		Models *[]PortalModel `json:"models,omitempty"`
		Keys   *[]PortalKey   `json:"keys,omitempty"`
	}{plain: plain(p)}
	if p.Section == "model" {
		items := p.Models
		if items == nil {
			items = []PortalModel{}
		}
		v.Models = &items
	}
	if p.Section == "key" || p.Section == "overview" {
		items := p.Keys
		if items == nil {
			items = []PortalKey{}
		}
		v.Keys = &items
	}
	return json.Marshal(v)
}
