// Package config resolves non-secret settings with explicit provenance.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
	"github.com/BurntSushi/toml"
)

type Options struct {
	ConfigFile, ProjectFile, WorkDir, HomeDir string
	ConfigDir, DataDir, StateDir, Profile     string
	NoProject                                 bool
	// AllowMissingConfig is reserved for explicit initialization of a new file.
	AllowMissingConfig bool
	// A non-nil Env replaces the process environment, which makes CI and tests explicit.
	Env map[string]string
	// Overrides are canonical dotted keys, e.g. policy.cleanup or profiles.bupt.controller.
	Overrides map[string]any
}

type Paths struct {
	ConfigDir   string `json:"config_dir"`
	DataDir     string `json:"data_dir"`
	StateDir    string `json:"state_dir"`
	ConfigFile  string `json:"config_file"`
	ProjectFile string `json:"project_file,omitempty"`
	Socket      string `json:"socket"`
}

type Entry struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

type Config struct {
	Version        int                       `json:"version"`
	DefaultProfile string                    `json:"default_profile"`
	DefaultPreset  string                    `json:"default_preset,omitempty"`
	Selected       string                    `json:"selected_profile"`
	Profiles       map[string]domain.Profile `json:"profiles"`
	Presets        map[string]domain.Preset  `json:"presets"`
	Policy         domain.RunPolicy          `json:"policy"`
	Paths          Paths                     `json:"paths"`
	Sources        map[string]string         `json:"sources"`
	values         map[string]any
	snapshot       *resolutionSnapshot
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

func ValidProfileName(name string) bool { return nameRE.MatchString(name) }

func Load(o Options) (*Config, error) {
	env := o.Env
	if env == nil {
		env = processEnv()
	}
	home := o.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, err
		}
	}
	buptDir := filepath.Join(home, ".bupt")
	paths := Paths{
		ConfigDir: appDirectory(o.ConfigDir, env["XDG_CONFIG_HOME"], buptDir),
		DataDir:   appDirectory(o.DataDir, env["XDG_DATA_HOME"], filepath.Join(buptDir, "data")),
		StateDir:  appDirectory(o.StateDir, env["XDG_STATE_HOME"], filepath.Join(buptDir, "state")),
	}
	paths.ConfigDir = expandHome(paths.ConfigDir, home)
	paths.DataDir = expandHome(paths.DataDir, home)
	paths.StateDir = expandHome(paths.StateDir, home)
	paths.ConfigFile = expandHome(pick(o.ConfigFile, pick(env["BUPT_CONFIG"], filepath.Join(paths.ConfigDir, "config.toml"))), home)
	paths.Socket = filepath.Join(paths.StateDir, "agent.sock")
	values := defaults(paths)
	sources := make(map[string]string)
	markLeaves(values, "", "builtin", sources)
	global, err := readFile(paths.ConfigFile, (o.ConfigFile != "" || env["BUPT_CONFIG"] != "") && !o.AllowMissingConfig)
	if err != nil {
		return nil, err
	}
	if err = validateDocument(global, false); err != nil {
		return nil, fmt.Errorf("global config: %w", err)
	}
	merge(values, global, "", "global:"+paths.ConfigFile, sources)
	// All accounts inherit connection defaults; explicit empty gateway_url is preserved.
	if pm, ok := values["profiles"].(map[string]any); ok {
		base := defaults(paths)["profiles"].(map[string]any)["bupt"].(map[string]any)
		for name, p := range pm {
			if fields, ok := p.(map[string]any); ok {
				merged := cloneMap(base)
				merge(merged, fields, "", "", nil)
				pm[name] = merged
				flat := map[string]string{}
				markLeaves(merged, "profiles."+name, "builtin", flat)
				for key, src := range flat {
					if _, ok := sources[key]; !ok {
						sources[key] = src
					}
				}
			}
		}
	}

	var project map[string]any
	if !o.NoProject {
		paths.ProjectFile = expandHome(o.ProjectFile, home)
		if paths.ProjectFile == "" {
			work := o.WorkDir
			if work == "" {
				work, err = os.Getwd()
				if err != nil {
					return nil, err
				}
			}
			paths.ProjectFile = findProject(work)
		}
		if paths.ProjectFile != "" {
			project, err = readFile(paths.ProjectFile, true)
			if err != nil {
				return nil, err
			}
			if err = validateDocument(project, true); err != nil {
				return nil, fmt.Errorf("project config: %w", err)
			}
		}
	}
	selected, _ := values["default_profile"].(string)
	if v, ok := project["profile"].(string); ok {
		selected = v
	}
	if env["BUPT_PROFILE"] != "" {
		selected = env["BUPT_PROFILE"]
	}
	if v, ok := o.Overrides["default_profile"].(string); ok {
		selected = v
	}
	if v, ok := o.Overrides["profile"].(string); ok {
		selected = v
	}
	if o.Profile != "" {
		selected = o.Profile
	}
	snapshot := &resolutionSnapshot{base: cloneMap(values), sources: copySources(sources), project: cloneMap(project), overrides: cloneMap(o.Overrides), paths: paths, home: home, explicitProfile: o.Profile, envProfile: env["BUPT_PROFILE"], environment: map[string]any{}, environmentSources: map[string]string{}}
	// Materialize only supported environment values, binding endpoint overrides
	// to the originally selected account. Never retain the process environment.
	if err = applyEnvironment(snapshot.environment, selected, env, snapshot.environmentSources); err != nil {
		return nil, err
	}
	return snapshot.resolve(selected)
}

type resolutionSnapshot struct {
	base, project, overrides, environment map[string]any
	sources, environmentSources           map[string]string
	paths                                 Paths
	home, explicitProfile, envProfile     string
}

func copySources(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ForProfile resolves defaults and presets from captured input layers without
// filesystem or environment reads. Connection settings retain their original
// profile bindings; published maps and the returned maps do not alias inputs.
func (c *Config) ForProfile(name string) (*Config, error) {
	if name == "" {
		name = c.Selected
	}
	if c.snapshot == nil {
		return nil, domain.E("CONFIG_SNAPSHOT_REQUIRED", "profile resolution requires configuration loaded with config.Load")
	}
	result, err := c.snapshot.resolve(name)
	if err == nil {
		result.Sources["profile"] = "profile-selection"
	}
	return result, err
}
func (s *resolutionSnapshot) resolve(selected string) (*Config, error) {
	values, sources := cloneMap(s.base), copySources(s.sources)
	project, paths, home := cloneMap(s.project), s.paths, s.home
	o := Options{Overrides: cloneMap(s.overrides), Profile: s.explicitProfile}
	env := map[string]string{"BUPT_PROFILE": s.envProfile}
	var err error
	if !nameRE.MatchString(selected) {
		return nil, domain.E("INVALID_PROFILE", "profile name must be a safe identifier")
	}
	profiles, _ := values["profiles"].(map[string]any)
	profile, ok := profiles[selected].(map[string]any)
	if !ok {
		return nil, domain.E("PROFILE_NOT_FOUND", "profile "+selected+" is not configured")
	}
	// Profile policy and presets sit between global settings and the project.
	if p, ok := profile["policy"].(map[string]any); ok {
		merge(values, map[string]any{"policy": p}, "", "profile:"+selected, sources)
	}
	if p, ok := profile["presets"].(map[string]any); ok {
		merge(values, map[string]any{"presets": p}, "", "profile:"+selected, sources)
	}
	if value, ok := profile["default_preset"]; ok {
		merge(values, map[string]any{"default_preset": value}, "", "profile:"+selected, sources)
	}
	merge(values, project, "", "project:"+paths.ProjectFile, sources)
	merge(values, s.environment, "", "", nil)
	for key, source := range s.environmentSources {
		sources[key] = source
	}
	keys := make([]string, 0, len(o.Overrides))
	for k := range o.Overrides {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err = validateOverride(k, o.Overrides[k]); err != nil {
			return nil, err
		}
		value := o.Overrides[k]
		if incoming, ok := value.(map[string]any); ok {
			if old, exists := lookup(values, k); exists {
				if existing, ok := old.(map[string]any); ok {
					merged := cloneMap(existing)
					merge(merged, incoming, "", "", nil)
					value = merged
				}
			}
		}
		set(values, k, value)
		markValue(o.Overrides[k], k, "command-line", sources)
	}
	values["profile"] = selected
	sources["profile"] = "global-default"
	if project["profile"] != nil {
		sources["profile"] = "project:" + paths.ProjectFile
	}
	if env["BUPT_PROFILE"] != "" {
		sources["profile"] = "env:BUPT_PROFILE"
	}
	if o.Profile != "" || o.Overrides["profile"] != nil || o.Overrides["default_profile"] != nil {
		sources["profile"] = "command-line"
	}
	if err = validateDocument(values, false); err != nil {
		return nil, err
	}
	var result Config
	if err = decode(values, &result); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}
	result.Selected, result.Paths, result.Sources, result.values = selected, paths, sources, values
	result.snapshot = s
	if result.Version != 1 {
		return nil, domain.E("CONFIG_VERSION", "only configuration version 1 is supported")
	}
	for name, p := range result.Profiles {
		if !nameRE.MatchString(name) {
			return nil, domain.E("INVALID_PROFILE", "invalid profile identifier")
		}
		p.Name = name
		if p.Controller == "" {
			p.Controller = "local"
		}
		if p.ControllerSocket == "" {
			p.ControllerSocket = paths.Socket
		}
		p.ControllerIdentityFile = expandHome(p.ControllerIdentityFile, home)
		p.ControllerSocket = expandHome(p.ControllerSocket, home)
		p.Relay.IdentityFile = expandHome(p.Relay.IdentityFile, home)
		p.Relay.KnownHostsFile = expandHome(p.Relay.KnownHostsFile, home)
		if p.Relay.PortMin == 0 {
			p.Relay.PortMin = 24000
		}
		if p.Relay.PortMax == 0 {
			p.Relay.PortMax = 24999
		}
		if err = ValidateProfile(p); err != nil {
			return nil, fmt.Errorf("profile %s: %w", name, err)
		}
		result.Profiles[name] = p
	}
	if _, ok = result.Profiles[selected]; !ok {
		return nil, domain.E("PROFILE_NOT_FOUND", "selected profile is not configured")
	}
	if err = ValidatePolicy(result.Policy, false); err != nil {
		return nil, err
	}
	if result.Presets == nil {
		result.Presets = make(map[string]domain.Preset)
	}
	for name, p := range result.Presets {
		if !nameRE.MatchString(name) {
			return nil, domain.E("INVALID_PRESET", "invalid preset identifier")
		}
		p.Name = pick(p.Name, name)
		// Merge raw preset policy to preserve explicit false values.
		policyMap, _ := values["policy"].(map[string]any)
		combined := cloneMap(policyMap)
		if presetsMap, ok := values["presets"].(map[string]any); ok {
			if raw, ok := presetsMap[name].(map[string]any); ok {
				if policy, ok := raw["policy"].(map[string]any); ok {
					merge(combined, policy, "", "", nil)
				}
			}
		}
		if err = decode(combined, &p.Policy); err != nil {
			return nil, err
		}
		if err = ValidatePolicy(p.Policy, false); err != nil {
			return nil, fmt.Errorf("preset %s: %w", name, err)
		}
		result.Presets[name] = p
	}
	if result.DefaultPreset != "" {
		if _, ok := result.Presets[result.DefaultPreset]; !ok {
			return nil, domain.E("PRESET_NOT_FOUND", "default preset is not configured")
		}
	}
	return &result, nil
}

func defaults(paths Paths) map[string]any {
	return map[string]any{
		"version": int64(1), "default_profile": "bupt", "default_preset": "",
		"profiles": map[string]any{"bupt": map[string]any{
			"platform_url": "https://gpuhub.bupt.edu.cn", "gateway_url": "https://webvpn.bupt.edu.cn",
			"controller": "local", "controller_socket": paths.Socket,
			"relay": map[string]any{"port_min": int64(24000), "port_max": int64(24999)},
		}},
		"presets": map[string]any{},
		"policy":  map[string]any{"cleanup": "release", "stop_grace": "60s", "force_stop": true, "auto_renew": false},
	}
}

func (c *Config) SelectedProfile() (domain.Profile, error) {
	p, ok := c.Profiles[c.Selected]
	if !ok {
		return domain.Profile{}, domain.E("PROFILE_NOT_FOUND", "selected profile is not configured")
	}
	return p, nil
}
func (c *Config) Profile(name string) (domain.Profile, error) {
	if name == "" {
		return c.SelectedProfile()
	}
	p, ok := c.Profiles[name]
	if !ok {
		return domain.Profile{}, domain.E("PROFILE_NOT_FOUND", "profile "+name+" is not configured")
	}
	return p, nil
}
func (c *Config) Preset(name string) (domain.Preset, error) {
	p, ok := c.Presets[name]
	if !ok {
		return domain.Preset{}, domain.E("PRESET_NOT_FOUND", "preset "+name+" is not configured")
	}
	return p, nil
}
func (c *Config) Get(key string) (any, bool) { return lookup(c.values, key) }
func (c *Config) Explain() map[string]Entry {
	out := map[string]Entry{}
	for key, source := range c.Sources {
		if v, ok := lookup(c.values, key); ok {
			out[key] = Entry{Value: Redact(v), Source: source}
		}
	}
	return out
}

func ValidatePolicy(p domain.RunPolicy, requireLimit bool) error {
	if p.Cleanup != "none" && p.Cleanup != "stop" && p.Cleanup != "release" && p.Cleanup != "delete" {
		return domain.E("INVALID_POLICY", "cleanup must be none, stop, release, or delete")
	}
	if p.MaxRuntime != "" {
		d, e := time.ParseDuration(p.MaxRuntime)
		if e != nil || d <= 0 {
			return domain.E("INVALID_POLICY", "max_runtime must be a positive duration")
		}
	}
	if p.MaxRuntime != "" && !p.Until.IsZero() {
		return domain.E("INVALID_POLICY", "use max_runtime or until, not both")
	}
	if p.StopGrace != "" {
		d, e := time.ParseDuration(p.StopGrace)
		if e != nil || d < 0 {
			return domain.E("INVALID_POLICY", "stop_grace must be a nonnegative duration")
		}
	}
	// Templates may defer the limit to a preset or command. Validate the resolved
	// runtime policy with requireLimit=true immediately before allocating resources.
	if requireLimit && p.MaxRuntime == "" && p.Until.IsZero() {
		return domain.E("LIMIT_REQUIRED", "managed runs require an explicit run limit")
	}
	return nil
}

func ValidateProfile(p domain.Profile) error {
	if p.PortalURL != "" {
		if err := validateOrigin(p.PortalURL, "portal_url"); err != nil {
			return err
		}
	}
	if err := validateOrigin(p.PlatformURL, "platform_url"); err != nil {
		return err
	}
	if p.GatewayURL != "" {
		if err := validateOrigin(p.GatewayURL, "gateway_url"); err != nil {
			return err
		}
	}
	if p.Controller != "" && p.Controller != "local" {
		u, e := url.Parse(p.Controller)
		if e != nil || u.Scheme != "ssh" || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || !(u.Path == "" || u.Path == "/") {
			return domain.E("INVALID_CONTROLLER", "controller must be local or an ssh:// endpoint")
		}
		if u.User != nil {
			if _, ok := u.User.Password(); ok {
				return domain.E("SECRET_IN_CONFIG", "controller URLs must not contain passwords")
			}
		}
	}
	if p.HTTPProxy != "" {
		u, e := url.Parse(p.HTTPProxy)
		if e != nil || u.Hostname() == "" || !(u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "socks5" || u.Scheme == "socks5h") {
			return domain.E("INVALID_PROXY", "unsupported proxy URL")
		}
		if u.User != nil {
			return domain.E("SECRET_IN_CONFIG", "proxy credentials must not be stored in configuration URLs")
		}
	}
	if p.Relay.PortMin < 1024 || p.Relay.PortMax > 65535 || p.Relay.PortMin > p.Relay.PortMax {
		return domain.E("INVALID_PORT_RANGE", "relay port range must be within 1024..65535")
	}
	return nil
}
func validateOrigin(raw, key string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !(u.Path == "" || u.Path == "/") {
		return domain.E("INVALID_ORIGIN", key+" must be a credential-free origin")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !(u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
			return domain.E("INVALID_ORIGIN", key+" must use HTTPS (HTTP is allowed only for loopback tests)")
		}
	}
	return nil
}

func readFile(path string, required bool) (map[string]any, error) {
	m := map[string]any{}
	b, e := os.ReadFile(path)
	if errors.Is(e, fs.ErrNotExist) && !required {
		return m, nil
	}
	if e != nil {
		return nil, e
	}
	if len(b) > 4<<20 {
		return nil, domain.E("CONFIG_TOO_LARGE", "configuration exceeds 4 MiB")
	}
	if _, e = toml.Decode(string(b), &m); e != nil {
		// Decoder diagnostics can quote the offending line, including a misplaced secret.
		return nil, domain.E("INVALID_TOML", "could not parse "+path+"; check TOML syntax")
	}
	return m, nil
}
func validateDocument(m map[string]any, project bool) error {
	if err := rejectSecrets(m, ""); err != nil {
		return err
	}
	allowed := map[string]bool{"version": true, "default_profile": true, "default_preset": true, "profile": true, "profiles": true, "presets": true, "policy": true}
	for k := range m {
		if !allowed[k] {
			return domain.E("UNKNOWN_CONFIG_KEY", "unknown top-level configuration key "+k)
		}
	}
	if project {
		for k := range m {
			if k != "version" && k != "profile" && k != "presets" && k != "policy" && k != "default_preset" {
				return domain.E("UNTRUSTED_PROJECT_OVERRIDE", "project files cannot change trusted profiles or endpoints")
			}
		}
	}
	if value, ok := m["default_preset"]; ok {
		if name, valid := value.(string); !valid || name != "" && !nameRE.MatchString(name) {
			return domain.E("INVALID_PRESET", "default_preset must be an empty string or a valid preset name")
		}
	}
	if p, ok := m["policy"]; ok {
		if err := validateFields(p, reflect.TypeOf(domain.RunPolicy{}), "policy", nil); err != nil {
			return err
		}
	}
	if p, ok := m["presets"].(map[string]any); ok {
		for name, v := range p {
			if err := validateFields(v, reflect.TypeOf(domain.Preset{}), "presets."+name, nil); err != nil {
				return err
			}
			if fields, ok := v.(map[string]any); ok {
				if policy, ok := fields["policy"]; ok {
					if err := validateFields(policy, reflect.TypeOf(domain.RunPolicy{}), "presets."+name+".policy", nil); err != nil {
						return err
					}
				}
				if presets, ok := fields["presets"]; ok {
					if err := validateDocument(map[string]any{"presets": presets}, false); err != nil {
						return err
					}
				}
			}
		}
	}
	if p, ok := m["profiles"].(map[string]any); ok {
		for name, v := range p {
			if err := validateFields(v, reflect.TypeOf(domain.Profile{}), "profiles."+name, map[string]bool{"policy": true, "presets": true, "default_preset": true}); err != nil {
				return err
			}
			if fields, ok := v.(map[string]any); ok {
				for _, key := range []string{"presets", "default_preset"} {
					if value, ok := fields[key]; ok {
						if err := validateDocument(map[string]any{key: value}, false); err != nil {
							return err
						}
					}
				}
				if relay, ok := fields["relay"]; ok {
					if err := validateFields(relay, reflect.TypeOf(domain.RelayConfig{}), "profiles."+name+".relay", nil); err != nil {
						return err
					}
				}
				if policy, ok := fields["policy"]; ok {
					if err := validateFields(policy, reflect.TypeOf(domain.RunPolicy{}), "profiles."+name+".policy", nil); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
func validateFields(v any, t reflect.Type, path string, extra map[string]bool) error {
	m, ok := v.(map[string]any)
	if !ok {
		return domain.E("INVALID_CONFIG", path+" must be a table")
	}
	allowed := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag != "" && tag != "-" {
			allowed[tag] = true
		}
	}
	for k := range m {
		if !allowed[k] && !extra[k] {
			return domain.E("UNKNOWN_CONFIG_KEY", "unknown configuration key "+path+"."+k)
		}
	}
	return nil
}
func sensitive(key string) bool {
	k := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for _, s := range []string{"password", "passphrase", "secret", "cookie", "token", "authorization", "private_key", "privatekey", "master_key"} {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}
func rejectSecrets(v any, path string) error {
	switch m := v.(type) {
	case map[string]any:
		for k, x := range m {
			if sensitive(k) {
				return domain.E("SECRET_IN_CONFIG", "credentials must use the encrypted store, not configuration key "+join(path, k))
			}
			if err := rejectSecrets(x, join(path, k)); err != nil {
				return err
			}
		}
	case []any:
		for _, x := range m {
			if err := rejectSecrets(x, path); err != nil {
				return err
			}
		}
	case string:
		if u, e := url.Parse(m); e == nil && u.Host != "" {
			if u.User != nil {
				if _, ok := u.User.Password(); ok {
					return domain.E("SECRET_IN_CONFIG", "configuration URLs must not contain passwords")
				}
			}
			for key := range u.Query() {
				if sensitive(key) {
					return domain.E("SECRET_IN_CONFIG", "configuration URLs must not contain credentials")
				}
			}
		}
	default:
		rv := reflect.ValueOf(v)
		if rv.IsValid() && (rv.Kind() == reflect.Struct || rv.Kind() == reflect.Map || rv.Kind() == reflect.Slice || rv.Kind() == reflect.Pointer) {
			if _, ok := v.(time.Time); !ok {
				var normalized any
				b, e := json.Marshal(v)
				if e != nil {
					return e
				}
				if e = json.Unmarshal(b, &normalized); e != nil {
					return e
				}
				return rejectSecrets(normalized, path)
			}
		}
	}
	return nil
}
func Redact(v any) any {
	switch x := v.(type) {
	case map[string]any:
		o := map[string]any{}
		for k, v := range x {
			if sensitive(k) {
				o[k] = "[REDACTED]"
			} else {
				o[k] = Redact(v)
			}
		}
		return o
	case []any:
		o := make([]any, len(x))
		for i, v := range x {
			o[i] = Redact(v)
		}
		return o
	case string:
		if u, e := url.Parse(x); e == nil && u.User != nil {
			u.User = url.User("[REDACTED]")
			return u.String()
		}
		return x
	default:
		return v
	}
}
func validateOverride(k string, v any) error {
	parts := strings.Split(k, ".")
	for _, p := range parts {
		if p == "" || sensitive(p) {
			return domain.E("SECRET_IN_CONFIG", "invalid or secret configuration override")
		}
	}
	if parts[0] != "policy" && parts[0] != "presets" && parts[0] != "profiles" && k != "profile" && k != "default_profile" && k != "default_preset" && k != "version" {
		return domain.E("UNKNOWN_CONFIG_KEY", "unknown override "+k)
	}
	return rejectSecrets(v, k)
}
func applyEnvironment(v map[string]any, p string, env map[string]string, sources map[string]string) error {
	mapping := map[string]string{"BUPT_PLATFORM_URL": "profiles." + p + ".platform_url", "BUPT_GATEWAY_URL": "profiles." + p + ".gateway_url", "BUPT_USERNAME": "profiles." + p + ".username", "BUPT_CONTROLLER": "profiles." + p + ".controller", "BUPT_CONTROLLER_SOCKET": "profiles." + p + ".controller_socket", "BUPT_CONTROLLER_IDENTITY_FILE": "profiles." + p + ".controller_identity_file", "BUPT_CONTROLLER_HOST_KEY": "profiles." + p + ".controller_host_key", "BUPT_HTTP_PROXY": "profiles." + p + ".http_proxy", "BUPT_RELAY_ADDRESS": "profiles." + p + ".relay.address", "BUPT_RELAY_USER": "profiles." + p + ".relay.user", "BUPT_RELAY_IDENTITY_FILE": "profiles." + p + ".relay.identity_file", "BUPT_RELAY_HOST_KEY": "profiles." + p + ".relay.host_key", "BUPT_RELAY_INSTANCE_ADDRESS": "profiles." + p + ".relay.instance_address", "BUPT_MAX_RUNTIME": "policy.max_runtime", "BUPT_UNTIL": "policy.until", "BUPT_CLEANUP": "policy.cleanup", "BUPT_STOP_GRACE": "policy.stop_grace", "BUPT_AUTO_RENEW": "policy.auto_renew", "BUPT_FORCE_STOP": "policy.force_stop"}
	mapping["BUPT_PRESET"] = "default_preset"
	for key, path := range mapping {
		s, ok := env[key]
		if !ok {
			continue
		}
		var value any = s
		if key == "BUPT_AUTO_RENEW" || key == "BUPT_FORCE_STOP" {
			b, e := strconv.ParseBool(s)
			if e != nil {
				return domain.E("INVALID_ENV", key+" must be a boolean")
			}
			value = b
		}
		if key == "BUPT_UNTIL" {
			t, e := time.Parse(time.RFC3339, s)
			if e != nil {
				return domain.E("INVALID_ENV", key+" must be RFC3339")
			}
			value = t
		}
		set(v, path, value)
		sources[path] = "env:" + key
	}
	return nil
}
func processEnv() map[string]string {
	m := map[string]string{}
	for _, s := range os.Environ() {
		k, v, _ := strings.Cut(s, "=")
		m[k] = v
	}
	return m
}

// Each explicit or XDG directory overrides only its own part of the personal layout.
func appDirectory(explicit, xdgHome, personal string) string {
	if explicit != "" {
		return explicit
	}
	if xdgHome != "" {
		return filepath.Join(xdgHome, "bupt")
	}
	return personal
}
func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}
func expandHome(s, home string) string {
	if s == "~" {
		return home
	}
	if strings.HasPrefix(s, "~/") {
		return filepath.Join(home, s[2:])
	}
	return s
}
func findProject(work string) string {
	p, e := filepath.Abs(work)
	if e != nil {
		return ""
	}
	for {
		f := filepath.Join(p, ".bupt.toml")
		if st, e := os.Stat(f); e == nil && !st.IsDir() {
			return f
		}
		next := filepath.Dir(p)
		if next == p {
			return ""
		}
		p = next
	}
}
func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return cloneLayerValue(reflect.ValueOf(m)).Interface().(map[string]any)
}
func cloneLayerValue(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.New(v.Type()).Elem()
		out.Set(cloneLayerValue(v.Elem()))
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneLayerValue(iter.Value()))
		}
		return out
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		for i := 0; i < v.Len(); i++ {
			out.Index(i).Set(cloneLayerValue(v.Index(i)))
		}
		return out
	default:
		return v
	}
}

func merge(dst, src map[string]any, prefix, source string, sources map[string]string) {
	for k, v := range src {
		path := join(prefix, k)
		if m, ok := v.(map[string]any); ok {
			d, ok := dst[k].(map[string]any)
			if !ok {
				d = map[string]any{}
				dst[k] = d
			}
			merge(d, m, path, source, sources)
		} else {
			dst[k] = v
			if sources != nil {
				sources[path] = source
			}
		}
	}
}
func markLeaves(m map[string]any, prefix, source string, out map[string]string) {
	for k, v := range m {
		markValue(v, join(prefix, k), source, out)
	}
}
func markValue(v any, path, source string, out map[string]string) {
	if m, ok := v.(map[string]any); ok {
		markLeaves(m, path, source, out)
	} else {
		out[path] = source
	}
}
func set(m map[string]any, path string, v any) {
	parts := strings.Split(path, ".")
	for _, k := range parts[:len(parts)-1] {
		n, ok := m[k].(map[string]any)
		if !ok {
			n = map[string]any{}
			m[k] = n
		}
		m = n
	}
	m[parts[len(parts)-1]] = v
}
func lookup(m map[string]any, path string) (any, bool) {
	var v any = m
	for _, k := range strings.Split(path, ".") {
		x, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		v, ok = x[k]
		if !ok {
			return nil, false
		}
	}
	return v, true
}
func decode(v any, out any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}

// SetFile edits one non-secret global setting atomically. It never follows a config symlink.
func SetFile(path, key string, value any) error {
	return editFile(path, key, value, false)
}

// UnsetFile removes one local setting; inherited values can become visible again.
func UnsetFile(path, key string) error { return editFile(path, key, nil, true) }
func editFile(path, key string, value any, remove bool) error {
	if err := validateOverride(key, value); err != nil {
		return err
	}
	if st, e := os.Lstat(path); e == nil && st.Mode()&os.ModeSymlink != 0 {
		return domain.E("CONFIG_SYMLINK", "edit the configuration symlink target explicitly")
	}
	m, e := readFile(path, false)
	if e != nil {
		return e
	}
	if remove {
		parts := strings.Split(key, ".")
		parent := m
		for _, part := range parts[:len(parts)-1] {
			next, ok := parent[part].(map[string]any)
			if !ok {
				return domain.E("CONFIG_KEY_NOT_FOUND", "setting is not present in this file")
			}
			parent = next
		}
		if _, ok := parent[parts[len(parts)-1]]; !ok {
			return domain.E("CONFIG_KEY_NOT_FOUND", "setting is not present in this file")
		}
		delete(parent, parts[len(parts)-1])
	} else {
		set(m, key, value)
	}
	if err := validateDocument(m, false); err != nil {
		return err
	}
	var b strings.Builder
	if err := toml.NewEncoder(&b).Encode(m); err != nil {
		return err
	}
	if e = os.MkdirAll(filepath.Dir(path), 0o700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".bupt-config-*")
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = f.Chmod(0o600); e == nil {
		_, e = f.WriteString(b.String())
	}
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(name, path)
}
