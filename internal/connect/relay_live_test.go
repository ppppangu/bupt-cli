//go:build integration

package connect_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"bupt.local/cli/internal/client"
	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"
)

// Explicit opt-in exercises only the configured, already-installed relay. It
// creates a temporary loopback lease and keys; it never calls school APIs.
func TestLiveRelayRoundTripAndRevoke(t *testing.T) {
	if os.Getenv("BUPT_LIVE_RELAY_TEST") != "1" {
		t.Skip("set BUPT_LIVE_RELAY_TEST=1 to test your configured relay")
	}
	cfg, e := config.Load(config.Options{NoProject: true})
	if e != nil {
		t.Fatal(e)
	}
	profile, e := cfg.SelectedProfile()
	if e != nil {
		t.Fatal(e)
	}
	if profile.Relay.Address == "" {
		t.Fatal("relay is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	if e = os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	reversePath := filepath.Join(dir, "reverse")
	reversePublic, e := connect.EnsureIdentity(reversePath, "bupt-live-reverse")
	if e != nil {
		t.Fatal(e)
	}
	accessPath := filepath.Join(dir, "access")
	accessPublic, e := connect.EnsureIdentity(accessPath, "bupt-live-access")
	if e != nil {
		t.Fatal(e)
	}
	accessKey, _, _, _, e := ssh.ParseAuthorizedKey([]byte(accessPublic))
	if e != nil {
		t.Fatal(e)
	}
	_, private, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	signer, e := ssh.NewSignerFromKey(private)
	if e != nil {
		t.Fatal(e)
	}
	sshConfig := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() == "smoke" && bytes.Equal(k.Marshal(), accessKey.Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("unrecognized test identity")
	}}
	sshConfig.AddHostKey(signer)
	local, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer local.Close()
	go func() {
		for {
			conn, e := local.Accept()
			if e != nil {
				return
			}
			go func() {
				defer conn.Close()
				server, channels, requests, e := ssh.NewServerConn(conn, sshConfig)
				if e != nil {
					return
				}
				defer server.Close()
				go func() {
					for req := range requests {
						req.Reply(true, nil)
					}
				}()
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "test only")
						continue
					}
					channel, reqs, e := incoming.Accept()
					if e != nil {
						return
					}
					go func() {
						defer channel.Close()
						for req := range reqs {
							if req.Type == "exec" {
								req.Reply(true, nil)
								io.WriteString(channel, "BUPT_RELAY_SMOKE\n")
								channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{7}))
								return
							}
							req.Reply(false, nil)
						}
					}()
				}
			}()
		}
	}()
	rpc := func(method string, p any, out any) error {
		return client.GenericRelayRPC(ctx, profile.Relay, domain.RPCRequest{ID: uuid.NewString(), Method: method, Profile: profile.Name, Params: domain.Result(p)}, out)
	}
	marker := fmt.Sprint(time.Now().UnixNano())
	var lease domain.PortLease
	revoked := false
	if e = rpc("relay.reserve", map[string]string{"instance_id": marker, "node_id": marker}, &lease); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if revoked {
			return
		}
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		var receipt domain.PortLease
		e := client.GenericRelayRPC(cleanup, profile.Relay, domain.RPCRequest{Method: "relay.revoke", Profile: profile.Name, Params: domain.Result(map[string]string{"key": lease.Key})}, &receipt)
		if e != nil {
			t.Errorf("temporary relay lease cleanup failed: %v", e)
		}
	}()
	if e = rpc("relay.enroll", map[string]string{"key": lease.Key, "public_key": reversePublic}, &lease); e != nil {
		t.Fatal(e)
	}
	tunnelConfig := profile.Relay
	tunnelConfig.IdentityFile = reversePath
	tunnel, e := connect.DialRelay(ctx, tunnelConfig)
	if e != nil {
		t.Fatal(e)
	}
	defer tunnel.Close()
	listener, e := tunnel.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", lease.Port))
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go func() {
		for {
			incoming, e := listener.Accept()
			if e != nil {
				return
			}
			go func() {
				defer incoming.Close()
				target, e := net.Dial("tcp", local.Addr().String())
				if e != nil {
					return
				}
				defer target.Close()
				go func() {
					io.Copy(target, incoming)
					if tcp, ok := target.(*net.TCPConn); ok {
						tcp.CloseWrite()
					}
				}()
				io.Copy(incoming, target)
			}()
		}
	}()
	route := domain.Route{InstanceID: domain.ID(marker), NodeID: domain.ID(marker), Kind: "relay", Username: "smoke", Host: "127.0.0.1", Port: lease.Port, HostKey: string(ssh.MarshalAuthorizedKey(signer.PublicKey())), IdentityFile: accessPath, Relay: profile.Relay, RelayLeaseKey: lease.Key}
	var out, stderr bytes.Buffer
	code, e := connect.Run(ctx, route, connect.SessionOptions{Command: []string{"smoke"}}, nil, &out, &stderr, nil, nil)
	if e != nil || code != 7 || out.String() != "BUPT_RELAY_SMOKE\n" {
		t.Fatalf("relay byte stream/exit code failed: code=%d error=%v", code, e)
	}
	var forbidden any
	if e = client.GenericRelayRPC(ctx, tunnelConfig, domain.RPCRequest{Method: "agent.status", Profile: profile.Name}, &forbidden); e == nil {
		t.Fatal("reverse-only key obtained management RPC access")
	}
	listener.Close()
	tunnel.Close()
	if e = rpc("relay.revoke", map[string]string{"key": lease.Key}, &lease); e != nil {
		t.Fatal(e)
	}
	revoked = true
	if _, e = connect.DialSSH(ctx, route); e == nil {
		t.Fatal("revoked lease still allowed a new proxy connection")
	}
	t.Logf("verified pinned SSH round-trip, exact exit status, reverse-key restriction and revoked proxy denial (loopback port %d)", lease.Port)
}
