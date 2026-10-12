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
  const environment = process.env.GOAL_TEST_ENV === 'int' ? 'int' : 'prod';
  if (config.environment_id !== environment) throw Error('wrong environment identity');
  const me = await read('/api/me');
  const workspaces = await read('/api/workspaces');
  if (!workspaces.length) throw Error('requires existing production workspace');
  const page = await context.newPage();
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
    const agents = await read(`/api/agents?workspace_id=${workspace.id}`, workspace.id);
    const mika = agents.find(item => item.system_key === 'mika' && !item.archived_at);
    if (mika) {
      const entry = page.locator('[data-slot="sidebar"]').getByRole('button', { name: mika.name, exact: true });
      await expect(entry).toBeVisible();
      const session = sessions.find(item => item.agent_id === mika.id && item.status === 'active');
      if (session) { await entry.click(); await expect(page).toHaveURL(new RegExp(`session=${session.id}`)); }
      else unavailable.push('Mika has no existing session; acceptance does not create user chat');
    } else unavailable.push('Mika not created in production workspace');
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
  const evidence = { release_id: deployment.release_id, environment, checks, unavailable, page_errors: 0, failed_requests: 0 };
  fs.writeFileSync(path.join(directory, 'browser.json'), JSON.stringify(evidence, null, 2), { mode: 0o600 });
  console.log(JSON.stringify(evidence));
} finally { await browser.close(); }
