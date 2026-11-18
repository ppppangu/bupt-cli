package platform

import (
	"context"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/coder/websocket"
)

type ConsoleStream struct {
	conn    *websocket.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	pending []byte
	once    sync.Once
}

func (c *Client) Console(ctx context.Context, serviceID domain.ID) (io.ReadWriteCloser, error) {
	n, ok := new(big.Int).SetString(string(serviceID), 10)
	if !ok || n.Sign() <= 0 {
		return nil, domain.E("INVALID_ID", "console service ID must be a positive integer")
	}
	u, e := c.websocketEndpoint("/proxy/service/" + n.Text(16) + "/term")
	if e != nil {
		return nil, e
	}
	conn, r, e := websocket.Dial(ctx, u.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": []string{c.base.Scheme + "://" + c.base.Host}}})
	if e != nil {
		if r != nil {
			if ae := c.responseError(r); ae != nil {
				return nil, ae
			}
		}
		return nil, domain.E("CONSOLE_CONNECT", "could not connect to the platform console")
	}
	conn.SetReadLimit(8 << 20)
	if e = c.persist(); e != nil {
		conn.CloseNow()
		return nil, e
	}
	bg, cancel := context.WithCancel(ctx)
	s := &ConsoleStream{conn: conn, ctx: bg, cancel: cancel}
	go s.heartbeat()
	return s, nil
}
func (s *ConsoleStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	for len(s.pending) == 0 {
		_, b, e := s.conn.Read(s.ctx)
		if e != nil {
			return 0, e
		}
		if len(b) > 1 {
			s.pending = append([]byte(nil), b[1:]...)
		}
	}
	n := copy(p, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}
func (s *ConsoleStream) Write(p []byte) (int, error) {
	b := make([]byte, len(p)+1)
	copy(b[1:], p)
	if e := s.conn.Write(s.ctx, websocket.MessageBinary, b); e != nil {
		return 0, e
	}
	return len(p), nil
}
func (s *ConsoleStream) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return domain.E("INVALID_SIZE", "console dimensions must be positive")
	}
	b, _ := json.Marshal(map[string]int{"cols": cols, "rows": rows})
	return s.conn.Write(s.ctx, websocket.MessageBinary, append([]byte{1}, b...))
}
func (s *ConsoleStream) heartbeat() {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			if e := s.conn.Write(s.ctx, websocket.MessageBinary, []byte{2}); e != nil {
				s.Close()
				return
			}
		}
	}
}
func (s *ConsoleStream) Close() error {
	s.once.Do(func() { s.cancel(); _ = s.conn.CloseNow() })
	return nil
}
