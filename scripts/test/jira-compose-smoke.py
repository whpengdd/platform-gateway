#!/usr/bin/env python3
"""Exercise self/install/149 with disposable JSON credentials and actual containers."""
import base64
from contextlib import contextmanager
import http.server
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
import urllib.error
import uuid
repo = pathlib.Path(__file__).resolve().parents[2]
context = sys.argv[1] if len(sys.argv) > 1 else '-'
image = sys.argv[2] if len(sys.argv) > 2 else 'platform-gateway:config-smoke'
docker = ['docker'] + (['--context', context] if context != '-' else [])

def get(port, token):
    req=urllib.request.Request('http://127.0.0.1:'+str(port)+'/v1/jira/projects',headers={'Authorization':'Bearer '+token})
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req,timeout=2) as response: return response.status
    except urllib.error.HTTPError as err: return err.code

@contextmanager
def local_jira(auth):
    authorization = ('Bearer '+auth['token']) if auth['type']=='bearer' else 'Basic '+base64.b64encode((auth['username']+':'+auth['password']).encode()).decode()
    state = {'account_calls':0, 'queries':[]}
    lock = threading.Lock()
    class Jira(http.server.BaseHTTPRequestHandler):
        def log_message(self, *args): pass
        def send_json(self, value):
            data=json.dumps(value).encode()
            self.send_response(200); self.send_header('Content-Type','application/json')
            self.send_header('Content-Length',str(len(data))); self.end_headers(); self.wfile.write(data)
        def do_GET(self):
            assert self.headers.get('Authorization')==authorization, 'incorrect disposable upstream authentication'
            assert self.path=='/jira/rest/api/2/myself'
            with lock: state['account_calls']+=1
            self.send_json({'name':'mock-bot','displayName':'Mock Bot','timeZone':'Asia/Shanghai'})
        def do_POST(self):
            assert self.headers.get('Authorization')==authorization, 'incorrect disposable upstream authentication'
            assert self.path=='/jira/rest/api/2/search'
            body=json.loads(self.rfile.read(int(self.headers.get('Content-Length','0'))))
            assert '(project = "CS")' in body['jql'] and '(labels = zammad)' in body['jql']
            with lock: state['queries'].append(body['jql'])
            self.send_json({'startAt':0,'total':1,'issues':[{'id':'1','key':'CS-1','fields':{'project':{'key':'CS'},'summary':'mock-only'}}]})
    server=http.server.ThreadingHTTPServer(('0.0.0.0',0),Jira)
    thread=threading.Thread(target=server.serve_forever,daemon=True)
    thread.start()
    try:
        yield server,state
    finally:
        server.shutdown(); server.server_close(); thread.join()

def request(port, path, token, body=None):
    req=urllib.request.Request('http://127.0.0.1:'+str(port)+path,data=None if body is None else json.dumps(body).encode(),headers={'Authorization':'Bearer '+token,'Content-Type':'application/json'})
    opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req,timeout=5) as response:
            status,data=response.status,response.read()
    except urllib.error.HTTPError as err:
        status,data=err.code,err.read()
    assert status==200,(path,status,data)
    return json.loads(data)

def verify_timezone_search(port, token, state):
    before=state['account_calls']
    request(port,'/v1/jira/projects/CS/issues/search',token,{})
    assert state['account_calls']==before, 'unbounded search must not fetch account context'
    capability=request(port,'/v1/jira/projects/CS/capabilities',token)
    assert capability['bot']['identity']=='mock-bot' and 'timeZone' not in capability['bot']
    first_query=len(state['queries'])
    for start,end in [
        ('2026-09-29T10:00:45.123+08:00','2026-09-29T11:00:59+08:00'),
        ('2026-09-29T02:00:45.123Z','2026-09-29T03:00:59Z'),
    ]:
        request(port,'/v1/jira/projects/CS/issues/search',token,{'updatedFrom':start,'updatedTo':end})
    expected='(project = "CS") AND (labels = zammad) AND (updated >= "2026-09-29 10:00" AND updated <= "2026-09-29 11:00") ORDER BY updated ASC, key ASC'
    assert state['queries'][first_query:]==[expected,expected],state['queries'][first_query:]
    request(port,'/v1/jira/projects/CS/capabilities',token)
    assert state['account_calls']==before+1, 'capabilities and equivalent-offset searches must share one /myself snapshot'

for stack in ['self', 'install', '149']:
    for mode in ['bearer', 'basic']:
        name='gateway-compose-smoke-'+uuid.uuid4().hex[:8]
        auth={'type':'bearer','token':'mock-$token'} if mode=='bearer' else {'type':'basic','username':'mock','password':'mock-$\'"pass'}
        # Colima shares the user's home; all configuration and credentials are disposable.
        with tempfile.TemporaryDirectory(prefix=name,dir=os.path.expanduser('~')) as tmp, local_jira(auth) as (upstream,state):
            p=pathlib.Path(tmp)
            source=p/'config.json'
            def write_config(token):
                value={'server':{'listenAddr':':9123'},'audit':{'enabled':False},'tokens':[{'token':token,'jiraProjects':['CS']}], 'jira':{'baseUrl':'http://gateway-mock.test:'+str(upstream.server_port)+'/jira','auth':auth,'projects':{'CS':{'filterJql':'labels = zammad','readFields':['summary']}}}}
                new=p/'next.json';new.write_text(json.dumps(value));new.chmod(0o644);os.replace(new,source)
            write_config('mock-old')
            (p/'.env').write_text('JIRA_BASE_URL=invalid-stale-url\nGATEWAY_AUTH_FILE=/missing\nGATEWAY_TOKEN_INTERNAL=stale-token\nHTTPS_PROXY=http://mock.invalid:9\nNO_PROXY=mock.invalid\nSSL_CERT_FILE=/mock.pem\nEXTERNAL_VALUE=retained\n')
            service='rag-explorer-platform-gateway' if stack=='149' else 'platform-gateway'
            if stack=='self':
                (p/'base.yml').write_text((repo/'docker-compose.yml').read_text())
                # Port/volume override only; preserve the actual self env_file/mappings.
                (p/'override.yml').write_text('services:\n  platform-gateway:\n    ports: !override ["127.0.0.1::9123"]\n    volumes: !override\n      - type: bind\n        source: '+json.dumps(str(source))+'\n        target: /config.json\n        read_only: true\n        bind:\n          create_host_path: false\n')
                base=p/'base.yml'
            else:
                base=p/'base.json'
                base.write_text(json.dumps({'services':{service:{'image':image,'env_file':['.env'],'environment':{'JIRA_BASE_URL':'old-dedicated','CK_LOGS_BASIC_USER':'old','UNRELATED':'keep'},'ports':['127.0.0.1::9123']}}}))
                (p/'override.yml').write_bytes(subprocess.check_output(['node',str(repo/'scripts/deploy/compose-auth.mjs'),stack,str(source)]))
            (p/'image.json').write_text(json.dumps({'services':{service:{'image':image,'extra_hosts':['gateway-mock.test:host-gateway'],'environment':{'TZ':'UTC','ZONEINFO':'/missing-zoneinfo','GOROOT':'/missing-goroot'}}}}))
            env={**os.environ,'PLATFORM_GATEWAY_CONFIG_HOST_FILE':str(source),'PLATFORM_GATEWAY_AUTH_HOST_FILE':'/obsolete/missing'}
            if context!='-': env['DOCKER_CONTEXT']=context
            cmd=['docker-compose','--project-directory',str(p),'-p',name,'-f',str(base),'-f',str(p/'override.yml'),'-f',str(p/'image.json')]
            def compose(*args,check=True):
                return subprocess.run(cmd+list(args),env=env,check=check,capture_output=True,text=True)
            def start():
                compose('up','-d','--no-build','--force-recreate')
                cid=compose('ps','-q',service).stdout.strip()
                info=json.loads(subprocess.check_output(docker+['inspect',cid]))[0]
                port=int(info['NetworkSettings']['Ports']['9123/tcp'][0]['HostPort'])
                for _ in range(50):
                    try:
                        if get(port,'mock-old') in [200,401]: return cid,info,port
                    except OSError: pass
                    time.sleep(.1)
                raise RuntimeError('gateway did not start')
            try:
                cid,info,port=start()
                settings=dict(item.split('=',1) for item in info['Config']['Env'])
                for k,v in {'JIRA_BASE_URL':'invalid-stale-url','GATEWAY_AUTH_FILE':'/missing','GATEWAY_TOKEN_INTERNAL':'stale-token','EXTERNAL_VALUE':'retained','HTTPS_PROXY':'http://mock.invalid:9','NO_PROXY':'mock.invalid','SSL_CERT_FILE':'/mock.pem','GATEWAY_CONFIG_FILE':'/config.json'}.items(): assert settings[k]==v,k
                if stack!='self': assert settings['UNRELATED']=='keep' and 'CK_LOGS_BASIC_USER' not in settings
                assert info['Config']['WorkingDir']=='/'
                assert next(m for m in info['Mounts'] if m['Destination']=='/config.json')['RW'] is False
                assert subprocess.check_output(docker+['exec',cid,'id','-u'],text=True).strip()=='65532'
                subprocess.run(docker+['exec',cid,'/bin/sh','-c','test ! -d /usr/share/zoneinfo && test ! -d /usr/lib/zoneinfo && test ! -d /usr/share/lib/zoneinfo && test ! -d /etc/zoneinfo'],check=True,capture_output=True)
                actual=json.loads(subprocess.check_output(docker+['exec',cid,'cat','/config.json']))
                assert actual['jira']['auth']==auth
                assert get(port,'mock-old')==200 and get(port,'stale-token')==401
                verify_timezone_search(port,'mock-old',state)
                write_config('mock-new')
                assert get(port,'mock-old')==200 and get(port,'mock-new')==401
                cid,info,port=start()
                assert get(port,'mock-new')==200 and get(port,'mock-old')==401
                verify_timezone_search(port,'mock-new',state)
                compose('down')
                source.unlink()
                rejected=subprocess.run(['bash',str(repo/'scripts/deploy/standalone.sh'),'--mode','docker','--stack',stack,'--app-root',str(p),'--config-file',str(source)],capture_output=True)
                assert rejected.returncode!=0 and not source.exists()
                # Use a never-created path for the engine's missing-bind check.
                # A VM shared filesystem can briefly retain a just-deleted inode:
                # Compose may acknowledge start before the app reports ENOENT.
                # The launcher check above still covers the deleted config path.
                missing_source=p/'never-created.json'
                image_override=json.loads((p/'image.json').read_text())
                image_override['services'][service]['volumes']=[{'type':'bind','source':str(missing_source),'target':'/config.json','read_only':True,'bind':{'create_host_path':False}}]
                (p/'image.json').write_text(json.dumps(image_override))
                missing=compose('up','-d','--no-build',check=False)
                if missing.returncode==0:
                    # Podman's Docker API may create the missing source despite
                    # Compose's create_host_path:false. The supported launcher
                    # rejects it before invoking the engine (asserted above).
                    version=subprocess.check_output(docker+['version'],text=True,stderr=subprocess.DEVNULL)
                    assert 'Podman' in version, 'Compose accepted a missing source'
                    print('LIMITATION: Podman Compose API creates missing source; standalone preflight rejects it')
                else: assert not missing_source.exists() and not source.exists()
                print('PASS:',stack,mode,'actual environment, JSON credentials, UID, read-only source, missing source, atomic replacement + recreation, account cache, equivalent-offset Shanghai search without system zoneinfo')
            finally:
                compose('down',check=False)
