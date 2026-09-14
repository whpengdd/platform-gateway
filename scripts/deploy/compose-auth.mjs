// Emit an ephemeral override, without modifying the caller repository.
import fs from 'node:fs';
import path from 'node:path';
const [stack, source] = process.argv.slice(2);
if (!['install', '149'].includes(stack) || !source || !fs.statSync(source).isFile()) throw new Error('valid stack and authorization file required');
const service = stack === '149' ? 'rag-explorer-platform-gateway' : 'platform-gateway';
const environment = { GATEWAY_AUTH_FILE: '/config.json' };
for (const key of ['GATEWAY_TOKEN_EXTERNAL', 'GATEWAY_TOKEN_INTERNAL', 'GATEWAY_AUTH_TOKENS', 'GATEWAY_JIRA_TOKENS', 'GATEWAY_JIRA_TOKENS_FILE']) environment[key] = '';
// Compose resolves these references from the selected project environment.
// Keep values out of this temporary file and let the gateway validate auth mode.
for (const key of ['JIRA_BASE_URL', 'JIRA_API_TOKEN', 'JIRA_BASIC_USER', 'JIRA_BASIC_PASSWORD']) environment[key] = '${' + key + ':-}';
// JSON is valid YAML. Compose merges this mount by container target.
process.stdout.write(JSON.stringify({ services: { [service]: { working_dir: '/', environment, volumes: [{ type: 'bind', source: path.resolve(source), target: '/config.json', read_only: true, bind: { create_host_path: false } }] } } }));
