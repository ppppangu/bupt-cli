package install

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"golang.org/x/crypto/ssh"
)

func (h *handler) key(ctx context.Context, cfg *config.Config, method string, p map[string]any) (any, error) {
	name := paramString(p, "name")
	if !safeName.MatchString(name) {
		return nil, domain.E("INVALID_KEY_NAME", "key name must be a safe identifier")
	}
	identity := paramString(p, "identity_file")
	if method == "key.generate" && identity == "" {
		identity = filepath.Join(cfg.Paths.DataDir, "keys", name+"_ed25519")
	}
	var err error
	if identity != "" {
		identity, err = h.path(identity)
		if err != nil {
			return nil, err
		}
	}
	publicFile := paramString(p, "public_key_file")
	if publicFile != "" {
		publicFile, err = h.path(publicFile)
		if err != nil {
			return nil, err
		}
	}
	if method == "key.import" && identity == "" && publicFile == "" {
		return nil, domain.E("KEY_FILE_REQUIRED", "key import requires --identity-file or --public-key-file")
	}
	if paramBool(p, "dry_run", false) {
		return map[string]any{"operation": method, "name": name, "identity_file": identity, "public_key_file": publicFile, "dry_run": true}, nil
	}
	if err = ensurePrivateDir(cfg.Paths.DataDir); err != nil {
		return nil, err
	}
	keysDir := filepath.Join(cfg.Paths.DataDir, "keys")
	if err = ensurePrivateDir(keysDir); err != nil {
		return nil, err
	}
	var public string
	if method == "key.generate" {
		if err = ensurePrivateDir(filepath.Dir(identity)); err != nil {
			return nil, err
		}
		public, err = connect.EnsureIdentity(identity, "bupt "+name)
		if err != nil {
			return nil, err
		}
	} else if identity != "" {
		signer, e := connect.LoadSigner(identity, nil)
		if e != nil {
			return nil, e
		}
		public = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	}
	if publicFile != "" {
		data, e := readRegular(publicFile, 64<<10)
		if e != nil {
			return nil, e
		}
		provided, e := connect.CanonicalPublicKey(string(data))
		if e != nil {
			return nil, e
		}
		if public != "" && provided != public {
			return nil, domain.E("KEY_MISMATCH", "public key does not match the selected private identity")
		}
		public = provided
	}
	if public == "" {
		return nil, domain.E("PUBLIC_KEY_REQUIRED", "a public key is required")
	}
	params := map[string]any{"name": name, "public_key": public}
	if target := paramString(p, "target"); target != "" {
		params["target"] = target
	}
	var enrolled any
	if err = h.rpc(ctx, cfg, "key.import", params, &enrolled); err != nil {
		return map[string]any{"name": name, "identity_file": identity, "public_key": public, "enrolled": false}, err
	}
	metadata := map[string]any{"name": name, "public_key": public, "identity_file": identity}
	data, _ := json.MarshalIndent(metadata, "", "  ")
	backup, err := h.replaceFile(filepath.Join(keysDir, name+".json"), append(data, '\n'), 0600)
	if err != nil {
		return nil, err
	}
	metadata["enrollment"] = enrolled
	metadata["enrolled"] = true
	if backup != "" {
		metadata["backup"] = backup
	}
	return metadata, nil
}
