package connect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/domain"
	"golang.org/x/crypto/ssh"
)

const handshakeTimeout = 15 * time.Second

var leaseKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,191}$`)

// ParseSSHAddress supports ssh://user@host:port, host:port, and bare IP/host.
// It does not consult shell aliases, SSH shell wrappers, or the platform API.
func ParseSSHAddress(address, user string) (string, string, error) {
	if address == "" {
		return "", "", domain.E("INVALID_RELAY", "relay address is empty")
	}
	if strings.ContainsAny(address, "\r\n\x00") {
		return "", "", domain.E("INVALID_RELAY", "invalid relay address")
	}
	if strings.Contains(address, "://") {
		u, err := url.Parse(address)
		if err != nil || u.Scheme != "ssh" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return "", "", domain.E("INVALID_RELAY", "relay must be an ssh:// address")
		}
		if u.User != nil {
			if _, set := u.User.Password(); set {
				return "", "", domain.E("INVALID_RELAY", "passwords must not appear in relay URLs")
			}
			if user != "" && user != u.User.Username() {
				return "", "", domain.E("INVALID_RELAY", "relay usernames conflict")
			}
			user = u.User.Username()
		}
		address = u.Host
	}
	if user == "" || strings.ContainsAny(user, "\r\n\x00") {
		return "", "", domain.E("INVALID_RELAY", "relay username is required")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
			address = net.JoinHostPort(strings.Trim(address, "[]"), "22")
		} else if net.ParseIP(address) != nil || !strings.Contains(address, ":") {
			address = net.JoinHostPort(address, "22")
		} else {
			return "", "", domain.E("INVALID_RELAY", "invalid relay host or port")
		}
	}
	h, p, err := net.SplitHostPort(address)
	if err != nil || h == "" {
		return "", "", domain.E("INVALID_RELAY", "invalid relay address")
	}
	n, err := strconv.Atoi(p)
	if err != nil || n < 1 || n > 65535 {
		return "", "", domain.E("INVALID_RELAY", "invalid relay port")
	}
	return address, user, nil
}

func dialWithConfig(ctx context.Context, address string, config *ssh.ClientConfig) (*ssh.Client, net.Conn, error) {
	dialer := net.Dialer{Timeout: handshakeTimeout}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, nil, fmt.Errorf("SSH transport: %w", err)
	}
	deadline := time.Now().Add(handshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	c, ch, r, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		stop()
		conn.Close()
		return nil, nil, fmt.Errorf("SSH handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, ch, r)
	go func() { _ = client.Wait(); stop() }()
	return client, conn, nil
}

func DialRelay(ctx context.Context, r domain.RelayConfig) (*ssh.Client, error) {
	addr, user, err := ParseSSHAddress(r.Address, r.User)
	if err != nil {
		return nil, err
	}
	signer, err := LoadSigner(r.IdentityFile, nil)
	if err != nil {
		return nil, err
	}
	verify, err := relayHostKeyCallback(r)
	if err != nil {
		return nil, err
	}
	client, _, err := dialWithConfig(ctx, addr, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: verify, HostKeyAlgorithms: pinnedHostKeyAlgorithms(r.HostKey), Timeout: handshakeTimeout})
	if err == nil {
		keepAlive(ctx, client)
	}
	return client, err
}

// DialTransport returns the raw target TCP stream. In relay mode, the relay
// gateway must authorize the opaque lease key and emit only SSH bytes on stdout.
func DialTransport(ctx context.Context, route domain.Route) (net.Conn, error) {
	if route.Kind == "relay" {
		if !leaseKeyPattern.MatchString(route.RelayLeaseKey) {
			return nil, domain.E("INVALID_ROUTE", "relay route has no valid lease key")
		}
		client, err := DialRelay(ctx, route.Relay)
		if err != nil {
			return nil, err
		}
		session, err := client.NewSession()
		if err != nil {
			client.Close()
			return nil, err
		}
		stdin, err := session.StdinPipe()
		if err != nil {
			session.Close()
			client.Close()
			return nil, err
		}
		stdout, err := session.StdoutPipe()
		if err != nil {
			session.Close()
			client.Close()
			return nil, err
		}
		stderr := new(lockedBuffer)
		session.Stderr = stderr
		if err = session.Start("proxy " + route.RelayLeaseKey); err != nil {
			session.Close()
			client.Close()
			return nil, err
		}
		conn := &sessionConn{session: session, client: client, in: stdin, out: stdout, stderr: stderr, done: make(chan struct{}), address: "relay:" + route.RelayLeaseKey}
		stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
		go func() { conn.waitErr = session.Wait(); stopCancel(); close(conn.done) }()
		return conn, nil
	}
	if route.Kind != "" && route.Kind != "direct" && route.Kind != "mesh" {
		return nil, domain.E("INVALID_ROUTE", "unsupported connection route kind")
	}
	if route.Host == "" || route.Port < 1 || route.Port > 65535 {
		return nil, domain.E("INVALID_ROUTE", "target SSH host and port are required")
	}
	return (&net.Dialer{Timeout: handshakeTimeout}).DialContext(ctx, "tcp", net.JoinHostPort(route.Host, strconv.Itoa(route.Port)))
}

// DialSSH authenticates using only the saved route and key. HTTP cookies and
// platform login state deliberately do not participate in this path.
func DialSSH(ctx context.Context, route domain.Route) (*ssh.Client, error) {
	if route.Username == "" {
		return nil, domain.E("INVALID_ROUTE", "target SSH username is required")
	}
	verify, err := PinnedHostKeyCallback(route.HostKey)
	if err != nil {
		return nil, err
	}
	signer, err := LoadSigner(route.IdentityFile, nil)
	if err != nil {
		return nil, err
	}
	conn, err := DialTransport(ctx, route)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	deadline := time.Now().Add(handshakeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	addr := net.JoinHostPort(route.Host, strconv.Itoa(route.Port))
	if route.Kind == "relay" {
		addr = "bupt-instance-" + string(route.InstanceID)
	}
	c, ch, req, err := ssh.NewClientConn(conn, addr, &ssh.ClientConfig{User: route.Username, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: verify, HostKeyAlgorithms: pinnedHostKeyAlgorithms(route.HostKey), Timeout: handshakeTimeout})
	if err != nil {
		stop()
		conn.Close()
		return nil, fmt.Errorf("instance SSH handshake: %w", err)
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(c, ch, req)
	go func() { _ = client.Wait(); stop() }()
	keepAlive(ctx, client)
	return client, nil
}

func keepAlive(ctx context.Context, client *ssh.Client) {
	done := make(chan struct{})
	go func() { _ = client.Wait(); close(done) }()
	go func() {
		ticker := time.NewTicker(20 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				client.Close()
				return
			case <-done:
				return
			case <-ticker.C:
				answered := make(chan error, 1)
				go func() { _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); answered <- err }()
				timer := time.NewTimer(60 * time.Second)
				select {
				case err := <-answered:
					timer.Stop()
					if err != nil {
						client.Close()
						return
					}
				case <-timer.C:
					client.Close()
					return
				case <-ctx.Done():
					timer.Stop()
					client.Close()
					return
				case <-done:
					timer.Stop()
					return
				}
			}
		}
	}()
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if b.b.Len() < 4096 {
		left := 4096 - b.b.Len()
		if len(p) > left {
			p = p[:left]
		}
		_, _ = b.b.Write(p)
	}
	return n, nil
}
func (b *lockedBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type namedAddr string

func (a namedAddr) Network() string { return "ssh" }
func (a namedAddr) String() string  { return string(a) }

type sessionConn struct {
	session       *ssh.Session
	client        *ssh.Client
	in            io.WriteCloser
	out           io.Reader
	stderr        *lockedBuffer
	address       string
	closeOnce     sync.Once
	done          chan struct{}
	waitErr       error
	deadlineMu    sync.Mutex
	deadlineTimer *time.Timer
}

func (c *sessionConn) Read(p []byte) (int, error) {
	n, err := c.out.Read(p)
	if n == 0 && errors.Is(err, io.EOF) {
		select {
		case <-c.done:
			if c.waitErr != nil {
				return 0, domain.E("RELAY_PROXY_FAILED", "relay refused or closed the requested connection")
			}
		default:
		}
	}
	return n, err
}
func (c *sessionConn) Write(p []byte) (int, error) { return c.in.Write(p) }
func (c *sessionConn) CloseWrite() error           { return c.in.Close() }
func (c *sessionConn) Close() error {
	var result error
	c.closeOnce.Do(func() {
		c.deadlineMu.Lock()
		if c.deadlineTimer != nil {
			c.deadlineTimer.Stop()
		}
		c.deadlineMu.Unlock()
		c.in.Close()
		c.session.Close()
		result = c.client.Close()
	})
	return result
}
func (c *sessionConn) LocalAddr() net.Addr  { return namedAddr("bupt-client") }
func (c *sessionConn) RemoteAddr() net.Addr { return namedAddr(c.address) }
func (c *sessionConn) SetDeadline(t time.Time) error {
	c.deadlineMu.Lock()
	defer c.deadlineMu.Unlock()
	if c.deadlineTimer != nil {
		c.deadlineTimer.Stop()
		c.deadlineTimer = nil
	}
	if !t.IsZero() {
		c.deadlineTimer = time.AfterFunc(time.Until(t), func() { c.Close() })
	}
	return nil
}
func (c *sessionConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *sessionConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }
