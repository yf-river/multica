import { test } from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { resolveGoalTestAuditUrls, resolveGoalTestPlaywrightAPIBase } from './goal-test-audit-env.mjs';

test('audit addresses have no implicit integration fallback', () => {
  assert.throws(() => resolveGoalTestAuditUrls({}), /缺少/);
  assert.throws(() => resolveGoalTestPlaywrightAPIBase({}, {}), /缺少/);
  assert.equal(resolveGoalTestPlaywrightAPIBase({}, { REMOTE_API_URL: 'http://server/' }), 'http://server');
});

test('production metadata and private configuration work without int files', () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'multica-audit-env-'));
  try {
    fs.mkdirSync(path.join(root, '.run/deployments'), { recursive: true });
    fs.mkdirSync(path.join(root, '.run/env'), { recursive: true });
    fs.writeFileSync(path.join(root, '.run/deployments/goal-test-prod.json'), JSON.stringify({ runtime_config_file: path.join(root, '.run/env/prod-services.json'), frontend_url: 'http://host:4444', frontend_port: '4444', backend_url: 'http://host:5555' }));
    fs.writeFileSync(path.join(root, '.run/env/prod-services.json'), JSON.stringify({ server: { DATABASE_URL: 'private' }, web: {} }));
    const result = spawnSync(process.execPath, ['--input-type=module', '-e', `import {loadGoalTestEnv,resolveGoalTestAuditUrls} from ${JSON.stringify(new URL('./goal-test-audit-env.mjs', import.meta.url).href)}; console.log(JSON.stringify(resolveGoalTestAuditUrls(loadGoalTestEnv())));`], { env: { ...process.env, MULTICA_RELEASE_ROOT: root }, encoding: 'utf8' });
    assert.equal(result.status, 0, result.stderr);
    assert.deepEqual(JSON.parse(result.stdout), { frontendURL: 'http://host:4444', browserURL: 'http://127.0.0.1:4444', backendURL: 'http://host:5555' });
    fs.writeFileSync(path.join(root, '.run/deployments/goal-test-int.json'), JSON.stringify({ frontend_url: 'http://int:4446' }));
    const missing = spawnSync(process.execPath, ['--input-type=module', '-e', `import {loadGoalTestEnv} from ${JSON.stringify(new URL('./goal-test-audit-env.mjs', import.meta.url).href)}; loadGoalTestEnv();`], { env: { ...process.env, MULTICA_RELEASE_ROOT: root, GOAL_TEST_ENV: 'int' }, encoding: 'utf8' });
    assert.notEqual(missing.status, 0);
    assert.match(missing.stderr, /缺少 runtime_config_file/);

  } finally { fs.rmSync(root, { recursive: true }); }
});
