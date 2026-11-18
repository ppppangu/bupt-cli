package control

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"bupt.local/cli/internal/state"
	"bupt.local/cli/internal/wire"
	"github.com/google/uuid"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type streamTestVault struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (v *streamTestVault) Get(name string, out any) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	b, ok := v.values[name]
	if !ok {
		return os.ErrNotExist
	}
	return json.Unmarshal(b, out)
}
func (v *streamTestVault) Put(name string, value any) error {
	b, e := json.Marshal(value)
	if e != nil {
		return e
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.values[name] = b
	return nil
}
func (v *streamTestVault) Delete(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.values, name)
	return nil
}
func streamTestController(t *testing.T, profile domain.Profile, factory func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error)) *Controller {
	t.Helper()
	profile.Name = "test"
	if profile.PlatformURL == "" {
		profile.PlatformURL = "https://example.invalid"
	}
	stateDir := t.TempDir()
	if e := os.Chmod(stateDir, 0700); e != nil {
		t.Fatal(e)
	}
	store, e := state.Open(stateDir)
	if e != nil {
		t.Fatal(e)
	}
	c, e := New(Options{Config: &config.Config{Selected: "test", Profiles: map[string]domain.Profile{"test": profile}}, Store: store, Vault: &streamTestVault{values: map[string][]byte{}}, Factory: factory})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close(); store.Close() })
	return c
}
func streamTestParams(value any) Params { p, _ := Decode(domain.Result(value)); return p }

type streamTestSSH struct {
	listener        net.Listener
	home, key, host string
	mu              sync.Mutex
	conns           map[net.Conn]bool
}

func newStreamTestSSH(t *testing.T) *streamTestSSH {
	t.Helper()
	f := &streamTestSSH{home: t.TempDir(), key: filepath.Join(t.TempDir(), "identity"), conns: map[net.Conn]bool{}}
	pub, e := connect.EnsureIdentity(f.key, "stream-test")
	if e != nil {
		t.Fatal(e)
	}
	clientKey, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(pub))
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(private)
	if e != nil {
		t.Fatal(e)
	}
	f.host = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, p ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(p.Marshal(), clientKey.Marshal()) {
			return nil, fmt.Errorf("unauthorized")
		}
		return &ssh.Permissions{}, nil
	}}
	cfg.AddHostKey(signer)
	f.listener, e = net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		f.listener.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for conn := range f.conns {
			conn.Close()
		}
	})
	go func() {
		for {
			n, e := f.listener.Accept()
			if e != nil {
				return
			}
			f.mu.Lock()
			f.conns[n] = true
			f.mu.Unlock()
			go f.serve(n, cfg)
		}
	}()
	return f
}
func (f *streamTestSSH) route() domain.Route {
	host, port, _ := net.SplitHostPort(f.listener.Addr().String())
	n, _ := strconv.Atoi(port)
	return domain.Route{Profile: "test", InstanceID: "instance", NodeID: "node", Name: "gpu", Username: "tester", Home: f.home, Kind: "direct", Host: host, Port: n, HostKey: f.host, IdentityFile: f.key, State: "ready"}
}
func (f *streamTestSSH) serve(n net.Conn, cfg *ssh.ServerConfig) {
	defer func() { n.Close(); f.mu.Lock(); delete(f.conns, n); f.mu.Unlock() }()
	conn, channels, requests, e := ssh.NewServerConn(n, cfg)
	if e != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(requests)
	for request := range channels {
		switch request.ChannelType() {
		case "session":
			ch, reqs, e := request.Accept()
			if e != nil {
				continue
			}
			go f.session(ch, reqs)
		case "direct-tcpip":
			var p struct {
				Host       string
				Port       uint32
				Origin     string
				OriginPort uint32
			}
			if ssh.Unmarshal(request.ExtraData(), &p) != nil || p.Host != "127.0.0.1" {
				request.Reject(ssh.Prohibited, "loopback only")
				continue
			}
			up, e := net.Dial("tcp", net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port))))
			if e != nil {
				request.Reject(ssh.ConnectionFailed, "unavailable")
				continue
			}
			ch, reqs, e := request.Accept()
			if e != nil {
				up.Close()
				continue
			}
			go ssh.DiscardRequests(reqs)
			go func() {
				done := make(chan struct{})
				go func() {
					io.Copy(up, ch)
					if cw, ok := up.(interface{ CloseWrite() error }); ok {
						cw.CloseWrite()
					}
					close(done)
				}()
				io.Copy(ch, up)
				ch.CloseWrite()
				ch.Close()
				up.Close()
				<-done
			}()
		default:
			request.Reject(ssh.UnknownChannelType, "unsupported")
		}
	}
}
func (f *streamTestSSH) session(ch ssh.Channel, requests <-chan *ssh.Request) {
	defer ch.Close()
	done := make(chan struct{})
	var once sync.Once
	finish := func(code uint32) {
		once.Do(func() {
			ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
			ch.Close()
			close(done)
		})
	}
	for {
		select {
		case <-done:
			return
		case request, ok := <-requests:
			if !ok {
				return
			}
			switch request.Type {
			case "pty-req", "window-change", "env":
				request.Reply(true, nil)
			case "signal":
				request.Reply(true, nil)
				finish(143)
			case "subsystem":
				var p struct{ Name string }
				ssh.Unmarshal(request.Payload, &p)
				if p.Name != "sftp" {
					request.Reply(false, nil)
					continue
				}
				request.Reply(true, nil)
				server, e := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(f.home))
				if e != nil {
					return
				}
				server.Serve()
				server.Close()
				return
			case "exec":
				var p struct{ Command string }
				ssh.Unmarshal(request.Payload, &p)
				request.Reply(true, nil)
				switch p.Command {
				case "'cat'":
					go func() { io.Copy(ch, ch); finish(0) }()
				case "'hold'":
				default:
					go func() { ch.Write([]byte("out\n")); ch.Stderr().Write([]byte("err\n")); finish(37) }()
				}
			default:
				request.Reply(false, nil)
			}
		}
	}
}

func openTestStream(t *testing.T, c *Controller, method string, params any) (net.Conn, *bufio.Reader, domain.RPCResponse, <-chan struct{}) {
	t.Helper()
	server, client := net.Pipe()
	client.SetDeadline(time.Now().Add(8 * time.Second))
	done := make(chan struct{})
	p := streamTestParams(params)
	request := domain.RPCRequest{ID: "request", Method: method, Profile: "test", Params: domain.Result(params)}
	go func() {
		defer close(done)
		if method == "job.logs" || method == "job.attach" {
			c.HandleJobStream(context.Background(), request, p, bufio.NewReader(server), server)
		} else {
			c.HandleStream(context.Background(), request, p, bufio.NewReader(server), server)
		}
	}()
	t.Cleanup(func() {
		client.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("stream handler did not stop")
		}
	})
	reader := bufio.NewReader(client)
	var ack domain.RPCResponse
	if e := wire.ReadJSON(reader, &ack); e != nil {
		t.Fatal(e)
	}
	return client, reader, ack, done
}

type streamTestResult struct {
	Output, Stderr []byte
	Metadata       map[string]any
	Failure        *domain.Error
	Code           int
}

func readTestFrames(t *testing.T, reader io.Reader) streamTestResult {
	t.Helper()
	out := streamTestResult{Metadata: map[string]any{}}
	for {
		kind, value, e := wire.ReadFrame(reader)
		if e != nil {
			t.Fatal(e)
		}
		switch kind {
		case wire.Output:
			out.Output = append(out.Output, value...)
		case wire.Stderr:
			out.Stderr = append(out.Stderr, value...)
		case wire.Progress:
			var m map[string]any
			json.Unmarshal(value, &m)
			for k, v := range m {
				out.Metadata[k] = v
			}
		case wire.Failure:
			json.Unmarshal(value, &out.Failure)
		case wire.Exit:
			var status struct {
				Code int `json:"code"`
			}
			if e = json.Unmarshal(value, &status); e != nil {
				t.Fatal(e)
			}
			out.Code = status.Code
			return out
		}
	}
}
func sendTestInput(conn io.Writer, value []byte) {
	w := &wire.Writer{W: conn}
	_ = w.Frame(wire.Input, value)
	_ = w.Frame(wire.End, nil)
}

func TestCachedSSHStreamsDoNotAuthenticatePlatform(t *testing.T) {
	var apiCalls atomic.Int32
	c := streamTestController(t, domain.Profile{}, func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) {
		apiCalls.Add(1)
		return nil, domain.E("AUTH_EXPIRED", "expired")
	})
	server := newStreamTestSSH(t)
	if e := c.Store.PutRoute(server.route()); e != nil {
		t.Fatal(e)
	}
	conn, reader, ack, _ := openTestStream(t, c, "connect.session", map[string]any{"target": "gpu", "node": "node", "options": connect.SessionOptions{Command: []string{"cat"}}})
	if ack.Error != nil || ack.ID != "request" {
		t.Fatalf("%+v", ack)
	}
	payload := bytes.Repeat([]byte{0, 255, 'x', 10}, 20000)
	go sendTestInput(conn, payload)
	result := readTestFrames(t, reader)
	if result.Failure != nil || result.Code != 0 || !bytes.Equal(result.Output, payload) {
		t.Fatalf("code %d error %v output %d", result.Code, result.Failure, len(result.Output))
	}
	conn, reader, ack, _ = openTestStream(t, c, "connect.session", map[string]any{"target": "instance", "options": connect.SessionOptions{Command: []string{"exit", "37"}}})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	go sendTestInput(conn, nil)
	result = readTestFrames(t, reader)
	if result.Code != 37 || string(result.Output) != "out\n" || string(result.Stderr) != "err\n" || result.Failure != nil {
		t.Fatalf("%+v", result)
	}
	if apiCalls.Load() != 0 {
		t.Fatal("SSH consulted expired platform authentication")
	}
	wrong := server.route()
	wrong.HostKey = ""
	c.Store.PutRoute(wrong)
	_, _, ack, _ = openTestStream(t, c, "connect.session", map[string]any{"target": "gpu"})
	if ack.Error == nil || ack.Error.Code != "SSH_HOST_KEY_REQUIRED" || ack.Error.Retryable {
		t.Fatalf("%+v", ack)
	}
}

func TestSessionDisconnectStopsRemoteSession(t *testing.T) {
	c := streamTestController(t, domain.Profile{}, nil)
	server := newStreamTestSSH(t)
	c.Store.PutRoute(server.route())
	conn, _, ack, done := openTestStream(t, c, "connect.session", map[string]any{"target": "gpu", "options": connect.SessionOptions{Command: []string{"hold"}}})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	conn.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disconnected session did not cancel")
	}
}

func TestSFTPStreamSizeChecksAndUnaryMetadata(t *testing.T) {
	c := streamTestController(t, domain.Profile{}, nil)
	server := newStreamTestSSH(t)
	c.Store.PutRoute(server.route())
	payload := []byte{0, 255, 'a', 'b'}
	conn, reader, ack, _ := openTestStream(t, c, "connect.upload", map[string]any{"target": "gpu", "path": "data.bin", "size": len(payload), "mode": 0640})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	go sendTestInput(conn, payload)
	result := readTestFrames(t, reader)
	if result.Failure != nil || result.Code != 0 {
		t.Fatalf("%+v", result)
	}
	metadata, e := c.HandleFiles(context.Background(), "test", "connect.stat", streamTestParams(map[string]any{"target": "gpu", "path": "data.bin"}))
	if e != nil {
		t.Fatal(e)
	}
	info := metadata.(connect.FileInfo)
	if info.Size != 4 || info.Mode&0777 != 0640 {
		t.Fatalf("%+v", info)
	}
	_, reader, ack, _ = openTestStream(t, c, "connect.download", map[string]any{"target": "gpu", "path": "data.bin"})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	result = readTestFrames(t, reader)
	if result.Failure != nil || !bytes.Equal(result.Output, payload) {
		t.Fatalf("%+v", result)
	}
	for _, size := range []int{3, 5} {
		name := fmt.Sprintf("bad-%d", size)
		conn, reader, ack, _ = openTestStream(t, c, "connect.upload", map[string]any{"target": "gpu", "path": name, "size": size})
		if ack.Error != nil {
			t.Fatal(ack.Error)
		}
		go sendTestInput(conn, payload)
		result = readTestFrames(t, reader)
		if result.Failure == nil || result.Failure.Code != "SIZE_MISMATCH" {
			t.Fatalf("%+v", result)
		}
		if _, e = os.Stat(filepath.Join(server.home, name)); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("published malformed upload")
		}
	}
}

func TestFramedForwardAndBound(t *testing.T) {
	c := streamTestController(t, domain.Profile{}, nil)
	server := newStreamTestSSH(t)
	c.Store.PutRoute(server.route())
	echo, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer echo.Close()
	go func() {
		n, e := echo.Accept()
		if e == nil {
			defer n.Close()
			io.Copy(n, n)
		}
	}()
	_, port, _ := net.SplitHostPort(echo.Addr().String())
	n, _ := strconv.Atoi(port)
	conn, reader, ack, _ := openTestStream(t, c, "connect.forward", map[string]any{"target": "gpu", "host": "127.0.0.1", "port": n})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	payload := bytes.Repeat([]byte{255, 0, 1, 2}, 10000)
	go sendTestInput(conn, payload)
	result := readTestFrames(t, reader)
	if result.Failure != nil || !bytes.Equal(result.Output, payload) {
		t.Fatalf("%+v", result)
	}
	conn.Close()
	for i := 0; i < cap(streamForwardSlots); i++ {
		select {
		case streamForwardSlots <- struct{}{}:
		case <-time.After(time.Second):
			t.Fatal("forward slot leaked")
		}
	}
	defer func() {
		for i := 0; i < cap(streamForwardSlots); i++ {
			<-streamForwardSlots
		}
	}()
	_, _, ack, _ = openTestStream(t, c, "connect.forward", map[string]any{"target": "gpu", "host": "127.0.0.1", "port": n})
	if ack.Error == nil || ack.Error.Code != "FORWARD_LIMIT" {
		t.Fatalf("%+v", ack)
	}
}

func TestRelayProxyRequiresLeaseOwnerAndKeepsRawBytes(t *testing.T) {
	c := streamTestController(t, domain.Profile{}, nil)
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	n, _ := strconv.Atoi(port)
	lease := domain.PortLease{Key: "lease", Owner: "another", Profile: "test", InstanceID: "i", NodeID: "n", Port: n, State: "active"}
	c.Store.PutPortLease(lease)
	_, _, ack, _ := openTestStream(t, c, "relay.proxy", map[string]any{"key": "lease"})
	if ack.Error == nil || ack.Error.Code != "LEASE_FORBIDDEN" {
		t.Fatalf("%+v", ack)
	}
	lease.Key = "local-lease"
	lease.Owner = "local"
	lease.Port = n + 1
	listener.Close()
	listener, e = net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(n+1)))
	if e != nil {
		t.Skip("adjacent port not available")
	}
	defer listener.Close()
	if e = c.Store.PutPortLease(lease); e != nil {
		t.Fatal(e)
	}
	payload := []byte{0, 255, 10, 13, 'x'}
	go func() {
		up, e := listener.Accept()
		if e != nil {
			return
		}
		defer up.Close()
		b := make([]byte, len(payload))
		io.ReadFull(up, b)
		up.Write(b)
	}()
	conn, reader, ack, _ := openTestStream(t, c, "relay.proxy", map[string]any{"key": "local-lease"})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	go conn.Write(payload)
	out, e := io.ReadAll(reader)
	if e != nil || !bytes.Equal(out, payload) {
		t.Fatalf("%v %v", out, e)
	}
}

func TestSessionInputReceivesControlsAfterEnd(t *testing.T) {
	input, output := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer input.Close()
	defer output.Close()
	stream := newSessionInput(ctx, input, cancel)
	w := &wire.Writer{W: output}
	go func() {
		w.Frame(wire.End, nil)
		w.Frame(wire.Resize, domain.Result(connect.WindowSize{Rows: 40, Cols: 120}))
		w.Frame(wire.Signal, []byte("TERM"))
	}()
	select {
	case size := <-stream.resizes:
		if size.Rows != 40 || size.Cols != 120 {
			t.Fatal(size)
		}
	case <-time.After(time.Second):
		t.Fatal("resize after EOF was lost")
	}
	select {
	case sig := <-stream.signals:
		if sig != "TERM" {
			t.Fatal(sig)
		}
	case <-time.After(time.Second):
		t.Fatal("signal after EOF was lost")
	}
	if _, e := stream.Read(make([]byte, 8)); !errors.Is(e, io.EOF) {
		t.Fatal(e)
	}
}

func TestStreamErrorRetryClassification(t *testing.T) {
	for _, e := range []error{io.EOF, io.ErrUnexpectedEOF, net.ErrClosed, &net.OpError{Op: "read", Net: "tcp", Err: io.EOF}, &ssh.ExitMissingError{}} {
		if !streamRPCError(e).Retryable {
			t.Fatalf("not retryable: %v", e)
		}
	}
	for _, e := range []error{domain.E("SSH_HOST_KEY_MISMATCH", "changed"), domain.E("AUTH_EXPIRED", "expired"), fmt.Errorf("ssh: unable to authenticate")} {
		if streamRPCError(e).Retryable {
			t.Fatalf("unsafe retry: %v", e)
		}
	}
}

func TestPlatformUploadResumeUsesPrivateReceipt(t *testing.T) {
	var mu sync.Mutex
	var uploaded []byte
	var total int64
	var posts int
	secret := "private-upload-capability"
	var prefix []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/storage/config":
			io.WriteString(w, `{"stores":[{"name":"home","urlPath":"personal","isWorkDir":true}]}`)
		case strings.Contains(r.URL.Path, "/folders/"):
			io.WriteString(w, `{"items":[]}`)
		case r.Method == "POST":
			mu.Lock()
			posts++
			total, _ = strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
			uploaded = append([]byte(nil), prefix...)
			mu.Unlock()
			w.Header().Set("Location", "/api/storage/personal/files/upload?token="+secret)
			w.WriteHeader(201)
		case r.Method == "HEAD":
			mu.Lock()
			offset := len(uploaded)
			size := total
			mu.Unlock()
			w.Header().Set("Upload-Offset", strconv.Itoa(offset))
			w.Header().Set("Upload-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(200)
		case r.Method == "PATCH":
			b, e := io.ReadAll(r.Body)
			if e != nil {
				w.WriteHeader(400)
				return
			}
			mu.Lock()
			expected := len(uploaded)
			sent, _ := strconv.Atoi(r.Header.Get("Upload-Offset"))
			if expected != sent {
				mu.Unlock()
				w.WriteHeader(409)
				return
			}
			uploaded = append(uploaded, b...)
			offset := len(uploaded)
			mu.Unlock()
			w.Header().Set("Upload-Offset", strconv.Itoa(offset))
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := streamTestController(t, domain.Profile{PlatformURL: server.URL}, nil)
	payload := []byte("prefix-and-the-rest")
	hash := sha256.Sum256(payload)
	source := hex.EncodeToString(hash[:])
	id := uuid.NewString()
	prefix = payload[:7]
	// Emulate an earlier interrupted TUS upload without exposing its URL in state.
	mu.Lock()
	uploaded = append([]byte(nil), prefix...)
	total = int64(len(payload))
	mu.Unlock()
	receipt := UploadReceipt{UploadID: id, Profile: "test", Owner: "local", Store: "personal", Path: "data.bin", SourceHash: source, Size: int64(len(payload)), Offset: 7, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if e := c.Store.Put("uploads", uploadStateKey("test", id), receipt); e != nil {
		t.Fatal(e)
	}
	c.Vault.Put(uploadVaultKey("test", id), privateUpload{URL: server.URL + "/api/storage/personal/files/upload?token=" + secret})
	conn, reader, ack, _ := openTestStream(t, c, "data.upload", map[string]any{"path": "data.bin", "size": len(payload), "source_hash": source, "upload_id": id, "resume": true})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	var initial UploadReceipt
	json.Unmarshal(ack.Result, &initial)
	if initial.Offset != 7 {
		t.Fatalf("resume offset %d", initial.Offset)
	}
	go sendTestInput(conn, payload[initial.Offset:])
	result := readTestFrames(t, reader)
	if result.Failure != nil || result.Code != 0 || result.Metadata["complete"] != true {
		t.Fatalf("%+v", result)
	}
	mu.Lock()
	actual := append([]byte(nil), uploaded...)
	count := posts
	mu.Unlock()
	if !bytes.Equal(actual, payload) || count != 0 {
		t.Fatalf("upload %q creates %d", actual, count)
	}
	if bytes.Contains(ack.Result, []byte(secret)) || strings.Contains(fmt.Sprint(result.Metadata), secret) {
		t.Fatal("private upload capability escaped")
	}
	var records []UploadReceipt
	c.Store.List("uploads", &records)
	public, _ := json.Marshal(records)
	if bytes.Contains(public, []byte(secret)) || bytes.Contains(public, []byte("http")) {
		t.Fatal("upload URL persisted in public state")
	}
	_, _, ack, _ = openTestStream(t, c, "data.upload", map[string]any{"path": "different.bin", "size": len(payload), "source_hash": source, "upload_id": id})
	if ack.Error == nil || ack.Error.Code != "UPLOAD_MISMATCH" {
		t.Fatalf("%+v", ack)
	}
}

func TestPlatformUploadNewAndDownload(t *testing.T) {
	payload := bytes.Repeat([]byte{0, 255, 1, 2}, 1000)
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/storage/config":
			io.WriteString(w, `{"stores":[{"name":"home","urlPath":"personal","isWorkDir":true}]}`)
		case strings.Contains(r.URL.Path, "/folders/"):
			io.WriteString(w, `{"items":[]}`)
		case strings.Contains(r.URL.Path, "/download/"):
			w.Write(payload)
		case r.Method == "POST":
			w.Header().Set("Location", "/api/storage/personal/files/123")
			w.WriteHeader(201)
		case r.Method == "PATCH":
			received, _ = io.ReadAll(r.Body)
			w.Header().Set("Upload-Offset", strconv.Itoa(len(received)))
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	c := streamTestController(t, domain.Profile{PlatformURL: server.URL}, nil)
	conn, reader, ack, _ := openTestStream(t, c, "data.upload", map[string]any{"path": "data.bin", "size": len(payload), "resume": false})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	var metadata UploadReceipt
	json.Unmarshal(ack.Result, &metadata)
	if metadata.UploadID == "" || metadata.Offset != 0 {
		t.Fatalf("%+v", metadata)
	}
	go sendTestInput(conn, payload)
	result := readTestFrames(t, reader)
	if result.Failure != nil || !bytes.Equal(received, payload) {
		t.Fatalf("%+v", result)
	}
	_, reader, ack, _ = openTestStream(t, c, "data.download", map[string]any{"path": "data.bin"})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	result = readTestFrames(t, reader)
	if result.Failure != nil || !bytes.Equal(result.Output, payload) {
		t.Fatalf("%+v", result)
	}
}

type streamTestPlatform struct {
	domain.PlatformAPI
	snapshot domain.Snapshot
	calls    atomic.Int32
	console  io.ReadWriteCloser
}

func (p *streamTestPlatform) Sync(context.Context) (domain.Snapshot, error) {
	p.calls.Add(1)
	return p.snapshot, nil
}
func (p *streamTestPlatform) Console(context.Context, domain.ID) (io.ReadWriteCloser, error) {
	return p.console, nil
}
func (p *streamTestPlatform) Close() error { return nil }
func TestConsoleChecksOwnershipBeforeOpening(t *testing.T) {
	p := &streamTestPlatform{snapshot: domain.Snapshot{Identity: domain.Identity{UID: "owner"}, Instances: []domain.Instance{{ID: "i", OwnerID: "another", Name: "foreign", Nodes: []domain.Node{{ID: "n", Services: []domain.Service{{ID: "1", Name: "console"}}}}}}}}
	c := streamTestController(t, domain.Profile{}, func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return p, nil })
	_, _, ack, _ := openTestStream(t, c, "console", map[string]any{"target": "foreign"})
	if ack.Error == nil || ack.Error.Code != "OWNERSHIP_UNVERIFIED" {
		t.Fatalf("%+v", ack)
	}
}

type streamTestConsole struct {
	net.Conn
	sizes chan connect.WindowSize
}

func (c *streamTestConsole) Resize(cols, rows int) error {
	c.sizes <- connect.WindowSize{Rows: rows, Cols: cols}
	return nil
}
func TestConsoleStreamsBytesAndResize(t *testing.T) {
	controllerSide, terminalSide := net.Pipe()
	console := &streamTestConsole{Conn: controllerSide, sizes: make(chan connect.WindowSize, 4)}
	defer terminalSide.Close()
	p := &streamTestPlatform{console: console, snapshot: domain.Snapshot{Identity: domain.Identity{UID: "owner"}, Instances: []domain.Instance{{ID: "i", OwnerID: "owner", Name: "owned", Nodes: []domain.Node{{ID: "n", Services: []domain.Service{{ID: "11", Name: "console"}}}}}}}}
	c := streamTestController(t, domain.Profile{}, func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return p, nil })
	go func() {
		defer terminalSide.Close()
		b := make([]byte, 4096)
		for {
			n, e := terminalSide.Read(b)
			if n > 0 {
				if b[n-1] == 4 {
					return
				}
				if _, e = terminalSide.Write(b[:n]); e != nil {
					return
				}
			}
			if e != nil {
				return
			}
		}
	}()
	conn, reader, ack, _ := openTestStream(t, c, "console", map[string]any{"target": "owned", "rows": 30, "cols": 100})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	payload := []byte{'x', 0, 255, '\n'}
	go func() {
		w := &wire.Writer{W: conn}
		w.Frame(wire.Resize, domain.Result(connect.WindowSize{Rows: 50, Cols: 140}))
		w.Frame(wire.Input, payload)
		w.Frame(wire.End, nil)
	}()
	result := readTestFrames(t, reader)
	if result.Failure != nil || result.Code != 0 || !bytes.Equal(result.Output, payload) {
		t.Fatalf("%+v", result)
	}
	for _, want := range []connect.WindowSize{{Rows: 30, Cols: 100}, {Rows: 50, Cols: 140}} {
		select {
		case got := <-console.sizes:
			if got != want {
				t.Fatalf("got %+v want %+v", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("console resize was lost")
		}
	}
}

type streamTestJobBackend struct {
	code  int
	calls atomic.Int32
}

func (b *streamTestJobBackend) Invoke(context.Context, domain.Route, JobRequest) (JobStatusInfo, error) {
	b.calls.Add(1)
	return JobStatusInfo{State: "failed", ExitCode: &b.code}, nil
}
func TestJobAttachReadsLogsWithoutRelaunch(t *testing.T) {
	c := streamTestController(t, domain.Profile{}, nil)
	server := newStreamTestSSH(t)
	c.Store.PutRoute(server.route())
	id := uuid.NewString()
	run := domain.Run{ID: id, Profile: "test", Kind: "job", Name: "job", InstanceID: "instance", NodeID: "node", State: "running"}
	if e := c.Store.PutRun(run); e != nil {
		t.Fatal(e)
	}
	directory, _ := JobDirectory(id)
	os.MkdirAll(filepath.Join(server.home, directory), 0700)
	os.WriteFile(filepath.Join(server.home, directory, "stdout.log"), []byte("stdout\n"), 0600)
	os.WriteFile(filepath.Join(server.home, directory, "stderr.log"), []byte("stderr\n"), 0600)
	backend := &streamTestJobBackend{code: 17}
	c.opts.JobBackend = backend
	_, reader, ack, _ := openTestStream(t, c, "job.attach", map[string]any{"target": id})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	result := readTestFrames(t, reader)
	if result.Failure != nil || result.Code != 0 || string(result.Output) != "stdout\nstderr\n" || result.Metadata["exit_code"] != float64(17) {
		t.Fatalf("%+v", result)
	}
	if backend.calls.Load() != 1 {
		t.Fatal(backend.calls.Load())
	}
}

func TestJobLogsUseEncryptedArchiveAfterCleanup(t *testing.T) {
	var apiCalls atomic.Int32
	c := streamTestController(t, domain.Profile{}, func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) {
		apiCalls.Add(1)
		return nil, domain.E("AUTH_EXPIRED", "expired")
	})
	id := uuid.NewString()
	exit := 9
	run := domain.Run{ID: id, Profile: "test", Kind: "job", State: "complete", ExitCode: &exit}
	if err := c.Store.PutRun(run); err != nil {
		t.Fatal(err)
	}
	data := []byte("archived output\n")
	if err := c.Vault.Put(jobLogChunkKey(id, "stdout", 0), data); err != nil {
		t.Fatal(err)
	}
	for stream, size := range map[string]int64{"stdout": int64(len(data)), "stderr": 0} {
		if err := c.Store.Put("job_log_archives", id+"/"+stream, JobLogArchiveInfo{Bytes: size, Complete: true, UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	_, reader, ack, _ := openTestStream(t, c, "job.attach", map[string]any{"target": id})
	if ack.Error != nil {
		t.Fatal(ack.Error)
	}
	result := readTestFrames(t, reader)
	if result.Failure != nil || result.Code != 0 || !bytes.Equal(result.Output, data) || result.Metadata["archive_complete"] != true || result.Metadata["exit_code"] != float64(9) {
		t.Fatalf("%+v", result)
	}
	if apiCalls.Load() != 0 {
		t.Fatal("archived logs required school login")
	}
}

var _ = platform.UploadResult{}
