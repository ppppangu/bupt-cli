package client

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/wire"
)

func localFixture(t *testing.T) (*Client, net.Listener) {
	t.Helper()
	dir, e := os.MkdirTemp("/tmp", "bupt-client-")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "rpc.sock")
	l, e := net.Listen("unix", path)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	p := domain.Profile{Name: "bupt", Controller: "local", ControllerSocket: path}
	return &Client{Config: &config.Config{}, Profile: p}, l
}
func TestStreamHandshakeCancellation(t *testing.T) {
	c, l := localFixture(t)
	done := make(chan struct{})
	go func() {
		n, e := l.Accept()
		if e != nil {
			return
		}
		defer n.Close()
		var request domain.RPCRequest
		_ = wire.ReadJSON(bufio.NewReader(n), &request)
		_, _ = io.Copy(io.Discard, n)
		close(done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, e := c.Stream(ctx, "data.download", nil, nil, io.Discard)
	if e == nil {
		t.Fatal("expected cancellation")
	}
	if time.Since(start) > time.Second {
		t.Fatal("handshake did not cancel")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection remained open")
	}
}
func TestProxyNodeKeepsStdoutByteClean(t *testing.T) {
	c, l := localFixture(t)
	checked := make(chan domain.RPCRequest, 1)
	go func() {
		n, e := l.Accept()
		if e != nil {
			return
		}
		defer n.Close()
		var request domain.RPCRequest
		if wire.ReadJSON(bufio.NewReader(n), &request) != nil {
			return
		}
		checked <- request
		wire.WriteJSON(n, domain.RPCResponse{Result: domain.Result(map[string]any{"ready": true})})
		n.Write([]byte("SSH-2.0-fixture\r\n"))
	}()
	var out stringWriter
	if e := c.ProxyNode(context.Background(), "instance", "node", nil, &out); e != nil {
		t.Fatal(e)
	}
	if string(out) != "SSH-2.0-fixture\r\n" {
		t.Fatalf("polluted stream: %q", out)
	}
	request := <-checked
	var p map[string]string
	json.Unmarshal(request.Params, &p)
	if p["node"] != "node" || request.Method != "connect.proxy" {
		t.Fatal("node was not forwarded")
	}
}

type stringWriter string

func (s *stringWriter) Write(p []byte) (int, error) { *s += stringWriter(p); return len(p), nil }
