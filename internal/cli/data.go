package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
)

func (a *app) dataCommands() *cobra.Command {
	g := &cobra.Command{Use: "data", Short: "Access persistent platform storage without assuming an instance is running"}
	stores := a.simpleRPC("stores", "List configured persistent storage spaces", "data.config", 0, 0, nil)
	g.AddCommand(stores)
	list := a.simpleRPC("ls [PATH]", "List a persistent directory", "data.list", 0, 1, func(args []string) map[string]any {
		p := map[string]any{"path": ""}
		if len(args) > 0 {
			p["path"] = args[0]
		}
		return p
	})

	storageFlags(list)
	g.AddCommand(list)
	push := &cobra.Command{Use: "push LOCAL REMOTE", Short: "Upload one file with resumable platform storage transport", Args: exactArgs(2)}
	storageFlags(push)
	push.Flags().Bool("overwrite", false, "Permit replacing an existing destination")
	push.Flags().Bool("resume", true, "Resume a matching interrupted upload")
	push.Flags().Int64("size", -1, "Required byte length when LOCAL is - (stdin)")
	push.Flags().String("upload-id", "", "Resume an upload receipt from an earlier command")
	push.RunE = func(cmd *cobra.Command, args []string) error {
		params := map[string]any{"path": args[1]}
		copyChangedFlags(cmd, params)
		resume, _ := cmd.Flags().GetBool("resume")
		params["resume"] = resume
		if id, _ := cmd.Flags().GetString("upload-id"); id != "" {
			params["upload_id"] = id
		}
		return a.upload(cmd, "data.upload", args[0], params)
	}
	pull := &cobra.Command{Use: "pull REMOTE LOCAL", Short: "Download a persistent file; use - for stdout", Args: exactArgs(2)}
	storageFlags(pull)
	pull.Flags().Bool("overwrite", false, "Permit replacing the local destination")
	pull.RunE = func(cmd *cobra.Command, args []string) error {
		p := map[string]any{"path": args[0]}
		copyChangedFlags(cmd, p)
		return a.download(cmd, "data.download", args[1], p)
	}
	g.AddCommand(push, pull)
	for _, action := range []struct {
		use, id, desc string
		argc          int
	}{{"mkdir PATH", "mkdir", "创建目录", 1}, {"rename PATH NEW_NAME", "rename", "修改文件或目录名称", 2}, {"rm PATH", "remove", "删除所选存储对象", 1}} {
		cmd := &cobra.Command{Use: action.use, Short: action.desc, Args: exactArgs(action.argc)}
		storageFlags(cmd)
		if action.id == "remove" {
			cmd.Flags().String("confirm", "", "确认原文件名")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			p := map[string]any{"action": action.id, "path": args[0]}
			copyChangedFlags(cmd, p)
			if len(args) == 2 {
				p["new_name"] = args[1]
			}
			if action.id == "remove" {
				p["confirm"], _ = cmd.Flags().GetString("confirm")
			}
			return a.callAndPrint(cmd, "data.edit", p)
		}
		g.AddCommand(cmd)
	}
	return g
}
func storageFlags(c *cobra.Command) {
	c.Flags().String("store", "", "Storage name or configured URL path")
	c.Flags().String("group-id", "", "Project/group storage ID")
}
func sourceHash(input io.ReadSeeker) (string, error) {
	h := sha256.New()
	if _, e := io.Copy(h, input); e != nil {
		return "", e
	}
	if _, e := input.Seek(0, io.SeekStart); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (a *app) transferContext(cmd *cobra.Command) (context.Context, context.CancelFunc) {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	if cmd.Flags().Changed("timeout") {
		timed, stop := context.WithTimeout(ctx, a.timeout)
		return timed, func() { stop(); cancel() }
	}
	return ctx, cancel
}
func (a *app) upload(cmd *cobra.Command, method, local string, p map[string]any) error {
	if node, _ := cmd.Flags().GetString("node"); node != "" {
		p["node"] = node
	}
	var input io.Reader = a.options.In
	if local == "-" {
		if cmd.Flags().Lookup("size") == nil {
			return usage("stdin upload requires a known --size")
		}
		size, _ := cmd.Flags().GetInt64("size")
		if size < 0 {
			return usage("--size is required when uploading stdin")
		}
		p["size"] = size
		p["resume"] = false
	} else {
		f, e := os.Open(local)
		if e != nil {
			return e
		}
		defer f.Close()
		input = f
		st, e := f.Stat()
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return usage("source must be a regular file; use cp --recursive for directories")
		}
		p["size"] = st.Size()
		p["mode"] = uint32(st.Mode().Perm())
		if method == "data.upload" && !a.dryRun {
			hash, e := sourceHash(f)
			if e != nil {
				return e
			}
			p["source_hash"] = hash
		}
		p["modified"] = st.ModTime().UTC()
	}
	if a.dryRun {
		return a.emit(map[string]any{"operation": method, "destination": p, "dry_run": true})
	}
	if a.idempotencyKey != "" {
		p["idempotency_key"] = a.idempotencyKey
	}
	b, e := a.getBackend()
	if e != nil {
		return e
	}
	ctx, cancel := a.transferContext(cmd)
	defer cancel()
	result, e := b.Stream(ctx, method, p, input, io.Discard)
	if e != nil {
		return e
	}
	return a.emit(result)
}
func (a *app) download(cmd *cobra.Command, method, local string, p map[string]any) error {
	if node, _ := cmd.Flags().GetString("node"); node != "" {
		p["node"] = node
	}
	if a.dryRun {
		return a.emit(map[string]any{"operation": method, "source": p, "destination": local, "dry_run": true})
	}
	if local == "-" {
		if e := a.rawOutput(); e != nil {
			return e
		}
		b, e := a.getBackend()
		if e != nil {
			return e
		}
		ctx, cancel := a.transferContext(cmd)
		defer cancel()
		_, e = b.Stream(ctx, method, p, nil, a.options.Out)
		return e
	}
	if st, e := os.Stat(local); e == nil && st.IsDir() {
		remote, _ := p["path"].(string)
		local = filepath.Join(local, path.Base(remote))
	}
	overwrite, _ := cmd.Flags().GetBool("overwrite")
	if _, e := os.Lstat(local); e == nil && !overwrite {
		return domain.E("DESTINATION_EXISTS", "use --overwrite to replace the local file")
	}
	dir := filepath.Dir(local)
	tmp, e := os.CreateTemp(dir, ".bupt-download-*")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()
	b, e := a.getBackend()
	if e != nil {
		return e
	}
	ctx, cancel := a.transferContext(cmd)
	defer cancel()
	result, e := b.Stream(ctx, method, p, nil, tmp)
	if e != nil {
		return e
	}
	if e = tmp.Sync(); e != nil {
		return e
	}
	if e = tmp.Close(); e != nil {
		return e
	}
	if overwrite {
		e = os.Rename(name, local)
	} else {
		e = os.Link(name, local)
	}
	if e != nil {
		return e
	}
	if result == nil {
		result = map[string]any{}
	}
	result["destination"] = local
	return a.emit(result)
}

type remoteFile struct {
	target, path string
	remote       bool
}

func parseFile(s string) remoteFile {
	left, right, ok := strings.Cut(s, ":")
	if ok && left != "" && !strings.Contains(left, "/") {
		return remoteFile{left, right, true}
	}
	return remoteFile{path: s}
}
func (a *app) cpCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "cp SOURCE DESTINATION", Short: "Copy files using cached SSH routes; remote paths use INSTANCE:PATH", Args: exactArgs(2)}
	cmd.Flags().BoolP("recursive", "r", false, "Copy a directory tree")
	cmd.Flags().Bool("overwrite", false, "Permit replacing an existing destination")
	cmd.Flags().String("node", "", "Node selector for a multi-node instance")
	cmd.Flags().Int64("size", -1, "从标准输入上传时的准确字节数")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		src, dst := parseFile(args[0]), parseFile(args[1])
		if src.remote == dst.remote {
			return usage("cp requires exactly one INSTANCE:PATH operand")
		}
		recursive, _ := cmd.Flags().GetBool("recursive")
		if cmd.Flags().Changed("size") && (src.remote || src.path != "-" || recursive) {
			return usage("--size 只用于从标准输入上传单个文件")
		}
		if recursive {
			if src.path == "-" || dst.path == "-" {
				return usage("递归复制需要文件系统目录")
			}
			return a.transferBatch(func() error {
				if src.remote {
					return a.downloadTree(cmd, src.target, src.path, dst.path)
				}
				return a.uploadTree(cmd, src.path, dst.target, dst.path)
			})
		}
		if src.remote {
			p := map[string]any{"target": src.target, "path": src.path}
			copyChangedFlags(cmd, p)
			return a.download(cmd, "connect.download", dst.path, p)
		}
		p := map[string]any{"target": dst.target, "path": dst.path}
		copyChangedFlags(cmd, p)
		return a.upload(cmd, "connect.upload", src.path, p)
	}
	return cmd
}
func (a *app) transferBatch(run func() error) error {
	if !a.json {
		return run()
	}
	results := []any{}
	previous := a.transferResults
	a.transferResults = &results
	err := run()
	a.transferResults = previous
	if err != nil {
		return err
	}
	return a.emit(results)
}
func fileParams(cmd *cobra.Command, target, path string) map[string]any {
	p := map[string]any{"target": target, "path": path}
	copyChangedFlags(cmd, p)
	return p
}
func (a *app) uploadTree(cmd *cobra.Command, local, target, remote string) error {
	st, e := os.Lstat(local)
	if e != nil {
		return e
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return domain.E("SYMLINK_NOT_SUPPORTED", "recursive copy does not follow symbolic links")
	}
	if !st.IsDir() {
		p := map[string]any{"target": target, "path": remote}
		copyChangedFlags(cmd, p)
		return a.upload(cmd, "connect.upload", local, p)
	}
	b, e := a.getBackend()
	if e != nil {
		return e
	}
	return filepath.WalkDir(local, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, e := filepath.Rel(local, filename)
		if e != nil {
			return e
		}
		dest := path.Join(remote, filepath.ToSlash(rel))
		if entry.Type()&os.ModeSymlink != 0 {
			return domain.E("SYMLINK_NOT_SUPPORTED", "recursive copy does not follow symbolic links: "+filename)
		}
		if entry.IsDir() {
			if a.dryRun {
				return nil
			}
			var ignored any
			p := fileParams(cmd, target, dest)
			p["recursive"] = true
			return b.Call(cmd.Context(), "connect.mkdir", p, &ignored)
		}
		p := map[string]any{"target": target, "path": dest}
		copyChangedFlags(cmd, p)
		return a.upload(cmd, "connect.upload", filename, p)
	})
}
func (a *app) downloadTree(cmd *cobra.Command, target, remote, local string) error {
	if a.dryRun {
		return a.emit(map[string]any{"operation": "connect.download", "target": target, "source": remote, "destination": local, "recursive": true, "dry_run": true})
	}
	b, e := a.getBackend()
	if e != nil {
		return e
	}
	var visit func(string, string) error
	visit = func(src, dst string) error {
		var info map[string]any
		if e := b.Call(cmd.Context(), "connect.stat", fileParams(cmd, target, src), &info); e != nil {
			return e
		}
		if symlink, _ := info["is_symlink"].(bool); symlink {
			return domain.E("SYMLINK_NOT_SUPPORTED", "recursive copy does not follow symbolic links")
		}
		dir, _ := info["is_dir"].(bool)
		if !dir {
			return a.download(cmd, "connect.download", dst, map[string]any{"target": target, "path": src})
		}
		if st, e := os.Lstat(dst); e == nil && st.Mode()&os.ModeSymlink != 0 {
			return domain.E("SYMLINK_NOT_SUPPORTED", "recursive download will not traverse local symbolic links")
		}
		if e := os.MkdirAll(dst, 0700); e != nil {
			return e
		}
		var entries []map[string]any
		if e := b.Call(cmd.Context(), "connect.list", fileParams(cmd, target, src), &entries); e != nil {
			return e
		}
		for _, ent := range entries {
			name, _ := ent["name"].(string)
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
				return domain.E("INVALID_REMOTE_PATH", "directory response contains an invalid filename")
			}
			if e := visit(path.Join(src, name), filepath.Join(dst, name)); e != nil {
				return e
			}
		}
		return nil
	}
	return visit(remote, local)
}
func (a *app) sftpCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "sftp INSTANCE", Short: "Open a small SFTP command shell using an existing route", Args: exactArgs(1)}
	cmd.Flags().Bool("overwrite", false, "Permit replacing existing files")
	cmd.Flags().String("node", "", "Node selector for a multi-node instance")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if e := a.rawOutput(); e != nil {
			return e
		}
		if a.dryRun {
			p := targetParams(args)
			copyChangedFlags(cmd, p)
			p["repair"] = false
			return a.callAndPrint(cmd, "route.get", p)
		}
		b, e := a.getBackend()
		if e != nil {
			return e
		}
		target := args[0]
		cwd := "."
		scanner := bufio.NewScanner(a.options.In)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		if a.interactive() {
			fmt.Fprintln(a.options.Err, "Commands: ls [path], pwd, cd path, get remote [local], put local [remote], mkdir path, rename old new, rm path, quit")
		}
		for {
			if a.interactive() {
				fmt.Fprint(a.options.Err, "bupt-sftp> ")
			}
			if !scanner.Scan() {
				return scanner.Err()
			}
			words, e := splitWords(scanner.Text())
			if e != nil {
				if !a.interactive() {
					return e
				}
				fmt.Fprintln(a.options.Err, e)
				continue
			}
			if len(words) == 0 {
				continue
			}
			resolve := func(p string) string {
				if strings.HasPrefix(p, "/") {
					return path.Clean(p)
				}
				return path.Join(cwd, p)
			}
			var result any
			switch words[0] {
			case "quit", "exit":
				return nil
			case "help":
				fmt.Fprintln(a.options.Out, "ls pwd cd get put mkdir rename rm quit")
				continue
			case "pwd":
				fmt.Fprintln(a.options.Out, cwd)
				continue
			case "ls":
				p := cwd
				if len(words) > 1 {
					p = resolve(words[1])
				}
				e = b.Call(cmd.Context(), "connect.list", fileParams(cmd, target, p), &result)
			case "cd":
				if len(words) != 2 {
					e = usage("cd PATH")
					break
				}
				dest := resolve(words[1])
				var st map[string]any
				e = b.Call(cmd.Context(), "connect.stat", fileParams(cmd, target, dest), &st)
				if e == nil {
					dir, _ := st["is_dir"].(bool)
					if !dir {
						e = domain.E("NOT_DIRECTORY", dest)
					} else {
						cwd = dest
					}
				}
			case "get":
				if len(words) < 2 || len(words) > 3 {
					e = usage("get REMOTE [LOCAL]")
					break
				}
				dest := path.Base(words[1])
				if len(words) == 3 {
					dest = words[2]
				}
				e = a.download(cmd, "connect.download", dest, map[string]any{"target": target, "path": resolve(words[1])})
			case "put":
				if len(words) < 2 || len(words) > 3 {
					e = usage("put LOCAL [REMOTE]")
					break
				}
				dest := filepath.Base(words[1])
				if len(words) == 3 {
					dest = words[2]
				}
				overwrite, _ := cmd.Flags().GetBool("overwrite")
				e = a.upload(cmd, "connect.upload", words[1], map[string]any{"target": target, "path": resolve(dest), "overwrite": overwrite})
			case "mkdir", "rm":
				if len(words) != 2 {
					e = usage("%s PATH", words[0])
					break
				}
				method := "connect.mkdir"
				if words[0] == "rm" {
					method = "connect.remove"
				}
				e = b.Call(cmd.Context(), method, fileParams(cmd, target, resolve(words[1])), &result)
			case "rename":
				if len(words) != 3 {
					e = usage("rename OLD NEW")
					break
				}
				p := fileParams(cmd, target, resolve(words[1]))
				p["destination"] = resolve(words[2])
				e = b.Call(cmd.Context(), "connect.rename", p, &result)
			default:
				e = usage("unknown SFTP command %q", words[0])
			}
			if e != nil {
				if !a.interactive() {
					return e
				}
				fmt.Fprintln(a.options.Err, e)
				continue
			}
			if result != nil {
				if e = a.emit(result); e != nil {
					return e
				}
			}
		}
	}
	return cmd
}
func splitWords(s string) ([]string, error) {
	out := []string{}
	var word strings.Builder
	quote := rune(0)
	escape := false
	active := false
	flush := func() {
		if active {
			out = append(out, word.String())
			word.Reset()
			active = false
		}
	}
	for _, r := range s {
		if escape {
			word.WriteRune(r)
			escape = false
			active = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escape = true
			active = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
			active = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			active = true
			continue
		}
		if r == ' ' || r == '\t' {
			flush()
			continue
		}
		word.WriteRune(r)
		active = true
	}
	if quote != 0 || escape {
		return nil, usage("unterminated quote or escape")
	}
	flush()
	return out, nil
}
