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

context = sys.argv[1] if len(sys.argv) > 1 else 'colima'
image = sys.argv[2] if len(sys.argv) > 2 else 'platform-gateway:jira-smoke'
docker = ['docker', '--context', context]

def run(*args, check=True):
    p = subprocess.run(docker + list(args), capture_output=True, text=True)
    if check and p.returncode:
        raise RuntimeError('Docker command failed: ' + p.stderr)
    return p

class Jira(http.server.BaseHTTPRequestHandler):
    calls = 0
    def log_message(self, *args):
        pass
    def do_POST(self):
        Jira.calls += 1
        assert self.headers.get('Authorization') == 'Bearer mock-upstream'
        body = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
        assert self.path == '/jira/rest/api/2/search'
        assert '(project = "CS")' in body['jql']
        assert '(labels = zammad)' in body['jql']
        data = json.dumps({'startAt':0, 'total':1, 'issues':[{'id':'1','key':'CS-1','fields':{'project':{'key':'CS'},'summary':'mock-only'}}]}).encode()
        self.send_response(200)
        self.send_header('Content-Type','application/json')
        self.send_header('Content-Length',str(len(data)))
        self.end_headers()
        self.wfile.write(data)

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

name = 'gateway-jira-smoke-' + uuid.uuid4().hex[:10]
# Colima shares the user's home; no real configuration is read or overwritten.
with tempfile.TemporaryDirectory(prefix='gateway-jira-smoke-',dir=os.path.expanduser('~')) as tmp:
    root = pathlib.Path(tmp)
    cert, key, config = root/'ca.pem', root/'key.pem', root/'config.json'
    subprocess.run(['openssl','req','-x509','-newkey','rsa:2048','-nodes','-days','1','-keyout',str(key),'-out',str(cert),'-subj','/CN=host.lima.internal','-addext','subjectAltName=DNS:host.lima.internal'], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    cert.chmod(0o644)
    server = http.server.ThreadingHTTPServer(('0.0.0.0',0),Jira)
    tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.load_cert_chain(cert,key)
    server.socket = tls.wrap_socket(server.socket,server_side=True)
    thread = threading.Thread(target=server.serve_forever,daemon=True)
    thread.start()
    def write_config(token):
        value={'tokens':[{'token':token,'jiraProjects':['CS']},{'token':'mock-ck','cklogs':'external'},{'token':'mock-internal','cklogs':'internal'}],'jira':{'projects':{'CS':{'filterJql':'labels = zammad','readFields':['summary']}}}}
        new = root/'next.json'
        new.write_text(json.dumps(value))
        new.chmod(0o644)
        os.replace(new,config)
    def start():
        run('run','-d','--name',name,'--workdir','/','-p','127.0.0.1::8091',
            '--mount','type=bind,src='+str(config)+',dst=/config.json,readonly',
            '--mount','type=bind,src='+str(cert)+',dst=/mock-ca.pem,readonly',
            '-e','SSL_CERT_FILE=/mock-ca.pem','-e','JIRA_BASE_URL=https://host.lima.internal:'+str(server.server_port)+'/jira',
            '-e','JIRA_API_TOKEN=mock-upstream','-e','CK_LOGS_BASIC_USER=mock','-e','CK_LOGS_BASIC_PASS=mock','-e','GATEWAY_LOG_DIR=off',image)
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
        run('run','--rm','--entrypoint','/bin/sh',image,'-c','test ! -e /config.json && test ! -e /src/config.json && test "$(id -u)" = 65532')
        write_config('mock-jira-old')
        port=start()
        inspect=json.loads(run('inspect',name).stdout)[0]
        assert inspect['Config']['WorkingDir']=='/'
        mount=next(m for m in inspect['Mounts'] if m['Destination']=='/config.json')
        assert mount['RW'] is False
        assert run('exec',name,'id','-u').stdout.strip()=='65532'
        request(port,'/v1/jira/projects','mock-jira-old')
        request(port,'/v1/jira/projects/CS/issues/search','mock-jira-old',{})
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
        port=start()
        request(port,'/v1/jira/projects','mock-jira-old',expected=401)
        request(port,'/v1/jira/projects/CS/issues/search','mock-jira-new',{})
        print('PASS: TLS mock, UID 65532, read-only mount, missing source, cross-service/project denial, atomic replacement + recreation, token rotation, no config in runtime image')
    finally:
        run('rm','-f',name,check=False)
        server.shutdown()
        server.server_close()
