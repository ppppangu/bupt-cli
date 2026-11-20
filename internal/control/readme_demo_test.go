package control

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"bupt.local/cli/internal/domain"
)

const demoModel = "qwen-demo"

type readmeSchool struct {
	*tuiSchool
}

func (s *readmeSchool) Specs(ctx context.Context) ([]domain.Spec, error) {
	spec, err := s.Spec(ctx, "9")
	return []domain.Spec{spec}, err
}

func (s *readmeSchool) Spec(context.Context, domain.ID) (domain.Spec, error) {
	return domain.Spec{ID: "9", Name: "gpu-a", Version: "PyTorch", Options: map[string]any{
		"poolName": "gpu", "poolDisplayName": "GPU 资源池", "autoApprove": true,
		"resourceSummary":     map[string]any{"gpu": 100, "cpu": 4, "memory": 16384},
		"resourceConstraints": `{"900":{"role":"workStation","constraints":[{"type":"GPU","value":"1:A100-40GB:40960"},{"type":"CPU","value":"4"},{"type":"Memory","value":"16384"}]}}`,
	}}, nil
}

func (s *readmeSchool) Submit(ctx context.Context, req domain.TaskRequest) (domain.TaskResult, error) {
	var delay time.Duration
	switch req.Type {
	case "CreateAppTask":
		delay = 1700 * time.Millisecond
	case "StartAppTask":
		delay = 3500 * time.Millisecond
	}
	if delay > 0 {
		select {
		case <-ctx.Done():
			return domain.TaskResult{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	result, err := s.lifecyclePlatform.Submit(ctx, req)
	if err == nil && req.Type == "CreateAppTask" {
		s.mu.Lock()
		for index := range s.snap.Instances {
			instance := &s.snap.Instances[index]
			if instance.ID == result.InstanceID {
				for node := range instance.Nodes {
					instance.Nodes[node].Resources = map[string]any{
						"gpu": 100, "gpuType": "A100-40GB", "gpuMem": 40960, "cpu": 4, "memory": 16384,
					}
				}
				break
			}
		}
		s.mu.Unlock()
	}
	return result, err
}

func (s *readmeSchool) PortalPage(ctx context.Context, a domain.PortalAccount, p domain.PortalInput) (domain.PortalPage, error) {
	page, err := s.tuiSchool.PortalPage(ctx, a, p)
	if p.Section == "model" {
		page.Models[0].ID, page.Models[0].Name = demoModel, "Qwen Demo"
		page.Models[0].Context = "64K"
		alternate := page.Models[0]
		alternate.ID, alternate.Name, alternate.Context = "qwen-lite", "Qwen Lite", "32K"
		page.Models = append(page.Models, alternate)
	}
	if p.Section == "guide" {
		page.Data["base_url"] = "https://api.example.invalid/v1"
	}
	return page, err
}

func (s *readmeSchool) RunModelTrial(_ context.Context, base string, r domain.TrialRequest, key string) (domain.TrialResult, error) {
	messages, ok := r.Body["messages"].([]map[string]string)
	if !ok || len(messages) != 1 || key != "fixture-private-primary" || r.Path != "/chat/completions" ||
		r.Model != demoModel || base != "https://api.example.invalid/v1" || messages[0]["role"] != "user" {
		return domain.TrialResult{}, domain.E("FIXTURE_MISMATCH", "模型、密钥或请求格式错误")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.input = append(s.input, messages[0]["content"])
	cost, input, output, total := 0.000014, 12, 22, 34
	s.cost += cost
	return domain.TrialResult{Model: demoModel, Kind: "chat", RequestID: "demo-request", ElapsedMS: 328,
		Answer:     "GPU 擅长并行处理大量同类计算。CPU 擅长处理复杂指令和通用任务。",
		CostPoints: &cost, Usage: &domain.TrialUsage{Prompt: &input, Completion: &output, Total: &total}}, nil
}

func TestREADMEWorkflow(t *testing.T) {
	name := os.Getenv("BUPT_DEMO_WORKFLOW")
	switch name {
	case "":
		t.Skip("run docs/demo/record.sh to record a README workflow")
	case "tui-compute", "tui-model", "tui-integration", "cli-demo":
	default:
		t.Fatalf("unknown demo workflow %q", name)
	}
	vhs, err := exec.LookPath("vhs")
	if err != nil {
		t.Fatal(err)
	}
	c, school, _ := lifecycleController(t)
	school.snap.Instances = []domain.Instance{
		{ID: "19", OwnerID: "7", UserID: "7", Name: "course-dev", State: "stopped", SpecID: "9", ReserveType: "manual"},
	}
	ui := &tuiSchool{lifecyclePlatform: school, keys: []domain.PortalKey{
		{ID: "primary", Name: "课程调试", Masked: "demo-…01", Status: "active", Models: []string{demoModel}},
		{ID: "backup", Name: "程序开发", Masked: "demo-…02", Status: "active", Models: []string{demoModel}},
	}}
	adapter := &readmeSchool{tuiSchool: ui}
	_, bin, cfg, socket, dir, ssh := serveTerminalFixture(t, c, ui, adapter, "gpu-lab")
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, name+".gif")
	screen := filepath.Join(dir, name+".ascii")
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, vhs, "-o", out, "-o", screen, filepath.Join(repo, "docs/demo/tapes", name+".tape"))
	cmd.Dir = dir
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "BUPT_") || strings.HasPrefix(e, "XDG_") || strings.HasPrefix(e, "PATH=") || strings.HasPrefix(e, "HISTFILE=")
	}), "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BUPT_CONFIG="+cfg, "BUPT_CONTROLLER_SOCKET="+socket, "BUPT_DEMO_FIXTURE=1", "BUPT_NO_PROJECT=1",
		"XDG_DATA_HOME="+dir, "XDG_STATE_HOME="+dir, "XDG_CONFIG_HOME="+dir, "HISTFILE=/dev/null", "TZ=Asia/Shanghai")
	output, err := cmd.CombinedOutput()
	if err != nil || strings.Contains(string(output), "could not match") || strings.Contains(string(output), "timed out") {
		t.Fatalf("VHS %s: %v\n%s", name, err, output)
	}
	t.Log(string(output))
	checkREADMEWorkflow(t, name, dir, school, ui, ssh)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > 4*1024*1024 || !strings.HasPrefix(string(data[:min(6, len(data))]), "GIF8") {
		t.Fatalf("invalid demo GIF or size exceeds 4 MiB: %d bytes", len(data))
	}
	path := filepath.Join(repo, "docs/demo/assets", name+".gif")
	file, err := os.CreateTemp(filepath.Dir(path), ".record-*.gif")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Chmod(0644); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		t.Fatal(err)
	}
}

func checkREADMEWorkflow(t *testing.T, name, dir string, school *lifecyclePlatform, ui *tuiSchool, ssh *streamTestSSH) {
	t.Helper()
	if name == "tui-compute" || name == "cli-demo" {
		for _, op := range []string{"CreateAppTask", "AppReserveTask", "StartAppTask", "StopAppTask"} {
			if school.count(op) != 1 {
				t.Fatalf("expected one %s, got %d", op, school.count(op))
			}
		}
		if school.count("ForceStopAppTask") != 0 || school.count("RemoveAppTask") != 0 {
			t.Fatal("stop caused an unexpected side effect")
		}
		snap, err := school.Sync(t.Context())
		if err != nil || !slices.ContainsFunc(snap.Instances, func(i domain.Instance) bool {
			return i.Name == "lab" && i.State == "stopped" && i.Reserved &&
				len(i.Nodes) == 1 && number(i.Nodes[0].Resources["gpu"]) == 100
		}) {
			t.Fatalf("lab did not stop with its allocation intact: %+v %v", snap.Instances, err)
		}
		ssh.mu.Lock()
		defer ssh.mu.Unlock()
		if !slices.Equal(ssh.shellInput, []string{"hostname", "exit"}) {
			t.Fatalf("interactive SSH input %q", ssh.shellInput)
		}
	}
	ui.mu.Lock()
	defer ui.mu.Unlock()
	if name == "tui-model" {
		if !slices.Equal(ui.input, []string{"GPU 和 CPU 有什么区别？"}) || ui.cost != 0.000014 {
			t.Fatalf("inference input %q or cost %v", ui.input, ui.cost)
		}
	} else if len(ui.input) != 0 || ui.cost != 0 {
		t.Fatal("an example or compute demo triggered a model call")
	}
	if name == "tui-integration" {
		for _, example := range []struct{ file, prefix string }{{"example.py", "import "}, {"example.sh", "curl "}} {
			data, err := os.ReadFile(filepath.Join(dir, example.file))
			code := string(data)
			if err != nil || !strings.Contains(code, example.prefix) || !strings.Contains(code, demoModel) ||
				!strings.Contains(code, "https://api.example.invalid/v1") || !strings.Contains(code, "BUPT_API_KEY") ||
				strings.Contains(code, "fixture-private") {
				t.Fatalf("invalid or unsafe %s: %v", example.file, err)
			}
		}
	}
}
