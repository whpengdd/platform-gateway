#!/usr/bin/env node
/**
 * Spawn ./bin/platform-gateway with selected keys from the app-root env file.
 * Avoids bash `source` corrupting $ and quotes. Does not print secret values.
 */
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { present, readEnvFile } from "./env-file.mjs";

const GATEWAY_KEYS = [
  "LISTEN_ADDR",
  "GATEWAY_TOKEN_EXTERNAL",
  "GATEWAY_TOKEN_INTERNAL",
  "GATEWAY_AUTH_TOKENS",
  "GATEWAY_ALLOW_CIDRS",
  "GATEWAY_LOG_DIR",
  "CK_LOGS_BASE_URL",
  "CK_LOGS_BASIC_USER",
  "CK_LOGS_BASIC_PASS",
  "CK_LOGS_INDEX",
  "CKLOGS_MAX_CONCURRENCY",
  "CKLOGS_QUEUE_SIZE",
  "CKLOGS_QUEUE_WAIT_MS",
  "CK_LOGS_TIMEOUT_MS",
  "PLATFORM_GATEWAY_TOKEN",
  "PLATFORM_GATEWAY_INTERNAL_TOKEN",
];

function parseArgs(argv) {
  const args = { appRoot: "", gatewayRoot: "", envFile: "", listen: "127.0.0.1:8091", binary: "" };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--app-root") args.appRoot = String(argv[++i] || "");
    else if (arg === "--gateway-root") args.gatewayRoot = String(argv[++i] || "");
    else if (arg === "--env-file") args.envFile = String(argv[++i] || "");
    else if (arg === "--listen") args.listen = String(argv[++i] || "");
    else if (arg === "--binary") args.binary = String(argv[++i] || "");
  }
  return args;
}

function buildChildEnv(fileEnv, listen, logDir) {
  const env = { ...process.env };
  for (const key of GATEWAY_KEYS) {
    if (present(fileEnv, key)) env[key] = fileEnv[key];
  }
  env.LISTEN_ADDR = listen;
  env.GATEWAY_LOG_DIR = env.GATEWAY_LOG_DIR || logDir;
  if (!present(env, "GATEWAY_TOKEN_EXTERNAL") && present(fileEnv, "PLATFORM_GATEWAY_TOKEN")) {
    env.GATEWAY_TOKEN_EXTERNAL = fileEnv.PLATFORM_GATEWAY_TOKEN;
  }
  if (!present(env, "GATEWAY_TOKEN_INTERNAL") && present(fileEnv, "PLATFORM_GATEWAY_INTERNAL_TOKEN")) {
    env.GATEWAY_TOKEN_INTERNAL = fileEnv.PLATFORM_GATEWAY_INTERNAL_TOKEN;
  }
  const hasToken = present(env, "GATEWAY_TOKEN_EXTERNAL")
    || present(env, "GATEWAY_TOKEN_INTERNAL")
    || present(env, "GATEWAY_AUTH_TOKENS");
  if (!hasToken) {
    throw new Error("run-host: no gateway token in env file; secret values not printed");
  }
  return env;
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const gatewayRoot = args.gatewayRoot || path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
  const appRoot = args.appRoot || gatewayRoot;
  const envFile = args.envFile || path.join(appRoot, ".env.local");
  const binary = args.binary || path.join(gatewayRoot, "bin/platform-gateway");
  if (!fs.existsSync(binary)) throw new Error(`run-host: missing binary ${binary}`);
  const logDir = path.join(appRoot, "logs/platform-gateway");
  fs.mkdirSync(logDir, { recursive: true });
  const childEnv = buildChildEnv(readEnvFile(envFile), args.listen, logDir);
  const logFd = fs.openSync(path.join(appRoot, "gateway.log"), "a");
  const child = spawn(binary, [], {
    cwd: appRoot,
    env: childEnv,
    detached: true,
    stdio: ["ignore", logFd, logFd],
  });
  child.unref();
  process.stdout.write(String(child.pid));
}

export { buildChildEnv, GATEWAY_KEYS };

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}
