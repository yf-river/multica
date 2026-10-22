import fs from 'node:fs';
import path from 'node:path';
import { expect } from '@playwright/test';
import { loadProductionDeployment, repoRoot } from './lib/goal-test-audit-env.mjs';
import { launchGoalTestBrowser } from './lib/goal-test-browser-audit.mjs';

const deployment = loadProductionDeployment();
const environment = process.env.GOAL_TEST_ENV || 'prod';
const profile = JSON.parse(fs.readFileSync(deployment.daemon_profile_path, 'utf8'));
const issues = process.argv.slice(2);
if (!issues.length) throw Error('请指定需要核对的现有任务编号');
const { browser, context } = await launchGoalTestBrowser(deployment.frontend_url, profile.token);
const directory = path.join(repoRoot, '.run/evidence/delivery-browser', deployment.release_id);
fs.mkdirSync(directory, { recursive: true });
const errors = [];
const failures = [];
const checked = [];
async function read(route, workspace) {
  const response = await context.request.get(deployment.frontend_url + '/api/' + route, {
    headers: workspace ? { 'X-Workspace-ID': workspace } : {},
  });
  if (!response.ok()) throw Error(`${route}: HTTP ${response.status()}`);
  return response.json();
}
try {
  if ((await read('config')).environment_id !== environment) throw Error('环境身份不匹配');
  const workspaces = await read('workspaces');
  const workspace = workspaces.find(item => item.id === profile.workspace_id);
  if (!workspace) throw Error('现有 profile 工作区不可访问');
  const page = await context.newPage();
  page.on('pageerror', error => errors.push(error.message));
  page.on('response', response => {
    if (response.status() >= 400 && new URL(response.url()).origin === new URL(deployment.frontend_url).origin)
      failures.push({ path: new URL(response.url()).pathname, status: response.status() });
  });
  for (const identifier of issues) {
    const issue = await read('issues/' + encodeURIComponent(identifier), workspace.id);
    await page.goto(`${deployment.frontend_url}/${workspace.slug}/issues/${issue.id}`, { waitUntil: 'networkidle' });
    await expect(page.getByText(issue.title, { exact: true }).first()).toBeVisible();
    const history = page.getByRole('button', { name: /显示历史运行|Show historical runs/ });
    if (await history.count()) await history.first().click();
    await page.screenshot({ path: path.join(directory, `${issue.identifier}.png`), fullPage: true });
    await page.reload({ waitUntil: 'networkidle' });
    await expect(page.getByText(issue.title, { exact: true }).first()).toBeVisible();
    checked.push({ id: issue.id, identifier: issue.identifier, status: issue.status, screenshot: `${issue.identifier}.png` });
  }
  if (errors.length || failures.length) throw Error(JSON.stringify({ errors, failures }));
  const evidence = { environment, release_id: deployment.release_id, commit: deployment.commit, checked, errors, failures, verified_at: new Date().toISOString() };
  fs.writeFileSync(path.join(directory, 'browser.json'), JSON.stringify(evidence, null, 2), { mode: 0o600 });
  console.log(JSON.stringify(evidence));
} finally {
  await browser.close();
}
