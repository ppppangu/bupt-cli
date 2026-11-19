//go:build demogen

// Package tui demo generator: renders scripted journeys through the real
// TUI with canned school data, dumping one ANSI frame per keystroke so the
// recorded GIFs match real navigation. Build with -tags demogen; the
// shipped binary never includes this file.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"bupt.local/cli/internal/navigation"
	"github.com/gdamore/tcell/v2"
)

func fptr(v float64) *float64 { return &v }
func iptr(v int) *int         { return &v }

// demoBackend answers the methods the scripted journeys touch.
type demoBackend struct{}

func (demoBackend) Call(_ context.Context, method string, params, out any) error {
	var m map[string]any
	_ = json.Unmarshal(domain.Result(params), &m)
	value, err := demoReply(method, m)
	if err != nil {
		return err
	}
	return json.Unmarshal(domain.Result(value), out)
}

func (demoBackend) Stream(context.Context, string, any, io.Reader, io.Writer) (map[string]any, error) {
	return nil, fmt.Errorf("demo backend does not stream")
}

var demoModels = []domain.PortalModel{
	{ID: "qwen3.8-27b", Name: "Qwen3.8-27B", Kind: "chat", Vendor: "通义千问", Context: "128K", InputPrice: fptr(0.8), OutputPrice: fptr(1.6), Health: "ok", LatencyMS: fptr(312), SampledAt: "2 分钟前"},
	{ID: "deepseek-v4-flash", Name: "DeepSeek-V4-Flash", Kind: "chat", Vendor: "深度求索", Context: "64K", InputPrice: fptr(0.4), OutputPrice: fptr(1.2), Health: "ok", LatencyMS: fptr(268), SampledAt: "1 分钟前"},
	{ID: "glm-4.5-air", Name: "GLM-4.5-Air", Kind: "chat", Vendor: "智谱", Context: "128K", InputPrice: fptr(0.5), OutputPrice: fptr(1.5), Health: "degraded", SampledAt: "5 分钟前"},
	{ID: "bge-m3", Name: "BGE-M3", Kind: "embedding", Vendor: "BAAI", Context: "8K", InputPrice: fptr(0.1), OutputPrice: fptr(0.1), Health: "ok", LatencyMS: fptr(45), SampledAt: "刚刚"},
	{ID: "jina-embeddings-v3", Name: "Jina-Embeddings-v3", Kind: "embedding", Vendor: "Jina AI", Context: "32K", InputPrice: fptr(0.2), OutputPrice: fptr(0.2), Health: "ok", LatencyMS: fptr(88), SampledAt: "刚刚"},
	{ID: "jina-reranker-m0", Name: "Jina-Reranker-M0", Kind: "rerank", Vendor: "Jina AI", Context: "8K", InputPrice: fptr(0.2), OutputPrice: fptr(0.2), Health: "ok", LatencyMS: fptr(60), SampledAt: "刚刚"},
}

var demoKeys = []domain.PortalKey{
	{ID: "key-research", Name: "科研", Masked: "sk-…7f2k", Models: []string{"qwen3.8-27b", "bge-m3", "jina-reranker-m0"}, Status: "active", Used: fptr(123.4), CreatedAt: "2026-11-02", Stored: true},
	{ID: "key-course", Name: "课程作业", Masked: "sk-…a9q1", Models: []string{"qwen3.8-27b", "deepseek-v4-flash"}, Status: "active", Used: fptr(58.1), CreatedAt: "2026-11-15", Stored: true},
}

var demoPools = []domain.ResourcePool{
	{ID: "pool-gpu", Name: "GPU 资源池", Templates: []domain.TemplateSummary{
		{ID: "tpl-pytorch", Name: "PyTorch 2.3 · CUDA 12.1", Version: "v2", Resources: map[string]any{"gpu": "1×A100-80G", "cpu": 8, "memory": 65536}},
		{ID: "tpl-llama", Name: "Llama-Factory 微调环境", Version: "v1", Resources: map[string]any{"gpu": "2×A100-80G", "cpu": 16, "memory": 131072}},
	}},
	{ID: "pool-cpu", Name: "CPU 资源池", Templates: []domain.TemplateSummary{
		{ID: "tpl-dev", Name: "Ubuntu 22.04 开发环境", Version: "v3", Resources: map[string]any{"cpu": 4, "memory": 16384}},
	}},
}

func demoSpec() domain.Spec {
	return domain.Spec{
		ID: "tpl-pytorch", Name: "PyTorch 2.3 · CUDA 12.1", Version: "v2",
		Description: "预装 PyTorch 2.3 与 CUDA 12.1 驱动，适合模型微调与推理实验。",
		Options: map[string]any{
			"scope":       "user",
			"storageName": "系统盘 100 GiB",
			"resourceSummary": map[string]any{"gpu": "1×A100-80G", "cpu": 8, "memory": 65536},
			"resourceConstraints": map[string]any{"gpu": map[string]any{"nodeName": "GPU 节点", "constraints": []map[string]any{
				{"type": "GPU", "value": "1×A100-80G", "options": "1×A100-80G,2×A100-80G"},
				{"type": "CPU", "value": "8", "options": "8,16"},
				{"type": "Memory", "value": "65536", "options": "65536,131072"},
			}}},
		},
	}
}

func demoReply(method string, p map[string]any) (any, error) {
	switch method {
	case "status.get":
		now := time.Now()
		return domain.StatusReport{
			Profile: "默认", Controller: "本机", Platform: "authenticated", Gateway: "authenticated",
			API: domain.PortalSummary{State: "authenticated", Balance: fptr(1234.5), Keys: iptr(2), Models: iptr(6), CheckedAt: now},
			Catalog:           &domain.CatalogCounts{Pools: 2, Templates: 3, ObservedAt: now},
			SnapshotAvailable: true, Source: "live", CheckedAt: now,
			Instances: []domain.Instance{
				{ID: "101", Name: "llm-finetune", SpecID: "tpl-pytorch", State: "running", Health: "healthy", Reserved: true, ExpiresAt: now.Add(20 * time.Minute), CreatedAt: now.Add(-3 * time.Hour), Nodes: []domain.Node{{ID: "7", Name: "worker-a100-07"}}, SSHState: "ready"},
				{ID: "102", Name: "gpu-dev", SpecID: "tpl-pytorch", State: "stopped", Reserved: false, CreatedAt: now.Add(-26 * time.Hour)},
			},
			Runs: []domain.Run{{ID: "run-9f21", Kind: "session", State: "running", InstanceID: "101"}},
			Routes: []domain.Route{{InstanceID: "101", NodeID: "7", State: "ready"}},
		}, nil
	case "portal.read":
		switch p["section"] {
		case "model":
			return domain.PortalPage{Section: "model", Source: "live", ObservedAt: time.Now(), Models: demoModels}, nil
		case "key":
			return domain.PortalPage{Section: "key", Source: "live", ObservedAt: time.Now(), Keys: demoKeys}, nil
		}
	case "resources.overview":
		return domain.ResourceOverview{
			Profile:     "默认",
			Capacity:    []domain.Capacity{{Scope: "个人", Resource: "GPU", Used: fptr(1), Limit: fptr(2), Unit: "卡"}, {Scope: "个人", Resource: "CPU", Used: fptr(4), Limit: fptr(16), Unit: "核"}, {Scope: "项目", Resource: "算力积分", Used: fptr(12.5), Limit: fptr(100), Unit: "分/天"}},
			QuotaSource: "学校", Pools: demoPools, CatalogSource: "live", CatalogAt: &[]time.Time{time.Now().Add(-9 * time.Minute)}[0],
		}, nil
	case "spec.show":
		return demoSpec(), nil
	case "portal.trial":
		return domain.TrialResult{
			ResponseID: "resp-9f21", RequestID: "req-77aa", CostPoints: fptr(0.42),
			Model: "qwen3.8-27b", Kind: "chat", ElapsedMS: 1480, Finish: "stop",
			Answer: "北邮「人人有算力」平台为师生提供模型 API 与 GPU 算力：登录统一身份认证即可申请实例、调用模型，按实际用量计费。",
			Usage:  &domain.TrialUsage{Prompt: iptr(14), Completion: iptr(38), Total: iptr(52)},
		}, nil
	}
	return nil, fmt.Errorf("demo backend: no reply for %s", method)
}

func demoWorkspace(home string) (*workspace, error) {
	cfg, e := config.Load(config.Options{HomeDir: home, Env: map[string]string{}, NoProject: true})
	if e != nil {
		return nil, e
	}
	cfg.Policy.MaxRuntime = "20m"
	s := tcell.NewSimulationScreen("UTF-8")
	if e := s.Init(); e != nil {
		return nil, e
	}
	now := time.Now()
	w := &workspace{
		Options:   Options{Config: cfg, Backend: demoBackend{}, Timeout: 2 * time.Second},
		screen:    s,
		ctx:       context.Background(),
		events:    make(chan func(), 16),
		hasReport: true,
		bar: accountView{
			snapshot: domain.AccountStatus{Profile: "默认", VPN: "not_required", Platform: "authenticated", Name: "张三", CampusID: "2022110000", Balance: fptr(1234.5), Running: iptr(2)},
		},
		report: domain.StatusReport{
			Profile: "默认", Controller: "本机", Platform: "authenticated", Gateway: "authenticated",
			API:               domain.PortalSummary{State: "authenticated", Balance: fptr(1234.5), Keys: iptr(2), Models: iptr(6), CheckedAt: now},
			SnapshotAvailable: true, Source: "live", CheckedAt: now,
			Instances: []domain.Instance{
				{ID: "101", Name: "llm-finetune", SpecID: "tpl-pytorch", State: "running", Health: "healthy", Reserved: true, ExpiresAt: now.Add(20 * time.Minute), CreatedAt: now.Add(-3 * time.Hour), Nodes: []domain.Node{{ID: "7", Name: "worker-a100-07"}}, SSHState: "ready"},
				{ID: "102", Name: "gpu-dev", SpecID: "tpl-pytorch", State: "stopped", Reserved: false, CreatedAt: now.Add(-26 * time.Hour)},
			},
			Runs:  []domain.Run{{ID: "run-9f21", Kind: "session", State: "running", InstanceID: "101"}},
			Routes: []domain.Route{{InstanceID: "101", NodeID: "7", State: "ready"}},
		},
	}
	w.pages = []*page{{title: "校园算力平台", command: "bupt status", build: w.home}}
	s.SetSize(100, 28)
	w.draw()
	return w, nil
}

// pump drains events posted by asynchronous page loads.
func pump(w *workspace) {
	for {
		select {
		case f := <-w.events:
			f()
			continue
		case <-time.After(100 * time.Millisecond):
			return
		}
	}
}

type journey struct {
	scene string
	w     *workspace
	out   string
	log   []string
	step  int
}

func (j *journey) rows() []row { return j.w.view(j.w.current()).rows }

func (j *journey) rowList() string {
	var parts []string
	for _, r := range j.rows() {
		parts = append(parts, r.key+"="+r.title)
	}
	return strings.Join(parts, " | ")
}

func (j *journey) click(key string) error {
	for i, r := range j.rows() {
		if r.key == key {
			j.w.move(i)
			j.w.handleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
			pump(j.w)
			return nil
		}
	}
	return fmt.Errorf("row %q not found on %q; rows: %s", key, j.w.current().title, j.rowList())
}

func (j *journey) clickTitle(contains string) error {
	for i, r := range j.rows() {
		if strings.Contains(r.title, contains) {
			j.w.move(i)
			j.w.handleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0))
			pump(j.w)
			return nil
		}
	}
	return fmt.Errorf("row title containing %q not found on %q", contains, j.w.current().title)
}

func (j *journey) frame(note string) error {
	j.step++
	pump(j.w)
	j.w.draw()
	name := fmt.Sprintf("%02d-%s.ans", j.step, note)
	if e := dumpANSI(j.w, filepath.Join(j.out, j.scene, name)); e != nil {
		return e
	}
	j.log = append(j.log, fmt.Sprintf("%s %02d %-22s %s", j.scene, j.step, note, j.w.current().title))
	return nil
}

// frameRaw captures the screen without draining events, for pending states.
func (j *journey) frameRaw(note string) error {
	j.step++
	j.w.draw()
	name := fmt.Sprintf("%02d-%s.ans", j.step, note)
	if e := dumpANSI(j.w, filepath.Join(j.out, j.scene, name)); e != nil {
		return e
	}
	j.log = append(j.log, fmt.Sprintf("%s %02d %-22s %s", j.scene, j.step, note, j.w.current().title))
	return nil
}

// key sends one keystroke (as the recorder will) and captures the frame it
// produces, keeping frames and keystrokes aligned one to one.
func (j *journey) key(k tcell.Key, note string) error {
	j.w.handleKey(tcell.NewEventKey(k, 0, 0))
	pump(j.w)
	return j.frame(note)
}

// runeKey types one rune and captures the resulting frame.
func (j *journey) runeKey(r rune, note string) error {
	j.w.handleKey(tcell.NewEventKey(tcell.KeyRune, r, 0))
	pump(j.w)
	return j.frame(note)
}

// typeFrames types text rune by rune, capturing a frame per keystroke.
func (j *journey) typeFrames(text, note string) error {
	for i, r := range []rune(text) {
		if e := j.runeKey(r, fmt.Sprintf("%s-%02d", note, i+1)); e != nil {
			return e
		}
	}
	return nil
}

// GenerateDemoFrames renders the scripted TUI journeys into dir and writes
// a journey log alongside the ANSI frames.
func GenerateDemoFrames(dir string) error {
	if e := os.MkdirAll(filepath.Join(dir, "api"), 0o755); e != nil {
		return e
	}
	if e := os.MkdirAll(filepath.Join(dir, "compute"), 0o755); e != nil {
		return e
	}

	// --- Model API journey: browse models, run a chat trial, pick a key ---
	w, e := demoWorkspace(dir)
	if e != nil {
		return e
	}
	j := &journey{scene: "api", w: w, out: dir}
	area, _ := navigation.Find("api")
	w.openArea(area)
	if e := j.frame("api-home"); e != nil {
		return e
	}
	if e := j.runeKey('2', "models"); e != nil { // shortcut 2 → 模型与价格
		return e
	}
	if e := j.key(tcell.KeyEscape, "back-to-api"); e != nil {
		return e
	}
	if e := j.runeKey('1', "trial-tasks"); e != nil { // shortcut 1 → 在线调试
		return e
	}
	if e := j.key(tcell.KeyEnter, "model-picker"); e != nil { // 对话 selected → pick chat model
		return e
	}
	if e := j.key(tcell.KeyEnter, "trial-form"); e != nil { // first healthy chat model
		return e
	}
	if e := j.key(tcell.KeyEnter, "editor"); e != nil { // open the prompt editor
		return e
	}
	if e := j.typeFrames("用一句话介绍北邮的算力平台", "typing"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyEscape, "draft-saved"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyDown, "focus-key"); e != nil { // send row is not focusable before a key is chosen
		return e
	}
	if e := j.key(tcell.KeyEnter, "key-picker"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyEnter, "key-chosen"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyUp, "focus-send"); e != nil {
		return e
	}
	j.w.handleKey(tcell.NewEventKey(tcell.KeyEnter, 0, 0)) // submit; capture the pending state before the reply lands
	if e := j.frameRaw("submitting"); e != nil {
		return e
	}
	pump(j.w)
	if e := j.frame("reply"); e != nil {
		return e
	}

	// --- Compute journey: catalog, create form, instances, filter ---
	j.scene = "compute"
	j.step = 0
	w, e = demoWorkspace(dir)
	if e != nil {
		return e
	}
	j.w = w
	area, _ = navigation.Find("compute")
	w.openArea(area)
	if e := j.frame("compute-home"); e != nil {
		return e
	}
	if e := j.runeKey('3', "catalog"); e != nil { // shortcut 3 → 资源与价格
		return e
	}
	if e := j.key(tcell.KeyEscape, "back-to-compute"); e != nil {
		return e
	}
	if e := j.runeKey('2', "pick-template"); e != nil { // shortcut 2 → 创建实例
		return e
	}
	if e := j.key(tcell.KeyEnter, "create-form"); e != nil { // first template
		return e
	}
	if e := j.key(tcell.KeyDown, "focus-limit"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyDown, "focus-estimate"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyDown, "focus-preview"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyDown, "focus-options"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyDown, "focus-spec"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyEnter, "template-notes"); e != nil { // 查看模板说明
		return e
	}
	if e := j.key(tcell.KeyEscape, "back-to-form"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyEscape, "back-to-templates"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyEscape, "back-to-compute-2"); e != nil {
		return e
	}
	if e := j.runeKey('1', "instances"); e != nil { // shortcut 1 → 我的实例
		return e
	}
	if e := j.typeFrames("/gpu", "filter"); e != nil {
		return e
	}
	if e := j.key(tcell.KeyBackspace2, "filter-backspace-1"); e != nil { // gp
		return e
	}
	if e := j.key(tcell.KeyBackspace2, "filter-backspace-2"); e != nil { // g
		return e
	}
	if e := j.key(tcell.KeyBackspace2, "filter-clear"); e != nil { // empty
		return e
	}
	if e := j.key(tcell.KeyEnter, "filter-apply"); e != nil { // close the filter input
		return e
	}
	if e := j.key(tcell.KeyUp, "focus-llm"); e != nil { // move back to the running instance
		return e
	}
	if e := j.key(tcell.KeyRight, "instance-detail"); e != nil {
		return e
	}

	if e := os.WriteFile(filepath.Join(dir, "journey.txt"), []byte(strings.Join(j.log, "\n")+"\n"), 0o644); e != nil {
		return e
	}
	return nil
}

// --- ANSI dump ---

func ansiStyle(style tcell.Style) (string, bool) {
	fg, bg, attr := style.Decompose()
	var codes []string
	if attr&tcell.AttrBold != 0 {
		codes = append(codes, "1")
	}
	if attr&tcell.AttrDim != 0 {
		codes = append(codes, "2")
	}
	if attr&tcell.AttrItalic != 0 {
		codes = append(codes, "3")
	}
	if attr&tcell.AttrUnderline != 0 {
		codes = append(codes, "4")
	}
	if attr&tcell.AttrBlink != 0 {
		codes = append(codes, "5")
	}
	if attr&tcell.AttrReverse != 0 {
		codes = append(codes, "7")
	}
	if attr&tcell.AttrStrikeThrough != 0 {
		codes = append(codes, "9")
	}
	if fg != tcell.ColorDefault {
		h := fg.Hex()
		r := (h >> 16) & 0xff
		g := (h >> 8) & 0xff
		b := h & 0xff
		codes = append(codes, fmt.Sprintf("38;2;%d;%d;%d", r, g, b))
	}
	if bg != tcell.ColorDefault {
		h := bg.Hex()
		r := (h >> 16) & 0xff
		g := (h >> 8) & 0xff
		b := h & 0xff
		codes = append(codes, fmt.Sprintf("48;2;%d;%d;%d", r, g, b))
	}
	if len(codes) == 0 {
		return "", false
	}
	return "\x1b[0;" + strings.Join(codes, ";") + "m", true
}

func dumpANSI(w *workspace, path string) error {
	s := w.screen.(tcell.SimulationScreen)
	cols, rows := s.Size()
	var b strings.Builder
	b.WriteString("\x1b[?25l")
	last := "\x1b[0m"
	for y := 0; y < rows; y++ {
		if y > 0 {
			b.WriteString("\n")
		}
		for x := 0; x < cols; {
			mainc, comb, style, width := s.GetContent(x, y)
			seq, ok := ansiStyle(style)
			if ok && seq != last {
				b.WriteString(seq)
				last = seq
			}
			if mainc == 0 {
				b.WriteString(" ")
			} else {
				b.WriteString(string(mainc))
				b.WriteString(string(comb))
			}
			x += max(1, width)
		}
	}
	b.WriteString("\x1b[0m\x1b[?25h\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}
