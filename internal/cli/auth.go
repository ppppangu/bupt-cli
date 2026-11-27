package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func (a *app) interactive() bool {
	if a.nonInteractive {
		return false
	}
	if a.options.Interactive != nil {
		return a.options.Interactive()
	}
	return !a.json && !a.jsonl && !a.ids && isTerminal(a.options.In) && isTerminal(a.options.Out) && isTerminal(a.options.Err)
}
func (a *app) secretInput(prompt string) (string, error) {
	if !a.interactive() {
		return "", domain.E("AUTH_INPUT_REQUIRED", "supply credentials through --password-stdin or --credentials-stdin")
	}
	if a.options.ReadSecret != nil {
		return a.options.ReadSecret(prompt)
	}
	f, ok := a.options.In.(*os.File)
	if !ok {
		return "", domain.E("AUTH_INPUT_REQUIRED", "password entry requires a terminal or protected stdin")
	}
	fmt.Fprint(a.options.Err, prompt)
	b, err := term.ReadPassword(int(f.Fd()))
	fmt.Fprintln(a.options.Err)
	if err != nil {
		return "", err
	}
	secret := string(b)
	clear(b)
	return secret, nil
}
func readSecret(r io.Reader) (string, error) {
	b, err := io.ReadAll(io.LimitReader(r, 64*1024+1))
	if err != nil {
		return "", err
	}
	if len(b) > 64*1024 {
		return "", domain.E("CREDENTIAL_TOO_LARGE", "credential input exceeds 64 KiB")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), nil
}
func (a *app) authCommands() *cobra.Command {
	g := &cobra.Command{Use: "auth", Short: "Manage school sessions separately from existing SSH connections"}
	g.AddCommand(a.simpleRPC("status", "Inspect authentication without revealing credentials", "auth.status", 0, 0, nil))
	g.AddCommand(a.simpleRPC("info", "Inspect the platform's configured login methods", "auth.info", 0, 0, nil))
	probe := &cobra.Command{Use: "probe", Short: "Inspect independent copies of the saved session without logging in", Args: exactArgs(0)}
	probe.Flags().String("proxy", "", "Optional loopback HTTP/SOCKS proxy for an IP-change test; the school may invalidate the session")
	probe.RunE = func(cmd *cobra.Command, _ []string) error {
		proxy, _ := cmd.Flags().GetString("proxy")
		if a.dryRun {
			return a.emit(map[string]any{"operation": "auth.probe", "proxy": proxy, "dry_run": true})
		}
		return a.callAndPrint(cmd, "auth.probe", map[string]any{"proxy": proxy})
	}
	g.AddCommand(probe)
	g.AddCommand(a.simpleRPC("refresh", "Refresh sessions using protected stored credentials", "auth.refresh", 0, 0, nil))

	device := &cobra.Command{Use: "device", Short: "Start WebVPN device approval only (does not complete university SSO)", Args: exactArgs(0)}
	device.Flags().Bool("wait", false, "Wait for device approval")
	device.Flags().String("qr-file", "", "Save the QR image to a private file")
	device.RunE = func(cmd *cobra.Command, _ []string) error {
		wait, _ := cmd.Flags().GetBool("wait")
		if a.dryRun {
			return a.callAndPrint(cmd, "auth.gateway.start", nil)
		}
		challenge, err := a.runAuthChallenge(cmd, "auth.gateway.start", "auth.gateway.poll", wait, "webvpn", nil)
		if err != nil {
			return err
		}
		return a.emit(challenge)
	}
	g.AddCommand(device, a.authPollCommand())
	confirm := &cobra.Command{Use: "confirm", Short: "Explicitly confirm a school-requested takeover of another WebVPN session", Args: exactArgs(0)}
	confirm.Flags().Bool("take-over", false, "Confirm replacing the other WebVPN login when the school requests it")
	confirm.RunE = func(cmd *cobra.Command, _ []string) error {
		takeOver, _ := cmd.Flags().GetBool("take-over")
		if !takeOver {
			return usage("auth confirm requires --take-over to explicitly replace another WebVPN login")
		}
		return a.callAndPrint(cmd, "auth.gateway.confirm", map[string]any{"take_over": true})
	}
	g.AddCommand(confirm)
	g.AddCommand(a.captchaCommand())
	g.AddCommand(a.authAccessCommand())
	setup := &cobra.Command{Use: "setup", Short: "Explicitly initialize or change a platform password in an authenticated session", Args: exactArgs(0)}
	setup.Flags().Bool("password-stdin", false, "Read the new password from stdin")
	setup.Flags().Bool("credentials-stdin", false, "Read password/old_password JSON from stdin")
	setup.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.dryRun {
			return a.callAndPrint(cmd, "auth.setup", map[string]any{})
		}
		ps, _ := cmd.Flags().GetBool("password-stdin")
		cs, _ := cmd.Flags().GetBool("credentials-stdin")
		if ps && cs {
			return usage("choose --password-stdin or --credentials-stdin")
		}
		p := map[string]any{}
		if cs {
			raw, e := readSecret(a.options.In)
			if e != nil {
				return e
			}
			var data struct {
				Password    string
				OldPassword string `json:"old_password"`
			}
			if e = json.Unmarshal([]byte(raw), &data); e != nil {
				return usage("credentials stdin must be JSON containing password and optional old_password")
			}
			p["password"] = data.Password
			if data.OldPassword != "" {
				p["old_password"] = data.OldPassword
			}
		} else if ps {
			v, e := readSecret(a.options.In)
			if e != nil {
				return e
			}
			p["password"] = v
		} else {
			v, e := a.secretInput("New platform password: ")
			if e != nil {
				return e
			}
			confirm, e := a.secretInput("Repeat new password: ")
			if e != nil {
				return e
			}
			if v != confirm {
				return domain.E("PASSWORD_MISMATCH", "the entered passwords do not match")
			}
			p["password"] = v
		}
		if s, _ := p["password"].(string); s == "" || s != strings.TrimSpace(s) {
			return usage("password must not be empty or have surrounding whitespace")
		}
		return a.callAndPrint(cmd, "auth.setup", p)
	}
	g.AddCommand(setup)
	return g
}
func authenticated(state string) bool {
	return state == "authenticated" || state == "ready" || state == "valid" || state == "not_required" || state == "disabled"
}

func (a *app) authAccessCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "access", Short: "Save the instance console password for initial SSH key bootstrap", Long: "Store the instance terminal password in the controller's protected vault for SSH key bootstrap. Use bupt login for university SSO.", Args: exactArgs(0)}
	cmd.Flags().Bool("password-stdin", false, "Read the instance console password from protected stdin")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.dryRun {
			return a.callAndPrint(cmd, "auth.access", nil)
		}
		stdin, _ := cmd.Flags().GetBool("password-stdin")
		var password string
		var e error
		if stdin {
			password, e = readSecret(a.options.In)
		} else {
			password, e = a.secretInput("Instance console password (SSH key bootstrap): ")
		}
		if e != nil {
			return e
		}
		if password == "" {
			return usage("instance console password must not be empty")
		}
		params := map[string]any{"password": password}
		defer delete(params, "password")
		return a.callAndPrint(cmd, "auth.access", params)
	}
	return cmd
}
func (a *app) loginLocal(cmd *cobra.Command, _ []string) error {
	ps, _ := cmd.Flags().GetBool("password-stdin")
	cs, _ := cmd.Flags().GetBool("credentials-stdin")
	if ps && cs {
		return usage("choose --password-stdin or --credentials-stdin")
	}
	device, _ := cmd.Flags().GetBool("device")
	gatewayOnly, _ := cmd.Flags().GetBool("gateway-only")
	wait, _ := cmd.Flags().GetBool("wait")
	noWait, _ := cmd.Flags().GetBool("no-wait")
	if wait && noWait {
		return usage("choose --wait or --no-wait")
	}
	if a.dryRun {
		return a.callAndPrint(cmd, "auth.login", map[string]any{"device": device, "gateway_only": gatewayOnly})
	}
	profile, e := a.cfg.SelectedProfile()
	if e != nil {
		return e
	}
	var status domain.AuthStatus
	if err := a.rpc(cmd.Context(), "auth.status", nil, &status); err != nil {
		return err
	}
	if profile.GatewayURL != "" && (device || !authenticated(status.Gateway)) {
		if !device && !a.interactive() {
			return domain.E("AUTH_REQUIRED", "school approval is required; run bupt login --device --wait")
		}
		follow := (wait || a.interactive()) && !noWait
		challenge, err := a.runAuthChallenge(cmd, "auth.gateway.start", "auth.gateway.poll", follow, "webvpn", nil)
		if err != nil {
			return err
		}
		if !follow {
			return a.emit(challenge)
		}
	}
	if gatewayOnly {
		return a.callAndPrint(cmd, "auth.status", nil)
	}
	username, _ := cmd.Flags().GetString("username")
	if username == "" {
		username = profile.Username
	}
	password := ""
	additional := map[string]any{}
	if cs {
		raw, err := readSecret(a.options.In)
		if err != nil {
			return err
		}
		var data struct {
			Username, Password, Token string
			CaptchaUUID               string `json:"captcha_uuid"`
			CaptchaCode               string `json:"captcha_code"`
		}
		if err = json.Unmarshal([]byte(raw), &data); err != nil {
			return usage("credentials stdin must contain username/password JSON")
		}
		if data.Username != "" {
			username = data.Username
		}
		password = data.Password
		if data.Token != "" {
			additional["token"] = data.Token
		}
		if data.CaptchaUUID != "" {
			additional["captcha_uuid"] = data.CaptchaUUID
		}
		if data.CaptchaCode != "" {
			additional["captcha_code"] = data.CaptchaCode
		}
	} else if ps {
		password, e = readSecret(a.options.In)
		if e != nil {
			return e
		}
	} else if a.interactive() {
		if username == "" {
			fmt.Fprint(a.options.Err, "Platform username: ")
			v, err := readInputLine(a.options.In)
			if err != nil {
				return err
			}
			username = strings.TrimSpace(v)
		}
		password, e = a.secretInput("Platform password: ")
		if e != nil {
			return e
		}
	} else {
		return a.callAndPrint(cmd, "auth.refresh", nil)
	}
	if username == "" || password == "" {
		return usage("username and platform password are required")
	}
	additional["username"] = username
	additional["password"] = password
	return a.loginWithCaptcha(cmd, additional)
}

type captchaChallenge struct {
	UUID  string `json:"uuid"`
	Image string `json:"image"`
}

// readInputLine deliberately avoids read-ahead: password entry accesses the
// terminal directly, and the next CAPTCHA answer must remain on stdin.
func readInputLine(r io.Reader) (string, error) {
	buf := make([]byte, 0, 64)
	var next [1]byte
	for len(buf) < 4096 {
		_, e := io.ReadFull(r, next[:])
		if e != nil {
			if errors.Is(e, io.EOF) && len(buf) > 0 {
				return strings.TrimSuffix(string(buf), "\r"), nil
			}
			return "", e
		}
		if next[0] == '\n' {
			return strings.TrimSuffix(string(buf), "\r"), nil
		}
		buf = append(buf, next[0])
	}
	return "", usage("input line exceeds 4096 bytes")
}

func (a *app) loginWithCaptcha(cmd *cobra.Command, params map[string]any) error {
	defer func() { delete(params, "password"); delete(params, "token"); delete(params, "captcha_code") }()
	for attempts := 0; ; {
		var result any
		err := a.rpc(cmd.Context(), "auth.login", params, &result)
		if err == nil {
			return a.emit(result)
		}
		var de *domain.Error
		if !a.interactive() || !errors.As(err, &de) || de.Code != "CAPTCHA_REQUIRED" {
			return err
		}
		if attempts >= 3 {
			return domain.E("CAPTCHA_REQUIRED", "CAPTCHA verification did not complete after three attempts; run bupt login again")
		}
		var challenge captchaChallenge
		if err = a.rpc(cmd.Context(), "auth.captcha", nil, &challenge); err != nil {
			return err
		}
		if challenge.UUID == "" {
			return domain.E("CAPTCHA_FORMAT", "platform CAPTCHA response omitted its challenge")
		}
		code, err := a.promptCaptcha(cmd.Context(), challenge.Image, fmt.Sprintf("Platform CAPTCHA required (%d/3). Image:", attempts+1))
		if err != nil {
			return err
		}
		params["captcha_uuid"] = challenge.UUID
		params["captcha_code"] = code
		attempts++
	}
}

func (a *app) promptCaptcha(ctx context.Context, encoded, label string) (string, error) {
	data, extension, err := decodeCaptchaImage(encoded)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(a.cfg.Paths.StateDir, "captchas")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0077 != 0 {
		return "", domain.E("INSECURE_CAPTCHA_DIRECTORY", "CAPTCHA directory must be a real directory with mode 0700")
	}
	file, err := os.CreateTemp(dir, "challenge-*"+extension)
	if err != nil {
		return "", err
	}
	filename := file.Name()
	if err = file.Chmod(0600); err == nil {
		_, err = file.Write(data)
	}
	closeErr := file.Close()
	if err != nil {
		_ = os.Remove(filename)
		return "", err
	}
	if closeErr != nil {
		_ = os.Remove(filename)
		return "", closeErr
	}
	fmt.Fprintf(a.options.Err, "%s  %s\n", label, filename)
	inline, inlineErr := a.inlineCaptchaPNG(data)
	if inlineErr != nil {
		fmt.Fprintln(a.options.Err, "Could not display the CAPTCHA in this terminal; using the saved image.")
	}
	if !inline {
		if err = a.openImage(ctx, filename); err != nil {
			fmt.Fprintln(a.options.Err, "Could not open an image viewer; open the image file above manually.")
		}
	}
	fmt.Fprint(a.options.Err, "CAPTCHA code: ")
	code, err := readInputLine(a.options.In)
	if err != nil {
		return "", domain.E("AUTH_INPUT_REQUIRED", "no CAPTCHA answer was provided")
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return "", domain.E("AUTH_INPUT_REQUIRED", "CAPTCHA answer must not be empty")
	}
	return code, nil
}

func (a *app) openImage(ctx context.Context, filename string) error {
	if a.options.OpenImage != nil {
		return a.options.OpenImage(ctx, filename)
	}
	command := ""
	switch runtime.GOOS {
	case "darwin":
		command = "open"
	case "linux":
		command = "xdg-open"
	default:
		return domain.E("IMAGE_VIEWER_UNAVAILABLE", "open the saved CAPTCHA image manually")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, command, filename)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func decodeCaptchaImage(encoded string) ([]byte, string, error) {
	imageType := ""
	if strings.HasPrefix(encoded, "data:") {
		prefix, data, ok := strings.Cut(encoded, ",")
		if !ok || !strings.HasSuffix(prefix, ";base64") {
			return nil, "", domain.E("CAPTCHA_FORMAT", "platform CAPTCHA image is not a base64 data URL")
		}
		encoded = data
		imageType = strings.TrimSuffix(strings.TrimPrefix(prefix, "data:"), ";base64")
	}
	if len(encoded) > 8<<20 {
		return nil, "", domain.E("CAPTCHA_FORMAT", "platform CAPTCHA image exceeds its size limit")
	}
	data, e := base64.StdEncoding.DecodeString(encoded)
	if e != nil || len(data) == 0 {
		return nil, "", domain.E("CAPTCHA_FORMAT", "platform CAPTCHA image is not valid base64")
	}
	if imageType == "" {
		imageType = http.DetectContentType(data)
	}
	extension := ".png"
	switch imageType {
	case "image/jpeg":
		extension = ".jpg"
	case "image/gif":
		extension = ".gif"
	case "image/webp":
		extension = ".webp"
	case "image/svg+xml":
		extension = ".svg"
	}
	return data, extension, nil
}

func (a *app) captchaCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "captcha", Short: "Fetch a platform CAPTCHA for manual completion", Args: exactArgs(0)}
	cmd.Flags().String("file", "", "Save the CAPTCHA image to a new private file")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		filename, _ := cmd.Flags().GetString("file")
		if filename == "" && !a.json && !a.jsonl {
			return usage("use --file FILE to save the CAPTCHA image, or --json to read its encoded data")
		}
		if a.dryRun {
			return a.emit(map[string]any{"operation": "auth.captcha", "output": filename, "dry_run": true})
		}
		var challenge captchaChallenge
		if e := a.rpc(cmd.Context(), "auth.captcha", nil, &challenge); e != nil {
			return e
		}
		if filename == "" {
			return a.emit(challenge)
		}
		data, _, e := decodeCaptchaImage(challenge.Image)
		if e != nil {
			return e
		}
		f, e := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, writeErr := f.Write(data)
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(filename)
			return writeErr
		}
		if closeErr != nil {
			_ = os.Remove(filename)
			return closeErr
		}
		return a.emit(map[string]any{"uuid": challenge.UUID, "image_file": filename})
	}
	return cmd
}

func (a *app) logoutCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "logout", Short: "退出当前配置在控制器上的学校登录会话", Args: exactArgs(0)}
	cmd.Flags().Bool("force", false, "仍有托管运行需要学校会话时，强制退出")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		params := map[string]any{}
		copyChangedFlags(cmd, params)
		if a.ids {
			return a.rpc(cmd.Context(), "auth.logout", params, nil)
		}
		return a.callAndPrint(cmd, "auth.logout", params)
	}
	return cmd
}

func (a *app) loginCommand() *cobra.Command {
	login := &cobra.Command{Use: "login", Short: "登录学校账号，WebVPN 扫码后可选择统一认证扫码或账号密码", Args: exactArgs(0)}
	login.Flags().Bool("sso-password", false, "第二步使用统一身份认证账号密码，默认扫码")
	login.Flags().Bool("local", false, "Explicitly use the platform's local-password login")
	login.Flags().String("username", "", "登录账号，--sso-password 使用统一认证学工号")
	login.Flags().Bool("password-stdin", false, "Read the selected login method's password from stdin")
	login.Flags().Bool("credentials-stdin", false, "Read username/password JSON and optional token/captcha fields from stdin")
	login.Flags().Bool("device", false, "Use device approval for WebVPN; combine with --wait without a terminal")
	login.Flags().Bool("gateway-only", false, "Authenticate only WebVPN")
	login.Flags().Bool("wait", false, "Wait for device approval, including without a TTY")
	login.Flags().Bool("no-wait", false, "Return the device challenge without waiting")
	login.Flags().String("qr-file", "", "Save private QR images using this filename with stage suffixes")
	addAgentLoginFlags(login)
	login.RunE = a.login
	return login
}
