package tui

import (
	"context"
	"errors"
	"fmt"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/tuiprefs"
	"github.com/rivo/uniseg"
)

const prefsTitle = "顶栏显示"

type accountSubscriber interface {
	WatchAccount(context.Context, bool, func(domain.AccountStatus) error) error
}

// All mutations of this view run on the UI loop. The subscriptions only enqueue
// notifications, so an incoming change never rebuilds a draft or moves focus.
type accountView struct {
	snapshot                  domain.AccountStatus
	prefs                     tuiprefs.Settings
	stop                      context.CancelFunc
	generation                uint64
	loginRevision             uint64
	verifying                 bool
	prefProblem, watchProblem string
}

func (w *workspace) startAccountView() {
	w.bar.prefs = tuiprefs.Defaults()
	w.reloadPreferences()
	dir := w.Config.Paths.ConfigDir
	go tuiprefs.Watch(w.ctx, dir, func() { w.post(w.ctx, w.reloadPreferences) })
}
func (w *workspace) reloadPreferences() {
	prefs, err := tuiprefs.Read(w.Config.Paths.ConfigDir)
	if err != nil {
		w.bar.prefProblem = err.Error()
		if w.bar.stop != nil {
			return
		}
		// An unreadable initial preference must not enable school polling.
		prefs = tuiprefs.Defaults()
		prefs.AutoVerify = false
	} else {
		w.bar.prefProblem = ""
	}
	w.applyPreferences(prefs)
}
func (w *workspace) applyPreferences(prefs tuiprefs.Settings) {
	restart := w.bar.stop == nil || !w.Cached && prefs.AutoVerify != w.bar.prefs.AutoVerify
	w.bar.prefs = prefs
	if restart {
		w.startAccountWatch()
	}
}
func (w *workspace) startAccountWatch() {
	if w.bar.stop != nil {
		w.bar.stop()
	}
	ctx, cancel := context.WithCancel(w.ctx)
	w.bar.stop = cancel
	w.bar.generation++
	w.bar.snapshot = domain.AccountStatus{}
	w.bar.watchProblem = ""
	gen := w.bar.generation
	b, ok := w.Backend.(accountSubscriber)
	if !ok {
		w.bar.watchProblem = "客户端未提供状态订阅"
		return
	}
	profile := w.Config.Selected
	live := !w.Cached && w.bar.prefs.AutoVerify
	go func() {
		delay := 250 * time.Millisecond
		for ctx.Err() == nil {
			delivered := false
			err := b.WatchAccount(ctx, live, func(s domain.AccountStatus) error {
				if s.Profile != profile || s.Epoch == "" {
					return fmt.Errorf("控制器返回了不匹配的账号快照")
				}
				delivered = true
				w.post(ctx, func() {
					if gen != w.bar.generation {
						return
					}
					w.acceptAccount(s)
				})
				return ctx.Err()
			})
			if ctx.Err() != nil {
				return
			}
			message := "控制器连接已断开"
			if err != nil {
				message = whenError(err)
			}
			w.post(ctx, func() {
				if gen == w.bar.generation {
					w.bar.snapshot = domain.AccountStatus{}
					w.bar.watchProblem = message
				}
			})
			if delivered {
				delay = 250 * time.Millisecond
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = min(10*time.Second, delay*2)
		}
	}()
}
func (w *workspace) acceptAccount(s domain.AccountStatus) {
	s = s.Effective(time.Now())
	prev := w.bar.snapshot
	if prev.Epoch == s.Epoch && s.Revision <= prev.Revision {
		return
	}
	w.bar.snapshot = s
	w.bar.watchProblem = ""
	// Retry only a currently visible read which failed for lack of a session.
	// A successful login in another terminal must not trigger model calls.
	if s.Platform == "authenticated" && prev.Platform != "authenticated" && !w.Cached {
		w.bar.loginRevision++
		w.resumeProtectedRead(w.current())
	}
}

func (w *workspace) resumeProtectedRead(p *page) bool {
	if !p.blockedByLogin || p.readLoginRevision == w.bar.loginRevision || p != w.current() || p.loading || p.submitting || w.bar.snapshot.Effective(time.Now()).Platform != "authenticated" {
		return false
	}
	w.fetchPage(p, true)
	return true
}

func loginRequired(err error) bool {
	var de *domain.Error
	return errors.As(err, &de) && (de.Code == "AUTH_REQUIRED" || de.Code == "AUTH_EXPIRED" || de.Code == "GATEWAY_AUTH_EXPIRED")
}

func firstLoginError(errs ...error) error {
	for _, err := range errs {
		if loginRequired(err) {
			return err
		}
	}
	return nil
}

// The controller publishes the result on the regular ordered watch.
func (w *workspace) verifyAccount() {
	if w.Cached || w.bar.verifying {
		return
	}
	p := w.current()
	w.bar.verifying = true
	w.notice = "正在核验学校会话…"
	go func() {
		ctx, cancel := context.WithTimeout(w.ctx, w.Timeout)
		defer cancel()
		var status domain.AccountStatus
		err := w.Backend.Call(ctx, "account.refresh", nil, &status)
		w.post(w.ctx, func() {
			w.bar.verifying = false
			if w.current() != p {
				return
			}
			if err != nil {
				w.notice = "核验失败 · " + whenError(err)
			} else if s := status.Effective(time.Now()); s.VPN == "unknown" || s.Platform == "unknown" {
				w.notice = "暂时无法确认学校会话"
			} else {
				w.notice = "账号状态已更新"
			}
		})
	}()
}

func accountLabel(state string) string {
	switch state {
	case "authenticated":
		return "已登录"
	case "expired", "unauthenticated":
		return "未登录"
	case "blocked_by_gateway":
		return "待网络认证"
	case "not_configured", "not_required":
		return "未配置"
	default:
		return "待确认"
	}
}

func vpnLabel(state string) string {
	if state == "not_configured" || state == "not_required" {
		return "直连"
	}
	return accountLabel(state)
}

func accountLine(s domain.AccountStatus, prefs tuiprefs.Settings, width int) string {
	vpn, platform := vpnLabel(s.VPN), accountLabel(s.Platform)
	line := "WebVPN " + vpn + "  算力平台 " + platform
	if uniseg.StringWidth(line) > width {
		if s.Platform == "blocked_by_gateway" {
			platform = "待网络"
		}
		line = "VPN " + vpn + "  平台 " + platform
	}
	add := func(field string) {
		field = clean(field)
		if uniseg.StringWidth(line)+3+uniseg.StringWidth(field) <= width {
			line += " · " + field
		}
	}
	if s.Platform != "authenticated" {
		s.Name, s.CampusID, s.Balance, s.Running = "", "", nil, nil
	}
	if prefs.ShowBalance {
		add("余额 " + ntext(s.Balance) + " 点")
	}
	if prefs.ShowRunning {
		running := "—"
		if s.Running != nil {
			running = fmt.Sprint(*s.Running)
		}
		add("运行 " + running + " 台")
	}
	if prefs.ShowName {
		name := s.Name
		if name == "" {
			name = "—"
		}
		add("姓名 " + name)
	}
	if prefs.ShowCampusID {
		sid := s.CampusID
		if sid == "" {
			sid = "—"
		}
		add("学工号 " + sid)
	}
	return line
}

func (w *workspace) preferencesSaved(prefs tuiprefs.Settings, err error) {
	if err != nil {
		w.bar.prefProblem = err.Error()
		w.notice = "保存顶栏设置失败"
		return
	}
	w.bar.prefProblem = ""
	w.applyPreferences(prefs)
	w.notice = "顶栏设置已保存"
}
func (w *workspace) topbarSettings() {
	dir := w.Config.Paths.ConfigDir
	w.local(prefsTitle, "", func() content {
		v := content{facts: []string{"WebVPN 与算力平台状态始终显示。"}}
		if w.bar.prefProblem != "" {
			v.facts = append(v.facts, w.bar.prefProblem)
		}
		type toggle struct {
			key, title string
			field      func(*tuiprefs.Settings) *bool
		}
		fields := []toggle{
			{"balance", "剩余积分", func(p *tuiprefs.Settings) *bool { return &p.ShowBalance }},
			{"running", "运行实例数", func(p *tuiprefs.Settings) *bool { return &p.ShowRunning }},
			{"name", "姓名", func(p *tuiprefs.Settings) *bool { return &p.ShowName }},
			{"campus_id", "学工号", func(p *tuiprefs.Settings) *bool { return &p.ShowCampusID }},
			{"auto_verify", "自动核验学校会话", func(p *tuiprefs.Settings) *bool { return &p.AutoVerify }},
		}
		for _, f := range fields {
			enabled := *f.field(&w.bar.prefs)
			state := "关闭"
			if enabled {
				state = "开启"
			}
			v.rows = append(v.rows, link(f.key, f.title, state, "", func() {
				w.preferencesSaved(tuiprefs.Update(dir, func(p *tuiprefs.Settings) {
					field := f.field(p)
					*field = !*field
				}))
			}))
		}
		if !w.Cached {
			v.rows = append(v.rows, link("verify", "立即核验学校会话", "向学校查询当前登录状态", "", w.verifyAccount))
		}
		v.rows = append(v.rows, link("defaults", "恢复默认设置", "显示两项认证、余额和运行数，开启自动核验", "", func() { w.preferencesSaved(tuiprefs.Reset(dir)) }))
		return v
	})
}
