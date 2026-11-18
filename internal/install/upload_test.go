package install

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

// This in-memory SFTP peer delays the first write acknowledgement until a second
// write arrives. It exercises pipelining without an SSH server, TCP listener or
// timing-based throughput assertion. All file writes stay in t.TempDir().
func pipelinedSFTP(t *testing.T) (*sftp.Client, <-chan struct{}) {
	t.Helper()
	client, clientProxy := net.Pipe()
	server, serverProxy := net.Pipe()
	peer, err := sftp.NewServer(server, sftp.WithServerWorkingDirectory(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	secondWrite := make(chan struct{})
	firstWrite := make(chan struct{})
	shutdown := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(3)
	go func() { defer workers.Done(); _ = peer.Serve() }()
	go func() {
		defer workers.Done()
		writes := 0
		for {
			packet, err := sftpPacket(clientProxy)
			if err != nil {
				return
			}
			if packet[4] == 6 { // SSH_FXP_WRITE
				writes++
				if writes == 1 {
					close(firstWrite)
				}
				if writes == 2 {
					close(secondWrite)
				}
			}
			if _, err = serverProxy.Write(packet); err != nil {
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			packet, err := sftpPacket(serverProxy)
			if err != nil {
				return
			}
			if packet[4] == 101 { // SSH_FXP_STATUS (write acknowledgement)
				select {
				case <-firstWrite:
					select {
					case <-secondWrite:
					case <-shutdown:
						return
					}
				default:
				}
			}
			if _, err = clientProxy.Write(packet); err != nil {
				return
			}
		}
	}()
	cl, err := sftp.NewClientPipe(client, client, uploadClientOptions()...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(shutdown)
		_ = cl.Close()
		_ = client.Close()
		_ = clientProxy.Close()
		_ = serverProxy.Close()
		_ = peer.Close()
		_ = server.Close()
		workers.Wait()
	})
	return cl, secondWrite
}

func sftpPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size < 1 || size > 1<<20 {
		return nil, errors.New("invalid SFTP packet size")
	}
	packet := make([]byte, 4+int(size))
	copy(packet, header[:])
	_, err := io.ReadFull(r, packet[4:])
	return packet, err
}

func TestUploadPipelinesBinaryAndPreservesContents(t *testing.T) {
	cl, pipelined := pipelinedSFTP(t)
	r := &sshRemote{sftp: cl}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "staged-binary")
	data := bytes.Repeat([]byte("BUPT upload test\x00\xff"), 128<<10)
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err = r.Upload(ctx, destination, f, 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-pipelined:
	default:
		t.Fatal("upload waited for each write acknowledgement")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("concurrent upload changed file contents")
	}
	requireMode(t, destination, 0600)
	if err = r.Upload(ctx, destination, bytes.NewReader([]byte("replacement")), 0600); err == nil {
		t.Fatal("overwrote an existing staged file")
	}
}

func TestUploadCancellationInterruptsPendingWrites(t *testing.T) {
	cl, _ := pipelinedSFTP(t)
	r := &sshRemote{sftp: cl}
	// One packet cannot satisfy the peer's two-write gate, simulating a server
	// that stops acknowledging an in-flight transfer.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	destination := filepath.Join(t.TempDir(), "partial")
	started := time.Now()
	err := r.Upload(ctx, destination, bytes.NewReader(bytes.Repeat([]byte("x"), 32<<10)), 0600)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want caller deadline, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("cancellation waited on the file's write lock")
	}
}
