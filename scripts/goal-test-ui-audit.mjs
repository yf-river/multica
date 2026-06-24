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
const warmupEnabled = process.env.GOAL_TEST_UI_AUDIT_WARMUP !== "0";
const maxRouteMs = Number(process.env.GOAL_TEST_UI_AUDIT_MAX_ROUTE_MS || "3000");
const maxApiMs = Number(process.env.GOAL_TEST_UI_AUDIT_MAX_API_MS || "1000");
const maxApiRequests = Number(process.env.GOAL_TEST_UI_AUDIT_MAX_API_REQUESTS || "20");
const artifactRoot = path.resolve(process.env.GOAL_TEST_UI_AUDIT_DIR || path.join(repoRoot, "artifacts/acceptance"));
const screenshotDir = path.join(artifactRoot, "ui-audit-screenshots");
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");

mkdirSync(screenshotDir, { recursive: true });

const routes = [
  { id: "login", label: "登录页", path: "/login", auth: false, expect: ["登录 Multica"] },
  { id: "issues", label: "任务", path: `/${workspaceSlug}/issues`, expect: ["新建任务"] },
  { id: "inbox", label: "收件箱", path: `/${workspaceSlug}/inbox`, expect: ["收件箱"] },
  { id: "agents", label: "智能体", path: `/${workspaceSlug}/agents`, expect: ["智能体"] },
  { id: "squads", label: "小队", path: `/${workspaceSlug}/squads`, expect: ["小队"] },
  { id: "usage", label: "用量", path: `/${workspaceSlug}/usage`, expect: ["用量"] },
  { id: "runtimes", label: "运行时", path: `/${workspaceSlug}/runtimes`, expect: ["运行时"] },
  { id: "settings", label: "设置", path: `/${workspaceSlug}/settings`, expect: ["设置"] },
  {
    id: "training-runs",
    label: "训练与评估/运行看板",
    path: `/${workspaceSlug}/training/runs`,
    expect: ["运行看板"],
    uiContract: { requiredTestIds: ["training-route-runs"], forbiddenTestIds: ["training-tab-strip"] },
  },
  {
    id: "training-prompts",
    label: "训练与评估/提示词库",
    path: `/${workspaceSlug}/training/prompts`,
    expect: ["提示词库"],
    uiContract: { requiredTestIds: ["training-route-prompts"], forbiddenTestIds: ["training-tab-strip"] },
  },
  {
    id: "training-prompt-playground",
    label: "训练与评估/提示词调试场",
    path: `/${workspaceSlug}/training/prompt-playground`,
    expect: ["提示词调试场"],
    uiContract: {
      requiredText: ["本地模板实验室", "模板质检台", "不启动智能体", "不创建任务、不消耗模型", "保存质检记录", "质检结论"],
      forbiddenText: ["执行目标池", "真实任务发射台", "真实执行准备度", "观测回写契约", "写入真实任务队列"],
      requiredTestIds: [
        "prompt-playground-page-shell",
        "prompt-playground-workbench",
        "prompt-playground-purpose-map",
        "prompt-playground-template-lab",
        "prompt-playground-local-pipeline",
        "prompt-playground-quality-gate",
      ],
      forbiddenTestIds: [
        "training-tab-strip",
        "training-summary-strip",
        "agent-playground-page-shell",
        "agent-playground-workbench",
        "agent-playground-run-console",
        "agent-playground-launch-brief",
        "agent-playground-execution-topology",
        "agent-playground-execution-bus",
        "agent-playground-agent-selector",
        "agent-playground-runtime-selector",
        "agent-playground-queue-contract",
        "agent-playground-evidence-strip",
        "agent-playground-task-payload",
        "agent-playground-task-pipeline",
        "agent-playground-observability-contract",
      ],
    },
  },
  {
    id: "training-agent-playground",
    label: "训练与评估/智能体调试场",
    path: `/${workspaceSlug}/training/agent-playground`,
    expect: ["智能体调试场"],
    uiContract: {
      requiredText: ["执行目标池", "入队目标", "真实任务发射台", "执行对象", "执行智能体", "自动选择训练评估智能体", "真实运行时", "入队链路", "写入真实任务队列", "执行节点", "Trace", "用量", "真实执行准备度", "观测回写契约", "真实运行"],
      forbiddenText: ["本地模板实验室", "不启动智能体", "保存质检记录", "质检结论"],
      requiredTestIds: [
        "agent-playground-page-shell",
        "agent-playground-workbench",
        "agent-playground-target-queue",
        "agent-playground-target-queue-item",
        "agent-playground-execution-stage",
        "agent-playground-run-console",
        "agent-playground-launch-brief",
        "agent-playground-execution-topology",
        "agent-playground-execution-bus",
        "agent-playground-agent-selector",
        "agent-playground-runtime-selector",
        "agent-playground-queue-contract",
        "agent-playground-evidence-strip",
        "agent-playground-task-payload",
        "agent-playground-task-pipeline",
        "agent-playground-observability-contract",
      ],
      forbiddenTestIds: [
        "training-tab-strip",
        "training-summary-strip",
        "prompt-playground-page-shell",
        "prompt-playground-workbench",
        "prompt-playground-purpose-map",
        "prompt-playground-template-lab",
        "prompt-playground-local-pipeline",
        "prompt-playground-quality-gate",
      ],
    },
  },
  {
    id: "training-datasets",
    label: "训练与评估/数据集",
    path: `/${workspaceSlug}/training/datasets`,
    expect: ["数据集"],
    uiContract: { requiredTestIds: ["training-route-datasets"], forbiddenTestIds: ["training-tab-strip"] },
  },
  {
    id: "training-test-suites",
    label: "训练与评估/测试套件",
    path: `/${workspaceSlug}/training/test-suites`,
    expect: ["测试套件"],
    uiContract: { requiredTestIds: ["training-route-test-suites"], forbiddenTestIds: ["training-tab-strip"] },
  },
  {
    id: "training-experiments",
    label: "训练与评估/实验",
    path: `/${workspaceSlug}/training/experiments`,
    expect: ["实验"],
    uiContract: { requiredTestIds: ["training-route-experiments"], forbiddenTestIds: ["training-tab-strip"] },
  },
  {
    id: "training-optimization-runs",
    label: "训练与评估/优化运行",
    path: `/${workspaceSlug}/training/optimization-runs`,
    expect: ["优化运行"],
    uiContract: { requiredTestIds: ["training-route-optimization-runs"], forbiddenTestIds: ["training-tab-strip"] },
  },
  {
    id: "training-run-history",
    label: "训练与评估/运行历史",
    path: `/${workspaceSlug}/training/run-history`,
    expect: ["运行历史"],
    uiContract: { requiredTestIds: ["training-route-run-history"], forbiddenTestIds: ["training-tab-strip"] },
  },
];

const forbiddenText = [
  "Goal Test Daemon",
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
  "waitlist",
  "newsletter",
];

const token = await login();
const warmup = warmupEnabled ? await warmupRoutes(token) : { enabled: false, results: [] };
const browser = await chromium.launch({ headless: true, args: ["--no-proxy-server"] });
const context = await browser.newContext({ viewport: { width: 1440, height: 900 }, ignoreHTTPSErrors: true });
await context.addCookies([{ name: "multica_logged_in", value: "1", url: browserURL, sameSite: "Lax" }]);
await context.addInitScript((authToken) => {
  localStorage.setItem("multica_token", authToken);
  localStorage.setItem("multica:chat:isOpen", "false");
}, token);

const results = [];
const events = [];

for (const route of routes) {
  const page = await context.newPage();
  const step = await auditRoute(page, route);
  events.push(...step.events);
  await page.close().catch(() => {});
  results.push(step);
}

await browser.close();

const deploymentLogs = runDeploymentLogVerification();
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
  const requests = [];
  const failedRequests = [];
  const responses = [];
  const routeEvents = [];
  const onConsole = (message) => {
    if (message.type() === "error") {
      const text = message.text();
      if (!isResourceLoadConsoleError(text)) {
        routeEvents.push({ route: route.id, type: "console-error", text: text.slice(0, 500) });
      }
    }
  };
  const onPageError = (error) => {
    routeEvents.push({ route: route.id, type: "pageerror", text: error.message.slice(0, 500) });
  };
  const onRequest = (request) => {
    if (isAuditedRequest(request.url())) {
      requests.push({ url: request.url(), method: request.method(), start: Date.now() });
    }
  };
  const onRequestFailed = (request) => {
    const failure = request.failure()?.errorText || "unknown";
    if (isAuditedRequest(request.url()) && failure !== "net::ERR_ABORTED") {
      failedRequests.push({ url: request.url(), method: request.method(), failure });
    }
  };
  const onResponse = (response) => {
    if (!isAuditedRequest(response.url())) return;
    const request = response.request();
    const item = [...requests].reverse().find((candidate) => candidate.url === response.url() && candidate.method === request.method() && !candidate.status);
    if (item) {
      item.status = response.status();
      item.ms = Date.now() - item.start;
    }
    responses.push({
      url: response.url(),
      method: request.method(),
      status: response.status(),
      resource_type: request.resourceType(),
    });
  };
  page.on("console", onConsole);
  page.on("pageerror", onPageError);
  page.on("request", onRequest);
  page.on("requestfailed", onRequestFailed);
  page.on("response", onResponse);

  const startedAt = Date.now();
  let navigationError = "";
  let bodyText = "";
  let title = "";
  let screenshot = "";
  try {
    await page.goto(`${browserURL}${route.path}`, { waitUntil: "domcontentloaded", timeout: 10_000 });
    if (route.auth !== false) {
      await page.evaluate((authToken) => {
        localStorage.setItem("multica_token", authToken);
        localStorage.setItem("multica:chat:isOpen", "false");
      }, token);
    }
    await waitForRouteText(page, route.expect);
    bodyText = await page.locator("body").innerText({ timeout: 5_000 }).catch(() => "");
    title = await page.title().catch(() => "");
    screenshot = path.join(screenshotDir, `${route.id}-${stamp}.png`);
    await page.screenshot({ path: screenshot, fullPage: false, timeout: 5_000 }).catch(() => {});
  } catch (error) {
    navigationError = error instanceof Error ? error.message : String(error);
  } finally {
    page.off("console", onConsole);
    page.off("pageerror", onPageError);
    page.off("request", onRequest);
    page.off("requestfailed", onRequestFailed);
    page.off("response", onResponse);
  }

  const elapsedMs = Date.now() - startedAt;
  const badStatuses = responses
    .filter((item) => item.status >= 400)
    .map((item) => ({ status: item.status, path: requestPath(item.url), resource_type: item.resource_type }))
    .slice(0, 20);
  const apiRequests = requests.filter((item) => item.url.includes("/api/"));
  const apiPathCounts = countByPath(apiRequests);
  const slowRequests = requests
    .filter((item) => (item.ms ?? 0) > maxApiMs)
    .map((item) => ({ status: item.status, ms: item.ms, path: requestPath(item.url) }))
    .slice(0, 20);
  const missingExpectedText = route.expect.filter((text) => !bodyText.includes(text));
  const forbiddenMatches = forbiddenText.filter((text) => bodyText.includes(text));
  const uiContract = await auditRouteUiContract(page, route, bodyText);
  const loadingResidue = ["Rendering", "Compiling", "Loading", "加载中", "渲染中"].filter((text) => bodyText.includes(text));
  const failures = [
    ...(navigationError ? [`导航失败：${navigationError.split("\n")[0]}`] : []),
    ...(elapsedMs > maxRouteMs ? [`页面耗时 ${elapsedMs}ms 超过 ${maxRouteMs}ms`] : []),
    ...(bodyText.trim().length === 0 ? ["页面 body 为空"] : []),
    ...missingExpectedText.map((text) => `缺少期望文本：${text}`),
    ...badStatuses.map((item) => `请求状态异常：${item.status} ${item.path}`),
    ...failedRequests.map((item) => `请求失败：${requestPath(item.url)} ${item.failure}`),
    ...(apiRequests.length > maxApiRequests ? [`API 请求数 ${apiRequests.length} 超过 ${maxApiRequests}`] : []),
    ...slowRequests.map((item) => `慢请求：${item.ms}ms ${item.path}`),
    ...uiContract.failures,
    ...loadingResidue.map((text) => `加载残留：${text}`),
    ...forbiddenMatches.map((text) => `中文语义/营销残留：${text}`),
  ];

  return {
    id: route.id,
    label: route.label,
    url: `${browserURL}${route.path}`,
    final_url: page.url(),
    title,
    elapsed_ms: elapsedMs,
    ok: failures.length === 0,
    failures,
    api_request_count: apiRequests.length,
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

async function waitForRouteText(page, expectedTexts) {
  await page
    .waitForFunction(
      (expected) => {
        const text = document.body?.innerText || "";
        if (expected.some((item) => text.includes(item))) return true;
        return text.trim().length > 50;
      },
      expectedTexts,
      { timeout: 8_000 },
    )
    .catch(() => {});
}

function trimSlash(value) {
  return value.replace(/\/+$/, "");
}

function isAuditedRequest(url) {
  return url.startsWith(frontendURL) || url.startsWith(browserURL) || url.startsWith(backendURL);
}

function isResourceLoadConsoleError(text) {
  return text.startsWith("Failed to load resource:");
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
    const path = requestPath(request.url);
    counts.set(path, (counts.get(path) || 0) + 1);
  }
  return Array.from(counts.entries())
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([path, count]) => ({ path, count }));
}
