#!/usr/bin/env python3
"""Verify app-stack environment delivery in Docker's actual container config."""
import json
import os
import pathlib
import subprocess
import sys
import tempfile
import uuid
repo = pathlib.Path(__file__).resolve().parents[2]
context = sys.argv[1] if len(sys.argv) > 1 else 'colima'
image = sys.argv[2] if len(sys.argv) > 2 else 'platform-gateway:jira-smoke'
compose = ['docker-compose']
docker = ['docker', '--context', context]
for stack in ['install', '149']:
    for mode in ['bearer', 'basic']:
        name = 'gateway-compose-smoke-' + uuid.uuid4().hex[:8]
        with tempfile.TemporaryDirectory(prefix=name, dir=os.path.expanduser('~')) as tmp:
            p = pathlib.Path(tmp)
            source = p/'config.json'; source.write_text('{}'); source.chmod(0o644)
            (p/'empty.env').write_text('')
            service = 'platform-gateway' if stack == 'install' else 'rag-explorer-platform-gateway'
            (p/'base.json').write_text(json.dumps({'services':{service:{'image':image,'profiles':['platform-gateway']}}}))
            (p/'overlay.json').write_bytes(subprocess.check_output(['node',str(repo/'scripts/deploy/compose-auth.mjs'),stack,str(source)]))
            env = {'PATH':os.environ['PATH'],'HOME':os.path.expanduser('~'),'DOCKER_CONTEXT':context,'JIRA_BASE_URL':'https://mock.invalid/context'}
            if mode == 'bearer': env['JIRA_API_TOKEN'] = 'mock-$token-with-symbols'
            else: env.update(JIRA_BASIC_USER='mock-user',JIRA_BASIC_PASSWORD='mock-$password-with-symbols')
            cmd = compose + ['--env-file',str(p/'empty.env'),'--profile','platform-gateway','-p',name,'-f',str(p/'base.json'),'-f',str(p/'overlay.json')]
            try:
                subprocess.run(cmd+['create',service],env=env,check=True,stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
                cid = subprocess.check_output(cmd+['ps','-aq',service],env=env,text=True).strip()
                config = json.loads(subprocess.check_output(docker+['inspect',cid]))[0]['Config']
                settings = dict(item.split('=',1) for item in config['Env'])
                for key in ['JIRA_BASE_URL','JIRA_API_TOKEN','JIRA_BASIC_USER','JIRA_BASIC_PASSWORD']:
                    assert settings[key] == env.get(key,''), key + ' changed'
                assert settings['GATEWAY_AUTH_FILE'] == '/config.json'
                print('PASS:',stack,mode,'Jira environment and literal dollar signs preserved')
            finally:
                subprocess.run(cmd+['down'],env=env,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
