import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const runEnv = readEnvFile(path.join(repoRoot, ".run/env/goal-test-int.env"));
const env = {
  ...runEnv,
  ...process.env,
};

const apiBase = trimSlash(process.env.GOAL_TEST_BACKEND_URL || runEnv.REMOTE_API_URL || runEnv.NEXT_PUBLIC_API_URL || "http://127.0.0.1:18762");
const account = env.GOAL_TEST_ACCOUNT || env.E2E_ACCOUNT || "develop";
const password = env.GOAL_TEST_PASSWORD || env.E2E_PASSWORD || "develop123";
const workspaceSlug = env.GOAL_TEST_WORKSPACE_SLUG || env.E2E_WORKSPACE || "ai-studio";
const caseCount = positiveInt(env.GOAL_TEST_DATASET_STREAM_CASES, 360);
const pageSize = positiveInt(env.GOAL_TEST_DATASET_STREAM_PAGE_SIZE, 50);
const maxPageMs = positiveInt(env.GOAL_TEST_DATASET_STREAM_MAX_PAGE_MS, 1500);
const maxTotalMs = positiveInt(env.GOAL_TEST_DATASET_STREAM_MAX_TOTAL_MS, 15000);
const artifactRoot = acceptanceDir(repoRoot, env.GOAL_TEST_DATASET_STREAM_AUDIT_DIR);
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const suffix = Date.now();
const auditTag = `stream-${suffix}`;
const batchTagPrefix = `stream-batch-${suffix}`;

mkdirSync(artifactRoot, { recursive: true });

const failures = [];
let activeWorkspaceId = "";
const token = await login();
const workspace = await getWorkspace(token);
activeWorkspaceId = workspace.id;
const startedAt = Date.now();

const prompt = await postJSON(token, "/api/prompt-library", {
  name: `Dataset Stream 压测提示词 ${suffix}`,
  description: "通过公开 API 创建，用于验证大数据集分页、排序和标签聚合。",
  prompt_type: "需求澄清",
  content: "请处理 {{issue_title}}，输出中文验收结论、风险和下一步。",
  variables: [{ name: "issue_title", label: "任务标题", required: true }],
  tags: ["dataset-stream-audit", auditTag],
  status: "启用",
});
if (!prompt?.id) fail("创建提示词响应缺少 id");

const cases = Array.from({ length: caseCount }, (_, index) => ({
  case_name: `Dataset Stream 压测用例 ${String(index + 1).padStart(4, "0")}`,
  variables: {
    issue_title: `批量数据集分页 ${index + 1}`,
    audit_tag: auditTag,
  },
  expected_contains: ["中文验收结论", `编号 ${index + 1}`],
  tags: [auditTag, `${batchTagPrefix}-${index % 12}`, index % 2 === 0 ? "偶数样本" : "奇数样本"],
}));

const createStartedAt = Date.now();
const dataset = await postJSON(token, "/api/prompt-evaluation-assets", {
  prompt_id: prompt.id,
  name: `Dataset Stream 压测数据集 ${suffix}`,
  description: `公开 API 创建 ${caseCount} 条 payload cases，用于压测 keyset cursor。`,
  asset_type: "数据集",
  payload: {
    schema: "multica.dataset_stream.audit.v1",
    schema_version: 1,
    audit_tag: auditTag,
    cases,
  },
  status: "启用",
});
const createMs = Date.now() - createStartedAt;
if (!dataset?.id) fail("创建数据集响应缺少 id");
if (Number(dataset.dataset_row_count ?? 0) !== caseCount) {
  fail(`数据集行计数不匹配：期望 ${caseCount}，实际 ${dataset.dataset_row_count ?? "缺失"}`);
}

const stream = await readAllPages(token, dataset.id);
if (stream.totalCount !== caseCount) fail(`total_count 不匹配：期望 ${caseCount}，实际 ${stream.totalCount}`);
if (stream.items.length !== caseCount) fail(`分页读取总数不匹配：期望 ${caseCount}，实际 ${stream.items.length}`);
if (new Set(stream.items.map((item) => item.id)).size !== stream.items.length) fail("分页结果存在重复 case id");
for (let index = 0; index < stream.items.length; index += 1) {
  const expectedIndex = index;
  if (Number(stream.items[index]?.case_index) !== expectedIndex) {
    fail(`case_index 顺序不连续：位置 ${index} 实际 ${stream.items[index]?.case_index}`);
    break;
  }
}
const slowPages = stream.pages.filter((page) => page.ms > maxPageMs);
for (const page of slowPages) {
  fail(`分页请求过慢：第 ${page.page} 页 ${page.ms}ms 超过 ${maxPageMs}ms`);
}

const descPage = await timedGetJSON(token, `/api/prompt-evaluation-cases?asset_id=${encodeURIComponent(dataset.id)}&limit=5&sort_by=case_index&sort_direction=desc`);
const descItems = descPage.data.items ?? [];
if (descItems.length !== 5 || Number(descItems[0]?.case_index) !== caseCount - 1 || Number(descItems[4]?.case_index) !== caseCount - 5) {
  fail(`倒序排序首屏不符合预期：${JSON.stringify(descItems.map((item) => item.case_index))}`);
}

const tagSummary = await timedGetJSON(token, `/api/prompt-evaluation-cases/tag-summaries?asset_id=${encodeURIComponent(dataset.id)}&limit=20`);
const auditTagSummary = (tagSummary.data.items ?? []).find((item) => item.tag === auditTag);
if (Number(auditTagSummary?.case_count ?? 0) !== caseCount) {
  fail(`标签聚合不匹配：${auditTag} 期望 ${caseCount}，实际 ${auditTagSummary?.case_count ?? "缺失"}`);
}

const tagDatasetSummary = await timedGetJSON(token, `/api/prompt-evaluation-cases/tag-dataset-summaries?keyword=${encodeURIComponent(auditTag)}&limit=20&top_dataset_limit=5`);
const auditTagDatasetSummary = (tagDatasetSummary.data.items ?? []).find((item) => item.tag === auditTag);
if (Number(auditTagDatasetSummary?.case_count ?? 0) !== caseCount || Number(auditTagDatasetSummary?.dataset_count ?? 0) < 1) {
  fail(`跨数据集标签聚合不匹配：${JSON.stringify(auditTagDatasetSummary)}`);
}

const version = await postJSON(token, `/api/prompt-evaluation-assets/${dataset.id}/dataset-versions`, {
  version_label: "Dataset Stream 压测快照",
  metadata: { 来源: "goal-test-dataset-stream-audit", audit_tag: auditTag },
});
if (!version?.id || Number(version.row_count ?? 0) !== caseCount) {
  fail(`数据集版本快照不匹配：${JSON.stringify(version)}`);
}

const tagTrend = await timedGetJSON(token, `/api/prompt-evaluation-assets/${dataset.id}/dataset-versions/tag-trends?version_limit=1&limit=20`);
const auditTrend = (tagTrend.data.items ?? []).find((item) => item.tag === auditTag && Number(item.version) === Number(version.version));
if (Number(auditTrend?.case_count ?? 0) !== caseCount) {
  fail(`版本标签趋势不匹配：${JSON.stringify(auditTrend)}`);
}

const totalMs = Date.now() - startedAt;
if (totalMs > maxTotalMs) fail(`总耗时 ${totalMs}ms 超过 ${maxTotalMs}ms`);

const payload = {
  schema: "multica.goal_test.dataset_stream_audit.v1",
  generated_at: generatedAt,
  api_base: apiBase,
  account,
  workspace_slug: workspaceSlug,
  workspace_id: workspace.id,
  thresholds: {
    case_count: caseCount,
    page_size: pageSize,
    max_page_ms: maxPageMs,
    max_total_ms: maxTotalMs,
  },
  ok: failures.length === 0,
  failures,
  prompt: {
    id: prompt.id,
    name: prompt.name,
  },
  dataset: {
    id: dataset.id,
    name: dataset.name,
    dataset_row_count: dataset.dataset_row_count,
    audit_tag: auditTag,
    create_ms: createMs,
  },
  stream: {
    total_count: stream.totalCount,
    read_count: stream.items.length,
    page_count: stream.pages.length,
    page_size: pageSize,
    slow_pages: slowPages,
    pages: stream.pages,
    first_case_index: stream.items[0]?.case_index ?? null,
    last_case_index: stream.items.at(-1)?.case_index ?? null,
  },
  sort_check: {
    request_ms: descPage.ms,
    first_indexes: descItems.map((item) => item.case_index),
  },
  tag_summary: {
    request_ms: tagSummary.ms,
    audit_tag_case_count: auditTagSummary?.case_count ?? 0,
  },
  tag_dataset_summary: {
    request_ms: tagDatasetSummary.ms,
    audit_tag_case_count: auditTagDatasetSummary?.case_count ?? 0,
    audit_tag_dataset_count: auditTagDatasetSummary?.dataset_count ?? 0,
    top_datasets: auditTagDatasetSummary?.top_datasets ?? [],
  },
  dataset_version: {
    id: version?.id ?? "",
    version: version?.version ?? 0,
    row_count: version?.row_count ?? 0,
    row_fingerprint: version?.row_fingerprint ?? "",
  },
  version_tag_trend: {
    request_ms: tagTrend.ms,
    audit_tag_case_count: auditTrend?.case_count ?? 0,
  },
  elapsed_ms: totalMs,
};

const jsonPath = path.join(artifactRoot, `dataset-stream-audit-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `dataset-stream-audit-${stamp}.md`);
writeFileSync(jsonPath, `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(markdownPath, renderMarkdown(payload));
writeFileSync(path.join(artifactRoot, "dataset-stream-audit-latest.json"), `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(path.join(artifactRoot, "dataset-stream-audit-summary.md"), renderMarkdown(payload));

console.log(JSON.stringify({ ok: payload.ok, json: jsonPath, markdown: markdownPath, failures }, null, 2));
if (!payload.ok) process.exitCode = 1;

async function readAllPages(token, datasetID) {
  const pages = [];
  const items = [];
  let cursor = "";
  let page = 0;
  let totalCount = 0;
  while (true) {
    page += 1;
    const search = new URLSearchParams({
      asset_id: datasetID,
      limit: String(pageSize),
      sort_by: "case_index",
      sort_direction: "asc",
    });
    if (cursor) search.set("cursor", cursor);
    const response = await timedGetJSON(token, `/api/prompt-evaluation-cases?${search}`);
    const body = response.data;
    const pageItems = body.items ?? [];
    totalCount = Number(body.total_count ?? body.total ?? totalCount);
    pages.push({
      page,
      ms: response.ms,
      count: pageItems.length,
      offset: Number(body.offset ?? 0),
      has_more: Boolean(body.has_more),
      first_case_index: pageItems[0]?.case_index ?? null,
      last_case_index: pageItems.at(-1)?.case_index ?? null,
    });
    items.push(...pageItems);
    if (!body.has_more) break;
    if (!body.next_cursor) {
      fail(`第 ${page} 页 has_more=true 但缺少 next_cursor`);
      break;
    }
    cursor = body.next_cursor;
    if (page > Math.ceil(caseCount / pageSize) + 5) {
      fail("分页页数超过预期，疑似 cursor 循环");
      break;
    }
  }
  return { totalCount, pages, items };
}

async function login() {
  const response = await fetchJSON("/auth/login", {
    method: "POST",
    body: { account, password },
  });
  if (!response.token) throw new Error("登录响应缺少 token");
  return response.token;
}

async function getWorkspace(token) {
  const response = await fetchJSON("/api/workspaces", { token });
  const items = Array.isArray(response) ? response : response.items ?? [];
  const workspace = items.find((item) => item.slug === workspaceSlug);
  if (!workspace?.id) throw new Error(`未找到工作区 ${workspaceSlug}`);
  return workspace;
}

async function postJSON(token, pathname, body) {
  return fetchJSON(pathname, { token, method: "POST", body });
}

async function timedGetJSON(token, pathname) {
  const started = Date.now();
  const data = await fetchJSON(pathname, { token });
  return { data, ms: Date.now() - started };
}

async function fetchJSON(pathname, { token, method = "GET", body } = {}) {
  const response = await fetch(`${apiBase}${pathname}`, {
    method,
    headers: {
      ...(token ? { authorization: `Bearer ${token}` } : {}),
      ...(token && activeWorkspaceId ? { "X-Workspace-ID": activeWorkspaceId } : {}),
      ...(body !== undefined ? { "content-type": "application/json" } : {}),
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  let data = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = text;
  }
  if (!response.ok) {
    throw new Error(`${method} ${pathname} returned ${response.status}: ${typeof data === "string" ? data : JSON.stringify(data)}`);
  }
  return data;
}

function fail(message) {
  failures.push(message);
}

function readEnvFile(file) {
  if (!existsSync(file)) return {};
  const values = {};
  for (const raw of readFileSync(file, "utf8").split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) values[match[1]] = match[2].replace(/^['"]|['"]$/g, "");
  }
  return values;
}

function positiveInt(value, fallback) {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed > 0 ? Math.floor(parsed) : fallback;
}

function trimSlash(value) {
  return String(value || "").replace(/\/+$/, "");
}

function renderMarkdown(payload) {
  const lines = [
    "# Dataset Stream 压测审计",
    "",
    `- 生成时间：${payload.generated_at}`,
    `- 环境：${payload.api_base}`,
    `- 工作区：${payload.workspace_slug}`,
    `- 结果：${payload.ok ? "通过" : "失败"}`,
    `- 数据集：${payload.dataset.name} (${payload.dataset.id})`,
    `- 用例数：${payload.stream.read_count} / ${payload.stream.total_count}`,
    `- 分页：${payload.stream.page_count} 页，每页 ${payload.stream.page_size}`,
    `- 总耗时：${payload.elapsed_ms}ms`,
    `- 最慢分页：${Math.max(...payload.stream.pages.map((page) => page.ms), 0)}ms`,
    `- 标签聚合：${payload.tag_summary.audit_tag_case_count} 条`,
    `- 跨数据集聚合：${payload.tag_dataset_summary.audit_tag_dataset_count} 个数据集 / ${payload.tag_dataset_summary.audit_tag_case_count} 条`,
    `- 版本趋势：${payload.version_tag_trend.audit_tag_case_count} 条`,
    "",
    "## 分页明细",
    "",
    "| 页 | 耗时 ms | 数量 | offset | 首序号 | 末序号 |",
    "| --- | ---: | ---: | ---: | ---: | ---: |",
    ...payload.stream.pages.map((page) => `| ${page.page} | ${page.ms} | ${page.count} | ${page.offset} | ${page.first_case_index ?? ""} | ${page.last_case_index ?? ""} |`),
  ];
  if (payload.failures.length > 0) {
    lines.push("", "## 失败项", "", ...payload.failures.map((item) => `- ${item}`));
  }
  return `${lines.join("\n")}\n`;
}
