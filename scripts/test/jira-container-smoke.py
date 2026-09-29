#!/usr/bin/env python3
"""Local-only container smoke with TLS mock Jira and disposable credentials.
Usage: python3 scripts/test/jira-container-smoke.py [docker-context] [image]
"""
import http.server
import json
import os
import pathlib
import ssl
import subprocess
import sys
import tempfile
import threading
import time
import urllib.request
import urllib.error
import uuid

context = sys.argv[1] if len(sys.argv) > 1 else '-'
image = sys.argv[2] if len(sys.argv) > 2 else 'platform-gateway:config-smoke'
docker = ['docker'] + (['--context', context] if context != '-' else [])

def run(*args, check=True):
    p = subprocess.run(docker + list(args), capture_output=True, text=True)
    if check and p.returncode:
        raise RuntimeError('Docker command failed: ' + p.stderr)
    return p

class Jira(http.server.BaseHTTPRequestHandler):
    calls = 0
    account_calls = 0
    queries = []
    lock = threading.Lock()
    def log_message(self, *args):
        pass
    def send_json(self, value):
        data = json.dumps(value).encode()
        self.send_response(200)
        self.send_header('Content-Type','application/json')
        self.send_header('Content-Length',str(len(data)))
        self.end_headers()
        self.wfile.write(data)
    def do_GET(self):
        with Jira.lock:
            Jira.calls += 1
            Jira.account_calls += 1
        assert self.headers.get('Authorization') == 'Bearer mock-upstream'
        assert self.path == '/jira/rest/api/2/myself'
        self.send_json({'name':'mock-bot','displayName':'Mock Bot','timeZone':'Asia/Shanghai'})
    def do_POST(self):
        with Jira.lock:
            Jira.calls += 1
        assert self.headers.get('Authorization') == 'Bearer mock-upstream'
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
        assert self.path == '/jira/rest/api/2/search'
        assert '(project = "CS")' in body['jql']
        assert '(labels = zammad)' in body['jql']
        with Jira.lock:
            Jira.queries.append(body['jql'])
        self.send_json({'startAt':0, 'total':1, 'issues':[{'id':'1','key':'CS-1','fields':{'project':{'key':'CS'},'summary':'mock-only'}}]})

def request(port, path, token='', body=None, expected=200):
    headers = {'Authorization':'Bearer '+token, 'Content-Type':'application/json'}
    req = urllib.request.Request('http://127.0.0.1:'+str(port)+path, data=None if body is None else json.dumps(body).encode(), headers=headers)
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
    try:
        with opener.open(req, timeout=5) as r:
            status, data = r.status, r.read()
    except urllib.error.HTTPError as e:
        status, data = e.code, e.read()
    assert status == expected, (path,status,expected,data)
    return data

def verify_timezone_search(port, token):
    before = Jira.account_calls
    capability = json.loads(request(port,'/v1/jira/projects/CS/capabilities',token))
    assert capability['bot']['identity'] == 'mock-bot'
    assert 'timeZone' not in capability['bot']
    first_query = len(Jira.queries)
    for start, end in [
        ('2026-09-29T10:00:45.123+08:00','2026-09-29T11:00:59+08:00'),
        ('2026-09-29T02:00:45.123Z','2026-09-29T03:00:59Z'),
    ]:
        request(port,'/v1/jira/projects/CS/issues/search',token,{'updatedFrom':start,'updatedTo':end})
    expected = '(project = "CS") AND (labels = zammad) AND (updated >= "2026-09-29 10:00" AND updated <= "2026-09-29 11:00") ORDER BY updated ASC, key ASC'
    assert Jira.queries[first_query:] == [expected, expected], Jira.queries[first_query:]
    request(port,'/v1/jira/projects/CS/capabilities',token)
    assert Jira.account_calls == before + 1, 'capabilities and equivalent-offset searches must share one /myself snapshot'

name = 'gateway-jira-smoke-' + uuid.uuid4().hex[:10]
# Colima shares the user's home; no real configuration is read or overwritten.
with tempfile.TemporaryDirectory(prefix='gateway-jira-smoke-',dir=os.path.expanduser('~')) as tmp:
    root = pathlib.Path(tmp)
    cert, key, config = root/'ca.pem', root/'key.pem', root/'config.json'
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(key),'-out',str(cert),'-subj','/CN=gateway-mock.test','-addext','subjectAltName=DNS:gateway-mock.test'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    cert.chmod(0o644)
    server = http.server.ThreadingHTTPServer(('0.0.0.0',0),Jira)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(cert,key)
    server.socket = tls.wrap_socket(server.socket,server_side=True)
    thread = threading.Thread(target=server.serve_forever,daemon=True)
    thread.start()
    class CK(http.server.BaseHTTPRequestHandler):
        direct = 0
        proxied = 0
        def log_message(self, *args): pass
        def do_CONNECT(self):
            raise AssertionError('Jira must bypass proxy')
        def do_POST(self):
            assert self.headers.get('Authorization') == 'Basic bW9jazptb2Nr'
            self.rfile.read(int(self.headers.get('Content-Length', '0')))
            if self.path.startswith('http://'): CK.proxied += 1
            else: CK.direct += 1
            data=b'{"responses":[{"hits":{"total":0,"hits":[]}}]}'
            self.send_response(200); self.send_header('Content-Type','application/json')
            self.send_header('Content-Length',str(len(data))); self.end_headers(); self.wfile.write(data)
    ck_server=http.server.ThreadingHTTPServer(('0.0.0.0',0),CK)
    proxy_server=http.server.ThreadingHTTPServer(('0.0.0.0',0),CK)
    for mock in [ck_server,proxy_server]: threading.Thread(target=mock.serve_forever,daemon=True).start()
    def write_config(token):
        value={'audit':{'enabled':False},'cklogs':{'baseUrl':'http://gateway-mock.test:'+str(ck_server.server_port),'auth':{'type':'basic','username':'mock','password':'mock'}},'tokens':[{'token':token,'jiraProjects':['CS']},{'token':'mock-ck','cklogs':'external'},{'token':'mock-internal','cklogs':'internal'}],'jira':{'baseUrl':'https://gateway-mock.test:'+str(server.server_port)+'/jira','auth':{'type':'bearer','token':'mock-upstream'},'projects':{'CS':{'filterJql':'labels = zammad','readFields':['summary']}}}}
        new = root/'next.json'
        new.write_text(json.dumps(value))
        new.chmod(0o644)
        os.replace(new,config)
    def start(bypass=False):
        run('run','-d','--name',name,'--workdir','/','--add-host','gateway-mock.test:host-gateway','-p','127.0.0.1::8091',
            '--mount','type=bind,src='+str(config)+',dst=/config.json,readonly',
            '--mount','type=bind,src='+str(cert)+',dst=/mock-ca.pem,readonly',
            '-e','SSL_CERT_FILE=/mock-ca.pem','-e','GATEWAY_CONFIG_FILE=/config.json',
            '-e','TZ=UTC','-e','ZONEINFO=/missing-zoneinfo','-e','GOROOT=/missing-goroot',
            '-e','HTTP_PROXY=http://gateway-mock.test:'+str(proxy_server.server_port),
            '-e','HTTPS_PROXY=http://gateway-mock.test:'+str(proxy_server.server_port),
            '-e','NO_PROXY='+('gateway-mock.test' if bypass else ''),
            '-e','JIRA_BASE_URL=invalid-stale-url','-e','GATEWAY_AUTH_FILE=/missing',
            '-e','GATEWAY_TOKEN_INTERNAL=stale-token','-e','EXTERNAL_VALUE=retained',image)
        port=int(run('inspect','--format','{{(index (index .NetworkSettings.Ports "8091/tcp") 0).HostPort}}',name).stdout.strip())
        for _ in range(50):
            try:
                request(port,'/ready')
                return port
            except (OSError,AssertionError):
                time.sleep(.1)
        raise RuntimeError('container not ready')
    try:
        # Missing bind source must fail, not turn into a directory.
        missing = run('run','--rm','--mount','type=bind,src='+str(root/'absent.json')+',dst=/config.json,readonly',image,check=False)
        assert missing.returncode != 0 and not (root/'absent.json').exists()
        run('run','--rm','--entrypoint','/bin/sh',image,'-c',
            'test ! -e /config.json && test ! -e /src/config.json && test "$(id -u)" = 65532 && '
            'test ! -d /usr/share/zoneinfo && test ! -d /usr/lib/zoneinfo && '
            'test ! -d /usr/share/lib/zoneinfo && test ! -d /etc/zoneinfo')
        write_config('mock-jira-old')
        port=start()
        inspect=json.loads(run('inspect',name).stdout)[0]
        assert inspect['Config']['WorkingDir']=='/'
        settings=dict(item.split('=',1) for item in inspect['Config']['Env'])
        assert settings['EXTERNAL_VALUE']=='retained' and settings['JIRA_BASE_URL']=='invalid-stale-url'
        mount=next(m for m in inspect['Mounts'] if m['Destination']=='/config.json')
        assert mount['RW'] is False
        assert run('exec',name,'id','-u').stdout.strip()=='65532'
        request(port,'/v1/jira/projects','mock-jira-old')
        request(port,'/v1/jira/projects/CS/issues/search','mock-jira-old',{})
        assert Jira.account_calls == 0, 'unbounded search must not fetch account context'
        verify_timezone_search(port,'mock-jira-old')
        request(port,'/v1/cklogs/delivery','mock-ck',{'account':'a@example.com','direction':'outbound','peer':'b@example.com','timeRange':{'from':'2026-09-01T00:00:00Z','to':'2026-09-01T01:00:00Z'}})
        assert CK.proxied > 0 and CK.direct == 0
        request(port,'/v1/jira/projects','stale-token',expected=401)
        assert not run('exec',name,'test','-d','/logs',check=False).returncode == 0
        before=Jira.calls
        request(port,'/v1/jira/projects/IT/capabilities','mock-jira-old',expected=403)
        request(port,'/v1/jira/projects/CS/capabilities','mock-ck',expected=403)
        request(port,'/v1/jira/projects/CS/capabilities','mock-internal',expected=403)
        request(port,'/v1/cklogs/delivery','mock-jira-old',{},403)
        request(port,'/v1/cklogs/analysis/delivery','mock-ck',{},403)
        assert Jira.calls==before
        write_config('mock-jira-new')
        request(port,'/v1/jira/projects','mock-jira-old')
        request(port,'/v1/jira/projects','mock-jira-new',expected=401)
        run('rm','-f',name)
        port=start(bypass=True)
        proxied=CK.proxied
        request(port,'/v1/cklogs/delivery','mock-ck',{'account':'a@example.com','direction':'outbound','peer':'b@example.com','timeRange':{'from':'2026-09-01T00:00:00Z','to':'2026-09-01T01:00:00Z'}})
        assert CK.direct > 0 and CK.proxied == proxied
        request(port,'/v1/jira/projects','mock-jira-old',expected=401)
        request(port,'/v1/jira/projects/CS/issues/search','mock-jira-new',{})
        verify_timezone_search(port,'mock-jira-new')
        print('PASS: TLS mock, UID 65532, read-only mount, missing source, cross-service/project denial, atomic replacement + recreation, token rotation, no config in runtime image, CKLogs proxy + NO_PROXY, Jira direct, account cache, equivalent-offset Shanghai search without system zoneinfo')
    finally:
        run('rm','-f',name,check=False)
        for mock in [ck_server,proxy_server]: mock.shutdown(); mock.server_close()
        server.shutdown()
        server.server_close()
