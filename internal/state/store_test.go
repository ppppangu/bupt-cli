package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

func errCode(e error) string {
	var de *domain.Error
	if errors.As(e, &de) {
		return de.Code
	}
	return ""
}
func openTest(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "state"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func task(id string) domain.TaskRecord {
	return domain.TaskRecord{ID: id, Profile: "bupt", Operation: "create", Request: domain.TaskRequest{ID: id, Type: "CreateAppTask", UserID: "42", Payload: map[string]any{"appName": "one", "appSpecEid": json.Number("9007199254740993")}}}
}

func TestTaskCreationPersistsAndDeduplicates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	v, created, e := s.CreateTask(task("id-1"))
	if e != nil || !created || v.Hash == "" {
		t.Fatalf("%+v %v %v", v, created, e)
	}
	v.State = "complete"
	v.Result = &domain.TaskResult{ID: v.ID, State: "complete", InstanceID: "123"}
	if e = s.PutTask(v); e != nil {
		t.Fatal(e)
	}
	if e = s.Close(); e != nil {
		t.Fatal(e)
	}
	s, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	v, created, e = s.CreateTask(task("id-1"))
	if e != nil || created || v.State != "complete" || v.Result.InstanceID != "123" {
		t.Fatalf("lost receipt %+v %v %v", v, created, e)
	}
	if n, ok := v.Request.Payload["appSpecEid"].(json.Number); !ok || n.String() != "9007199254740993" {
		t.Fatal("large ID lost precision", v.Request.Payload)
	}
	if e = s.PutTask(v); e != nil {
		t.Fatal("reloaded task changed its hash", e)
	}
	different := task("id-1")
	different.Request.Payload["appName"] = "two"
	if _, _, e = s.CreateTask(different); errCode(e) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal(e)
	}
	if e = s.DeleteTask("id-1"); e != nil {
		t.Fatal(e)
	}
	if _, _, e = s.CreateTask(different); errCode(e) != "IDEMPOTENCY_CONFLICT" {
		t.Fatal("archiving erased idempotency", e)
	}
}

func TestConcurrentSameTaskHasOneCreator(t *testing.T) {
	s := openTest(t)
	var created atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, c, e := s.CreateTask(task("same"))
			if e != nil {
				t.Error(e)
			}
			if c {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatal(created.Load())
	}
}

func TestPortAllocationConcurrentUniqueAndIdempotent(t *testing.T) {
	s := openTest(t)
	var wg sync.WaitGroup
	got := make(chan domain.PortLease, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v, e := s.AllocatePort(domain.PortLease{Key: fmt.Sprintf("key-%02d", i), Owner: "owner", Profile: "bupt", InstanceID: domain.ID(fmt.Sprint(i + 1)), NodeID: "n"}, 24000, 24050, func(p int) bool { return p >= 24003 })
			if e != nil {
				t.Error(e)
				return
			}
			got <- v
		}(i)
	}
	wg.Wait()
	close(got)
	ports := map[int]bool{}
	var one domain.PortLease
	for v := range got {
		if ports[v.Port] {
			t.Fatal("duplicate port", v.Port)
		}
		ports[v.Port] = true
		one = v
	}
	if len(ports) != 40 {
		t.Fatal(len(ports))
	}
	repeat, e := s.AllocatePort(one, 24000, 24050, nil)
	if e != nil || repeat.Port != one.Port {
		t.Fatal(repeat, e)
	}
	bad := one
	bad.Owner = "other"
	if _, e = s.AllocatePort(bad, 24000, 24050, nil); errCode(e) != "LEASE_CONFLICT" {
		t.Fatal(e)
	}
	if e = s.ReleasePort(one.Key); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetPortLease(one.Key); errCode(e) != "NOT_FOUND" {
		t.Fatal(e)
	}
	one.Key = "new"
	newLease, e := s.AllocatePort(one, one.Port, one.Port, nil)
	if e != nil || newLease.Port != one.Port {
		t.Fatal(newLease, e)
	}
}

func TestPortPoolAndLeaseIdentityGuards(t *testing.T) {
	s := openTest(t)
	v := domain.PortLease{Key: "a", Owner: "owner", Profile: "bupt", InstanceID: "i", NodeID: "n"}
	v, e := s.AllocatePort(v, 24000, 24000, nil)
	if e != nil {
		t.Fatal(e)
	}
	w := v
	w.Key = "b"
	if _, e = s.AllocatePort(w, 24000, 24000, nil); errCode(e) != "PORT_POOL_EXHAUSTED" {
		t.Fatal(e)
	}
	v.PublicKey = "ssh-ed25519 public"
	v.State = "ready"
	if e = s.PutPortLease(v); e != nil {
		t.Fatal(e)
	}
	v.Port++
	if e = s.PutPortLease(v); errCode(e) != "LEASE_CONFLICT" {
		t.Fatal(e)
	}
}

func TestRoutesRemainWhenAuthenticationExpires(t *testing.T) {
	s := openTest(t)
	v := domain.Route{Profile: "bupt", InstanceID: "i", NodeID: "n", Host: "127.0.0.1", Port: 24000, State: "ready", VerifiedAt: time.Now()}
	if e := s.PutRoute(v); e != nil {
		t.Fatal(e)
	}
	if e := s.Put("auth_status", "bupt", map[string]string{"state": "expired"}); e != nil {
		t.Fatal(e)
	}
	if e := s.Delete("auth_status", "bupt"); e != nil {
		t.Fatal(e)
	}
	got, e := s.GetRoute("bupt", "i", "")
	if e != nil || got.Port != v.Port || got.State != "ready" {
		t.Fatal(got, e)
	}
	v.NodeID = "another"
	if e = s.PutRoute(v); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetRoute("bupt", "i", ""); errCode(e) != "AMBIGUOUS_NODE" {
		t.Fatal(e)
	}
	if e = s.DeleteRoute("bupt", "i", ""); e != nil {
		t.Fatal(e)
	}
	all, e := s.ListRoutes("bupt")
	if e != nil || len(all) != 0 {
		t.Fatal(all, e)
	}
}

func TestRunUpdateIsAtomicAndKeepsOwnership(t *testing.T) {
	s := openTest(t)
	if e := s.PutRun(domain.Run{ID: "r", Profile: "p", State: "running"}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := s.UpdateRun("r", func(r *domain.Run) error { r.PolicyRevision++; return nil }); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	r, e := s.GetRun("r")
	if e != nil || r.PolicyRevision != 20 {
		t.Fatal(r, e)
	}
	if e = s.UpdateRun("r", func(r *domain.Run) error { r.Profile = "another"; return nil }); errCode(e) != "INVALID_RUN" {
		t.Fatal(e)
	}
}

func TestAuditsWhitelistAndBackup(t *testing.T) {
	s := openTest(t)
	if e := s.AppendAudit(domain.AuditEvent{Kind: "auth.updated", Data: map[string]any{"state": "ready", "password": "must-not-store", "cookie": "never", "user_dump": map[string]any{"passwordSha256": "hidden"}}}); e != nil {
		t.Fatal(e)
	}
	events, e := s.ListAudits(time.Time{}, 100)
	if e != nil || len(events) != 1 {
		t.Fatal(events, e)
	}
	if len(events[0].Data) != 1 || events[0].Data["state"] != "ready" {
		t.Fatal(events[0])
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if e = s.Backup(backup); e != nil {
		t.Fatal(e)
	}
	st, e := os.Stat(backup)
	if e != nil || st.Mode().Perm() != 0o600 || st.Size() == 0 {
		t.Fatal(st, e)
	}
	if e = s.Backup(backup); !errors.Is(e, os.ErrExist) {
		t.Fatal("overwrote backup", e)
	}
}

func TestAuditDiagnosticRedaction(t *testing.T) {
	s := openTest(t)
	message := "password=secret-one token='secret-two'\nAuthorization: Bearer secret-three\nCookie: sid=secret-four; another=secret-five\n-----BEGIN PRIVATE KEY-----\nsecret-six\n-----END PRIVATE KEY-----"
	if e := s.AppendAudit(domain.AuditEvent{Kind: "auth.failed", Message: message, Data: map[string]any{"reason": "passwordSha256=secret-seven"}}); e != nil {
		t.Fatal(e)
	}
	all, e := s.ListAudits(time.Time{}, 1)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(all)
	if strings.Contains(string(data), "secret-") {
		t.Fatal("sensitive diagnostic persisted", string(data))
	}
}

func TestStatePathsAreProtected(t *testing.T) {
	dir := t.TempDir()
	public := filepath.Join(dir, "public")
	if e := os.Mkdir(public, 0o755); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(public); errCode(e) != "INSECURE_PERMISSIONS" {
		t.Fatal(e)
	}
	target := filepath.Join(dir, "target")
	if e := os.Mkdir(target, 0o700); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(dir, "link")
	if e := os.Symlink(target, link); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(link); errCode(e) != "UNSAFE_PATH" {
		t.Fatal(e)
	}
}
