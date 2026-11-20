package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/client"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/platform"
)

type tuiSchool struct {
	*lifecyclePlatform
	keys  []domain.PortalKey
	input []string
	cost  float64
}

func (s *tuiSchool) PortalAccount(context.Context) (domain.PortalAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	balance := 30 - s.cost
	return domain.PortalAccount{SID: "cas_20260005", Name: "终端验收", Balance: &balance}, nil
}

func (s *tuiSchool) PortalPage(_ context.Context, a domain.PortalAccount, p domain.PortalInput) (domain.PortalPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := domain.PortalPage{Section: p.Section, Account: &a, Models: []domain.PortalModel{}, Keys: []domain.PortalKey{}, ObservedAt: time.Now()}
	switch p.Section {
	case "model":
		input, output := 0.25, 0.50
		result.Models = append(result.Models, domain.PortalModel{ID: "fixture-chat", Name: "Fixture Chat", Kind: "chat", Health: "ok", InputPrice: &input, OutputPrice: &output})
	case "key":
		result.Keys = slices.Clone(s.keys)
	case "guide":
		result.Data = map[string]any{"base_url": "https://myai.bupt.edu.cn/llm-gw/v1"}
	case "usage":
		window := client.SchoolUsageWindow(time.Now(), p.Days)
		dates := make([]string, 0, p.Days)
		for day := p.Days; day > 0; day-- {
			dates = append(dates, client.SchoolUsageWindow(time.Now(), day).Start)
		}
		result.Data = map[string]any{"days": window.Days, "dates": dates, "periodCalls": len(s.input), "periodTokens": 12, "periodCostPoints": s.cost, "usageAvailable": true}
	}
	return result, nil
}

func (s *tuiSchool) PortalChange(_ context.Context, _ domain.PortalChange) (domain.PortalKey, string, error) {
	return domain.PortalKey{}, "", errors.New("the journey selects an existing fixture key")
}

func (s *tuiSchool) RunModelTrial(_ context.Context, base string, r domain.TrialRequest, key string) (domain.TrialResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	messages, ok := r.Body["messages"].([]map[string]string)
	if base != "https://myai.bupt.edu.cn/llm-gw/v1" || r.Path != "/chat/completions" || r.Model != "fixture-chat" ||
		(key != "fixture-private-primary" && key != "fixture-private-backup") || !ok || len(messages) != 1 || messages[0]["role"] != "user" {
		return domain.TrialResult{}, domain.E("FIXTURE_MISMATCH", "错误的模型、授权或请求正文")
	}
	prompt := messages[0]["content"]
	s.input = append(s.input, prompt)
	switch prompt {
	case "unknown-response":
		s.cost += 0.25
		return domain.TrialResult{}, domain.E("MODEL_RESULT_UNKNOWN", "学校响应超时，请核对用量")
	case "expired-key":
		if key != "fixture-private-primary" {
			return domain.TrialResult{}, domain.E("FIXTURE_MISMATCH", "密钥上下文丢失")
		}
		s.keys[0].Status = "disabled"
		return domain.TrialResult{}, domain.E("MODEL_KEY_REJECTED", "所选 Key 已失效")
	case "insufficient":
		return domain.TrialResult{}, domain.E("MODEL_BALANCE_INSUFFICIENT", "余额不足")
	case "first-question", "second-question":
		cost, tokens := 1.25, 6
		s.cost += cost
		return domain.TrialResult{Model: r.Model, Kind: r.Kind, Answer: "answer-to-" + prompt,
			CostPoints: &cost, RequestID: "request-" + prompt, Usage: &domain.TrialUsage{Total: &tokens}}, nil
	default:
		return domain.TrialResult{}, domain.E("FIXTURE_MISMATCH", "草稿错误或空输入抵达网关")
	}
}

func (s *tuiSchool) ComputePage(_ context.Context, section string, page, limit int, _ string) (platform.ComputePage, error) {
	if section != "billing" || limit != 100 {
		return platform.ComputePage{}, domain.E("FIXTURE_MISMATCH", "算力账单未使用学校分页契约")
	}
	now := client.SchoolUsageWindow(time.Now(), 1).End
	var ids []int
	switch page {
	case 1:
		ids = []int{12, 11, 10, 9, 8, 7, 6}
	case 2:
		ids = []int{6, 5, 4, 3, 2, 1}
	default:
		return platform.ComputePage{}, domain.E("FIXTURE_MISMATCH", "重复或越界分页")
	}
	r := platform.ComputePage{Section: section, Page: page, Limit: limit, Total: 12}
	for _, id := range ids {
		r.Items = append(r.Items, map[string]any{"id": id, "appName": fmt.Sprintf("bill-%02d", id), "endAt": now, "usagePoints": json.Number("0.25"), "state": "settled"})
	}
	return r, nil
}

// serveTerminalFixture starts a real controller on a private socket and builds
// the normal bupt executable for a terminal journey.
func serveTerminalFixture(t *testing.T, c *Controller, school *tuiSchool, api domain.PlatformAPI, hostname string) (context.Context, string, string, string, string, *streamTestSSH) {
	t.Helper()
	adapter := school
	c.opts.Factory = func(domain.Profile, domain.SecretStore) (domain.PlatformAPI, error) { return api, nil }
	ticket := c.accountTicket("test")
	account, _ := adapter.PortalAccount(t.Context())
	ticket.observePortal(account, true)
	for _, key := range adapter.keys {
		ns := c.portalNamespace("test")
		if err := c.Vault.Put(portalCacheKey(ns, account.SID, "secret/"+key.ID), "fixture-private-"+key.ID); err != nil {
			t.Fatal(err)
		}
		if err := c.Vault.Put(portalCacheKey(ns, account.SID, "stored/"+key.ID), true); err != nil {
			t.Fatal(err)
		}
	}

	ssh := newStreamTestSSH(t)
	ssh.mu.Lock()
	ssh.interactiveShell = true
	ssh.demoHostname = hostname
	ssh.mu.Unlock()
	route := ssh.route()
	school.sshFixture = &route
	dir, err := os.MkdirTemp("", "bupt-tui-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := *c.Configuration()
	cfg.Paths.DataDir = dir
	c.configuration.Store(&cfg)
	if err := os.Mkdir(filepath.Join(dir, "keys"), 0700); err != nil {
		t.Fatal(err)
	}
	private, err := os.ReadFile(ssh.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keys", "test-automation"), private, 0600); err != nil {
		t.Fatal(err)
	}
	clear(private)
	configFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(configFile, []byte("version=1\ndefault_profile='test'\n[profiles.test]\nplatform_url='https://example.invalid'\n[policy]\nmax_runtime='20m'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Second)
	socket := filepath.Join(dir, "agent.sock")
	serving := make(chan error, 1)
	go func() { serving <- c.Serve(ctx, socket) }()
	t.Cleanup(func() { cancel(); <-serving })
	for {
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	bin := filepath.Join(dir, "bupt")
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/bupt")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return ctx, bin, configFile, socket, dir, ssh
}

func TestTUISingleSessionPTY(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and drives the real terminal program")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for the POSIX PTY driver")
	}
	c, school, _ := lifecycleController(t)
	adapter := &tuiSchool{lifecyclePlatform: school, keys: []domain.PortalKey{
		{ID: "primary", Name: "Primary key", Status: "active", Models: []string{"fixture-chat"}},
		{ID: "backup", Name: "Backup key", Status: "active", Models: []string{"fixture-chat"}},
	}}
	ctx, bin, configFile, socket, dir, ssh := serveTerminalFixture(t, c, adapter, adapter, "")
	driver := exec.CommandContext(ctx, python, "../../scripts/check_tui_session.py", bin, configFile, socket, dir)
	if out, err := driver.CombinedOutput(); err != nil {
		t.Fatalf("PTY: %v\n%s", err, out)
	} else {
		t.Log(string(out))
	}
	ssh.mu.Lock()
	input := slices.Clone(ssh.shellInput)
	ssh.mu.Unlock()
	if !slices.Equal(input, []string{"terminal-check", "exit"}) {
		t.Fatalf("wrong SSH input: %q", input)
	}
	if school.count("CreateAppTask") != 1 || school.count("StartAppTask") != 1 || school.count("StopAppTask") != 1 || school.count("ForceStopAppTask") != 0 || school.count("AppUnreserveTask") != 0 {
		t.Fatal("TUI submitted duplicate or unexpected lifecycle operations")
	}
	snapshot, err := school.Sync(t.Context())
	if err != nil || len(snapshot.Instances) != 1 || snapshot.Instances[0].State != "stopped" || !snapshot.Instances[0].Reserved {
		t.Fatal(snapshot.Instances, err)
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if !slices.Equal(adapter.input, []string{"first-question", "second-question", "unknown-response", "expired-key", "insufficient"}) || adapter.cost != 2.75 {
		t.Fatalf("unexpected inference effect: input=%q cost=%g", adapter.input, adapter.cost)
	}
	code, err := os.ReadFile(filepath.Join(dir, "example.py"))
	if err != nil || !strings.Contains(string(code), "second-question") || !strings.Contains(string(code), "fixture-chat") || strings.Contains(string(code), "fixture-private") {
		t.Fatalf("wrong exported code: %v\n%s", err, code)
	}
}
