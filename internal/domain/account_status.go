package domain

import (
	"strings"
	"time"
)

// CampusNumber returns a numeric school identifier, preserving leading zeros.
// Other subjects, including temporary portal accounts, have no campus number.
func CampusNumber(subject string) string {
	number := strings.TrimPrefix(subject, "cas_")
	if strings.Trim(number, "0123456789") != "" {
		return ""
	}
	return number
}

// AccountStatus is the controller's minimal per-profile display projection.
type AccountStatus struct {
	LoginRevision     uint64    `json:"login_revision,omitempty"`
	ConsentRequired   bool      `json:"consent_required,omitempty"`
	RunningValidUntil time.Time `json:"running_valid_until,omitzero"`
	ValidUntil        time.Time `json:"valid_until,omitzero"`
	Epoch             string    `json:"epoch"`
	Revision          uint64    `json:"revision"`
	Profile           string    `json:"profile"`
	VPN               string    `json:"vpn"`
	Platform          string    `json:"platform"`
	Name              string    `json:"name,omitempty"`
	CampusID          string    `json:"campus_id,omitempty"`
	Balance           *float64  `json:"balance,omitempty"`
	Running           *int      `json:"running,omitempty"`
}

// AccountObservation contains verified facts from a single passive sample.
type AccountObservation struct {
	ConsentRequired                       bool
	VPN, Platform, Reason, Name, CampusID string
	Balance                               *float64
	RetryAfter                            time.Duration
}

// NextExpiry is the next time any displayed evidence can change without input.
func (s AccountStatus) NextExpiry() time.Time {
	if !s.RunningValidUntil.IsZero() && (s.ValidUntil.IsZero() || s.RunningValidUntil.Before(s.ValidUntil)) {
		return s.RunningValidUntil
	}
	return s.ValidUntil
}

// Effective also handles a suspended or terminated controller. Expired evidence
// is never affirmative, even when a file reader missed its invalidation.
func (s AccountStatus) Effective(now time.Time) AccountStatus {
	if !s.RunningValidUntil.IsZero() && !now.Before(s.RunningValidUntil) {
		s.Running = nil
		s.RunningValidUntil = time.Time{}
	}
	if !s.ValidUntil.IsZero() && !now.Before(s.ValidUntil) {
		if s.VPN == "authenticated" {
			s.VPN = "unknown"
		}
		if s.Platform == "authenticated" {
			s.Platform = "unknown"
		}
		s.Name, s.CampusID, s.Balance, s.Running = "", "", nil, nil
		s.ConsentRequired = false
		s.ValidUntil = time.Time{}
		s.RunningValidUntil = time.Time{}
	}
	return s
}
