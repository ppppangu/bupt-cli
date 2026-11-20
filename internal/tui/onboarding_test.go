package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/install"
	"github.com/gdamore/tcell/v2"
)

func TestIdleWindowReflectsLocalEnrollmentWithoutAnotherAccountFrame(t *testing.T) {
	w, backend := testWindow(t, false, filepath.Join(t.TempDir(), "config"))
	w.Config.Paths.ConfigDir = filepath.Dir(w.Config.Paths.ConfigFile)
	paths := w.Config.Paths
	if err := os.MkdirAll(paths.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	record := map[string]any{
		"manager": "process", "name": "bupt-agent", "pid": os.Getpid(), "config": paths.ConfigFile,
		"executable": binary, "setup_pending": true,
		"arguments": []string{"agent", "run", "--no-project", "--config", paths.ConfigFile, "--data-dir", paths.DataDir,
			"--state-dir", paths.StateDir, "--socket", w.Config.Profiles[w.Config.Selected].ControllerSocket,
			"--profile", w.Config.Selected, "--master-key-file", filepath.Join(paths.DataDir, "master.key")},
	}
	publish := func() {
		t.Helper()
		b, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		tmp := filepath.Join(paths.StateDir, "service.tmp")
		if err := os.WriteFile(tmp, b, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, filepath.Join(paths.StateDir, "service.json")); err != nil {
			t.Fatal(err)
		}
	}
	publish()
	w.StartWorkspace = func(context.Context, string) error {
		t.Error("an observer attempted to start a controller")
		return nil
	}
	screen := &statusScreen{tcell.NewSimulationScreen("UTF-8"), make(chan string, 32), 4}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); _ = runScreen(ctx, w.Options, screen) }()
	defer func() { cancel(); <-done }()
	frame := domain.AccountStatus{Epoch: "initial", Revision: 1, Profile: w.Config.Selected, VPN: "expired", Platform: "blocked_by_gateway"}
	backend.stream(t, true).send(t, frame)
	waitForLine := func(want string) {
		t.Helper()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		for {
			select {
			case line := <-screen.line:
				if strings.Contains(line, want) {
					return
				}
			case <-timer.C:
				t.Fatalf("idle terminal did not show %q", want)
			}
		}
	}
	waitForLine("完成个人工作区初始化")
	record["setup_pending"] = false
	publish()
	waitForLine("登录学校账号")
	for _, c := range backend.observed() {
		if c.method != "status.get" || c.params["cached"] != true {
			t.Fatalf("local setup observation triggered %s", c.method)
		}
	}
}

func TestOnboardingOffersScopedRecoveryAndPreservesUserFocus(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{})
	home := w.current()
	focus := func() string { v := w.view(home); return home.selected(v).key }
	s := positiveFrame(w.Config.Selected)
	w.acceptAccount(s)
	if focus() != "api" {
		t.Fatal("signed-in home should start with the services")
	}
	s.Revision++
	s.ConsentRequired = true
	w.acceptAccount(s)
	if focus() != "agreement-required" {
		t.Fatal("newly reported terms should be actionable")
	}
	s.Revision++
	s.Platform = "expired"
	w.acceptAccount(s)
	if focus() != "login" {
		t.Fatal("login must precede consent")
	}
	w.move(2)
	prev := focus()
	s.ConsentRequired = false
	s.Revision++
	w.acceptAccount(s)
	if focus() != prev {
		t.Fatal("a later observation stole the user's focus")
	}
	home.interacted = false
	s.VPN, s.Platform, s.Revision = "unknown", "unknown", s.Revision+1
	w.acceptAccount(s)
	if focus() == "login" || focus() == "workspace-start" {
		t.Fatal("an unknown school state is not evidence of missing setup or a login rejection")
	}
	w.connection = controllerRecovery{code: "AGENT_NOT_RUNNING", action: install.WorkspaceInitialize}
	if focus() != "workspace-start" {
		t.Fatal("initial setup is not the default")
	}
	for _, cached := range []bool{true, false} {
		w.Cached = cached
		home.interacted = false
		if (focus() == "workspace-start") == cached {
			t.Fatal("workspace action in the wrong mode")
		}
	}
	w.tutorials()
	v := w.view(w.current())
	if w.statusPage(w.current()) || slices.ContainsFunc(v.rows, func(r row) bool { return r.recovery }) {
		t.Fatal("workspace failures obscured the offline tutorial")
	}
}

func TestUnavailableControllerNeverStartsFromNavigation(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{reply: func(context.Context, string, map[string]any) (any, error) {
		return domain.StatusReport{}, nil
	}})
	home := w.current()
	var starts atomic.Int32
	release := make(chan struct{})
	w.StartWorkspace = func(ctx context.Context, action string) error {
		starts.Add(1)
		if action != string(install.WorkspaceInitialize) {
			return fmt.Errorf("unexpected setup action %q", action)
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	w.controllerProblem(domain.E("CONTROLLER_CONNECT", "permission denied"))
	v := w.view(home)
	if home.selected(v).key != "reconnect" {
		t.Fatal("a permission failure offered installation")
	}
	profile := w.Config.Profiles[w.Config.Selected]
	profile.Controller = "ssh://operator@remote.invalid"
	w.Config.Profiles[w.Config.Selected] = profile
	w.controllerProblem(errors.New("test remote unavailable"))
	if home.selected(w.view(home)).key != "reconnect" {
		t.Fatal("remote transport failure must not offer a local install")
	}
	profile.Controller = "local"
	w.Config.Profiles[w.Config.Selected] = profile
	w.connection = controllerRecovery{code: "AGENT_NOT_RUNNING", action: install.WorkspaceInitialize}
	home.interacted = false
	trialKey(w, tcell.KeyRight)
	if starts.Load() != 0 || w.connection.starting {
		t.Fatal("right arrow started setup")
	}
	trialKey(w, tcell.KeyEnter)
	trialKey(w, tcell.KeyEnter)
	close(release)
	waitFor(t, w, func() bool { return !w.connection.starting })
	if starts.Load() != 1 {
		t.Fatalf("explicit setup started %d times", starts.Load())
	}
}

func TestLoginAtBlockedReadReturnsToSamePage(t *testing.T) {
	var reads, prompts atomic.Int32
	w := fixtureWorkspace(t, &fixtureBackend{reply: func(_ context.Context, method string, _ map[string]any) (any, error) {
		switch method {
		case "status.get":
			return domain.StatusReport{}, nil
		case "portal.read":
			if reads.Add(1) == 1 {
				return nil, domain.E("AUTH_REQUIRED", "需要确认身份。")
			}
			return domain.PortalPage{}, nil
		}
		t.Fatalf("unexpected request %s", method)
		return nil, nil
	}})
	w.Foreground = func(_ context.Context, action TerminalAction) error {
		if action.Name != "login" || len(action.Args) != 0 {
			return fmt.Errorf("unexpected action %v", action)
		}
		if prompts.Add(1) == 1 {
			return errors.New("用户取消")
		}
		return nil
	}
	p := queryPage(w, "需要身份的读取", "bupt api model", "portal.read", nil, true, func(domain.PortalPage) content { return content{empty: "已读取"} })
	w.push(p)
	waitFor(t, w, func() bool { return p.blockedByLogin })
	if p.selected(w.view(p)).key != "login" {
		t.Fatal("no recovery on the protected page")
	}
	trialKey(w, tcell.KeyEnter)
	trialKey(w, tcell.KeyRight)
	if !w.current().loginMenu || prompts.Load() != 0 {
		t.Fatal("browsing started login")
	}
	trialKey(w, tcell.KeyEnter)
	if !w.current().loginMenu || reads.Load() != 1 {
		t.Fatal("cancelled login cleared its destination")
	}
	trialKey(w, tcell.KeyEnter)
	waitFor(t, w, func() bool { return !p.loading && !p.blockedByLogin })
	if w.current() != p || reads.Load() != 2 || prompts.Load() != 2 {
		t.Fatalf("unexpected return or duplicate request: %d reads %d logins", reads.Load(), prompts.Load())
	}
}

func TestNoServiceManagerRequiresChoosingProcess(t *testing.T) {
	w := fixtureWorkspace(t, &fixtureBackend{reply: func(context.Context, string, map[string]any) (any, error) {
		return domain.StatusReport{}, nil
	}})
	home := w.current()
	w.connection = controllerRecovery{code: "AGENT_NOT_RUNNING", action: install.WorkspaceInitialize}
	calls := make(chan string, 2)
	w.StartWorkspace = func(_ context.Context, action string) error {
		calls <- action
		if action == string(install.WorkspaceInitialize) {
			return domain.E("USER_SERVICE_UNAVAILABLE", "没有用户服务总线")
		}
		return nil
	}
	trialKey(w, tcell.KeyEnter)
	waitFor(t, w, func() bool { return !w.connection.starting })
	if len(calls) != 1 || <-calls != "initialize" {
		t.Fatal("tried a fallback without an explicit choice")
	}
	r := home.selected(w.view(home))
	if r == nil || r.title != "作为本机进程启动" {
		t.Fatal("missing process choice", r)
	}
	trialKey(w, tcell.KeyRight)
	if len(calls) != 0 {
		t.Fatal("navigation started an alternative controller")
	}
	trialKey(w, tcell.KeyEnter)
	waitFor(t, w, func() bool { return !w.connection.starting })
	if len(calls) != 1 || <-calls != "process" {
		t.Fatal("explicit process selection not respected")
	}
}

func TestRenewedComputeLoginRetriesOnlyRejectedRead(t *testing.T) {
	var reads atomic.Int32
	q := &fixtureBackend{reply: func(_ context.Context, _ string, _ map[string]any) (any, error) {
		if reads.Add(1) == 1 {
			return nil, domain.E("AUTH_REQUIRED", "GPU ticket required")
		}
		return domain.Snapshot{}, nil
	}}
	w := fixtureWorkspace(t, q)
	s := positiveFrame(w.Config.Selected)
	w.acceptAccount(s)
	p := queryPage(w, "算力资源", "", "compute.read", nil, true, func(domain.Snapshot) content { return content{} })
	w.push(p)
	waitFor(t, w, func() bool { return p.blockedByLogin })
	s.Revision++
	s.ValidUntil = s.ValidUntil.Add(time.Minute)
	w.acceptAccount(s)
	if p.loading || reads.Load() != 1 {
		t.Fatal("passive verification retried the request")
	}
	s.Revision++
	s.LoginRevision++
	w.acceptAccount(s)
	waitFor(t, w, func() bool { return !p.loading && !p.blockedByLogin })
	if reads.Load() != 2 {
		t.Fatal("compute login did not restore the read")
	}
	for _, report := range []domain.StatusReport{{Platform: "expired"}, {AuthReason: "AUTH_1032"}, {Warnings: []string{"AUTH_REQUIRED"}}, {Gateway: "expired"}} {
		if !schoolLoginCode(computeLoginCode(report)) {
			t.Fatal(report)
		}
	}
	if computeLoginCode(domain.StatusReport{Platform: "unknown", Warnings: []string{"NETWORK"}}) != "" {
		t.Fatal("a network error prompted authentication")
	}
}

func TestStatusDenialArrivingAfterLoginOrReconnect(t *testing.T) {
	for _, tc := range []struct {
		name       string
		report     domain.StatusReport
		err        error
		stillNeeds bool
		leave      bool
	}{
		{name: "expired in status report", report: domain.StatusReport{Platform: "expired"}},
		{name: "auth RPC refusal", err: domain.E("AUTH_REQUIRED", "需要登录")},
		{name: "old controller missing", err: domain.E("AGENT_NOT_RUNNING", "服务已停止")},
		{name: "the renewed ticket still fails", report: domain.StatusReport{Platform: "expired"}, stillNeeds: true},
		{name: "user left the area before the old denial", report: domain.StatusReport{Platform: "expired"}, leave: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			w := fixtureWorkspace(t, &fixtureBackend{reply: func(_ context.Context, method string, p map[string]any) (any, error) {
				if method != "status.get" || p["compute"] != true || p["cached"] != false {
					return nil, errors.New("unexpected operation")
				}
				if calls.Add(1) == 1 {
					close(started)
					<-release
					return tc.report, tc.err
				}
				if tc.stillNeeds {
					return domain.StatusReport{Platform: "expired"}, nil
				}
				return domain.StatusReport{Gateway: "authenticated", Platform: "authenticated", Source: "live"}, nil
			}})
			page := &page{area: "compute", build: w.computeMenu}
			w.push(page)
			s := positiveFrame(w.Config.Selected)
			w.acceptAccount(s)
			w.refreshStatus(false)
			<-started
			if errorCode(tc.err) == "AGENT_NOT_RUNNING" {
				s.Epoch = "new-controller"
			} else {
				s.LoginRevision++
			}
			s.Revision++
			w.acceptAccount(s)
			if tc.leave {
				w.back()
			}
			close(release)
			waitFor(t, w, func() bool { return !w.refreshing })
			wantCalls, wantStatus := int32(2), ""
			if tc.leave {
				wantCalls = 1
			}
			if tc.stillNeeds {
				wantStatus = "AUTH_REQUIRED"
			}
			if calls.Load() != wantCalls || w.statusCode != wantStatus || w.connection.code != "" || !tc.leave && !tc.stillNeeds && w.report.Platform != "authenticated" {
				t.Fatalf("late denial after new evidence: %d reads, %s, %s", calls.Load(), w.statusCode, w.connection.code)
			}
		})
	}
}
