import fs from 'node:fs';
import path from 'node:path';
import { expect } from '@playwright/test';
import { loadProductionDeployment, repoRoot } from './lib/goal-test-audit-env.mjs';
import { launchGoalTestBrowser, loginGoalTest } from './lib/goal-test-browser-audit.mjs';

const deployment = loadProductionDeployment();
const profile = JSON.parse(fs.readFileSync(deployment.daemon_profile_path));
const token = process.env.MULTICA_TEST_ACCOUNT && process.env.MULTICA_TEST_PASSWORD
  ? await loginGoalTest({ backendURL: deployment.backend_url, account: process.env.MULTICA_TEST_ACCOUNT, password: process.env.MULTICA_TEST_PASSWORD })
  : profile.token;
const base = deployment.frontend_url;
const { browser, context } = await launchGoalTestBrowser(base, token);
const errors = [];
const failures = [];
const checks = [];
const unavailable = [];
const read = async (url, workspace) => {
  const response = await context.request.get(base + url, { headers: workspace ? { 'X-Workspace-ID': workspace } : {} });
  if (!response.ok()) throw Error(`${url}: ${response.status()}`);
  return response.json();
};
try {
  const config = await read('/api/config');
  const environment = process.env.GOAL_TEST_ENV || 'prod';
  if (config.environment_id !== environment) throw Error('wrong environment identity');
  const me = await read('/api/me');
  const workspaces = await read('/api/workspaces');
  if (!workspaces.length) throw Error('requires existing production workspace');
  const page = await context.newPage();
  // 开发模式首次按路由编译，仍须完成原有页面、网络与业务断言。
  if (deployment.frontend_mode === 'dev') page.setDefaultNavigationTimeout(180_000);
  page.on('pageerror', error => errors.push(error.message));
  page.on('console', message => { if (message.type() === 'error') errors.push(message.text()); });
  page.on('response', response => { if (response.status() >= 400 && new URL(response.url()).origin === new URL(base).origin) failures.push({ path: new URL(response.url()).pathname, status: response.status() }); });
  for (const workspace of workspaces) {
    for (const route of ['inbox', 'issues', 'agents', 'chat', 'health', 'meals', 'cooking', 'goals']) {
      const response = await page.goto(`${base}/${workspace.slug}/${route}`, { waitUntil: 'networkidle' });
      if (!response?.ok()) throw Error(`${route}: ${response?.status()}`);
      await expect(page.locator('[data-slot="sidebar-inset"]')).toBeVisible();
      if (process.env.GOAL_TEST_ENV !== 'int') await expect(page.getByText('联调环境', { exact: true })).toHaveCount(0);
    }
    const repositories = await read(`/api/workspaces/${workspace.id}/repos/details`, workspace.id);
    const repoIds = new Set(repositories.repos.map(repo => repo.id));
    for (const project of repositories.projects) for (const resource of project.resources) {
      if (["github_repo", "gongfeng_repo"].includes(resource.resource_type) && !repoIds.has(resource.resource_ref.repository_id)) throw Error("project repository missing from registry");
    }
    await page.goto(`${base}/${workspace.slug}/settings?tab=repositories`, {waitUntil: 'networkidle'});
    await expect(page.getByPlaceholder('搜索仓库、组织或路径')).toBeVisible();
    checks.push('unified repository registry and project bindings');
    await page.reload({ waitUntil: 'networkidle' });
    if ((await read('/api/me')).id !== me.id) throw Error('identity changed after refresh');
    const sessions = await read('/api/chat/sessions?status=all', workspace.id);
    const existing = sessions.find(item => item.status === 'active');
    if (existing) {
      await read(`/api/chat/sessions/${existing.id}/messages`, workspace.id);
      await page.goto(`${base}/${workspace.slug}/chat?session=${existing.id}`, { waitUntil: 'networkidle' });
      await expect(page).toHaveURL(new RegExp(`session=${existing.id}`));
      checks.push('existing chat route and history requests');
    } else unavailable.push('no existing chat session in workspace');
    const reports = await read('/api/health/reports', workspace.id);
    if (reports.length) {
      const response = await context.request.get(`${base}/api/health/reports/${reports[0].id}/file?workspace_id=${workspace.id}`);
      if (!response.ok() || !(await response.body()).length) throw Error('report original unavailable');
      checks.push('private report original accessible');
    } else unavailable.push('no production health report to verify original');
    const chatEntry = page.locator('[data-slot="sidebar"]').getByRole('link', { name: '聊天', exact: true });
    if (await chatEntry.count()) {
      await expect(chatEntry).toBeVisible();
      await chatEntry.click();
      await expect(page).toHaveURL(new RegExp(`/${workspace.slug}/chat$`));
      await expect(page.locator('[data-slot="sidebar"]').getByText('我的任务', { exact: true })).toHaveCount(0);
      checks.push('persistent generic Chat navigation and retired personal tasks entry');
    } else {
      // The fixed controller also validates the old program during rollback.
      const agents = await read('/api/agents', workspace.id);
      const mika = agents.find(item => item.system_key === 'mika' && !item.archived_at);
      if (mika) await expect(page.locator('[data-slot="sidebar"]').getByRole('button', { name: mika.name, exact: true })).toBeVisible();
      checks.push('legacy navigation during rollback');
    }
    const websocket = await page.evaluate(slug => new Promise(resolve => {
      const url = new URL('/ws', location.href);url.protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';url.searchParams.set('workspace_slug', slug);
      const socket = new WebSocket(url);
      const timer = setTimeout(() => { socket.close(); resolve(false); }, 5000);
      socket.onopen = () => { clearTimeout(timer); socket.close(); resolve(true); };
      socket.onerror = () => { clearTimeout(timer); resolve(false); };
    }), workspace.slug);
    if (!websocket) throw Error('authenticated WebSocket upgrade failed');
    checks.push('workspace main routes, refresh and authenticated WebSocket');
  }
  const workspace = workspaces[0];
  const cookies = await context.cookies(base);
  const csrf = cookies.find(item => item.name === `multica_csrf_${environment}`)?.value;
  const patch = await context.request.patch(base + '/api/me', { headers: { 'X-CSRF-Token': csrf }, data: { name: me.name } });
  if (!patch.ok()) throw Error(`CSRF authenticated write: ${patch.status()}`);
  checks.push(`${environment} cookie authentication and CSRF`);
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto(`${base}/${workspace.slug}/health`, { waitUntil: 'networkidle' });
  if (!await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)) throw Error('390px overflow');
  await page.locator('[data-slot="sidebar-trigger"]').first().click();
  await expect(page.getByRole('link', { name: '饮食', exact: true })).toBeVisible();
  checks.push('390px health and sidebar');
  if (errors.length || failures.length) throw Error(JSON.stringify({ page_errors: errors.length, failed_requests: failures }));
  const directory = path.join(repoRoot, `.run/evidence/${environment}-release`, deployment.release_id);
  fs.mkdirSync(directory, { recursive: true });
  const evidence = { release_id: deployment.release_id, source_digest: deployment.source_digest, verified_at: new Date().toISOString(), environment, checks, unavailable, page_errors: 0, failed_requests: 0 };
  fs.writeFileSync(path.join(directory, 'browser.json'), JSON.stringify(evidence, null, 2), { mode: 0o600 });
  console.log(JSON.stringify(evidence));
} finally { await browser.close(); }
