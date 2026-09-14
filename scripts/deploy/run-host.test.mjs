import assert from 'node:assert/strict';
import test from 'node:test';
import { buildChildEnv } from './run-host.mjs';
test('passes file path and upstream credentials without caller token mapping', () => {
 const env = buildChildEnv({ GATEWAY_AUTH_FILE: '/tmp/config.json', PLATFORM_GATEWAY_TOKEN: 'caller-secret', PLATFORM_GATEWAY_INTERNAL_TOKEN: 'caller-internal', CK_LOGS_BASIC_USER: 'u', CK_LOGS_BASIC_PASS: 'p', JIRA_API_TOKEN: 'upstream' }, '127.0.0.1:8091', '/tmp/logs');
 assert.equal(env.GATEWAY_AUTH_FILE, '/tmp/config.json');
 assert.equal(env.JIRA_API_TOKEN, 'upstream');
 assert.equal(env.GATEWAY_TOKEN_EXTERNAL, undefined);
 assert.equal(env.PLATFORM_GATEWAY_TOKEN, undefined);
 assert.equal(env.LISTEN_ADDR, '127.0.0.1:8091');
});
test('rejects legacy gateway credentials without leaking their values', () => {
 for (const key of ['GATEWAY_TOKEN_EXTERNAL', 'GATEWAY_TOKEN_INTERNAL', 'GATEWAY_AUTH_TOKENS', 'GATEWAY_JIRA_TOKENS', 'GATEWAY_JIRA_TOKENS_FILE']) {
  assert.throws(() => buildChildEnv({[key]: 'secret-sensitive'}, '127.0.0.1:8091', '/tmp/logs'), error => /legacy/.test(error.message) && !error.message.includes('secret-sensitive'));
 }
});
test('default path is resolved by the host launcher, no token environment required', () => {
 assert.equal(buildChildEnv({}, '127.0.0.1:8091', '/tmp/logs').GATEWAY_TOKEN_INTERNAL, undefined);
});
