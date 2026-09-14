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
  "GATEWAY_AUTH_FILE",
  "JIRA_BASE_URL",
  "JIRA_API_TOKEN",
  "JIRA_BASIC_USER",
  "JIRA_BASIC_PASSWORD",
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
];

function parseArgs(argv) {
  const args = { appRoot: "", gatewayRoot: "", envFile: "", listen: "127.0.0.1:8091", binary: "", authFile: "" };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--app-root") args.appRoot = String(argv[++i] || "");
    else if (arg === "--gateway-root") args.gatewayRoot = String(argv[++i] || "");
    else if (arg === "--env-file") args.envFile = String(argv[++i] || "");
    else if (arg === "--listen") args.listen = String(argv[++i] || "");
    else if (arg === "--auth-file") args.authFile = String(argv[++i] || "");
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
  for (const key of ["GATEWAY_TOKEN_EXTERNAL", "GATEWAY_TOKEN_INTERNAL", "GATEWAY_AUTH_TOKENS", "GATEWAY_JIRA_TOKENS", "GATEWAY_JIRA_TOKENS_FILE"]) {
    if (String(fileEnv[key] || "") !== "" || String(env[key] || "") !== "") throw new Error("run-host: remove legacy gateway token environment; use config.json");
    delete env[key];
  }
  delete env.PLATFORM_GATEWAY_TOKEN;
  delete env.PLATFORM_GATEWAY_INTERNAL_TOKEN;
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
  childEnv.GATEWAY_AUTH_FILE = path.resolve(appRoot, args.authFile || childEnv.GATEWAY_AUTH_FILE || "config.json");
  if (!fs.statSync(childEnv.GATEWAY_AUTH_FILE).isFile()) throw new Error("run-host: authorization source must be a file");
  fs.accessSync(childEnv.GATEWAY_AUTH_FILE, fs.constants.R_OK);
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
