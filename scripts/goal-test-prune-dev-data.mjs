import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const pruneTargetEnv = normalizePruneEnv(readArg("env") || process.env.GOAL_TEST_PRUNE_ENV || "int");
const pruneEnvFileName = pruneTargetEnv === "prod" ? "goal-test-prod.env" : "goal-test-int.env";
const pruneEnvFilePath = path.join(repoRoot, ".run/env", pruneEnvFileName);
const env = {
  ...readEnvFile(pruneEnvFilePath),
  ...process.env,
};

const apiBase = trimSlash(env.GOAL_TEST_BACKEND_URL || env.REMOTE_API_URL || env.NEXT_PUBLIC_API_URL || "http://127.0.0.1:18762");
const account = env.GOAL_TEST_ACCOUNT || env.E2E_ACCOUNT || "goal-test-daemon";
const password = env.GOAL_TEST_PASSWORD || env.E2E_PASSWORD || "e2e-password";
const workspaceSlug = env.GOAL_TEST_WORKSPACE_SLUG || env.E2E_WORKSPACE || "goal-test-daemon";
const artifactRoot = acceptanceDir(repoRoot, env.GOAL_TEST_PRUNE_DEV_DATA_DIR);
const apply = process.argv.includes("--apply");
const canonicalSOPOnly = process.argv.includes("--canonical-sop-only") || env.GOAL_TEST_CANONICAL_SOP_ONLY === "1";
const keep = positiveInt(readArg("keep") ?? env.GOAL_TEST_PRUNE_DEV_DATA_KEEP, 5);
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");

const CANONICAL_PROJECT_TITLES = new Set(["usercenter", "gateway", "ida-deployment"]);
const CANONICAL_AGENT_NAMES = new Set(["pm", "01", "02", "03", "04", "05"]);
const CANONICAL_SQUAD_NAMES = new Set(["pm"]);

const PROTECTED_NAMES = new Set([
  ...CANONICAL_PROJECT_TITLES,
  ...CANONICAL_AGENT_NAMES,
  ...CANONICAL_SQUAD_NAMES,
  "用户中心需求澄清提示词",
  "goal-test 联调工作区",
]);

const DEV_PATTERNS = [
  /\bE2E\b/i,
  /\bcurl\b/i,
  /\bgoal-test\b/i,
  /\bsmoke(?:\s|-)?test\b/i,
  /Dataset Stream 压测/,
  /prompt-eval-codex/i,
  /端到端验收/,
  /真实端到端/,
  /真实.*Agent/i,
  /真实.*智能体/,
  /训练闭环/,
  /生产部署验收/,
  /生产验收/,
  /页面验收/,
  /验收创建/,
  /验收小队/,
  /验收智能体/,
  /验收\s*Agent/i,
  /Codex\s*验收/i,
  /数据集版本治理/,
  /user-center 小队/,
  /Multica 编码小队/,
  /SOP Delivery Squad/,
  /柳贵测试开发/,
];

mkdirSync(artifactRoot, { recursive: true });

const token = await login();
const workspace = await getWorkspace(token);
const runtimeBlockingSquadIds = await collectRuntimeBlockingSquadIds(token);

const categories = [
  {
    id: "issues",
    label: "任务",
    list: () => getJSON(token, "/api/issues?limit=500"),
    items: (data) => arrayFrom(data, "items", "issues"),
    text: (item) => [item.identifier, item.title, item.description, item.project?.title, item.assignee?.name, item.metadata],
    date: (item) => item.updated_at || item.created_at,
    action: (item) => del(token, `/api/issues/${item.id}`),
  },
  {
    id: "projects",
    label: "项目",
    list: () => getJSON(token, "/api/projects"),
    items: (data) => arrayFrom(data, "items", "projects"),
    text: (item) => [item.title, item.description],
    date: (item) => item.updated_at || item.created_at,
    forcePrune: (item) => canonicalSOPOnly && !CANONICAL_PROJECT_TITLES.has(String(item.title || "").trim()),
    action: (item) => del(token, `/api/projects/${item.id}`),
  },
  {
    id: "agents",
    label: "智能体",
    list: () => getJSON(token, "/api/agents?include_archived=true"),
    items: (data) => arrayFrom(data),
    text: (item) => [item.name, item.description, item.instructions, item.metadata],
    date: (item) => item.updated_at || item.created_at || item.last_seen_at,
    skip: (item) => Boolean(item.archived_at),
    forcePrune: (item) => canonicalSOPOnly && !CANONICAL_AGENT_NAMES.has(String(item.name || "").trim()),
    action: (item) => postJSON(token, `/api/agents/${item.id}/archive`, {}),
  },
  {
    id: "squads",
    label: "小队",
    list: () => getJSON(token, "/api/squads"),
    items: (data) => arrayFrom(data),
    text: (item) => [item.name, item.description, item.sop_profile],
    date: (item) => item.updated_at || item.created_at,
    forcePrune: (item) => runtimeBlockingSquadIds.has(item.id) || (canonicalSOPOnly && !CANONICAL_SQUAD_NAMES.has(String(item.name || "").trim())),
    action: (item) => del(token, `/api/squads/${item.id}`),
  },
  {
    id: "runtimes",
    label: "运行时",
    list: () => getJSON(token, "/api/runtimes"),
    items: (data) => arrayFrom(data),
    text: (item) => [item.name, item.daemon_id, item.device_info, item.metadata],
    date: (item) => item.last_seen_at || item.updated_at || item.created_at,
    skip: (item) => item.status === "online",
    action: (item) => del(token, `/api/runtimes/${item.id}`),
  },
  {
    id: "prompt_library",
    label: "提示词",
    list: () => getJSON(token, "/api/prompt-library"),
    items: (data) => arrayFrom(data, "items"),
    text: (item) => [item.name, item.description, item.content, item.tags],
    date: (item) => item.updated_at || item.created_at,
    action: (item) => del(token, `/api/prompt-library/${item.id}`),
  },
  {
    id: "training_assets",
    label: "训练与评估资产",
    list: () => getJSON(token, "/api/prompt-evaluation-assets"),
    items: (data) => arrayFrom(data, "items"),
    text: (item) => [item.name, item.description, item.asset_type, item.payload],
    date: (item) => item.updated_at || item.created_at,
    action: (item) => del(token, `/api/prompt-evaluation-assets/${item.id}`),
  },
  {
    id: "inbox",
    label: "收件箱",
    list: () => getJSON(token, "/api/inbox"),
    items: (data) => arrayFrom(data),
    text: (item) => [item.title, item.body, item.details, item.type],
    date: (item) => item.created_at || item.updated_at,
    skip: (item) => Boolean(item.archived_at),
    action: (item) => postJSON(token, `/api/inbox/${item.id}/archive`, {}),
  },
];

const evidence = {
  schema: "multica.goal_test.dev_data_prune.v1",
  generated_at: generatedAt,
  mode: apply ? "apply" : "dry-run",
  target_environment: pruneTargetEnv,
  env_file: path.relative(repoRoot, pruneEnvFilePath),
  backend_url: apiBase,
  workspace_slug: workspaceSlug,
  workspace_id: workspace.id,
  account,
  keep_per_category: keep,
  canonical_sop_only: canonicalSOPOnly,
  categories: [],
  summary: {
    candidate_count: 0,
    kept_count: 0,
    planned_count: 0,
    applied_count: 0,
    failed_count: 0,
  },
};

for (const category of categories) {
  const listed = await category.list();
  const allItems = category.items(listed).filter((item) => item?.id);
  const candidates = allItems
    .filter((item) => !isProtectedSeed(item))
    .filter((item) => !category.skip?.(item))
    .filter((item) => category.forcePrune?.(item) || isDevelopmentRecord(category.text(item)))
    .sort((a, b) => Date.parse(category.date(b) || "") - Date.parse(category.date(a) || ""));
  const forced = candidates.filter((item) => category.forcePrune?.(item));
  const eligibleForKeep = candidates.filter((item) => !category.forcePrune?.(item));
  const kept = eligibleForKeep.slice(0, keep);
  const planned = [...forced, ...eligibleForKeep.slice(keep)];
  const result = {
    id: category.id,
    label: category.label,
    total_count: allItems.length,
    candidate_count: candidates.length,
    kept_count: kept.length,
    planned_count: planned.length,
    applied_count: 0,
    failed_count: 0,
    kept: kept.map(itemSummary),
    planned: planned.map(itemSummary),
    failures: [],
  };
  if (apply) {
    for (const item of planned) {
      try {
        await category.action(item);
        result.applied_count += 1;
      } catch (error) {
        result.failed_count += 1;
        result.failures.push({ ...itemSummary(item), error: String(error?.message || error) });
      }
    }
  }
  evidence.categories.push(result);
  evidence.summary.candidate_count += result.candidate_count;
  evidence.summary.kept_count += result.kept_count;
  evidence.summary.planned_count += result.planned_count;
  evidence.summary.applied_count += result.applied_count;
  evidence.summary.failed_count += result.failed_count;
}

const artifactPrefix = pruneTargetEnv === "prod" ? "prod-data-prune" : "dev-data-prune";
const artifactPath = path.join(artifactRoot, `${artifactPrefix}-${stamp}.json`);
const latestPath = path.join(artifactRoot, `${artifactPrefix}-latest.json`);
writeFileSync(artifactPath, JSON.stringify(evidence, null, 2) + "\n");
writeFileSync(latestPath, JSON.stringify(evidence, null, 2) + "\n");
console.log(JSON.stringify({ ok: evidence.summary.failed_count === 0, artifact: artifactPath, latest: latestPath, summary: evidence.summary }, null, 2));
if (evidence.summary.failed_count > 0) process.exitCode = 1;

async function login() {
  const data = await postJSON(null, "/auth/login", { account, password });
  if (!data?.token) throw new Error("login response did not include token");
  return data.token;
}

async function getWorkspace(authToken) {
  const data = await getJSON(authToken, "/api/workspaces");
  const items = Array.isArray(data) ? data : data.items ?? [];
  const found = items.find((item) => item.slug === workspaceSlug);
  if (!found?.id) throw new Error(`workspace ${workspaceSlug} not found`);
  return found;
}

function isDevelopmentRecord(values) {
  const text = stringify(values);
  return DEV_PATTERNS.some((pattern) => pattern.test(text));
}

function isProtectedSeed(item) {
  const name = String(item.name || item.title || "").trim();
  return PROTECTED_NAMES.has(name);
}

async function collectRuntimeBlockingSquadIds(authToken) {
  const [agentsData, squadsData] = await Promise.all([
    getJSON(authToken, "/api/agents?include_archived=true"),
    getJSON(authToken, "/api/squads"),
  ]);
  const agents = arrayFrom(agentsData);
  const squads = arrayFrom(squadsData);
  const archivedLeaderByID = new Map(
    agents
      .filter((agent) => agent?.id && agent.archived_at && agent.runtime_id)
      .map((agent) => [agent.id, agent]),
  );
  return new Set(
    squads
      .filter((squad) => {
        const leader = archivedLeaderByID.get(squad.leader_id);
        return leader && isDevelopmentRecord([squad.name, squad.description, leader.name, leader.description]);
      })
      .map((squad) => squad.id),
  );
}

function itemSummary(item) {
  return {
    id: item.id,
    name: item.name || item.title || item.identifier || item.id,
    status: item.archived_at ? "archived" : item.status || "",
    created_at: item.created_at || "",
    updated_at: item.updated_at || "",
  };
}

function arrayFrom(data, ...keys) {
  if (Array.isArray(data)) return data;
  for (const key of keys) {
    if (Array.isArray(data?.[key])) return data[key];
  }
  return [];
}

function stringify(value) {
  if (value == null) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  if (Array.isArray(value)) return value.map(stringify).join(" ");
  try {
    return JSON.stringify(value);
  } catch {
    return "";
  }
}

async function getJSON(authToken, route) {
  return fetchJSON(authToken, route, { method: "GET" });
}

async function postJSON(authToken, route, body) {
  return fetchJSON(authToken, route, { method: "POST", body: JSON.stringify(body) });
}

async function del(authToken, route) {
  return fetchJSON(authToken, route, { method: "DELETE" });
}

async function fetchJSON(authToken, route, init = {}) {
  const headers = {
    accept: "application/json",
    "content-type": "application/json",
    "x-workspace-slug": workspaceSlug,
    ...(init.headers || {}),
  };
  if (authToken) headers.authorization = `Bearer ${authToken}`;
  const response = await fetch(`${apiBase}${route}`, { ...init, headers });
  if (!response.ok) {
    throw new Error(`${init.method || "GET"} ${route} failed: ${response.status} ${await response.text()}`);
  }
  if (response.status === 204) return {};
  const text = await response.text();
  return text ? JSON.parse(text) : {};
}

function readEnvFile(file) {
  try {
    const content = readFileSync(file, "utf8");
    const parsed = {};
    for (const line of content.split(/\r?\n/)) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith("#") || !trimmed.includes("=")) continue;
      const index = trimmed.indexOf("=");
      parsed[trimmed.slice(0, index)] = trimmed.slice(index + 1).replace(/^['"]|['"]$/g, "");
    }
    return parsed;
  } catch {
    return {};
  }
}

function normalizePruneEnv(value) {
  const normalized = String(value || "").trim().toLowerCase();
  if (normalized === "int" || normalized === "prod") return normalized;
  throw new Error(`Unsupported prune environment: ${value}. Use int or prod.`);
}

function trimSlash(value) {
  return String(value || "").replace(/\/+$/, "");
}

function positiveInt(value, fallback) {
  const parsed = Number.parseInt(String(value || ""), 10);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : fallback;
}

function readArg(name) {
  const prefix = `--${name}=`;
  const found = process.argv.find((arg) => arg.startsWith(prefix));
  if (found) return found.slice(prefix.length);
  const index = process.argv.indexOf(`--${name}`);
  return index >= 0 ? process.argv[index + 1] : null;
}
