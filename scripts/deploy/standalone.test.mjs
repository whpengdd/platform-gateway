import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const dir = path.dirname(fileURLToPath(import.meta.url));
const read = (name) => fs.readFileSync(path.join(dir, name), "utf8");

test("shell scripts parse", () => {
  for (const name of ["standalone.sh", "build-image.sh", "update-151.sh", "update-public.sh", "update-149.sh"]) {
    execFileSync("bash", ["-n", path.join(dir, name)]);
  }
});

test("host standalone never fuser-kills 8091 or binds 0.0.0.0", () => {
  const src = read("standalone.sh");
  assert.doesNotMatch(src, /fuser\s+-k[^\n]*8091/);
  assert.match(src, /127\.0\.0\.1:8091/);
  assert.match(src, /--mode host\|docker/);
  assert.match(src, /--stack self\|install\|149/);
  assert.match(src, /up -d --no-build --force-recreate --no-deps platform-gateway/);
  assert.match(src, /up -d --no-build --force-recreate --no-deps rag-explorer-platform-gateway/);
  assert.match(src, /seq 1 30/);
  assert.match(src, /LISTEN must be loopback/);
  assert.match(src, /extracting binary from docker image/);
});

test("build-image tags VERSION", () => {
  const src = read("build-image.sh");
  assert.match(src, /rag-explorer-ai-platform-gateway/);
  assert.match(src, /VERSION/);
  assert.doesNotMatch(src, /0\.0\.0\.0/);
});

test("app-stack updates require app-root and do not go-build inside rag-explorer-ai", () => {
  assert.match(read("update-149.sh"), /--app-root/);
  assert.match(read("update-public.sh"), /--app-root/);
  assert.match(read("update-151.sh"), /--mode host/);
  assert.doesNotMatch(read("update-149.sh"), /gateway\/bin/);
  assert.match(read("update-149.sh"), /BUILD_HTTPS_PROXY/);
  assert.match(read("update-public.sh"), /BUILD_HTTPS_PROXY/);
});

test("app overlay mounts current config read-only and overrides legacy sources", () => {
  const tmp = fs.mkdtempSync(path.join(process.env.TMPDIR || '/tmp', 'gateway-overlay-'));
  try {
    const file = path.join(tmp, 'config.json'); fs.writeFileSync(file, '{}');
    for (const stack of ['install', '149']) {
      const value = JSON.parse(execFileSync(process.execPath, [path.join(dir, 'compose-auth.mjs'), stack, file], {encoding:'utf8'}));
      const service = Object.values(value.services)[0];
      assert.equal(service.working_dir, '/');
      assert.equal(service.volumes[0].source, file);
      assert.equal(service.volumes[0].read_only, true);
      assert.equal(service.volumes[0].bind.create_host_path, false);
      assert.equal(service.environment.GATEWAY_AUTH_FILE, '/config.json');
      assert.equal(service.environment.GATEWAY_TOKEN_EXTERNAL, '');
    }
    assert.throws(() => execFileSync(process.execPath, [path.join(dir, 'compose-auth.mjs'), 'install', path.join(tmp,'absent')], {stdio:'pipe'}));
  } finally {fs.rmSync(tmp,{recursive:true,force:true});}
});

test("app overlay resolves Jira credentials in the merged Compose service", () => {
  const probe = (command, args) => {
    try { execFileSync(command, [...args, 'version'], {stdio:'pipe'}); return [command, args]; }
    catch { return null; }
  };
  const compose = probe('docker', ['compose']) || probe('docker-compose', []);
  assert.ok(compose, 'Docker Compose is required for deployment integration tests');
  const tmp = fs.mkdtempSync(path.join(process.env.TMPDIR || '/tmp', 'gateway-compose-'));
  try {
    const file=path.join(tmp,'config.json');fs.writeFileSync(file,'{}');
    const envFile=path.join(tmp,'empty.env');fs.writeFileSync(envFile,'');
    for (const stack of ['install','149']) {
      const service=stack==='149'?'rag-explorer-platform-gateway':'platform-gateway';
      const base=path.join(tmp,'base.json');const overlay=path.join(tmp,'overlay.json');
      fs.writeFileSync(base, JSON.stringify({services:{[service]:{image:'mock-only',profiles:['platform-gateway'],environment:{GATEWAY_TOKEN_EXTERNAL:'legacy-mock',CK_LOGS_BASIC_USER:'mock'}}}}));
      const output=execFileSync(process.execPath,[path.join(dir,'compose-auth.mjs'),stack,file],{encoding:'utf8'});
      fs.writeFileSync(overlay,output);
      for (const authMode of ['bearer','basic']) {
        const env={PATH:process.env.PATH,HOME:tmp,JIRA_BASE_URL:'https://mock.invalid/context'};
        if (authMode==='bearer') env.JIRA_API_TOKEN='test-$token-with-symbols';
        else {env.JIRA_BASIC_USER='mock-user';env.JIRA_BASIC_PASSWORD='mock-password';}
        const value=JSON.parse(execFileSync(compose[0],[...compose[1],'--env-file',envFile,'--profile','platform-gateway','-p','gateway-test','-f',base,'-f',overlay,'config','--format','json'],{env,encoding:'utf8'}));
        const settings=value.services[service].environment;
        assert.equal(settings.JIRA_BASE_URL,env.JIRA_BASE_URL);
        // Canonical Compose output escapes dollars for safe reloading. The
        // container smoke checks the actual engine value without this escaping.
        assert.equal(settings.JIRA_API_TOKEN,(env.JIRA_API_TOKEN||'').split('$').join('$$'));
        assert.equal(settings.JIRA_BASIC_USER,env.JIRA_BASIC_USER||'');
        assert.equal(settings.JIRA_BASIC_PASSWORD,env.JIRA_BASIC_PASSWORD||'');
        assert.equal(settings.GATEWAY_TOKEN_EXTERNAL,'');
        assert.equal(value.services[service].volumes[0].read_only,true);
        assert.ok(!output.includes('test-$token-with-symbols')&&!output.includes('mock-password'));
      }
    }
  } finally {fs.rmSync(tmp,{recursive:true,force:true});}
});
