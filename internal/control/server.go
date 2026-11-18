package control

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/state"
	"bupt.local/cli/internal/wire"
)

// Serve exposes one request per private Unix connection. SSH gateways replace
// client_id with the identity selected by authorized_keys before reaching here.
func (c *Controller) Serve(ctx context.Context, socket string) error {
	if socket == "" {
		socket = c.Configuration().Paths.Socket
	}
	if e := os.MkdirAll(filepath.Dir(socket), 0700); e != nil {
		return e
	}
	parent, e := os.Lstat(filepath.Dir(socket))
	if e != nil {
		return e
	}
	if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0077 != 0 {
		return domain.E("INSECURE_SOCKET_DIRECTORY", "controller socket requires a private directory with mode 0700")
	}
	if st, e := os.Lstat(socket); e == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return domain.E("SOCKET_CONFLICT", "refusing to replace a non-socket path")
		}
		n, de := net.DialTimeout("unix", socket, time.Second)
		if de == nil {
			n.Close()
			return domain.E("AGENT_RUNNING", "controller already listens on this socket")
		}
		if e = os.Remove(socket); e != nil {
			return e
		}
	}
	ln, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	defer ln.Close()
	defer os.Remove(socket)
	if e = os.Chmod(socket, 0600); e != nil {
		return e
	}
	pidPath := filepath.Join(c.Store.Dir(), "agent.pid")
	if e = os.WriteFile(pidPath, []byte(fmt.Sprint(os.Getpid())+"\n"), 0600); e != nil {
		return e
	}
	defer os.Remove(pidPath)
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-serveCtx.Done():
		case <-c.ctx.Done():
			cancel()
		}
		ln.Close()
	}()
	c.spawn("reconciler", func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			c.Reconcile(c.ctx)
			tasks, _ := c.Store.ListTasks("")
			for _, t := range tasks {
				if t.ParentID == "" && !terminalTask(t.State) && time.Since(t.UpdatedAt) > 30*time.Second {
					c.resumeTask(t)
				}
			}
			c.gcLeases()
			select {
			case <-c.ctx.Done():
				return
			case <-serveCtx.Done():
				return
			case <-tick.C:
			}
		}
	})
	var connections sync.WaitGroup
	defer func() { cancel(); connections.Wait() }()
	for {
		n, e := ln.Accept()
		if e != nil {
			if serveCtx.Err() != nil || c.ctx.Err() != nil {
				return nil
			}
			return e
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			defer n.Close()
			stop := context.AfterFunc(serveCtx, func() { n.Close() })
			defer stop()
			c.serveConnection(serveCtx, n)
		}()
	}
}
func (c *Controller) serveConnection(ctx context.Context, n net.Conn) {
	_ = n.SetReadDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(n)
	var req domain.RPCRequest
	if e := wire.ReadJSON(reader, &req); e != nil {
		_ = wire.WriteJSON(n, domain.RPCResponse{Error: rpcError(domain.E("INVALID_RPC", "invalid or oversized request"))})
		return
	}
	_ = n.SetReadDeadline(time.Time{})
	if req.Profile == "" {
		req.Profile = c.Configuration().Selected
	}
	p, e := Decode(req.Params)
	if e == nil {
		req.Profile, e = c.authorizedProfile(req, p)
	}
	if e == nil {
		e = c.Authorize(req.ClientID, req.Method, req.Profile)
	}
	if e != nil {
		_ = wire.WriteJSON(n, domain.RPCResponse{ID: req.ID, Error: rpcError(e)})
		return
	}
	if c.HandleStream(ctx, req, p, reader, n) {
		return
	}
	if c.HandleJobStream(ctx, req, p, reader, n) {
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	v, e := c.Handle(requestCtx, req, p)
	response := domain.RPCResponse{ID: req.ID}
	if e != nil {
		response.Error = rpcError(e)
	} else {
		response.Result = publicResult(v)
	}
	_ = wire.WriteJSON(n, response)
	if req.Method == "agent.stop" && e == nil {
		c.cancel()
	}
}

// Intent hashes can depend on private job argv/env. Keep them in the ledger,
// not in frontend state or CLI output; preserve integer precision in projection.
func publicResult(value any) json.RawMessage {
	raw := domain.Result(value)
	var public any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&public) != nil {
		return raw
	}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			delete(x, "intent_hash")
			for _, child := range x {
				visit(child)
			}
		case []any:
			for _, child := range x {
				visit(child)
			}
		}
	}
	visit(public)
	return domain.Result(public)
}
func rpcError(e error) *domain.Error {
	if e == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(e, &de) {
		return de
	}
	if errors.Is(e, context.Canceled) {
		return &domain.Error{Code: "CANCELLED", Message: "request cancelled"}
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return &domain.Error{Code: "TIMEOUT", Message: "request timed out", Retryable: true}
	}
	// HTTP and SSH errors can contain signed URLs, so protocol output never
	// includes arbitrary upstream error strings.
	return &domain.Error{Code: "IO_ERROR", Message: "controller I/O failed; check connectivity and doctor", Retryable: true}
}
func (c *Controller) gcLeases() {
	leases, _ := c.Store.ListPortLeases("")
	for _, l := range leases {
		if l.State == "revoked_pending_close" && portAvailable(l.Port) {
			_ = c.Store.ReleasePort(l.Key)
		}
	}
}

// Gateway is invoked by a forced SSH command, not by a caller-supplied shell.
// The only accepted commands are rpc and proxy <opaque-lease-id>.
func Gateway(ctx context.Context, socket, clientID, original string, in io.Reader, out io.Writer) error {
	if !safeID.MatchString(clientID) {
		return domain.E("INVALID_CLIENT", "gateway requires a registered client identity")
	}
	args := strings.Fields(original)
	if len(args) == 0 {
		return domain.E("GATEWAY_COMMAND", "interactive shells are not enabled for this identity")
	}
	d := net.Dialer{}
	n, e := d.DialContext(ctx, "unix", socket)
	if e != nil {
		return domain.E("AGENT_NOT_RUNNING", "relay controller is unavailable")
	}
	defer n.Close()
	stop := context.AfterFunc(ctx, func() { n.Close() })
	defer stop()
	reader := bufio.NewReader(in)
	if len(args) == 1 && args[0] == "rpc" {
		var q domain.RPCRequest
		if e = wire.ReadJSON(reader, &q); e != nil {
			return domain.E("INVALID_RPC", "invalid gateway request")
		}
		q.ClientID = clientID
		if e = wire.WriteJSON(n, q); e != nil {
			return e
		}
	} else if len(args) == 2 && args[0] == "proxy" && safeID.MatchString(args[1]) {
		q := domain.RPCRequest{Method: "relay.proxy", ClientID: clientID, Params: domain.Result(map[string]string{"key": args[1]})}
		if e = wire.WriteJSON(n, q); e != nil {
			return e
		}
		r := bufio.NewReader(n)
		var ack domain.RPCResponse
		if e = wire.ReadJSON(r, &ack); e != nil {
			return e
		}
		if ack.Error != nil {
			return ack.Error
		}
		go func() {
			io.Copy(n, reader)
			if u, ok := n.(*net.UnixConn); ok {
				u.CloseWrite()
			}
		}()
		_, e = io.Copy(out, r)
		return e
	} else {
		return domain.E("GATEWAY_COMMAND", "only rpc or a registered proxy lease is allowed")
	}
	go func() {
		io.Copy(n, reader)
		if u, ok := n.(*net.UnixConn); ok {
			u.CloseWrite()
		}
	}()
	_, e = io.Copy(out, n)
	return e
}

func RunAgent(ctx context.Context, cfg *config.Config, params map[string]any, log io.Writer) error {
	p, _ := Decode(domain.Result(params))
	if p.Bool("dry_run") {
		return nil
	}
	store, e := state.Open(cfg.Paths.StateDir)
	if e != nil {
		return e
	}
	defer store.Close()
	key := p.String("master_key_file")
	if key == "" {
		candidate := filepath.Join(cfg.Paths.DataDir, "master.key")
		if _, e := os.Stat(candidate); e == nil {
			key = candidate
		}
	}
	vault, e := state.OpenVault(cfg.Paths.DataDir, key)
	if e != nil {
		return e
	}
	defer vault.Close()
	socket := p.String("socket")
	if socket == "" {
		socket = cfg.Paths.Socket
	}
	c, e := New(Options{Config: cfg, Store: store, Vault: vault, AuthorizedKeys: p.String("authorized_keys"), Executable: p.String("executable"), Socket: socket})
	if e != nil {
		return e
	}
	defer c.Close()
	if path := p.String("bootstrap_client"); path != "" {
		v, e := ReadBootstrapClient(path)
		if e != nil {
			return e
		}
		var old ClientIdentity
		if e = store.Get("clients", v.ID, &old); e != nil {
			if _, e = c.RegisterClient(v); e != nil {
				return e
			}
		}
	}
	if log != nil {
		fmt.Fprintln(log, "bupt controller listening on", socket)
	}
	return c.Serve(ctx, socket)
}
