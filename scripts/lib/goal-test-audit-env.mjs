import { readFileSync } from 'node:fs';
import path from 'node:path';
import process from 'node:process';

export const repoRoot = path.resolve(process.env.MULTICA_RELEASE_ROOT || path.join(import.meta.dirname, '../..'));

export function loadProductionDeployment() {
  const environment = process.env.GOAL_TEST_ENV || 'prod';
  if (!['int', 'prod', 'verify-1', 'verify-2', 'verify-3'].includes(environment)) throw Error('未知运行环境: ' + environment);
  return JSON.parse(readFileSync(path.join(repoRoot, `.run/deployments/goal-test-${environment}.json`), 'utf8'));
}

export function loadGoalTestEnv() {
  const deployment = loadProductionDeployment();
  if (!deployment.runtime_config_file) throw Error('部署记录缺少 runtime_config_file');
  const config = JSON.parse(readFileSync(deployment.runtime_config_file, 'utf8'));
  const environment = process.env.GOAL_TEST_ENV || 'prod';
  if (!['int', 'prod', 'verify-1', 'verify-2', 'verify-3'].includes(environment)) throw Error('未知运行环境: ' + environment);
  return { ...process.env, ...config.server, ...config.web, GOAL_TEST_ENV: environment,
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
