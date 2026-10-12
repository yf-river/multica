import fs from 'node:fs';
import { chromium } from 'playwright';

const base = process.env.QUADRANT_URL || 'http://127.0.0.1:13682';
const profile = JSON.parse(fs.readFileSync('/root/.multica/profiles/goal-test-int/config.json'));
const headers = { Authorization: `Bearer ${profile.token}`, 'Content-Type': 'application/json', 'X-Workspace-ID': profile.workspace_id };
async function api(path, method = 'GET', body, workspace = profile.workspace_id) {
  const r = await fetch(base + path, { method, headers: { ...headers, 'X-Workspace-ID': workspace }, body: body ? JSON.stringify(body) : undefined });
  if (!r.ok) throw new Error(`${method} ${path}: ${r.status} ${await r.text()}`);
  return r.status === 204 ? null : r.json();
}
const browser = await chromium.launch({ headless: true });
const context = await browser.newContext({ viewport: { width: 1600, height: 1200 } });
await context.addInitScript(token => {
  localStorage.setItem('multica_token', token);
  localStorage.setItem('multica:chat:isOpen', 'false');
}, profile.token);
const page = await context.newPage();
const errors = [];
page.on('pageerror', e => errors.push(e.message));
page.on('response', r => { if (r.url().endsWith('/move')) console.log('MOVE', r.status()); });
const evidence = { base, checks: [], errors };
const created = [];
let extraWorkspace;
const priorities = ['important_urgent', 'important_not_urgent', 'not_important_urgent', 'not_important_not_urgent'];
const labels = ['重要且紧急', '重要不紧急', '不重要但紧急', '不重要且不紧急'];
const dir = '.run/evidence/quadrants';
fs.mkdirSync(dir, { recursive: true });
async function check(name, fn) { await fn(); evidence.checks.push(name); console.log('PASS', name); }
async function drag(card, target) {
  await card.scrollIntoViewIfNeeded();
  const a = await card.boundingBox();
  const b = await target.boundingBox();
  if (!a || !b) throw new Error('Missing drag geometry');
  await page.mouse.move(a.x + 6, a.y + 6);
  await page.mouse.down();
  await page.mouse.move(a.x + 20, a.y + 6, { steps: 5 });
  await page.waitForTimeout(200);
  await page.mouse.move(b.x + b.width / 2, b.y + b.height - 12, { steps: 30 });
  await page.waitForTimeout(300);
  await page.screenshot({ path: dir + '/drag.png' });
  await page.mouse.up();
}
async function waitIssue(id, predicate, workspace = profile.workspace_id) {
  for (let n = 0; n < 30; n++) {
    const issue = await api(`/api/issues/${id}`, "GET", undefined, workspace);
    if (predicate(issue)) return issue;
    await page.waitForTimeout(200);
  }
  throw new Error('Issue persistence assertion timed out');
}
try {
  const me = await api('/api/me');
  const issue = await api('/api/issues', 'POST', { title: '四象限验收-' + Date.now(), status: 'backlog', priority: priorities[0], assignee_type: 'member', assignee_id: me.id });
  created.push(issue.id);
  await page.goto(base + '/yf/issues');
  const card = page.locator('[data-board-card]').filter({ hasText: issue.title });
  await card.waitFor();
  const myPage = await context.newPage();
  myPage.on('pageerror', e => errors.push(e.message));
  await myPage.goto(base + '/yf/my-issues');
  await myPage.getByText(issue.title, { exact: true }).waitFor();
  for (let n = 1; n < 4; n++) {
    await check('拖入' + labels[n] + '并持久化', async () => {
      const target = page.locator('section').filter({ has: page.getByText(labels[n], { exact: true }) });
      await drag(card, target);
      await waitIssue(issue.id, i => i.priority === priorities[n] && i.status === 'backlog');
      await myPage.locator('section').filter({ has: myPage.getByText(labels[n], { exact: true }) }).getByText(issue.title, { exact: true }).waitFor();
    });
  }
  evidence.checks.push('已打开的我的任务实时同步象限');
  await myPage.close();
  await check('移到待办保留象限', async () => {
    const heading = page.getByText('待办', { exact: true }).first();
    const target = heading.locator('xpath=ancestor::div[contains(@class,"rounded-xl")][1]');
    await drag(card, target);
    await waitIssue(issue.id, i => i.status === 'todo' && i.priority === priorities[3]);
  });
  await check('移回待规划恢复象限', async () => {
    const target = page.getByText('待规划', { exact: true }).first().locator('xpath=ancestor::div[contains(@class,"rounded-xl")][1]');
    await drag(card, target);
    await waitIssue(issue.id, i => i.status === 'backlog' && i.priority === priorities[3]);
  });
  await check('我的任务展示已更新任务', async () => {
    await page.getByRole('link', { name: '我的任务', exact: true }).click();
    await page.waitForURL(base + '/yf/my-issues');
    await page.getByRole('heading', { name: '我的任务', exact: true }).waitFor();
    await page.locator('section').filter({ has: page.getByText(labels[3], { exact: true }) }).getByText(issue.title, { exact: true }).waitFor();
  });
  extraWorkspace = await api('/api/workspaces', 'POST', { name: '四象限验收临时工作区', slug: 'quadrant-check-' + Date.now(), issue_prefix: 'QCHECK' });
  await api('/api/issue-statuses', 'POST', { key: 'quadrant_check_stage', name: '象限验收状态', category: 'backlog', color: '#808080' }, extraWorkspace.id);
  const other = await api('/api/issues', 'POST', { title: '跨工作区验收-' + Date.now(), status: 'quadrant_check_stage', priority: priorities[1], assignee_type: 'member', assignee_id: me.id }, extraWorkspace.id);
  await check('我的任务跨工作区聚合', async () => {
    await page.goto(base + '/yf/my-issues');
    await page.getByRole('heading', { name: '我的任务', exact: true }).waitFor();
    await page.getByText(other.title, { exact: true }).waitFor();
    await page.getByText(issue.title, { exact: true }).waitFor();
  });
  await check('全部、已分配和我创建的均聚合跨工作区任务', async () => {
    for (const name of ['全部', '我创建的', '已分配']) {
      await page.getByRole('button', { name, exact: true }).click();
      await page.getByText(other.title, { exact: true }).waitFor();
      await page.getByText(issue.title, { exact: true }).waitFor();
    }
  });
  await check('跨工作区任务拖拽保存到所属工作区', async () => {
    const otherCard = page.locator('[data-board-card]').filter({ hasText: other.title });
    const target = page.locator('section').filter({ has: page.getByText(labels[3], { exact: true }) });
    await drag(otherCard, target);
    await waitIssue(other.id, i => i.priority === priorities[3] && i.status === 'quadrant_check_stage', extraWorkspace.id);
  });
  await check('其他工作区变更实时更新聚合看板', async () => {
    await api('/api/issues/' + other.id, 'PUT', { priority: priorities[0] }, extraWorkspace.id);
    await page.locator('section').filter({ has: page.getByText(labels[0], { exact: true }) }).getByText(other.title, { exact: true }).waitFor();
  });
  await check('跨工作区任务切换状态并恢复象限', async () => {
    const card = page.locator('[data-board-card]').filter({ hasText: other.title });
    await drag(card, page.getByText('待办', { exact: true }).first().locator('xpath=ancestor::div[contains(@class,"rounded-xl")][1]'));
    await waitIssue(other.id, i => i.status === 'todo' && i.priority === priorities[0], extraWorkspace.id);
    await drag(card, page.locator('section').filter({ has: page.getByText(labels[0], { exact: true }) }));
    await waitIssue(other.id, i => i.status === 'backlog' && i.priority === priorities[0], extraWorkspace.id);
  });
  await check('聚合任务详情链接使用所属工作区', async () => {
    const link = page.getByRole('link').filter({ hasText: other.title });
    const href = await link.getAttribute('href');
    if (!href.startsWith('/' + extraWorkspace.slug + '/issues/')) throw new Error('Wrong detail workspace');
    await link.click();
    await page.waitForURL(url => url.pathname.startsWith('/' + extraWorkspace.slug + '/issues/'));
    await page.getByText(other.title, { exact: true }).first().waitFor();
    if (!page.url().includes('/' + extraWorkspace.slug + '/issues/')) throw new Error('Wrong detail route');
    await page.goto(base + '/yf/my-issues');
  });
  await check('非成员工作区的个人任务不可聚合或读取', async () => {
    const { createRequire } = await import('node:module');
    const { Client } = createRequire(import.meta.url)('pg');
    const services = JSON.parse(fs.readFileSync('.run/env/int-services.json'));
    const db = new Client({ connectionString: services.server.DATABASE_URL });
    await db.connect();
    let removed;
    try {
      removed = (await db.query('DELETE FROM member WHERE workspace_id=$1 AND user_id=$2 RETURNING *', [extraWorkspace.id, me.id])).rows[0];
      if (!removed) throw new Error('Missing temporary membership');
      const result = await api('/api/issues/table/rows', 'POST', { query: { scope: { kind: 'my', relation: 'assigned' }, filters: {}, sort: { field: 'position', direction: 'asc' } }, group: { kind: 'none' }, page: { limit: 50, cursor: null }, hierarchy: { enabled: false }, parent_id: null });
      if (result.rows.some(row => row.issue.id === other.id)) throw new Error('Unauthorized workspace leaked');
      const denied = await fetch(base + '/api/issues/' + other.id, { headers: { ...headers, 'X-Workspace-ID': extraWorkspace.id } });
      if (![403, 404].includes(denied.status)) throw new Error('Unauthorized task readable: ' + denied.status);
    } finally {
      if (removed) await db.query('INSERT INTO member SELECT * FROM jsonb_populate_record(NULL::member, $1::jsonb)', [JSON.stringify(removed)]);
      await db.end();
    }
  });
  await page.screenshot({ path: dir + '/my-issues.png' });
  if (errors.length) throw new Error(errors.join('\n'));
} catch (e) {
  evidence.failure = String(e);
  await page.screenshot({ path: dir + '/failure.png' });
  process.exitCode = 1;
} finally {
  for (const id of created) await api('/api/issues/' + id, 'DELETE');
  if (extraWorkspace) await api('/api/workspaces/' + extraWorkspace.id, 'DELETE');
  evidence.cleanedUp = true;
  fs.writeFileSync(dir + '/browser.json', JSON.stringify(evidence, null, 2));
  await browser.close();
}
