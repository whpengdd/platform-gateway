// Deployment probes read the file but never override application settings.
import fs from 'node:fs';
import { readEnvFile } from './env-file.mjs';
import { resolveConfig } from './run-host.mjs';
try {
  const [operation, appRoot, explicit = '', envFile = ''] = process.argv.slice(2);
  const file = resolveConfig(appRoot, explicit, { ...process.env, ...readEnvFile(envFile) });
  if (operation === 'path') process.stdout.write(file);
  else if (operation === 'health') {
    const config = JSON.parse(fs.readFileSync(file, 'utf8'));
    const listen = config.server?.listenAddr ?? ':8091';
    const match = /^(.*):([0-9]+)$/.exec(listen);
    if (!match || +match[2] < 1 || +match[2] > 65535) throw new Error('invalid server.listenAddr');
    let host = match[1];
    if (!host || host === '0.0.0.0') host = '127.0.0.1';
    if (host === '[::]') host = '[::1]';
    process.stdout.write(`http://${host}:${match[2]}/health`);
  } else throw new Error('unknown config operation');
} catch {
  console.error("invalid or unreadable gateway configuration for deployment probe");
  process.exit(1);
}
