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

const browserURL = trimSlash(process.env.GOAL_TEST_BROWSER_URL || `http://127.0.0.1:${env.FRONTEND_PORT || "13682"}`);
const frontendURL = trimSlash(process.env.GOAL_TEST_FRONTEND_URL || env.FRONTEND_ORIGIN || "http://9.134.129.162:13682");
const backendURL = trimSlash(process.env.GOAL_TEST_BACKEND_URL || env.REMOTE_API_URL || "http://127.0.0.1:18762");
const noProxy = mergeNoProxy(process.env.NO_PROXY || process.env.no_proxy || "", [browserURL, frontendURL, backendURL]);
process.env.NO_PROXY = noProxy;
process.env.no_proxy = noProxy;
const workspaceSlug = process.env.GOAL_TEST_WORKSPACE_SLUG || "goal-test-daemon";
const account = process.env.GOAL_TEST_ACCOUNT || "goal-test-daemon";
const password = process.env.GOAL_TEST_PASSWORD || "e2e-password";
const maxRouteMs = Number(process.env.GOAL_TEST_TRAINING_AUDIT_MAX_ROUTE_MS || "3500");
const maxClickMs = Number(process.env.GOAL_TEST_TRAINING_AUDIT_MAX_CLICK_MS || "3500");
const maxApiMs = Number(process.env.GOAL_TEST_TRAINING_AUDIT_MAX_API_MS || "1200");
const maxApiRequests = Number(process.env.GOAL_TEST_TRAINING_AUDIT_MAX_API_REQUESTS || "18");
const artifactRoot = path.resolve(process.env.GOAL_TEST_TRAINING_AUDIT_DIR || path.join(repoRoot, "artifacts/acceptance"));
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");

mkdirSync(artifactRoot, { recursive: true });

const routes = [
  { id: "runs", label: "运行看板", path: `/${workspaceSlug}/training/runs`, expect: "团队运行看板" },
  { id: "prompts", label: "提示词库", path: `/${workspaceSlug}/training/prompts`, expect: "提示词库" },
  { id: "prompt-playground", label: "提示词调试场", path: `/${workspaceSlug}/training/prompt-playground`, expect: "提示词调试场" },
  { id: "agent-playground", label: "智能体调试场", path: `/${workspaceSlug}/training/agent-playground`, expect: "智能体调试场" },
  { id: "datasets", label: "数据集", path: `/${workspaceSlug}/training/datasets`, expect: "数据集" },
  { id: "test-suites", label: "测试套件", path: `/${workspaceSlug}/training/test-suites`, expect: "测试套件" },
  { id: "experiments", label: "实验", path: `/${workspaceSlug}/training/experiments`, expect: "实验" },
  { id: "optimization-runs", label: "优化运行", path: `/${workspaceSlug}/training/optimization-runs`, expect: "优化运行" },
  { id: "run-history", label: "运行历史", path: `/${workspaceSlug}/training/run-history`, expect: "运行历史" },
];

const token = await login();
const browser = await chromium.launch({ headless: true, args: ["--no-proxy-server", "--proxy-server=direct://", "--proxy-bypass-list=*"] });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true });
await context.addCookies([{ name: "multica_logged_in", value: "1", url: browserURL, sameSite: "Lax" }]);
await context.addInitScript((authToken) => {
  localStorage.setItem("multica_token", authToken);
  localStorage.setItem("multica:chat:isOpen", "false");
}, token);

const results = [];
for (const route of routes) {
  const page = await context.newPage();
  results.push(await auditTrainingRoute(page, route));
  await page.close().catch(() => {});
}
const clickPage = await context.newPage();
const clickResults = await auditTrainingRouteClicks(clickPage);
await clickPage.close().catch(() => {});
await browser.close();

const deploymentLogs = runDeploymentLogVerification();
const summary = summarize(results, clickResults, deploymentLogs);
const payload = {
  schema: "multica.goal_test.training_performance_audit.v1",
  generated_at: generatedAt,
  frontend_url: frontendURL,
  browser_url: browserURL,
  backend_url: backendURL,
  workspace_slug: workspaceSlug,
  account,
  thresholds: {
    max_route_ms: maxRouteMs,
    max_click_ms: maxClickMs,
    max_api_ms: maxApiMs,
    max_api_requests: maxApiRequests,
  },
  deployment_logs: deploymentLogs,
  summary,
  routes: results,
  click_results: clickResults,
};

const jsonPath = path.join(artifactRoot, `training-performance-audit-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `training-performance-audit-${stamp}.md`);
writeFileSync(jsonPath, `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(markdownPath, renderMarkdown(payload));
writeFileSync(path.join(artifactRoot, "training-performance-audit-latest.json"), `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(path.join(artifactRoot, "training-performance-audit-summary.md"), renderMarkdown(payload));

console.log(JSON.stringify({ ok: summary.ok, json: jsonPath, markdown: markdownPath, failures: summary.failures }, null, 2));
if (!summary.ok) process.exitCode = 1;

async function auditTrainingRoute(page, route) {
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
  let bodyText = "";
  let navigationError = "";
  try {
    await page.goto(`${browserURL}${route.path}`, { waitUntil: "domcontentloaded", timeout: 15_000 });
    await page.waitForFunction(
      (expected) => document.body?.innerText.includes(expected),
      route.expect,
      { timeout: 10_000 },
    );
    readyMs = Date.now() - startedAt;
    await page.waitForLoadState("networkidle", { timeout: 8_000 }).catch(() => {});
    bodyText = await page.locator("body").innerText({ timeout: 5_000 }).catch(() => "");
  } catch (error) {
    navigationError = error instanceof Error ? error.message : String(error);
  } finally {
    page.off("request", onRequest);
    page.off("response", onResponse);
    page.off("requestfailed", onRequestFailed);
    page.off("console", onConsole);
    page.off("pageerror", onPageError);
  }

  const elapsedMs = Date.now() - startedAt;
  if (!readyMs) readyMs = elapsedMs;
  const apiRequests = requests.filter((item) => requestPath(item.url).startsWith("/api/"));
  const trainingApiRequests = apiRequests.filter((item) => requestPath(item.url).startsWith("/api/prompt-evaluation"));
  const badStatuses = requests
    .filter((item) => item.status && item.status >= 400)
    .map((item) => ({ status: item.status, ms: item.ms ?? null, path: requestPath(item.url) }));
  const slowRequests = requests
    .filter((item) => (item.ms ?? 0) > maxApiMs)
    .map((item) => ({ status: item.status ?? null, ms: item.ms ?? null, path: requestPath(item.url) }))
    .slice(0, 20);
  const requestBoundaries = {
    prompt_evaluation_summary: hasPath(trainingApiRequests, "summary"),
    runtime_readiness: hasPath(trainingApiRequests, "runtime-readiness"),
    cases: hasPath(trainingApiRequests, "cases"),
    assets: hasPath(trainingApiRequests, "assets"),
    runs: hasPath(trainingApiRequests, "runs"),
    candidates: hasPath(trainingApiRequests, "optimization-candidates"),
  };
  const boundaryFailures = expectedBoundaryFailures(route.id, requestBoundaries);
  const failures = [
    ...(navigationError ? [`导航失败：${navigationError.split("\n")[0]}`] : []),
    ...(readyMs > maxRouteMs ? [`页面可用耗时 ${readyMs}ms 超过 ${maxRouteMs}ms`] : []),
    ...(apiRequests.length > maxApiRequests ? [`API 请求数 ${apiRequests.length} 超过 ${maxApiRequests}`] : []),
    ...badStatuses.map((item) => `请求状态异常：${item.status} ${item.path}`),
    ...failedRequests.map((item) => `请求失败：${item.path} ${item.failure}`),
    ...slowRequests.map((item) => `慢请求：${item.ms}ms ${item.path}`),
    ...consoleErrors.map((item) => `console error：${item}`),
    ...pageErrors.map((item) => `pageerror：${item}`),
    ...boundaryFailures,
  ];

  return {
    id: route.id,
    label: route.label,
    url: `${browserURL}${route.path}`,
    final_url: page.url(),
    elapsed_ms: elapsedMs,
    ready_ms: readyMs,
    ok: failures.length === 0,
    failures,
    api_request_count: apiRequests.length,
    training_api_request_count: trainingApiRequests.length,
    request_boundaries: requestBoundaries,
    api_path_counts: countByPath(apiRequests),
    slow_requests: slowRequests,
    bad_statuses: badStatuses,
    failed_requests: failedRequests,
    console_errors: consoleErrors,
    page_errors: pageErrors,
    body_excerpt: bodyText.split("\n").filter(Boolean).slice(0, 24),
  };
}

async function auditTrainingRouteClicks(page) {
  const consoleErrors = [];
  const pageErrors = [];
  const onConsole = (message) => {
    if (message.type() === "error" && !message.text().startsWith("Failed to load resource:")) {
      consoleErrors.push(message.text().slice(0, 500));
    }
  };
  const onPageError = (error) => {
    pageErrors.push(error.message.slice(0, 500));
  };
  page.on("console", onConsole);
  page.on("pageerror", onPageError);

  const clicks = [];
  let setupError = "";
  try {
    await page.goto(`${browserURL}/${workspaceSlug}/training/runs`, { waitUntil: "domcontentloaded", timeout: 15_000 });
    await page.waitForFunction(
      (expected) => document.body?.innerText.includes(expected),
      routes[0].expect,
      { timeout: 10_000 },
    );
    await page.waitForLoadState("networkidle", { timeout: 8_000 }).catch(() => {});
  } catch (error) {
    setupError = error instanceof Error ? error.message : String(error);
  }

  if (!setupError) {
    for (const route of routes) {
      const startedAt = Date.now();
      let errorText = "";
      try {
        await page.getByRole("link", { name: route.label, exact: true }).last().click({ timeout: 10_000 });
        await page.waitForURL(`**${route.path}`, { timeout: 15_000 });
        await page.waitForFunction(
          (expected) => document.body?.innerText.includes(expected),
          route.expect,
          { timeout: 10_000 },
        );
      } catch (error) {
        errorText = error instanceof Error ? error.message.split("\n")[0] : String(error);
      }
      const elapsedMs = Date.now() - startedAt;
      const failures = [
        ...(errorText ? [`点击失败：${errorText}`] : []),
        ...(elapsedMs > maxClickMs ? [`点击可用耗时 ${elapsedMs}ms 超过 ${maxClickMs}ms`] : []),
      ];
      clicks.push({
        id: route.id,
        label: route.label,
        target_path: route.path,
        final_url: page.url(),
        elapsed_ms: elapsedMs,
        ok: failures.length === 0,
        failures,
      });
    }
  }

  page.off("console", onConsole);
  page.off("pageerror", onPageError);
  const failures = [
    ...(setupError ? [`点击审计初始化失败：${setupError.split("\n")[0]}`] : []),
    ...clicks.flatMap((item) => item.failures.map((failure) => `${item.label}: ${failure}`)),
    ...consoleErrors.map((item) => `console error：${item}`),
    ...pageErrors.map((item) => `pageerror：${item}`),
  ];
  return {
    ok: failures.length === 0,
    max_click_ms: maxClickMs,
    click_count: clicks.length,
    passed_clicks: clicks.filter((item) => item.ok).length,
    failed_clicks: clicks.filter((item) => !item.ok).length,
    failures,
    clicks,
    console_errors: consoleErrors,
    page_errors: pageErrors,
  };
}

function expectedBoundaryFailures(routeId, boundaries) {
  const failures = [];
  const contracts = {
    runs: {
      required: ["prompt_evaluation_summary", "runtime_readiness", "runs", "candidates"],
      forbidden: ["cases", "assets"],
    },
    prompts: {
      required: ["prompt_evaluation_summary"],
      forbidden: ["runtime_readiness", "cases", "assets", "runs", "candidates"],
    },
    datasets: {
      required: ["prompt_evaluation_summary", "cases", "assets"],
      forbidden: ["runtime_readiness", "runs", "candidates"],
    },
    "test-suites": {
      required: ["prompt_evaluation_summary", "cases", "assets"],
      forbidden: ["runtime_readiness", "runs", "candidates"],
    },
    experiments: {
      required: ["prompt_evaluation_summary", "cases", "assets"],
      forbidden: ["runtime_readiness", "runs", "candidates"],
    },
    "optimization-runs": {
      required: ["prompt_evaluation_summary", "assets", "runs", "candidates"],
      forbidden: ["runtime_readiness", "cases"],
    },
    "run-history": {
      required: ["prompt_evaluation_summary", "cases", "runs", "candidates"],
      forbidden: ["runtime_readiness", "assets"],
    },
  };
  const contract = contracts[routeId];
  if (contract) {
    for (const key of contract.required) {
      if (!boundaries[key]) failures.push(`${routeId} 缺少 ${formatBoundaryName(key)} 请求`);
    }
    for (const key of contract.forbidden) {
      if (boundaries[key]) failures.push(`${routeId} 不应请求 ${formatBoundaryName(key)}`);
    }
  }
  if (routeId === "prompt-playground") {
    if (boundaries.prompt_evaluation_summary) failures.push("提示词调试场不应请求 summary");
    if (boundaries.runtime_readiness) failures.push("提示词调试场不应请求 runtime readiness");
    if (boundaries.cases) failures.push("提示词调试场不应请求 structured cases");
    if (!boundaries.assets) failures.push("提示词调试场缺少 assets 请求");
    if (!boundaries.runs) failures.push("提示词调试场缺少 runs 请求");
  }
  if (routeId === "agent-playground") {
    if (boundaries.prompt_evaluation_summary) failures.push("智能体调试场不应请求 summary");
    if (!boundaries.runtime_readiness) failures.push("智能体调试场缺少 runtime readiness 请求");
    if (!boundaries.cases) failures.push("智能体调试场缺少 structured cases 请求");
  }
  return failures;
}

function formatBoundaryName(key) {
  const labels = {
    prompt_evaluation_summary: "summary",
    runtime_readiness: "runtime readiness",
    cases: "structured cases",
    assets: "assets",
    runs: "runs",
    candidates: "optimization candidates",
  };
  return labels[key] || key;
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

function summarize(routeResults, clickResults, logEvidence) {
  const routeFailures = routeResults.flatMap((route) => route.failures.map((failure) => `${route.label}: ${failure}`));
  const clickFailures = clickResults.ok ? [] : clickResults.failures.map((failure) => `点击审计：${failure}`);
  const logFailures = logEvidence.ok ? [] : [`当前部署日志窗口未通过：${logEvidence.error || "verify-logs failed"}`];
  const failures = [...routeFailures, ...clickFailures, ...logFailures];
  const slowestRoutes = [...routeResults]
    .sort((a, b) => b.ready_ms - a.ready_ms)
    .slice(0, 5)
    .map((route) => ({ id: route.id, label: route.label, ready_ms: route.ready_ms, elapsed_ms: route.elapsed_ms, api_request_count: route.api_request_count }));
  return {
    ok: failures.length === 0,
    route_count: routeResults.length,
    passed_routes: routeResults.filter((route) => route.ok).length,
    failed_routes: routeResults.filter((route) => !route.ok).length,
    total_api_requests: routeResults.reduce((sum, route) => sum + route.api_request_count, 0),
    total_training_api_requests: routeResults.reduce((sum, route) => sum + route.training_api_request_count, 0),
    click_count: clickResults.click_count,
    passed_clicks: clickResults.passed_clicks,
    failed_clicks: clickResults.failed_clicks,
    deployment_logs_ok: logEvidence.ok,
    slowest_routes: slowestRoutes,
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
    "# 训练与评估性能审计",
    "",
    `生成时间：${payload.generated_at}`,
    `浏览器入口：${payload.browser_url}`,
    `后端：${payload.backend_url}`,
    `工作区：${payload.workspace_slug}`,
    `结论：${payload.summary.ok ? "通过" : "未通过"}`,
    "",
    "## 汇总",
    "",
    `- 页面数：${payload.summary.route_count}`,
    `- 通过：${payload.summary.passed_routes}`,
    `- 失败：${payload.summary.failed_routes}`,
    `- API 请求总数：${payload.summary.total_api_requests}`,
    `- 训练评估 API 请求总数：${payload.summary.total_training_api_requests}`,
    `- 点击路径：${payload.summary.passed_clicks}/${payload.summary.click_count} 通过`,
    `- 当前部署日志窗口：${payload.summary.deployment_logs_ok ? "通过" : "未通过"}`,
    "",
    "## 最慢页面",
    "",
    ...payload.summary.slowest_routes.map((route) => `- ${route.label}：可用 ${route.ready_ms}ms，总等待 ${route.elapsed_ms}ms，API ${route.api_request_count}`),
    "",
  ];
  if (payload.summary.failures.length > 0) {
    lines.push("## 阻断项", "");
    for (const failure of payload.summary.failures.slice(0, 100)) lines.push(`- ${failure}`);
    lines.push("");
  }
  lines.push("## 点击耗时", "");
  for (const click of payload.click_results.clicks) {
    lines.push(`- ${click.ok ? "通过" : "失败"}：${click.label}，点击到可用 ${click.elapsed_ms}ms，目标 ${click.target_path}`);
    for (const failure of click.failures) lines.push(`  - 问题：${failure}`);
  }
  lines.push("");
  lines.push("## 页面明细", "");
  for (const route of payload.routes) {
    lines.push(`### ${route.ok ? "通过" : "失败"}：${route.label}`);
    lines.push(`- 可用耗时：${route.ready_ms}ms`);
    lines.push(`- 总等待耗时：${route.elapsed_ms}ms`);
    lines.push(`- API 请求：${route.api_request_count}`);
    lines.push(`- 训练评估 API：${route.training_api_request_count}`);
    lines.push(`- 请求边界：summary=${route.request_boundaries.prompt_evaluation_summary} runtime=${route.request_boundaries.runtime_readiness} cases=${route.request_boundaries.cases} assets=${route.request_boundaries.assets} runs=${route.request_boundaries.runs}`);
    for (const failure of route.failures) lines.push(`- 问题：${failure}`);
    lines.push("");
  }
  return `${lines.join("\n")}\n`;
}

function hasPath(requests, needle) {
  return requests.some((item) => requestPath(item.url).includes(needle));
}

function requestPath(url) {
  try {
    const parsed = new URL(url);
    return `${parsed.pathname}${parsed.search}`;
  } catch {
    return url;
  }
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

function isAuditedRequest(url) {
  return url.startsWith(frontendURL) || url.startsWith(browserURL) || url.startsWith(backendURL);
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

function trimSlash(value) {
  return value.replace(/\/+$/, "");
}

function mergeNoProxy(current, urls) {
  const hosts = new Set(
    String(current || "")
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean),
  );
  for (const url of urls) {
    try {
      const parsed = new URL(url);
      if (parsed.hostname) hosts.add(parsed.hostname);
    } catch {
      // Ignore non-URL values.
    }
  }
  hosts.add("127.0.0.1");
  hosts.add("localhost");
  return Array.from(hosts).join(",");
}
