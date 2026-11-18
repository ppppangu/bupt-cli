package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
)

// Default login follows the controller's challenge state. The CLI does not
// choose CAS endpoints, exchange tickets, or advance between login providers.
func (a *app) login(cmd *cobra.Command, args []string) error {
	local, _ := cmd.Flags().GetBool("local")
	passwordStdin, _ := cmd.Flags().GetBool("password-stdin")
	credentialsStdin, _ := cmd.Flags().GetBool("credentials-stdin")
	if passwordStdin && credentialsStdin {
		return usage("choose --password-stdin or --credentials-stdin")
	}
	wait, _ := cmd.Flags().GetBool("wait")
	noWait, _ := cmd.Flags().GetBool("no-wait")
	if wait && noWait {
		return usage("choose --wait or --no-wait")
	}
	gatewayOnly, _ := cmd.Flags().GetBool("gateway-only")
	campusPassword, _ := cmd.Flags().GetBool("sso-password")
	if campusPassword {
		if local || gatewayOnly {
			return usage("--sso-password requires university SSO; remove --local or --gateway-only")
		}
	} else {
		if local || passwordStdin || credentialsStdin {
			if gatewayOnly {
				return usage("--gateway-only cannot be combined with local-password login")
			}
			return a.loginLocal(cmd, args)
		}
		if cmd.Flags().Changed("username") {
			return usage("--username requires --sso-password, --local or protected credential stdin")
		}
	}
	start, poll, stage := "auth.start", "auth.poll", ""
	if gatewayOnly {
		start, poll, stage = "auth.gateway.start", "auth.gateway.poll", "webvpn"
	}
	var startParams map[string]any
	if campusPassword {
		startParams = map[string]any{"sso_method": "password"}
	}
	if a.dryRun {
		return a.callAndPrint(cmd, start, startParams)
	}
	challenge, e := a.runAuthChallenge(cmd, start, poll, (wait || a.interactive() || passwordStdin || credentialsStdin) && !noWait, stage, startParams)
	if e != nil {
		return e
	}
	return a.emitLogin(challenge)
}

func (a *app) emitLogin(ch domain.AuthChallenge) error {
	if ch.RequiredAction == domain.EnterCampusPassword && !a.json && !a.jsonl {
		fmt.Fprintln(a.options.Err, "运行 bupt auth poll --wait 继续填写。")
		ch.CaptchaImage = ""
	}
	return a.emit(ch)
}

func (a *app) authPollCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "poll", Short: "Advance the current WebVPN → university SSO → platform login", Args: exactArgs(0)}
	cmd.Flags().Bool("wait", false, "Continue until login finishes; enter credentials for a pending password login")
	cmd.Flags().String("username", "", "University username for a pending SSO password login")
	cmd.Flags().Bool("password-stdin", false, "Read the university password from stdin")
	cmd.Flags().Bool("credentials-stdin", false, "Read university username/password and optional captcha_code JSON from stdin")
	cmd.Flags().String("qr-file", "", "Save private QR images using this filename with stage suffixes")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		ps, _ := cmd.Flags().GetBool("password-stdin")
		cs, _ := cmd.Flags().GetBool("credentials-stdin")
		if ps && cs {
			return usage("choose --password-stdin or --credentials-stdin")
		}
		if a.dryRun {
			return a.callAndPrint(cmd, "auth.poll", nil)
		}
		wait, _ := cmd.Flags().GetBool("wait")
		challenge, e := a.runAuthChallenge(cmd, "auth.poll", "auth.poll", wait || ps || cs, "", nil)
		if e != nil {
			return e
		}
		return a.emitLogin(challenge)
	}
	return cmd
}

func (a *app) runAuthChallenge(cmd *cobra.Command, firstMethod, pollMethod string, wait bool, fallbackStage string, firstParams map[string]any) (domain.AuthChallenge, error) {
	ctx, cancel := context.WithTimeout(cmd.Context(), a.timeout)
	defer cancel()
	qrFile, _ := cmd.Flags().GetString("qr-file")
	renderer := newAuthChallengeRenderer(a.options.Err, a.interactive(), qrFile, fallbackStage)
	var challenge domain.AuthChallenge
	if e := a.rpc(ctx, firstMethod, firstParams, &challenge); e != nil {
		return challenge, e
	}
	campusPassword, _ := cmd.Flags().GetBool("sso-password")
	ps, _ := cmd.Flags().GetBool("password-stdin")
	cs, _ := cmd.Flags().GetBool("credentials-stdin")
	expectPassword := campusPassword || cmd.Name() == "poll" && (ps || cs || cmd.Flags().Changed("username"))
	for {
		if expectPassword && challenge.Stage == "university_sso" && challenge.Kind != domain.CampusPasswordKind {
			return challenge, domain.E("SSO_METHOD_MISMATCH", "控制器返回了其他统一认证方式。请确认控制器已更新，再运行 bupt login --sso-password。")
		}
		if e := renderer.Show(challenge); e != nil {
			return challenge, e
		}
		if e := authChallengeError(challenge); e != nil {
			return challenge, e
		}
		if authenticated(challenge.State) || challenge.State == "complete" || challenge.State == "success" {
			return challenge, nil
		}
		if !wait && !(challenge.Kind == domain.CampusPasswordKind && (ps || cs)) {
			return challenge, nil
		}
		if challenge.Kind == domain.CampusPasswordKind {
			if (!campusPassword && cmd.Name() != "poll") || challenge.Stage != "university_sso" || challenge.RequiredAction != domain.EnterCampusPassword || challenge.ID == "" {
				return challenge, domain.E("SSO_METHOD_MISMATCH", "当前操作未请求这张统一认证密码表单。")
			}
			next, err := a.submitCampusPassword(ctx, cmd, challenge)
			if err != nil {
				return challenge, err
			}
			if next.Stage == "university_sso" && next.ID != challenge.ID && next.RequiredAction == domain.EnterCampusPassword {
				if ps || cs {
					return next, domain.E("AUTH_INPUT_REQUIRED", "下一阶段需要人工授权，请运行 bupt auth poll --wait。")
				}
				challenge = next
				continue
			}
			if next.Stage != "complete" || !authenticated(next.State) {
				return challenge, domain.E("SSO_VERIFICATION_PENDING", "平台尚未验证登录结果。请用 bupt auth status 检查。")
			}
			challenge = next
			continue
		}
		delay := time.Second
		if challenge.PollAfterMS > 0 {
			delay = time.Duration(challenge.PollAfterMS) * time.Millisecond
			if delay <= 0 {
				delay = time.Second
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			if ctx.Err() == context.Canceled {
				return challenge, ctx.Err()
			}
			return challenge, domain.E("AUTH_REQUIRED", "login is still pending; use bupt auth poll --wait or start bupt auth login again")
		case <-timer.C:
		}
		var next domain.AuthChallenge
		if e := a.rpc(ctx, pollMethod, nil, &next); e != nil {
			return challenge, e
		}
		challenge = next
	}
}

// Credentials are read only after the controller has issued a CAS password
// challenge. A valid session or a pending WebVPN approval never consumes stdin.
func (a *app) submitCampusPassword(ctx context.Context, cmd *cobra.Command, ch domain.AuthChallenge) (domain.AuthChallenge, error) {
	var next domain.AuthChallenge
	var input struct {
		Username    string `json:"username"`
		Password    string `json:"password"`
		CaptchaCode string `json:"captcha_code"`
	}
	username, _ := cmd.Flags().GetString("username")
	ps, _ := cmd.Flags().GetBool("password-stdin")
	cs, _ := cmd.Flags().GetBool("credentials-stdin")
	switch {
	case cs:
		raw, err := readSecret(a.options.In)
		if err != nil {
			return next, err
		}
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil || decoder.Decode(new(any)) != io.EOF {
			return next, usage("SSO credentials stdin accepts one JSON object containing username, password and optional captcha_code")
		}
		if username != "" && input.Username != "" && username != input.Username {
			return next, usage("--username differs from the username supplied through stdin")
		}
	case ps:
		if username == "" {
			return next, usage("--sso-password with --password-stdin requires --username")
		}
		value, err := readSecret(a.options.In)
		if err != nil {
			return next, err
		}
		input.Password = value
	default:
		if !a.interactive() {
			return next, domain.E("AUTH_INPUT_REQUIRED", "请在终端运行 bupt auth poll --wait，或通过 --credentials-stdin 提供统一认证凭据。")
		}
		if username == "" {
			fmt.Fprint(a.options.Err, "统一身份认证账号（学工号）: ")
			line, err := readInputLine(a.options.In)
			if err != nil || strings.TrimSpace(line) == "" {
				return next, domain.E("AUTH_INPUT_REQUIRED", "请填写统一身份认证账号。")
			}
			username = strings.TrimSpace(line)
		}
		value, err := a.secretInput("统一身份认证密码: ")
		if err != nil {
			return next, err
		}
		input.Password = value
	}
	if input.Username == "" {
		input.Username = username
	}
	if strings.TrimSpace(input.Username) == "" || input.Password == "" {
		return next, usage("university username and password are required")
	}
	if ch.CaptchaImage != "" && input.CaptchaCode == "" {
		if ps || cs || !a.interactive() {
			return next, domain.E("SSO_CAPTCHA_REQUIRED", "学校要求图形验证码。请在交互终端运行 bupt auth poll --wait 查看并填写。")
		}
		answer, err := a.promptCaptcha(ctx, ch.CaptchaImage, "统一身份认证图形验证码")
		if err != nil {
			return next, err
		}
		input.CaptchaCode = answer
	}
	params := map[string]any{"challenge_id": ch.ID, "username": input.Username, "password": input.Password}
	if input.CaptchaCode != "" {
		params["captcha_code"] = input.CaptchaCode
	}
	err := a.rpc(ctx, "auth.sso.submit", params, &next)
	delete(params, "password")
	delete(params, "captcha_code")
	return next, err
}

func authChallengeError(c domain.AuthChallenge) error {
	if c.Kind == domain.CampusPasswordKind && c.State == "expired" {
		return domain.E("SSO_CHALLENGE_EXPIRED", "密码表单已过期，请重新运行 bupt login --sso-password。")
	}
	switch c.State {
	case "confirmation_required":
		return domain.E("AUTH_CONFIRM_REQUIRED", "school login requires confirmation to replace another WebVPN session; run bupt auth confirm --take-over only if you want to replace that login")
	case "refresh_required":
		return domain.E("AUTH_REFRESH_REQUIRED", "school login needs a fresh QR code; run bupt auth login again")
	case "expired", "denied", "rejected", "failed", "cancelled", "canceled":
		return domain.E("AUTH_REQUIRED", "school login "+c.State+"; run bupt auth login again")
	}
	return nil
}

// authChallengeRenderer is presentation-only: a future TUI can consume the same
// controller challenges without implementing any school authentication logic.
type authChallengeRenderer struct {
	out                                           io.Writer
	interactive                                   bool
	qrFile, fallbackStage                         string
	lastStage, lastState, lastAction, lastMessage string
	seenURLs                                      map[string]bool
	fileCounts                                    map[string]int
}

func newAuthChallengeRenderer(out io.Writer, interactive bool, qrFile, fallbackStage string) *authChallengeRenderer {
	return &authChallengeRenderer{out: out, interactive: interactive, qrFile: qrFile, fallbackStage: fallbackStage, seenURLs: map[string]bool{}, fileCounts: map[string]int{}}
}
func authStageLabel(stage string) string {
	switch stage {
	case "webvpn":
		return "WebVPN 校外访问认证"
	case "university_sso":
		return "北邮统一身份认证 / BUPT SSO"
	case "complete":
		return "平台登录完成 / Signed in"
	default:
		return "学校登录 / School sign-in"
	}
}
func (r *authChallengeRenderer) Show(c domain.AuthChallenge) error {
	stage := c.Stage
	if stage == "" {
		stage = r.fallbackStage
	}
	if stage != r.lastStage || r.lastState == "" {
		fmt.Fprintf(r.out, "\n[%s]\n", authStageLabel(stage))
	}
	changed := stage != r.lastStage || c.State != r.lastState || c.RequiredAction != r.lastAction || c.Message != r.lastMessage
	if changed && c.Message != "" {
		fmt.Fprintln(r.out, scalar(c.Message))
	}
	if changed && c.Message == "" {
		switch c.RequiredAction {
		case "scan_and_confirm", "scan_and_confirm_university_qr", "scan_qr":
			fmt.Fprintln(r.out, "请扫描当前阶段的二维码，并在手机上确认。")
		case domain.EnterCampusPassword:
			fmt.Fprintln(r.out, "使用学校统一身份认证账号和密码。")
		case "confirm_on_phone":
			fmt.Fprintln(r.out, "请在手机上完成确认。")
		case "scan_new_university_qr", "restart_login", "refresh_qr":
			fmt.Fprintln(r.out, "请重新运行 bupt auth login 获取新的二维码。")
		}
	}
	r.lastStage, r.lastState, r.lastAction, r.lastMessage = stage, c.State, c.RequiredAction, c.Message
	key := stage + "\x00" + c.URL
	if c.URL == "" || r.seenURLs[key] {
		return nil
	}
	fmt.Fprintf(r.out, "二维码 / QR: %s\n", c.URL)
	qr, e := qrcode.New(c.URL, qrcode.Medium)
	if e != nil {
		return e
	}
	if r.interactive {
		fmt.Fprintln(r.out, qr.ToSmallString(false))
	}
	if r.qrFile != "" {
		slug := "login"
		switch stage {
		case "webvpn":
			slug = "webvpn"
		case "university_sso":
			slug = "sso"
		case "complete":
			slug = "complete"
		}
		r.fileCounts[slug]++
		number := r.fileCounts[slug]
		base := r.qrFile
		if strings.EqualFold(filepath.Ext(base), ".png") {
			base = strings.TrimSuffix(base, filepath.Ext(base))
		}
		filename := base + "." + slug
		if number > 1 {
			filename += fmt.Sprintf(".%d", number)
		}
		filename += ".png"
		png, e := qr.PNG(320)
		if e != nil {
			return e
		}
		f, e := os.OpenFile(filename, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, writeErr := f.Write(png)
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(filename)
			return writeErr
		}
		if closeErr != nil {
			_ = os.Remove(filename)
			return closeErr
		}
		fmt.Fprintf(r.out, "二维码文件 / QR file: %s\n", filename)
	}
	r.seenURLs[key] = true
	return nil
}
