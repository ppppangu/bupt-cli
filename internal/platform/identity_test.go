package platform

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

func TestIdentityContactUsesOnlyOwnEIDAndCachesWhitelist(t *testing.T) {
	var mu sync.Mutex
	uid, eid := "7", "70"
	lookups := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/auth/user":
			writeJSON(w, map[string]any{"uid": uid, "eid": eid, "uname": "student"})
		case "/api/meter/user/70":
			lookups++
			writeJSON(w, map[string]any{"errCode": 0, "data": map[string]any{"user": map[string]any{"uid": 7, "eid": 70, "email": "student@example.edu", "passwordSha256": "PRIVATE_HASH", "token": "PRIVATE_TOKEN", "telephone": "PRIVATE_PHONE", "groups": []int{999}}, "users": []any{map[string]any{"uid": 99, "email": "other@example.edu"}}}})
		default:
			t.Error("unexpected account request", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c := testClient(t, s)
	c.profile.ExpectedUID = "7"
	auth, e := c.WhoAmI(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	enriched, e := c.enrichIdentityContact(context.Background(), auth)
	if e != nil || enriched.Email != "student@example.edu" {
		t.Fatal(enriched, e)
	}
	again, e := c.WhoAmI(context.Background())
	if e != nil || again.Email != "student@example.edu" || len(again.Groups) != 0 {
		t.Fatal("cached contact was not bound to the current identity", again, e)
	}
	_, e = c.enrichIdentityContact(context.Background(), auth)
	if e != nil {
		t.Fatal(e)
	}
	c.contact.mu.Lock()
	cache, _ := json.Marshal(c.contact.records)
	c.contact.mu.Unlock()
	if strings.Contains(string(cache), "PRIVATE_") || strings.Contains(string(cache), "other@example.edu") {
		t.Fatal("unrelated user data entered contact cache")
	}
	mu.Lock()
	if lookups != 1 {
		t.Error("contact query was repeated", lookups)
	}
	eid = "71"
	mu.Unlock()
	changed, e := c.WhoAmI(context.Background())
	if e != nil || changed.Email != "" {
		t.Fatal("contact crossed entity IDs", changed, e)
	}
	mu.Lock()
	uid = "8"
	mu.Unlock()
	_, e = c.WhoAmI(context.Background())
	assertCode(t, e, "IDENTITY_MISMATCH")
	mu.Lock()
	defer mu.Unlock()
	if lookups != 1 {
		t.Fatal("UID mismatch caused an account lookup")
	}
}

func TestContactMetadataFailuresAreOptionalButIdentityMismatchIsRejected(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		user   map[string]any
		want   string
	}{{"missing", 404, nil, ""}, {"unbound", 200, map[string]any{"email": "unverified@example.edu"}, ""}, {"different_uid", 200, map[string]any{"uid": 8, "email": "other@example.edu"}, "IDENTITY_METADATA_MISMATCH"}, {"different_eid", 200, map[string]any{"uid": 7, "eid": 71, "email": "other@example.edu"}, "IDENTITY_METADATA_MISMATCH"}, {"invalid_email", 200, map[string]any{"uid": 7, "email": "bad\r\ncontact"}, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			lookups := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth/user" {
					writeJSON(w, map[string]any{"uid": 7, "eid": 70})
					return
				}
				if r.URL.Path != "/api/meter/user/70" {
					t.Error("wrong metadata target")
					w.WriteHeader(404)
					return
				}
				mu.Lock()
				lookups++
				mu.Unlock()
				if tc.status != 200 {
					w.WriteHeader(tc.status)
					return
				}
				writeJSON(w, map[string]any{"errCode": 0, "data": map[string]any{"user": tc.user}})
			}))
			defer s.Close()
			c := testClient(t, s)
			auth, e := c.WhoAmI(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			got, e := c.enrichIdentityContact(context.Background(), auth)
			if tc.want != "" {
				assertCode(t, e, tc.want)
			} else if e != nil {
				t.Fatal(e)
			}
			if got.Email != "" {
				t.Fatal("unverified contact was applied")
			}
			if tc.want == "" {
				_, _ = c.enrichIdentityContact(context.Background(), auth)
				mu.Lock()
				defer mu.Unlock()
				if lookups != 1 {
					t.Fatal("optional metadata failure was not bounded by cache")
				}
			}
		})
	}
}

func TestContactLookupRejectsContextChangeWhileInFlight(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/user" {
			writeJSON(w, map[string]any{"uid": 7, "eid": 70})
			return
		}
		if r.URL.Path != "/api/meter/user/70" {
			t.Error("unexpected lookup")
			w.WriteHeader(404)
			return
		}
		close(entered)
		<-release
		writeJSON(w, map[string]any{"errCode": 0, "data": map[string]any{"user": map[string]any{"uid": 7, "email": "old@example.edu"}}})
	}))
	defer s.Close()
	c := testClient(t, s)
	auth, e := c.WhoAmI(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { _, e := c.enrichIdentityContact(context.Background(), auth); done <- e }()
	<-entered
	c.mu.Lock()
	c.identity = domain.Identity{UID: "8", EID: "80"}
	c.mu.Unlock()
	close(release)
	assertCode(t, <-done, "IDENTITY_CONTEXT_CHANGED")
	c.contact.mu.Lock()
	defer c.contact.mu.Unlock()
	if len(c.contact.records) != 0 {
		t.Fatal("changed identity populated the cache")
	}
}

func TestActorIdentityBindingAndReadonlyMemberships(t *testing.T) {
	v := &view{Entities: map[string]map[domain.ID]map[string]any{"User": {"70": {"eid": "70", "uid": "7", "uname": "student"}, "80": {"eid": "80", "uid": "8", "uname": "other"}}, "UserPrjUserGroupRole": {"101": {"eid": "101", "uid": "7", "gid": "31", "role": "member", "state": "active"}, "102": {"eid": "102", "uid": "7", "gid": "32", "role": "manager", "state": "active"}, "103": {"eid": "103", "uid": "8", "gid": "99", "role": "admin", "state": "active"}, "104": {"eid": "104", "uid": "7", "gid": "0", "role": "admin", "state": "active"}, "105": {"eid": "105", "uid": "7", "gid": "33", "role": "future_unknown", "state": "active"}}}}
	s := &eventSocket{views: map[string]*view{"UserActor": v}, tasks: map[string]domain.TaskResult{}}
	identity := domain.Identity{UID: "7"}
	bound, e := s.graph().bindIdentity(identity)
	if e != nil || bound.EID != "70" {
		t.Fatal(bound, e)
	}
	snapshot := s.snapshot(bound)
	if len(snapshot.Identity.Memberships) != 2 || snapshot.Identity.Memberships[0].GroupID != "31" || snapshot.Identity.Memberships[0].Role != "member" || snapshot.Identity.Memberships[1].Role != "manager" {
		t.Fatal(snapshot.Identity.Memberships)
	}
	if len(snapshot.Identity.Groups) != 0 {
		t.Fatal("membership was promoted into management authority")
	}
	_, e = s.graph().bindIdentity(domain.Identity{UID: "7", EID: "80"})
	assertCode(t, e, "IDENTITY_CONTEXT_CHANGED")
	v.Entities["User"]["71"] = map[string]any{"eid": "71", "uid": "7"}
	_, e = s.graph().bindIdentity(identity)
	assertCode(t, e, "IDENTITY_CONTEXT_CHANGED")
}

func TestPrimaryEmailDoesNotTriggerMetadataAndCacheExpires(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/user" {
			t.Error("primary email caused an unnecessary metadata lookup")
			w.WriteHeader(404)
			return
		}
		writeJSON(w, map[string]any{"uid": 7, "eid": 70, "email": "primary@example.edu"})
	}))
	defer s.Close()
	c := testClient(t, s)
	i, e := c.WhoAmI(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	i, e = c.enrichIdentityContact(context.Background(), i)
	if e != nil || i.Email != "primary@example.edu" {
		t.Fatal(i, e)
	}
	c.contact.mu.Lock()
	c.contact.records["7/70"] = identityContactRecord{UID: "7", EID: "70", Email: "expired@example.edu", ExpiresAt: time.Now().Add(-time.Second)}
	c.contact.mu.Unlock()
	if got := c.cachedIdentityContact(domain.Identity{UID: "7", EID: "70"}); got.Email != "" {
		t.Fatal("expired cache was used")
	}
}
