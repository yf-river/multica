import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import {
  attachBrowserAuditEvents,
  browserRequestPath as requestPath,
} from "./lib/browser-audit-events.mjs";
import {
  createBrowserRequestTools,
  launchGoalTestBrowser,
  loadGoalTestBrowserAudit,
  loginGoalTest,
  verifyGoalTestDeploymentLogs,
} from "./lib/goal-test-browser-audit.mjs";

const {
  env, frontendURL, browserURL, backendURL, workspaceSlug, account, password,
  artifactRoot, generatedAt, stamp,
} = loadGoalTestBrowserAudit();
const { isAuditedRequest, countByPath } = createBrowserRequestTools(
  [frontendURL, browserURL, backendURL],
  requestPath,
);
const maxClickMs = 3500;
const maxTotalMs = 6000;
const maxApiMs = 1200;
const maxApiRequests = 20;
mkdirSync(artifactRoot, { recursive: true });

const dashboardClicks = [
  { id: "life-memory", label: "记忆", link: "记忆", path: `/${workspaceSlug}/life?tab=memory`, ready: { heading: "记忆" } },
  { id: "life-experiment", label: "实验", link: "实验", path: `/${workspaceSlug}/life?tab=experiment`, ready: { heading: "实验" } },
  { id: "life-observers", label: "观察席", link: "观察席", path: `/${workspaceSlug}/life?tab=observers`, ready: { heading: "观察席" } },
  { id: "life-chronicle", label: "编年史", link: "编年史", path: `/${workspaceSlug}/life?tab=chronicle`, ready: { heading: "编年史" } },
  { id: "inbox", label: "收件箱", link: "收件箱", path: `/${workspaceSlug}/inbox`, ready: { heading: "收件箱" } },
  { id: "my-issues", label: "我的任务", link: "我的任务", path: `/${workspaceSlug}/my-issues`, ready: { heading: "我的任务" } },
  { id: "issues", label: "任务", link: "任务", path: `/${workspaceSlug}/issues`, ready: { heading: "任务" } },
  { id: "projects", label: "项目", link: "项目", path: `/${workspaceSlug}/projects`, ready: { heading: "项目" } },
  { id: "autopilots", label: "自动化", link: "自动化", path: `/${workspaceSlug}/autopilots`, ready: { heading: "自动化" } },
  { id: "agents", label: "智能体", link: "智能体", path: `/${workspaceSlug}/agents`, ready: { heading: "智能体" } },
  { id: "squads", label: "小队", link: "小队", path: `/${workspaceSlug}/squads`, ready: { heading: "小队" } },
  { id: "run-reviews", label: "运行复盘", link: "运行复盘", path: `/${workspaceSlug}/run-reviews`, ready: { heading: "运行复盘" } },
  { id: "runtimes", label: "运行时", link: "运行时", path: `/${workspaceSlug}/runtimes`, ready: { heading: "运行时" } },
  { id: "skills", label: "技能", link: "技能", path: `/${workspaceSlug}/skills`, ready: { heading: "技能" } },
  { id: "settings", label: "设置", link: "设置", path: `/${workspaceSlug}/settings`, ready: { heading: "设置" } },
];

const token = await loginGoalTest({ backendURL, account, password });
const { browser, context } = await launchGoalTestBrowser(browserURL, token);

const page = await context.newPage();
const setup = await openStartPage(page);
const clicks = setup.ok ? await auditClicks(page) : [];
await browser.close();

const deploymentLogs = verifyGoalTestDeploymentLogs(env);
const summary = summarize(setup, clicks, deploymentLogs);
const payload = {
  schema: "multica.goal_test.dashboard_click_audit.v1",
  generated_at: generatedAt,
  frontend_url: frontendURL,
  browser_url: browserURL,
  backend_url: backendURL,
  workspace_slug: workspaceSlug,
  account,
  thresholds: {
    max_click_ms: maxClickMs,
    max_total_ms: maxTotalMs,
    max_api_ms: maxApiMs,
    max_api_requests: maxApiRequests,
  },
  deployment_logs: deploymentLogs,
  setup,
  summary,
  clicks,
};

const jsonPath = path.join(artifactRoot, `dashboard-click-audit-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `dashboard-click-audit-${stamp}.md`);
writeFileSync(jsonPath, `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(markdownPath, renderMarkdown(payload));
writeFileSync(path.join(artifactRoot, "dashboard-click-audit-latest.json"), `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(path.join(artifactRoot, "dashboard-click-audit-summary.md"), renderMarkdown(payload));

console.log(JSON.stringify({ ok: summary.ok, json: jsonPath, markdown: markdownPath, failures: summary.failures }, null, 2));
if (!summary.ok) process.exitCode = 1;

async function openStartPage(page) {
  const startedAt = Date.now();
  try {
    await page.goto(`${browserURL}/${workspaceSlug}/issues`, { waitUntil: "domcontentloaded", timeout: 15_000 });
    await waitForReadySignal(page, { heading: "任务" }, 10_000);
    await page.waitForLoadState("networkidle", { timeout: 8_000 }).catch(() => {});
    return { ok: true, elapsed_ms: Date.now() - startedAt, path: page.url() };
  } catch (error) {
    return {
      ok: false,
      elapsed_ms: Date.now() - startedAt,
      path: page.url(),
      error: error instanceof Error ? error.message.split("\n")[0] : String(error),
    };
  }
}

async function auditClicks(page) {
  const results = [];
  for (const item of dashboardClicks) {
    await resetNavigationBaseline(page, item);
    results.push(await measureClick(page, item));
  }
  return results;
}

async function resetNavigationBaseline(page, item) {
  const baselinePath = `/${workspaceSlug}/issues`;
  const baselineReady = { heading: "任务" };
  if (new URL(page.url(), browserURL).pathname !== baselinePath) {
    await page.goto(`${browserURL}${baselinePath}`, { waitUntil: "domcontentloaded", timeout: 15_000 });
  }
  await waitForReadySignal(page, baselineReady, 10_000);
  await page.waitForLoadState("networkidle", { timeout: 3_000 }).catch(() => {});
}

async function measureClick(page, item) {
  const auditEvents = attachBrowserAuditEvents(page, { isAuditedRequest, requestPath });

  const startedAt = Date.now();
  let readyMs = 0;
  let totalMs = 0;
  let errorText = "";
  let bodyText = "";
  let navigationClick = null;
  try {
    navigationClick = await clickNavigationLink(page, item);
    await waitForReadySignal(page, item.ready, 10_000);
    readyMs = Date.now() - startedAt;
    await page.waitForLoadState("networkidle", { timeout: 5_000 }).catch(() => {});
    totalMs = Date.now() - startedAt;
    bodyText = await page.locator("body").innerText({ timeout: 5_000 }).catch(() => "");
  } catch (error) {
    errorText = error instanceof Error ? error.message.split("\n")[0] : String(error);
    totalMs = Date.now() - startedAt;
    if (!readyMs) readyMs = totalMs;
    bodyText = await page.locator("body").innerText({ timeout: 1_000 }).catch(() => "");
  } finally {
    auditEvents.detach();
  }

  if (!totalMs) totalMs = Date.now() - startedAt;
  if (!readyMs) readyMs = totalMs;
  const { requests, failedRequests, consoleErrors, pageErrors } = auditEvents;
  const apiRequests = requests.filter((request) => requestPath(request.url).startsWith("/api/"));
  const badStatuses = requests
    .filter((request) => request.status && request.status >= 400)
    .map((request) => ({ status: request.status, ms: request.ms ?? null, path: requestPath(request.url) }));
  const slowRequests = requests
    .filter((request) => (request.ms ?? 0) > maxApiMs)
    .map((request) => ({ status: request.status ?? null, ms: request.ms ?? null, path: requestPath(request.url) }))
    .slice(0, 20);
  const loadingResidue = ["Rendering", "Compiling", "Loading", "加载中", "渲染中"].filter((text) => bodyText.includes(text));
  const failures = [
    ...(errorText ? [`点击失败：${errorText}`] : []),
    ...(readyMs > maxClickMs ? [`点击可用耗时 ${readyMs}ms 超过 ${maxClickMs}ms`] : []),
    ...(totalMs > maxTotalMs ? [`点击总等待 ${totalMs}ms 超过 ${maxTotalMs}ms`] : []),
    ...(apiRequests.length > maxApiRequests ? [`API 请求数 ${apiRequests.length} 超过 ${maxApiRequests}`] : []),
    ...badStatuses.map((request) => `请求状态异常：${request.status} ${request.path}`),
    ...failedRequests.map((request) => `请求失败：${request.path} ${request.failure}`),
    ...slowRequests.map((request) => `慢请求：${request.ms}ms ${request.path}`),
    ...consoleErrors.map((error) => `console error：${error}`),
    ...pageErrors.map((error) => `pageerror：${error}`),
    ...loadingResidue.map((text) => `加载残留：${text}`),
  ];

  return {
    id: item.id,
    label: item.label,
    target_path: item.path,
    final_url: page.url(),
    ready_ms: readyMs,
    total_ms: totalMs,
    ok: failures.length === 0,
    failures,
    api_request_count: apiRequests.length,
    api_path_counts: countByPath(apiRequests),
    slow_requests: slowRequests,
    bad_statuses: badStatuses,
    failed_requests: failedRequests,
    console_errors: consoleErrors,
    page_errors: pageErrors,
    navigation_click: navigationClick,
    loading_residue: loadingResidue,
    body_excerpt: bodyText.split("\n").filter(Boolean).slice(0, 24),
  };
}

async function clickNavigationLink(page, item) {
  const attempts = [];
  const target = targetURL(item);
  const targetPattern = (url) => url.pathname === target.pathname && url.search === target.search;
  const nativeTimeoutMs = 1_200;
  const retryTimeoutMs = 2_500;
  try {
    const link = await locateNavigationLink(page, item);
    const descriptor = await describeNavigationLink(link);
    await link.scrollIntoViewIfNeeded({ timeout: 2_000 }).catch(() => {});
    await Promise.all([
      page.waitForURL(targetPattern, { timeout: nativeTimeoutMs }),
      link.click({ timeout: 10_000 }),
    ]);
    attempts.push({ method: "native", ok: true, ...descriptor });
    return { ok: true, attempts };
  } catch (error) {
    attempts.push({
      method: "native",
      ok: false,
      error: error instanceof Error ? error.message.split("\n")[0] : String(error),
      final_url: page.url(),
    });
  }

  try {
    const link = await locateNavigationLink(page, item);
    const descriptor = await describeNavigationLink(link);
    await link.scrollIntoViewIfNeeded({ timeout: 2_000 }).catch(() => {});
    await link.evaluate((node) => node.click());
    await page.waitForURL(targetPattern, { timeout: retryTimeoutMs });
    attempts.push({ method: "dom-click", ok: true, ...descriptor });
    return { ok: true, attempts };
  } catch (error) {
    attempts.push({
      method: "dom-click",
      ok: false,
      error: error instanceof Error ? error.message.split("\n")[0] : String(error),
      final_url: page.url(),
    });
  }

  throw new Error(`navigation click did not reach ${item.path}: ${attempts.map((attempt) => `${attempt.method}=${attempt.ok ? "ok" : attempt.error}`).join("; ")}`);
}

async function locateNavigationLink(page, item) {
  const links = page.locator("a");
  const target = targetURL(item);
  const count = await links.count();
  for (let i = 0; i < count; i += 1) {
    const link = links.nth(i);
    const href = await link.getAttribute("href").catch(() => null);
    if (!href) continue;
    let pathname = "";
    try {
      pathname = new URL(href, browserURL).pathname;
    } catch {
      continue;
    }
    if (pathname !== target.pathname || new URL(href, browserURL).search !== target.search) continue;
    const box = await link.boundingBox().catch(() => null);
    if (box && box.width > 0 && box.height > 0) return link;
  }
  throw new Error(`visible navigation link not found for ${item.path}`);
}

function targetURL(item) {
  return new URL(item.path, browserURL);
}

async function describeNavigationLink(link) {
  const [href, text, box] = await Promise.all([
    link.getAttribute("href").catch(() => null),
    link.innerText({ timeout: 1_000 }).catch(() => ""),
    link.boundingBox().catch(() => null),
  ]);
  return {
    href,
    text: text.replace(/\s+/g, " ").trim().slice(0, 120),
    box: box ? { x: Math.round(box.x), y: Math.round(box.y), width: Math.round(box.width), height: Math.round(box.height) } : null,
  };
}

async function waitForReadySignal(page, ready, timeout) {
  if (ready.testId) {
    await page.locator(`[data-testid="${ready.testId}"]`).waitFor({ state: "attached", timeout });
    return;
  }
  if (ready.heading) {
    await page.getByRole("heading", { name: ready.heading, exact: true }).first().waitFor({ state: "visible", timeout });
    return;
  }
  throw new Error("ready signal is missing");
}

function summarize(setup, clicks, logEvidence) {
  const clickFailures = clicks.flatMap((click) => click.failures.map((failure) => `${click.label}: ${failure}`));
  const logFailures = logEvidence.ok ? [] : [`当前部署日志窗口未通过：${logEvidence.error || "verify-logs failed"}`];
  const failures = [
    ...(setup.ok ? [] : [`初始化失败：${setup.error || "unknown"}`]),
    ...clickFailures,
    ...logFailures,
  ];
  const slowestClicks = [...clicks]
    .sort((a, b) => b.ready_ms - a.ready_ms)
    .slice(0, 8)
    .map((click) => ({
      id: click.id,
      label: click.label,
      ready_ms: click.ready_ms,
      total_ms: click.total_ms,
      api_request_count: click.api_request_count,
    }));
  return {
    ok: failures.length === 0,
    setup_ok: setup.ok,
    click_count: clicks.length,
    passed_clicks: clicks.filter((click) => click.ok).length,
    failed_clicks: clicks.filter((click) => !click.ok).length,
    total_api_requests: clicks.reduce((sum, click) => sum + click.api_request_count, 0),
    deployment_logs_ok: logEvidence.ok,
    slowest_clicks: slowestClicks,
    failures,
  };
}

function renderMarkdown(payload) {
  const lines = [
    "# goal-test Dashboard 点击审计",
    "",
    `生成时间：${payload.generated_at}`,
    `浏览器入口：${payload.browser_url}`,
    `后端：${payload.backend_url}`,
    `工作区：${payload.workspace_slug}`,
    `结论：${payload.summary.ok ? "通过" : "未通过"}`,
    "",
    "## 汇总",
    "",
    `- 初始化：${payload.setup.ok ? "通过" : "失败"}`,
    `- 点击路径：${payload.summary.passed_clicks}/${payload.summary.click_count} 通过`,
    `- API 请求总数：${payload.summary.total_api_requests}`,
    `- 当前部署日志窗口：${payload.summary.deployment_logs_ok ? "通过" : "未通过"}`,
    "",
    "## 最慢点击",
    "",
    ...payload.summary.slowest_clicks.map((click) => `- ${click.label}：可用 ${click.ready_ms}ms，总等待 ${click.total_ms}ms，API ${click.api_request_count}`),
    "",
  ];
  if (payload.summary.failures.length > 0) {
    lines.push("## 阻断项", "");
    for (const failure of payload.summary.failures.slice(0, 100)) lines.push(`- ${failure}`);
    lines.push("");
  }
  lines.push("## 点击明细", "");
  for (const click of payload.clicks) {
    lines.push(`### ${click.ok ? "通过" : "失败"}：${click.label}`);
    lines.push(`- 目标：${click.target_path}`);
    lines.push(`- 可用耗时：${click.ready_ms}ms`);
    lines.push(`- 总等待：${click.total_ms}ms`);
    lines.push(`- API 请求：${click.api_request_count}`);
    if (click.api_path_counts.length > 0) {
      lines.push(`- API 路径：${click.api_path_counts.map((item) => `${item.path}=${item.count}`).join("，")}`);
    }
    for (const failure of click.failures) lines.push(`- 问题：${failure}`);
    lines.push("");
  }
  return `${lines.join("\n")}\n`;
}
