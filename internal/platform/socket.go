package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type relation struct {
	Label  string    `json:"label"`
	Source domain.ID `json:"srcEid"`
	Target domain.ID `json:"tgtEid"`
}
type view struct {
	Index     int64
	Entities  map[string]map[domain.ID]map[string]any
	Relations []relation
}
type eventSocket struct {
	c         *Client
	conn      *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	syncMu    sync.Mutex
	notify    chan struct{}
	err       error
	ready     bool
	connected bool
	index     int64
	views     map[string]*view
	tasks     map[string]domain.TaskResult
	requests  map[string]string
	lastPong  time.Time
}

func (s *eventSocket) changed() { close(s.notify); s.notify = make(chan struct{}) }
func (s *eventSocket) fail(e error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = e
		s.changed()
	}
	s.mu.Unlock()
	s.cancel()
	_ = s.conn.CloseNow()
}
func (s *eventSocket) close() { s.fail(domain.E("CLOSED", "platform event connection closed")) }
func (s *eventSocket) send(name string, payload any) error {
	args := []any{name}
	if payload != nil {
		args = append(args, payload)
	}
	b, e := json.Marshal(args)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, append([]byte("42"), b...))
}
func (c *Client) eventConnection(ctx context.Context) (*eventSocket, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false, domain.E("CLOSED", "platform client is closed")
	}
	if c.socket != nil {
		c.socket.mu.Lock()
		alive := c.socket.err == nil
		c.socket.mu.Unlock()
		if alive {
			return c.socket, false, nil
		}
	}
	target, e := c.websocketEndpoint("/socket.io/")
	if e != nil {
		return nil, false, e
	}
	q := target.Query()
	q.Set("EIO", "3")
	q.Set("transport", "websocket")
	target.RawQuery = q.Encode()
	origin := c.base.Scheme + "://" + c.base.Host
	conn, r, e := websocket.Dial(ctx, target.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": []string{origin}}})
	if e != nil {
		if r != nil {
			if x := c.responseError(r); x != nil {
				return nil, false, x
			}
		}
		return nil, false, &domain.Error{Code: "EVENT_CONNECT", Message: "could not connect to the platform event stream", Retryable: true}
	}
	conn.SetReadLimit(64 << 20)
	if e = c.persist(); e != nil {
		conn.CloseNow()
		return nil, false, e
	}
	bg, cancel := context.WithCancel(context.Background())
	s := &eventSocket{c: c, conn: conn, ctx: bg, cancel: cancel, notify: make(chan struct{}), index: -1, views: map[string]*view{}, tasks: map[string]domain.TaskResult{}, requests: map[string]string{}, lastPong: time.Now()}
	c.socket = s
	go s.readLoop()
	return s, true, nil
}
func (s *eventSocket) readLoop() {
	for {
		_, b, e := s.conn.Read(s.ctx)
		if e != nil {
			s.fail(&domain.Error{Code: "EVENT_DISCONNECTED", Message: "platform event stream disconnected; task outcome may need reconciliation", Retryable: true})
			return
		}
		if len(b) == 0 {
			continue
		}
		msg := string(b)
		switch msg[0] {
		case '0':
			var hello struct {
				PingInterval int `json:"pingInterval"`
				PingTimeout  int `json:"pingTimeout"`
			}
			if json.Unmarshal(b[1:], &hello) != nil {
				s.fail(domain.E("PROTOCOL", "invalid Engine.IO handshake"))
				return
			}
			if hello.PingInterval <= 0 {
				hello.PingInterval = 25000
			}
			if hello.PingTimeout <= 0 {
				hello.PingTimeout = 60000
			}
			go s.heartbeat(time.Duration(hello.PingInterval)*time.Millisecond, time.Duration(hello.PingTimeout)*time.Millisecond)
		case '1':
			s.fail(domain.E("EVENT_DISCONNECTED", "server closed the event stream"))
			return
		case '2':
			_ = s.conn.Write(s.ctx, websocket.MessageText, []byte("3"+msg[1:]))
		case '3':
			s.mu.Lock()
			s.lastPong = time.Now()
			s.mu.Unlock()
		case '4':
			if strings.HasPrefix(msg, "40") {
				s.mu.Lock()
				if !s.connected {
					s.connected = true
					s.mu.Unlock()
					if e = s.send("SYNC", map[string]any{"index": -1}); e != nil {
						s.fail(e)
						return
					}
				} else {
					s.mu.Unlock()
				}
			} else if strings.HasPrefix(msg, "42") {
				if e = s.event(b[2:]); e != nil {
					s.fail(e)
					return
				}
			} else if strings.HasPrefix(msg, "41") || strings.HasPrefix(msg, "44") {
				s.fail(domain.E("AUTH_EXPIRED", "platform event session was rejected"))
				return
			} else if strings.HasPrefix(msg, "45") {
				s.fail(domain.E("PROTOCOL", "unexpected binary Socket.IO event"))
				return
			}
		}
	}
}
func (s *eventSocket) heartbeat(interval, timeout time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.mu.Lock()
			stale := time.Since(s.lastPong) > interval+timeout
			s.mu.Unlock()
			if stale {
				s.fail(domain.E("EVENT_TIMEOUT", "platform heartbeat timed out"))
				return
			}
			ctx, cancel := context.WithTimeout(s.ctx, timeout)
			e := s.conn.Write(ctx, websocket.MessageText, []byte("2"))
			cancel()
			if e != nil {
				s.fail(e)
				return
			}
		}
	}
}
func (s *eventSocket) event(b []byte) error {
	var args []json.RawMessage
	if e := json.Unmarshal(b, &args); e != nil || len(args) == 0 {
		return domain.E("PROTOCOL", "invalid Socket.IO event")
	}
	var name string
	if json.Unmarshal(args[0], &name) != nil {
		return domain.E("PROTOCOL", "invalid event name")
	}
	var m map[string]any
	if len(args) > 1 {
		if e := decodeInto(json.RawMessage(args[1]), &m); e != nil {
			return e
		}
	}
	s.mu.Lock()
	readyAck := false
	resync := false
	switch name {
	case "VIEW_DUMP":
		views := map[string]*view{}
		for _, item := range array(m["views"]) {
			outer := object(item)
			body := object(outer["view"])
			dump := object(body["memViewDump"])
			if dump == nil {
				s.mu.Unlock()
				return domain.E("PROTOCOL", "invalid memview dump")
			}
			v := &view{Index: integer(body["index"]), Entities: map[string]map[domain.ID]map[string]any{}}
			for typ, entities := range object(dump["entities"]) {
				for _, raw := range array(entities) {
					record := object(raw)
					record = filterEntity(typ, record)
					if record != nil {
						if v.Entities[typ] == nil {
							v.Entities[typ] = map[domain.ID]map[string]any{}
						}
						v.Entities[typ][id(record["eid"])] = record
					}
				}
			}
			_ = decodeInto(dump["relations"], &v.Relations)
			views[stringValue(outer["name"])] = v
		}
		s.views = views
		s.index = integer(m["minIndex"])
		s.ready = true
		s.tasksFromViews()
		s.changed()
		readyAck = true
	case "SYNCED":
		s.ready = true
		s.changed()
		readyAck = true
	case "TASK_RET":
		tid := stringValue(m["id"])
		r := s.tasks[tid]
		// A duplicate UUID means the original request exists, not that it
		// failed. Recover its authoritative Task entity before deciding.
		if integer(m["code"]) == 1004 {
			if !terminalTask(r.State) {
				r.ID, r.State, r.Code = tid, "received", 0
				s.tasks[tid] = r
			}
			s.ready = false
			resync = true
			s.changed()
			break
		}
		r.ID = tid
		r.State = stringValue(m["state"])
		r.Code = int(integer(m["code"]))
		r.Message = safeTaskMessage(r.Code, r.State)
		if r.Code != 0 && r.State == "" {
			r.State = "rejected"
		}
		s.tasks[tid] = r
		if r.Code == 1165 {
			s.ready = false
			resync = true
		}
		s.changed()
	case "EVENT":
		idx := integer(m["index"])
		prev := integer(m["previousIndex"])
		if idx <= s.index {
			break
		}
		if prev != s.index {
			s.ready = false
			resync = true
			break
		}
		s.index = idx
		vu := object(m["viewUpdate"])
		if vu != nil {
			vn := stringValue(m["createdBy"])
			if vn == "" {
				vn = stringValue(vu["viewName"])
			}
			if vn == "" {
				s.ready = false
				resync = true
				break
			}
			v := s.views[vn]
			if v == nil {
				v = &view{Index: -1, Entities: map[string]map[domain.ID]map[string]any{}}
				s.views[vn] = v
			}
			if idx > v.Index {
				if e := v.apply(vu); e != nil {
					s.ready = false
					resync = true
				} else {
					v.Index = idx
				}
			}
		}
		typ := stringValue(m["type"])
		tid := stringValue(m["taskId"])
		payload := object(m["payload"])
		if tid != "" {
			r := s.tasks[tid]
			r.ID = tid
			switch {
			case typ == "TaskCompleted":
				r.State = "complete"
			case typ == "TaskAborted":
				r.State = "aborted"
			case typ == "TaskCleaned":
				if !terminalTask(r.State) {
					r.State = "aborted"
				}
			case strings.HasSuffix(typ, "TaskAccepted"):
				r.State = "running"
			}
			for _, op := range array(vu["operations"]) {
				o := object(op)
				fields := fieldsMap(o["fields"])
				if stringValue(o["typeName"]) == "Task" {
					if state := stringValue(fields["state"]); state != "" {
						r.State = state
					}
					r.Code = int(integer(fields["code"]))
					r.Message = safeTaskMessage(r.Code, r.State)
				}
				if typ == "AppCreated" && stringValue(o["typeName"]) == "App" && stringValue(o["operName"]) == "entityAdd" {
					r.InstanceID = id(o["eid"])
				}
			}
			if typ == "AppCreated" && r.InstanceID == "" {
				r.InstanceID = id(payload["appEid"])
			}
			s.tasks[tid] = r
		}
		s.tasksFromViews()
		s.changed()
	}
	s.mu.Unlock()
	if readyAck {
		return s.send("READY_RECEIVE_EVENT", nil)
	}
	if resync {
		return s.send("SYNC", map[string]any{"index": -1})
	}
	return nil
}
func safeTaskMessage(code int, state string) string {
	if code != 0 {
		return fmt.Sprintf("platform task returned code %d", code)
	}
	return state
}
func terminalTask(state string) bool {
	return state == "complete" || state == "rejected" || state == "aborted" || state == "need_repeat"
}
func (s *eventSocket) tasksFromViews() {
	for _, v := range s.views {
		for _, m := range v.Entities["Task"] {
			tid := stringValue(m["id"])
			if tid == "" {
				continue
			}
			r := s.tasks[tid]
			r.ID = tid
			r.State = stringValue(m["state"])
			r.Code = int(integer(m["code"]))
			r.Message = safeTaskMessage(r.Code, r.State)
			s.tasks[tid] = r
		}
	}
}
func (s *eventSocket) waitReady(ctx context.Context) error {
	for {
		s.mu.Lock()
		if s.err != nil {
			e := s.err
			s.mu.Unlock()
			return e
		}
		if s.ready {
			s.mu.Unlock()
			return nil
		}
		ch := s.notify
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}
func (c *Client) Sync(ctx context.Context) (domain.Snapshot, error) {
	identity, e := c.WhoAmI(ctx)
	if e != nil {
		return domain.Snapshot{}, e
	}
	s, fresh, e := c.eventConnection(ctx)
	if e != nil {
		return domain.Snapshot{}, e
	}
	s.syncMu.Lock()
	defer s.syncMu.Unlock()
	if e = s.waitReady(ctx); e != nil {
		return domain.Snapshot{}, e
	}
	if !fresh {
		s.mu.Lock()
		s.ready = false
		index := s.index
		s.mu.Unlock()
		if e = s.send("SYNC", map[string]any{"index": index}); e != nil {
			return domain.Snapshot{}, e
		}
		if e = s.waitReady(ctx); e != nil {
			return domain.Snapshot{}, e
		}
	}
	c.mu.Lock()
	current := c.identity
	c.mu.Unlock()
	if current.UID != identity.UID || (current.EID != "" && identity.EID != "" && current.EID != identity.EID) {
		return domain.Snapshot{}, domain.E("IDENTITY_CONTEXT_CHANGED", "authenticated identity changed during synchronization")
	}
	s.mu.Lock()
	bound, bindErr := s.graph().bindIdentity(identity)
	if bindErr != nil {
		s.mu.Unlock()
		return domain.Snapshot{}, bindErr
	}
	snapshot := s.snapshot(bound)
	s.mu.Unlock()
	snapshot.Identity, e = c.enrichIdentityContact(ctx, snapshot.Identity)
	if e != nil {
		return domain.Snapshot{}, e
	}
	c.mu.Lock()
	if c.identity.UID != snapshot.Identity.UID || (c.identity.EID != "" && c.identity.EID != snapshot.Identity.EID) {
		c.mu.Unlock()
		return domain.Snapshot{}, domain.E("IDENTITY_CONTEXT_CHANGED", "authenticated identity changed before synchronization completed")
	}
	c.identity = snapshot.Identity
	c.mu.Unlock()
	return snapshot, nil
}
func (c *Client) Submit(ctx context.Context, request domain.TaskRequest) (domain.TaskResult, error) {
	if request.Type == "" {
		return domain.TaskResult{}, domain.E("INVALID_TASK", "task type is required")
	}
	if request.ID == "" {
		request.ID = uuid.NewString()
	}
	if _, e := uuid.Parse(request.ID); e != nil {
		return domain.TaskResult{}, domain.E("INVALID_TASK", "task ID must be a UUID")
	}
	identity, e := c.WhoAmI(ctx)
	if e != nil {
		return domain.TaskResult{ID: request.ID}, e
	}
	if request.UserID != "" && request.UserID != identity.UID {
		return domain.TaskResult{ID: request.ID}, domain.E("IDENTITY_MISMATCH", "task user differs from authenticated user")
	}
	request.UserID = identity.UID
	s, _, e := c.eventConnection(ctx)
	if e != nil {
		return domain.TaskResult{ID: request.ID}, e
	}
	if e = s.waitReady(ctx); e != nil {
		return domain.TaskResult{ID: request.ID}, e
	}
	payload, e := json.Marshal(request.Payload)
	if e != nil {
		return domain.TaskResult{ID: request.ID}, e
	}
	wire := map[string]any{"id": request.ID, "type": request.Type, "payload": string(payload), "userId": identity.UID.Number(), "adminUserId": 0}
	fingerprint := request.Type + "\n" + string(payload)
	s.mu.Lock()
	if old, exists := s.requests[request.ID]; exists && old != fingerprint {
		s.mu.Unlock()
		return domain.TaskResult{ID: request.ID}, domain.E("TASK_ID_REUSED", "task UUID was already used with a different payload")
	}
	s.requests[request.ID] = fingerprint
	existing := s.tasks[request.ID]
	s.mu.Unlock()
	if terminalTask(existing.State) {
		return taskOutcome(existing)
	}
	if existing.State != "running" && existing.State != "received" {
		if e = s.send("TASK", wire); e != nil {
			return domain.TaskResult{ID: request.ID}, &domain.Error{Code: "TASK_PENDING", Message: "task send outcome is uncertain; reconcile this UUID", TaskID: request.ID, Retryable: true}
		}
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		result := s.tasks[request.ID]
		result.ID = request.ID
		ch := s.notify
		err := s.err
		s.mu.Unlock()
		if terminalTask(result.State) {
			return taskOutcome(result)
		}
		if err != nil {
			return result, &domain.Error{Code: "TASK_PENDING", Message: "event connection was lost; reconcile the same task UUID before retrying", TaskID: request.ID, Retryable: true}
		}
		select {
		case <-ctx.Done():
			return result, &domain.Error{Code: "TASK_PENDING", Message: "task outcome is pending; retain this UUID for reconciliation", TaskID: request.ID, Retryable: errors.Is(ctx.Err(), context.DeadlineExceeded)}
		case <-ch:
		case <-ticker.C:
			if result.State != "running" && result.State != "received" {
				if e = s.send("TASK", wire); e != nil {
					return result, &domain.Error{Code: "TASK_PENDING", Message: "task send outcome is uncertain", TaskID: request.ID, Retryable: true}
				}
			}
		}
	}
}
func taskOutcome(r domain.TaskResult) (domain.TaskResult, error) {
	if r.State == "complete" && r.Code == 0 {
		return r, nil
	}
	return r, &domain.Error{Code: fmt.Sprintf("TASK_%d", r.Code), Message: "platform task " + r.State, TaskID: r.ID, Retryable: r.Code == 1165}
}
