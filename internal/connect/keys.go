// Package connect implements authenticated instance connections independently
// from the lifetime of the school's HTTP authentication session.
package connect

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"bupt.local/cli/internal/domain"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// EnsureIdentity creates a new private identity without overwriting an existing
// file. Only the public key is returned. No key is sent to the platform here.
func EnsureIdentity(filename, comment string) (string, error) {
	filename, err := ExpandLocalPath(filename)
	if err != nil {
		return "", err
	}
	if s, err := os.Lstat(filename); err == nil {
		if !s.Mode().IsRegular() {
			return "", domain.E("INVALID_KEY_FILE", "identity must be a regular file")
		}
		signer, err := LoadSigner(filename, nil)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		return "", err
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(private, safeComment(comment))
	if err != nil {
		return "", err
	}
	// Publish only a fully written key. Concurrent prepare operations must
	// never observe a half-written private identity or replace the winner.
	f, err := os.CreateTemp(filepath.Dir(filename), ".bupt-identity-*")
	if err != nil {
		return "", err
	}
	tempName := f.Name()
	defer os.Remove(tempName)
	data := pem.EncodeToMemory(block)
	_, writeErr := f.Write(data)
	for i := range data {
		data[i] = 0
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if err = os.Link(tempName, filename); errors.Is(err, os.ErrExist) {
		signer, loadErr := LoadSigner(filename, nil)
		if loadErr != nil {
			return "", loadErr
		}
		return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey()))), nil
	} else if err != nil {
		return "", err
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		return "", err
	}
	public := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	pub, err := os.OpenFile(filename+".pub", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return "", fmt.Errorf("identity created, but public-key file could not be created: %w", err)
	}
	_, err = fmt.Fprintln(pub, public+" "+safeComment(comment))
	if err == nil {
		err = pub.Sync()
	}
	if closeErr := pub.Close(); err == nil {
		err = closeErr
	}
	return public, err
}

func safeComment(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return '-'
		}
		return r
	}, s)
}

func ExpandLocalPath(filename string) (string, error) {
	if filename == "" {
		return "", domain.E("SSH_KEY_MISSING", "no identity file is configured")
	}
	if filename == "~" || strings.HasPrefix(filename, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if filename == "~" {
			return h, nil
		}
		filename = filepath.Join(h, strings.TrimPrefix(filename, "~/"))
	} else if strings.HasPrefix(filename, "~") {
		return "", domain.E("INVALID_PATH", "another user's home expansion is not supported")
	}
	return filepath.Clean(filename), nil
}

func LoadSigner(filename string, passphrase []byte) (ssh.Signer, error) {
	filename, err := ExpandLocalPath(filename)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return nil, domain.E("SSH_KEY_MISSING", "configured SSH identity file does not exist")
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, domain.E("INVALID_KEY_FILE", "SSH identity must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, domain.E("INSECURE_KEY_FILE", "SSH private key permissions must exclude group and other access")
	}
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}
	defer func() {
		for i := range data {
			data[i] = 0
		}
	}()
	var signer ssh.Signer
	if len(passphrase) > 0 {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(data, passphrase)
	} else {
		signer, err = ssh.ParsePrivateKey(data)
	}
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			return nil, domain.E("SSH_KEY_LOCKED", "the SSH identity requires a passphrase")
		}
		return nil, domain.E("INVALID_KEY_FILE", "cannot parse the configured SSH identity")
	}
	return signer, nil
}

// CanonicalPublicKey rejects authorized_keys options and additional keys.
func CanonicalPublicKey(text string) (string, error) {
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(text)))
	if err != nil || len(options) != 0 || len(strings.TrimSpace(string(rest))) != 0 {
		return "", domain.E("INVALID_PUBLIC_KEY", "expected a single public key without authorized_keys options")
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), nil
}

func Fingerprint(text string) (string, error) {
	canonical, err := CanonicalPublicKey(text)
	if err != nil {
		return "", err
	}
	key, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(canonical))
	return ssh.FingerprintSHA256(key), nil
}

// PinnedHostKeyCallback never learns a key from the connection being opened.
// Pins must come from a separately authenticated enrollment/console channel.
func PinnedHostKeyCallback(pin string) (ssh.HostKeyCallback, error) {
	if strings.HasPrefix(pin, "SHA256:") {
		if len(pin) < 20 || strings.ContainsAny(pin, "\r\n \t") {
			return nil, domain.E("INVALID_HOST_KEY", "invalid host-key fingerprint")
		}
		return func(_ string, _ net.Addr, key ssh.PublicKey) error {
			if subtle.ConstantTimeCompare([]byte(ssh.FingerprintSHA256(key)), []byte(pin)) != 1 {
				return domain.E("SSH_HOST_KEY_MISMATCH", "server host key differs from the authenticated pin")
			}
			return nil
		}, nil
	}
	canonical, err := CanonicalPublicKey(pin)
	if err != nil {
		return nil, domain.E("SSH_HOST_KEY_REQUIRED", "an authenticated host public key or trusted fingerprint is required")
	}
	expected, _, _, _, _ := ssh.ParseAuthorizedKey([]byte(canonical))
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if subtle.ConstantTimeCompare(expected.Marshal(), key.Marshal()) != 1 {
			return domain.E("SSH_HOST_KEY_MISMATCH", "server host key differs from the authenticated pin")
		}
		return nil
	}, nil
}

// A server often has several host-key types. Negotiate the enrolled key's type
// so a valid Ed25519 pin is not compared with an unrelated ECDSA host key.
func pinnedHostKeyAlgorithms(pin string) []string {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(pin))
	if err != nil {
		return nil // A fingerprint alone does not identify its key algorithm.
	}
	switch key.Type() {
	case ssh.KeyAlgoRSA:
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256}
	case ssh.CertAlgoRSAv01:
		return []string{ssh.CertAlgoRSASHA512v01, ssh.CertAlgoRSASHA256v01}
	default:
		return []string{key.Type()}
	}
}

func relayHostKeyCallback(r domain.RelayConfig) (ssh.HostKeyCallback, error) {
	if r.HostKey != "" {
		return PinnedHostKeyCallback(r.HostKey)
	}
	if r.KnownHostsFile != "" {
		name, err := ExpandLocalPath(r.KnownHostsFile)
		if err != nil {
			return nil, err
		}
		callback, err := knownhosts.New(name)
		if err != nil {
			return nil, domain.E("SSH_HOST_KEY_REQUIRED", "cannot read the configured relay known_hosts file")
		}
		return callback, nil
	}
	return nil, domain.E("SSH_HOST_KEY_REQUIRED", "relay has no trusted host key")
}
