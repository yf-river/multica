import { readFileSync } from 'node:fs';
import path from 'node:path';
import process from 'node:process';

export const repoRoot = path.resolve(process.env.MULTICA_RELEASE_ROOT || path.join(import.meta.dirname, '../..'));

export function loadProductionDeployment() {
  return JSON.parse(readFileSync(path.join(repoRoot, '.run/deployments/goal-test-prod.json'), 'utf8'));
}

export function loadGoalTestEnv() {
  const deployment = loadProductionDeployment();
  const config = JSON.parse(readFileSync(deployment.runtime_config_file || path.join(repoRoot, '.run/env/prod-services.json'), 'utf8'));
  return { ...process.env, ...config.server, ...config.web, GOAL_TEST_ENV: 'prod',
    FRONTEND_ORIGIN: deployment.frontend_url, FRONTEND_PORT: deployment.frontend_port,
    REMOTE_API_URL: deployment.backend_url };
}

export function readGoalTestEnvFile(file) {
  const values = {};
  for (const raw of readFileSync(file, 'utf8').split(/\r?\n/)) {
    const match = raw.trim().match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) values[match[1]] = match[2].replace(/^['"]|['"]$/g, '');
  }
  return values;
}

export function resolveGoalTestAuditUrls(env) {
  if (!env.FRONTEND_ORIGIN || !env.FRONTEND_PORT || !env.REMOTE_API_URL) throw Error('缺少实际部署地址，不会使用默认联调地址');
  return {
    frontendURL: trimSlash(env.FRONTEND_ORIGIN),
    browserURL: trimSlash(process.env.GOAL_TEST_BROWSER_URL || `http://127.0.0.1:${env.FRONTEND_PORT}`),
    backendURL: trimSlash(process.env.GOAL_TEST_BACKEND_URL || env.REMOTE_API_URL),
  };
}

export function resolveGoalTestPlaywrightAPIBase(processEnv, runEnv) {
  const value = processEnv.GOAL_TEST_BACKEND_URL || runEnv.NEXT_PUBLIC_API_URL || runEnv.REMOTE_API_URL ||
    (runEnv.PORT ? `http://127.0.0.1:${runEnv.PORT}` : '') || processEnv.NEXT_PUBLIC_API_URL;
  if (!value) throw Error('缺少实际 API 地址');
  return trimSlash(value);
}

function trimSlash(value) { return value.replace(/\/+$/, ''); }
