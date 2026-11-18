package platform

import (
	"context"
	"net/mail"
	"strconv"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/domain"
)

// Only this small, identity-bound projection is cached. The meter endpoint also
// returns administrative/account fields; none of those are retained or exposed.
type identityContactRecord struct {
	UID       domain.ID
	EID       domain.ID
	Email     string
	ExpiresAt time.Time
}
type identityContactCache struct {
	mu      sync.Mutex
	records map[string]identityContactRecord
}

func contactKey(i domain.Identity) string { return string(i.UID) + "/" + string(i.EID) }
func validIdentityID(id domain.ID) bool {
	n, e := strconv.ParseUint(string(id), 10, 64)
	return e == nil && n > 0 && strconv.FormatUint(n, 10) == string(id)
}
func validContactEmail(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 254 || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	parsed, e := mail.ParseAddress(value)
	if e != nil || parsed.Address != value {
		return ""
	}
	return value
}
func (c *Client) cachedIdentityContact(i domain.Identity) domain.Identity {
	if i.Email != "" || c.contact == nil || !validIdentityID(i.UID) || !validIdentityID(i.EID) {
		return i
	}
	c.contact.mu.Lock()
	record, ok := c.contact.records[contactKey(i)]
	c.contact.mu.Unlock()
	if ok && record.UID == i.UID && record.EID == i.EID && record.ExpiresAt.After(time.Now()) {
		i.Email = record.Email
	}
	return i
}

// Metadata lookup is deliberately only called by Sync after authenticating and
// binding the actor's UID/EID. Optional HTTP failures never replace primary auth.
func (c *Client) enrichIdentityContact(ctx context.Context, i domain.Identity) (domain.Identity, error) {
	if i.Email != "" || !validIdentityID(i.UID) || !validIdentityID(i.EID) {
		return i, nil
	}
	if c.profile.ExpectedUID != "" && c.profile.ExpectedUID != i.UID {
		return i, domain.E("IDENTITY_MISMATCH", "metadata identity differs from expected_uid")
	}
	if c.contact == nil {
		return i, nil
	}
	c.contact.mu.Lock()
	defer c.contact.mu.Unlock()
	if c.contact.records == nil {
		c.contact.records = map[string]identityContactRecord{}
	}
	key := contactKey(i)
	if cached, ok := c.contact.records[key]; ok && cached.UID == i.UID && cached.EID == i.EID && cached.ExpiresAt.After(time.Now()) {
		i.Email = cached.Email
		return i, nil
	}
	// The native frontend's vn.getUserByUserEid is GET /api/meter/user/{EID},
	// and its AL saga takes contact information from response.data.user.email.
	var response struct {
		ErrCode *int `json:"errCode"`
		Data    struct {
			User *struct {
				UID   domain.ID `json:"uid"`
				EID   domain.ID `json:"eid"`
				Email string    `json:"email"`
			} `json:"user"`
		} `json:"data"`
	}
	bounded, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	e := c.jsonRequest(bounded, "GET", "/api/meter/user/"+string(i.EID), nil, &response)
	record := identityContactRecord{UID: i.UID, EID: i.EID, ExpiresAt: time.Now().Add(15 * time.Second)}
	if e != nil || response.ErrCode == nil || *response.ErrCode != 0 || response.Data.User == nil {
		c.contact.records[key] = record
		return i, nil
	}
	user := response.Data.User
	if user.UID == "" {
		c.contact.records[key] = record
		return i, nil
	}
	if user.UID != i.UID || (user.EID != "" && user.EID != "0" && user.EID != i.EID) {
		return i, domain.E("IDENTITY_METADATA_MISMATCH", "current-user metadata did not match the authenticated UID/EID")
	}
	c.mu.Lock()
	current := c.identity
	c.mu.Unlock()
	if current.UID != i.UID || (current.EID != "" && current.EID != i.EID) {
		return i, domain.E("IDENTITY_CONTEXT_CHANGED", "authenticated identity changed while loading contact information")
	}
	record.Email = validContactEmail(user.Email)
	if record.Email != "" {
		record.ExpiresAt = time.Now().Add(5 * time.Minute)
	}
	c.contact.records[key] = record
	i.Email = record.Email
	return i, nil
}

func (g graph) bindIdentity(auth domain.Identity) (domain.Identity, error) {
	var own domain.ID
	for _, user := range g.entities["User"] {
		if id(user["uid"]) != auth.UID {
			continue
		}
		eid := id(user["eid"])
		if !validIdentityID(eid) {
			return auth, domain.E("IDENTITY_CONTEXT_CHANGED", "current user actor has an invalid entity ID")
		}
		if own != "" && own != eid {
			return auth, domain.E("IDENTITY_CONTEXT_CHANGED", "multiple actor identities matched the authenticated UID")
		}
		own = eid
	}
	if own == "" && len(g.entities["User"]) > 0 {
		return auth, domain.E("IDENTITY_CONTEXT_CHANGED", "synchronized user actors do not match the authenticated UID")
	}
	if own != "" {
		if auth.EID != "" && auth.EID != own {
			return auth, domain.E("IDENTITY_CONTEXT_CHANGED", "authenticated entity ID differs from the synchronized user actor")
		}
		auth.EID = own
	}
	return auth, nil
}
