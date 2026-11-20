package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/wire"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

type Client struct {
	Config  *config.Config
	Profile domain.Profile
}

func New(c *config.Config, name string) (*Client, error) {
	if name == "" {
		name = c.Selected
	}
	p, e := c.Profile(name)
	if e != nil {
		return nil, e
	}
	return &Client{Config: c, Profile: p}, nil
}
func (c *Client) open(ctx context.Context) (io.ReadWriteCloser, error) {
	if c.Profile.Controller == "" || c.Profile.Controller == "local" {
		path := c.Profile.ControllerSocket
		if path == "" {
			path = c.Config.Paths.Socket
		}
		d := net.Dialer{}
		n, e := d.DialContext(ctx, "unix", path)
		if e != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(e, os.ErrNotExist) || errors.Is(e, syscall.ECONNREFUSED) {
				return nil, domain.E("AGENT_NOT_RUNNING", "本地控制器尚未运行。")
			}
			return nil, domain.E("CONTROLLER_CONNECT", fmt.Sprintf("无法连接本地控制器。%v", e))
		}
		return n, nil
	}
	return OpenCommand(ctx, domain.RelayConfig{Address: c.Profile.Controller, User: c.Profile.ControllerUser, IdentityFile: c.Profile.ControllerIdentityFile, HostKey: c.Profile.ControllerHostKey}, "rpc")
}
func connectionError(ctx context.Context, code string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var detail *domain.Error
	if errors.As(err, &detail) {
		return err
	}
	if code != "CONTROLLER_CONNECT" {
		var parse *json.SyntaxError
		var wrongType *json.UnmarshalTypeError
		if errors.As(err, &parse) || errors.As(err, &wrongType) {
			return domain.E("PROTOCOL_ERROR", "invalid controller response")
		}
	}
	return &domain.Error{Code: code, Message: "controller transport failed", Retryable: true}
}

func (c *Client) request(method string, p any) domain.RPCRequest {
	return domain.RPCRequest{ID: uuid.NewString(), Method: method, Profile: c.Profile.Name, Params: domain.Result(p)}
}
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	n, e := c.open(ctx)
	if e != nil {
		return e
	}
	defer n.Close()
	stop := watchContext(ctx, n)
	defer stop()
	if e = wire.WriteJSON(n, c.request(method, params)); e != nil {
		return connectionError(ctx, "CONTROLLER_IO", e)
	}
	var r domain.RPCResponse
	if e = wire.ReadJSON(bufio.NewReader(n), &r); e != nil {
		return connectionError(ctx, "CONTROLLER_IO", e)
	}
	if r.Error != nil {
		return r.Error
	}
	if out != nil && len(r.Result) > 0 {
		if e = json.Unmarshal(r.Result, out); e != nil {
			return connectionError(ctx, "CONTROLLER_IO", e)
		}
	}
	return nil
}
func (c *Client) streamOpen(ctx context.Context, method string, params any) (io.ReadWriteCloser, *bufio.Reader, map[string]any, error) {
	n, e := c.open(ctx)
	if e != nil {
		return nil, nil, nil, connectionError(ctx, "CONTROLLER_CONNECT", e)
	}
	stop := watchContext(ctx, n)
	defer stop()
	if e = wire.WriteJSON(n, c.request(method, params)); e != nil {
		n.Close()
		return nil, nil, nil, connectionError(ctx, "CONTROLLER_IO", e)
	}
	r := bufio.NewReader(n)
	var response domain.RPCResponse
	if e = wire.ReadJSON(r, &response); e != nil {
		n.Close()
		if ctx.Err() != nil {
			e = ctx.Err()
		}
		return nil, nil, nil, connectionError(ctx, "CONTROLLER_IO", e)
	}
	if response.Error != nil {
		n.Close()
		return nil, nil, nil, response.Error
	}
	m := map[string]any{}
	_ = json.Unmarshal(response.Result, &m)
	return n, r, m, nil
}
func (c *Client) Session(ctx context.Context, target string, opts connect.SessionOptions, in io.Reader, out, errOut io.Writer, resizes <-chan connect.WindowSize, signals <-chan string) (int, error) {
	return c.SessionNode(ctx, target, "", opts, in, out, errOut, resizes, signals)
}
func (c *Client) SessionNode(ctx context.Context, target, node string, opts connect.SessionOptions, in io.Reader, out, errOut io.Writer, resizes <-chan connect.WindowSize, signals <-chan string) (int, error) {
	n, r, _, e := c.streamOpen(ctx, "connect.session", map[string]any{"target": target, "node": node, "options": opts})
	if e != nil {
		return 255, e
	}
	defer n.Close()
	if opts.OnConnected != nil {
		opts.OnConnected()
	}
	stop := watchContext(ctx, n)
	defer stop()
	fw := &wire.Writer{W: n}
	done := make(chan struct{})
	defer close(done)
	if in != nil {
		go func() {
			_, _ = io.Copy(wire.FrameWriter{Writer: fw, Kind: wire.Input}, in)
			_ = fw.Frame(wire.End, nil)
		}()
	} else {
		_ = fw.Frame(wire.End, nil)
	}
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case s, ok := <-resizes:
				if !ok {
					resizes = nil
					continue
				}
				b, _ := json.Marshal(s)
				_ = fw.Frame(wire.Resize, b)
			case s, ok := <-signals:
				if !ok {
					signals = nil
					continue
				}
				_ = fw.Frame(wire.Signal, []byte(s))
			}
		}
	}()
	for {
		k, b, e := wire.ReadFrame(r)
		if e != nil {
			if ctx.Err() != nil {
				return 255, ctx.Err()
			}
			return 255, connectionError(ctx, "CONTROLLER_IO", e)
		}
		switch k {
		case wire.Output:
			if _, e = out.Write(b); e != nil {
				return 255, e
			}
		case wire.Stderr:
			if _, e = errOut.Write(b); e != nil {
				return 255, e
			}
		case wire.Failure:
			var de domain.Error
			if json.Unmarshal(b, &de) == nil {
				return 255, &de
			}
			return 255, domain.E("PROTOCOL_ERROR", "invalid controller session failure frame")
		case wire.Exit:
			var v struct {
				Code *int `json:"code"`
			}
			if e = json.Unmarshal(b, &v); e != nil || v.Code == nil || *v.Code < 0 || *v.Code > 255 {
				return 255, domain.E("PROTOCOL_ERROR", "invalid controller session exit frame")
			}
			return *v.Code, nil
		}
	}
}
func (c *Client) Proxy(ctx context.Context, target string, in io.Reader, out io.Writer) error {
	return c.ProxyNode(ctx, target, "", in, out)
}
func (c *Client) ProxyNode(ctx context.Context, target, node string, in io.Reader, out io.Writer) error {
	n, r, _, e := c.streamOpen(ctx, "connect.proxy", map[string]any{"target": target, "node": node})
	if e != nil {
		return e
	}
	defer n.Close()
	stop := watchContext(ctx, n)
	defer stop()
	go func() {
		if in != nil {
			io.Copy(n, in)
		}
		if cw, ok := n.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	_, e = io.Copy(out, r)
	return e
}
func (c *Client) Stream(ctx context.Context, method string, params any, in io.Reader, out io.Writer) (map[string]any, error) {
	n, r, meta, e := c.streamOpen(ctx, method, params)
	if e != nil {
		return meta, e
	}
	defer n.Close()
	stop := watchContext(ctx, n)
	defer stop()
	fw := &wire.Writer{W: n}
	if in != nil {
		offset := int64(0)
		if v, ok := meta["offset"].(float64); ok {
			offset = int64(v)
		}
		if offset > 0 {
			if s, ok := in.(io.Seeker); ok {
				if _, e = s.Seek(offset, io.SeekStart); e != nil {
					return meta, e
				}
			} else {
				if _, e = io.CopyN(io.Discard, in, offset); e != nil {
					return meta, e
				}
			}
		}
		go func() {
			_, _ = io.Copy(wire.FrameWriter{Writer: fw, Kind: wire.Input}, in)
			_ = fw.Frame(wire.End, nil)
		}()
	}
	for {
		k, b, e := wire.ReadFrame(r)
		if e != nil {
			if ctx.Err() != nil {
				return meta, ctx.Err()
			}
			return meta, e
		}
		switch k {
		case wire.Output:
			if out != nil {
				if _, e = out.Write(b); e != nil {
					return meta, e
				}
			}
		case wire.Progress:
			var m map[string]any
			if json.Unmarshal(b, &m) == nil {
				for k, v := range m {
					meta[k] = v
				}
			}
		case wire.Failure:
			var de domain.Error
			if json.Unmarshal(b, &de) == nil {
				return meta, &de
			}
			return meta, domain.E("REMOTE_ERROR", "stream failed")
		case wire.Exit:
			var v struct {
				Code int `json:"code"`
			}
			_ = json.Unmarshal(b, &v)
			if v.Code != 0 {
				return meta, domain.E("STREAM_FAILED", fmt.Sprintf("remote stream exited %d", v.Code))
			}
			return meta, nil
		}
	}
}
func watchContext(ctx context.Context, c io.Closer) func() {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			c.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}

type commandConn struct {
	client  *ssh.Client
	session *ssh.Session
	in      io.WriteCloser
	out     io.Reader
	once    sync.Once
	done    chan struct{}
}

func (c *commandConn) Read(b []byte) (int, error)  { return c.out.Read(b) }
func (c *commandConn) Write(b []byte) (int, error) { return c.in.Write(b) }
func (c *commandConn) CloseWrite() error           { return c.in.Close() }
func (c *commandConn) Close() error {
	var e error
	c.once.Do(func() {
		if c.done != nil {
			close(c.done)
		}
		c.in.Close()
		c.session.Close()
		e = c.client.Close()
	})
	return e
}
func OpenCommand(ctx context.Context, r domain.RelayConfig, command string) (io.ReadWriteCloser, error) {
	cl, e := connect.DialRelay(ctx, r)
	if e != nil {
		return nil, e
	}
	s, e := cl.NewSession()
	if e != nil {
		cl.Close()
		return nil, e
	}
	in, e := s.StdinPipe()
	if e != nil {
		s.Close()
		cl.Close()
		return nil, e
	}
	out, e := s.StdoutPipe()
	if e != nil {
		s.Close()
		cl.Close()
		return nil, e
	}
	s.Stderr = io.Discard
	if e = s.Start(command); e != nil {
		s.Close()
		cl.Close()
		return nil, e
	}
	n := &commandConn{client: cl, session: s, in: in, out: out, done: make(chan struct{})}
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-n.done:
				return
			case <-ctx.Done():
				n.Close()
				return
			case <-t.C:
				if _, _, e := cl.SendRequest("keepalive@openssh.com", true, nil); e != nil {
					n.Close()
					return
				}
			}
		}
	}()
	return n, nil
}

// GenericRelayRPC uses a restricted SSH command, not an interactive server shell.
func GenericRelayRPC(ctx context.Context, r domain.RelayConfig, request domain.RPCRequest, out any) error {
	n, e := OpenCommand(ctx, r, "rpc")
	if e != nil {
		return e
	}
	defer n.Close()
	stop := watchContext(ctx, n)
	defer stop()
	if e = wire.WriteJSON(n, request); e != nil {
		return e
	}
	var response domain.RPCResponse
	if e = wire.ReadJSON(bufio.NewReader(n), &response); e != nil {
		return e
	}
	if response.Error != nil {
		return response.Error
	}
	if out != nil {
		return json.Unmarshal(response.Result, out)
	}
	return nil
}
