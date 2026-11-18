package connect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type sshFixture struct {
	addr             string
	hostKey          string
	keyFile          string
	home             string
	listener         net.Listener
	mu               sync.Mutex
	connections      map[net.Conn]bool
	execs            chan string
	ptys             chan string
	windows          chan WindowSize
	signals          chan string
	proxyDestination string
}

func signerForTest(t *testing.T) ssh.Signer {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
func newSSHFixture(t *testing.T) *sshFixture {
	t.Helper()
	f := &sshFixture{home: t.TempDir(), connections: map[net.Conn]bool{}, execs: make(chan string, 20), ptys: make(chan string, 20), windows: make(chan WindowSize, 20), signals: make(chan string, 20)}
	f.keyFile = filepath.Join(t.TempDir(), "client")
	public, err := EnsureIdentity(f.keyFile, "test")
	if err != nil {
		t.Fatal(err)
	}
	clientPub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(public))
	if err != nil {
		t.Fatal(err)
	}
	host := signerForTest(t)
	f.hostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(host.PublicKey())))
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(clientPub.Marshal(), key.Marshal()) {
			return nil, fmt.Errorf("unknown client")
		}
		return &ssh.Permissions{}, nil
	}}
	config.AddHostKey(host)
	// Real OpenSSH servers commonly expose ECDSA and Ed25519 together. A
	// pinned Ed25519 client must negotiate that key instead of the default.
	ecdsaKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	alternative, err := ssh.NewSignerFromKey(ecdsaKey)
	if err != nil {
		t.Fatal(err)
	}
	config.AddHostKey(alternative)
	f.listener, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f.addr = f.listener.Addr().String()
	t.Cleanup(func() {
		f.listener.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for c := range f.connections {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := f.listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.connections[c] = true
			f.mu.Unlock()
			go f.serve(c, config)
		}
	}()
	return f
}
func (f *sshFixture) serve(c net.Conn, config *ssh.ServerConfig) {
	defer func() { c.Close(); f.mu.Lock(); delete(f.connections, c); f.mu.Unlock() }()
	conn, channels, requests, err := ssh.NewServerConn(c, config)
	if err != nil {
		return
	}
	defer conn.Close()
	go func() {
		for r := range requests {
			if r.WantReply {
				r.Reply(true, nil)
			}
		}
	}()
	var workers sync.WaitGroup
	for n := range channels {
		switch n.ChannelType() {
		case "session":
			ch, reqs, err := n.Accept()
			if err != nil {
				continue
			}
			workers.Add(1)
			go func() { defer workers.Done(); f.session(ch, reqs) }()
		case "direct-tcpip":
			var request struct {
				Host       string
				Port       uint32
				Origin     string
				OriginPort uint32
			}
			if ssh.Unmarshal(n.ExtraData(), &request) != nil || request.Host != "127.0.0.1" {
				n.Reject(ssh.Prohibited, "loopback only")
				continue
			}
			target, err := net.Dial("tcp", net.JoinHostPort(request.Host, strconv.Itoa(int(request.Port))))
			if err != nil {
				n.Reject(ssh.ConnectionFailed, "not available")
				continue
			}
			ch, reqs, err := n.Accept()
			if err != nil {
				target.Close()
				continue
			}
			go ssh.DiscardRequests(reqs)
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer ch.Close()
				defer target.Close()
				done := make(chan struct{}, 2)
				go func() { io.Copy(ch, target); ch.CloseWrite(); done <- struct{}{} }()
				go func() {
					io.Copy(target, ch)
					if tc, ok := target.(*net.TCPConn); ok {
						tc.CloseWrite()
					}
					done <- struct{}{}
				}()
				<-done
				<-done
			}()
		default:
			n.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
	workers.Wait()
}
func (f *sshFixture) session(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	done := make(chan struct{})
	defer close(done)
	var commandStarted sync.Once
	for req := range requests {
		switch req.Type {
		case "env":
			req.Reply(true, nil)
		case "pty-req":
			var p struct {
				Term                      string
				Cols, Rows, Width, Height uint32
				Modes                     string
			}
			_ = ssh.Unmarshal(req.Payload, &p)
			f.ptys <- p.Term
			req.Reply(true, nil)
		case "window-change":
			var w struct{ Cols, Rows, Width, Height uint32 }
			_ = ssh.Unmarshal(req.Payload, &w)
			f.windows <- WindowSize{Rows: int(w.Rows), Cols: int(w.Cols)}
		case "signal":
			var s struct{ Name string }
			_ = ssh.Unmarshal(req.Payload, &s)
			f.signals <- s.Name
			if s.Name == "TERM" {
				sendExit(ch, 143)
				return
			}
		case "exec":
			var e struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &e)
			f.execs <- e.Command
			req.Reply(true, nil)
			commandStarted.Do(func() {
				go func() {
					switch {
					case e.Command == testEnrollmentCommand():
						runTestEnrollment(ch, f.home)
					case e.Command == "'exit' '37'":
						io.WriteString(ch, "stdout\n")
						io.WriteString(ch.Stderr(), "stderr\n")
						sendExit(ch, 37)
					case e.Command == "'cat'":
						io.Copy(ch, ch)
						sendExit(ch, 0)
					case e.Command == "'hold'":
						<-done
					case strings.HasPrefix(e.Command, "proxy ") && f.proxyDestination != "":
						if e.Command != "proxy lease-1" {
							sendExit(ch, 1)
							return
						}
						target, err := net.Dial("tcp", f.proxyDestination)
						if err != nil {
							sendExit(ch, 1)
							return
						}
						defer target.Close()
						copies := make(chan struct{}, 2)
						go func() {
							io.Copy(target, ch)
							if tcp, ok := target.(*net.TCPConn); ok {
								tcp.CloseWrite()
							}
							copies <- struct{}{}
						}()
						go func() { io.Copy(ch, target); ch.CloseWrite(); copies <- struct{}{} }()
						<-copies
						<-copies
						sendExit(ch, 0)
					default:
						sendExit(ch, 0)
					}
				}()
			})
		case "shell":
			req.Reply(true, nil)
			io.WriteString(ch, "shell-ready\n")
			sendExit(ch, 0)
			return
		case "subsystem":
			var s struct{ Name string }
			_ = ssh.Unmarshal(req.Payload, &s)
			if s.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			server, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(f.home))
			if err == nil {
				_ = server.Serve()
				server.Close()
			}
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}
func sendExit(ch ssh.Channel, status uint32) {
	ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
	ch.Close()
}
func (f *sshFixture) route() domain.Route {
	host, port, _ := net.SplitHostPort(f.addr)
	var p int
	fmt.Sscan(port, &p)
	return domain.Route{Profile: "expired-school-session", InstanceID: "123", NodeID: "456", Username: "student", Kind: "direct", Host: host, Port: p, HostKey: f.hostKey, IdentityFile: f.keyFile, Home: f.home}
}

func TestRunUsesPinnedSSHWithoutSchoolAuthentication(t *testing.T) {
	f := newSSHFixture(t)
	var stdout, stderr bytes.Buffer
	code, err := Run(context.Background(), f.route(), SessionOptions{Command: []string{"exit", "37"}}, nil, &stdout, &stderr, nil, nil)
	if err != nil || code != 37 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if stdout.String() != "stdout\n" || stderr.String() != "stderr\n" {
		t.Fatalf("unexpected streams %q %q", stdout.String(), stderr.String())
	}
	wrong := f.route()
	wrong.HostKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signerForTest(t).PublicKey())))
	_, err = DialSSH(context.Background(), wrong)
	var typed *domain.Error
	if !errors.As(err, &typed) || typed.Code != "SSH_HOST_KEY_MISMATCH" {
		t.Fatalf("missing mismatch error: %v", err)
	}
	wrong.HostKey = ""
	_, err = DialSSH(context.Background(), wrong)
	if !errors.As(err, &typed) || typed.Code != "SSH_HOST_KEY_REQUIRED" {
		t.Fatalf("missing pin was accepted: %v", err)
	}
}
func TestPTYResizeSignalAndTermFallback(t *testing.T) {
	f := newSSHFixture(t)
	resize := make(chan WindowSize)
	signals := make(chan string)
	type result struct {
		code int
		err  error
	}
	ended := make(chan result, 1)
	go func() {
		code, err := Run(context.Background(), f.route(), SessionOptions{Command: []string{"hold"}, PTY: true, Term: "xterm-ghostty", Rows: 24, Cols: 80}, nil, io.Discard, io.Discard, resize, signals)
		ended <- result{code, err}
	}()
	select {
	case term := <-f.ptys:
		if term != "xterm-256color" {
			t.Fatal(term)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no pty")
	}
	<-f.execs
	resize <- WindowSize{Rows: 50, Cols: 140}
	select {
	case size := <-f.windows:
		if size.Rows != 50 || size.Cols != 140 {
			t.Fatal(size)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no resize")
	}
	signals <- "SIGTERM"
	select {
	case got := <-f.signals:
		if got != "TERM" {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no signal")
	}
	select {
	case r := <-ended:
		if r.err != nil || r.code != 143 {
			t.Fatalf("%+v", r)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("session did not finish")
	}
}
func TestSessionCancellation(t *testing.T) {
	f := newSSHFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()
	start := time.Now()
	code, err := Run(ctx, f.route(), SessionOptions{Command: []string{"hold"}}, nil, io.Discard, io.Discard, nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) || code != 255 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancellation blocked")
	}
}
func TestRelayProxyPreservesAllBytes(t *testing.T) {
	target := newSSHFixture(t)
	relay := newSSHFixture(t)
	relay.proxyDestination = target.addr
	route := target.route()
	route.Kind = "relay"
	route.RelayLeaseKey = "lease-1"
	route.Relay = domain.RelayConfig{Address: relay.addr, User: "operator", IdentityFile: relay.keyFile, HostKey: relay.hostKey}
	data := bytes.Repeat([]byte{0, 1, 2, 3, 10, 13, 27, 127, 128, 200, 255}, 10000)
	var output bytes.Buffer
	code, err := Run(context.Background(), route, SessionOptions{Command: []string{"cat"}}, bytes.NewReader(data), &output, io.Discard, nil, nil)
	if err != nil || code != 0 {
		t.Fatalf("code=%d err=%v", code, err)
	}
	if !bytes.Equal(output.Bytes(), data) {
		t.Fatalf("proxy corrupted bytes: %d != %d", output.Len(), len(data))
	}
	select {
	case command := <-relay.execs:
		if command != "proxy lease-1" {
			t.Fatal(command)
		}
	case <-time.After(time.Second):
		t.Fatal("no relay exec")
	}
	route.RelayLeaseKey = "lease-1; touch /tmp/no"
	if _, err := DialTransport(context.Background(), route); err == nil {
		t.Fatal("unsafe lease key accepted")
	}
}
func TestShellArgumentsAreLiteral(t *testing.T) {
	created := filepath.Join(t.TempDir(), "should-not-exist")
	value := "a' $(touch " + created + ");\nUnicode 中"
	command, err := ShellCommand([]string{"printf", "%s", value})
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/sh", "-c", command).Output()
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != value {
		t.Fatalf("changed argument: %q", out)
	}
	if _, err = os.Stat(created); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("shell injection occurred")
	}
	if _, err = ShellCommand([]string{"x", "\x00"}); err == nil {
		t.Fatal("NUL accepted")
	}
}
func TestSFTPAtomicUploadAndDownload(t *testing.T) {
	f := newSSHFixture(t)
	route := f.route()
	data := bytes.Repeat([]byte{0, 255, 3, 'x'}, 4000)
	n, err := Upload(context.Background(), route, "~/dir/data.bin", bytes.NewReader(data), FileOptions{Mkdir: true, Mode: 0640})
	if err != nil || n != int64(len(data)) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var out bytes.Buffer
	n, err = Download(context.Background(), route, "dir/data.bin", &out)
	if err != nil || !bytes.Equal(out.Bytes(), data) {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, err = Upload(context.Background(), route, "dir/data.bin", strings.NewReader("replace"), FileOptions{}); err == nil {
		t.Fatal("overwrote existing file")
	}
	if _, err = Upload(context.Background(), route, "dir/data.bin", strings.NewReader("replace"), FileOptions{Overwrite: true}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(f.home, "dir/data.bin"))
	if string(got) != "replace" {
		t.Fatal(string(got))
	}
	entries, err := List(context.Background(), route, "~/dir")
	if err != nil || len(entries) != 1 || entries[0].Name != "data.bin" {
		t.Fatalf("%+v %v", entries, err)
	}
	raceTarget := filepath.Join(f.home, "race")
	reader := &callbackReader{Reader: strings.NewReader("ours"), callback: func() {
		if err := os.WriteFile(raceTarget, []byte("theirs"), 0600); err != nil {
			t.Error(err)
		}
	}}
	_, err = Upload(context.Background(), route, "race", reader, FileOptions{})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "ALREADY_EXISTS" {
		t.Fatalf("race not detected: %v", err)
	}
	got, _ = os.ReadFile(raceTarget)
	if string(got) != "theirs" {
		t.Fatal("concurrent destination was overwritten")
	}
}

func TestSFTPMetadataAndSafeRemoval(t *testing.T) {
	f := newSSHFixture(t)
	route := f.route()
	ctx := context.Background()
	if err := Mkdir(ctx, route, "tree/nested", true, 0750); err != nil {
		t.Fatal(err)
	}
	if err := Mkdir(ctx, route, "tree/nested", true, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := Stat(ctx, route, "tree/nested")
	if err != nil || !info.IsDir || info.Path != filepath.Join(f.home, "tree/nested") || info.Mode&0777 != 0750 {
		t.Fatalf("stat: %+v %v", info, err)
	}
	if _, err = Upload(ctx, route, "tree/nested/first", strings.NewReader("first"), FileOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = Upload(ctx, route, "tree/nested/second", strings.NewReader("second"), FileOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = Rename(ctx, route, "tree/nested/first", "tree/nested/second", false); err == nil {
		t.Fatal("rename replaced existing destination")
	}
	if err = Rename(ctx, route, "tree/nested/first", "tree/nested/moved", false); err != nil {
		t.Fatal(err)
	}
	if err = Rename(ctx, route, "tree/nested/moved", "tree/nested/second", true); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(f.home, "tree/nested/second"))
	if err != nil || string(got) != "first" {
		t.Fatalf("renamed contents: %q %v", got, err)
	}
	for _, name := range []string{"/", "~", "tree/.."} {
		err = Remove(ctx, route, name, true)
		var de *domain.Error
		if !errors.As(err, &de) || de.Code != "PROTECTED_PATH" {
			t.Fatalf("unprotected %s: %v", name, err)
		}
	}
	outside := t.TempDir()
	if err = os.WriteFile(filepath.Join(outside, "keep"), []byte("survives"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(f.home, "tree/link")); err != nil {
		t.Fatal(err)
	}
	info, err = Stat(ctx, route, "tree/link")
	if err != nil || !info.IsSymlink || info.IsDir {
		t.Fatalf("symlink stat: %+v %v", info, err)
	}
	if err = Remove(ctx, route, "tree", false); err == nil {
		t.Fatal("removed non-empty directory without recursive")
	}
	if err = Remove(ctx, route, "tree", true); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(outside, "keep"))
	if err != nil || string(got) != "survives" {
		t.Fatalf("followed symlink during recursive removal: %q %v", got, err)
	}
}

type callbackReader struct {
	io.Reader
	once     sync.Once
	callback func()
}

func (r *callbackReader) Read(p []byte) (int, error) { r.once.Do(r.callback); return r.Reader.Read(p) }
func TestForward(t *testing.T) {
	f := newSSHFixture(t)
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	local, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan error, 1)
	go func() { ended <- Forward(ctx, f.route(), local, echo.Addr().String()) }()
	c, err := net.Dial("tcp", local.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	payload := []byte{0, 255, 10, 13, 'a', 'b'}
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err = c.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err = io.ReadFull(c, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, got) {
		t.Fatal(got)
	}
	cancel()
	select {
	case err := <-ended:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("forward did not stop")
	}
}
func TestKeysAndPaths(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "identity")
	first, err := EnsureIdentity(filename, "a\nb")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filename)
	again, err := EnsureIdentity(filename, "new")
	if err != nil || again != first {
		t.Fatalf("%q %v", again, err)
	}
	after, _ := os.ReadFile(filename)
	if !bytes.Equal(before, after) {
		t.Fatal("identity replaced")
	}
	os.Chmod(filename, 0644)
	if _, err = LoadSigner(filename, nil); err == nil {
		t.Fatal("broad private-key permissions accepted")
	}
	if _, err = CanonicalPublicKey("command=\"bad\" " + first); err == nil {
		t.Fatal("authorized-key options accepted")
	}
	if _, err = CanonicalPublicKey(first + "\n" + first); err == nil {
		t.Fatal("multiple keys accepted")
	}
	for in, want := range map[string]string{"~": "/home/test", "~/x": "/home/test/x", "x": "/home/test/x", "/tmp/a": "/tmp/a"} {
		got, e := RemotePath("/home/test", in)
		if e != nil || got != want {
			t.Fatalf("%s => %s %v", in, got, e)
		}
	}
	for _, in := range []string{"", "~other/x", "bad\x00"} {
		if _, err = RemotePath("/home/test", in); err == nil {
			t.Fatal(in)
		}
	}
}

func TestConcurrentIdentityCreation(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "identity")
	var wg sync.WaitGroup
	keys := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			key, err := EnsureIdentity(filename, "test")
			if err != nil {
				t.Error(err)
				return
			}
			keys <- key
		}()
	}
	wg.Wait()
	close(keys)
	first := ""
	for key := range keys {
		if first == "" {
			first = key
		}
		if key != first {
			t.Fatal("concurrent prepare changed the controller identity")
		}
	}
	if first == "" {
		t.Fatal("no identity was created")
	}
}
func TestParseSSHAddress(t *testing.T) {
	for _, test := range []struct{ in, user, address, wantUser string }{{"ssh://alice@example.com:2222", "", "example.com:2222", "alice"}, {"127.0.0.1", "u", "127.0.0.1:22", "u"}, {"::1", "u", "[::1]:22", "u"}} {
		a, u, err := ParseSSHAddress(test.in, test.user)
		if err != nil || a != test.address || u != test.wantUser {
			t.Fatalf("%+v => %s %s %v", test, a, u, err)
		}
	}
	for _, bad := range []string{"ssh://u:p@host", "ssh://u@host/path", "ssh://u@host?x=1", "host:0", "host:70000", "http://u@host"} {
		if _, _, err := ParseSSHAddress(bad, ""); err == nil {
			t.Fatal(bad)
		}
	}
}

func readConsoleProgram(r io.Reader) (string, string, error) {
	scanner := bufio.NewScanner(r)
	var encoded strings.Builder
	var echo strings.Builder
	delimiter := ""
	for scanner.Scan() {
		line := scanner.Text()
		echo.WriteString(line + "\n")
		if strings.HasPrefix(line, "python3 - <<'") {
			delimiter = strings.TrimSuffix(strings.TrimPrefix(line, "python3 - <<'"), "'")
			continue
		}
		if delimiter != "" && line == delimiter {
			break
		}
		if regexp.MustCompile(`^'[A-Za-z0-9+/=]+'$`).MatchString(line) {
			encoded.WriteString(strings.Trim(line, "'"))
		}
	}
	program, err := base64.StdEncoding.DecodeString(encoded.String())
	return string(program), echo.String(), err
}
func TestConsoleMarkersAndTimeout(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		program, echo, err := readConsoleProgram(b)
		if err != nil {
			t.Error(err)
			return
		}
		id := regexp.MustCompile(`BUPT_RESULT_([0-9a-f]{32}):`).FindStringSubmatch(program)
		if len(id) != 2 {
			t.Error("no marker")
			return
		}
		io.WriteString(b, echo)
		io.WriteString(b, "BUPT_RESULT_old:e30=:BUPT_END_old\r\n")
		data, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]string{"status": "ready"}})
		text := "\r\nBUPT_RESULT_" + id[1] + ":" + base64.StdEncoding.EncodeToString(data) + ":BUPT_END_" + id[1] + "\r\n"
		for _, part := range []string{text[:20], text[20:]} {
			io.WriteString(b, part)
		}
	}()
	var result map[string]string
	if err := ExecuteConsole(context.Background(), a, "result={'status':'ready'}\n", &result, time.Second); err != nil || result["status"] != "ready" {
		t.Fatalf("%v %+v", err, result)
	}
	silent, peer := net.Pipe()
	defer silent.Close()
	defer peer.Close()
	go func() { _, _, _ = readConsoleProgram(peer) }()
	err := ExecuteConsole(context.Background(), silent, "result={}\n", nil, 50*time.Millisecond)
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != "BOOTSTRAP_TIMEOUT" {
		t.Fatalf("unexpected timeout: %v", err)
	}
}
func TestBootstrapPreservesExistingAuthorization(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 unavailable")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil || keygen == "" {
		t.Skip("ssh-keygen unavailable")
	}
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	os.Mkdir(sshDir, 0700)
	old := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signerForTest(t).PublicKey()))) + " platform-existing\n"
	os.WriteFile(filepath.Join(sshDir, "authorized_keys"), []byte(old), 0600)
	client := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signerForTest(t).PublicKey())))
	hostKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signerForTest(t).PublicKey())))
	req := BootstrapRequest{Profile: "p", InstanceID: "123", NodeID: "456", ExpectedUsername: "tester", PublicKeys: []string{client, client}, SSHPort: 22}
	program, err := PrepareProgram(req)
	if err != nil {
		t.Fatal(err)
	}
	program = strings.TrimSuffix(program, "\nresult=prepare(spec)\n")
	h, _ := json.Marshal(home)
	k, _ := json.Marshal(hostKey)
	harness := "import pwd,types,os,json\npwd.getpwuid=lambda uid:types.SimpleNamespace(pw_name='tester',pw_dir=" + string(h) + ")\n" + program + "\ndiscover_host_key=lambda port:" + string(k) + "\nresult=prepare(spec)\nprint(json.dumps(result))\n"
	run := func() BootstrapResult {
		t.Helper()
		c := exec.Command(python, "-c", harness)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v %s", err, out)
		}
		var got BootstrapResult
		if err = json.Unmarshal(out, &got); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		return got
	}
	first := run()
	second := run()
	if first.AddedKeys != 1 || second.AddedKeys != 0 {
		t.Fatalf("not idempotent: %+v %+v", first, second)
	}
	contents, _ := os.ReadFile(filepath.Join(sshDir, "authorized_keys"))
	if !bytes.HasPrefix(contents, []byte(old)) || bytes.Count(contents, []byte(strings.Fields(client)[1])) != 1 {
		t.Fatal("existing authorization changed or duplicate added")
	}
	if first.Home != home || first.Username != "tester" || first.HostKey != hostKey || first.TunnelPublicKey == "" {
		t.Fatalf("%+v", first)
	}
	info, _ := os.Stat(first.TunnelKeyPath)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	matches, _ := filepath.Glob(filepath.Join(first.StateDir, "authorized_keys.before-*"))
	if len(matches) != 1 {
		t.Fatal(matches)
	}
	cfg := TunnelConfig{RelayHost: "example.com", RelayPort: 2222, RelayUser: "bupt", RelayHostKey: hostKey, Port: 24000, TargetPort: 22, LeaseKey: "lease-1"}
	configProgram, err := TunnelProgram("configure", first, cfg)
	if err != nil {
		t.Fatal(err)
	}
	testConfigure := "import pwd,types,os,json\npwd.getpwuid=lambda uid:types.SimpleNamespace(pw_name='tester',pw_dir=" + string(h) + ")\n" + configProgram + "\nprint(json.dumps(result))\n"
	out, err := exec.Command(python, "-c", testConfigure).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	var status TunnelStatus
	if err = json.Unmarshal(out, &status); err != nil || status.State != "configured" {
		t.Fatalf("%v %s", err, out)
	}
	configData, _ := os.ReadFile(filepath.Join(first.StateDir, "tunnel.json"))
	if !bytes.Contains(configData, []byte("StrictHostKeyChecking=yes")) || !bytes.Contains(configData, []byte("127.0.0.1:24000:127.0.0.1:22")) {
		t.Fatal(string(configData))
	}
	runner, err := os.ReadFile(filepath.Join(first.StateDir, "run_tunnel.py"))
	if err != nil {
		t.Fatal(err)
	}
	c := exec.Command(python, "-c", "import ast,sys;ast.parse(sys.stdin.read())")
	c.Stdin = bytes.NewReader(runner)
	if out, err = c.CombinedOutput(); err != nil {
		t.Fatalf("runner syntax: %v %s", err, out)
	}
}
