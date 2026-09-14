#!/usr/bin/env python3
"""Verify config exclusion in the actual Docker builder context, then test Go 1.23."""
import pathlib
import shutil
import subprocess
import sys
import tempfile
import uuid
repo=pathlib.Path(__file__).resolve().parents[2]
context=sys.argv[1] if len(sys.argv)>1 else '-'
docker=['docker'] + (['--context',context] if context != '-' else [])
tag='platform-gateway:context-smoke-'+uuid.uuid4().hex[:8]
with tempfile.TemporaryDirectory(prefix='gateway-build-smoke-') as tmp:
    root=pathlib.Path(tmp)
    for name in ['cmd','internal']:
        shutil.copytree(repo/name,root/name)
    for name in ['Dockerfile','.dockerignore','go.mod']:
        shutil.copy2(repo/name,root/name)
    (root/'config.json').write_text('{"token":"mock-sentinel-only"}')
    (root/'nested').mkdir()
    (root/'nested'/'config.json').write_text('{"token":"nested-mock-only"}')
    try:
        subprocess.run(docker+['build','--target','builder','-t',tag,str(root)],check=True)
        subprocess.run(docker+['run','--rm','--entrypoint','/bin/sh',tag,'-c','test ! -e /src/config.json && test ! -e /src/nested/config.json && go test ./... && go vet ./...'],check=True)
        print('PASS: root/nested config excluded from builder context; Go 1.23 tests and vet')
    finally:
        subprocess.run(docker+['image','rm',tag],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
