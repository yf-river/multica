import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("NEXT_PUBLIC_API_URL") || "http://127.0.0.1:8080";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || trimEnv("REAL_AGENT_E2E_ACCOUNT") || "goal-test-daemon";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || trimEnv("REAL_AGENT_E2E_PASSWORD") || "e2e-password";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || trimEnv("REAL_AGENT_E2E_WORKSPACE") || "goal-test-daemon";
const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.resolve(trimEnv("BUSINESS_TRAINING_SEED_DIR") || path.join(repoRoot, "artifacts/acceptance"));
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const evidencePath = path.join(artifactRoot, `business-training-seed-${stamp}.json`);
const latestEvidencePath = path.join(artifactRoot, "business-training-seed-latest.json");

const PROMPT_NAME = "用户中心业务评估提示词";
const DATASET_NAME = "用户中心需求样例数据集";
const SUITE_NAME = "用户中心需求评估套件";
const EXPERIMENT_NAME = "用户中心提示词对比实验";

let activeWorkspaceId = "";
const evidence = {
  schema: "multica.business_training_seed.v1",
  generated_at: generatedAt,
  api_url: apiURL,
  account,
  workspace_slug: workspaceSlug,
  evidence_path: evidencePath,
  latest_evidence_path: latestEvidencePath,
  result: "unknown",
};

const token = login();
const workspace = findWorkspace(token);
activeWorkspaceId = workspace.id;
evidence.workspace_id = workspace.id;

const prompt = await upsertPrompt(token);
const dataset = await upsertAsset(token, "数据集", DATASET_NAME, {
  prompt_id: prompt.id,
  name: DATASET_NAME,
  description: "用户中心日常需求拆解样例，用于训练与评估默认数据。",
  asset_type: "数据集",
  payload: {
    schema: "multica.training_evaluation.payload.v1",
    schema_version: 1,
    语义版本: "multica.training_evaluation.v1",
    cases: [
      {
        case_name: "用户资料接口需求澄清",
        variables: { issue_title: "为 usercenter 增加用户资料查询接口" },
        expected_contains: ["目标", "边界", "验收条件", "风险"],
        tags: ["usercenter", "需求澄清", "业务样例"],
      },
      {
        case_name: "登录失败排查需求澄清",
        variables: { issue_title: "排查 usercenter 登录失败" },
        expected_contains: ["目标", "边界", "验收条件", "风险"],
        tags: ["usercenter", "故障排查", "业务样例"],
      },
    ],
    metric_contract: ["总用例数", "通过率", "失败原因"],
  },
  status: "启用",
});

const suite = await upsertAsset(token, "测试套件", SUITE_NAME, {
  prompt_id: prompt.id,
  name: SUITE_NAME,
  description: "用户中心提示词业务回归套件，默认纳入运行看板。",
  asset_type: "测试套件",
  payload: {
    schema: "multica.training_evaluation.payload.v1",
    schema_version: 1,
    语义版本: "multica.training_evaluation.v1",
    linked_dataset_ids: [dataset.id],
    cases: [
      {
        case_name: "资料接口需求输出结构",
        variables: { issue_title: "为 usercenter 增加用户资料查询接口" },
        expected_contains: ["目标", "边界", "验收条件", "风险"],
        tags: ["usercenter", "接口需求", "业务样例"],
      },
      {
        case_name: "登录失败需求输出结构",
        variables: { issue_title: "排查 usercenter 登录失败" },
        expected_contains: ["目标", "边界", "验收条件", "风险"],
        tags: ["usercenter", "故障排查", "业务样例"],
      },
    ],
    metric_contract: ["总用例数", "通过数", "失败数", "通过率"],
  },
  status: "启用",
});

const experiment = await upsertAsset(token, "实验", EXPERIMENT_NAME, {
  prompt_id: prompt.id,
  name: EXPERIMENT_NAME,
  description: "比较用户中心需求澄清提示词的结构完整性和中文一致性。",
  asset_type: "实验",
  payload: {
    schema: "multica.training_evaluation.payload.v1",
    schema_version: 1,
    语义版本: "multica.training_evaluation.v1",
    实验对象: PROMPT_NAME,
    对比维度: ["结构完整性", "边界清晰度", "中文一致性"],
    cases: [
      {
        case_name: "资料接口实验样例",
        variables: { issue_title: "为 usercenter 增加用户资料查询接口" },
        expected_contains: ["目标", "验收条件"],
        tags: ["usercenter", "业务样例"],
      },
    ],
  },
  status: "启用",
});

const runAsset = post(`/api/prompt-evaluation-assets/${suite.id}/run`, null, token);
const summary = get("/api/prompt-evaluation-summary", token);
const runTotal = Number(summary?.["运行状态"]?.["运行总数"] ?? 0);
const assetTotal = Number(summary?.["资产统计"]?.["资产总数"] ?? 0);
if (runTotal < 1 || assetTotal < 3) {
  fail(`训练摘要不足：${JSON.stringify({ runTotal, assetTotal, summary })}`);
}

Object.assign(evidence, {
  result: "passed",
  prompt: pick(prompt, ["id", "name", "version", "status"]),
  dataset: pick(dataset, ["id", "name", "asset_type", "dataset_row_count", "structured_case_count"]),
  test_suite: pick(suite, ["id", "name", "asset_type", "test_suite_case_count", "structured_case_count"]),
  experiment: pick(experiment, ["id", "name", "asset_type", "experiment_dimension_count", "structured_case_count"]),
  run_asset: pick(runAsset, ["id", "name", "asset_type", "payload"]),
  business_summary: {
    运行总数: runTotal,
    资产总数: assetTotal,
    输入token: Number(summary?.["指标"]?.["输入token"] ?? 0),
    通过率: Number(summary?.["指标"]?.["通过率"] ?? 0),
  },
});
writeEvidence();
console.log(JSON.stringify(evidence, null, 2));

async function upsertPrompt(token) {
  const existing = findByName(get("/api/prompt-library", token), PROMPT_NAME);
  const payload = {
    name: PROMPT_NAME,
    description: "用户中心小队需求澄清和方案拆解使用的业务提示词。",
    prompt_type: "需求澄清",
    content: "请用中文分析 {{issue_title}}，按目标、边界、方案步骤、验收条件、风险输出，面向团队协作执行。",
    variables: [{ name: "issue_title", label: "任务标题", required: true }],
    tags: ["usercenter", "业务样例", "训练评估"],
    status: "启用",
  };
  if (!existing?.id) return post("/api/prompt-library", payload, token);
  return put(`/api/prompt-library/${existing.id}`, payload, token);
}

async function upsertAsset(token, assetType, name, payload) {
  const assets = get(`/api/prompt-evaluation-assets?asset_type=${encodeURIComponent(assetType)}`, token);
  const existing = findByName(assets, name);
  if (!existing?.id) return post("/api/prompt-evaluation-assets", payload, token);
  return put(`/api/prompt-evaluation-assets/${existing.id}`, payload, token);
}

function login() {
  const data = post("/auth/login", { account, password }, null);
  if (!data?.token) fail("登录响应缺少 token");
  return data.token;
}

function findWorkspace(token) {
  const workspaces = get("/api/workspaces", token);
  const workspace = (Array.isArray(workspaces) ? workspaces : workspaces.items ?? []).find((item) => item.slug === workspaceSlug);
  if (!workspace?.id) fail(`未找到工作区 ${workspaceSlug}`);
  return workspace;
}

function findByName(response, name) {
  const items = Array.isArray(response) ? response : response.items ?? [];
  return items.find((item) => item.name === name) ?? null;
}

function get(pathname, token) {
  return request("GET", pathname, undefined, token);
}

function post(pathname, body, token) {
  return request("POST", pathname, body, token);
}

function put(pathname, body, token) {
  return request("PUT", pathname, body, token);
}

function request(method, pathname, body, token) {
  const headers = { "content-type": "application/json" };
  if (token) {
    headers.authorization = `Bearer ${token}`;
    if (activeWorkspaceId) headers["x-workspace-id"] = activeWorkspaceId;
    headers["x-workspace-slug"] = workspaceSlug;
  }
  const response = fetchSync(`${apiURL}${pathname}`, {
    method,
    headers,
    body: body == null ? undefined : JSON.stringify(body),
  });
  if (response.status < 200 || response.status >= 300) {
    fail(`${method} ${pathname} failed: ${response.status} ${response.body}`);
  }
  if (!response.body.trim()) return {};
  try {
    return JSON.parse(response.body);
  } catch (error) {
    fail(`${method} ${pathname} returned non-json: ${error.message}: ${response.body}`);
  }
}

function fetchSync(url, options) {
  const args = ["-sS", "-X", options.method ?? "GET", "-w", "\n%{http_code}"];
  for (const [key, value] of Object.entries(options.headers ?? {})) {
    args.push("-H", `${key}: ${value}`);
  }
  if (options.body) args.push("--data-binary", options.body);
  args.push(url);
  const output = execFileSync("curl", args, { encoding: "utf8", maxBuffer: 10 * 1024 * 1024 });
  const split = output.lastIndexOf("\n");
  return {
    body: split >= 0 ? output.slice(0, split) : "",
    status: Number(split >= 0 ? output.slice(split + 1) : output),
  };
}

function pick(item, keys) {
  return Object.fromEntries(keys.map((key) => [key, item?.[key]]));
}

function trimEnv(name) {
  const value = process.env[name];
  return typeof value === "string" ? value.trim() : "";
}

function fail(message) {
  evidence.result = "failed";
  evidence.error = message;
  writeEvidence();
  console.error(JSON.stringify(evidence, null, 2));
  process.exit(1);
}

function writeEvidence() {
  mkdirSync(artifactRoot, { recursive: true });
  const text = `${JSON.stringify(evidence, null, 2)}\n`;
  writeFileSync(evidencePath, text);
  writeFileSync(latestEvidencePath, text);
}
