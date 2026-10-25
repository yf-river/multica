// 清理清单必须能发现新增执行型表，并区分规则、豁免与无关表。
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { test } from 'node:test';
import { loadRules, likeMatch, uncoveredTables, staleExemptions } from './verification-scrub.mjs';

const file = path.join(import.meta.dirname, '..', 'verification-scrub.json');

test('清理清单覆盖当前执行型表', () => {
  const rules = loadRules(file);
  const tables = [
    'workspace', 'issue', 'comment',
    'workspace_mcp_server', 'agent_mcp_server', 'workspace_mcp_check', 'plugin_remote_mcp_oauth_state',
    'review_config', 'issue_review',
    'supervision_config', 'supervision_batch', 'supervision_action',
    'supervision_round', 'supervision_batch_round', 'supervision_frozen_material',
    'supervision_investigation', 'supervision_finding', 'supervision_material', 'supervision_subject',
    'autopilot', 'autopilot_trigger', 'autopilot_run', 'autopilot_quota_reservation',
    'autopilot_quota_period', 'autopilot_collaborator', 'autopilot_rule_version', 'autopilot_subscriber',
    'personal_access_token', 'task_token', 'daemon_token', 'channel_binding_token', 'lark_binding_token',
    'external_credential_profile', 'plugin_secret', 'plugin_remote_mcp_secret',
    'vcs_connection', 'daemon_connection', 'user_composio_connection',
    'channel_installation', 'plugin_installation', 'plugin_installation_config', 'lark_installation',
    'github_installation', 'github_pending_installation',
    'plugin_hook_schedule', 'agent_task_queue', 'agent_runtime', 'agent', 'task_message', 'task_trace_event',
    'sys_cron_executions', 'domain_event_outbox', 'domain_event_delivery',
    'channel_inbound_message_dedup', 'lark_inbound_message_dedup', 'channel_inbound_audit', 'lark_inbound_audit',
    'upstream_sync_settings', 'upstream_sync_config', 'upstream_sync_run', 'upstream_sync_item', 'upstream_sync_commit', 'upstream_sync_absorption_task',
    'issue_delivery', 'issue_delivery_plan', 'issue_delivery_authorization', 'delivery_owner', 'delivery_publication',
    'delivery_notice', 'delivery_evidence', 'webhook_delivery', 'channel_task_delivery',
    'release_paused_task', 'release_window', 'github_pull_request_check_run',
    'issue_source_context_object_intent', 'chat_session', 'runtime_usage', 'daemon_pairing_session',
  ];
  assert.deepEqual(uncoveredTables(tables, rules), []);
  assert.deepEqual(staleExemptions(tables, rules), []);
});

test('新增执行型表没有规则时被拦截', () => {
  const rules = loadRules(file);
  assert.deepEqual(uncoveredTables(['workspace', 'review_scheduler_state'], rules), ['review_scheduler_state']);
  assert.deepEqual(uncoveredTables(['workspace', 'fresh_automation_run'], rules), ['fresh_automation_run']);
  assert.deepEqual(uncoveredTables(['future_mcp_connection_state'], rules), ['future_mcp_connection_state']);
});

test('豁免表不重复要求规则，但改名后提示清理', () => {
  const rules = loadRules(file);
  assert.deepEqual(uncoveredTables(['chat_session'], rules), []);
  assert.deepEqual(uncoveredTables(['chat_session_v2'], rules), []);
  assert.deepEqual(staleExemptions(['chat_session_v2'], rules), [
    'autopilot_collaborator', 'autopilot_quota_period', 'autopilot_rule_version', 'autopilot_subscriber',
    'chat_session', 'daemon_pairing_session', 'runtime_usage', 'upstream_sync_settings',
  ]);
});

test('LIKE 覆盖写法按字面匹配其余字符', () => {
  assert.equal(likeMatch('plugin_hook_schedule', '%hook%'), true);
  assert.equal(likeMatch('upstream_sync_run', '%_run'), true);
  assert.equal(likeMatch('run', '%_run'), false);
  assert.equal(likeMatch('delivery_evidence', 'delivery%'), true);
  assert.equal(likeMatch('a.b', 'a.b'), true);
});

test('规则格式错误时拒绝加载', () => {
  const file = path.join(os.tmpdir(), 'scrub-broken-'+process.pid+'.json');
  fs.writeFileSync(file, JSON.stringify({version: 2, rules: [{table: 'Bad-Name', sql: 'DELETE FROM x'}], coverage: {patterns: ['%token%']}}));
  assert.throws(() => loadRules(file), /清理规则格式错误/);
  fs.unlinkSync(file);
});
