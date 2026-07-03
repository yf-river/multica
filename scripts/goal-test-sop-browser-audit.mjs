import { chromium } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const runEnv = readEnvFile(path.join(repoRoot, ".run/env/goal-test-int.env"));
const artifactRoot = acceptanceDir(repoRoot, process.env.GOAL_TEST_SOP_BROWSER_AUDIT_DIR);
const screenshotDir = path.join(artifactRoot, "sop-browser-audit-screenshots");
const downloadDir = path.join(artifactRoot, "sop-browser-audit-downloads");
const evidencePath = path.resolve(process.env.SOP_E2E_EVIDENCE || path.join(artifactRoot, "sop-customer-comment-e2e-latest.json"));
const evidence = JSON.parse(readFileSync(evidencePath, "utf8"));
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");

const backendURL = trimSlash(process.env.GOAL_TEST_BACKEND_URL || runEnv.REMOTE_API_URL || evidence.api_url || "http://127.0.0.1:18762");
const browserURL = trimSlash(process.env.GOAL_TEST_BROWSER_URL || `http://127.0.0.1:${runEnv.FRONTEND_PORT || "13682"}`);
const workspaceSlug = process.env.GOAL_TEST_WORKSPACE_SLUG || evidence.workspace_slug || "ai-studio";
const account = process.env.GOAL_TEST_ACCOUNT || evidence.account || "develop";
const password = process.env.GOAL_TEST_PASSWORD || "develop123";
const issue = evidence.issue || {};
const stageArtifacts = evidence.stage_artifacts || {};
const mr = evidence.mr_handoff || firstLinkedMergeRequest(evidence) || {};
const tapd = evidence.source_fetch || evidence.tapd || {};
const stageAttachmentCandidates = collectStageAttachmentCandidates(evidence, stageArtifacts);

if (!issue.id) throw new Error(`evidence missing issue.id: ${evidencePath}`);

mkdirSync(screenshotDir, { recursive: true });
mkdirSync(downloadDir, { recursive: true });

const token = await login();
const workspaceID = await resolveWorkspaceID(token);
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
  result.checks.tapd_source_title = evidence.source_fetch?.status === "fetch_failed" || findCheck(evidence, "source_fetch_failed_recorded")
    ? true
    : await tapdCard.getByTestId("tapd-source-title").filter({ hasText: tapd.title || "用户快捷入口需求" }).count() > 0;
  result.checks.tapd_source_id = await tapdCard.getByText(tapd.resource_id || "1147654106001004154").count() > 0;
  const tapdHref = await tapdCard.getByRole("link").first().getAttribute("href");
  result.checks.tapd_source_link = tapdHref === (evidence.tapd_source_url || tapd.url);

  if (mr.url) {
    result.checks.mr_association = await page.locator(`a[href="${mr.url}"]`).count() > 0;
  } else {
    result.checks.mr_association = true;
  }

  const attachments = stageAttachmentCandidates;
  result.checks.stage_attachment_count = attachments.length >= 5;
  if (attachments.length > 0) {
    const first = attachments[0];
    const filename = first.filename;
    if (first.id) {
      const attachmentComment = await findAttachmentComment(first.id, filename);
      result.checks.stage_attachment_comment_api = Boolean(attachmentComment?.id);
      if (attachmentComment?.id) result.stage_attachment_comment_id = attachmentComment.id;
    } else {
      result.checks.stage_attachment_comment_api = true;
    }
    const filenameLocator = page.getByText(filename, { exact: true }).first();
    await scrollUntilVisible(page, filenameLocator, `附件 ${filename}`);
    result.checks.stage_attachment_visible = await filenameLocator.isVisible();
    await filenameLocator
      .locator("xpath=ancestor::div[contains(@class,'rounded-md')][.//button[@aria-label='预览']][1]//button[@aria-label='预览']")
      .click();
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

  result.checks.duration_tooltip = await hoverAndCheckText(page, "总耗时说明", ["Agent 执行耗时", "人工确认耗时"]);
  result.checks.token_tooltip = await hoverAndCheckText(page, "总 Token说明", ["输入 Token", "缓存命中率"]);

  result.checks.node_metric_tooltip = false;
  const nodeMetricHelp = page.getByLabel("节点指标说明").first();
  if (await nodeMetricHelp.count()) {
    await nodeMetricHelp.hover();
    await page.waitForTimeout(300);
    result.checks.node_metric_tooltip = await page.getByText("耗时").count() > 0 || await page.getByText("Token").count() > 0;
    await page.mouse.move(0, 0);
  }

  const nodeDownload = await clickAndRecordDownload(page, "导出节点数据");
  const rawDownload = await clickAndRecordDownload(page, "导出 RAW 交互信息");
  result.checks.node_csv_export = Boolean(nodeDownload);
  result.checks.raw_event_csv_export = Boolean(rawDownload);
  result.checks.node_csv_content = Boolean(nodeDownload?.content_checks?.summary_row && nodeDownload?.content_checks?.node_headers);
  result.checks.raw_event_csv_content = Boolean(rawDownload?.content_checks?.raw_headers && rawDownload?.content_checks?.event_rows);

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
  const savePath = path.join(downloadDir, item.suggested_filename);
  await download.saveAs(savePath);
  item.path = savePath;
  const content = readFileSync(savePath, "utf8");
  item.content_checks = inspectDownloadedCsv(label, content);
  result.downloads.push(item);
  return item;
}

function inspectDownloadedCsv(label, content) {
  const firstLine = content.split(/\r?\n/)[0] || "";
  if (label.includes("节点")) {
    return {
      node_headers: [
        "row_type",
        "total_duration_ms",
        "total_token",
        "total_thinking_rounds",
        "node_key",
        "node_duration_ms",
        "node_token_total",
        "node_thinking_rounds",
      ].every((header) => firstLine.includes(header)),
      summary_row: /^summary,/.test(content) || /\nsummary,/.test(content),
      sop_node_rows: /\nsop_node,/.test(content),
    };
  }
  return {
    raw_headers: [
      "id",
      "kind",
      "category",
      "time",
      "task_id",
      "duration_ms",
      "token_total",
      "metadata_detail",
    ].every((header) => firstLine.includes(header)),
    event_rows: content.trim().split(/\r?\n/).length > 1,
  };
}

function firstLinkedMergeRequest(payload) {
  const check = findCheck(payload, "platform_mr_linked");
  const linked = check?.detail?.linked_pull_requests;
  const first = Array.isArray(linked) ? linked[0] : null;
  if (!first?.html_url) return null;
  return { url: first.html_url, title: first.title || first.html_url };
}

function collectStageAttachmentCandidates(payload, artifacts) {
  if (Array.isArray(artifacts.attachments) && artifacts.attachments.length > 0) {
    return artifacts.attachments.filter((item) => item?.filename);
  }
  const previewCheck = findCheck(payload, "artifact_previews_available");
  const filenames = previewCheck?.detail?.filenames;
  if (!Array.isArray(filenames)) return [];
  return filenames.filter(Boolean).map((filename) => ({ filename }));
}

function findCheck(payload, name) {
  const checks = Array.isArray(payload.checks) ? payload.checks : [];
  return checks.find((item) => item?.name === name || item?.id === name) || null;
}

async function hoverAndCheckText(page, label, texts) {
  const target = page.getByLabel(label).first();
  await target.hover();
  await page.waitForTimeout(800);
  const ok = (await Promise.all(texts.map(async (text) => {
    await page.getByText(text).first().waitFor({ timeout: 2_000 });
    return page.getByText(text).count();
  }).map((check) => check.catch(() => 0)))).every((count) => count > 0);
  await page.mouse.move(0, 0);
  return ok;
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

async function resolveWorkspaceID(authToken) {
  const response = await fetch(`${backendURL}/api/workspaces`, {
    headers: { authorization: `Bearer ${authToken}` },
  });
  if (!response.ok) throw new Error(`workspace list failed: ${response.status} ${await response.text()}`);
  const workspaces = await response.json();
  const workspace = Array.isArray(workspaces) ? workspaces.find((item) => item.slug === workspaceSlug) : null;
  if (!workspace?.id) throw new Error(`workspace not found: ${workspaceSlug}`);
  return workspace.id;
}

async function findAttachmentComment(attachmentID, filename) {
  const response = await fetch(`${backendURL}/api/issues/${issue.id}/comments?summary=false&limit=2000`, {
    headers: {
      authorization: `Bearer ${token}`,
      "x-workspace-id": workspaceID,
    },
  });
  if (!response.ok) throw new Error(`comments lookup failed: ${response.status} ${await response.text()}`);
  const payload = await response.json();
  const comments = Array.isArray(payload) ? payload : payload.items ?? payload.comments ?? [];
  return comments.find((comment) =>
    Array.isArray(comment.attachments) &&
    comment.attachments.some((attachment) => attachment.id === attachmentID || attachment.filename === filename),
  ) || null;
}

async function scrollUntilVisible(page, locator, label) {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    if (await locator.isVisible().catch(() => false)) return;
    await page.evaluate((ratio) => {
      const candidates = Array.from(document.querySelectorAll("main, section, div"))
        .filter((el) => el instanceof HTMLElement && el.scrollHeight > el.clientHeight + 200)
        .sort((a, b) => (b.scrollHeight - b.clientHeight) - (a.scrollHeight - a.clientHeight));
      const scroller = candidates[0] || document.scrollingElement || document.documentElement;
      scroller.scrollTop = Math.min(scroller.scrollHeight, scroller.scrollTop + Math.max(600, scroller.clientHeight * ratio));
    }, attempt < 8 ? 0.75 : 1.2);
    await page.waitForTimeout(250);
  }
  await locator.waitFor({ timeout: 1_000 }).catch((error) => {
    throw new Error(`${label} 未在 issue 页面滚动范围内出现: ${error.message}`);
  });
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
