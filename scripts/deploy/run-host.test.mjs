import assert from 'node:assert/strict';
import test from 'node:test';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { buildChildEnv, resolveConfig } from './run-host.mjs';
const launcher = new URL('./run-host.mjs', import.meta.url);
test('generic environment delivery preserves punctuation and stale values', () => {
 const input = { GATEWAY_AUTH_FILE:'/absent', JIRA_BASE_URL:'bad-url', GATEWAY_TOKEN_INTERNAL:'stale', HTTPS_PROXY:'http://proxy', SSL_CERT_FILE:'/cert', UNRELATED:" $'\" " };
 const env=buildChildEnv(input);
 for (const [k,v] of Object.entries(input)) assert.equal(env[k],v);
 assert.equal(resolveConfig('/app','',env),'/app/config.json');
 assert.equal(resolveConfig('/app','relative.json',env),'/app/relative.json');
 assert.equal(resolveConfig('/app','',{GATEWAY_CONFIG_FILE:'chosen.json'}),'/app/chosen.json');
});
test('removed and unknown CLI options fail', () => {
 for(const option of ['--listen','--auth-file','--unknown']) assert.throws(()=>execFileSync(process.execPath,[launcher.pathname,option,'value'],{stdio:'pipe'}));
});
test('launcher uses app cwd, file selector and no audit directory side effects', async () => {
 const root=fs.mkdtempSync(path.join(os.tmpdir(),'gateway-host-'));
 try {
  fs.writeFileSync(path.join(root,'config.json'),JSON.stringify({server:{listenAddr:'127.0.0.1:9123'},audit:{enabled:false}}));
  fs.writeFileSync(path.join(root,'.env.local'),'GATEWAY_AUTH_FILE=/missing\nLISTEN_ADDR=bad\nGATEWAY_TOKEN_INTERNAL=old\nEXTERNAL_VALUE=$literal\n');
  const binary=path.join(root,'mock');
  fs.writeFileSync(binary,`#!/usr/bin/env node\nrequire('fs').writeFileSync('result.json',JSON.stringify({cwd:process.cwd(),env:process.env}));\n`,{mode:0o755});
  execFileSync(process.execPath,[launcher.pathname,'--app-root',root,'--binary',binary]);
  for(let i=0;i<100&&!fs.existsSync(path.join(root,'result.json'));i++) await new Promise(r=>setTimeout(r,20));
  const result=JSON.parse(fs.readFileSync(path.join(root,'result.json')));
  assert.equal(result.cwd,root);
  assert.equal(result.env.GATEWAY_CONFIG_FILE,path.join(root,'config.json'));
  assert.equal(result.env.EXTERNAL_VALUE,'$literal');
  assert.equal(result.env.GATEWAY_TOKEN_INTERNAL,'old');
  assert.ok(!fs.existsSync(path.join(root,'logs')));
  assert.equal(execFileSync(process.execPath,[new URL('./config-path.mjs',import.meta.url).pathname,'health',root],{encoding:'utf8'}),'http://127.0.0.1:9123/health');
 } finally {fs.rmSync(root,{recursive:true,force:true});}
});
