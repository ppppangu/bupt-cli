package connect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"sort"
	"strings"
	"sync"

	"bupt.local/cli/internal/domain"
	"golang.org/x/crypto/ssh"
)

type WindowSize struct {
	Rows int `json:"rows"`
	Cols int `json:"cols"`
}
type SessionOptions struct {
	OnConnected func()            `json:"-"`
	Command     []string          `json:"command,omitempty"`
	PTY         bool              `json:"pty"`
	Term        string            `json:"term,omitempty"`
	Rows        int               `json:"rows,omitempty"`
	Cols        int               `json:"cols,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

func QuotePOSIX(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func ShellCommand(args []string) (string, error) {
	if len(args) == 0 {
		return "", domain.E("INVALID_COMMAND", "command is empty")
	}
	out := make([]string, len(args))
	for i, s := range args {
		if strings.ContainsRune(s, 0) {
			return "", domain.E("INVALID_COMMAND", "NUL is not valid in an argument")
		}
		out[i] = QuotePOSIX(s)
	}
	return strings.Join(out, " "), nil
}

// Run streams a single SSH session. It never restarts an instance or reauthenticates
// to the school, and preserves remote process exit statuses.
func Run(ctx context.Context, route domain.Route, options SessionOptions, stdin io.Reader, stdout, stderr io.Writer, resizes <-chan WindowSize, signals <-chan string) (int, error) {
	client, err := DialSSH(ctx, route)
	if err != nil {
		return 255, err
	}
	defer client.Close()
	return RunClient(ctx, client, options, stdin, stdout, stderr, resizes, signals)
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func RunClient(ctx context.Context, client *ssh.Client, options SessionOptions, stdin io.Reader, stdout, stderr io.Writer, resizes <-chan WindowSize, signals <-chan string) (int, error) {
	stopCancel := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopCancel()
	session, err := client.NewSession()
	if err != nil {
		return 255, err
	}
	defer session.Close()
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	session.Stdout = stdout
	session.Stderr = stderr
	input, err := session.StdinPipe()
	if err != nil {
		return 255, err
	}
	if stdin != nil {
		go func() { _, _ = io.Copy(input, stdin); _ = input.Close() }()
	} else {
		_ = input.Close()
	}
	keys := make([]string, 0, len(options.Env))
	for k := range options.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !envName.MatchString(k) || strings.ContainsRune(options.Env[k], 0) {
			return 255, domain.E("INVALID_ENV", "invalid environment variable")
		}
		if err = session.Setenv(k, options.Env[k]); err != nil {
			return 255, domain.E("SSH_ENV_REJECTED", "SSH server rejected an environment variable; use an explicit remote launcher")
		}
	}
	if options.PTY {
		if options.Term == "" || options.Term == "xterm-ghostty" {
			options.Term = "xterm-256color"
		}
		if strings.ContainsAny(options.Term, "\x00\r\n") {
			return 255, domain.E("INVALID_TERM", "invalid terminal type")
		}
		if options.Rows <= 0 {
			options.Rows = 24
		}
		if options.Cols <= 0 {
			options.Cols = 80
		}
		if err = session.RequestPty(options.Term, options.Rows, options.Cols, ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}); err != nil {
			return 255, err
		}
	}
	if len(options.Command) == 0 {
		err = session.Shell()
	} else {
		var command string
		command, err = ShellCommand(options.Command)
		if err == nil {
			err = session.Start(command)
		}
	}
	if err != nil {
		return 255, err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				_ = session.Signal(ssh.SIGTERM)
				_ = session.Close()
				return
			case size, ok := <-resizes:
				if !ok {
					resizes = nil
					continue
				}
				if options.PTY && size.Rows > 0 && size.Cols > 0 {
					_ = session.WindowChange(size.Rows, size.Cols)
				}
			case sig, ok := <-signals:
				if !ok {
					signals = nil
					continue
				}
				sig = strings.TrimPrefix(strings.ToUpper(sig), "SIG")
				switch sig {
				case "INT", "TERM", "HUP", "QUIT", "KILL", "USR1", "USR2", "ALRM", "PIPE":
					_ = session.Signal(ssh.Signal(sig))
				}
			}
		}
	}()
	err = session.Wait()
	if ctx.Err() != nil {
		return 255, ctx.Err()
	}
	if err == nil {
		return 0, nil
	}
	var exit *ssh.ExitError
	if errors.As(err, &exit) {
		return exit.ExitStatus(), nil
	}
	return 255, fmt.Errorf("SSH session: %w", err)
}

// Forward serves a caller-owned local listener until cancellation. It reuses an
// SSH connection and bounds the number of concurrent forwarded connections.
func Forward(ctx context.Context, route domain.Route, listener net.Listener, remoteHostPort string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer listener.Close()
	h, p, err := net.SplitHostPort(remoteHostPort)
	if err != nil || h == "" || p == "" {
		return domain.E("INVALID_FORWARD", "remote destination must be host:port")
	}
	client, err := DialSSH(ctx, route)
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	defer func() {
		cancel()
		client.Close()
		wg.Wait()
	}()
	stop := context.AfterFunc(ctx, func() { listener.Close(); client.Close() })
	defer stop()
	go func() { _ = client.Wait(); cancel() }()
	limit := make(chan struct{}, 64)
	for {
		local, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			local.Close()
			return ctx.Err()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-limit }()
			defer local.Close()
			stopLocal := context.AfterFunc(ctx, func() { _ = local.Close() })
			defer stopLocal()
			remote, err := client.Dial("tcp", remoteHostPort)
			if err != nil {
				return
			}
			defer remote.Close()
			bridge(local, remote)
		}()
	}
}

func bridge(a, b net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(a, b)
		if c, ok := a.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(b, a)
		if c, ok := b.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		done <- struct{}{}
	}()
	<-done
	<-done
}
