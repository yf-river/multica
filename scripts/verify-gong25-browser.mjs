import fs from 'node:fs';
import path from 'node:path';
import { expect } from '@playwright/test';
import { loadProductionDeployment, repoRoot } from './lib/goal-test-audit-env.mjs';
import { launchGoalTestBrowser } from './lib/goal-test-browser-audit.mjs';

const deployment = loadProductionDeployment();
if (process.env.GOAL_TEST_ENV !== 'int') throw Error('Feature acceptance changes preferences: int only');
const profile = JSON.parse(fs.readFileSync(deployment.daemon_profile_path));
const { browser, context } = await launchGoalTestBrowser(deployment.frontend_url, profile.token);
const base = deployment.frontend_url;
const errors = [], failures = [], checks = [];
const read = async (url, ws) => {
  const response = await context.request.get(base + url, { headers: ws ? { 'X-Workspace-ID': ws } : {} });
  if (!response.ok()) throw Error(`${url}: ${response.status()}`);
  return response.json();
};
try {
  const workspaces = await read('/api/workspaces');
  const workspace = workspaces.find(ws => ws.id === profile.workspace_id) || workspaces[0];
  if (!workspace) throw Error('int workspace missing');
  const page = await context.newPage();
  page.on('pageerror', e => errors.push(e.message));
  page.on('response', r => { if (r.status() >= 400 && r.url().startsWith(base)) failures.push({url: new URL(r.url()).pathname, status:r.status()}); });
  const settingsURL = `${base}/${workspace.slug}/settings?tab=workspace`;
  await page.goto(settingsURL, {waitUntil:'networkidle'});
  const sidebar = page.locator('[data-slot="sidebar"]');
  await expect(sidebar.getByRole('link', {name:'聊天', exact:true})).toBeVisible();
  await expect(sidebar.getByText('我的任务', {exact:true})).toHaveCount(0);
  await expect(page.getByText('工作区菜单', {exact:true})).toBeVisible();
  const toggle = page.getByRole('switch', {name:'项目', exact:true});
  const originallyEnabled = await toggle.isChecked();
  const change = async () => {
    const response = page.waitForResponse(r => r.request().method() === 'PATCH' && new URL(r.url()).pathname === `/api/workspaces/${workspace.id}`);
    await toggle.click();
    const saved = await response;
    if (!saved.ok()) throw Error(`menu save: ${saved.status()}`);
    await expect(toggle).toBeEnabled();
  };
  try {
    await change();
    await expect(toggle).toBeChecked({checked: !originallyEnabled});
    await expect(sidebar.getByRole('link', {name:'项目', exact:true})).toHaveCount(originallyEnabled ? 0 : 1);
    await page.reload({waitUntil:'networkidle'});
    await expect(toggle).toBeChecked({checked: !originallyEnabled});
    await expect(sidebar.getByRole('link', {name:'聊天', exact:true})).toBeVisible();
    if (workspaces.length > 1) {
      const other = workspaces.find(ws => ws.id !== workspace.id);
      await page.goto(`${base}/${other.slug}/settings?tab=workspace`, {waitUntil:'networkidle'});
      await expect(page.getByRole('switch', {name:'项目', exact:true})).toBeChecked({checked: !other.settings?.hidden_menu_items?.includes('projects')});
      await page.goto(settingsURL, {waitUntil:'networkidle'});
    }
    checks.push('workspace menu save, immediate navigation update, refresh persistence and workspace isolation');
  } finally {
    await page.goto(settingsURL, {waitUntil:'networkidle'});
    if (await toggle.isChecked() !== originallyEnabled) await change();
  }
  await toggle.focus();
  expect(await toggle.evaluate(el => el === document.activeElement)).toBe(true);
  await page.keyboard.press('Tab');
  expect(await page.evaluate(() => document.activeElement?.getAttribute('role'))).toBe('switch');
  checks.push('keyboard switch navigation and focus');
  await page.goto(`${base}/${workspace.slug}/my-issues`, {waitUntil:'networkidle'});
  await expect(page).toHaveURL(`${base}/${workspace.slug}/issues`);
  checks.push('retired personal task bookmark redirects to workspace tasks');
  const issuePage = await read('/api/issues?limit=30', workspace.id);
  const issues = Array.isArray(issuePage) ? issuePage : issuePage.issues;
  if (!issues?.length) throw Error('int needs an existing issue for scroll acceptance');
  const selected = issues.slice(0, 2);
  for (const issue of [...selected, selected[0]]) {
    await page.goto(`${base}/${workspace.slug}/issues/${issue.id}`, {waitUntil:'networkidle'});
    const scroller = page.locator(`[data-tab-scroll-root="main:${issue.id}"]`);
    await expect(scroller).toBeVisible();
    await expect.poll(() => scroller.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop)).toBeLessThan(8);
    await scroller.evaluate(el => { el.scrollTop = 0; });
    await page.goto(`${base}/${workspace.slug}/issues`, {waitUntil:'networkidle'});
  }
  checks.push('opening, switching and reopening task details starts at the bottom of the message panel');
  await page.setViewportSize({width:390,height:844});
  await page.goto(settingsURL, {waitUntil:'networkidle'});
  await expect(toggle).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.locator('[data-slot="sidebar-trigger"]').first().click();
  await expect(page.getByRole('link',{name:'聊天',exact:true}).last()).toBeVisible();
  const directory = path.join(repoRoot,'.run/evidence/gong-25',deployment.release_id);
  fs.mkdirSync(directory,{recursive:true});
  await page.screenshot({path:path.join(directory,'mobile.png'),fullPage:true});
  checks.push('390px menu settings, persistent chat and no horizontal overflow');
  if (errors.length || failures.length) throw Error(JSON.stringify({errors,failures}));
  const evidence = {release_id:deployment.release_id, checks, errors, failures};
  fs.writeFileSync(path.join(directory,'feature.json'),JSON.stringify(evidence,null,2));
  console.log(JSON.stringify(evidence));
} finally { await browser.close(); }
