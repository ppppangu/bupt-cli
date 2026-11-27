package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
)

func addAgentLoginFlags(cmd *cobra.Command) {
	cmd.Flags().Bool("agent", false, "立即返回当前扫码步骤和私有 PNG 路径，默认输出 JSON")
	cmd.Flags().Bool("open", false, "用本机图片查看器打开当前二维码")
	for _, flag := range []string{"wait", "local", "gateway-only", "sso-password", "username", "credentials-stdin", "password-stdin"} {
		if cmd.Flags().Lookup(flag) != nil {
			cmd.MarkFlagsMutuallyExclusive("agent", flag)
		}
	}
}

func agentLoginRequested(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	value, _ := cmd.Flags().GetBool("agent")
	return value
}

// A CLI-local projection. Raw challenge URLs and password forms belong to
// the controller protocol, not to the agent transcript.
type agentLoginStep struct {
	Stage          string    `json:"stage"`
	State          string    `json:"state"`
	RequiredAction string    `json:"required_action"`
	PollAfterMS    int       `json:"poll_after_ms"`
	ExpiresAt      time.Time `json:"expires_at,omitzero"`
	QRFile         string    `json:"qr_file,omitempty"`
	Hint           string    `json:"hint,omitempty"`
}

func (a *app) agentLogin(cmd *cobra.Command, method string) error {
	if a.dryRun {
		return a.callAndPrint(cmd, method, nil)
	}
	a.cleanLoginQRs()
	var ch domain.AuthChallenge
	if err := a.rpc(cmd.Context(), method, nil, &ch); err != nil {
		return err
	}
	if err := authChallengeError(ch); err != nil {
		var problem *domain.Error
		if errors.As(err, &problem) && problem.Code != "AUTH_CONFIRM_REQUIRED" {
			problem.Hint = "bupt login --agent"
		}
		return err
	}
	if ch.Kind == domain.CampusPasswordKind {
		return &domain.Error{Code: "AUTH_INPUT_REQUIRED", Message: "当前登录正在等待统一认证密码", Hint: "在交互终端运行 bupt auth poll --wait，或用 bupt login --agent 发起扫码登录"}
	}
	if ch.Stage == "" || ch.State == "" || (ch.Stage == "complete") != (ch.State == "authenticated") {
		return domain.E("PROTOCOL_ERROR", "控制器返回了不完整的登录状态")
	}
	step := agentLoginStep{Stage: ch.Stage, State: ch.State, RequiredAction: ch.RequiredAction, PollAfterMS: ch.PollAfterMS, ExpiresAt: ch.ExpiresAt}
	if ch.Stage == "complete" {
		step.PollAfterMS = 0
		return a.emit(step)
	}
	step.Hint = "bupt auth poll --agent"
	if step.PollAfterMS <= 0 {
		step.PollAfterMS = 1000
	}
	if !ch.ExpiresAt.IsZero() && !time.Now().Before(ch.ExpiresAt) {
		return &domain.Error{Code: "AUTH_REQUIRED", Message: "二维码已过期", Hint: "bupt login --agent"}
	}
	if ch.URL == "" {
		return a.emit(step)
	}
	qr, err := qrcode.New(ch.URL, qrcode.Medium)
	if err != nil {
		return err
	}
	open, _ := cmd.Flags().GetBool("open")
	base, _ := cmd.Flags().GetString("qr-file")
	if base != "" {
		renderer := newAuthChallengeRenderer(a.options.Err, false, base, "")
		step.QRFile, err = renderer.writeQR(ch.Stage, qr)
		if err == nil {
			step.QRFile, err = filepath.Abs(step.QRFile)
		}
	} else if a.format != "table" || open {
		step.QRFile, err = a.cacheLoginQR(ch, qr)
	}
	if err != nil {
		return &domain.Error{Code: "QR_FILE_UNAVAILABLE", Message: err.Error(), Hint: "bupt auth poll --agent --output table"}
	}
	if step.QRFile != "" && !open {
		step.Hint = "向用户展示新的 qr_file，按 poll_after_ms 轮询 bupt auth poll --agent"
	}
	if open && a.openImage(cmd.Context(), step.QRFile) != nil {
		step.Hint = "请手动打开 qr_file，扫码后运行 bupt auth poll --agent"
	}
	if a.format == "table" {
		fmt.Fprintln(a.options.Out, authStageLabel(ch.Stage))
		fmt.Fprintln(a.options.Out, qr.ToSmallString(false))
	}
	return a.emit(step)
}

var managedQRName = regexp.MustCompile(`^qr-[0-9a-f]{64}\.png$`)

func (a *app) loginQRDir() string { return filepath.Join(a.cfg.Paths.StateDir, "login-qr") }

// Only automatically named regular files in the private QR directory are
// eligible. Their modification time records the expiry of the challenge.
func (a *app) cleanLoginQRs() {
	if a.dryRun {
		return
	}
	dir := a.loginQRDir()
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	now := time.Now()
	for _, entry := range entries {
		if !managedQRName.MatchString(entry.Name()) {
			continue
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() && !info.ModTime().After(now) {
			_ = os.Remove(filepath.Join(dir, entry.Name()))
		}
	}
}

func (a *app) cacheLoginQR(ch domain.AuthChallenge, qr *qrcode.QRCode) (string, error) {
	dir, err := filepath.Abs(a.loginQRDir())
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return "", domain.E("INSECURE_PERMISSIONS", "二维码目录需要 0700 权限")
	}
	digest := sha256.Sum256([]byte(a.cfg.Selected + "\x00" + ch.Stage + "\x00" + ch.URL))
	path := filepath.Join(dir, fmt.Sprintf("qr-%x.png", digest))
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return "", domain.E("UNSAFE_PATH", "二维码路径需要私有普通文件")
		}
		return path, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	// Concurrent polls must only see complete images.
	f, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	writeErr := qr.Write(320, f)
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	expires := ch.ExpiresAt
	if expires.IsZero() {
		expires = time.Now().Add(10 * time.Minute)
	}
	if err := os.Chtimes(f.Name(), time.Now(), expires); err != nil {
		return "", err
	}
	return path, os.Rename(f.Name(), path)
}
