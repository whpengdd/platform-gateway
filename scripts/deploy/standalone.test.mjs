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
  assert.doesNotMatch(src, /--listen\)/);
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

test("app overlays remove dedicated mappings but preserve env_file and unrelated settings", () => {
 const tmp=fs.mkdtempSync(path.join('/tmp','gateway-compose-'));
 try {
  const source=path.join(tmp,'config.json');fs.writeFileSync(source,'{}');
  fs.writeFileSync(path.join(tmp,'external.env'),'JIRA_BASE_URL=stale-from-env-file\nGATEWAY_TOKEN_INTERNAL=old-token\nHTTPS_PROXY=http://mock-proxy\nSSL_CERT_FILE=/mock.pem\nEXTERNAL_VALUE=preserved\n');
  for(const stack of ['install','149']) {
   const service=stack==='149'?'rag-explorer-platform-gateway':'platform-gateway';
   const base=path.join(tmp,'base.json'),overlay=path.join(tmp,'overlay.yml');
   fs.writeFileSync(base,JSON.stringify({services:{[service]:{image:'mock',env_file:['external.env'],environment:{JIRA_BASE_URL:'dedicated-old',CK_LOGS_BASIC_USER:'old',NO_PROXY:'mock',UNRELATED:'keep'}}}}));
   fs.writeFileSync(overlay,execFileSync(process.execPath,[path.join(dir,'compose-auth.mjs'),stack,source]));
   const value=JSON.parse(execFileSync('docker-compose',['-p','gateway-test','-f',base,'-f',overlay,'config','--format','json'],{encoding:'utf8'})).services[service];
   assert.equal(value.environment.GATEWAY_CONFIG_FILE,'/config.json');
   assert.equal(value.environment.JIRA_BASE_URL,'stale-from-env-file');
   assert.equal(value.environment.GATEWAY_TOKEN_INTERNAL,'old-token');
   assert.equal(value.environment.CK_LOGS_BASIC_USER,undefined);
   assert.equal(value.environment.HTTPS_PROXY,'http://mock-proxy');
   assert.equal(value.environment.SSL_CERT_FILE,'/mock.pem');
   assert.equal(value.environment.NO_PROXY,'mock');
   assert.equal(value.environment.UNRELATED,'keep');
   assert.equal(value.environment.EXTERNAL_VALUE,'preserved');
   assert.equal(value.volumes[0].source,source);
   assert.equal(value.volumes[0].read_only,true);
   assert.notEqual(value.volumes[0].bind.create_host_path,true);
   assert.equal(value.working_dir,'/');
  }
  assert.throws(()=>execFileSync(process.execPath,[path.join(dir,'compose-auth.mjs'),'install',path.join(tmp,'missing')],{stdio:'pipe'}));
 } finally {fs.rmSync(tmp,{recursive:true,force:true});}
});

test("standalone rejects removed parameters", () => {
 for(const option of ['--auth-file','--listen']) assert.throws(()=>execFileSync('bash',[path.join(dir,'standalone.sh'),'--mode','host',option,'value'],{stdio:'pipe'}));
});
