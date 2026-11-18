package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) rawOutput() error {
	if a.json || a.jsonl || a.quiet {
		return usage("raw connection commands cannot be combined with --json, --jsonl, or --quiet")
	}
	return nil
}
func (a *app) sshCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "ssh INSTANCE [-- COMMAND [ARG...]]", Short: "Connect through a cached SSH route without forcing school reauthentication", Args: rangeArgs(1, -1)}
	cmd.Flags().BoolP("tty", "t", false, "Request a terminal")
	cmd.Flags().BoolP("no-tty", "T", false, "Do not allocate a terminal")
	cmd.Flags().String("session", "", "Attach or create a persistent tmux session")
	cmd.Flags().Bool("reconnect", false, "Reconnect a named tmux session after transport failure")
	cmd.Flags().String("node", "", "Node selector for a multi-node instance")
	ensure := &cobra.Command{Use: "ensure INSTANCE", Short: "Prepare or repair SSH for an existing running instance without allocating resources", Args: exactArgs(1)}
	ensure.Flags().String("node", "", "Node selector for a multi-node instance")
	ensure.RunE = func(cmd *cobra.Command, args []string) error {
		p := targetParams(args)
		copyChangedFlags(cmd, p)
		p["repair"] = true
		return a.callAndPrint(cmd, "route.get", p)
	}
	cmd.AddCommand(ensure)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) > 1 && cmd.ArgsLenAtDash() != 1 {
			return usage("separate remote commands with --")
		}
		tty, _ := cmd.Flags().GetBool("tty")
		noTTY, _ := cmd.Flags().GetBool("no-tty")
		session, _ := cmd.Flags().GetString("session")
		reconnect, _ := cmd.Flags().GetBool("reconnect")
		if tty && noTTY {
			return usage("choose --tty or --no-tty")
		}
		if reconnect && session == "" {
			return usage("--reconnect requires --session")
		}
		if session != "" && noTTY {
			return usage("--session requires a terminal; omit --no-tty")
		}
		if reconnect {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			cmd.SetContext(ctx)
		}
		for {
			err := a.session(cmd, args[0], args[1:], tty, noTTY, session)
			if !reconnect || err == nil {
				return err
			}
			if !reconnectable(err) {
				return err
			}
			if cmd.Context().Err() != nil {
				return cmd.Context().Err()
			}
			fmt.Fprintln(a.options.Err, "SSH disconnected; reconnecting the existing tmux session in 3s...")
			select {
			case <-cmd.Context().Done():
				return cmd.Context().Err()
			case <-time.After(3 * time.Second):
			}
		}
	}
	return cmd
}

func reconnectable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var remote *ExitError
	if errors.As(err, &remote) {
		return false
	}
	var de *domain.Error
	if errors.As(err, &de) {
		return de.Retryable || de.Code == "RELAY_PROXY_FAILED"
	}
	var network net.Error
	return errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, io.ErrClosedPipe)
}
func (a *app) execCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "exec INSTANCE -- COMMAND [ARG...]", Short: "Execute a remote command and preserve its exit status", Args: rangeArgs(2, -1)}
	cmd.Flags().String("node", "", "Node selector for a multi-node instance")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if cmd.ArgsLenAtDash() != 1 {
			return usage("use exec INSTANCE -- COMMAND [ARG...]")
		}
		return a.session(cmd, args[0], args[1:], false, true, "")
	}
	return cmd
}
func (a *app) session(cmd *cobra.Command, target string, command []string, forcePTY, noPTY bool, persistent string) error {
	node, _ := cmd.Flags().GetString("node")
	if err := a.rawOutput(); err != nil {
		return err
	}
	if a.dryRun {
		return a.callAndPrint(cmd, "route.get", map[string]any{"target": target, "node": node, "repair": false})
	}
	if persistent != "" {
		if len(command) > 0 {
			return usage("--session cannot be combined with a remote command")
		}
		if strings.ContainsAny(persistent, "\x00\r\n") {
			return usage("invalid tmux session name")
		}
		command = []string{"tmux", "new-session", "-A", "-s", persistent}
		forcePTY = true
	}
	b, err := a.getBackend()
	if err != nil {
		return err
	}
	pty := forcePTY || (!noPTY && isTerminal(a.options.In) && isTerminal(a.options.Out))
	opts := connect.SessionOptions{Command: command, PTY: pty, Term: "xterm-256color", Rows: 24, Cols: 80}
	resizes := make(chan connect.WindowSize, 1)
	signals := make(chan string, 4)
	stop := make(chan struct{})
	var old *term.State
	var fd int
	if f, ok := a.options.In.(*os.File); ok && pty && term.IsTerminal(int(f.Fd())) {
		fd = int(f.Fd())
		cols, rows, e := term.GetSize(fd)
		if e == nil {
			opts.Cols, opts.Rows = cols, rows
		}
		old, err = term.MakeRaw(fd)
		if err != nil {
			return err
		}
		defer term.Restore(fd, old)
	}
	osSignals := make(chan os.Signal, 4)
	signal.Notify(osSignals, syscall.SIGWINCH, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(osSignals)
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			case sig := <-osSignals:
				if sig == syscall.SIGWINCH {
					if pty && old != nil {
						c, r, e := term.GetSize(fd)
						if e == nil {
							select {
							case resizes <- connect.WindowSize{Rows: r, Cols: c}:
							default:
							}
						}
					}
					continue
				}
				n := "INT"
				if sig == syscall.SIGTERM {
					n = "TERM"
				}
				select {
				case signals <- n:
				default:
				}
			}
		}
	}()
	ctx := cmd.Context()
	if cmd.Flags().Changed("timeout") {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}
	var exit int
	if node != "" {
		nb, ok := b.(nodeSessionBackend)
		if !ok {
			return domain.E("TRANSPORT_CAPABILITY", "this controller transport does not support node-specific SSH sessions")
		}
		exit, err = nb.SessionNode(ctx, target, node, opts, a.options.In, a.options.Out, a.options.Err, resizes, signals)
	} else {
		exit, err = b.Session(ctx, target, opts, a.options.In, a.options.Out, a.options.Err, resizes, signals)
	}
	if err != nil {
		return err
	}
	if exit != 0 {
		return &ExitError{Code: exit}
	}
	return nil
}
func isTerminal(v any) bool { f, ok := v.(*os.File); return ok && term.IsTerminal(int(f.Fd())) }
func (a *app) proxyCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "proxy INSTANCE", Short: "Expose a byte-clean stream for OpenSSH ProxyCommand", Args: exactArgs(1)}
	cmd.Flags().String("node", "", "Node selector for a multi-node instance")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if e := a.rawOutput(); e != nil {
			return e
		}
		if a.dryRun {
			return usage("proxy --dry-run is not a byte stream; use inspect or doctor")
		}
		b, e := a.getBackend()
		if e != nil {
			return e
		}
		if node, _ := cmd.Flags().GetString("node"); node != "" {
			nb, ok := b.(nodeProxyBackend)
			if !ok {
				return domain.E("TRANSPORT_CAPABILITY", "this controller transport does not support node-specific SSH proxying")
			}
			return nb.ProxyNode(cmd.Context(), args[0], node, a.options.In, a.options.Out)
		}
		return b.Proxy(cmd.Context(), args[0], a.options.In, a.options.Out)
	}
	return cmd
}
func (a *app) consoleCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "console INSTANCE", Short: "Open the platform terminal through its native WebSocket protocol", Args: exactArgs(1)}
	cmd.Flags().String("node", "", "Node selector")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if e := a.rawOutput(); e != nil {
			return e
		}
		if a.dryRun {
			return a.callAndPrint(cmd, "instance.inspect", targetParams(args))
		}
		b, e := a.getBackend()
		if e != nil {
			return e
		}
		p := targetParams(args)
		copyChangedFlags(cmd, p)
		p["cols"] = 80
		p["rows"] = 24
		if f, ok := a.options.In.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
			c, r, e := term.GetSize(int(f.Fd()))
			if e == nil {
				p["cols"] = c
				p["rows"] = r
			}
			old, e := term.MakeRaw(int(f.Fd()))
			if e != nil {
				return e
			}
			defer term.Restore(int(f.Fd()), old)
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		_, e = b.Stream(ctx, "console", p, a.options.In, a.options.Out)
		return e
	}
	return cmd
}
func (a *app) forwardCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "forward INSTANCE PORT", Short: "Forward a local loopback port through the existing SSH route", Args: exactArgs(2)}
	cmd.Flags().String("local", "127.0.0.1:0", "Local listen address")
	cmd.Flags().String("remote-host", "127.0.0.1", "Destination as seen inside the instance")
	cmd.Flags().String("node", "", "Node selector for a multi-node instance")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		port, e := strconv.Atoi(args[1])
		if e != nil || port < 1 || port > 65535 {
			return usage("PORT must be between 1 and 65535")
		}
		local, _ := cmd.Flags().GetString("local")
		remote, _ := cmd.Flags().GetString("remote-host")
		node, _ := cmd.Flags().GetString("node")
		if _, _, e = net.SplitHostPort(local); e != nil {
			return usage("--local must be host:port")
		}
		if a.dryRun {
			return a.emit(map[string]any{"target": args[0], "local": local, "remote_host": remote, "remote_port": port, "dry_run": true})
		}
		b, e := a.getBackend()
		if e != nil {
			return e
		}
		listener, e := net.Listen("tcp", local)
		if e != nil {
			return e
		}
		defer listener.Close()
		if e = a.emit(map[string]any{"target": args[0], "listen": listener.Addr().String(), "remote_host": remote, "remote_port": port}); e != nil {
			return e
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		go func() { <-ctx.Done(); listener.Close() }()
		var wg sync.WaitGroup
		defer func() { cancel(); wg.Wait() }()
		for {
			conn, e := listener.Accept()
			if e != nil {
				if ctx.Err() != nil {
					return nil
				}
				return e
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				_, e := b.Stream(ctx, "connect.forward", map[string]any{"target": args[0], "node": node, "host": remote, "port": port}, conn, conn)
				if e != nil && ctx.Err() == nil {
					fmt.Fprintln(a.options.Err, "forward:", e)
				}
			}()
		}
	}
	return cmd
}
