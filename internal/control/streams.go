package control

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
	"bupt.local/cli/internal/state"
	"bupt.local/cli/internal/wire"
	"github.com/coder/websocket"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// A single process normally owns one controller. Bound forwarding separately
// from ordinary commands so a local listener cannot exhaust SSH transports.
var streamForwardSlots = make(chan struct{}, 64)

func streamRPCError(err error) *domain.Error {
	if err == nil {
		return nil
	}
	var de *domain.Error
	if errors.As(err, &de) {
		copy := *de
		copy.Message = state.RedactText(copy.Message)
		return &copy
	}
	if errors.Is(err, context.Canceled) {
		return &domain.Error{Code: "CANCELED", Message: "stream canceled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &domain.Error{Code: "TIMEOUT", Message: "stream deadline exceeded", Retryable: true}
	}
	var network net.Error
	var missingExit *ssh.ExitMissingError
	retryable := errors.As(err, &network) || errors.As(err, &missingExit) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
	return &domain.Error{Code: "STREAM_FAILED", Message: state.RedactText(err.Error()), Retryable: retryable}
}

func streamAck(conn io.Writer, id string, result any, err error) error {
	response := domain.RPCResponse{ID: id, Error: streamRPCError(err)}
	if err == nil {
		response.Result = domain.Result(result)
	}
	return wire.WriteJSON(conn, response)
}

func streamFinish(w *wire.Writer, code int, err error) {
	if err != nil {
		if w.Frame(wire.Failure, domain.Result(streamRPCError(err))) != nil {
			return
		}
		if code == 0 {
			code = 1
		}
	}
	_ = w.Frame(wire.Exit, domain.Result(map[string]int{"code": code}))
}

func streamInt(p Params, name string, required bool) (int64, error) {
	if !p.Has(name) {
		if required {
			return 0, domain.E("INVALID_INPUT", name+" is required")
		}
		return 0, nil
	}
	var value int64
	if err := json.Unmarshal(p[name], &value); err != nil {
		return 0, domain.E("INVALID_INPUT", name+" must be an integer")
	}
	return value, nil
}

func streamMode(p Params) (fs.FileMode, error) {
	mode, err := streamInt(p, "mode", false)
	if err != nil {
		return 0, err
	}
	if mode < 0 || mode > 0777 {
		return 0, domain.E("INVALID_MODE", "file mode must contain only user, group and other permissions")
	}
	return fs.FileMode(mode), nil
}

// HandleStream handles one authorized RPC for its entire lifetime. It writes a
// JSON acknowledgement before switching to frames (or raw bytes for proxies).
// The caller must authenticate and authorize request.ClientID before calling.
// Saved SSH routes deliberately never invoke c.api or c.snapshot.
func (c *Controller) HandleStream(ctx context.Context, request domain.RPCRequest, p Params, reader *bufio.Reader, conn io.ReadWriteCloser) bool {
	switch request.Method {
	case "connect.session", "connect.proxy", "relay.proxy", "connect.forward", "connect.upload", "connect.download", "console", "data.upload", "data.download":
	default:
		return false
	}
	if request.Profile == "" {
		request.Profile = c.Configuration().Selected
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() { stop(); cancel(); _ = conn.Close() }()
	fail := func(err error) { _ = streamAck(conn, request.ID, nil, err) }

	if p.Bool("dry_run") {
		fail(domain.E("PREVIEW_UNSUPPORTED", "流式操作需要使用独立的只读预览"))
		return true
	}
	if request.Method == "connect.download" || request.Method == "data.download" {
		go watchOutputStream(reader, cancel)
	}
	if request.Method == "data.upload" || request.Method == "data.download" {
		c.handleStorageStream(ctx, request, p, reader, conn)
		return true
	}
	if request.Method == "console" {
		c.handleConsoleStream(ctx, request, p, reader, conn)
		return true
	}
	if request.Method == "connect.proxy" || request.Method == "relay.proxy" || request.Method == "connect.forward" {
		select {
		case streamForwardSlots <- struct{}{}:
			defer func() { <-streamForwardSlots }()
		case <-ctx.Done():
			fail(ctx.Err())
			return true
		default:
			fail(&domain.Error{Code: "FORWARD_LIMIT", Message: "too many active forwarded connections", Retryable: true})
			return true
		}
	}
	if request.Method == "relay.proxy" {
		address, err := c.RelayAddress(request.ClientID, p.String("key"))
		if err != nil {
			fail(err)
			return true
		}
		upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			fail(domain.E("TUNNEL_UNREACHABLE", "the enrolled reverse tunnel is not accepting connections"))
			return true
		}
		defer upstream.Close()
		if streamAck(conn, request.ID, map[string]any{"ready": true}, nil) == nil {
			streamRaw(ctx, conn, reader, upstream)
		}
		return true
	}
	route, err := c.CachedRoute(request.Profile, p.String("target"), p.String("node"))
	if err != nil {
		fail(err)
		return true
	}
	meta := map[string]any{"instance_id": route.InstanceID, "node_id": route.NodeID}
	switch request.Method {
	case "connect.proxy":
		upstream, err := connect.DialTransport(ctx, route)
		if err != nil {
			fail(err)
			return true
		}
		defer upstream.Close()
		if streamAck(conn, request.ID, meta, nil) == nil {
			streamRaw(ctx, conn, reader, upstream)
		}
	case "connect.session":
		var options connect.SessionOptions
		if err = p.Into("options", &options); err != nil {
			fail(domain.E("INVALID_INPUT", "invalid SSH session options"))
			return true
		}
		client, err := connect.DialSSH(ctx, route)
		if err != nil {
			fail(err)
			return true
		}
		defer client.Close()
		if streamAck(conn, request.ID, meta, nil) != nil {
			return true
		}
		operation, stopOperation := context.WithCancel(ctx)
		defer stopOperation()
		input := newSessionInput(operation, reader, stopOperation)
		defer input.Close()
		w := &wire.Writer{W: conn}
		code, runErr := connect.RunClient(operation, client, options, input, wire.FrameWriter{Writer: w, Kind: wire.Output}, wire.FrameWriter{Writer: w, Kind: wire.Stderr}, input.resizes, input.signals)
		if inputErr := input.Err(); inputErr != nil {
			runErr = inputErr
		}
		streamFinish(w, code, runErr)
	case "connect.forward":
		port, err := streamInt(p, "port", true)
		host := p.String("host")
		if host == "" {
			host = "127.0.0.1"
		}
		if err != nil || port < 1 || port > 65535 || strings.ContainsAny(host, "\x00\r\n /@") {
			fail(domain.E("INVALID_FORWARD", "forward destination must be a host and a port from 1 to 65535"))
			return true
		}
		client, err := connect.DialSSH(ctx, route)
		if err != nil {
			fail(err)
			return true
		}
		defer client.Close()
		upstream, err := client.DialContext(ctx, "tcp", net.JoinHostPort(strings.Trim(host, "[]"), strconv.FormatInt(port, 10)))
		if err != nil {
			fail(err)
			return true
		}
		defer upstream.Close()
		if streamAck(conn, request.ID, meta, nil) == nil {
			streamDuplex(ctx, conn, reader, upstream, false, nil)
		}
	case "connect.upload":
		size, err := streamInt(p, "size", true)
		if err != nil || size < 0 {
			fail(domain.E("INVALID_SIZE", "a nonnegative upload size is required"))
			return true
		}
		mode, err := streamMode(p)
		if err != nil {
			fail(err)
			return true
		}
		if streamAck(conn, request.ID, meta, nil) != nil {
			return true
		}
		w := &wire.Writer{W: conn}
		input := &exactStreamReader{R: &wire.InputReader{R: reader}, Remaining: size}
		n, err := connect.Upload(ctx, route, p.String("path"), input, connect.FileOptions{Overwrite: p.Bool("overwrite"), Mkdir: p.Bool("mkdir"), Mode: mode})
		if err == nil {
			_ = w.Frame(wire.Progress, domain.Result(map[string]any{"bytes": n, "size": size, "complete": true}))
		}
		streamFinish(w, 0, err)
	case "connect.download":
		if streamAck(conn, request.ID, meta, nil) != nil {
			return true
		}
		w := &wire.Writer{W: conn}
		n, err := connect.Download(ctx, route, p.String("path"), wire.FrameWriter{Writer: w, Kind: wire.Output})
		if err == nil {
			_ = w.Frame(wire.Progress, domain.Result(map[string]any{"bytes": n, "complete": true}))
		}
		streamFinish(w, 0, err)
	}
	return true
}

// sessionInput keeps resize and signal frames working even after stdin's End.
// Its small queue provides bounded backpressure without buffering an entire file.
type sessionInput struct {
	ctx     context.Context
	cancel  context.CancelFunc
	chunks  chan []byte
	resizes chan connect.WindowSize
	signals chan string
	pending []byte
	mu      sync.Mutex
	err     error
}

func newSessionInput(ctx context.Context, source io.Reader, cancel context.CancelFunc) *sessionInput {
	s := &sessionInput{ctx: ctx, cancel: cancel, chunks: make(chan []byte, 4), resizes: make(chan connect.WindowSize, 1), signals: make(chan string, 8)}
	go func() {
		ended := false
		defer func() {
			if !ended {
				close(s.chunks)
			}
		}()
		for {
			kind, value, err := wire.ReadFrame(source)
			if err != nil {
				s.fail(err)
				return
			}
			switch kind {
			case wire.Input:
				if ended || len(value) > 1<<20 {
					s.fail(domain.E("INVALID_STREAM", "invalid or excessive terminal input frame"))
					return
				}
				select {
				case s.chunks <- value:
				case <-ctx.Done():
					return
				}
			case wire.End:
				if ended {
					s.fail(domain.E("INVALID_STREAM", "duplicate input end frame"))
					return
				}
				ended = true
				close(s.chunks)
			case wire.Resize:
				var size connect.WindowSize
				if json.Unmarshal(value, &size) != nil || size.Rows <= 0 || size.Cols <= 0 || size.Rows > 65535 || size.Cols > 65535 {
					s.fail(domain.E("INVALID_SIZE", "invalid terminal dimensions"))
					return
				}
				select {
				case s.resizes <- size:
				default:
					select {
					case <-s.resizes:
					default:
					}
					select {
					case s.resizes <- size:
					default:
					}
				}
			case wire.Signal:
				if len(value) > 16 {
					s.fail(domain.E("INVALID_SIGNAL", "invalid terminal signal"))
					return
				}
				select {
				case s.signals <- string(value):
				case <-ctx.Done():
					return
				}
			default:
				s.fail(domain.E("INVALID_STREAM", "unexpected terminal input frame"))
				return
			}
		}
	}()
	return s
}
func (s *sessionInput) fail(err error) { s.mu.Lock(); s.err = err; s.mu.Unlock(); s.cancel() }
func (s *sessionInput) Err() error     { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *sessionInput) Close() error   { s.cancel(); return nil }
func (s *sessionInput) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	for len(s.pending) == 0 {
		select {
		case value, ok := <-s.chunks:
			if !ok {
				if err := s.Err(); err != nil {
					return 0, err
				}
				return 0, io.EOF
			}
			s.pending = value
		case <-s.ctx.Done():
			return 0, s.ctx.Err()
		}
	}
	n := copy(b, s.pending)
	s.pending = s.pending[n:]
	return n, nil
}

// exactStreamReader rejects truncation or extra input before SFTP publishes its
// temporary file. TUS also validates End after its declared byte count.
type exactStreamReader struct {
	R         io.Reader
	Remaining int64
	validated bool
}

func (r *exactStreamReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	if r.Remaining == 0 {
		if r.validated {
			return 0, io.EOF
		}
		var extra [1]byte
		n, err := r.R.Read(extra[:])
		if n > 0 {
			return 0, domain.E("SIZE_MISMATCH", "upload contains more bytes than declared")
		}
		if errors.Is(err, io.EOF) {
			r.validated = true
		}
		return 0, err
	}
	if int64(len(b)) > r.Remaining {
		b = b[:r.Remaining]
	}
	n, err := r.R.Read(b)
	r.Remaining -= int64(n)
	if errors.Is(err, io.EOF) && r.Remaining > 0 {
		err = domain.E("SIZE_MISMATCH", "upload ended before its declared size")
	}
	return n, err
}
func (r *exactStreamReader) Finish() error {
	if r.Remaining != 0 {
		return domain.E("SIZE_MISMATCH", "upload ended before its declared size")
	}
	var b [1]byte
	_, err := r.Read(b[:])
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func streamRaw(ctx context.Context, conn io.ReadWriteCloser, reader io.Reader, upstream net.Conn) {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); _ = upstream.Close() })
	defer stop()
	go func() {
		defer close(done)
		_, err := io.Copy(upstream, reader)
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		if err != nil {
			_ = upstream.Close()
		}
	}()
	_, _ = io.Copy(conn, upstream)
	_ = upstream.Close()
	_ = conn.Close()
	<-done
}

// Output-only RPCs still read the socket so a disconnected CLI immediately
// cancels an otherwise silent download or log-follow operation.
func watchOutputStream(reader io.Reader, cancel context.CancelFunc) {
	for {
		kind, _, err := wire.ReadFrame(reader)
		if err != nil || kind != wire.End {
			cancel()
			return
		}
	}
}

// HandleJobStream attaches to existing output. It neither starts nor cancels
// the job; job stdin is intentionally detached. The job's actual exit status
// is metadata, while the stream itself exits zero after a successful read.
func (c *Controller) HandleJobStream(ctx context.Context, request domain.RPCRequest, p Params, reader *bufio.Reader, conn io.ReadWriteCloser) bool {
	if request.Method != "job.logs" && request.Method != "job.attach" {
		return false
	}
	if request.Profile == "" {
		request.Profile = c.Configuration().Selected
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer func() { stop(); cancel(); _ = conn.Close() }()
	run, err := c.job(request.Profile, p.String("target"))
	if err != nil {
		_ = streamAck(conn, request.ID, nil, err)
		return true
	}
	if run.Kind != "job" {
		_ = streamAck(conn, request.ID, nil, domain.E("NOT_A_JOB", "run is not a detached job"))
		return true
	}
	selection := p.String("stream")
	if selection == "" {
		selection = "combined"
	}
	if selection != "combined" && selection != "stdout" && selection != "stderr" {
		_ = streamAck(conn, request.ID, nil, domain.E("INVALID_STREAM", "job stream must be stdout, stderr or combined"))
		return true
	}
	follow := request.Method == "job.attach" || p.Bool("follow")
	if streamAck(conn, request.ID, map[string]any{"run_id": run.ID, "stream": selection, "follow": follow}, nil) != nil {
		return true
	}
	go watchOutputStream(reader, cancel)
	w := &wire.Writer{W: conn}
	names := []string{"stdout", "stderr"}
	if selection != "combined" {
		names = []string{selection}
	}
	offsets := make([]int64, len(names))
	archiveFallback := func(cause error) {
		if current, lookupErr := c.GetRun(request.Profile, run.ID); lookupErr == nil {
			if current.ExitCode == nil {
				current.ExitCode = run.ExitCode
			}
			run = current
		}
		found, archiveErr := c.streamJobArchive(ctx, run, names, offsets, w)
		if !found {
			archiveErr = cause
		}
		streamFinish(w, 0, archiveErr)
	}
	if run.ExitCode != nil && c.completeJobArchive(run.ID, names) {
		archiveFallback(nil)
		return true
	}
	var route domain.Route
	for {
		route, err = c.CachedRoute(request.Profile, string(run.InstanceID), string(run.NodeID))
		if err == nil {
			break
		}
		if !follow || run.ExitCode != nil || run.State == "failed" || run.State == "canceled" || run.State == "complete" {
			archiveFallback(err)
			return true
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(500 * time.Millisecond):
		}
		run, err = c.GetRun(request.Profile, run.ID)
		if err != nil {
			streamFinish(w, 0, err)
			return true
		}
	}
	files, err := connect.OpenSFTP(ctx, route)
	if err != nil {
		archiveFallback(err)
		return true
	}
	defer files.Close()
	home := route.Home
	if home == "" {
		home, err = files.Client.Getwd()
		if err != nil {
			streamFinish(w, 0, err)
			return true
		}
	}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		relative, err := JobLogPath(run.ID, name)
		if err != nil {
			streamFinish(w, 0, err)
			return true
		}
		resolved, err := connect.RemotePath(home, relative)
		if err != nil {
			streamFinish(w, 0, err)
			return true
		}
		paths = append(paths, resolved)
	}
	readLogs := func() error {
		for i, name := range paths {
			file, err := files.Client.Open(name)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			info, err := file.Stat()
			if err == nil && !info.Mode().IsRegular() {
				err = domain.E("INVALID_JOB_LOG", "job log is not a regular file")
			}
			if err == nil && info.Size() < offsets[i] {
				err = domain.E("JOB_LOG_CHANGED", "job log was truncated while following it")
			}
			if err == nil {
				_, err = file.Seek(offsets[i], io.SeekStart)
			}
			if err == nil {
				var n int64
				n, err = io.Copy(wire.FrameWriter{Writer: w, Kind: wire.Output}, file)
				offsets[i] += n
			}
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	}
	for {
		if err = readLogs(); err != nil {
			archiveFallback(err)
			return true
		}
		if !follow {
			if run.ExitCode != nil {
				_ = w.Frame(wire.Progress, domain.Result(map[string]any{"run_id": run.ID, "exit_code": *run.ExitCode}))
			}
			streamFinish(w, 0, nil)
			return true
		}
		probe, stopProbe := context.WithTimeout(ctx, 10*time.Second)
		status, err := c.invokeJob(probe, run, "status")
		stopProbe()
		if err != nil {
			archiveFallback(err)
			return true
		}
		if status.ExitCode != nil {
			run.ExitCode = status.ExitCode
			if err = readLogs(); err != nil {
				archiveFallback(err)
				return true
			}
			err = w.Frame(wire.Progress, domain.Result(map[string]any{"run_id": run.ID, "state": status.State, "exit_code": *status.ExitCode}))
			streamFinish(w, 0, err)
			return true
		}
		if status.State == "unknown" {
			streamFinish(w, 0, &domain.Error{Code: "JOB_OUTCOME_UNKNOWN", Message: "remote job outcome is not yet confirmed", Retryable: true})
			return true
		}
		select {
		case <-ctx.Done():
			return true
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func (c *Controller) completeJobArchive(id string, streams []string) bool {
	for _, stream := range streams {
		info, err := c.JobLogArchive(id, stream)
		if err != nil || !info.Complete {
			return false
		}
	}
	return true
}

func (c *Controller) streamJobArchive(ctx context.Context, run domain.Run, streams []string, offsets []int64, w *wire.Writer) (bool, error) {
	infos := make([]JobLogArchiveInfo, len(streams))
	exists := make([]bool, len(streams))
	found, complete := false, true
	for i, stream := range streams {
		info, err := c.JobLogArchive(run.ID, stream)
		if err != nil {
			complete = false
			continue
		}
		found = true
		exists[i] = true
		infos[i] = info
		if !info.Complete || offsets[i] > info.Bytes {
			complete = false
		}
	}
	if !found {
		return false, nil
	}
	for i, stream := range streams {
		if !exists[i] || offsets[i] >= infos[i].Bytes {
			continue
		}
		n, err := c.ReadArchivedJobLog(ctx, run.ID, stream, offsets[i], wire.FrameWriter{Writer: w, Kind: wire.Output})
		offsets[i] += n
		if err != nil {
			return true, err
		}
	}
	metadata := map[string]any{"run_id": run.ID, "source": "archive", "archive_complete": complete}
	if run.ExitCode != nil {
		metadata["exit_code"] = *run.ExitCode
	}
	if err := w.Frame(wire.Progress, domain.Result(metadata)); err != nil {
		return true, err
	}
	if !complete {
		return true, &domain.Error{Code: "LOG_ARCHIVE_INCOMPLETE", Message: "available archived output was returned, but the log archive is incomplete", Retryable: true}
	}
	return true, nil
}

func streamDuplex(ctx context.Context, conn io.ReadWriteCloser, reader io.Reader, upstream io.ReadWriteCloser, terminalEOF bool, resize func([]byte)) {
	w := &wire.Writer{W: conn}
	inputDone := make(chan error, 1)
	stop := context.AfterFunc(ctx, func() { _ = upstream.Close(); _ = conn.Close() })
	defer stop()
	go func() {
		_, err := io.Copy(upstream, &wire.InputReader{R: reader, OnResize: resize})
		if cw, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else if terminalEOF && err == nil {
			_, _ = upstream.Write([]byte{4})
		}
		if err != nil {
			_ = upstream.Close()
		}
		inputDone <- err
	}()
	_, err := io.Copy(wire.FrameWriter{Writer: w, Kind: wire.Output}, upstream)
	select {
	case inputErr := <-inputDone:
		if inputErr != nil {
			err = inputErr
		}
	default:
	}
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
		err = nil
	}
	streamFinish(w, 0, err)
	_ = upstream.Close()
	_ = conn.Close()
}

func (c *Controller) handleConsoleStream(ctx context.Context, request domain.RPCRequest, p Params, reader *bufio.Reader, conn io.ReadWriteCloser) {
	fail := func(err error) { _ = streamAck(conn, request.ID, nil, err) }
	snapshot, err := c.snapshot(ctx, request.Profile)
	if err != nil {
		fail(err)
		return
	}
	instance, err := Resolve(snapshot, p.String("target"))
	if err != nil {
		fail(err)
		return
	}
	if err = Owned(snapshot, instance); err != nil {
		fail(err)
		return
	}
	node, err := SelectNode(instance, p.String("node"))
	if err != nil {
		fail(err)
		return
	}
	serviceID, err := findConsole(node)
	if err != nil {
		fail(err)
		return
	}
	api, err := c.api(request.Profile)
	if err != nil {
		fail(err)
		return
	}
	console, err := api.Console(ctx, serviceID)
	if err != nil {
		fail(err)
		return
	}
	defer console.Close()
	var resize func([]byte)
	if sized, ok := console.(interface{ Resize(int, int) error }); ok {
		rows, cols := p.Int("rows"), p.Int("cols")
		if rows <= 0 {
			rows = 24
		}
		if cols <= 0 {
			cols = 80
		}
		if rows > 65535 || cols > 65535 {
			fail(domain.E("INVALID_SIZE", "invalid console dimensions"))
			return
		}
		if err = sized.Resize(int(cols), int(rows)); err != nil {
			fail(err)
			return
		}
		resize = func(b []byte) {
			var value connect.WindowSize
			if json.Unmarshal(b, &value) == nil && value.Rows > 0 && value.Cols > 0 && value.Rows <= 65535 && value.Cols <= 65535 {
				_ = sized.Resize(value.Cols, value.Rows)
			}
		}
	}
	if streamAck(conn, request.ID, map[string]any{"instance_id": instance.ID, "node_id": node.ID}, nil) == nil {
		streamDuplex(ctx, conn, reader, console, true, resize)
	}
}

// HandleFiles implements unary SFTP operations using only a saved route.
func (c *Controller) HandleFiles(ctx context.Context, profile, method string, p Params) (any, error) {
	if profile == "" {
		profile = c.Configuration().Selected
	}
	route, err := c.CachedRoute(profile, p.String("target"), p.String("node"))
	if err != nil {
		return nil, err
	}
	name := p.String("path")
	switch method {
	case "connect.list":
		if name == "" {
			name = "."
		}
		return connect.List(ctx, route, name)
	case "connect.stat":
		return connect.Stat(ctx, route, name)
	case "connect.mkdir":
		mode, err := streamMode(p)
		if err != nil {
			return nil, err
		}
		err = connect.Mkdir(ctx, route, name, p.Bool("recursive"), mode)
		return map[string]any{"path": name, "created": err == nil}, err
	case "connect.rename":
		err = connect.Rename(ctx, route, name, p.String("destination"), p.Bool("overwrite"))
		return map[string]any{"path": name, "destination": p.String("destination"), "renamed": err == nil}, err
	case "connect.remove":
		err = connect.Remove(ctx, route, name, p.Bool("recursive"))
		return map[string]any{"path": name, "removed": err == nil}, err
	default:
		return nil, domain.E("UNKNOWN_METHOD", "unknown SFTP operation")
	}
}

// UploadReceipt contains public resumability metadata. The platform upload URL
// is a capability and lives only in SecretStore under upload/<profile>/<id>.
type UploadReceipt struct {
	UploadID   string    `json:"upload_id"`
	Profile    string    `json:"profile"`
	Owner      string    `json:"owner"`
	Store      string    `json:"store"`
	GroupID    domain.ID `json:"group_id,omitempty"`
	Path       string    `json:"path"`
	SourceHash string    `json:"source_hash,omitempty"`
	Size       int64     `json:"size"`
	Offset     int64     `json:"offset"`
	Complete   bool      `json:"complete"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type privateUpload struct {
	URL string `json:"url"`
}

func uploadOwner(id string) string {
	if id == "" {
		return "local"
	}
	return id
}
func uploadStateKey(p, id string) string { return p + "/" + id }
func uploadVaultKey(p, id string) string { return "upload/" + p + "/" + id }

func (c *Controller) storageClient(profile string) (*platform.Client, error) {
	api, err := c.api(profile)
	if err != nil {
		return nil, err
	}
	client, ok := api.(*platform.Client)
	if !ok {
		return nil, domain.E("STORAGE_UNAVAILABLE", "this platform adapter does not support storage transfers")
	}
	return client, nil
}

func validateStorageStreamPath(value platform.StoragePath) error {
	if value.Path == "" || strings.ContainsAny(value.Path+value.Store, "\x00\\") {
		return domain.E("INVALID_PATH", "a valid storage file path is required")
	}
	for _, part := range strings.Split(value.Path+"/"+value.Store, "/") {
		if part == "." || part == ".." {
			return domain.E("INVALID_PATH", "storage paths must not contain dot segments")
		}
	}
	if strings.HasSuffix(value.Path, "/") {
		return domain.E("INVALID_PATH", "storage target must include a file name")
	}
	return nil
}

func (c *Controller) handleStorageStream(ctx context.Context, request domain.RPCRequest, p Params, reader *bufio.Reader, conn io.ReadWriteCloser) {
	fail := func(err error) { _ = streamAck(conn, request.ID, nil, err) }
	api, err := c.storageClient(request.Profile)
	if err != nil {
		fail(err)
		return
	}
	target, err := storagePath(ctx, api, p)
	if err != nil {
		fail(err)
		return
	}
	if err = validateStorageStreamPath(target); err != nil {
		fail(err)
		return
	}
	if request.Method == "data.download" {
		if streamAck(conn, request.ID, map[string]any{"path": target.Path}, nil) != nil {
			return
		}
		w := &wire.Writer{W: conn}
		err = api.Download(ctx, target, wire.FrameWriter{Writer: w, Kind: wire.Output})
		streamFinish(w, 0, err)
		return
	}
	size, err := streamInt(p, "size", true)
	if err != nil || size < 0 {
		fail(domain.E("INVALID_SIZE", "a nonnegative upload size is required"))
		return
	}
	if c.Vault == nil {
		fail(domain.E("SECRET_STORE", "a secret store is required for resumable uploads"))
		return
	}
	receipt, resumeURL, release, err := c.beginUpload(request, p, target, size)
	if err != nil {
		fail(err)
		return
	}
	defer release()
	if receipt.Complete {
		if streamAck(conn, request.ID, receipt, nil) == nil {
			streamFinish(&wire.Writer{W: conn}, 0, nil)
		}
		return
	}
	if resumeURL == "" && !p.Bool("overwrite") {
		directory := target
		directory.Path = path.Dir(target.Path)
		if directory.Path == "." {
			directory.Path = ""
		}
		entries, err := api.ListFiles(ctx, directory)
		if err != nil {
			fail(err)
			return
		}
		for _, entry := range entries {
			if entry.Name == path.Base(target.Path) {
				fail(domain.E("ALREADY_EXISTS", "storage destination exists; explicitly request overwrite"))
				return
			}
		}
	}
	w := &wire.Writer{W: conn}
	input := &exactStreamReader{R: &wire.InputReader{R: reader}}
	seeker := &wire.StreamSeeker{R: input}
	acked := false
	options := platform.UploadOptions{ResumeURL: resumeURL, ContentType: p.String("content_type")}
	options.Progress = func(progress platform.UploadResult) error {
		if err := c.Vault.Put(uploadVaultKey(receipt.Profile, receipt.UploadID), privateUpload{URL: progress.URL}); err != nil {
			return domain.E("SECRET_STORE", "could not persist the private upload receipt")
		}
		receipt.Offset = progress.Offset
		receipt.UpdatedAt = time.Now().UTC()
		if err := c.Store.Put("uploads", uploadStateKey(receipt.Profile, receipt.UploadID), receipt); err != nil {
			return err
		}
		if !acked {
			seeker.Base = progress.Offset
			input.Remaining = size - progress.Offset
			if err := streamAck(conn, request.ID, receipt, nil); err != nil {
				return err
			}
			acked = true
			return nil
		}
		return w.Frame(wire.Progress, domain.Result(receipt))
	}
	result, err := api.Upload(ctx, target, seeker, size, options)
	if err == nil {
		err = input.Finish()
	}
	if err == nil {
		receipt.Offset = result.Offset
		receipt.Complete = true
		receipt.UpdatedAt = time.Now().UTC()
		err = c.Store.Put("uploads", uploadStateKey(receipt.Profile, receipt.UploadID), receipt)
		if err == nil {
			err = w.Frame(wire.Progress, domain.Result(receipt))
		}
	}
	if !acked {
		fail(err)
		return
	}
	streamFinish(w, 0, err)
}

func (c *Controller) beginUpload(request domain.RPCRequest, p Params, target platform.StoragePath, size int64) (UploadReceipt, string, func(), error) {
	c.intentMu.Lock()
	defer c.intentMu.Unlock()
	owner, source := uploadOwner(request.ClientID), p.String("source_hash")
	if source != "" {
		b, err := hex.DecodeString(source)
		if err != nil || len(b) != 32 {
			return UploadReceipt{}, "", nil, domain.E("INVALID_HASH", "source_hash must be a SHA-256 hex digest")
		}
		source = strings.ToLower(source)
	}
	id := p.String("upload_id")
	if id == "" && p.String("idempotency_key") != "" {
		id = uuid.NewSHA1(uuid.NameSpaceURL, []byte("upload/"+request.Profile+"/"+owner+"/"+p.String("idempotency_key"))).String()
	}
	var receipt UploadReceipt
	existing := false
	if id != "" {
		parsed, err := uuid.Parse(id)
		if err != nil {
			return receipt, "", nil, domain.E("INVALID_UPLOAD", "invalid upload ID")
		}
		id = parsed.String()
		err = c.Store.Get("uploads", uploadStateKey(request.Profile, id), &receipt)
		if err == nil {
			existing = true
		} else {
			var de *domain.Error
			if !errors.As(err, &de) || de.Code != "NOT_FOUND" {
				return receipt, "", nil, err
			}
			if p.String("upload_id") != "" {
				return receipt, "", nil, domain.E("UPLOAD_NOT_FOUND", "upload receipt was not found in this profile")
			}
		}
	} else if p.Bool("resume") && source != "" {
		var all []UploadReceipt
		if err := c.Store.List("uploads", &all); err != nil {
			return receipt, "", nil, err
		}
		for _, candidate := range all {
			if candidate.Profile == request.Profile && candidate.Owner == owner && candidate.SourceHash == source && candidate.Size == size && candidate.Store == target.Store && candidate.GroupID == target.GroupID && candidate.Path == target.Path && !candidate.Complete {
				if !existing || candidate.UpdatedAt.After(receipt.UpdatedAt) {
					receipt = candidate
					id = candidate.UploadID
					existing = true
				}
			}
		}
	}
	if existing && (receipt.UploadID != id || receipt.Profile != request.Profile || receipt.Owner != owner || receipt.SourceHash != source || receipt.Size != size || receipt.Store != target.Store || receipt.GroupID != target.GroupID || receipt.Path != target.Path) {
		return receipt, "", nil, domain.E("UPLOAD_MISMATCH", "upload ID belongs to a different source, owner or destination")
	}
	if existing && source == "" {
		return receipt, "", nil, domain.E("SOURCE_HASH_REQUIRED", "resuming an upload requires the original source hash")
	}
	if existing && (receipt.Offset < 0 || receipt.Offset > size || (receipt.Complete && receipt.Offset != size)) {
		return receipt, "", nil, domain.E("UPLOAD_RECEIPT_INVALID", "persisted upload receipt has an invalid byte offset")
	}
	if !existing {
		if id == "" {
			id = uuid.NewString()
		}
		now := time.Now().UTC()
		receipt = UploadReceipt{UploadID: id, Profile: request.Profile, Owner: owner, Store: target.Store, GroupID: target.GroupID, Path: target.Path, SourceHash: source, Size: size, CreatedAt: now, UpdatedAt: now}
	}
	claim := "upload/" + uploadStateKey(request.Profile, id)
	c.mu.Lock()
	if c.working[claim] {
		c.mu.Unlock()
		return receipt, "", nil, &domain.Error{Code: "UPLOAD_BUSY", Message: "this upload is already active", Retryable: true}
	}
	c.working[claim] = true
	c.mu.Unlock()
	release := func() { c.mu.Lock(); delete(c.working, claim); c.mu.Unlock() }
	if !existing {
		if err := c.Store.Put("uploads", uploadStateKey(request.Profile, id), receipt); err != nil {
			release()
			return receipt, "", nil, err
		}
	}
	var private privateUpload
	if existing && !receipt.Complete {
		err := c.Vault.Get(uploadVaultKey(request.Profile, id), &private)
		if err != nil {
			var de *domain.Error
			if !errors.Is(err, os.ErrNotExist) && !(errors.As(err, &de) && de.Code == "NOT_FOUND") {
				release()
				return receipt, "", nil, domain.E("SECRET_STORE", "could not read the private upload receipt")
			}
			if receipt.Offset != 0 {
				release()
				return receipt, "", nil, domain.E("UPLOAD_RECEIPT_MISSING", "private resume receipt is missing")
			}
		}
	}
	return receipt, private.URL, release, nil
}
