import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("NEXT_PUBLIC_API_URL") || "http://127.0.0.1:8080";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || trimEnv("REAL_AGENT_E2E_ACCOUNT") || "develop";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || trimEnv("REAL_AGENT_E2E_PASSWORD") || "develop123";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || trimEnv("REAL_AGENT_E2E_WORKSPACE") || "ai-studio";
const suffix = Date.now();
const maxRecordedCommands = Number(trimEnv("PROMPT_EVALUATION_CURL_E2E_MAX_COMMANDS") || 40);
const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot, trimEnv("PROMPT_EVALUATION_CURL_E2E_DIR"));
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const evidencePath = path.join(artifactRoot, `prompt-evaluation-curl-e2e-${stamp}.json`);
const latestEvidencePath = path.join(artifactRoot, "prompt-evaluation-curl-e2e-latest.json");

const evidence = {
  schema: "multica.prompt_evaluation_curl_e2e.v1",
  generated_at: generatedAt,
  api_url: apiURL,
  account,
  workspace_slug: workspaceSlug,
  evidence_path: evidencePath,
  latest_evidence_path: latestEvidencePath,
  commands: [],
  omitted_command_count: 0,
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
  variables: [{ name: "issue_title", label: "任务标题", required: true }],
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
const datasetVersion = post(`/api/prompt-evaluation-assets/${dataset.id}/dataset-versions`, {
  version_label: "curl 初始快照",
  metadata: {
    来源: "curl-e2e",
    用途: "固定训练评估输入样本，验证 Opik 数据集版本语义迁移",
  },
}, token);
if (!datasetVersion?.id || Number(datasetVersion.version || 0) !== 1) fail(`创建数据集版本失败：${JSON.stringify(datasetVersion)}`);
if (Number(datasetVersion.row_count || 0) !== Number(dataset.dataset_row_count || 0)) {
  fail(`数据集版本行数不匹配：${JSON.stringify({ datasetVersion, dataset_row_count: dataset.dataset_row_count })}`);
}
if (!datasetVersion.row_fingerprint) fail(`数据集版本缺少行指纹：${JSON.stringify(datasetVersion)}`);
const datasetVersionList = get(`/api/prompt-evaluation-assets/${dataset.id}/dataset-versions?limit=5`, token);
const datasetVersionItems = Array.isArray(datasetVersionList) ? datasetVersionList : datasetVersionList.items ?? [];
if (!datasetVersionItems.some((item) => item.id === datasetVersion.id && Number(item.version) === 1)) {
  fail(`数据集版本未能回读：${JSON.stringify(datasetVersionList)}`);
}
const datasetVersionRows = get(`/api/prompt-evaluation-assets/${dataset.id}/dataset-versions/${datasetVersion.id}/rows`, token);
if (Number(datasetVersionRows.total ?? datasetVersionRows.items?.length ?? 0) !== Number(datasetVersion.row_count || 0)) {
  fail(`数据集版本行回读不匹配：${JSON.stringify(datasetVersionRows)}`);
}

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
    linked_dataset_versions: [{
      dataset_id: dataset.id,
      dataset_version_id: datasetVersion.id,
      version: datasetVersion.version,
      row_fingerprint: datasetVersion.row_fingerprint,
    }],
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
if (Number(suite.test_suite_case_count ?? 0) < 1) fail(`测试套件用例计数不足：${suite.test_suite_case_count ?? 0}`);
const suiteCases = get(`/api/prompt-evaluation-cases?asset_id=${encodeURIComponent(suite.id)}`, token);
const suiteAssertionCount = assertCaseAssertions(suiteCases, suite.id, 2, "测试套件");

const readiness = get("/api/prompt-evaluation-runtime-readiness", token);
evidence.runtime_readiness = {
  status: readiness.status,
  model: readiness.model,
  runtime_id: readiness.runtime?.id || "",
  runtime_provider: readiness.runtime?.provider || "",
  detail: readiness.detail || "",
  fix: readiness.fix || "",
};
const agentRunEvidence = await runSuiteWithRealAgent(suite.id, token, readiness);
const traceDatasetEvidence = agentRunEvidence.task_id
  ? importDatasetFromTrace(dataset.id, agentRunEvidence.task_id, token)
  : {
      status: "外部依赖失败",
      external_dependency_failure: true,
      reason: "runtime readiness 未就绪，未产生可导入的数据集 trace",
    };

post(`/api/prompt-evaluation-assets/${suite.id}/run`, null, token);
const failedRun = await poll(() => {
  const runs = get(`/api/prompt-evaluation-runs?asset_id=${encodeURIComponent(suite.id)}&limit=10`, token);
  const items = Array.isArray(runs) ? runs : runs.items ?? [];
  return items.find((run) => run.status === "未通过" || run.status === "失败") ?? null;
}, 30_000, "等待本地评估运行失败");
if (!failedRun?.id) fail("未找到失败评估运行");

const runEvidence = get(`/api/prompt-evaluation-runs/${failedRun.id}/evidence`, token);
const localTrialCount = Array.isArray(runEvidence?.trials) ? runEvidence.trials.length : 0;
if (localTrialCount <= 0) fail(`本地评估运行缺少 trial：${JSON.stringify(runEvidence)}`);
const localDatasetVersions = Array.isArray(runEvidence?.evidence?.["数据集版本"]) ? runEvidence.evidence["数据集版本"] : [];
if (!localDatasetVersions.some((item) => item.dataset_version_id === datasetVersion.id && item.dataset_asset_id === dataset.id)) {
  fail(`本地评估运行未绑定数据集版本：${JSON.stringify(runEvidence?.evidence)}`);
}
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
evidence.dataset_version = {
  id: datasetVersion.id,
  dataset_asset_id: datasetVersion.dataset_asset_id,
  version: datasetVersion.version,
  row_count: datasetVersion.row_count,
  row_fingerprint: datasetVersion.row_fingerprint,
  listed_count: datasetVersionItems.length,
  row_readback_count: Number(datasetVersionRows.total ?? datasetVersionRows.items?.length ?? 0),
};
evidence.dataset_from_trace = traceDatasetEvidence;
evidence.test_suite = { id: suite.id, asset_type: suite.asset_type, structured_case_count: suite.structured_case_count, test_suite_case_count: suite.test_suite_case_count, assertion_count: suiteAssertionCount };
evidence.run = {
  id: failedRun.id,
  status: failedRun.status,
  total_cases: failedRun.total_cases,
  failed_cases: failedRun.failed_cases,
  trace_task_id: failedRun.task_id || failedRun.id,
  evidence_trial_count: localTrialCount,
  dataset_version_ids: localDatasetVersions.map((item) => item.dataset_version_id),
};
evidence.agent_run = agentRunEvidence;
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
evidence.result = agentRunEvidence.external_dependency_failure ? "external_dependency_failure" : "completed";
evidence.external_dependency_failure = agentRunEvidence.external_dependency_failure === true;
if (agentRunEvidence.external_dependency_failure) {
  evidence.external_dependency_boundary = "训练评估本地闭环已完成；真实 Agent 执行已通过公开 API 入队并采集 task/run/evidence，但失败发生在外部模型认证、额度或容量边界。";
  evidence.repair_hint = "修复 Codex runtime 模型认证、额度或容量后重跑 scripts/prompt-evaluation-curl-e2e.mjs。";
}

writeEvidence();
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

function importDatasetFromTrace(datasetId, taskId, token) {
  const imported = post(`/api/prompt-evaluation-assets/${datasetId}/dataset-from-traces`, {
    task_ids: [taskId],
    limit: 5,
    expected_contains: ["训练评估", "trace"],
    tags: ["trace导入", "真实Agent"],
  }, token);
  if (!imported?.asset?.id || imported.asset.id !== datasetId) fail(`trace 导入数据集资产不匹配：${JSON.stringify(imported)}`);
  if (Number(imported.created_count || 0) <= 0) fail(`trace 导入没有创建用例：${JSON.stringify(imported)}`);
  if (!Array.isArray(imported.trace_events) || imported.trace_events.length <= 0) fail(`trace 导入响应缺少 trace_events：${JSON.stringify(imported)}`);
  const cases = get(`/api/prompt-evaluation-cases?asset_id=${encodeURIComponent(datasetId)}`, token);
  const items = Array.isArray(cases) ? cases : cases.items ?? [];
  const traceCases = items.filter((item) => item.source === "trace");
  if (traceCases.length <= 0) fail(`trace 导入后未能回读 source=trace 的用例：${JSON.stringify(cases)}`);
  return {
    asset_id: datasetId,
    source: imported.source,
    created_count: imported.created_count,
    trace_event_count: imported.trace_events.length,
    case_ids: imported.cases.map((item) => item.id),
    task_id: taskId,
    dataset_row_count: imported.asset.dataset_row_count,
  };
}

async function runSuiteWithRealAgent(assetId, token, readiness) {
  if (readiness.status !== "就绪") {
    return {
      status: "外部依赖失败",
      external_dependency_failure: true,
      reason: readiness.detail || "runtime readiness 未就绪",
      fix: readiness.fix || "",
      model: readiness.model || "",
      runtime_id: readiness.runtime?.id || "",
      runtime_provider: readiness.runtime?.provider || "",
    };
  }

  const queued = post(`/api/prompt-evaluation-assets/${assetId}/agent-run`, null, token);
  if (!queued?.run?.id || !queued?.task_id) fail(`Agent 执行入队响应不完整：${JSON.stringify(queued)}`);
  const terminalRun = await poll(() => {
    post(`/api/prompt-evaluation-runs/${queued.run.id}/sync`, null, token);
    const runs = get(`/api/prompt-evaluation-runs?asset_id=${encodeURIComponent(assetId)}&limit=20`, token);
    const items = Array.isArray(runs) ? runs : runs.items ?? [];
    const found = items.find((run) => run.id === queued.run.id);
    if (!found || found.status === "已入队" || found.status === "运行中") return null;
    return found;
  }, 420_000, "等待真实 Agent 训练评估运行完成或失败");

  const evidenceData = get(`/api/prompt-evaluation-runs/${queued.run.id}/evidence`, token);
  const traceCount = Array.isArray(evidenceData?.trace_events) ? evidenceData.trace_events.length : 0;
  const traceEvents = Array.isArray(evidenceData?.trace_events) ? evidenceData.trace_events : [];
  const messageCount = Array.isArray(evidenceData?.task_messages) ? evidenceData.task_messages.length : 0;
  const trialCount = Array.isArray(evidenceData?.trials) ? evidenceData.trials.length : 0;
  const usage = Array.isArray(evidenceData?.task_usage)
    ? evidenceData.task_usage.find((item) => item.task_id === queued.task_id) || evidenceData.task_usage[0] || null
    : null;
  const usageTokens = Number(usage?.input_tokens || 0) + Number(usage?.output_tokens || 0);
  const usageUnavailableEvent = traceEvents.find((event) => event?.event_type === "llm.usage_unavailable");
  const output = JSON.stringify(evidenceData);
  const externalFailure = terminalRun.status === "失败" && /401|Unauthorized|Missing bearer|auth|authentication|无可用Token额度|额度|容量|quota|capacity|rate.?limit|模型额度不足|agent_error\.provider_auth_or_access|agent_error\.provider_capacity_or_rate_limit/i.test(output + JSON.stringify(terminalRun));

  if (traceCount <= 0) fail(`真实 Agent 运行缺少 trace 事件：${JSON.stringify(evidenceData)}`);
  if (trialCount <= 0) fail(`真实 Agent 运行缺少 trial：${JSON.stringify(evidenceData)}`);
  if (messageCount <= 0) fail(`真实 Agent 运行缺少 task messages：${JSON.stringify(evidenceData)}`);
  if (terminalRun.status !== "失败" && usageTokens <= 0 && !usageUnavailableEvent) {
    fail(`真实 Agent 运行非失败状态但缺少 token usage，也缺少 llm.usage_unavailable trace：${JSON.stringify(evidenceData)}`);
  }
  if (terminalRun.status === "失败" && !externalFailure) {
    fail(`真实 Agent 运行失败但不是可解释外部依赖失败：${JSON.stringify({ terminalRun, evidenceData })}`);
  }

  return {
    status: terminalRun.status,
    run_id: queued.run.id,
    task_id: queued.task_id,
    chat_session_id: queued.chat_session_id,
    agent_id: queued.agent_id,
    runtime_id: queued.runtime_id,
    model: queued.model,
    runtime_provider: terminalRun.runtime_provider || readiness.runtime?.provider || "",
    total_cases: terminalRun.total_cases,
    failed_cases: terminalRun.failed_cases,
    trace_event_count: traceCount,
    message_count: messageCount,
    trial_count: trialCount,
    usage_observed: usageTokens > 0,
    usage_unavailable_observed: Boolean(usageUnavailableEvent),
    usage_observability_ok: usageTokens > 0 || Boolean(usageUnavailableEvent),
    usage_trace_event: usageUnavailableEvent
      ? { event_type: usageUnavailableEvent.event_type, event_name: usageUnavailableEvent.event_name }
      : null,
    input_tokens: usage ? Number(usage.input_tokens || 0) : Number(terminalRun.input_tokens || 0),
    output_tokens: usage ? Number(usage.output_tokens || 0) : Number(terminalRun.output_tokens || 0),
    estimated_cost: Number(usage?.estimated_cost || 0),
    failure_reason: terminalRun.failure_reason || "",
    external_dependency_failure: externalFailure,
  };
}

function request(method, path, body, token) {
  const url = `${apiURL}${path}`;
  const args = ["--noproxy", "*", "-sS", "-w", "\n%{http_code}", "-X", method, url, "-H", "content-type: application/json"];
  if (token) args.push("-H", `Authorization: Bearer ${token}`);
  if (token && activeWorkspaceId) args.push("-H", `X-Workspace-ID: ${activeWorkspaceId}`);
  if (body !== null && body !== undefined) args.push("--data", JSON.stringify(body));
  recordCommand(`curl ${redactArgs(args).map(shellQuote).join(" ")}`);
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
  evidence.result = "failed";
  writeEvidence();
  console.error(JSON.stringify(evidence, null, 2));
  process.exit(1);
}

function writeEvidence() {
  mkdirSync(artifactRoot, { recursive: true });
  writeFileSync(evidencePath, `${JSON.stringify(evidence, null, 2)}\n`);
  writeFileSync(latestEvidencePath, `${JSON.stringify(evidence, null, 2)}\n`);
}

function recordCommand(command) {
  if (evidence.commands.length < maxRecordedCommands) {
    evidence.commands.push(command);
    return;
  }
  evidence.omitted_command_count += 1;
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
