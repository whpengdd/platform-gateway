#!/usr/bin/env node
/**
 * Spawn ./bin/platform-gateway with external environment from the app-root env file.
 * Avoids bash `source` corrupting $ and quotes. Does not print secret values.
 */
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";
import { readEnvFile } from "./env-file.mjs";

function parseArgs(argv) {
  const args = { appRoot: "", gatewayRoot: "", envFile: "", binary: "", configFile: "" };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--app-root") args.appRoot = String(argv[++i] || "");
    else if (arg === "--gateway-root") args.gatewayRoot = String(argv[++i] || "");
    else if (arg === "--env-file") args.envFile = String(argv[++i] || "");
    else if (arg === "--config-file") args.configFile = String(argv[++i] || "");
    else if (arg === "--binary") args.binary = String(argv[++i] || "");
    else throw new Error(`run-host: unknown argument ${arg}`);
    if (!argv[i] || argv[i].startsWith("--")) throw new Error("run-host: missing argument value");
  }
  return args;
}

function buildChildEnv(fileEnv) {
  return { ...process.env, ...fileEnv };
}
export function resolveConfig(appRoot, explicit, env) {
  return path.resolve(appRoot, explicit || env.GATEWAY_CONFIG_FILE || "config.json");
}

function main() {
  const args = parseArgs(process.argv.slice(2));
  const gatewayRoot = args.gatewayRoot || path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
  const appRoot = path.resolve(args.appRoot || gatewayRoot);
  const envFile = args.envFile || path.join(appRoot, ".env.local");
  const binary = args.binary || path.join(gatewayRoot, "bin/platform-gateway");
  if (!fs.existsSync(binary)) throw new Error(`run-host: missing binary ${binary}`);
  const childEnv = buildChildEnv(readEnvFile(envFile));
  childEnv.GATEWAY_CONFIG_FILE = resolveConfig(appRoot, args.configFile, childEnv);
  if (!fs.statSync(childEnv.GATEWAY_CONFIG_FILE).isFile()) throw new Error("run-host: configuration source must be a file");
  fs.accessSync(childEnv.GATEWAY_CONFIG_FILE, fs.constants.R_OK);
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

export { buildChildEnv };

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    main();
  } catch (error) {
    console.error(error instanceof Error ? error.message : String(error));
    process.exit(1);
  }
}
