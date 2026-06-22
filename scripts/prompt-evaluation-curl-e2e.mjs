import { execFileSync } from "node:child_process";
import process from "node:process";

const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("NEXT_PUBLIC_API_URL") || "http://127.0.0.1:8080";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || trimEnv("REAL_AGENT_E2E_ACCOUNT") || "goal-test-daemon";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || trimEnv("REAL_AGENT_E2E_PASSWORD") || "e2e-password";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || trimEnv("REAL_AGENT_E2E_WORKSPACE") || "goal-test-daemon";
const suffix = Date.now();

const evidence = {
  schema: "multica.prompt_evaluation_curl_e2e.v1",
  api_url: apiURL,
  account,
  workspace_slug: workspaceSlug,
  commands: [],
  result: "unknown",
};
let activeWorkspaceId = "";

const login = post("/auth/login", { account, password }, null);
const token = login.token;
if (!token) fail("登录响应缺少 token");

const workspaces = get("/api/workspaces", token);
const workspace = (Array.isArray(workspaces) ? workspaces : workspaces.items ?? []).find((item) => item.slug === workspaceSlug);
if (!workspace?.id) fail(`未找到工作区 ${workspaceSlug}`);
activeWorkspaceId = workspace.id;
evidence.workspace_id = workspace.id;

const prompt = post("/api/prompt-library", {
  name: `curl 训练闭环提示词 ${suffix}`,
  description: "通过公开 API 创建，用于验证提示词版本、数据集、测试套件、实验运行和优化候选发布。",
  prompt_type: "需求澄清",
  content: "请用中文澄清 {{issue_title}}，输出目标、边界、验收条件和风险。",
  variables: [{ name: "issue_title", label: "Issue 标题", required: true }],
  tags: ["curl-e2e", "训练与评估"],
  status: "启用",
}, token);
if (!prompt?.id) fail("创建提示词响应缺少 id");

const initialVersions = get(`/api/prompt-library/${prompt.id}/versions`, token);
assertVersion(initialVersions, 1, "手动创建", null, "初始提示词版本缺失");

const dataset = post("/api/prompt-evaluation-assets", {
  prompt_id: prompt.id,
  name: `curl 训练闭环数据集 ${suffix}`,
  description: "通过公开 API 创建的结构化数据集。",
  asset_type: "数据集",
  payload: {
    schema: "multica.training_evaluation.payload.v1",
    schema_version: 1,
    语义版本: "multica.training_evaluation.v1",
    cases: [
      {
        case_name: "登录失败澄清",
        variables: { issue_title: "user-center 登录失败" },
        expected_contains: ["验收条件", "trace/task id"],
        tags: ["user-center", "失败用例"],
      },
    ],
    metric_contract: ["总用例数", "通过率", "失败原因", "trace/task id"],
  },
  status: "启用",
}, token);
if (!dataset?.id) fail("创建数据集响应缺少 id");
if (Number(dataset.dataset_row_count ?? 0) < 1) fail(`数据集行计数不足：${dataset.dataset_row_count ?? 0}`);
const datasetCases = get(`/api/prompt-evaluation-cases?asset_id=${encodeURIComponent(dataset.id)}`, token);
const datasetAssertionCount = assertCaseAssertions(datasetCases, dataset.id, 2, "数据集");

const suite = post("/api/prompt-evaluation-assets", {
  prompt_id: prompt.id,
  name: `curl 训练闭环测试套件 ${suffix}`,
  description: "通过公开 API 创建的测试套件，故意包含一个失败断言以触发优化候选。",
  asset_type: "测试套件",
  payload: {
    schema: "multica.training_evaluation.payload.v1",
    schema_version: 1,
    语义版本: "multica.training_evaluation.v1",
    linked_dataset_ids: [dataset.id],
    cases: [
      {
        case_name: "必须输出优化候选",
        variables: { issue_title: "user-center 登录失败" },
        expected_contains: ["这个短语不会出现在普通渲染结果中", "优化候选"],
        tags: ["curl-e2e", "必然失败"],
      },
    ],
    metric_contract: ["总用例数", "通过数", "失败数", "通过率", "失败原因"],
  },
  status: "启用",
}, token);
if (!suite?.id) fail("创建测试套件响应缺少 id");
const suiteCases = get(`/api/prompt-evaluation-cases?asset_id=${encodeURIComponent(suite.id)}`, token);
const suiteAssertionCount = assertCaseAssertions(suiteCases, suite.id, 2, "测试套件");

post(`/api/prompt-evaluation-assets/${suite.id}/run`, null, token);
const failedRun = await poll(() => {
  const runs = get(`/api/prompt-evaluation-runs?asset_id=${encodeURIComponent(suite.id)}&limit=10`, token);
  const items = Array.isArray(runs) ? runs : runs.items ?? [];
  return items.find((run) => run.status === "未通过" || run.status === "失败") ?? null;
}, 30_000, "等待本地评估运行失败");
if (!failedRun?.id) fail("未找到失败评估运行");

const runEvidence = get(`/api/prompt-evaluation-runs/${failedRun.id}/evidence`, token);
const candidate = post(`/api/prompt-evaluation-runs/${failedRun.id}/optimization-candidates`, null, token);
if (!candidate?.id) fail("创建优化候选响应缺少 id");

const edited = put(`/api/prompt-evaluation-optimization-candidates/${candidate.id}`, {
  candidate_name: `${candidate.candidate_name || "curl 优化候选"} 人工确认版`,
  candidate_content: `${candidate.candidate_content || prompt.content}\n\n人工发布要求：保留中文指标，必须输出优化候选、失败原因和 trace/task id。`,
  rationale: `${candidate.rationale || ""}\n人工确认：curl E2E 已检查失败用例和版本链。`,
  edit_note: "curl E2E 人工确认发布前补充验收口径。",
}, token);
if (!edited?.id) fail("编辑优化候选响应缺少 id");

const published = post(`/api/prompt-evaluation-optimization-candidates/${candidate.id}/publish`, null, token);
if (!published?.candidate || !published?.prompt?.id) fail("发布优化候选响应缺少 candidate 或 prompt");

const publishedVersions = get(`/api/prompt-library/${published.prompt.id}/versions`, token);
assertVersion(publishedVersions, Number(published.prompt.version || 2), "优化候选发布", candidate.id, "发布版本历史缺失");

const summary = get("/api/prompt-evaluation-summary", token);
evidence.prompt = { id: prompt.id, version: prompt.version, version_count: itemCount(initialVersions) };
evidence.dataset = { id: dataset.id, asset_type: dataset.asset_type, structured_case_count: dataset.structured_case_count, dataset_row_count: dataset.dataset_row_count, assertion_count: datasetAssertionCount };
evidence.test_suite = { id: suite.id, asset_type: suite.asset_type, structured_case_count: suite.structured_case_count, assertion_count: suiteAssertionCount };
evidence.run = {
  id: failedRun.id,
  status: failedRun.status,
  total_cases: failedRun.total_cases,
  failed_cases: failedRun.failed_cases,
  trace_task_id: failedRun.task_id || failedRun.id,
  evidence_trial_count: Array.isArray(runEvidence?.trials) ? runEvidence.trials.length : 0,
};
evidence.optimization_candidate = {
  id: candidate.id,
  edited_id: edited.id,
  status: published.candidate.status,
  published_prompt_id: published.candidate.published_prompt_id,
  source_candidate_id: candidate.id,
};
evidence.published_prompt = {
  id: published.prompt.id,
  version: published.prompt.version,
  version_count: itemCount(publishedVersions),
};
evidence.summary = summary;
evidence.result = "completed";

console.log(JSON.stringify(evidence, null, 2));

function trimEnv(name) {
  return (process.env[name] || "").trim();
}

function get(path, token) {
  return request("GET", path, null, token);
}

function post(path, body, token) {
  return request("POST", path, body, token);
}

function put(path, body, token) {
  return request("PUT", path, body, token);
}

function request(method, path, body, token) {
  const url = `${apiURL}${path}`;
  const args = ["--noproxy", "*", "-sS", "-w", "\n%{http_code}", "-X", method, url, "-H", "content-type: application/json"];
  if (token) args.push("-H", `Authorization: Bearer ${token}`);
  if (token && activeWorkspaceId) args.push("-H", `X-Workspace-ID: ${activeWorkspaceId}`);
  if (body !== null && body !== undefined) args.push("--data", JSON.stringify(body));
  evidence.commands.push(`curl ${redactArgs(args).map(shellQuote).join(" ")}`);
  const out = execFileSync("curl", args, { encoding: "utf8", maxBuffer: 10 * 1024 * 1024 });
  const splitAt = out.lastIndexOf("\n");
  const responseBody = splitAt >= 0 ? out.slice(0, splitAt) : out;
  const status = Number(splitAt >= 0 ? out.slice(splitAt + 1).trim() : 0);
  if (status < 200 || status >= 300) {
    fail(`${method} ${path} 返回 ${status}: ${responseBody}`);
  }
  return responseBody.trim() ? JSON.parse(responseBody) : null;
}

async function poll(fn, timeoutMs, label) {
  const started = Date.now();
  let last = null;
  while (Date.now() - started < timeoutMs) {
    last = await fn();
    if (last) return last;
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  fail(`${label}超时，最后结果：${JSON.stringify(last)}`);
}

function assertVersion(response, version, source, sourceCandidateID, message) {
  const items = response?.items ?? [];
  const found = items.find((item) => Number(item.version) === Number(version) && item.source === source);
  if (!found) fail(`${message}: ${JSON.stringify(items)}`);
  if (sourceCandidateID !== null && found.source_candidate_id !== sourceCandidateID) {
    fail(`${message}: source_candidate_id=${found.source_candidate_id}, want ${sourceCandidateID}`);
  }
}

function assertCaseAssertions(response, assetId, minAssertions, label) {
  const items = response?.items ?? [];
  const matching = items.filter((item) => item.asset_id === assetId);
  if (matching.length === 0) fail(`${label} 未回读到结构化用例`);
  const count = matching.reduce((sum, item) => sum + (Array.isArray(item.assertions) ? item.assertions.length : 0), 0);
  if (count < minAssertions) fail(`${label} 结构化断言不足：${count}/${minAssertions}`);
  return count;
}

function itemCount(response) {
  return Number(response?.total ?? response?.items?.length ?? 0);
}

function fail(message) {
  evidence.error = message;
  console.error(JSON.stringify(evidence, null, 2));
  process.exit(1);
}

function shellQuote(value) {
  const raw = String(value);
  if (/^[A-Za-z0-9_./:=?&%-]+$/.test(raw)) return raw;
  return `'${raw.replace(/'/g, "'\\''")}'`;
}

function redactArgs(args) {
  return args.map((arg, index) => {
    if (index > 0 && args[index - 1] === "-H" && /^Authorization:/i.test(arg)) {
      return "Authorization: Bearer <redacted>";
    }
    return arg;
  });
}
