package state

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func newVault(t *testing.T) (*Vault, string, string) {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "master.key")
	if e := CreateMasterKey(key); e != nil {
		t.Fatal(e)
	}
	vaultDir := filepath.Join(dir, "vault")
	v, e := OpenVault(vaultDir, key)
	if e != nil {
		t.Fatal(e)
	}
	return v, vaultDir, key
}

func TestVaultRoundTripAndNoPlaintextOnDisk(t *testing.T) {
	v, dir, key := newVault(t)
	value := map[string]any{"password": "platform-password-unique-124", "cookie": "opaque-cookie-unique-356", "uid": json.Number("9007199254740993")}
	if e := v.Put("profile/bupt/session", value); e != nil {
		t.Fatal(e)
	}
	var got map[string]any
	if e := v.Get("profile/bupt/session", &got); e != nil {
		t.Fatal(e)
	}
	if got["password"] != value["password"] || got["uid"].(json.Number).String() != "9007199254740993" {
		t.Fatal("bad decrypted data")
	}
	if e := v.Close(); e != nil {
		t.Fatal(e)
	}
	file, e := os.ReadFile(filepath.Join(dir, "credentials.db"))
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{"platform-password-unique-124", "opaque-cookie-unique-356", "profile/bupt/session"} {
		if bytes.Contains(file, []byte(secret)) {
			t.Fatalf("plaintext %q on disk", secret)
		}
	}
	v, e = OpenVault(dir, key)
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	if e = v.Get("profile/bupt/session", &got); e != nil {
		t.Fatal(e)
	}
	if e = v.Delete("profile/bupt/session"); e != nil {
		t.Fatal(e)
	}
	if e = v.Get("profile/bupt/session", &got); !IsNotFound(e) {
		t.Fatal(e)
	}
}

func TestWrongKeyAndTamperedCiphertextAreRejected(t *testing.T) {
	v, dir, key := newVault(t)
	if e := v.Put("auth", map[string]string{"password": "value"}); e != nil {
		t.Fatal(e)
	}
	v.Close()
	other := filepath.Join(t.TempDir(), "other.key")
	if e := CreateMasterKey(other); e != nil {
		t.Fatal(e)
	}
	if opened, e := OpenVault(dir, other); errCode(e) != "SECRET_DECRYPTION_FAILED" {
		if opened != nil {
			opened.Close()
		}
		t.Fatal(e)
	}
	db, e := bolt.Open(filepath.Join(dir, "credentials.db"), 0o600, nil)
	if e != nil {
		t.Fatal(e)
	}
	e = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("secrets"))
		var env secretEnvelope
		if e := json.Unmarshal(b.Get(secretKey("auth")), &env); e != nil {
			return e
		}
		env.Ciphertext[0] ^= 0xff
		data, e := json.Marshal(env)
		if e != nil {
			return e
		}
		return b.Put(secretKey("auth"), data)
	})
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	v, e = OpenVault(dir, key)
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	var out map[string]string
	if e = v.Get("auth", &out); errCode(e) != "SECRET_DECRYPTION_FAILED" {
		t.Fatal(e)
	}
}

func TestRecordBindingPreventsCrossProfileSwaps(t *testing.T) {
	v, dir, key := newVault(t)
	v.Put("first", map[string]string{"cookie": "first-cookie"})
	v.Put("second", map[string]string{"cookie": "second-cookie"})
	v.Close()
	db, e := bolt.Open(filepath.Join(dir, "credentials.db"), 0o600, nil)
	if e != nil {
		t.Fatal(e)
	}
	e = db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("secrets"))
		a := append([]byte{}, b.Get(secretKey("first"))...)
		return b.Put(secretKey("second"), a)
	})
	if e != nil {
		t.Fatal(e)
	}
	db.Close()
	v, e = OpenVault(dir, key)
	if e != nil {
		t.Fatal(e)
	}
	defer v.Close()
	var out map[string]string
	if e = v.Get("second", &out); errCode(e) != "SECRET_DECRYPTION_FAILED" {
		t.Fatal(e)
	}
}

func TestExplicitMasterKeyOnlyAndPermissions(t *testing.T) {
	t.Setenv("BUPT_MASTER_KEY_FILE", "")
	t.Setenv("BUPT_MASTER_KEY", "")
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	if _, e := OpenVault(filepath.Join(t.TempDir(), "vault"), ""); errCode(e) != "MASTER_KEY_REQUIRED" {
		t.Fatal(e)
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if e := CreateMasterKey(key); e != nil {
		t.Fatal(e)
	}
	old, _ := os.ReadFile(key)
	if e := CreateMasterKey(key); !errors.Is(e, os.ErrExist) {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(key)
	if !bytes.Equal(old, after) {
		t.Fatal("master key was replaced")
	}
	if e := os.Chmod(key, 0o644); e != nil {
		t.Fatal(e)
	}
	if _, e := OpenVault(filepath.Join(dir, "vault"), key); errCode(e) != "INSECURE_PERMISSIONS" {
		t.Fatal(e)
	}
	t.Setenv("BUPT_MASTER_KEY", base64.StdEncoding.EncodeToString(old))
	v, e := OpenVault(filepath.Join(dir, "env-vault"), "")
	if e != nil {
		t.Fatal(e)
	}
	v.Close()
}

func TestVaultLogoutDoesNotDeleteConnectionState(t *testing.T) {
	v, dir, _ := newVault(t)
	defer v.Close()
	s, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if e = s.Put("routes", "cached", map[string]any{"host": "127.0.0.1", "port": 24000}); e != nil {
		t.Fatal(e)
	}
	if e = v.Put("platform/auth", map[string]string{"cookie": "secret"}); e != nil {
		t.Fatal(e)
	}
	if e = v.Delete("platform/auth"); e != nil {
		t.Fatal(e)
	}
	var route map[string]any
	if e = s.Get("routes", "cached", &route); e != nil || route["host"] != "127.0.0.1" {
		t.Fatal(route, e)
	}
}

func TestBadNonceDoesNotPanic(t *testing.T) {
	v, _, _ := newVault(t)
	defer v.Close()
	if e := v.Put("auth", "value"); e != nil {
		t.Fatal(e)
	}
	e := v.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("secrets"))
		var env secretEnvelope
		if e := json.Unmarshal(b.Get(secretKey("auth")), &env); e != nil {
			return e
		}
		env.Nonce = []byte{1}
		data, e := json.Marshal(env)
		if e != nil {
			return e
		}
		return b.Put(secretKey("auth"), data)
	})
	if e != nil {
		t.Fatal(e)
	}
	var out string
	if e = v.Get("auth", &out); errCode(e) != "VAULT_INTEGRITY" {
		t.Fatal(e)
	}
}
