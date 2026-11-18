package connect

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"time"

	"bupt.local/cli/internal/domain"
)

// AuthorizedKeysResult reports only public enrollment metadata. Unmanaged
// matching keys remain authorized and are explicitly reported to the caller.
type AuthorizedKeysResult struct {
	AddedKeys               int      `json:"added_keys"`
	RemovedKeys             int      `json:"removed_keys"`
	Changed                 bool     `json:"changed"`
	BackupPath              string   `json:"backup_path,omitempty"`
	PreservedUnmanagedBlobs []string `json:"preserved_unmanaged_blobs,omitempty"`
}

func normalizeAuthorizedKeys(add, remove []string) ([]string, []string, error) {
	if len(add) > 64 || len(remove) > 1024 {
		return nil, nil, domain.E("INVALID_ENROLLMENT", "at most 64 active keys and 1024 revoked keys can be reconciled at once")
	}
	total := 0
	canonical := func(values []string) ([]string, map[string]bool, error) {
		out := make([]string, 0, len(values))
		seen := map[string]bool{}
		for _, value := range values {
			total += len(value)
			if total > 2<<20 {
				return nil, nil, domain.E("INVALID_ENROLLMENT", "public-key enrollment input exceeds its size limit")
			}
			key, err := CanonicalPublicKey(value)
			if err != nil {
				return nil, nil, err
			}
			blob := strings.Fields(key)[1]
			if !seen[blob] {
				out = append(out, key)
				seen[blob] = true
			}
		}
		return out, seen, nil
	}
	active, activeSet, err := canonical(add)
	if err != nil {
		return nil, nil, err
	}
	revoked, revokedSet, err := canonical(remove)
	if err != nil {
		return nil, nil, err
	}
	for blob := range revokedSet {
		if activeSet[blob] {
			return nil, nil, domain.E("KEY_STATE_CONFLICT", "the same public key cannot be active and revoked")
		}
	}
	return active, revoked, nil
}

type enrollmentOutput struct{ bytes.Buffer }

func (b *enrollmentOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4<<20 {
		return 0, domain.E("ENROLLMENT_PROTOCOL", "key enrollment output exceeded its limit")
	}
	return b.Buffer.Write(p)
}

// ApplyAuthorizedKeys reconciles only this tool's enrollments over an already
// verified SSH route. User data travels on stdin; the remote Python command is
// fixed and contains no keys, passwords, private identities, or arbitrary code.
func ApplyAuthorizedKeys(ctx context.Context, route domain.Route, add, remove []string) (AuthorizedKeysResult, error) {
	var result AuthorizedKeysResult
	active, revoked, err := normalizeAuthorizedKeys(add, remove)
	if err != nil {
		return result, err
	}
	if len(active) == 0 && len(revoked) == 0 {
		return result, nil
	}
	data, err := json.Marshal(map[string]any{"add": active, "remove": revoked, "expected_username": route.Username, "expected_home": route.Home})
	if err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var output enrollmentOutput
	code, err := Run(ctx, route, SessionOptions{Command: []string{"python3", "-c", authorizedKeysProgram}}, bytes.NewReader(data), &output, io.Discard, nil, nil)
	if err != nil {
		return result, err
	}
	var response struct {
		OK     bool                 `json:"ok"`
		Result AuthorizedKeysResult `json:"result"`
		Error  string               `json:"error"`
	}
	if err = json.Unmarshal(output.Bytes(), &response); err != nil {
		return result, domain.E("ENROLLMENT_PROTOCOL", "key enrollment did not return a verified result; retrying is safe")
	}
	if code != 0 || !response.OK {
		message := cleanDiagnostic(response.Error)
		if message == "" {
			message = "Python 3 key enrollment failed"
		}
		return result, domain.E("ENROLLMENT_FAILED", message)
	}
	return response.Result, nil
}

// Both authenticated-console bootstrap and native SSH reconciliation execute
// this same core. Unknown lines are retained byte-for-byte. Removal recognizes
// only the exact no-options lines emitted by this tool, including its original
// truncated fingerprint marker and the legacy bupt-device marker.
const authorizedKeysPython = `import os,pwd,pathlib,stat,fcntl,hashlib,base64,time,secrets,shlex
def ak_guard(st,kind,private=False):
    if st.st_uid!=os.getuid():raise RuntimeError('SSH enrollment path is owned by another user')
    if kind=='directory' and not stat.S_ISDIR(st.st_mode):raise RuntimeError('SSH enrollment path is not a directory')
    if kind=='file' and (not stat.S_ISREG(st.st_mode) or st.st_nlink!=1):raise RuntimeError('SSH enrollment file is not a private regular file')
    if stat.S_IMODE(st.st_mode)&(0o077 if private else 0o022):raise RuntimeError('SSH enrollment path permissions are unsafe')
def ak_lock(fd):
    deadline=time.monotonic()+15
    while True:
        try:fcntl.flock(fd,fcntl.LOCK_EX|fcntl.LOCK_NB);return
        except BlockingIOError:
            if time.monotonic()>=deadline:raise RuntimeError('SSH enrollment is busy; retry after the other update finishes')
            time.sleep(.05)
def ak_read(directory):
    try:fd=os.open('authorized_keys',os.O_RDONLY|os.O_NOFOLLOW,dir_fd=directory)
    except FileNotFoundError:return None,b''
    with os.fdopen(fd,'rb') as source:
        info=os.fstat(source.fileno());ak_guard(info,'file')
        if info.st_size>8*1024*1024:raise RuntimeError('Existing authorized_keys exceeds the enrollment limit')
        value=source.read(8*1024*1024+1)
        if len(value)>8*1024*1024:raise RuntimeError('Existing authorized_keys exceeds the enrollment limit')
        return info,value
def ak_signature(info):
    return None if info is None else (info.st_dev,info.st_ino,info.st_size,info.st_mtime_ns,info.st_ctime_ns)
def ak_entry(line):
    raw=line.strip()
    if not raw or raw.startswith(b'#'):return None
    try:parts=shlex.split(raw.decode('utf-8','surrogateescape'),comments=False,posix=True)
    except ValueError:return None
    for index in (0,1):
        if len(parts)>index+1 and parts[index].startswith(('ssh-','ecdsa-','sk-')):
            return (parts[index],parts[index+1])
    return None
def ak_marker(blob):
    digest=base64.b64encode(hashlib.sha256(base64.b64decode(blob,validate=True)).digest()).decode().rstrip('=')
    return ('bupt-client:SHA256:'+digest,'bupt-client:'+digest[:20])
def ak_owned(line,keytype,blob):
    parts=line.strip().split()
    if len(parts)!=3 or parts[0]!=keytype.encode() or parts[1]!=blob.encode():return False
    return parts[2] in tuple(s.encode() for s in ak_marker(blob))+ (b'bupt-device',)
def ak_new_file(directory,name,value):
    fd=os.open(name,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600,dir_fd=directory)
    with os.fdopen(fd,'wb') as target:
        target.write(value);target.flush();os.fsync(target.fileno())
def apply_authorized_keys(add,remove,home,backup_dir=None):
    active={k.split()[1]:k.split()[0] for k in add}
    revoked={k.split()[1]:k.split()[0] for k in remove}
    if active.keys()&revoked.keys():raise RuntimeError('The same key cannot be active and revoked')
    sshdir=home/'.ssh'
    sshdir.mkdir(mode=0o700,exist_ok=True)
    directory=os.open(str(sshdir),os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    try:
        ak_guard(os.fstat(directory),'directory')
        lockfd=os.open('.bupt-enrollment.lock',os.O_CREAT|os.O_RDWR|os.O_NOFOLLOW,0o600,dir_fd=directory)
        with os.fdopen(lockfd,'a+') as lock:
            ak_guard(os.fstat(lock.fileno()),'file',True);ak_lock(lock.fileno())
            before,old=ak_read(directory)
            kept=[];removed=0;preserved=set()
            for line in old.splitlines(keepends=True):
                entry=ak_entry(line)
                if entry and entry[1] in revoked and entry[0]==revoked[entry[1]]:
                    if ak_owned(line,*entry):removed+=1;continue
                    preserved.add(entry[1])
                kept.append(line)
            current=b''.join(kept)
            existing={entry[1] for line in kept for entry in [ak_entry(line)] if entry}
            additions=[]
            for blob,keytype in active.items():
                if blob not in existing:
                    additions.append((keytype+' '+blob+' '+ak_marker(blob)[0]+'\n').encode())
                    existing.add(blob)
            if additions:
                if current and not current.endswith(b'\n'):current+=b'\n'
                current+=b''.join(additions)
            result={'added_keys':len(additions),'removed_keys':removed,'changed':current!=old,'preserved_unmanaged_blobs':sorted(preserved)}
            if current==old:return result
            backupfd=None;temp=None
            try:
                if before is not None:
                    backuppath=backup_dir if backup_dir is not None else sshdir
                    backupfd=os.open(str(backuppath),os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
                    ak_guard(os.fstat(backupfd),'directory')
                    backup='authorized_keys.before-'+str(time.time_ns())+'-'+secrets.token_hex(8)
                    ak_new_file(backupfd,backup,old);os.fsync(backupfd)
                    result['backup_path']=str(backuppath/backup)
                temp='.bupt-authorized_keys-'+secrets.token_hex(16)
                ak_new_file(directory,temp,current)
                latest,latest_data=ak_read(directory)
                if ak_signature(latest)!=ak_signature(before) or latest_data!=old:
                    raise RuntimeError('authorized_keys changed outside the enrollment lock; retry without overwriting it')
                os.replace(temp,'authorized_keys',src_dir_fd=directory,dst_dir_fd=directory);temp=None;os.fsync(directory)
            finally:
                if temp is not None:
                    try:os.unlink(temp,dir_fd=directory)
                    except FileNotFoundError:pass
                if backupfd is not None:os.close(backupfd)
            return result
    finally:os.close(directory)
`

const authorizedKeysProgram = authorizedKeysPython + `
import json,sys
try:
    spec=json.load(sys.stdin)
    account=pwd.getpwuid(os.getuid());home=pathlib.Path(account.pw_dir)
    if spec.get('expected_username') and account.pw_name!=spec['expected_username']:raise RuntimeError('SSH account differs from the enrolled instance user')
    if spec.get('expected_home') and str(home)!=spec['expected_home']:raise RuntimeError('SSH home differs from the enrolled instance home')
    result=apply_authorized_keys(spec['add'],spec['remove'],home)
    response={'ok':True,'result':result}
except Exception as error:
    response={'ok':False,'error':str(error)[:500]}
print(json.dumps(response,separators=(',',':')),flush=True)
raise SystemExit(0 if response['ok'] else 1)
`
