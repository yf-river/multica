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
const { isAuditedRequest, countByPath, buildApiRequestBudget } = createBrowserRequestTools(
  [frontendURL, browserURL, backendURL],
  requestPath,
);
const maxRouteMs = 3000;
const maxApiMs = 1000;
const maxApiRequests = 20;
const screenshotDir = path.join(artifactRoot, "ui-audit-screenshots");

mkdirSync(screenshotDir, { recursive: true });

const forbiddenAcceptanceTexts = [
  "GOAL_TEST_ACCEPTANCE",
];

const routes = [
  { id: "login", label: "登录页", path: "/login", auth: false, expect: ["登录 Multica"] },
  {
    id: "companion",
    label: "搭子",
    path: `/${workspaceSlug}/companion`,
    expect: ["搭子"],
    uiContract: { forbiddenText: forbiddenAcceptanceTexts },
  },
  {
    id: "life",
    label: "人生",
    path: `/${workspaceSlug}/life`,
    expect: ["人生"],
    uiContract: { forbiddenText: forbiddenAcceptanceTexts },
  },
  {
    id: "issues",
    label: "任务",
    path: `/${workspaceSlug}/issues`,
    expect: ["新建任务"],
    uiContract: { forbiddenText: forbiddenAcceptanceTexts },
  },
  {
    id: "inbox",
    label: "收件箱",
    path: `/${workspaceSlug}/inbox`,
    expect: ["收件箱"],
    uiContract: { forbiddenText: forbiddenAcceptanceTexts },
  },
  {
    id: "my-issues",
    label: "我的任务",
    path: `/${workspaceSlug}/my-issues`,
    expect: ["我的任务"],
    uiContract: { forbiddenText: forbiddenAcceptanceTexts },
  },
  {
    id: "projects",
    label: "项目",
    path: `/${workspaceSlug}/projects`,
    expect: ["项目"],
    uiContract: { forbiddenText: forbiddenAcceptanceTexts },
  },
  {
    id: "autopilots",
    label: "自动化",
    path: `/${workspaceSlug}/autopilots`,
    expect: ["自动化"],
    uiContract: { forbiddenText: forbiddenAcceptanceTexts },
  },
  {
    id: "agents",
    label: "智能体",
    path: `/${workspaceSlug}/agents`,
    expect: ["智能体"],
    uiContract: { forbiddenText: ["GOAL_TEST_ACCEPTANCE"] },
  },
  {
    id: "squads",
    label: "小队",
    path: `/${workspaceSlug}/squads`,
    expect: ["小队"],
    uiContract: { forbiddenText: ["GOAL_TEST_ACCEPTANCE"] },
  },
  {
    id: "run-reviews",
    label: "运行复盘",
    path: `/${workspaceSlug}/run-reviews`,
    expect: ["运行复盘"],
  },
  { id: "runtimes", label: "运行时", path: `/${workspaceSlug}/runtimes`, expect: ["运行时"] },
  { id: "skills", label: "技能", path: `/${workspaceSlug}/skills`, expect: ["技能"] },
  { id: "settings", label: "设置", path: `/${workspaceSlug}/settings`, expect: ["设置"] },
];

const forbiddenText = [
  "AI Studio Developer",
  "Issues",
  "Sign up",
  "Sign in",
  "Download desktop",
  "Cloud computer",
  "ACME-123",
  "HTML preview",
  "Contact sales",
  "Agent 调试场",
  "Agent运行数",
  "Agent执行",
  "Agent 最终交付",
  "创建 user-center 需求澄清提示词",
  "user-center 模板",
  "查看当前 Workspace",
  "API Token",
  "Token ·",
  "每日 Token",
  "每周 Token",
  "它们了解你的工作区",
  "任务、项目、skill",
  "waitlist",
  "newsletter",
];

const token = await loginGoalTest({ backendURL, account, password });
const warmup = await warmupRoutes(token);
const { browser, context } = await launchGoalTestBrowser(browserURL, token);

const results = [];
const events = [];

for (const route of routes) {
  const routeContext = route.auth === false
    ? await browser.newContext({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true })
    : context;
  const page = await routeContext.newPage();
  const step = await auditRoute(page, route);
  events.push(...step.events);
  await page.close().catch(() => {});
  if (routeContext !== context) await routeContext.close().catch(() => {});
  results.push(step);
}

await browser.close();

const deploymentLogs = verifyGoalTestDeploymentLogs(env);
const summary = summarize(results, events, deploymentLogs);
const payload = {
  schema: "multica.goal_test.ui_audit.v1",
  generated_at: generatedAt,
  frontend_url: frontendURL,
  browser_url: browserURL,
  backend_url: backendURL,
  workspace_slug: workspaceSlug,
  account,
  thresholds: {
    max_route_ms: maxRouteMs,
    max_api_ms: maxApiMs,
    max_api_requests: maxApiRequests,
    post_load_wait_ms: 1_500,
  },
  warmup,
  deployment_logs: deploymentLogs,
  summary,
  routes: results,
  events,
};

const jsonPath = path.join(artifactRoot, `ui-audit-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `ui-audit-summary-${stamp}.md`);
writeFileSync(jsonPath, `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(markdownPath, renderMarkdown(payload));
writeFileSync(path.join(artifactRoot, "ui-audit-latest.json"), `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(path.join(artifactRoot, "ui-audit-summary.md"), renderMarkdown(payload));

console.log(JSON.stringify({ ok: summary.ok, json: jsonPath, markdown: markdownPath, failures: summary.failures }, null, 2));
if (!summary.ok) process.exitCode = 1;

async function auditRoute(page, route) {
  const responses = [];
  const auditEvents = attachBrowserAuditEvents(page, {
    isAuditedRequest,
    requestPath,
    formatFailedRequest: (request, failure) => ({ url: request.url(), method: request.method(), failure }),
    formatConsoleError: (text) => ({ route: route.id, type: "console-error", text: text.slice(0, 500) }),
    formatPageError: (error) => ({ route: route.id, type: "pageerror", text: error.message.slice(0, 500) }),
    onAuditedResponse: (response, request) => {
      responses.push({
        url: response.url(),
        method: request.method(),
        status: response.status(),
        resource_type: request.resourceType(),
      });
    },
  });

  const startedAt = Date.now();
  let navigationError = "";
  let bodyText = "";
  let title = "";
  let screenshot = "";
  try {
    await page.goto(`${browserURL}${route.path}`, { waitUntil: "domcontentloaded", timeout: 10_000 });
    if (route.finalPathIncludes) {
      await page.waitForURL((url) => url.href.includes(route.finalPathIncludes), { timeout: 8_000 }).catch(() => {});
    }
    await waitForRouteText(page, route.expect);
    bodyText = await page.locator("body").innerText({ timeout: 5_000 }).catch(() => "");
    title = await page.title().catch(() => "");
    screenshot = path.join(screenshotDir, `${route.id}-${stamp}.png`);
    await page.screenshot({ path: screenshot, fullPage: false, timeout: 5_000 }).catch(() => {});
  } catch (error) {
    navigationError = error instanceof Error ? error.message : String(error);
  } finally {
    auditEvents.detach();
  }

  const elapsedMs = Date.now() - startedAt;
  const { requests, failedRequests } = auditEvents;
  const routeEvents = auditEvents.errors;
  const badStatuses = responses
    .filter((item) => item.status >= 400 && !isExpectedPublicAuthProbe(route, item))
    .map((item) => ({ status: item.status, path: requestPath(item.url), resource_type: item.resource_type }))
    .slice(0, 20);
  const apiRequests = requests.filter((item) => item.url.includes("/api/"));
  const apiPathCounts = countByPath(apiRequests);
  const apiBudget = buildApiRequestBudget(apiRequests);
  const slowRequests = requests
    .filter((item) => (item.ms ?? 0) > maxApiMs)
    .map((item) => ({ status: item.status, ms: item.ms, path: requestPath(item.url) }))
    .slice(0, 20);
  const missingExpectedText = route.expect.filter((text) => !bodyText.includes(text));
  const finalURL = page.url();
  const finalPathFailures = route.finalPathIncludes && !finalURL.includes(route.finalPathIncludes)
    ? [`最终路径不符合预期：期望包含 ${route.finalPathIncludes}，实际 ${finalURL}`]
    : [];
  const forbiddenMatches = forbiddenText.filter((text) => bodyText.includes(text));
  const uiContract = await auditRouteUiContract(page, route, bodyText);
  const loadingResidue = ["Rendering", "Compiling", "Loading", "加载中", "渲染中"].filter((text) => bodyText.includes(text));
  const failures = [
    ...(navigationError ? [`导航失败：${navigationError.split("\n")[0]}`] : []),
    ...(elapsedMs > maxRouteMs ? [`页面耗时 ${elapsedMs}ms 超过 ${maxRouteMs}ms`] : []),
    ...(bodyText.trim().length === 0 ? ["页面 body 为空"] : []),
    ...missingExpectedText.map((text) => `缺少期望文本：${text}`),
    ...finalPathFailures,
    ...badStatuses.map((item) => `请求状态异常：${item.status} ${item.path}`),
    ...failedRequests.map((item) => `请求失败：${requestPath(item.url)} ${item.failure}`),
    ...(apiBudget.count > maxApiRequests ? [`API 预算请求数 ${apiBudget.count} 超过 ${maxApiRequests}（实际 ${apiRequests.length}）`] : []),
    ...slowRequests.map((item) => `慢请求：${item.ms}ms ${item.path}`),
    ...uiContract.failures,
    ...loadingResidue.map((text) => `加载残留：${text}`),
    ...forbiddenMatches.map((text) => `中文语义/营销残留：${text}`),
  ];

  return {
    id: route.id,
    label: route.label,
    url: `${browserURL}${route.path}`,
    final_url: finalURL,
    title,
    elapsed_ms: elapsedMs,
    ok: failures.length === 0,
    failures,
    api_request_count: apiRequests.length,
    api_request_budget_count: apiBudget.count,
    api_request_budget: apiBudget,
    api_path_counts: apiPathCounts,
    slow_requests: slowRequests,
    bad_statuses: badStatuses,
    failed_requests: failedRequests.map((item) => ({ path: requestPath(item.url), failure: item.failure })),
    events: routeEvents,
    missing_expected_text: missingExpectedText,
    ui_contract: uiContract,
    forbidden_text: forbiddenMatches,
    loading_residue: loadingResidue,
    screenshot,
    body_excerpt: bodyText.split("\n").filter(Boolean).slice(0, 40),
  };
}

function isExpectedPublicAuthProbe(route, response) {
  if (route.auth !== false || response.status !== 401) return false;
  const path = requestPath(response.url);
  return path === "/api/me" || path === "/api/workspaces";
}

async function auditRouteUiContract(page, route, bodyText) {
  const contract = route.uiContract;
  if (!contract) return { checked: false, failures: [] };

  const missingRequiredText = (contract.requiredText ?? []).filter((text) => !bodyText.includes(text));
  const forbiddenText = (contract.forbiddenText ?? []).filter((text) => bodyText.includes(text));
  const requiredTestIds = [];
  for (const testId of contract.requiredTestIds ?? []) {
    const count = await testIdCount(page, testId);
    requiredTestIds.push({ testid: testId, count });
  }
  const forbiddenTestIds = [];
  for (const testId of contract.forbiddenTestIds ?? []) {
    const count = await testIdCount(page, testId);
    forbiddenTestIds.push({ testid: testId, count });
  }

  const failures = [
    ...missingRequiredText.map((text) => `页面契约缺少文本：${text}`),
    ...forbiddenText.map((text) => `页面契约出现互斥文本：${text}`),
    ...requiredTestIds.filter((item) => item.count === 0).map((item) => `页面契约缺少 testid：${item.testid}`),
    ...forbiddenTestIds.filter((item) => item.count > 0).map((item) => `页面契约出现互斥 testid：${item.testid}`),
  ];

  return {
    checked: true,
    required_text: contract.requiredText ?? [],
    forbidden_text: contract.forbiddenText ?? [],
    missing_required_text: missingRequiredText,
    matched_forbidden_text: forbiddenText,
    required_testids: requiredTestIds,
    forbidden_testids: forbiddenTestIds,
    failures,
  };
}

async function testIdCount(page, testId) {
  return page.locator(`[data-testid="${testId}"]`).count().catch(() => 0);
}

async function warmupRoutes(authToken) {
  const results = [];
  for (const route of routes) {
    const startedAt = Date.now();
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 15_000);
    try {
      const response = await fetch(`${browserURL}${route.path}`, {
        redirect: "manual",
        signal: controller.signal,
        headers: {
          cookie: "multica_logged_in=1",
          authorization: `Bearer ${authToken}`,
        },
      });
      results.push({ route: route.id, status: response.status, ms: Date.now() - startedAt });
    } catch (error) {
      results.push({
        route: route.id,
        status: "failed",
        ms: Date.now() - startedAt,
        error: error instanceof Error ? error.message : String(error),
      });
    } finally {
      clearTimeout(timeout);
    }
  }
  return { enabled: true, results };
}

function summarize(routeResults, browserEvents, logEvidence) {
  const routeFailures = routeResults.flatMap((route) => route.failures.map((failure) => `${route.label}: ${failure}`));
  const eventFailures = browserEvents.map((event) => `${event.route}: ${event.type} ${event.text}`);
  const logFailures = logEvidence.ok ? [] : [`当前部署日志窗口未通过：${logEvidence.error || "verify-logs failed"}`];
  const failures = [...routeFailures, ...eventFailures, ...logFailures];
  return {
    ok: failures.length === 0,
    route_count: routeResults.length,
    passed_routes: routeResults.filter((route) => route.ok).length,
    failed_routes: routeResults.filter((route) => !route.ok).length,
    browser_event_count: browserEvents.length,
    deployment_logs_ok: logEvidence.ok,
    failures,
  };
}

function renderMarkdown(payload) {
  const lines = [
    "# goal-test UI 巡检报告",
    "",
    `生成时间：${payload.generated_at}`,
    `公开前端：${payload.frontend_url}`,
    `浏览器巡检入口：${payload.browser_url}`,
    `后端：${payload.backend_url}`,
    `结论：${payload.summary.ok ? "通过" : "未通过"}`,
    "",
    "## 汇总",
    "",
    `- 页面数：${payload.summary.route_count}`,
    `- 通过：${payload.summary.passed_routes}`,
    `- 失败：${payload.summary.failed_routes}`,
    `- 浏览器错误事件：${payload.summary.browser_event_count}`,
    `- 当前部署日志窗口：${payload.summary.deployment_logs_ok ? "通过" : "未通过"}`,
    `- 预热：${payload.warmup.enabled ? "已执行" : "未执行"}`,
    "",
  ];
  if (payload.summary.failures.length > 0) {
    lines.push("## 阻断项", "");
    for (const failure of payload.summary.failures.slice(0, 80)) lines.push(`- ${failure}`);
    lines.push("");
  }
  lines.push("## 页面明细", "");
  for (const route of payload.routes) {
    lines.push(`### ${route.ok ? "通过" : "失败"}：${route.label}`);
    lines.push(`- URL：${route.final_url || route.url}`);
    lines.push(`- 耗时：${route.elapsed_ms}ms`);
    lines.push(`- API 请求：${route.api_request_count}`);
    lines.push(`- 截图：${route.screenshot}`);
    if (route.failures.length > 0) {
      for (const failure of route.failures) lines.push(`- 问题：${failure}`);
    }
    lines.push("");
  }
  return `${lines.join("\n")}\n`;
}

async function waitForRouteText(page, expectedTexts) {
  if (!expectedTexts || expectedTexts.length === 0) return;
  await page
    .waitForFunction(
      (expected) => {
        const text = document.body?.innerText || "";
        return expected.every((item) => text.includes(item));
      },
      expectedTexts,
      { timeout: 8_000 },
    )
    .catch(() => {});
}
