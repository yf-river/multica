import { chromium } from "@playwright/test";
import { spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const env = {
  ...process.env,
  ...readEnvFile(path.join(repoRoot, ".run/env/goal-test-int.env")),
};

const frontendURL = trimSlash(process.env.GOAL_TEST_FRONTEND_URL || env.FRONTEND_ORIGIN || "http://9.134.129.162:13682");
const browserURL = trimSlash(process.env.GOAL_TEST_BROWSER_URL || `http://127.0.0.1:${env.FRONTEND_PORT || "13682"}`);
const backendURL = trimSlash(process.env.GOAL_TEST_BACKEND_URL || env.REMOTE_API_URL || "http://127.0.0.1:18762");
const workspaceSlug = process.env.GOAL_TEST_WORKSPACE_SLUG || "goal-test-daemon";
const account = process.env.GOAL_TEST_ACCOUNT || "goal-test-daemon";
const password = process.env.GOAL_TEST_PASSWORD || "e2e-password";
const maxClickMs = Number(process.env.GOAL_TEST_DASHBOARD_CLICK_MAX_MS || "3500");
const maxTotalMs = Number(process.env.GOAL_TEST_DASHBOARD_CLICK_MAX_TOTAL_MS || "6000");
const maxApiMs = Number(process.env.GOAL_TEST_DASHBOARD_CLICK_MAX_API_MS || "1200");
const maxApiRequests = Number(process.env.GOAL_TEST_DASHBOARD_CLICK_MAX_API_REQUESTS || "20");
const artifactRoot = path.resolve(process.env.GOAL_TEST_DASHBOARD_CLICK_AUDIT_DIR || path.join(repoRoot, "artifacts/acceptance"));
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");

mkdirSync(artifactRoot, { recursive: true });

const dashboardClicks = [
  { id: "inbox", label: "收件箱", link: "收件箱", path: `/${workspaceSlug}/inbox`, ready: { heading: "收件箱" } },
  { id: "issues", label: "任务", link: "任务", path: `/${workspaceSlug}/issues`, ready: { heading: "任务" } },
  { id: "projects", label: "项目", link: "项目", path: `/${workspaceSlug}/projects`, ready: { heading: "项目" } },
  { id: "agents", label: "智能体", link: "智能体", path: `/${workspaceSlug}/agents`, ready: { heading: "智能体" } },
  { id: "squads", label: "小队", link: "小队", path: `/${workspaceSlug}/squads`, ready: { heading: "小队" } },
  { id: "run-reviews", label: "运行复盘", link: "运行复盘", path: `/${workspaceSlug}/run-reviews`, ready: { heading: "运行复盘" } },
  { id: "runtimes", label: "运行时", link: "运行时", path: `/${workspaceSlug}/runtimes`, ready: { heading: "运行时" } },
  { id: "settings", label: "设置", link: "设置", path: `/${workspaceSlug}/settings`, ready: { heading: "设置" } },
  { id: "training-prompts", label: "训练与评估/提示词库", link: "提示词库", path: `/${workspaceSlug}/training/prompts`, ready: { testId: "training-route-prompts" } },
  { id: "training-debug-runs", label: "训练与评估/调试运行", link: "调试运行", path: `/${workspaceSlug}/training/debug-runs`, ready: { testId: "debug-runs-page-shell" } },
  { id: "training-datasets", label: "训练与评估/数据集", link: "数据集", path: `/${workspaceSlug}/training/datasets`, ready: { testId: "training-route-datasets" } },
  { id: "training-test-suites", label: "训练与评估/测试套件", link: "测试套件", path: `/${workspaceSlug}/training/test-suites`, ready: { testId: "training-route-test-suites" } },
];

const token = await login();
const browser = await chromium.launch({ headless: true, args: ["--no-proxy-server", "--proxy-server=direct://", "--proxy-bypass-list=*"] });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true });
await context.addCookies([{ name: "multica_logged_in", value: "1", url: browserURL, sameSite: "Lax" }]);
await context.addInitScript((authToken) => {
  localStorage.setItem("multica_token", authToken);
  localStorage.setItem("multica:chat:isOpen", "false");
}, token);

const page = await context.newPage();
const setup = await openStartPage(page);
const clicks = setup.ok ? await auditClicks(page) : [];
await browser.close();

const deploymentLogs = runDeploymentLogVerification();
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
    results.push(await measureClick(page, item));
  }
  return results;
}

async function measureClick(page, item) {
  const requests = [];
  const failedRequests = [];
  const consoleErrors = [];
  const pageErrors = [];
  const onRequest = (request) => {
    if (!isAuditedRequest(request.url())) return;
    requests.push({
      url: request.url(),
      method: request.method(),
      type: request.resourceType(),
      start: Date.now(),
    });
  };
  const onResponse = (response) => {
    if (!isAuditedRequest(response.url())) return;
    const request = response.request();
    const item = [...requests].reverse().find((candidate) => candidate.url === response.url() && candidate.method === request.method() && !candidate.status);
    if (!item) return;
    item.status = response.status();
    item.ms = Date.now() - item.start;
  };
  const onRequestFailed = (request) => {
    const failure = request.failure()?.errorText || "unknown";
    if (isAuditedRequest(request.url()) && failure !== "net::ERR_ABORTED") {
      failedRequests.push({ path: requestPath(request.url()), method: request.method(), failure });
    }
  };
  const onConsole = (message) => {
    if (message.type() === "error" && !message.text().startsWith("Failed to load resource:")) {
      consoleErrors.push(message.text().slice(0, 500));
    }
  };
  const onPageError = (error) => {
    pageErrors.push(error.message.slice(0, 500));
  };

  page.on("request", onRequest);
  page.on("response", onResponse);
  page.on("requestfailed", onRequestFailed);
  page.on("console", onConsole);
  page.on("pageerror", onPageError);

  const startedAt = Date.now();
  let readyMs = 0;
  let totalMs = 0;
  let errorText = "";
  let bodyText = "";
  try {
    const navLink = await locateNavigationLink(page, item);
    await navLink.click({ timeout: 10_000 });
    await page.waitForURL(`**${item.path}`, { timeout: 15_000 });
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
    page.off("request", onRequest);
    page.off("response", onResponse);
    page.off("requestfailed", onRequestFailed);
    page.off("console", onConsole);
    page.off("pageerror", onPageError);
  }

  if (!totalMs) totalMs = Date.now() - startedAt;
  if (!readyMs) readyMs = totalMs;
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
    loading_residue: loadingResidue,
    body_excerpt: bodyText.split("\n").filter(Boolean).slice(0, 24),
  };
}

async function locateNavigationLink(page, item) {
  const links = page.locator("a");
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
    if (pathname !== item.path) continue;
    const box = await link.boundingBox().catch(() => null);
    if (box && box.width > 0 && box.height > 0) return link;
  }
  return page.getByRole("link", { name: item.link }).first();
}

async function waitForReadySignal(page, ready, timeout) {
  if (ready.testId) {
    await page.locator(`[data-testid="${ready.testId}"]`).waitFor({ state: "attached", timeout });
    return;
  }
  if (ready.heading) {
    await page.getByRole("heading", { name: ready.heading, exact: true }).waitFor({ state: "visible", timeout });
    return;
  }
  throw new Error("ready signal is missing");
}

async function login() {
  const response = await fetch(`${backendURL}/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ account, password }),
  });
  if (!response.ok) {
    throw new Error(`login failed: ${response.status} ${await response.text()}`);
  }
  const data = await response.json();
  if (!data.token) throw new Error("login response did not include token");
  return data.token;
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

function runDeploymentLogVerification() {
  const target = env.GOAL_TEST_ENV || "int";
  const result = spawnSync("node", ["scripts/goal-test-environments.mjs", "verify-logs", target], {
    cwd: repoRoot,
    encoding: "utf8",
  });
  const raw = result.stdout || result.stderr || "";
  let evidence = null;
  try {
    evidence = raw ? JSON.parse(raw) : null;
  } catch {
    evidence = null;
  }
  return {
    ok: result.status === 0 && evidence?.ok === true,
    target,
    exit_code: result.status,
    evidence,
    error: result.status === 0 ? "" : (result.stderr || result.stdout || "").slice(0, 2000),
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

function countByPath(requests) {
  const counts = new Map();
  for (const request of requests) {
    const key = requestPath(request.url);
    counts.set(key, (counts.get(key) || 0) + 1);
  }
  return Array.from(counts.entries())
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([path, count]) => ({ path, count }));
}

function requestPath(url) {
  try {
    const parsed = new URL(url);
    return `${parsed.pathname}${parsed.search}`;
  } catch {
    return url;
  }
}

function isAuditedRequest(url) {
  return url.startsWith(frontendURL) || url.startsWith(browserURL) || url.startsWith(backendURL);
}

function trimSlash(value) {
  return value.replace(/\/+$/, "");
}

function readEnvFile(file) {
  try {
    return Object.fromEntries(
      readFileSync(file, "utf8")
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter((line) => line && !line.startsWith("#") && line.includes("="))
        .map((line) => {
          const index = line.indexOf("=");
          return [line.slice(0, index), line.slice(index + 1)];
        }),
    );
  } catch {
    return {};
  }
}
