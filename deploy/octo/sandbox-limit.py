#!/usr/bin/env python3
"""Docker AuthZ plugin: cap running containers on the dedicated Octo VM."""
import http.client,json,os,re,socket,socketserver,threading,urllib.parse
from http.server import BaseHTTPRequestHandler,HTTPServer

SOCKET='/run/docker/plugins/octo-sandbox-limit.sock'
STATE='/var/lib/octo-sandbox-limit/reservations.json'
DOCKER='/var/run/docker.sock'
LIMIT=4
START=re.compile(r'^/(?:v[0-9]+\.[0-9]+/)?containers/([^/]+)/start/?$')
lock=threading.Lock()

class UnixHTTP(http.client.HTTPConnection):
 def __init__(self,path): super().__init__('localhost',timeout=8);self.path=path
 def connect(self): self.sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);self.sock.settimeout(self.timeout);self.sock.connect(self.path)

def active_ids():
 c=UnixHTTP(DOCKER)
 try:
  c.request('GET','/v1.52/containers/json')
  r=c.getresponse()
  if r.status!=200: raise RuntimeError('Docker list unavailable')
  data=json.loads(r.read())
  if not isinstance(data,list):raise RuntimeError('Docker list invalid')
  return [x['Id'] for x in data]
 finally:c.close()

def write_state(state):
 os.makedirs(os.path.dirname(STATE),mode=0o700,exist_ok=True)
 tmp=STATE+'.tmp'
 with open(tmp,'w',encoding='utf8') as f:
  os.fchmod(f.fileno(),0o600);json.dump(state,f);f.flush();os.fsync(f.fileno())
 os.replace(tmp,STATE)

def load_state():
 boot=open('/proc/sys/kernel/random/boot_id',encoding='utf8').read().strip()
 try:
  with open(STATE,encoding='utf8') as f:s=json.load(f)
 except FileNotFoundError:s={'boot':boot,'pending':{}}
 if s.get('boot')!=boot:s={'boot':boot,'pending':{}}
 if not isinstance(s.get('pending'),dict):raise RuntimeError('invalid reservation state')
 write_state(s)
 return s
state=load_state()

def target(d):
 if d.get('RequestMethod')!='POST':return None
 m=START.fullmatch(urllib.parse.urlsplit(d.get('RequestUri',d.get('RequestURI',''))).path)
 return m.group(1) if m else None

def authorize(endpoint,d):
 if not d.get('RequestMethod') or not d.get('RequestUri',d.get('RequestURI')):return {'Allow':False,'Err':'Docker authorization metadata missing'}
 ident=target(d)
 if not ident:return {'Allow':True}
 if endpoint.endswith('AuthZRes'):
  with lock:
   if ident in state['pending']:
    del state['pending'][ident];write_state(state)
  return {'Allow':True}
 with lock:
  if ident in state['pending']:return {'Allow':False,'Msg':'Sandbox start already pending'}
  active=active_ids()
  pending=sum(not any(x.startswith(k) or k.startswith(x) for x in active) for k in state['pending'])
  if len(active)+pending>=LIMIT:return {'Allow':False,'Msg':'Octo sandbox capacity reached: maximum 4 running containers; end an idle session and retry'}
  state['pending'][ident]=True
  write_state(state)
  return {'Allow':True}

class Handler(BaseHTTPRequestHandler):
 def log_message(self,*args):pass
 def do_POST(self):
  try:
   length=int(self.headers.get('Content-Length','0'))
   if length>4*1024*1024:raise ValueError('request too large')
   data=json.loads(self.rfile.read(length) or b'{}')
   if self.path=='/Plugin.Activate':result={'Implements':['authz']}
   elif self.path in ('/AuthZPlugin.AuthZReq','/AuthZPlugin.AuthZRes'):result=authorize(self.path,data)
   else:result={'Err':'unknown endpoint'}
  except Exception as e:
   print('octo-sandbox-limit error: '+type(e).__name__,flush=True)
   result={'Allow':False,'Err':'Sandbox admission unavailable; contact administrator'}
  raw=json.dumps(result).encode();self.send_response(200);self.send_header('Content-Type','application/json');self.send_header('Content-Length',str(len(raw)));self.end_headers();self.wfile.write(raw)
class Server(socketserver.ThreadingMixIn,socketserver.UnixStreamServer):
 daemon_threads=True
 allow_reuse_address=True
if __name__=='__main__':
 os.makedirs(os.path.dirname(SOCKET),mode=0o755,exist_ok=True)
 try:os.unlink(SOCKET)
 except FileNotFoundError:pass
 with Server(SOCKET,Handler) as server:
  os.chmod(SOCKET,0o600);print('octo sandbox admission ready: max=4',flush=True);server.serve_forever()
