#!/usr/bin/env python3
"""Exercise self/install/149 with disposable JSON credentials and actual containers."""
import json
import os
import pathlib
import subprocess
import sys
import tempfile
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

for stack in ['self', 'install', '149']:
    for mode in ['bearer', 'basic']:
        name='gateway-compose-smoke-'+uuid.uuid4().hex[:8]
        with tempfile.TemporaryDirectory(prefix=name) as tmp:
            p=pathlib.Path(tmp)
            source=p/'config.json'
            auth={'type':'bearer','token':'mock-$token'} if mode=='bearer' else {'type':'basic','username':'mock','password':'mock-$\'"pass'}
            def write_config(token):
                value={'server':{'listenAddr':':9123'},'audit':{'enabled':False},'tokens':[{'token':token,'jiraProjects':['CS']}], 'jira':{'baseUrl':'https://mock.invalid/jira','auth':auth,'projects':{'CS':{}}}}
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
            (p/'image.json').write_text(json.dumps({'services':{service:{'image':image}}}))
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
                actual=json.loads(subprocess.check_output(docker+['exec',cid,'cat','/config.json']))
                assert actual['jira']['auth']==auth
                assert get(port,'mock-old')==200 and get(port,'stale-token')==401
                write_config('mock-new')
                assert get(port,'mock-old')==200 and get(port,'mock-new')==401
                cid,info,port=start()
                assert get(port,'mock-new')==200 and get(port,'mock-old')==401
                compose('down')
                source.unlink()
                rejected=subprocess.run(['bash',str(repo/'scripts/deploy/standalone.sh'),'--mode','docker','--stack',stack,'--app-root',str(p),'--config-file',str(source)],capture_output=True)
                assert rejected.returncode!=0 and not source.exists()
                missing=compose('up','-d','--no-build',check=False)
                if missing.returncode==0:
                    # Podman's Docker API may create the missing source despite
                    # Compose's create_host_path:false. The supported launcher
                    # rejects it before invoking the engine (asserted above).
                    version=subprocess.check_output(docker+['version'],text=True,stderr=subprocess.DEVNULL)
                    assert 'Podman' in version, 'Compose accepted a missing source'
                    print('LIMITATION: Podman Compose API creates missing source; standalone preflight rejects it')
                else: assert not source.exists()
                print('PASS:',stack,mode,'actual environment, JSON credentials, UID, read-only source, missing source, atomic replacement + recreation')
            finally:
                compose('down',check=False)
