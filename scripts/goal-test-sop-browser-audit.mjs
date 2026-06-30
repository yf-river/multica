import { chromium } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const runEnv = readEnvFile(path.join(repoRoot, ".run/env/goal-test-int.env"));
const artifactRoot = path.resolve(process.env.GOAL_TEST_SOP_BROWSER_AUDIT_DIR || path.join(repoRoot, "artifacts/acceptance"));
const screenshotDir = path.join(artifactRoot, "sop-browser-audit-screenshots");
const evidencePath = path.resolve(process.env.SOP_E2E_EVIDENCE || path.join(artifactRoot, "sop-customer-comment-e2e-latest.json"));
const evidence = JSON.parse(readFileSync(evidencePath, "utf8"));
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");

const backendURL = trimSlash(process.env.GOAL_TEST_BACKEND_URL || runEnv.REMOTE_API_URL || evidence.api_url || "http://127.0.0.1:18762");
const browserURL = trimSlash(process.env.GOAL_TEST_BROWSER_URL || `http://127.0.0.1:${runEnv.FRONTEND_PORT || "13682"}`);
const workspaceSlug = process.env.GOAL_TEST_WORKSPACE_SLUG || evidence.workspace_slug || "goal-test-daemon";
const account = process.env.GOAL_TEST_ACCOUNT || evidence.account || "goal-test-daemon";
const password = process.env.GOAL_TEST_PASSWORD || "e2e-password";
const issue = evidence.issue || {};
const stageArtifacts = evidence.stage_artifacts || {};
const mr = evidence.mr_handoff || {};

if (!issue.id) throw new Error(`evidence missing issue.id: ${evidencePath}`);

mkdirSync(screenshotDir, { recursive: true });

const token = await login();
const browser = await chromium.launch({ headless: true, args: ["--no-proxy-server"] });
const context = await browser.newContext({
  viewport: { width: 1440, height: 1000 },
  ignoreHTTPSErrors: true,
  acceptDownloads: true,
});
await context.addCookies([{ name: "multica_logged_in", value: "1", url: browserURL, sameSite: "Lax" }]);
await context.addInitScript((authToken) => {
  localStorage.setItem("multica_token", authToken);
  localStorage.setItem("multica:chat:isOpen", "false");
}, token);

const result = {
  schema: "multica.goal_test.sop_browser_audit.v1",
  generated_at: generatedAt,
  evidence_path: evidencePath,
  browser_url: browserURL,
  backend_url: backendURL,
  workspace_slug: workspaceSlug,
  issue_id: issue.id,
  issue_identifier: issue.identifier || "",
  checks: {},
  screenshots: {},
  downloads: [],
  failures: [],
};

try {
  const page = await context.newPage();
  collectBrowserFailures(page, result.failures);
  await auditIssueDetail(page);
  await auditRunReview(page);
  await page.close();
} finally {
  await browser.close();
}

result.ok = result.failures.length === 0 && Object.values(result.checks).every(Boolean);
const jsonPath = path.join(artifactRoot, `sop-browser-audit-${stamp}.json`);
writeFileSync(jsonPath, `${JSON.stringify(result, null, 2)}\n`);
writeFileSync(path.join(artifactRoot, "sop-browser-audit-latest.json"), `${JSON.stringify(result, null, 2)}\n`);
console.log(JSON.stringify({ ok: result.ok, json: jsonPath, failures: result.failures }, null, 2));
if (!result.ok) process.exitCode = 1;

async function auditIssueDetail(page) {
  await page.goto(`${browserURL}/${workspaceSlug}/issues/${encodeURIComponent(issue.id)}`, {
    waitUntil: "domcontentloaded",
    timeout: 30_000,
  });
  await page.locator('[data-testid="tapd-source-card"]').waitFor({ timeout: 30_000 });
  const tapdCard = page.locator('[data-testid="tapd-source-card"]').first();
  result.checks.tapd_source_card = await tapdCard.isVisible();
  result.checks.tapd_source_title = await tapdCard.getByTestId("tapd-source-title").filter({ hasText: evidence.source_fetch?.title || "用户快捷入口需求" }).count() > 0;
  result.checks.tapd_source_id = await tapdCard.getByText(evidence.source_fetch?.resource_id || "1147654106001004154").count() > 0;
  const tapdHref = await tapdCard.getByRole("link").first().getAttribute("href");
  result.checks.tapd_source_link = tapdHref === evidence.tapd_source_url;

  if (mr.url) {
    result.checks.mr_association = await page.locator(`a[href="${mr.url}"]`).count() > 0;
  } else {
    result.checks.mr_association = false;
  }

  const attachments = Array.isArray(stageArtifacts.attachments) ? stageArtifacts.attachments : [];
  result.checks.stage_attachment_count = attachments.length >= 5;
  if (attachments.length > 0) {
    const first = attachments[0];
    const filename = first.filename;
    await page.getByText(filename, { exact: true }).first().waitFor({ timeout: 30_000 });
    result.checks.stage_attachment_visible = await page.getByText(filename, { exact: true }).first().isVisible();
    await page.getByLabel("预览").first().click();
    await page.getByRole("dialog", { name: filename }).waitFor({ timeout: 30_000 });
    result.checks.stage_attachment_preview = await page.getByRole("dialog", { name: filename }).isVisible();
    await page.screenshot({ path: path.join(screenshotDir, `issue-${issue.id}-attachment-preview.png`), fullPage: false });
    result.screenshots.issue_attachment_preview = path.join(screenshotDir, `issue-${issue.id}-attachment-preview.png`);
    await page.getByLabel("关闭").click();
  } else {
    result.checks.stage_attachment_visible = false;
    result.checks.stage_attachment_preview = false;
  }

  await page.screenshot({ path: path.join(screenshotDir, `issue-${issue.id}-detail.png`), fullPage: true });
  result.screenshots.issue_detail = path.join(screenshotDir, `issue-${issue.id}-detail.png`);
}

async function auditRunReview(page) {
  await page.goto(`${browserURL}/${workspaceSlug}/run-reviews?issue=${encodeURIComponent(issue.id)}`, {
    waitUntil: "domcontentloaded",
    timeout: 30_000,
  });
  await page.getByRole("heading", { name: "运行复盘" }).waitFor({ timeout: 30_000 });
  const issueTitle = evidence.issue?.title || issue.title || "";
  result.checks.run_review_page = issueTitle
    ? (await page.getByRole("heading", { name: issueTitle }).count().catch(() => 0)) > 0
    : true;
  result.checks.thinking_rounds_label = await page.getByText("思考轮次").count() > 0;
  result.checks.horizontal_timeline = await page.getByTestId("run-review-horizontal-timeline").isVisible();

  await page.getByLabel("总耗时说明").click();
  result.checks.duration_tooltip = await page.getByText("Agent 执行耗时").count() > 0 && await page.getByText("人工确认耗时").count() > 0;
  await page.keyboard.press("Escape");

  await page.getByLabel("总 Token说明").click();
  result.checks.token_tooltip = await page.getByText("输入").count() > 0 && await page.getByText("缓存命中率").count() > 0;
  await page.keyboard.press("Escape");

  result.checks.node_metric_tooltip = false;
  const nodeMetricHelp = page.getByLabel("节点指标说明").first();
  if (await nodeMetricHelp.count()) {
    await nodeMetricHelp.click();
    result.checks.node_metric_tooltip = await page.getByText("耗时").count() > 0 || await page.getByText("Token").count() > 0;
    await page.keyboard.press("Escape");
  }

  const nodeDownload = await clickAndRecordDownload(page, "导出节点数据");
  const rawDownload = await clickAndRecordDownload(page, "导出 RAW 交互信息");
  result.checks.node_csv_export = Boolean(nodeDownload);
  result.checks.raw_event_csv_export = Boolean(rawDownload);

  await page.screenshot({ path: path.join(screenshotDir, `issue-${issue.id}-run-review.png`), fullPage: true });
  result.screenshots.run_review = path.join(screenshotDir, `issue-${issue.id}-run-review.png`);
}

async function clickAndRecordDownload(page, label) {
  const button = page.getByRole("button", { name: label }).first();
  if (!(await button.count())) return null;
  const download = await Promise.all([
    page.waitForEvent("download", { timeout: 15_000 }),
    button.click(),
  ]).then(([d]) => d).catch((error) => {
    result.failures.push(`${label} download failed: ${error.message}`);
    return null;
  });
  if (!download) return null;
  const item = { label, suggested_filename: download.suggestedFilename() };
  result.downloads.push(item);
  return item;
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

function collectBrowserFailures(page, failures) {
  page.on("console", (message) => {
    if (message.type() === "error" && !message.text().includes("Failed to load resource")) {
      failures.push(`console-error: ${message.text().slice(0, 500)}`);
    }
  });
  page.on("pageerror", (error) => failures.push(`pageerror: ${error.message.slice(0, 500)}`));
  page.on("response", (response) => {
    if (response.status() >= 500) failures.push(`http-${response.status()}: ${response.url()}`);
  });
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
  return String(value || "").replace(/\/+$/, "");
}
