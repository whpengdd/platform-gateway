// Emit a static migration overlay. !reset removes only dedicated mappings;
// Compose still delivers env_file and unrelated environment entries unchanged.
import fs from 'node:fs';
import path from 'node:path';
const [stack, source] = process.argv.slice(2);
if (!['install', '149'].includes(stack) || !source || !fs.statSync(source).isFile()) throw new Error('valid stack and configuration file required');
const service = stack === '149' ? 'rag-explorer-platform-gateway' : 'platform-gateway';
process.stdout.write(`services:
  ${service}:
    working_dir: /
    environment:
      GATEWAY_CONFIG_FILE: /config.json
      GATEWAY_AUTH_FILE: !reset null
      GATEWAY_TOKEN_EXTERNAL: !reset null
      GATEWAY_TOKEN_INTERNAL: !reset null
      GATEWAY_AUTH_TOKENS: !reset null
      GATEWAY_JIRA_TOKENS: !reset null
      GATEWAY_JIRA_TOKENS_FILE: !reset null
      JIRA_BASE_URL: !reset null
      JIRA_API_TOKEN: !reset null
      JIRA_BASIC_USER: !reset null
      JIRA_BASIC_PASSWORD: !reset null
      CK_LOGS_BASE_URL: !reset null
      CK_LOGS_BASIC_USER: !reset null
      CK_LOGS_BASIC_PASS: !reset null
      CK_LOGS_INDEX: !reset null
      CK_LOGS_TIMEOUT_MS: !reset null
      CKLOGS_MAX_CONCURRENCY: !reset null
      CKLOGS_QUEUE_SIZE: !reset null
      CKLOGS_QUEUE_WAIT_MS: !reset null
      LISTEN_ADDR: !reset null
      GATEWAY_ALLOW_CIDRS: !reset null
      GATEWAY_LOG_DIR: !reset null
    volumes:
      - type: bind
        source: ${JSON.stringify(path.resolve(source)).replaceAll('$', '$$')}
        target: /config.json
        read_only: true
        bind:
          create_host_path: false
`);
