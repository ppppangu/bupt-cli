// Package state stores controller state independently from platform authentication.
package state

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	bolt "go.etcd.io/bbolt"
)

const (
	runsBucket   = "runs"
	tasksBucket  = "tasks"
	routesBucket = "routes"
	leasesBucket = "port_leases"
	portsBucket  = "_ports"
	auditBucket  = "audit"
	metaBucket   = "_meta"
)

type Store struct {
	db  *bolt.DB
	dir string
}

func Open(dir string) (*Store, error) {
	if err := secureDir(dir); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "state.db")
	if err := secureExistingFile(p); err != nil {
		return nil, err
	}
	db, err := bolt.Open(p, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open state: %w", err)
	}
	s := &Store{db: db, dir: dir}
	err = db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{runsBucket, tasksBucket, routesBucket, leasesBucket, portsBucket, auditBucket, metaBucket} {
			if _, e := tx.CreateBucketIfNotExists([]byte(name)); e != nil {
				return e
			}
		}
		b := tx.Bucket([]byte(metaBucket))
		v := b.Get([]byte("version"))
		if v != nil && string(v) != "1" {
			return domain.E("STATE_VERSION", "unsupported state schema")
		}
		return b.Put([]byte("version"), []byte("1"))
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Dir() string  { return s.dir }

func secureDir(dir string) error {
	if dir == "" {
		return domain.E("INVALID_PATH", "state directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return domain.E("UNSAFE_PATH", "state path must be a real directory")
	}
	if st.Mode().Perm()&0o077 != 0 {
		return domain.E("INSECURE_PERMISSIONS", "state directory must have mode 0700")
	}
	return nil
}
func secureExistingFile(path string) error {
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0 {
		return domain.E("UNSAFE_PATH", "state file must be a regular file")
	}
	if st.Mode().Perm()&0o077 != 0 {
		return domain.E("INSECURE_PERMISSIONS", "state files must not be accessible by group or other users")
	}
	return nil
}
func validKey(bucket, key string) error {
	if bucket == "" || len(bucket) > 128 || strings.ContainsRune(bucket, 0) || key == "" || len(key) > 4096 {
		return domain.E("INVALID_STATE_KEY", "invalid state bucket or key")
	}
	return nil
}
func protectedBucket(b string) bool {
	return b == tasksBucket || b == leasesBucket || b == portsBucket || b == auditBucket || strings.HasPrefix(b, "_")
}
func notFound() error { return domain.E("NOT_FOUND", "state record was not found") }
func put(tx *bolt.Tx, bucket, key string, v any) error {
	data, e := json.Marshal(v)
	if e != nil {
		return e
	}
	b, e := tx.CreateBucketIfNotExists([]byte(bucket))
	if e != nil {
		return e
	}
	return b.Put([]byte(key), data)
}
func get(tx *bolt.Tx, bucket, key string, out any) error {
	b := tx.Bucket([]byte(bucket))
	if b == nil {
		return notFound()
	}
	v := b.Get([]byte(key))
	if v == nil {
		return notFound()
	}
	return decodeJSON(v, out)
}

func decodeJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(out)
}
func (s *Store) Put(bucket, key string, v any) error {
	if e := validKey(bucket, key); e != nil {
		return e
	}
	if protectedBucket(bucket) {
		return domain.E("PROTECTED_BUCKET", "use typed state operations for "+bucket)
	}
	return s.db.Update(func(tx *bolt.Tx) error { return put(tx, bucket, key, v) })
}
func (s *Store) Get(bucket, key string, out any) error {
	if e := validKey(bucket, key); e != nil {
		return e
	}
	return s.db.View(func(tx *bolt.Tx) error { return get(tx, bucket, key, out) })
}
func (s *Store) Delete(bucket, key string) error {
	if e := validKey(bucket, key); e != nil {
		return e
	}
	if protectedBucket(bucket) {
		return domain.E("PROTECTED_BUCKET", "use typed state operations for "+bucket)
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucket))
		if b == nil {
			return nil
		}
		return b.Delete([]byte(key))
	})
}
func (s *Store) List(bucket string, out any) error {
	return s.db.View(func(tx *bolt.Tx) error {
		values := []json.RawMessage{}
		b := tx.Bucket([]byte(bucket))
		if b != nil {
			e := b.ForEach(func(k, v []byte) error {
				if v != nil {
					values = append(values, append(json.RawMessage(nil), v...))
				}
				return nil
			})
			if e != nil {
				return e
			}
		}
		data, e := json.Marshal(values)
		if e != nil {
			return e
		}
		return decodeJSON(data, out)
	})
}

func (s *Store) PutRun(v domain.Run) error {
	if v.ID == "" || v.Profile == "" {
		return domain.E("INVALID_RUN", "run ID and profile are required")
	}
	return s.Put(runsBucket, v.ID, v)
}
func (s *Store) GetRun(id string) (domain.Run, error) {
	var v domain.Run
	e := s.Get(runsBucket, id, &v)
	return v, e
}
func (s *Store) ListRuns(profile string) ([]domain.Run, error) {
	all := []domain.Run{}
	if e := s.List(runsBucket, &all); e != nil {
		return nil, e
	}
	out := []domain.Run{}
	for _, v := range all {
		if profile == "" || v.Profile == profile {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}
func (s *Store) DeleteRun(id string) error { return s.Delete(runsBucket, id) }
func (s *Store) UpdateRun(id string, fn func(*domain.Run) error) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var v domain.Run
		if e := get(tx, runsBucket, id, &v); e != nil {
			return e
		}
		profile := v.Profile
		if e := fn(&v); e != nil {
			return e
		}
		if v.ID != id || v.Profile != profile {
			return domain.E("INVALID_RUN", "run identity cannot change")
		}
		v.UpdatedAt = time.Now().UTC()
		return put(tx, runsBucket, id, v)
	})
}

func HashTaskRequest(profile, operation string, r domain.TaskRequest) (string, error) {
	r.ID = ""
	b, e := json.Marshal(struct {
		Profile   string             `json:"profile"`
		Operation string             `json:"operation"`
		Request   domain.TaskRequest `json:"request"`
	}{profile, operation, r})
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func normalizedTask(v domain.TaskRecord) (domain.TaskRecord, error) {
	if v.ID == "" || v.Profile == "" || v.Operation == "" || v.Request.Type == "" {
		return v, domain.E("INVALID_TASK", "task identity, profile, operation and request type are required")
	}
	if v.Request.ID == "" {
		v.Request.ID = v.ID
	}
	if v.Request.ID != v.ID {
		return v, domain.E("INVALID_TASK", "record and request UUID must match")
	}
	h, e := HashTaskRequest(v.Profile, v.Operation, v.Request)
	if e != nil {
		return v, e
	}
	if v.Hash != "" && v.Hash != h {
		return v, domain.E("TASK_HASH_MISMATCH", "supplied task hash does not match the request")
	}
	v.Hash = h
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	if v.UpdatedAt.IsZero() {
		v.UpdatedAt = v.CreatedAt
	}
	if v.State == "" {
		v.State = "prepared"
	}
	return v, nil
}
func conflictTask(id string) error {
	return &domain.Error{Code: "IDEMPOTENCY_CONFLICT", Message: "request ID already belongs to a different operation or payload", TaskID: id}
}
func (s *Store) CreateTask(v domain.TaskRecord) (domain.TaskRecord, bool, error) {
	return s.CreateTaskWithIntent(v, nil)
}

// The authorized request and its rebuild parameters must survive the same crash.
func (s *Store) CreateTaskWithIntent(v domain.TaskRecord, intent any) (domain.TaskRecord, bool, error) {
	v, e := normalizedTask(v)
	if e != nil {
		return v, false, e
	}
	created := false
	e = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(tasksBucket))
		if old := b.Get([]byte(v.ID)); old != nil {
			var x domain.TaskRecord
			if e := decodeJSON(old, &x); e != nil {
				return e
			}
			if x.Hash != v.Hash {
				return conflictTask(v.ID)
			}
			v = x
			return nil
		}
		if intent != nil {
			if err := put(tx, "task_intents", v.ID, intent); err != nil {
				return err
			}
		}
		created = true
		return put(tx, tasksBucket, v.ID, v)
	})
	return v, created, e
}
func (s *Store) PutTask(v domain.TaskRecord) error {
	v, e := normalizedTask(v)
	if e != nil {
		return e
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		if old := tx.Bucket([]byte(tasksBucket)).Get([]byte(v.ID)); old != nil {
			var x domain.TaskRecord
			if e := decodeJSON(old, &x); e != nil {
				return e
			}
			if x.Hash != v.Hash {
				return conflictTask(v.ID)
			}
			v.CreatedAt = x.CreatedAt
			v.Verified = v.Verified || x.Verified
		}
		v.UpdatedAt = time.Now().UTC()
		return put(tx, tasksBucket, v.ID, v)
	})
}
func (s *Store) GetTask(id string) (domain.TaskRecord, error) {
	var v domain.TaskRecord
	e := s.Get(tasksBucket, id, &v)
	return v, e
}
func (s *Store) ListTasks(profile string) ([]domain.TaskRecord, error) {
	all := []domain.TaskRecord{}
	if e := s.List(tasksBucket, &all); e != nil {
		return nil, e
	}
	out := []domain.TaskRecord{}
	for _, v := range all {
		if profile == "" || v.Profile == profile {
			out = append(out, v)
		}
	}
	return out, nil
}

// DeleteTask archives the result but retains its identity footprint. Reusing an
// old request ID for a different create must never silently become possible.
func (s *Store) DeleteTask(id string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		var v domain.TaskRecord
		if e := get(tx, tasksBucket, id, &v); e != nil {
			return e
		}
		v.Result = nil
		v.State = "archived"
		v.UpdatedAt = time.Now().UTC()
		return put(tx, tasksBucket, id, v)
	})
}

func RouteKey(profile string, instanceID, nodeID domain.ID) string {
	b, _ := json.Marshal([]string{profile, string(instanceID), string(nodeID)})
	return string(b)
}
func (s *Store) PutRoute(v domain.Route) error {
	if v.Profile == "" || v.InstanceID == "" || v.NodeID == "" {
		return domain.E("INVALID_ROUTE", "route profile, instance and node are required")
	}
	return s.Put(routesBucket, RouteKey(v.Profile, v.InstanceID, v.NodeID), v)
}
func (s *Store) GetRoute(profile string, instanceID, nodeID domain.ID) (domain.Route, error) {
	if nodeID != "" {
		var v domain.Route
		e := s.Get(routesBucket, RouteKey(profile, instanceID, nodeID), &v)
		return v, e
	}
	all, e := s.ListRoutes(profile)
	if e != nil {
		return domain.Route{}, e
	}
	matches := []domain.Route{}
	for _, v := range all {
		if v.InstanceID == instanceID {
			matches = append(matches, v)
		}
	}
	if len(matches) == 0 {
		return domain.Route{}, notFound()
	}
	if len(matches) > 1 {
		return domain.Route{}, domain.E("AMBIGUOUS_NODE", "select a node for this instance")
	}
	return matches[0], nil
}
func (s *Store) ListRoutes(profile string) ([]domain.Route, error) {
	all := []domain.Route{}
	if e := s.List(routesBucket, &all); e != nil {
		return nil, e
	}
	out := []domain.Route{}
	for _, v := range all {
		if profile == "" || v.Profile == profile {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *Store) DeleteRoute(profile string, instanceID, nodeID domain.ID) error {
	if nodeID != "" {
		return s.Delete(routesBucket, RouteKey(profile, instanceID, nodeID))
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(routesBucket))
		c := b.Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			var r domain.Route
			if e := decodeJSON(v, &r); e != nil {
				return e
			}
			if r.Profile == profile && r.InstanceID == instanceID {
				if e := c.Delete(); e != nil {
					return e
				}
			}
		}
		return nil
	})
}

func portKey(port int) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(port))
	return b[:]
}
func sameLease(a, b domain.PortLease) bool {
	return a.Owner == b.Owner && a.Profile == b.Profile && a.InstanceID == b.InstanceID && a.NodeID == b.NodeID
}

// AllocatePort serializes allocation. available must be a short, non-reentrant probe.
func (s *Store) AllocatePort(v domain.PortLease, min, max int, available func(int) bool) (domain.PortLease, error) {
	if v.Key == "" || v.Owner == "" || v.InstanceID == "" || v.NodeID == "" {
		return v, domain.E("INVALID_LEASE", "lease key, owner, instance and node are required")
	}
	if min < 1024 || max > 65535 || min > max {
		return v, domain.E("INVALID_PORT_RANGE", "invalid relay port range")
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		leases := tx.Bucket([]byte(leasesBucket))
		ports := tx.Bucket([]byte(portsBucket))
		if b := leases.Get([]byte(v.Key)); b != nil {
			var old domain.PortLease
			if e := decodeJSON(b, &old); e != nil {
				return e
			}
			if !sameLease(v, old) {
				return domain.E("LEASE_CONFLICT", "lease key belongs to a different target")
			}
			v = old
			return nil
		}
		for port := min; port <= max; port++ {
			if ports.Get(portKey(port)) != nil || available != nil && !available(port) {
				continue
			}
			v.Port = port
			v.CreatedAt = time.Now().UTC()
			v.UpdatedAt = v.CreatedAt
			if v.State == "" {
				v.State = "reserved"
			}
			if e := put(tx, leasesBucket, v.Key, v); e != nil {
				return e
			}
			return ports.Put(portKey(port), []byte(v.Key))
		}
		return domain.E("PORT_POOL_EXHAUSTED", "no free relay port is available")
	})
	return v, err
}
func (s *Store) PutPortLease(v domain.PortLease) error {
	if v.Key == "" || v.Owner == "" || v.Port < 1024 || v.Port > 65535 {
		return domain.E("INVALID_LEASE", "invalid relay lease")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(leasesBucket))
		ports := tx.Bucket([]byte(portsBucket))
		if old := b.Get([]byte(v.Key)); old != nil {
			var x domain.PortLease
			if e := decodeJSON(old, &x); e != nil {
				return e
			}
			if !sameLease(v, x) || v.Port != x.Port {
				return domain.E("LEASE_CONFLICT", "lease identity and port cannot change")
			}
			v.CreatedAt = x.CreatedAt
		}
		if k := ports.Get(portKey(v.Port)); k != nil && string(k) != v.Key {
			return domain.E("PORT_CONFLICT", "port is allocated to another lease")
		}
		v.UpdatedAt = time.Now().UTC()
		if v.CreatedAt.IsZero() {
			v.CreatedAt = v.UpdatedAt
		}
		if e := put(tx, leasesBucket, v.Key, v); e != nil {
			return e
		}
		return ports.Put(portKey(v.Port), []byte(v.Key))
	})
}
func (s *Store) GetPortLease(key string) (domain.PortLease, error) {
	var v domain.PortLease
	e := s.Get(leasesBucket, key, &v)
	return v, e
}
func (s *Store) ListPortLeases(owner string) ([]domain.PortLease, error) {
	all := []domain.PortLease{}
	if e := s.List(leasesBucket, &all); e != nil {
		return nil, e
	}
	out := []domain.PortLease{}
	for _, v := range all {
		if owner == "" || v.Owner == owner {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, nil
}
func (s *Store) ReleasePort(key string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(leasesBucket))
		old := b.Get([]byte(key))
		if old == nil {
			return nil
		}
		var v domain.PortLease
		if e := decodeJSON(old, &v); e != nil {
			return e
		}
		ports := tx.Bucket([]byte(portsBucket))
		if existing := ports.Get(portKey(v.Port)); existing != nil && string(existing) != key {
			return domain.E("STATE_INTEGRITY", "port allocation does not match its lease")
		}
		if e := ports.Delete(portKey(v.Port)); e != nil {
			return e
		}
		return b.Delete([]byte(key))
	})
}

var auditFields = map[string]bool{"state": true, "code": true, "retryable": true, "port": true, "phase": true, "deadline": true, "requested_until": true, "approved_expires_at": true, "client": true, "client_id": true, "scope": true, "reason": true, "policy_revision": true, "exit_code": true, "cleanup_state": true, "auth_state": true, "instance_id": true, "node_id": true, "profile": true, "count": true, "operation": true, "request_id": true, "duration_ms": true, "bytes": true}

var privatePEM = regexp.MustCompile(`(?s)-----BEGIN [^-]*PRIVATE KEY-----.*?-----END [^-]*PRIVATE KEY-----`)
var authHeader = regexp.MustCompile(`(?i)(authorization|cookie|set-cookie)\s*:\s*[^\r\n]+`)
var secretAssignment = regexp.MustCompile(`(?i)((?:password(?:sha256)?|passphrase|secret|token|private[_ -]?key)\s*[=:]\s*)(?:"[^"]*"|'[^']*'|[^\s,;]+)`)

// RedactText protects diagnostic summaries as a second line of defense. Callers
// should still avoid including request bodies or complete platform view dumps.
func RedactText(s string) string {
	s = privatePEM.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	s = authHeader.ReplaceAllString(s, "$1: [REDACTED]")
	s = secretAssignment.ReplaceAllString(s, "$1[REDACTED]")
	return s
}

func cleanAudit(v domain.AuditEvent) domain.AuditEvent {
	if v.Time.IsZero() {
		v.Time = time.Now().UTC()
	}
	data := map[string]any{}
	for k, x := range v.Data {
		if !auditFields[k] {
			continue
		}
		switch value := x.(type) {
		case string:
			data[k] = RedactText(value)
		case bool, int, int64, uint64, float64, json.Number, nil:
			data[k] = x
		}
	}
	v.Data = data
	v.Message = RedactText(v.Message)
	// Messages are deliberately bounded; call sites must supply summaries, never dumps.
	if len(v.Message) > 2048 {
		v.Message = v.Message[:2048]
	}
	return v
}
func (s *Store) AppendAudit(v domain.AuditEvent) error {
	v = cleanAudit(v)
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(auditBucket))
		id, e := b.NextSequence()
		if e != nil {
			return e
		}
		var key [8]byte
		binary.BigEndian.PutUint64(key[:], id)
		data, e := json.Marshal(v)
		if e != nil {
			return e
		}
		return b.Put(key[:], data)
	})
}
func (s *Store) ListAudits(after time.Time, limit int) ([]domain.AuditEvent, error) {
	return s.listAudits("", "", after, limit)
}

func (s *Store) ListInstanceAudits(profile, target string, limit int) ([]domain.AuditEvent, error) {
	return s.listAudits(profile, target, time.Time{}, limit)
}

func (s *Store) listAudits(profile, target string, after time.Time, limit int) ([]domain.AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 10000 {
		limit = 10000
	}
	out := []domain.AuditEvent{}
	e := s.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket([]byte(auditBucket)).Cursor()
		for k, v := c.Last(); k != nil && len(out) < limit; k, v = c.Prev() {
			var x domain.AuditEvent
			if e := decodeJSON(v, &x); e != nil {
				return e
			}
			if profile != "" && x.Profile != profile || target != "" && x.Target != target || !after.IsZero() && !x.Time.After(after) {
				continue
			}
			out = append(out, x)
		}
		return nil
	})
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, e
}
func (s *Store) Backup(path string) error {
	if err := secureExistingFile(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	good := false
	defer func() {
		f.Close()
		if !good {
			os.Remove(path)
		}
	}()
	err = s.db.View(func(tx *bolt.Tx) error { _, e := tx.WriteTo(f); return e })
	if err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	good = true
	return f.Close()
}
