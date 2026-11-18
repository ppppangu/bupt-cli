package platform

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"

	"bupt.local/cli/internal/domain"
)

type Storage struct {
	Name      string `json:"name"`
	URLPath   string `json:"url_path"`
	HomePath  string `json:"home_path,omitempty"`
	IsWorkDir bool   `json:"is_work_dir"`
}
type StoragePath struct {
	Store   string    `json:"store"`
	GroupID domain.ID `json:"group_id,omitempty"`
	Path    string    `json:"path"`
}
type File struct {
	Name     string `json:"name"`
	IsDir    bool   `json:"is_dir"`
	Size     int64  `json:"size"`
	Modified string `json:"modified,omitempty"`
	Owner    string `json:"owner,omitempty"`
}
type UploadOptions struct {
	ResumeURL   string
	ChunkSize   int64
	ContentType string
	Progress    func(UploadResult) error
}
type UploadResult struct {
	URL      string `json:"url"`
	Offset   int64  `json:"offset"`
	Size     int64  `json:"size"`
	Complete bool   `json:"complete"`
}

func (c *Client) StorageConfig(ctx context.Context) ([]Storage, error) {
	var m map[string]any
	if e := c.jsonRequest(ctx, "GET", "/api/storage/config", nil, &m); e != nil {
		return nil, e
	}
	v, e := unwrap(m)
	if e != nil {
		return nil, e
	}
	m = object(v)
	out := []Storage{}
	for _, v := range array(m["stores"]) {
		s := object(v)
		out = append(out, Storage{Name: stringValue(s["name"]), URLPath: stringValue(s["urlPath"]), HomePath: stringValue(s["homePath"]), IsWorkDir: boolValue(s["isWorkDir"])})
	}
	return out, nil
}
func storageSegments(s string) (string, error) {
	if strings.ContainsAny(s, "\x00\\") {
		return "", domain.E("INVALID_PATH", "invalid storage path")
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	encoded := []string{}
	for _, p := range parts {
		if p == ".." || p == "." {
			return "", domain.E("INVALID_PATH", "storage paths must not contain dot segments")
		}
		if p != "" {
			encoded = append(encoded, url.PathEscape(p))
		}
	}
	return strings.Join(encoded, "/"), nil
}
func (c *Client) storageURL(p StoragePath, kind string, dir bool) (string, error) {
	store, e := storageSegments(p.Store)
	if e != nil {
		return "", e
	}
	fp, e := storageSegments(p.Path)
	if e != nil {
		return "", e
	}
	b := "/api/storage"
	if store != "" {
		b += "/" + store
	}
	if p.GroupID != "" {
		b += "/groups/" + url.PathEscape(string(p.GroupID))
	}
	b += "/" + kind + "/" + fp
	if dir && !strings.HasSuffix(b, "/") {
		b += "/"
	}
	return c.endpoint(b), nil
}
func (c *Client) ListFiles(ctx context.Context, p StoragePath) ([]File, error) {
	u, e := c.storageURL(p, "folders", false)
	if e != nil {
		return nil, e
	}
	var m map[string]any
	if e = c.jsonURL(ctx, "GET", u, nil, &m); e != nil {
		return nil, e
	}
	v, e := unwrap(m)
	if e != nil {
		return nil, e
	}
	m = object(v)
	out := []File{}
	for _, v := range array(m["items"]) {
		f := object(v)
		out = append(out, File{Name: stringValue(f["name"]), IsDir: boolValue(f["isDir"]), Size: integer(f["size"]), Modified: stringValue(f["modified"]), Owner: stringValue(f["owner"])})
	}
	return out, nil
}
func (c *Client) Download(ctx context.Context, p StoragePath, w io.Writer) error {
	u, e := c.storageURL(p, "download", false)
	if e != nil {
		return e
	}
	r, e := c.do(ctx, "GET", u, nil, nil)
	if e != nil {
		return e
	}
	defer r.Body.Close()
	if e = c.responseError(r); e != nil {
		return e
	}
	_, e = io.Copy(w, r.Body)
	return e
}
func (c *Client) validateUploadURL(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return "", domain.E("TUS_PROTOCOL", "invalid upload URL")
	}
	base := c.base
	// The native storage server may return its own absolute Location. Wengine's
	// browser shim proxies this URL; reproduce that mapping only for the already
	// configured native platform origin, never for an arbitrary third party.
	if c.gateway != nil && u.Host != "" && u.Host != base.Host {
		native, _ := url.Parse(c.profile.PlatformURL)
		if native != nil && u.Host == native.Host && u.Scheme == native.Scheme {
			mapped, err := ProxyURL(c.gateway.String(), u.String())
			if err != nil {
				return "", err
			}
			u, _ = url.Parse(mapped)
		}
	}
	if u.Host == "" && strings.HasPrefix(u.Path, "/api/storage/") && proxyPath.MatchString(base.EscapedPath()) {
		mapped, _ := url.Parse(c.endpoint(u.EscapedPath()))
		mapped.RawQuery = u.RawQuery
		u = mapped
	}
	u = base.ResolveReference(u)
	if _, e := storageSegments(u.Path); e != nil {
		return "", domain.E("TUS_PROTOCOL", "upload URL contains invalid path segments")
	}
	prefix := strings.TrimRight(base.EscapedPath(), "/") + "/api/storage/"
	if u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || !strings.HasPrefix(u.EscapedPath(), prefix) {
		return "", domain.E("TUS_PROTOCOL", "upload URL is outside platform storage")
	}
	return u.String(), nil
}
func tusHeaders() http.Header { return http.Header{"Tus-Resumable": []string{"1.0.0"}} }
func (c *Client) Upload(ctx context.Context, p StoragePath, reader io.ReadSeeker, size int64, opt UploadOptions) (UploadResult, error) {
	result := UploadResult{Size: size}
	if reader == nil {
		return result, domain.E("INVALID_INPUT", "upload reader is required")
	}
	chunk := opt.ChunkSize
	if chunk == 0 {
		chunk = 8 << 20
	}
	if chunk < 1 || chunk > 64<<20 {
		return result, domain.E("INVALID_SIZE", "TUS chunk size must be between 1 byte and 64 MiB")
	}
	if size < 0 {
		return result, domain.E("INVALID_SIZE", "upload size must be nonnegative")
	}
	if _, e := storageSegments(p.Path); e != nil {
		return result, e
	}
	name := path.Base(p.Path)
	if name == "." || name == "/" || name == "" {
		return result, domain.E("INVALID_PATH", "upload target must include a filename")
	}
	dir := p
	dir.Path = path.Dir(p.Path)
	if dir.Path == "." {
		dir.Path = ""
	}
	endpoint, e := c.storageURL(dir, "files", true)
	if e != nil {
		return result, e
	}
	if opt.ResumeURL != "" {
		result.URL, e = c.validateUploadURL(opt.ResumeURL)
		if e != nil {
			return result, e
		}
		r, e := c.do(ctx, "HEAD", result.URL, nil, tusHeaders())
		if e != nil {
			return result, e
		}
		r.Body.Close()
		if e = c.responseError(r); e != nil {
			return result, e
		}
		offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || offset < 0 || offset > size {
			return result, domain.E("TUS_PROTOCOL", "invalid resume offset")
		}
		length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
		if err != nil || length != size {
			return result, domain.E("TUS_MISMATCH", "resume upload has a different size")
		}
		result.Offset = offset
	} else {
		h := tusHeaders()
		h.Set("Upload-Length", strconv.FormatInt(size, 10))
		metadata := map[string]string{"name": name, "filename": name, "type": opt.ContentType, "filetype": opt.ContentType}
		keys := []string{}
		for k := range metadata {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := []string{}
		for _, k := range keys {
			parts = append(parts, k+" "+base64.StdEncoding.EncodeToString([]byte(metadata[k])))
		}
		h.Set("Upload-Metadata", strings.Join(parts, ","))
		r, e := c.do(ctx, "POST", endpoint, nil, h)
		if e != nil {
			return result, e
		}
		r.Body.Close()
		if e = c.responseError(r); e != nil {
			return result, e
		}
		loc := r.Header.Get("Location")
		if loc == "" {
			return result, domain.E("TUS_PROTOCOL", "upload response omitted Location")
		}
		base, _ := url.Parse(endpoint)
		ref, e := url.Parse(loc)
		if e != nil {
			return result, e
		}
		if ref.Host == "" && strings.HasPrefix(ref.Path, "/api/storage/") && proxyPath.MatchString(c.base.EscapedPath()) {
			result.URL, e = c.validateUploadURL(loc)
		} else {
			result.URL, e = c.validateUploadURL(base.ResolveReference(ref).String())
		}
		if e != nil {
			return result, e
		}
	}
	if opt.Progress != nil {
		if e = opt.Progress(result); e != nil {
			return result, e
		}
	}
	if _, e = reader.Seek(result.Offset, io.SeekStart); e != nil {
		return result, e
	}
	for result.Offset < size {
		n := min(chunk, size-result.Offset)
		h := tusHeaders()
		h.Set("Content-Type", "application/offset+octet-stream")
		h.Set("Upload-Offset", strconv.FormatInt(result.Offset, 10))
		limited := &io.LimitedReader{R: reader, N: n}
		r, e := c.do(ctx, "PATCH", result.URL, limited, h)
		if e != nil {
			return result, e
		}
		r.Body.Close()
		if e = c.responseError(r); e != nil {
			return result, e
		}
		next, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
		if err != nil || next != result.Offset+n || limited.N != 0 {
			return result, domain.E("TUS_PROTOCOL", fmt.Sprintf("unexpected uploaded offset after byte %d", result.Offset))
		}
		result.Offset = next
		if opt.Progress != nil {
			if e = opt.Progress(result); e != nil {
				return result, e
			}
		}
	}
	result.Complete = true
	return result, nil
}

// EditStorage operates relative to a selected store or project. School paths
// are validated in the same way as downloads before a request is constructed.
func (c *Client) EditStorage(ctx context.Context, action string, p StoragePath, newName, confirm string, dry bool) (map[string]any, error) {
	if _, e := storageSegments(p.Path); e != nil {
		return nil, e
	}
	cleanPath := strings.Trim(p.Path, "/")
	name := path.Base(cleanPath)
	if cleanPath == "" || name == "." || name == ".." {
		return nil, domain.E("INVALID_PATH", "请指定存储根目录中的具体对象。")
	}
	if p.GroupID != "" && !validIdentityID(p.GroupID) {
		return nil, domain.E("INVALID_ID", "项目 ID 无效。")
	}
	parent := path.Dir(cleanPath)
	if parent == "." {
		parent = ""
	}
	result := map[string]any{"operation": action, "store": p.Store, "path": p.Path, "dry_run": dry}
	var method string
	var body any
	switch action {
	case "mkdir":
		method = "POST"
		p.Path = parent
		body = map[string]any{"dirs": []string{name}}
	case "rename":
		if newName == "" || strings.ContainsAny(newName, "/\\\x00\r\n") || newName == "." || newName == ".." {
			return nil, domain.E("INVALID_PATH", "新名称须为一个文件名。")
		}
		method = "PUT"
		body = map[string]string{"name": newName}
		result["new_name"] = newName
	case "remove":
		if !dry && confirm != name {
			return nil, domain.E("CONFIRM_REQUIRED", "删除需要 --confirm 原文件名。")
		}
		method = "DELETE"
		p.Path = parent
		body = map[string]any{"items": []string{name}}
	default:
		return nil, domain.E("INVALID_ACTION", "未知存储操作。")
	}
	target, e := c.storageURL(p, "folders", false)
	if e != nil {
		return nil, e
	}
	if dry {
		return result, nil
	}
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	peer := &Client{profile: c.profile, http: &client, vault: c.vault, base: c.base, gateway: c.gateway, cookies: c.cookies}
	var response map[string]any
	if e = peer.jsonURL(ctx, method, target, body, &response); e != nil {
		return nil, e
	}
	if _, e = unwrap(response); e != nil {
		return nil, e
	}
	result["state"] = "complete"
	return result, nil
}
