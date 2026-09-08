/**
 * Raw KEY=VALUE env files (compose env_file / node --env-file).
 * Values after the first '=' are literal. Never log secret values.
 */
import fs from "node:fs";
import path from "node:path";

export function parseRawEnv(source) {
  const out = {};
  for (const rawLine of String(source ?? "").split("\n")) {
    const line = rawLine.endsWith("\r") ? rawLine.slice(0, -1) : rawLine;
    if (!line.trim() || /^\s*#/.test(line)) continue;
    const separator = line.indexOf("=");
    if (separator <= 0) continue;
    const key = line.slice(0, separator);
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(key)) continue;
    out[key] = line.slice(separator + 1);
  }
  return out;
}

export function readEnvFile(filePath) {
  if (!filePath || !fs.existsSync(filePath)) return {};
  return parseRawEnv(fs.readFileSync(filePath, "utf8"));
}

export function tokenList(value) {
  return String(value ?? "").split(",").map((part) => part.trim()).filter(Boolean);
}

export function present(environment, key) {
  return String(environment?.[key] ?? "").trim() !== "";
}

/**
 * Replace existing KEY= lines or append. Does not rewrite unrelated lines.
 * Returns { wrote: string[], skipped: string[] } of keys (not values).
 */
export function upsertEnvFile(filePath, updates) {
  const entries = Object.entries(updates || {}).filter(([, value]) => value != null && String(value) !== "");
  if (!filePath) throw new Error("upsertEnvFile: filePath required");
  const dir = path.dirname(filePath);
  fs.mkdirSync(dir, { recursive: true });
  const original = fs.existsSync(filePath) ? fs.readFileSync(filePath, "utf8") : "";
  const lines = original ? original.split("\n") : [];
  if (lines.length && lines[lines.length - 1] === "") lines.pop();
  const wrote = [];
  const skipped = [];
  const remaining = new Map(entries.map(([key, value]) => [key, String(value)]));

  const next = lines.map((line) => {
    if (!line.trim() || /^\s*#/.test(line) || !line.includes("=")) return line;
    const key = line.slice(0, line.indexOf("="));
    if (!remaining.has(key)) return line;
    const current = line.slice(line.indexOf("=") + 1);
    const wanted = remaining.get(key);
    remaining.delete(key);
    if (current === wanted) {
      skipped.push(key);
      return line;
    }
    wrote.push(key);
    return `${key}=${wanted}`;
  });

  for (const [key, value] of remaining) {
    next.push(`${key}=${value}`);
    wrote.push(key);
  }

  let body = next.join("\n");
  if (body && !body.endsWith("\n")) body += "\n";
  if (wrote.length) fs.writeFileSync(filePath, body, { encoding: "utf8", mode: 0o600 });
  return { wrote, skipped, path: filePath };
}
