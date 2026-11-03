package sandbox

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Execute the exact Python listing program against an incremental fake
// directory-FD filesystem. This runs on Windows too; real Linux NOFOLLOW and
// Docker mount behavior still require a dedicated runtime acceptance test.
func TestIMListingBoundsDirectoriesDepthAndOutput(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		python, err = exec.LookPath("python")
	}
	if err != nil {
		t.Skip("Python is unavailable")
	}
	const fixture = `import json,os,stat,sys,types
mode=sys.argv[1]
os.O_DIRECTORY=getattr(os,'O_DIRECTORY',65536)
os.O_NOFOLLOW=getattr(os,'O_NOFOLLOW',131072)
fds={};counter=0
def fake_open(name,flags,dir_fd=None):
 global counter
 if not (flags & os.O_NOFOLLOW): raise ValueError('nofollow missing')
 p='/' if name=='/' else fds[dir_fd].rstrip('/')+'/'+name
 counter+=1;fds[counter]=p;return counter
def fake_close(fd): pass
class Entry:
 def __init__(self,name,kind): self.name=name;self.kind=kind
 def stat(self,follow_symlinks=False):
  if follow_symlinks: raise ValueError('follow requested')
  return types.SimpleNamespace(
   st_mode={'dir':stat.S_IFDIR,'file':stat.S_IFREG,'link':stat.S_IFLNK}[self.kind]|0o755,
   st_size=3,st_mtime=0)
def entries(p):
 if p=='/': yield Entry('workspace','dir');return
 if p=='/workspace': yield Entry('output','dir');return
 if mode=='normal':
  if p=='/workspace/output':
   yield Entry('a.txt','file');yield Entry('link','link');yield Entry('sub','dir')
  elif p=='/workspace/output/sub': yield Entry('b.txt','file')
 elif mode=='many_dirs':
  if p=='/workspace/output':
   for n in range(1001): yield Entry('d'+str(n),'dir')
 elif mode=='deep':
  if p.count('/')<22: yield Entry('d','dir')
 elif mode=='output':
  if p=='/workspace/output':
   for n in range(1000): yield Entry('f'+str(n)+'x'*300,'file')
class Scan:
 def __init__(self,fd): self.fd=fd
 def __enter__(self): return entries(fds[self.fd])
 def __exit__(self,*a): pass
os.open=fake_open;os.close=fake_close;os.scandir=lambda fd:Scan(fd)
sys.argv=['listing','/workspace','/workspace/output']
`
	run := func(mode string) ([]byte, error) {
		return exec.Command(python, "-c", fixture+"\n"+imBoundedListProgram, mode).CombinedOutput()
	}
	normal, err := run("normal")
	require.NoError(t, err, string(normal))
	var entries []map[string]any
	require.NoError(t, json.Unmarshal(normal, &entries))
	require.Len(t, entries, 2)
	for _, tc := range []struct{ mode, want string }{
		{"many_dirs", "entry limit"}, {"deep", "depth limit"}, {"output", "output limit"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			out, err := run(tc.mode)
			require.Error(t, err)
			require.True(t, strings.Contains(string(out), tc.want), string(out))
		})
	}
	require.LessOrEqual(t, IMMaxListingTime.Seconds(), 10.0)
}
