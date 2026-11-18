package install

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"bupt.local/cli/internal/config"
	"bupt.local/cli/internal/domain"
	"golang.org/x/sys/unix"
)

// WorkspaceAction is a local onboarding decision. Inspection does not install
// anything, contact the controller, or change service manager state.
type WorkspaceAction string

const (
	WorkspaceInitialize    WorkspaceAction = "initialize"
	WorkspaceResume        WorkspaceAction = "resume"
	WorkspaceProcess       WorkspaceAction = "process"
	WorkspaceResumeProcess WorkspaceAction = "resume-process"
)

func InspectWorkspace(cfg *config.Config) (WorkspaceAction, error) {
	if cfg == nil {
		return "", domain.E("CONFIG_REQUIRED", "请先选择工作区")
	}
	h, err := newHandler()
	if err != nil {
		return "", err
	}
	action, record, err := h.inspectWorkspace(cfg)
	if err == nil && action == WorkspaceResumeProcess {
		if err = previousProcessGone(record); err != nil {
			return "", err
		}
	}
	return action, err
}

// An installation can be reachable before device enrollment has finished.
// Only our own matching pending intent permits offering this action.
func PendingWorkspace(cfg *config.Config) WorkspaceAction {
	if cfg == nil {
		return ""
	}
	p, err := cfg.SelectedProfile()
	if err != nil || p.Controller != "" && p.Controller != "local" {
		return ""
	}
	raw, err := readRegular(filepath.Join(cfg.Paths.StateDir, "service.json"), 64<<10)
	var record serviceRecord
	if err != nil || json.Unmarshal(raw, &record) != nil || !record.SetupPending {
		return ""
	}
	h, err := newHandler()
	if err != nil {
		return ""
	}
	action, checked, err := h.inspectWorkspace(cfg)
	if err != nil || !checked.SetupPending {
		return ""
	}
	return action
}

func (h *handler) inspectWorkspace(cfg *config.Config) (WorkspaceAction, serviceRecord, error) {
	var record serviceRecord
	profile, err := cfg.SelectedProfile()
	if err != nil {
		return "", record, err
	}
	if profile.Controller != "local" && profile.Controller != "" {
		return "", record, fmt.Errorf("请连接此账号已配置的远程控制器")
	}
	if h.goos != "linux" && h.goos != "darwin" {
		return "", record, fmt.Errorf("请按此系统原有的方式启动控制器，再重新连接")
	}
	for _, dir := range []string{cfg.Paths.ConfigDir, cfg.Paths.DataDir, cfg.Paths.StateDir} {
		st, err := os.Lstat(dir)
		if !errors.Is(err, os.ErrNotExist) && (err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0) {
			return "", record, fmt.Errorf("工作区目录须为当前用户的私有目录，请检查后重新连接")
		}
	}
	// The service must load the same trusted profile after the terminal exits.
	// A temporary endpoint override cannot be made persistent by a TUI action.
	if st, err := os.Lstat(cfg.Paths.ConfigFile); err == nil && (!st.Mode().IsRegular() || st.Size() > 1<<20) {
		return "", record, fmt.Errorf("请核对工作区的本机配置文件后重新连接")
	}
	trusted, err := config.Load(config.Options{HomeDir: h.home, ConfigDir: cfg.Paths.ConfigDir, ConfigFile: cfg.Paths.ConfigFile, DataDir: cfg.Paths.DataDir, StateDir: cfg.Paths.StateDir, Profile: cfg.Selected, Env: map[string]string{}, NoProject: true, AllowMissingConfig: true})
	if err != nil || trusted.Profiles[cfg.Selected] != profile {
		return "", record, fmt.Errorf("当前连接与本机保存的配置不一致，请核对所选配置后重新连接")
	}
	data, err := readRegular(filepath.Join(cfg.Paths.StateDir, "service.json"), 64<<10)
	keyPath := filepath.Join(cfg.Paths.DataDir, "master.key")
	if err == nil {
		if json.Unmarshal(data, &record) != nil || record.Config != cfg.Paths.ConfigFile || !filepath.IsAbs(record.Executable) || !safeName.MatchString(record.Name) {
			return "", record, fmt.Errorf("本地服务记录与此工作区不匹配，请核对后重新连接")
		}
		if record.Manager == "process" {
			if record.Path == "" && slices.Equal(record.Arguments, agentArgs(cfg, keyPath)) && (!record.SetupPending || record.Executable == h.executable) {
				return WorkspaceResumeProcess, record, nil
			}
			return "", record, fmt.Errorf("此独立进程缺少匹配的启动参数，请按原方式启动后重新连接")
		}
		// Compare the installed unit to the exact service the installer would
		// generate for this account, including profile, data, state and socket.
		installed := *h
		installed.executable = record.Executable
		name := record.Name
		if h.goos == "darwin" {
			name = strings.TrimPrefix(name, "cn.edu.bupt.cli.")
		}
		if record.SetupPending && (name != "bupt-agent" || record.Executable != h.executable) {
			return "", record, fmt.Errorf("请使用最初安装的 bupt 继续初始化此工作区")
		}
		expected, unit := installed.userService(cfg, keyPath, name)
		expected.SetupPending = record.SetupPending
		if reflect.DeepEqual(record, expected) {
			onDisk, e := readRegular(record.Path, 128<<10)
			if e == nil && matchingWorkspaceUnit(record, onDisk, unit) || record.SetupPending && errors.Is(e, os.ErrNotExist) {
				return WorkspaceResume, record, nil
			}
		}
		return "", record, fmt.Errorf("这是自定义或已更改的服务，请按原方式启动后重新连接")
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", record, fmt.Errorf("无法读取本地服务记录，请检查工作区权限后重新连接")
	}
	for _, dir := range []string{cfg.Paths.StateDir, cfg.Paths.DataDir} {
		entries, e := os.ReadDir(dir)
		if e != nil && !errors.Is(e, os.ErrNotExist) || len(entries) > 0 {
			return "", record, fmt.Errorf("此工作区已有数据且缺少服务记录，请按原方式启动控制器后重新连接")
		}
	}
	if profile.ControllerSocket != cfg.Paths.Socket {
		return "", record, fmt.Errorf("当前使用自定义控制器套接字，请启动对应控制器后重新连接")
	}
	record, _ = h.userService(cfg, keyPath, "bupt-agent")
	if _, e := os.Lstat(record.Path); !errors.Is(e, os.ErrNotExist) {
		return "", record, fmt.Errorf("已有本机服务使用此名称，请连接它原有的工作区")
	}
	return WorkspaceInitialize, record, nil
}

func matchingWorkspaceUnit(r serviceRecord, onDisk, unit []byte) bool {
	return bytes.Equal(onDisk, unit) || r.Manager == "launchd" && samePlist(onDisk, unit)
}

func ensureWorkspaceUnit(r serviceRecord, unit []byte) error {
	if err := os.MkdirAll(filepath.Dir(r.Path), 0700); err != nil {
		return err
	}
	if err := writeNew(r.Path, unit, 0600); !errors.Is(err, os.ErrExist) {
		return err
	}
	// An interrupted first setup may already have published this exact unit.
	onDisk, err := readRegular(r.Path, 128<<10)
	if err != nil || !matchingWorkspaceUnit(r, onDisk, unit) {
		return fmt.Errorf("已有其他服务使用此名称，请核对原有控制器")
	}
	return nil
}

// plist editors may reindent the LaunchAgent without changing its contents.
// Retain the complete unit check, including the exact role of whitespace in
// argument values, rather than accepting a label or substring on its own.
func samePlist(a, b []byte) bool {
	tokens := func(data []byte) ([]string, error) {
		decoder := xml.NewDecoder(bytes.NewReader(data))
		var result []string
		var tag string
		for {
			token, err := decoder.Token()
			switch {
			case errors.Is(err, io.EOF):
				return result, nil
			case err != nil:
				return nil, err
			}
			switch v := token.(type) {
			case xml.StartElement:
				tag = v.Name.Local
				result = append(result, fmt.Sprint(v))
			case xml.EndElement:
				result = append(result, "/"+v.Name.Local)
				tag = ""
			case xml.CharData:
				if tag == "string" || tag == "key" || len(bytes.TrimSpace(v)) > 0 {
					result = append(result, string(v))
				}
			}
		}
	}
	left, err := tokens(a)
	right, e := tokens(b)
	return err == nil && e == nil && slices.Equal(left, right)
}

func (h *handler) startWorkspace(ctx context.Context, cfg *config.Config, params map[string]any, errOut io.Writer) (any, error) {
	profile, err := cfg.SelectedProfile()
	if err != nil || profile.Controller != "local" && profile.Controller != "" {
		return nil, fmt.Errorf("请选择已配置的本地控制器")
	}
	if paramBool(params, "dry_run", false) {
		action, _, err := h.inspectWorkspace(cfg)
		return map[string]any{"action": action, "dry_run": true}, err
	}
	// The service name belongs to the user, even with separate XDG roots.
	// A short, nonblocking advisory lock needs no persistent lock file.
	lock, err := os.OpenFile(h.home, os.O_RDONLY|unix.O_DIRECTORY, 0)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("另一个终端正在启动工作区，完成后可重新连接")
		}
		return nil, err
	}
	action, record, inspectionErr := h.inspectWorkspace(cfg)
	// Recheck the socket while holding the lock. Permission, protocol and
	// timeout errors never justify installing a controller.
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	probeErr := h.rpc(probe, cfg, "agent.status", nil, nil)
	cancel()
	var de *domain.Error
	if probeErr == nil && !record.SetupPending {
		return map[string]bool{"started": true}, nil
	}
	if probeErr != nil && (!errors.As(probeErr, &de) || de.Code != "AGENT_NOT_RUNNING") {
		return nil, probeErr
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if inspectionErr != nil {
		return nil, inspectionErr
	}
	online := probeErr == nil
	requested := WorkspaceAction(paramString(params, "action"))
	processFallback := h.goos == "linux" && action == WorkspaceInitialize && requested == WorkspaceProcess
	if requested != action && !processFallback {
		return nil, fmt.Errorf("工作区状态已改变，请重新连接")
	}
	if action == WorkspaceResumeProcess && !online {
		if err := previousProcessGone(record); err != nil {
			return nil, err
		}
	}
	if action == WorkspaceInitialize {
		err := h.checkVacantService(ctx, record)
		if processFallback {
			var d *domain.Error
			if !errors.As(err, &d) || d.Code != "USER_SERVICE_UNAVAILABLE" {
				return nil, fmt.Errorf("用户服务状态已改变，请重新检查工作区")
			}
			record.Manager, record.Path, record.Name = "process", "", "bupt-agent"
			record.Arguments = agentArgs(cfg, filepath.Join(cfg.Paths.DataDir, "master.key"))
		} else if err != nil {
			return nil, err
		}
		if err := ensurePrivateDir(cfg.Paths.StateDir); err != nil {
			return nil, err
		}
		// Publish our intent before creating any keys, configuration or unit.
		record.SetupPending = true
		if err := h.writeWorkspaceRecord(cfg, record, true); err != nil {
			return nil, err
		}
	}
	var publicKey any
	if record.SetupPending {
		prepared, err := h.setup(ctx, cfg, "workspace.start", map[string]any{"mode": "local", "install": false, "start": false}, nil, io.Discard, errOut)
		if err != nil {
			return nil, err
		}
		publicKey = prepared.(map[string]any)["public_key"]
		if record.Manager != "process" {
			_, contents := h.userService(cfg, filepath.Join(cfg.Paths.DataDir, "master.key"), "bupt-agent")
			if err := ensureWorkspaceUnit(record, contents); err != nil {
				return nil, err
			}
		}
	}
	if !online {
		if record.Manager == "process" {
			if err := h.launchWorkspaceProcess(cfg, &record); err != nil {
				return nil, err
			}
		} else if err := h.startWorkspaceUnit(ctx, record, record.SetupPending, errOut); err != nil {
			return nil, err
		}
		if err := h.waitReady(ctx, cfg); err != nil {
			return nil, err
		}
	}
	if record.SetupPending {
		if err := h.rpc(ctx, cfg, "key.import", map[string]any{"name": "device", "public_key": publicKey}, nil); err != nil {
			return nil, err
		}
		record.SetupPending = false
		if err := h.writeWorkspaceRecord(cfg, record, false); err != nil {
			return nil, err
		}
	}
	return map[string]bool{"started": true}, nil
}

func (h *handler) writeWorkspaceRecord(cfg *config.Config, r serviceRecord, newFile bool) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(cfg.Paths.StateDir, "service.json")
	if newFile {
		return writeNew(path, append(b, '\n'), 0600)
	}
	_, err = h.replaceFile(path, append(b, '\n'), 0600)
	return err
}

func previousProcessGone(r serviceRecord) error {
	if r.PID == 0 && r.SetupPending {
		return nil
	}
	if r.PID > 1 && errors.Is(syscall.Kill(r.PID, 0), syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("无法确认上次的控制器进程已退出，请检查后重新连接")
}

func (h *handler) launchWorkspaceProcess(cfg *config.Config, r *serviceRecord) error {
	// A crash between spawning a process and saving its PID must not permit
	// another launch with an unknown predecessor.
	oldPID := r.PID
	r.PID = -1
	if err := h.writeWorkspaceRecord(cfg, *r, false); err != nil {
		return err
	}
	pid, err := h.start(r.Executable, r.Arguments, filepath.Join(cfg.Paths.StateDir, "agent.log"))
	if err != nil {
		r.PID = oldPID
		return errors.Join(err, h.writeWorkspaceRecord(cfg, *r, false))
	}
	r.PID = pid
	return h.writeWorkspaceRecord(cfg, *r, false)
}

func (h *handler) checkVacantService(ctx context.Context, r serviceRecord) error {
	if h.goos == "darwin" {
		status, err := h.launchAgentStatus(ctx, r)
		if err != nil {
			return err
		}
		if status != "" {
			return fmt.Errorf("当前用户已加载同名控制器，请连接原有工作区")
		}
		return nil
	}
	var state, detail bytes.Buffer
	err := h.run(ctx, "systemctl", []string{"--user", "show", "--property=LoadState", "--value", r.Name + ".service"}, nil, &state, &detail)
	if strings.TrimSpace(state.String()) == "not-found" {
		return ctx.Err()
	}
	if err != nil {
		message := strings.ToLower(detail.String())
		busUnavailable := strings.Contains(message, "failed to connect") && strings.Contains(message, "bus") && !strings.Contains(message, "denied") && !strings.Contains(message, "not permitted")
		if errors.Is(err, exec.ErrNotFound) || busUnavailable || strings.Contains(message, "not been booted with systemd") {
			return domain.E("USER_SERVICE_UNAVAILABLE", "当前没有可用的用户服务，可以启动独立的本机后台进程")
		}
		return fmt.Errorf("无法核验当前用户的 systemd 服务，请检查用户会话后重连")
	}
	return fmt.Errorf("当前用户已配置同名控制器，请连接原有工作区")
}

func (h *handler) launchAgentStatus(ctx context.Context, r serviceRecord) (string, error) {
	var out, detail bytes.Buffer
	target := "gui/" + strconv.Itoa(h.uid) + "/" + r.Name
	err := h.run(ctx, "launchctl", []string{"print", target}, nil, &out, &detail)
	if err == nil {
		if strings.TrimSpace(out.String()) == "" {
			return "", fmt.Errorf("无法核验 launchd 服务的来源，请检查后重新连接")
		}
		return out.String(), nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	message := strings.ToLower(detail.String() + " " + err.Error())
	if strings.Contains(message, "could not find service") || strings.Contains(message, "service not found") {
		return "", nil
	}
	return "", fmt.Errorf("无法核验当前用户的 launchd 服务，请检查用户会话后重连")
}

func (h *handler) startWorkspaceUnit(ctx context.Context, r serviceRecord, firstUse bool, errOut io.Writer) error {
	if h.goos == "darwin" {
		gui := "gui/" + strconv.Itoa(h.uid)
		target := gui + "/" + r.Name
		status, err := h.launchAgentStatus(ctx, r)
		if err != nil {
			return err
		}
		if status != "" {
			for _, line := range strings.Split(status, "\n") {
				if strings.TrimSpace(line) == "path = "+r.Path {
					return h.run(ctx, "launchctl", []string{"kickstart", target}, nil, io.Discard, errOut)
				}
			}
			return fmt.Errorf("同名服务已从其他位置加载，请检查原服务后重新连接")
		}
		return h.run(ctx, "launchctl", []string{"bootstrap", gui, r.Path}, nil, io.Discard, errOut)
	}
	name := r.Name + ".service"
	if firstUse {
		if err := h.run(ctx, "systemctl", []string{"--user", "daemon-reload"}, nil, io.Discard, errOut); err != nil {
			return err
		}
	}
	var properties bytes.Buffer
	if err := h.run(ctx, "systemctl", []string{"--user", "show", "--all", "--property=FragmentPath,DropInPaths,NeedDaemonReload", name}, nil, &properties, errOut); err != nil {
		return err
	}
	lines := strings.Split(properties.String(), "\n")
	if !slices.Contains(lines, "FragmentPath="+r.Path) || !slices.Contains(lines, "DropInPaths=") || !slices.Contains(lines, "NeedDaemonReload=no") {
		return fmt.Errorf("当前 systemd 加载的控制器与工作区记录不一致，请检查后重新连接")
	}
	if firstUse {
		return h.run(ctx, "systemctl", []string{"--user", "enable", "--now", name}, nil, io.Discard, errOut)
	}
	return h.run(ctx, "systemctl", []string{"--user", "start", name}, nil, io.Discard, errOut)
}
