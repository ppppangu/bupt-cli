package main

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"bupt.local/cli/internal/cli"
	"bupt.local/cli/internal/client"
	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/control"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/install"
)

var version = "dev"

func local(ctx context.Context, cfg *config.Config, method string, p map[string]any, in io.Reader, out, errOut io.Writer) (any, error) {
	switch method {
	case "agent.run":
		if dry, _ := p["dry_run"].(bool); dry {
			return map[string]any{"operation": method, "dry_run": true, "socket": cfg.Paths.Socket}, nil
		}
		ctx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		p["controller_version"] = version
		return nil, control.RunAgent(ctx, cfg, p, errOut)
	case "agent.gateway":
		get := func(k string) string { s, _ := p[k].(string); return s }
		socket := get("socket")
		if socket == "" {
			socket = cfg.Paths.Socket
		}
		if dry, _ := p["dry_run"].(bool); dry {
			return nil, domain.E("INVALID_MODE", "gateway does not support dry-run")
		}
		return nil, control.Gateway(ctx, socket, get("client"), os.Getenv("SSH_ORIGINAL_COMMAND"), in, out)
	case "agent.status", "agent.reload":
		c, e := client.New(cfg, cfg.Selected)
		if e != nil {
			return nil, e
		}
		var result any
		e = c.Call(ctx, method, p, &result)
		return result, e
	default:
		return install.Handle(ctx, cfg, method, p, in, out, errOut)
	}
}

func main() {
	hooks := cli.Hooks{NewClient: func(cfg *config.Config, p string) (cli.Backend, error) { return client.New(cfg, p) }, Local: local}
	os.Exit(cli.ExecuteOptions(context.Background(), os.Args[1:], cli.Options{In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Hooks: hooks, Version: version}))
}
