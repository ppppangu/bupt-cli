package connect

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"path"
	"strconv"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

type BootstrapRequest struct {
	Profile          string        `json:"profile"`
	InstanceID       domain.ID     `json:"instance_id"`
	NodeID           domain.ID     `json:"node_id"`
	ExpectedUsername string        `json:"expected_username,omitempty"`
	PublicKeys       []string      `json:"public_keys"`
	RevokedKeys      []string      `json:"revoked_keys,omitempty"`
	SSHPort          int           `json:"ssh_port"`
	SkipTunnelKey    bool          `json:"skip_tunnel_key,omitempty"`
	Timeout          time.Duration `json:"-"`
}
type BootstrapResult struct {
	InstanceID              domain.ID `json:"instance_id"`
	NodeID                  domain.ID `json:"node_id"`
	Username                string    `json:"username"`
	Home                    string    `json:"home"`
	HostKey                 string    `json:"host_key"`
	TunnelPublicKey         string    `json:"tunnel_public_key,omitempty"`
	StateDir                string    `json:"state_dir"`
	TunnelKeyPath           string    `json:"tunnel_key_path,omitempty"`
	AddedKeys               int       `json:"added_keys"`
	RemovedKeys             int       `json:"removed_keys"`
	PreservedUnmanagedBlobs []string  `json:"preserved_unmanaged_blobs,omitempty"`
}
type TunnelConfig struct {
	RelayHost    string `json:"relay_host"`
	RelayPort    int    `json:"relay_port"`
	RelayUser    string `json:"relay_user"`
	RelayHostKey string `json:"relay_host_key"`
	Port         int    `json:"port"`
	TargetPort   int    `json:"target_port"`
	LeaseKey     string `json:"lease_key"`
}
type TunnelStatus struct {
	State   string `json:"state"`
	PID     int    `json:"pid,omitempty"`
	Session string `json:"session,omitempty"`
	Changed bool   `json:"changed,omitempty"`
}

func validateBootstrap(req BootstrapRequest) (BootstrapRequest, error) {
	if req.InstanceID == "" || req.NodeID == "" {
		return req, domain.E("INVALID_BOOTSTRAP", "instance and node IDs are required")
	}
	if strings.ContainsAny(string(req.InstanceID)+string(req.NodeID)+req.Profile, "\r\n\x00") {
		return req, domain.E("INVALID_BOOTSTRAP", "invalid bootstrap identity")
	}
	if len(req.PublicKeys) == 0 || len(req.PublicKeys) > 64 {
		return req, domain.E("INVALID_BOOTSTRAP", "between 1 and 64 public keys are required")
	}
	var err error
	req.PublicKeys, req.RevokedKeys, err = normalizeAuthorizedKeys(req.PublicKeys, req.RevokedKeys)
	if err != nil {
		return req, err
	}
	if req.SSHPort == 0 {
		req.SSHPort = 22
	}
	if req.SSHPort < 1 || req.SSHPort > 65535 {
		return req, domain.E("INVALID_BOOTSTRAP", "invalid SSH port")
	}
	return req, nil
}

// PrepareProgram returns inert Python source. The source contains only public
// enrollment data; it creates its tunnel private key on the instance itself.
func PrepareProgram(req BootstrapRequest) (string, error) {
	req, err := validateBootstrap(req)
	if err != nil {
		return "", err
	}
	data, _ := json.Marshal(req)
	return "import json,base64\nspec=json.loads(base64.b64decode(" + QuotePOSIX(base64.StdEncoding.EncodeToString(data)) + "))\n" + preparePython + "\nresult=prepare(spec)\n", nil
}

func Prepare(ctx context.Context, console io.ReadWriteCloser, req BootstrapRequest) (BootstrapResult, error) {
	var result BootstrapResult
	program, err := PrepareProgram(req)
	if err != nil {
		return result, err
	}
	if err = ExecuteConsole(ctx, console, program, &result, req.Timeout); err != nil {
		return result, err
	}
	if result.InstanceID != req.InstanceID || result.NodeID != req.NodeID {
		return result, domain.E("BOOTSTRAP_IDENTITY_MISMATCH", "console returned a different instance or node")
	}
	if req.ExpectedUsername != "" && result.Username != req.ExpectedUsername {
		return result, domain.E("BOOTSTRAP_IDENTITY_MISMATCH", "console user differs from the expected account")
	}
	if result.Username == "" || result.Home == "" || !path.IsAbs(result.Home) {
		return result, domain.E("BOOTSTRAP_INVALID_RESULT", "console did not report a valid user and home")
	}
	key, err := CanonicalPublicKey(result.HostKey)
	if err != nil {
		return result, domain.E("SSH_HOST_KEY_REQUIRED", "authenticated console did not return a valid host key")
	}
	result.HostKey = key
	if !req.SkipTunnelKey {
		key, err = CanonicalPublicKey(result.TunnelPublicKey)
		if err != nil {
			return result, domain.E("BOOTSTRAP_INVALID_RESULT", "console did not return a valid tunnel public key")
		}
		result.TunnelPublicKey = key
	}
	return result, nil
}

func validateResultLocation(r BootstrapResult) error {
	if !path.IsAbs(r.Home) || r.StateDir == "" || path.Clean(r.StateDir) != r.StateDir || !strings.HasPrefix(r.StateDir, path.Join(r.Home, ".local/share/bupt/connections")+"/") {
		return domain.E("INVALID_BOOTSTRAP", "invalid instance state directory")
	}
	if r.TunnelKeyPath != "" && r.TunnelKeyPath != path.Join(r.StateDir, "tunnel_ed25519") {
		return domain.E("INVALID_BOOTSTRAP", "invalid tunnel identity path")
	}
	return nil
}

func TunnelProgram(action string, r BootstrapResult, cfg TunnelConfig) (string, error) {
	if err := validateResultLocation(r); err != nil {
		return "", err
	}
	switch action {
	case "configure", "start", "stop", "status":
	default:
		return "", domain.E("INVALID_BOOTSTRAP", "invalid tunnel action")
	}
	if action == "configure" {
		if cfg.RelayPort == 0 {
			cfg.RelayPort = 22
		}
		if cfg.TargetPort == 0 {
			cfg.TargetPort = 22
		}
		if cfg.Port < 1024 || cfg.Port > 65535 || cfg.TargetPort < 1 || cfg.TargetPort > 65535 || cfg.RelayPort < 1 || cfg.RelayPort > 65535 {
			return "", domain.E("INVALID_RELAY", "invalid tunnel port")
		}
		if cfg.RelayHost == "" || strings.ContainsAny(cfg.RelayHost, "/@ \t\r\n\x00") || strings.HasPrefix(cfg.RelayUser, "-") || cfg.RelayUser == "" || strings.ContainsAny(cfg.RelayUser, "/@ \t\r\n\x00") {
			return "", domain.E("INVALID_RELAY", "invalid relay host or user")
		}
		if strings.Contains(cfg.RelayHost, ":") && net.ParseIP(strings.Trim(cfg.RelayHost, "[]")) == nil {
			return "", domain.E("INVALID_RELAY", "invalid IPv6 relay address")
		}
		if !leaseKeyPattern.MatchString(cfg.LeaseKey) {
			return "", domain.E("INVALID_RELAY", "a valid relay lease key is required")
		}
		key, err := CanonicalPublicKey(cfg.RelayHostKey)
		if err != nil {
			return "", domain.E("SSH_HOST_KEY_REQUIRED", "relay host public key is required")
		}
		cfg.RelayHostKey = key
	}
	data, _ := json.Marshal(map[string]any{"action": action, "instance": r, "config": cfg})
	return "import json,base64\nspec=json.loads(base64.b64decode(" + QuotePOSIX(base64.StdEncoding.EncodeToString(data)) + "))\n" + tunnelPython + "\nresult=tunnel_action(spec)\n", nil
}
func ConfigureTunnel(ctx context.Context, c io.ReadWriteCloser, r BootstrapResult, cfg TunnelConfig) (TunnelStatus, error) {
	return tunnelAction(ctx, c, "configure", r, cfg)
}
func StartTunnel(ctx context.Context, c io.ReadWriteCloser, r BootstrapResult) (TunnelStatus, error) {
	return tunnelAction(ctx, c, "start", r, TunnelConfig{})
}
func StopTunnel(ctx context.Context, c io.ReadWriteCloser, r BootstrapResult) (TunnelStatus, error) {
	return tunnelAction(ctx, c, "stop", r, TunnelConfig{})
}
func StatusTunnel(ctx context.Context, c io.ReadWriteCloser, r BootstrapResult) (TunnelStatus, error) {
	return tunnelAction(ctx, c, "status", r, TunnelConfig{})
}
func tunnelAction(ctx context.Context, c io.ReadWriteCloser, a string, r BootstrapResult, cfg TunnelConfig) (TunnelStatus, error) {
	var result TunnelStatus
	p, err := TunnelProgram(a, r, cfg)
	if err != nil {
		return result, err
	}
	err = ExecuteConsole(ctx, c, p, &result, 30*time.Second)
	return result, err
}

type consoleEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// ExecuteConsole runs a program on a newly authenticated terminal stream. Callers
// must serialize uses of the stream. A random, encoded marker avoids mistaking
// terminal command echo or old shell output for a successful result.
func ExecuteConsole(ctx context.Context, c io.ReadWriteCloser, program string, out any, timeout time.Duration) error {
	if c == nil {
		return domain.E("CONSOLE_UNAVAILABLE", "no authenticated console stream")
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return err
	}
	id := hex.EncodeToString(token)
	begin := "BUPT_RESULT_" + id + ":"
	end := ":BUPT_END_" + id
	var source strings.Builder
	source.WriteString("import json as _bj,base64 as _bb,sys as _bs\ntry:\n")
	for _, line := range strings.Split(strings.TrimSuffix(program, "\n"), "\n") {
		source.WriteString(" " + line + "\n")
	}
	source.WriteString(" _be={'ok':True,'result':result}\nexcept Exception as _bx:\n _be={'ok':False,'error':str(_bx)[:500]}\n")
	source.WriteString("_bs.stdout.write('\\n'+" + strconv.Quote(begin) + "+_bb.b64encode(_bj.dumps(_be,separators=(',',':')).encode()).decode()+" + strconv.Quote(end) + "+'\\n');_bs.stdout.flush()\n")
	encoded := base64.StdEncoding.EncodeToString([]byte(source.String()))
	// Keep individual terminal input lines below the usual canonical-mode limit.
	delimiter := "__BUPT_" + id + "__"
	var command strings.Builder
	command.WriteString("python3 - <<'" + delimiter + "'\nimport base64\nexec(base64.b64decode(\n")
	for len(encoded) > 0 {
		n := 900
		if len(encoded) < n {
			n = len(encoded)
		}
		command.WriteString("'" + encoded[:n] + "'\n")
		encoded = encoded[n:]
	}
	command.WriteString("))\n" + delimiter + "\n")
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	if _, err := io.WriteString(c, command.String()); err != nil {
		if ctx.Err() != nil {
			return domain.E("BOOTSTRAP_TIMEOUT", "console bootstrap timed out")
		}
		return err
	}
	var received []byte
	buffer := make([]byte, 8192)
	for {
		n, err := c.Read(buffer)
		if n > 0 {
			received = append(received, buffer[:n]...)
			if len(received) > 2<<20 {
				return domain.E("BOOTSTRAP_OUTPUT_LIMIT", "console output exceeded the bootstrap limit")
			}
			start := bytes.Index(received, []byte(begin))
			if start >= 0 {
				tail := received[start+len(begin):]
				finish := bytes.Index(tail, []byte(end))
				if finish >= 0 {
					payload, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(tail[:finish])))
					if decodeErr != nil {
						return domain.E("BOOTSTRAP_INVALID_RESULT", "invalid console result encoding")
					}
					var envelope consoleEnvelope
					if decodeErr = json.Unmarshal(payload, &envelope); decodeErr != nil {
						return domain.E("BOOTSTRAP_INVALID_RESULT", "invalid console result")
					}
					if !envelope.OK {
						return domain.E("BOOTSTRAP_FAILED", cleanDiagnostic(envelope.Error))
					}
					if out != nil {
						if decodeErr = json.Unmarshal(envelope.Result, out); decodeErr != nil {
							return domain.E("BOOTSTRAP_INVALID_RESULT", "console result has unexpected fields")
						}
					}
					return nil
				}
			}
			if bytes.Contains(received, []byte("python3: command not found")) || bytes.Contains(received, []byte("python3: not found")) {
				return domain.E("BOOTSTRAP_DEPENDENCY", "the instance requires Python 3 for connection bootstrap")
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return domain.E("BOOTSTRAP_TIMEOUT", "console bootstrap timed out; its result must be checked before retrying")
			}
			return domain.E("CONSOLE_DISCONNECTED", "console disconnected before a verified bootstrap result")
		}
	}
}
func cleanDiagnostic(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, s)
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}

const preparePython = authorizedKeysPython + `
import shutil,subprocess
def within(child,parent):
    try:child.resolve().relative_to(parent.resolve());return True
    except ValueError:return False
def discover_host_key(port):
    found=[]
    if shutil.which('ssh-keyscan'):
        probe=subprocess.run(['ssh-keyscan','-T','5','-p',str(port),'-t','ed25519,ecdsa,rsa','127.0.0.1'],capture_output=True,text=True,timeout=9)
        for line in probe.stdout.splitlines():
            parts=line.split()
            if len(parts)>=3 and parts[1].startswith(('ssh-','ecdsa-')):
                found.append(' '.join(parts[1:3]))
        for kind in ('ssh-ed25519','ecdsa-sha2-nistp256','ssh-rsa'):
            for key in found:
                if key.startswith(kind+' '): return key
    if port==22:
        for name in ('ssh_host_ed25519_key.pub','ssh_host_ecdsa_key.pub','ssh_host_rsa_key.pub'):
            p=pathlib.Path('/etc/ssh')/name
            if p.is_file():
                text=p.read_text().strip().split()
                if len(text)>=2:return ' '.join(text[:2])
    raise RuntimeError('Cannot determine instance SSH host public key')
def private_file(path):
    if path.is_symlink() or (path.exists() and (not path.is_file() or path.stat().st_uid!=os.getuid())):
        raise RuntimeError('Unexpected owner or type for a private connection file')
    if path.exists() and stat.S_IMODE(path.stat().st_mode)&0o077:
        raise RuntimeError('Private connection file permissions are too broad')
def prepare(spec):
    account=pwd.getpwuid(os.getuid())
    if spec.get('expected_username') and account.pw_name!=spec['expected_username']:
        raise RuntimeError('Console user does not match the authenticated platform node')
    home=pathlib.Path(account.pw_dir)
    if not home.is_dir() or not os.access(home,os.W_OK):raise RuntimeError('Instance home is not writable')
    identity=spec.get('profile','')+':'+spec['instance_id']+':'+spec['node_id']
    state=home/'.local/share/bupt/connections'/hashlib.sha256(identity.encode()).hexdigest()[:24]
    state.mkdir(parents=True,exist_ok=True,mode=0o700)
    if state.is_symlink() or not within(state,home):raise RuntimeError('Connection state must stay in the instance home')
    if state.stat().st_uid!=os.getuid():raise RuntimeError('Connection state is owned by another user')
    state.chmod(0o700)
    lockfd=os.open(str(state/'bootstrap.lock'),os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    with os.fdopen(lockfd,'a+') as task_lock:
        fcntl.flock(task_lock.fileno(),fcntl.LOCK_EX)
        return prepare_locked(spec,account,home,state)
def prepare_locked(spec,account,home,state):
    identity=spec.get('profile','')+':'+spec['instance_id']+':'+spec['node_id']
    enrollment=apply_authorized_keys(spec['public_keys'],spec.get('revoked_keys',[]),home,state)
    key=state/'tunnel_ed25519'
    public=''
    if not spec.get('skip_tunnel_key'):
        private_file(key)
        if not key.exists():
            if not shutil.which('ssh-keygen'):raise RuntimeError('ssh-keygen is required in the instance')
            subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-C','bupt-tunnel-'+identity,'-f',str(key)],check=True,stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
        key.chmod(0o600)
        pub=state/'tunnel_ed25519.pub'
        if not pub.exists():
            output=subprocess.run(['ssh-keygen','-y','-P','','-f',str(key)],check=True,capture_output=True,text=True,stdin=subprocess.DEVNULL).stdout
            pub.write_text(output);pub.chmod(0o600)
        public=' '.join(pub.read_text().strip().split()[:2])
    result={'instance_id':spec['instance_id'],'node_id':spec['node_id'],'username':account.pw_name,'home':str(home),'host_key':discover_host_key(spec['ssh_port']),'tunnel_public_key':public,'state_dir':str(state),'tunnel_key_path':str(key) if public else '', 'added_keys':enrollment['added_keys'],'removed_keys':enrollment['removed_keys'],'preserved_unmanaged_blobs':enrollment['preserved_unmanaged_blobs']}
    (state/'enrollment.json').write_text(json.dumps(result));(state/'enrollment.json').chmod(0o600)
    return result
`

const runnerPython = `import datetime,fcntl,json,os,pathlib,signal,subprocess,time
state=pathlib.Path(__file__).resolve().parent
lock=(state/'runner.lock').open('a+')
try:fcntl.flock(lock.fileno(),fcntl.LOCK_EX|fcntl.LOCK_NB)
except BlockingIOError:raise SystemExit(0)
config=json.loads((state/'tunnel.json').read_text())
def ticks(pid):
    return pathlib.Path('/proc/'+str(pid)+'/stat').read_text().rsplit(')',1)[1].split()[19]
pid={'pid':os.getpid(),'start':ticks(os.getpid()),'config_hash':config['hash']}
temp=state/('runner.pid.'+str(os.getpid()))
temp.write_text(json.dumps(pid));temp.replace(state/'runner.pid')
child=None
def stop(signum,frame):
    if child is not None and child.poll() is None:
        child.terminate()
        try:child.wait(timeout=8)
        except subprocess.TimeoutExpired:child.kill();child.wait()
    try:
        current=json.loads((state/'runner.pid').read_text())
        if current.get('pid')==os.getpid():(state/'runner.pid').unlink()
    except (OSError,ValueError):pass
    raise SystemExit(0)
for sig in (signal.SIGTERM,signal.SIGINT,signal.SIGHUP):signal.signal(sig,stop)
while True:
    child=subprocess.Popen(config['command'],stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,text=True)
    output,_=child.communicate()
    log=state/'tunnel.log'
    if log.exists() and log.stat().st_size>1048576:log.replace(state/'tunnel.previous.log')
    with log.open('a') as f:f.write(datetime.datetime.now(datetime.timezone.utc).isoformat()+' exit='+str(child.returncode)+'\n'+output[-8192:])
    if any(text in output for text in ('Permission denied','Host key verification failed','REMOTE HOST IDENTIFICATION HAS CHANGED')):
        stop(signal.SIGTERM,None)
    time.sleep(10)
`

var tunnelPython = "import os,pwd,pathlib,json,hashlib,subprocess,shlex,time,signal,fcntl\nRUNNER = " + strconv.Quote(runnerPython) + "\n" + `
def within(child,parent):
    try:child.resolve().relative_to(parent.resolve());return True
    except ValueError:return False
def valid_pid(state):
    try:
        value=json.loads((state/'runner.pid').read_text());pid=int(value['pid'])
        proc=pathlib.Path('/proc')/str(pid)
        if pid<=1 or proc.stat().st_uid!=os.getuid():return None
        parts=(proc/'stat').read_text().rsplit(')',1)[1].split()
        if parts[0]=='Z' or parts[19]!=value['start']:return None
        args=(proc/'cmdline').read_bytes().split(b'\0')
        if str(state/'run_tunnel.py').encode() not in args:return None
        return value
    except (OSError,ValueError,KeyError,IndexError):return None
def stop_owned(state):
    value=valid_pid(state)
    if value:
        os.kill(value['pid'],signal.SIGTERM)
        for _ in range(100):
            if not valid_pid(state):break
            time.sleep(.1)
        else:raise RuntimeError('The owned tunnel supervisor did not stop within ten seconds')
    return value is not None
def tunnel_action(spec):
    instance=spec['instance'];state=pathlib.Path(instance['state_dir'])
    home=pathlib.Path(pwd.getpwuid(os.getuid()).pw_dir)
    if instance['username']!=pwd.getpwuid(os.getuid()).pw_name or state.is_symlink() or not within(state,home/'.local/share/bupt/connections') or not within(state,home):
        raise RuntimeError('Tunnel state belongs to a different user or location')
    if not (state/'enrollment.json').is_file():raise RuntimeError('Prepare the instance before configuring its tunnel')
    if state.stat().st_uid!=os.getuid() or state.stat().st_mode&0o077:raise RuntimeError('Tunnel state permissions are unsafe')
    lockfd=os.open(str(state/'bootstrap.lock'),os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600)
    with os.fdopen(lockfd,'a+') as task_lock:
        fcntl.flock(task_lock.fileno(),fcntl.LOCK_EX)
        enrolled=json.loads((state/'enrollment.json').read_text())
        for field in ('instance_id','node_id','username','state_dir'):
            if enrolled.get(field)!=instance.get(field):raise RuntimeError('Tunnel enrollment identity does not match the instance')
        return tunnel_action_locked(spec,state)
def tunnel_action_locked(spec,state):
    if spec['action']=='configure':
        cfg=spec['config'];key=state/'tunnel_ed25519'
        if not key.is_file() or key.is_symlink() or key.stat().st_uid!=os.getuid():raise RuntimeError('Instance tunnel identity is unavailable')
        known=state/'relay_known_hosts'
        alias='bupt-relay-'+hashlib.sha256((cfg['relay_host']+':'+str(cfg['relay_port'])+cfg['relay_host_key']).encode()).hexdigest()[:20]
        known.write_text(alias+' '+cfg['relay_host_key']+'\n');known.chmod(0o600)
        target=cfg['relay_user']+'@'+cfg['relay_host'].strip('[]')
        kind=cfg['relay_host_key'].split()[0]
        algorithms='rsa-sha2-512,rsa-sha2-256' if kind=='ssh-rsa' else kind
        command=['ssh','-F','/dev/null','-nNT','-i',str(key),'-p',str(cfg['relay_port']),
            '-o','BatchMode=yes','-o','IdentitiesOnly=yes','-o','IdentityAgent=none',
            '-o','StrictHostKeyChecking=yes','-o','HostKeyAlias='+alias,'-o','HostKeyAlgorithms='+algorithms,
            '-o','UserKnownHostsFile='+str(known),'-o','UpdateHostKeys=no','-o','ForwardAgent=no','-o','ForwardX11=no',
            '-o','ControlMaster=no','-o','ControlPath=none','-o','ExitOnForwardFailure=yes',
            '-o','ConnectTimeout=10','-o','ServerAliveInterval=20','-o','ServerAliveCountMax=3',
            '-o','LogLevel=ERROR','-R','127.0.0.1:'+str(cfg['port'])+':127.0.0.1:'+str(cfg['target_port']),target]
        data={'command':command,'lease_key':cfg['lease_key']}
        data['hash']=hashlib.sha256(json.dumps(data,sort_keys=True).encode()).hexdigest()
        old={}
        try:old=json.loads((state/'tunnel.json').read_text())
        except (OSError,ValueError):pass
        temp=state/'tunnel.json.tmp';temp.write_text(json.dumps(data));temp.chmod(0o600);temp.replace(state/'tunnel.json')
        (state/'run_tunnel.py').write_text(RUNNER);(state/'run_tunnel.py').chmod(0o700)
        return {'state':'configured','changed':old.get('hash')!=data['hash']}
    if spec['action']=='stop':
        changed=stop_owned(state);return {'state':'stopped','changed':changed}
    live=valid_pid(state)
    if spec['action']=='status':return {'state':'running' if live else 'stopped','pid':live['pid'] if live else 0}
    data=json.loads((state/'tunnel.json').read_text())
    if live and live.get('config_hash')==data['hash']:return {'state':'running','pid':live['pid'],'changed':False}
    if live:stop_owned(state)
    with (state/'supervisor.log').open('ab') as output:
        subprocess.Popen(['python3',str(state/'run_tunnel.py')],stdin=subprocess.DEVNULL,stdout=output,stderr=subprocess.STDOUT,start_new_session=True,close_fds=True)
    for _ in range(40):
        live=valid_pid(state)
        if live:return {'state':'running','pid':live['pid'],'changed':True}
        time.sleep(.05)
    raise RuntimeError('Tunnel supervisor failed to start; inspect the task-owned supervisor.log')
`
