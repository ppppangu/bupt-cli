package control

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"bupt.local/cli/internal/connect"
	"bupt.local/cli/internal/domain"
	"github.com/google/uuid"
)

type JobRequest struct {
	Action    string            `json:"action"`
	RunID     string            `json:"run_id"`
	Command   []string          `json:"command,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	WorkDir   string            `json:"work_dir,omitempty"`
	Deadline  int64             `json:"deadline,omitempty"`
	StopGrace int64             `json:"stop_grace,omitempty"`
	ForceStop bool              `json:"force_stop,omitempty"`
}
type JobStatusInfo struct {
	State         string `json:"state"`
	PID           int    `json:"pid,omitempty"`
	SupervisorPID int    `json:"supervisor_pid,omitempty"`
	ExitCode      *int   `json:"exit_code,omitempty"`
	StartedAt     int64  `json:"started_at,omitempty"`
	FinishedAt    int64  `json:"finished_at,omitempty"`
	Reason        string `json:"reason,omitempty"`
}
type JobBackend interface {
	Invoke(context.Context, domain.Route, JobRequest) (JobStatusInfo, error)
}
type JobLogBackend interface {
	ReadLog(context.Context, domain.Route, string, string, int64, io.Writer) (int64, error)
}
type JobLogArchiveInfo struct {
	Bytes     int64     `json:"bytes"`
	Complete  bool      `json:"complete"`
	UpdatedAt time.Time `json:"updated_at"`
}

const jobLogChunkSize = 1 << 20

func (sshJobBackend) ReadLog(ctx context.Context, route domain.Route, runID, stream string, offset int64, w io.Writer) (int64, error) {
	rel, e := JobLogPath(runID, stream)
	if e != nil {
		return 0, e
	}
	connection, e := connect.OpenSFTP(ctx, route)
	if e != nil {
		return 0, e
	}
	defer connection.Close()
	home := route.Home
	if home == "" {
		home, e = connection.Client.Getwd()
		if e != nil {
			return 0, e
		}
	}
	f, e := connection.Client.Open(path.Join(home, rel))
	if e != nil {
		return 0, e
	}
	defer f.Close()
	if _, e = f.Seek(offset, io.SeekStart); e != nil {
		return 0, e
	}
	return io.Copy(w, f)
}
func (c *Controller) JobLogArchive(runID, stream string) (JobLogArchiveInfo, error) {
	if _, e := JobLogPath(runID, stream); e != nil {
		return JobLogArchiveInfo{}, e
	}
	var info JobLogArchiveInfo
	e := c.Store.Get("job_log_archives", runID+"/"+stream, &info)
	return info, e
}
func (c *Controller) ReadArchivedJobLog(ctx context.Context, runID, stream string, offset int64, w io.Writer) (int64, error) {
	if c.Vault == nil {
		return 0, domain.E("SECRET_STORE", "encrypted log store is unavailable")
	}
	info, e := c.JobLogArchive(runID, stream)
	if e != nil {
		return 0, e
	}
	if offset < 0 || offset > info.Bytes {
		return 0, domain.E("INVALID_OFFSET", "offset is outside the archived log")
	}
	written := int64(0)
	for offset < info.Bytes {
		if e = ctx.Err(); e != nil {
			return written, e
		}
		var data []byte
		chunk := offset / jobLogChunkSize
		if e = c.Vault.Get(jobLogChunkKey(runID, stream, chunk), &data); e != nil {
			return written, domain.E("LOG_ARCHIVE_INCOMPLETE", "an encrypted log chunk is unavailable")
		}
		start := offset % jobLogChunkSize
		end := min(int64(len(data)), info.Bytes-chunk*jobLogChunkSize)
		if start >= end {
			return written, domain.E("LOG_ARCHIVE_INCOMPLETE", "invalid encrypted log chunk")
		}
		n, e := w.Write(data[start:end])
		clear(data)
		written += int64(n)
		offset += int64(n)
		if e != nil {
			return written, e
		}
		if n == 0 {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}
func jobLogChunkKey(runID, stream string, chunk int64) string {
	return fmt.Sprintf("run/%s/logs/%s/%012d", runID, stream, chunk)
}

type encryptedLogWriter struct {
	c             *Controller
	runID, stream string
	info          JobLogArchiveInfo
	buffer        []byte
	chunk         int64
}

func (w *encryptedLogWriter) flush() error {
	if len(w.buffer) == 0 {
		return nil
	}
	if e := w.c.Vault.Put(jobLogChunkKey(w.runID, w.stream, w.chunk), w.buffer); e != nil {
		return domain.E("SECRET_STORE", "could not save encrypted job log")
	}
	w.info.Bytes = w.chunk*jobLogChunkSize + int64(len(w.buffer))
	w.info.UpdatedAt = time.Now().UTC()
	if e := w.c.Store.Put("job_log_archives", w.runID+"/"+w.stream, w.info); e != nil {
		return e
	}
	if len(w.buffer) == jobLogChunkSize {
		w.chunk++
		clear(w.buffer)
		w.buffer = nil
	}
	return nil
}
func (w *encryptedLogWriter) Write(p []byte) (int, error) {
	consumed := 0
	for len(p) > 0 {
		n := min(len(p), jobLogChunkSize-len(w.buffer))
		w.buffer = append(w.buffer, p[:n]...)
		p = p[n:]
		consumed += n
		if len(w.buffer) == jobLogChunkSize {
			if e := w.flush(); e != nil {
				return consumed, e
			}
		}
	}
	return consumed, nil
}
func (c *Controller) archiveJobLogs(ctx context.Context, r domain.Run) error {
	if c.Vault == nil {
		return domain.E("SECRET_STORE", "encrypted log store is unavailable")
	}
	route, e := c.CachedRoute(r.Profile, string(r.InstanceID), string(r.NodeID))
	if e != nil {
		return e
	}
	var source JobLogBackend = sshJobBackend{}
	if c.opts.JobBackend != nil {
		custom, ok := c.opts.JobBackend.(JobLogBackend)
		if !ok {
			return domain.E("LOG_ARCHIVE_UNAVAILABLE", "job backend does not expose log retrieval")
		}
		source = custom
	}
	for _, stream := range []string{"stdout", "stderr"} {
		info, _ := c.JobLogArchive(r.ID, stream)
		if info.Complete {
			continue
		}
		writer := &encryptedLogWriter{c: c, runID: r.ID, stream: stream, info: info, chunk: info.Bytes / jobLogChunkSize}
		if info.Bytes%jobLogChunkSize != 0 {
			if e = c.Vault.Get(jobLogChunkKey(r.ID, stream, writer.chunk), &writer.buffer); e != nil {
				return domain.E("LOG_ARCHIVE_INCOMPLETE", "cannot resume an encrypted log archive")
			}
			length := int(info.Bytes % jobLogChunkSize)
			if len(writer.buffer) < length {
				return domain.E("LOG_ARCHIVE_INCOMPLETE", "encrypted archive is shorter than its recorded offset")
			}
			writer.buffer = writer.buffer[:length]
		}
		_, copyErr := source.ReadLog(ctx, route, r.ID, stream, info.Bytes, writer)
		flushErr := writer.flush()
		clear(writer.buffer)
		if flushErr != nil {
			return flushErr
		}
		if copyErr != nil {
			return domain.E("LOG_ARCHIVE_PENDING", "job log archive is incomplete")
		}
		writer.info.Complete = r.ExitCode != nil
		writer.info.UpdatedAt = time.Now().UTC()
		if e = c.Store.Put("job_log_archives", r.ID+"/"+stream, writer.info); e != nil {
			return e
		}
	}
	return nil
}

type sshJobBackend struct{}

// JobDirectory is relative to the verified SSH user's home. Only a run UUID is
// accepted, so log requests cannot select arbitrary remote files.
func JobDirectory(runID string) (string, error) {
	if _, e := uuid.Parse(runID); e != nil {
		return "", domain.E("INVALID_RUN", "run ID must be a UUID")
	}
	return path.Join(".local/state/bupt/runs", runID), nil
}
func JobLogPath(runID, stream string) (string, error) {
	dir, e := JobDirectory(runID)
	if e != nil {
		return "", e
	}
	if stream == "" {
		stream = "stdout"
	}
	if stream != "stdout" && stream != "stderr" {
		return "", domain.E("INVALID_STREAM", "job stream must be stdout or stderr")
	}
	return path.Join(dir, stream+".log"), nil
}
func (sshJobBackend) Invoke(ctx context.Context, route domain.Route, request JobRequest) (JobStatusInfo, error) {
	var status JobStatusInfo
	if _, e := JobDirectory(request.RunID); e != nil {
		return status, e
	}
	data, e := json.Marshal(request)
	if e != nil {
		return status, e
	}
	var stdout boundedJobOutput
	code, e := connect.Run(ctx, route, connect.SessionOptions{Command: []string{"python3", "-c", jobProgram}}, bytes.NewReader(data), &stdout, io.Discard, nil, nil)
	clear(data)
	if e != nil {
		return status, domain.E("JOB_TRANSPORT", "could not verify the remote job state")
	}
	if code != 0 {
		return status, domain.E("JOB_REMOTE", "remote job supervisor rejected the request")
	}
	if e = json.Unmarshal(stdout.Bytes(), &status); e != nil {
		return status, domain.E("JOB_PROTOCOL", "remote job returned invalid status")
	}
	return status, nil
}

type boundedJobOutput struct{ bytes.Buffer }

func (b *boundedJobOutput) Write(p []byte) (int, error) {
	n := len(p)
	if b.Len()+n > 64<<10 {
		return 0, domain.E("JOB_PROTOCOL", "remote job response exceeds limit")
	}
	_, e := b.Buffer.Write(p)
	return n, e
}
func (c *Controller) invokeJob(ctx context.Context, run domain.Run, action string) (JobStatusInfo, error) {
	route, e := c.CachedRoute(run.Profile, string(run.InstanceID), string(run.NodeID))
	if e != nil {
		return JobStatusInfo{}, e
	}
	request := JobRequest{Action: action, RunID: run.ID, Deadline: run.Deadline.Unix(), ForceStop: run.Policy.ForceStop}
	grace, _ := time.ParseDuration(run.Policy.StopGrace)
	request.StopGrace = int64(grace.Seconds())
	if request.StopGrace < 60 {
		request.StopGrace = 60
	}
	if action == "launch" {
		var intent runIntent
		if c.Vault == nil || c.Vault.Get(runIntentKey(run.ID), &intent) != nil {
			return JobStatusInfo{}, domain.E("JOB_CREDENTIALS", "encrypted job intent is unavailable")
		}
		request.Command = intent.Command
		request.Env = intent.Env
		request.WorkDir = intent.WorkDir
	}
	backend := c.opts.JobBackend
	if backend == nil {
		backend = sshJobBackend{}
	}
	return backend.Invoke(ctx, route, request)
}
func (c *Controller) JobStatus(ctx context.Context, profile, target string) (JobStatusInfo, error) {
	r, e := c.GetRun(profile, target)
	if e != nil {
		return JobStatusInfo{}, e
	}
	if r.Kind != "job" {
		return JobStatusInfo{}, domain.E("NOT_A_JOB", "run is not a detached job")
	}
	return c.invokeJob(ctx, r, "status")
}

// The program and SSH command line are constant. argv/env arrive on stdin and
// stay in memory; only non-secret lifecycle status and application output reach
// disk. An exclusive launch marker makes uncertain restarts fail closed.
const jobProgram = `import json,os,pathlib,signal,subprocess,sys,time,uuid
q=json.load(sys.stdin); rid=str(uuid.UUID(q['run_id']))
os.umask(0o077)
root=pathlib.Path.home()/'.local/state/bupt/runs';root.mkdir(parents=True,mode=0o700,exist_ok=True)
d=root/rid
if d.is_symlink(): raise RuntimeError('unsafe directory')
d.mkdir(mode=0o700,exist_ok=True)
status=d/'status.json';control=d/'control.json'
def read(p,default):
 try:
  if p.is_symlink(): raise RuntimeError('unsafe file')
  return json.loads(p.read_text())
 except FileNotFoundError: return default
def atomic(p,obj):
 tmp=d/(p.name+'.'+str(os.getpid())+'.tmp')
 fd=os.open(str(tmp),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
 with os.fdopen(fd,'w') as f: json.dump(obj,f);f.flush();os.fsync(f.fileno())
 os.replace(str(tmp),str(p))
def show(obj): print(json.dumps(obj),flush=True)
def boot():
 try: return pathlib.Path('/proc/sys/kernel/random/boot_id').read_text().strip()
 except OSError:return ''
def ticks(pid):
 try:return pathlib.Path('/proc/'+str(pid)+'/stat').read_text().rsplit(')',1)[1].split()[19]
 except OSError:return ''
def present(s):
 if s.get('state') in ('running','launching'):
  p=s.get('supervisor_pid',0)
  if not p or s.get('boot_id')!=boot() or not s.get('supervisor_ticks') or ticks(p)!=s.get('supervisor_ticks'):s=dict(s,state='unknown',reason='supervisor_unavailable')
 return {k:v for k,v in s.items() if k in ('state','pid','supervisor_pid','exit_code','started_at','finished_at','reason')}
action=q['action']
if action in ('status','cancel','policy'):
 if action!='status':
  cfg=read(control,{})
  if action=='cancel':cfg['cancel']=True
  if action=='policy':cfg.update(deadline=q['deadline'],stop_grace=max(60,q.get('stop_grace',60)),force_stop=q.get('force_stop',False))
  atomic(control,cfg)
 show(present(read(status,{'state':'missing'})));sys.exit(0)
if action!='launch':raise RuntimeError('invalid action')
if not q.get('command') or q.get('deadline',0)<=time.time():raise RuntimeError('invalid bounded command')
try:lock=os.open(str(d/'launch.lock'),os.O_WRONLY|os.O_CREAT|os.O_EXCL,0o600)
except FileExistsError:
 show(present(read(status,{'state':'unknown','reason':'launch_marker_without_status'})));sys.exit(0)
os.write(lock,rid.encode());os.fsync(lock);os.close(lock)
atomic(control,{'deadline':q['deadline'],'stop_grace':max(60,q.get('stop_grace',60)),'force_stop':q.get('force_stop',False)})
pid=os.fork()
if pid:
 for _ in range(100):
  s=read(status,{})
  if s:show(present(s));sys.exit(0)
  time.sleep(.02)
 show({'state':'launching','supervisor_pid':pid});sys.exit(0)
os.setsid()
null=os.open('/dev/null',os.O_RDWR)
for fd in (0,1,2):os.dup2(null,fd)
if null>2:os.close(null)
os.closerange(3,65536)
s={'state':'launching','supervisor_pid':os.getpid(),'supervisor_ticks':ticks(os.getpid()),'boot_id':boot(),'started_at':int(time.time())}
atomic(status,s)
try:
 env=os.environ.copy();env.update(q.get('env') or {})
 out=os.open(str(d/'stdout.log'),os.O_WRONLY|os.O_CREAT|os.O_APPEND|os.O_NOFOLLOW,0o600)
 err=os.open(str(d/'stderr.log'),os.O_WRONLY|os.O_CREAT|os.O_APPEND|os.O_NOFOLLOW,0o600)
 child=subprocess.Popen(q['command'],cwd=q.get('work_dir') or str(pathlib.Path.home()),env=env,stdin=subprocess.DEVNULL,stdout=out,stderr=err,start_new_session=True)
 os.close(out);os.close(err);q.clear();env.clear()
 s.update(state='running',pid=child.pid,child_ticks=ticks(child.pid));atomic(status,s)
 terminating=0;reason='';killed=False
 while child.poll() is None:
  cfg=read(control,{})
  if not terminating and (cfg.get('cancel') or time.time()>=cfg.get('deadline',0)):
   terminating=time.time();reason='cancelled' if cfg.get('cancel') else 'deadline'
   try:os.killpg(child.pid,signal.SIGTERM)
   except ProcessLookupError:pass
  if terminating and not killed and cfg.get('force_stop') and time.time()-terminating>=max(60,cfg.get('stop_grace',60)):
   try:os.killpg(child.pid,signal.SIGKILL)
   except ProcessLookupError:pass
   killed=True
  time.sleep(.2)
 code=child.returncode
 if code<0:code=128-code
 s.update(state='complete' if code==0 else 'failed',exit_code=code,finished_at=int(time.time()),reason=reason);atomic(status,s)
except BaseException:
 s.update(state='failed',exit_code=125,finished_at=int(time.time()),reason='launcher_failed');atomic(status,s)
os._exit(0)
`

func validateJob(command []string, env map[string]string, workdir string) error {
	if len(command) == 0 {
		return domain.E("COMMAND_REQUIRED", "job command is required")
	}
	for _, arg := range command {
		if strings.ContainsRune(arg, 0) {
			return domain.E("INVALID_COMMAND", "command contains NUL")
		}
	}
	if strings.ContainsRune(workdir, 0) {
		return domain.E("INVALID_PATH", "working directory contains NUL")
	}
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return domain.E("INVALID_ENV", "invalid environment variable")
		}
	}
	return nil
}
