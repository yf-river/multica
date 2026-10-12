import fs from "node:fs";
import path from "node:path";
import { loadGoalTestBrowserAudit, loginGoalTest, launchGoalTestBrowser } from "./lib/goal-test-browser-audit.mjs";
import { repoRoot } from './lib/goal-test-audit-env.mjs';

// 计时从浏览器真实点击开始；请求正文、账号密码和页面内容不写入证据。
const phase = process.argv[2] || "before";
const cfg = await loadGoalTestBrowserAudit();
const metadataPath = path.resolve(process.env.MENU_METADATA_PATH || path.join(repoRoot, '.run/deployments/goal-test-prod.json'));
const deployment = JSON.parse(fs.readFileSync(metadataPath, "utf8"));
const coldRuns = Number(process.env.MENU_COLD_RUNS || 3);
const warmRuns = Number(process.env.MENU_WARM_RUNS || 10);
const latencyMs = Number(process.env.MENU_LATENCY_MS || 0);
const directory = path.resolve(".run/evidence/menu-performance");
fs.mkdirSync(directory, { recursive: true });
const token = await loginGoalTest(cfg);
const { browser, context: discovery } = await launchGoalTestBrowser(cfg.browserURL, token);
const cookies = await discovery.cookies();
const result = { phase, generated_at: new Date().toISOString(), release_id: deployment.release_id,
  browser_url: cfg.browserURL, viewport: { width: 1440, height: 900 }, cold_runs: coldRuns, warm_runs: warmRuns,
  latency_ms: latencyMs, frontend_source_url: process.env.MENU_FRONTEND_SOURCE_URL || cfg.browserURL,
  menus: [], samples: [], errors: [], verified_frontend_versions: [] };
let slug;

function checkVersion() {
  if (JSON.parse(fs.readFileSync(deployment.primary_metadata_path || metadataPath, "utf8")).release_id !== (deployment.primary_release_id || deployment.release_id)) {
    throw new Error("Deployment release changed during measurement; discard this run");
  }
}

async function instrument(context) {
  if (process.env.MENU_FRONTEND_SOURCE_URL) {
    // 隔离页面资源使用原联调浏览器地址；API、认证、上传和实时连接走原服务。
    await context.route(`${cfg.browserURL}/**`, async route => {
      const url = new URL(route.request().url());
      if (/^\/(api|auth|ws|uploads|health|v1|docs)(\/|$)/.test(url.pathname)) return route.continue();
      const response = await route.fetch({ url: new URL(url.pathname + url.search, process.env.MENU_FRONTEND_SOURCE_URL).href });
      await route.fulfill({ response });
    });
  }
  await context.addInitScript(() => {
    localStorage.setItem("multica:chat:isOpen", "false");
    window.__menuAudit = { click: null, longTasks: [], events: [] };
    document.addEventListener("click", e => {
      const link = e.target.closest?.('[data-sidebar="sidebar"] a[href]');
      if (link) window.__menuAudit.click = { href: link.getAttribute("href"), at: performance.now() };
    }, true);
    new PerformanceObserver(list => {
      for (const e of list.getEntries()) window.__menuAudit.longTasks.push({ start: e.startTime, ms: e.duration });
    }).observe({ type: "longtask", buffered: true });
    try { new PerformanceObserver(list => {
      for (const e of list.getEntries()) if (e.name === "click") window.__menuAudit.events.push({ start: e.startTime, ms: e.duration });
    }).observe({ type: "event", durationThreshold: 16, buffered: true }); } catch {}
  });
}

async function ready(page, item, timed = false) {
  return page.waitForFunction(({ href, label, timed }) => {
    if (location.pathname + location.search !== href) return false;
    const main = document.querySelector('main[data-slot="sidebar-inset"]');
    if (!main) return false;
    const visible = n => n.getBoundingClientRect().height > 0 && getComputedStyle(n).visibility !== "hidden";
    const heading = [...main.querySelectorAll("h1,h2")].find(n => visible(n) && n.textContent.trim() === label);
    if (!heading) return false;
    const loading = [...main.querySelectorAll('[data-slot="skeleton"], [aria-busy="true"], .lucide-loader-circle, .lucide-loader-2, [role="status"]')]
      .some(n => visible(n) && (n.getAttribute("role") !== "status" || /加载|Loading|正在整理|正在读取/.test(n.textContent)));
    if (loading || main.innerText.trim().length < label.length + 5) return false;
    if (timed) {
      const click = window.__menuAudit.click;
      if (!click || click.href !== href) return false;
      window.__menuAudit.ready = performance.now();
    }
    return true;
  }, { ...item, timed }, { timeout: 20000, polling: "raf" });
}

async function newPage() {
  const context = await browser.newContext({ viewport: result.viewport, ignoreHTTPSErrors: true });
  await context.addCookies(cookies);
  await instrument(context);
  const page = await context.newPage();
  page.on("request", r => {
    const version = r.headers()["x-client-version"];
    if (version && !result.verified_frontend_versions.includes(version)) result.verified_frontend_versions.push(version);
  });
  if (latencyMs) {
    const cdp = await context.newCDPSession(page);
    await cdp.send("Network.enable");
    await cdp.send("Network.emulateNetworkConditions", { offline: false, latency: latencyMs, downloadThroughput: -1, uploadThroughput: -1 });
  }
  page.on("pageerror", e => result.errors.push({ type: "pageerror", message: e.message.slice(0, 300) }));
  page.on("console", e => { if (e.type() === "error") result.errors.push({ type: "console", message: e.text().slice(0, 300) }); });
  return { page, context };
}

async function click(page, item, phase, run) {
  const requests = [];
  const starts = new Map();
  const onRequest = r => {
    const u = new URL(r.url());
    if (u.origin !== new URL(cfg.browserURL).origin) return;
    const item = { path: u.pathname, type: r.resourceType(), prefetch: r.headers()["next-router-prefetch"] === "1" };
    starts.set(r, { at: Date.now(), item });
    requests.push(item);
  };
  const onResponse = res => {
    const record = starts.get(res.request());
    if (record) Object.assign(record.item, { status: res.status(), response_ms: Date.now() - record.at });
  };
  const onFinished = r => { const record = starts.get(r); if (record) record.item.ms = Date.now() - record.at; };
  const onFailed = r => { const record = starts.get(r); if (record) record.item.failed = r.failure()?.errorText; };
  page.on("request", onRequest); page.on("response", onResponse); page.on("requestfinished", onFinished); page.on("requestfailed", onFailed);
  await page.evaluate(() => { window.__menuAudit.click = null; window.__menuAudit.ready = null; });
  let sample;
  try {
    const target = page.locator(`[data-sidebar="sidebar"] a[href=${JSON.stringify(item.href)}]`).first();
    await target.scrollIntoViewIfNeeded();
    await target.click({ timeout: 10000 });
    await ready(page, item, true);
    // 给 ready 对应的提交一次绘制机会，时间指标本身不包含固定等待。
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    sample = await page.evaluate(() => {
      const a = window.__menuAudit;
      const tasks = a.longTasks.filter(t => t.start + t.ms >= a.click.at && t.start <= a.ready);
      return { ready_ms: Math.round(a.ready - a.click.at), long_task_ms: Math.round(tasks.reduce((n,t) => n+t.ms,0)),
        max_long_task_ms: Math.round(Math.max(0,...tasks.map(t=>t.ms))),
        event_ms: Math.max(0,...a.events.filter(t=>t.start >= a.click.at - 10 && t.start <= a.ready).map(t=>t.ms)) };
    });
  } catch (e) {
    sample = { error: e.message.split("\n")[0] };
    sample.diagnostic = await page.evaluate(() => ({ url: location.pathname + location.search,
      headings: [...document.querySelectorAll('main h1,main h2')].map(n=>n.textContent.trim()),
      loading: [...document.querySelectorAll('main [data-slot="skeleton"],main .lucide-loader-circle,main [role="status"]')].filter(n=>n.getBoundingClientRect().height>0).map(n=>({tag:n.tagName,slot:n.getAttribute("data-slot"),role:n.getAttribute("role")})) }));
  } finally {
    page.off("request", onRequest); page.off("response", onResponse); page.off("requestfinished", onFinished); page.off("requestfailed", onFailed);
  }
  sample = { label: item.label, href: item.href, phase, run, ...sample, requests };
  if (phase) result.samples.push(sample);
  return sample;
}

function save() {
  fs.writeFileSync(path.join(directory, `${phase}.json`), JSON.stringify(result, null, 2));
}

try {
  const response = await discovery.request.get(cfg.browserURL + "/api/workspaces");
  if (!response.ok()) throw new Error(`Workspace discovery failed: ${response.status()}`);
  const data = await response.json();
  const workspaces = Array.isArray(data) ? data : data.items;
  slug = workspaces.find(w => w.slug === cfg.workspaceSlug)?.slug || workspaces[0]?.slug;
  if (!slug) throw new Error("No accessible workspace");
  result.workspace_slug = slug;
  const page = await discovery.newPage();
  await instrument(discovery);
  await page.goto(`${cfg.browserURL}/${slug}/issues`, { waitUntil: "domcontentloaded" });
  await page.locator('[data-sidebar="sidebar"]').first().waitFor({ state: "visible" });
  const allowed = new Set(["inbox", "my-issues", "issues", "projects", "autopilots", "agents", "squads", "usage", "run-reviews", "runtimes", "skills", "settings", "health", "meals", "goals", "life?tab=memory", "life?tab=experiment", "life?tab=observers", "life?tab=chronicle"]);
  const links = await page.locator('[data-sidebar="sidebar"] a[href]').evaluateAll(nodes => nodes.filter(n=>n.getBoundingClientRect().height>0).map(n=>({ label:n.textContent.trim().replace(/\d+$/, "").trim(), href:n.getAttribute("href") })));
  result.menus = [...new Map(links.filter(x=>allowed.has(x.href.slice(slug.length+2))).map(x=>[x.href,x])).values()];
  if (result.menus.length !== allowed.size) throw new Error(`Expected ${allowed.size} menus, found ${result.menus.length}`);
  await discovery.close();
  for (const item of result.menus.filter(item => !process.env.MENU_ONLY || process.env.MENU_ONLY.split(",").some(suffix => item.href.endsWith(suffix)))) {
    checkVersion();
    const baseline = result.menus.find(x=>x.href===`/${slug}/${item.href===`/${slug}/issues` ? "inbox" : "issues"}`);
    for (let run=0; run<coldRuns; run++) {
      const { page, context } = await newPage();
      try {
        await page.goto(cfg.browserURL+baseline.href,{waitUntil:"domcontentloaded"});
        await ready(page,baseline);
        await click(page,item,"cold",run);
      } finally { await context.close(); }
    }
    const { page: warmPage, context: warmContext } = await newPage();
    try {
      await warmPage.goto(cfg.browserURL+baseline.href,{waitUntil:"domcontentloaded"});
      await ready(warmPage,baseline);
      await click(warmPage,item,null,0);
      for(let run=0;run<warmRuns;run++) {
        const reset=await click(warmPage,baseline,null,run);
        if(reset.error)throw new Error(`Baseline unavailable: ${reset.error}`);
        await click(warmPage,item,"warm",run);
      }
    } finally { await warmContext.close(); }
    checkVersion(); save();
    const samples=result.samples.filter(x=>x.href===item.href);
    console.log(JSON.stringify({ label:item.label, cold:samples.filter(x=>x.phase==="cold").map(x=>x.ready_ms??x.diagnostic), warm:samples.filter(x=>x.phase==="warm").map(x=>x.ready_ms??x.diagnostic) }));
  }
  checkVersion();
  if (result.verified_frontend_versions.some(version => version !== deployment.release_id)) throw new Error("Browser loaded an unexpected frontend build");
  if (result.errors.length || result.samples.some(sample => sample.error)) throw new Error("Browser measurement contains errors");
  result.complete=true;
} finally { save(); await browser.close(); }
console.log(JSON.stringify({ path:path.join(directory,`${phase}.json`), complete:result.complete, errors:result.errors.length }));
