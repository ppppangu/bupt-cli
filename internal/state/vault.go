package state

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"bupt.local/cli/internal/domain"
	bolt "go.etcd.io/bbolt"
)

var _ domain.SecretStore = (*Vault)(nil)

type Vault struct {
	db     *bolt.DB
	aead   cipher.AEAD
	key    []byte
	mu     sync.RWMutex
	closed bool
}
type secretEnvelope struct {
	Version    int       `json:"version"`
	Nonce      []byte    `json:"nonce"`
	Ciphertext []byte    `json:"ciphertext"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CreateMasterKey is explicit setup only. Existing keys are never overwritten.
func CreateMasterKey(path string) error {
	if path == "" {
		return domain.E("MASTER_KEY_REQUIRED", "a master key path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	key := make([]byte, 32)
	defer clear(key)
	good := false
	defer func() {
		f.Close()
		if !good {
			os.Remove(path)
		}
	}()
	if _, err = io.ReadFull(rand.Reader, key); err != nil {
		return err
	}
	if _, err = f.Write(key); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	good = true
	return f.Close()
}

func masterKey(path string) ([]byte, error) {
	if path == "" {
		path = os.Getenv("BUPT_MASTER_KEY_FILE")
	}
	if path == "" && os.Getenv("CREDENTIALS_DIRECTORY") != "" {
		for _, name := range []string{"master-key", "bupt-master-key"} {
			candidate := filepath.Join(os.Getenv("CREDENTIALS_DIRECTORY"), name)
			if _, e := os.Stat(candidate); e == nil {
				path = candidate
				break
			}
		}
	}
	var data []byte
	var err error
	if path != "" {
		if err = secureExistingFile(path); err != nil {
			return nil, err
		}
		if st, e := os.Stat(path); e != nil {
			return nil, e
		} else if st.Size() > 4096 {
			return nil, domain.E("INVALID_MASTER_KEY", "master key file is too large")
		}
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if len(data) == 32 {
			return data, nil
		}
	}
	if path == "" {
		data = []byte(os.Getenv("BUPT_MASTER_KEY"))
	}
	if len(data) == 0 {
		return nil, domain.E("MASTER_KEY_REQUIRED", "provide a master key file, systemd credential, or BUPT_MASTER_KEY; plaintext credential storage is disabled")
	}
	defer clear(data)
	text := strings.TrimSpace(string(data))
	if decoded, e := base64.StdEncoding.DecodeString(text); e == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, e := base64.RawStdEncoding.DecodeString(text); e == nil && len(decoded) == 32 {
		return decoded, nil
	}
	if decoded, e := hex.DecodeString(text); e == nil && len(decoded) == 32 {
		return decoded, nil
	}
	return nil, domain.E("INVALID_MASTER_KEY", "master key must be 32 random bytes or their base64/hex encoding")
}

func OpenVault(dir, masterKeyFile string) (*Vault, error) {
	key, err := masterKey(masterKeyFile)
	if err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			clear(key)
		}
	}()
	if err = secureDir(dir); err != nil {
		return nil, err
	}
	p := filepath.Join(dir, "credentials.db")
	if err = secureExistingFile(p); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	db, err := bolt.Open(p, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, err
	}
	v := &Vault{db: db, aead: aead, key: key}
	err = db.Update(func(tx *bolt.Tx) error {
		b, e := tx.CreateBucketIfNotExists([]byte("secrets"))
		if e != nil {
			return e
		}
		meta, e := tx.CreateBucketIfNotExists([]byte("meta"))
		if e != nil {
			return e
		}
		_ = b
		if version := meta.Get([]byte("version")); version != nil && string(version) != "1" {
			return domain.E("VAULT_VERSION", "unsupported credential format")
		}
		if check := meta.Get([]byte("key_check")); check != nil {
			var env secretEnvelope
			if e = json.Unmarshal(check, &env); e != nil {
				return domain.E("VAULT_INTEGRITY", "invalid credential metadata")
			}
			plain, e := v.open("\x00key-check", env)
			if e != nil {
				return e
			}
			defer clear(plain)
			if !bytes.Equal(plain, []byte("bupt vault version 1")) {
				return domain.E("VAULT_INTEGRITY", "invalid credential metadata")
			}
		}
		if meta.Get([]byte("key_check")) == nil {
			env, e := v.seal("\x00key-check", []byte("bupt vault version 1"))
			if e != nil {
				return e
			}
			data, e := json.Marshal(env)
			if e != nil {
				return e
			}
			if e = meta.Put([]byte("key_check"), data); e != nil {
				return e
			}
		}
		return meta.Put([]byte("version"), []byte("1"))
	})
	if err != nil {
		db.Close()
		return nil, err
	}
	cleanup = false
	return v, nil
}

func secretKey(name string) []byte { h := sha256.Sum256([]byte(name)); return h[:] }
func aad(name string) []byte       { return []byte("bupt-vault:v1:" + name) }
func (v *Vault) seal(name string, data []byte) (secretEnvelope, error) {
	nonce := make([]byte, v.aead.NonceSize())
	if _, e := io.ReadFull(rand.Reader, nonce); e != nil {
		return secretEnvelope{}, e
	}
	return secretEnvelope{Version: 1, Nonce: nonce, Ciphertext: v.aead.Seal(nil, nonce, data, aad(name)), UpdatedAt: time.Now().UTC()}, nil
}
func (v *Vault) open(name string, env secretEnvelope) ([]byte, error) {
	if env.Version != 1 || len(env.Nonce) != v.aead.NonceSize() {
		return nil, domain.E("VAULT_INTEGRITY", "invalid encrypted credential envelope")
	}
	p, e := v.aead.Open(nil, env.Nonce, env.Ciphertext, aad(name))
	if e != nil {
		return nil, domain.E("SECRET_DECRYPTION_FAILED", "credential decryption failed; verify the master key and encrypted store")
	}
	return p, nil
}
func validSecretName(name string) error {
	if name == "" || len(name) > 512 || strings.ContainsRune(name, 0) {
		return domain.E("INVALID_SECRET_NAME", "invalid credential name")
	}
	return nil
}
func (v *Vault) Get(name string, out any) error {
	if e := validSecretName(name); e != nil {
		return e
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.closed {
		return domain.E("VAULT_CLOSED", "credential store is closed")
	}
	return v.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("secrets")).Get(secretKey(name))
		if b == nil {
			return notFound()
		}
		var env secretEnvelope
		if e := json.Unmarshal(b, &env); e != nil {
			return domain.E("VAULT_INTEGRITY", "invalid credential record")
		}
		plain, e := v.open(name, env)
		if e != nil {
			return e
		}
		defer clear(plain)
		d := json.NewDecoder(bytes.NewReader(plain))
		d.UseNumber()
		if e = d.Decode(out); e != nil {
			return domain.E("SECRET_FORMAT", "credential record has an incompatible format")
		}
		return nil
	})
}
func (v *Vault) Put(name string, value any) error {
	if e := validSecretName(name); e != nil {
		return e
	}
	plain, e := json.Marshal(value)
	if e != nil {
		return e
	}
	defer clear(plain)
	if len(plain) > 16<<20 {
		return domain.E("SECRET_TOO_LARGE", "credential record exceeds 16 MiB")
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.closed {
		return domain.E("VAULT_CLOSED", "credential store is closed")
	}
	env, e := v.seal(name, plain)
	if e != nil {
		return e
	}
	data, e := json.Marshal(env)
	if e != nil {
		return e
	}
	return v.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("secrets")).Put(secretKey(name), data) })
}
func (v *Vault) Delete(name string) error {
	if e := validSecretName(name); e != nil {
		return e
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.closed {
		return domain.E("VAULT_CLOSED", "credential store is closed")
	}
	return v.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("secrets")).Delete(secretKey(name)) })
}
func (v *Vault) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.closed {
		return nil
	}
	v.closed = true
	err := v.db.Close()
	clear(v.key)
	v.aead = nil
	return err
}

func IsNotFound(err error) bool {
	var e *domain.Error
	return errors.As(err, &e) && e.Code == "NOT_FOUND"
}
