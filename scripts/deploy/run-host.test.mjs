import assert from "node:assert/strict";
import test from "node:test";
import { buildChildEnv } from "./run-host.mjs";

test("maps PLATFORM_GATEWAY_* into GATEWAY_TOKEN_* for the host binary", () => {
  const env = buildChildEnv({
    PLATFORM_GATEWAY_TOKEN: "ext",
    PLATFORM_GATEWAY_INTERNAL_TOKEN: "int",
    CK_LOGS_BASIC_USER: "u",
    CK_LOGS_BASIC_PASS: "p",
  }, "127.0.0.1:8091", "/tmp/logs");
  assert.equal(env.GATEWAY_TOKEN_EXTERNAL, "ext");
  assert.equal(env.GATEWAY_TOKEN_INTERNAL, "int");
  assert.equal(env.LISTEN_ADDR, "127.0.0.1:8091");
  assert.equal(env.CK_LOGS_BASIC_USER, "u");
});

test("fails closed without any token", () => {
  assert.throws(
    () => buildChildEnv({}, "127.0.0.1:8091", "/tmp/logs"),
    /no gateway token/,
  );
});
