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
