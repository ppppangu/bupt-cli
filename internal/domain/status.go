package domain

import "time"

type CatalogCounts struct {
	Pools      int       `json:"pools"`
	Templates  int       `json:"templates"`
	ObservedAt time.Time `json:"observed_at"`
}

type StatusReport struct {
	ComputeFeatures   map[string]bool `json:"compute_features,omitempty"`
	API               PortalSummary   `json:"api"`
	Catalog           *CatalogCounts  `json:"last_catalog,omitempty"`
	SnapshotAvailable bool            `json:"snapshot_available"`
	Capacity          []Capacity      `json:"capacity,omitempty"`
	Profile           string          `json:"profile"`
	Controller        string          `json:"controller"`
	Gateway           string          `json:"gateway"`
	Platform          string          `json:"platform"`
	AuthReason        string          `json:"auth_reason,omitempty"`
	CheckedAt         time.Time       `json:"checked_at"`
	Source            string          `json:"source"`
	Instances         []Instance      `json:"instances"`
	Runs              []Run           `json:"runs"`
	Routes            []Route         `json:"routes"`
	PendingTasks      int             `json:"pending_tasks"`
	Warnings          []string        `json:"warnings,omitempty"`
}
