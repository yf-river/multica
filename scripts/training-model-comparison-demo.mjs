#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const runEnv = readRunEnv("int");
const apiURL = trimSlash(trimEnv("ACCEPTANCE_API_URL") || trimEnv("GOAL_TEST_INT_API_URL") || runEnv.REMOTE_API_URL || "http://127.0.0.1:18762");
const browserURL = trimSlash(trimEnv("GOAL_TEST_BROWSER_URL") || runEnv.FRONTEND_ORIGIN || "http://9.134.129.162:13682");
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "develop";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "develop123";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "ai-studio";
const provider = trimEnv("MODEL_COMPARISON_PROVIDER") || "codebuddy";
const deepseekModel = trimEnv("MODEL_COMPARISON_DEEPSEEK_MODEL") || "deepseek-v4-pro-ioa";
const requestedKimiModel = trimEnv("MODEL_COMPARISON_KIMI_MODEL");
const judgeModelPreference = (trimEnv("MODEL_COMPARISON_JUDGE_MODELS") || "gpt-5.5,gpt-5.4,gpt-5.4-mini,gpt-5.3-codex-spark")
  .split(",")
  .map((item) => item.trim())
  .filter(Boolean);
const taskTimeoutMs = Number(trimEnv("MODEL_COMPARISON_TASK_TIMEOUT_MS") || 900_000);
const pollIntervalMs = Number(trimEnv("MODEL_COMPARISON_POLL_INTERVAL_MS") || 5_000);
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const humanStamp = new Intl.DateTimeFormat("zh-CN", {
  timeZone: "Asia/Shanghai",
  month: "2-digit",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
}).format(new Date()).replace(/\//g, "-");
const artifactRoot = acceptanceDir(repoRoot, trimEnv("MODEL_COMPARISON_ACCEPTANCE_DIR"));
const evidencePath = path.join(artifactRoot, `training-model-comparison-${stamp}.json`);
const latestEvidencePath = path.join(artifactRoot, "training-model-comparison-latest.json");

const taskCases = [
  {
    case_name: "标准规则实现",
    variables: {
      issue_title: "增强密码强度",
      requirement: "修改密码时，密码长度 8-32 位，至少包含大写字母、小写字母、数字、特殊字符四类中的三类。",
      focus: "输出需求澄清、技术方案、测试要点和风险，说明如何实现三选多字符类别校验。",
    },
    expected_contains: ["8-32", "至少包含3种", "大写字母", "小写字母", "数字", "特殊字符"],
    tags: ["密码强度", "标准规则"],
  },
  {
    case_name: "边界条件和特殊字符范围",
    variables: {
      issue_title: "增强密码强度",
      requirement: "特殊字符只包含 !@#$%^&*()_+|~=`{}[]:\";'<>?,./，长度边界必须覆盖 7、8、32、33 位。",
      focus: "输出边界条件、合法字符范围、非法字符处理和测试用例。",
    },
    expected_contains: ["7", "8", "32", "33", "特殊字符", "非法字符"],
    tags: ["密码强度", "边界条件"],
  },
  {
    case_name: "遗漏场景识别",
    variables: {
      issue_title: "增强密码强度",
      requirement: "需要识别错误实现：只检查长度、特殊字符范围过宽、把两类字符误判为通过、前后端规则不一致。",
      focus: "输出风险清单、回归测试和验收证据，避免只给出正向规则。",
    },
    expected_contains: ["只检查长度", "误判", "前后端", "回归", "验收证据"],
    tags: ["密码强度", "遗漏场景"],
  },
];

const evidence = {
  schema: "multica.training_model_comparison_demo.v1",
  generated_at: generatedAt,
  api_url: apiURL,
  browser_url: browserURL,
  workspace_slug: workspaceSlug,
  provider,
  requested_models: { deepseek: deepseekModel, kimi: requestedKimiModel || "auto:kimi" },
  evidence_path: evidencePath,
  latest_evidence_path: latestEvidencePath,
  assets: {},
  runs: [],
  checks: [],
  ok: false,
};

let token = "";
let workspace = null;

try {
  const login = await post("/auth/login", { account, password }, null, false);
  token = login.token;
  if (!token) fail("登录响应缺少 token");
  evidence.user = pick(login.user || {}, ["id", "account", "name"]);

  workspace = await resolveWorkspace();
  evidence.workspace = pick(workspace, ["id", "slug", "name"]);

  const runtime = await resolveCodeBuddyRuntime();
  evidence.runtime = pick(runtime, ["id", "name", "provider", "status", "last_seen_at"]);

  const runtimeModels = await resolveRuntimeModels(runtime.id);
  evidence.runtime_models = runtimeModels.map((item) => pick(item, ["id", "label", "provider", "default"]));
  const kimiModel = requestedKimiModel || pickKimiModel(runtimeModels);
  if (!runtimeModels.some((item) => item.id === deepseekModel)) {
    fail(`当前 ${provider} runtime 模型列表没有 ${deepseekModel}`, { available_models: runtimeModels.map((item) => item.id) });
  }
  if (!kimiModel) {
    fail("当前 CodeBuddy runtime 没有可用 Kimi 模型，无法完成 DeepSeek 与 Kimi 的真实对比", { available_models: runtimeModels.map((item) => item.id) });
  }
  if (!runtimeModels.some((item) => item.id === kimiModel)) {
    fail(`当前 ${provider} runtime 模型列表没有 ${kimiModel}`, { available_models: runtimeModels.map((item) => item.id) });
  }
  evidence.selected_models = { deepseek: deepseekModel, kimi: kimiModel };

  const prompt = await createPrompt();
  evidence.assets.prompt = pick(prompt, ["id", "name", "version", "status"]);
  const dataset = await createDataset(prompt.id);
  evidence.assets.dataset = pick(dataset, ["id", "name", "asset_type", "dataset_row_count", "structured_case_count"]);
  const datasetVersion = await post(`/api/prompt-evaluation-assets/${dataset.id}/dataset-versions`, {
    version_label: "密码强度评估基线",
    metadata: { 用途: "固定模型对比输入，保证 DeepSeek 与 Kimi 使用同一组需求样本" },
  });
  evidence.assets.dataset_version = pick(datasetVersion, ["id", "version", "row_count", "row_fingerprint"]);
  check("dataset_has_three_cases", Number(dataset.dataset_row_count || 0) >= 3 && Number(datasetVersion.row_count || 0) >= 3, evidence.assets.dataset_version);

  const agents = {
    deepseek: await createExecutionAgent(runtime, "DeepSeek V4 Pro", deepseekModel),
    kimi: await createExecutionAgent(runtime, "Kimi", kimiModel),
  };
  evidence.agents = {
    deepseek: pick(agents.deepseek, ["id", "name", "runtime_id", "model"]),
    kimi: pick(agents.kimi, ["id", "name", "runtime_id", "model"]),
  };

  const suites = {
    deepseek: await createSuite(prompt.id, dataset, datasetVersion, agents.deepseek, "DeepSeek V4 Pro"),
    kimi: await createSuite(prompt.id, dataset, datasetVersion, agents.kimi, "Kimi"),
  };
  evidence.assets.suites = {
    deepseek: pick(suites.deepseek, ["id", "name", "asset_type", "test_suite_case_count"]),
    kimi: pick(suites.kimi, ["id", "name", "asset_type", "test_suite_case_count"]),
  };

  const deepseekRun = await runSuiteWithAgent(suites.deepseek, "DeepSeek V4 Pro");
  evidence.runs.push(deepseekRun.summary);
  check("deepseek_run_reached_terminal_state", modelRunHasAuditableOutput(deepseekRun), deepseekRun.summary);

  const kimiRun = await runSuiteWithAgent(suites.kimi, "Kimi");
  evidence.runs.push(kimiRun.summary);
  check("kimi_run_reached_terminal_state", modelRunHasAuditableOutput(kimiRun), kimiRun.summary);

  const judge = await runCodexJudge({ deepseek: deepseekRun, kimi: kimiRun });
  evidence.judge = {
    model: judge.model,
    attempts: judge.attempts,
    result: judge.result,
  };
  check("judge_scored_both_models", Array.isArray(judge.result?.scores) && judge.result.scores.length >= 2, judge.result);

  const experiment = await createExperiment(prompt.id, dataset, datasetVersion, suites, { deepseek: deepseekRun, kimi: kimiRun }, judge);
  evidence.assets.experiment = pick(experiment, ["id", "name", "asset_type", "experiment_dimension_count"]);
  const localExperiment = await post(`/api/prompt-evaluation-assets/${experiment.id}/run`, null);
  evidence.assets.experiment_local_run_asset = pick(localExperiment, ["id", "name", "asset_type", "status"]);
  const optimization = await createOptimizationAsset(prompt.id, experiment, judge, { deepseek: deepseekRun, kimi: kimiRun });
  evidence.assets.optimization = pick(optimization, ["id", "name", "asset_type", "status"]);

  const experimentReadback = await get(`/api/prompt-evaluation-assets/${experiment.id}`);
  const readbackJudge = experimentReadback?.payload?.["GPT评审"];
  check("judge_result_readable_from_experiment_asset", Boolean(readbackJudge?.judge_model && readbackJudge?.scores?.length >= 2), {
    experiment_id: experiment.id,
    judge_model: readbackJudge?.judge_model,
    score_count: readbackJudge?.scores?.length || 0,
  });

  evidence.pages = {
    prompts: `${browserURL}/${workspace.slug}/training/prompts`,
    datasets: `${browserURL}/${workspace.slug}/training/datasets`,
    test_suites: `${browserURL}/${workspace.slug}/training/test-suites`,
    experiments: `${browserURL}/${workspace.slug}/training/experiments`,
    evaluation_runs: `${browserURL}/${workspace.slug}/training/evaluation-runs`,
    optimization_runs: `${browserURL}/${workspace.slug}/training/optimization-runs`,
  };
  evidence.ok = evidence.checks.every((item) => item.ok);
  evidence.status = evidence.ok ? "passed" : "failed";
  writeEvidence();
  console.log(JSON.stringify(evidence, null, 2));
  if (!evidence.ok) process.exitCode = 1;
} catch (error) {
  evidence.ok = false;
  evidence.status = "failed";
  evidence.error = error?.stack || error?.message || String(error);
  writeEvidence();
  console.error(JSON.stringify(evidence, null, 2));
  process.exitCode = 1;
}

async function resolveWorkspace() {
  const workspaces = await get("/api/workspaces");
  const items = Array.isArray(workspaces) ? workspaces : workspaces.items ?? [];
  const found = items.find((item) => item.slug === workspaceSlug);
  if (!found?.id) fail(`未找到工作区 ${workspaceSlug}`);
  return found;
}

async function resolveCodeBuddyRuntime() {
  const runtimes = await get(`/api/runtimes?workspace_id=${encodeURIComponent(workspace.id)}`);
  const now = Date.now();
  const candidates = (Array.isArray(runtimes) ? runtimes : runtimes.items ?? [])
    .filter((item) => String(item.provider).toLowerCase() === provider.toLowerCase())
    .filter((item) => item.status === "online")
    .sort((a, b) => Date.parse(b.last_seen_at || "") - Date.parse(a.last_seen_at || ""));
  const fresh = candidates.find((item) => now - Date.parse(item.last_seen_at || "") <= 180_000);
  if (!fresh) {
    fail(`未找到 3 分钟内在线的 ${provider} runtime`, { runtimes: candidates.map((item) => pick(item, ["id", "name", "status", "last_seen_at"])) });
  }
  return fresh;
}

async function resolveRuntimeModels(runtimeId) {
  const initial = await post(`/api/runtimes/${runtimeId}/models`, {});
  let current = initial;
  const started = Date.now();
  while (current.status === "pending" || current.status === "running") {
    if (Date.now() - started > 45_000) fail("模型列表发现超时", { request: initial });
    await sleep(750);
    current = await get(`/api/runtimes/${runtimeId}/models/${initial.id}`);
  }
  if (current.status !== "completed") {
    fail("模型列表发现失败", current);
  }
  if (current.supported === false) {
    fail("当前 runtime 不支持按 Agent 指定模型，无法完成 DeepSeek/Kimi 对比", current);
  }
  return Array.isArray(current.models) ? current.models : [];
}

function pickKimiModel(models) {
  const preferred = ["kimi-k2.7-ioa", "kimi-k2.6-ioa"];
  for (const id of preferred) {
    if (models.some((item) => item.id === id)) return id;
  }
  return models.find((item) => /kimi/i.test(`${item.id} ${item.label || ""}`))?.id || "";
}

async function createPrompt() {
  return post("/api/prompt-library", {
    name: `增强密码强度模型对比提示词 ${humanStamp}`,
    description: "用于比较不同模型对同一密码强度需求的理解、方案、测试和风险识别能力。",
    prompt_type: "模型对比评估",
    content: [
      "你正在处理一个真实产品需求，请基于输入输出中文结果。",
      "",
      "任务标题：{{issue_title}}",
      "需求正文：{{requirement}}",
      "本轮关注：{{focus}}",
      "",
      "请按以下结构输出：",
      "1. 需求理解",
      "2. 技术方案",
      "3. 边界条件",
      "4. 测试方案",
      "5. 风险和遗漏场景",
      "6. 验收证据",
      "",
      "要求：不要编造不存在的系统背景；遇到不确定处要显式说明假设。",
    ].join("\n"),
    variables: [
      { name: "issue_title", label: "任务标题", required: true },
      { name: "requirement", label: "需求正文", required: true },
      { name: "focus", label: "关注点", required: true },
    ],
    tags: ["密码强度", "模型对比", "客户演示"],
    status: "启用",
  });
}

async function createDataset(promptId) {
  return post("/api/prompt-evaluation-assets", {
    prompt_id: promptId,
    name: `增强密码强度评估数据集 ${humanStamp}`,
    description: "沉淀密码强度需求的标准规则、边界条件和遗漏场景，用于模型横向对比。",
    asset_type: "数据集",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      cases: taskCases,
      metric_contract: ["需求理解", "边界覆盖", "测试完整性", "风险识别", "输出结构"],
    },
    status: "启用",
  });
}

async function createExecutionAgent(runtime, label, model) {
  return post("/api/agents", {
    workspace_id: workspace.id,
    name: `密码强度评估-${label}-${humanStamp}`,
    description: `用于增强密码强度需求的模型对比运行，执行模型：${model}。`,
    instructions: [
      "你是训练与评估模型对比中的候选模型执行者。",
      "只处理输入中的需求和用例，不要读取或修改本地仓库。",
      "输出必须是中文，必须覆盖需求理解、技术方案、边界条件、测试方案、风险和验收证据。",
      "不要声称已经真实修改代码；这是方案与测试能力评估。",
    ].join("\n"),
    runtime_id: runtime.id,
    scope: "workspace",
    max_concurrent_tasks: 5,
    model,
    custom_args: [],
  });
}

async function createSuite(promptId, dataset, datasetVersion, agent, label) {
  return post("/api/prompt-evaluation-assets", {
    prompt_id: promptId,
    name: `增强密码强度模型对比套件 ${label} ${humanStamp}`,
    description: `使用同一密码强度数据集运行 ${label}，结果进入模型对比实验。`,
    asset_type: "测试套件",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      execution_agent_id: agent.id,
      执行智能体: { agent_id: agent.id, name: agent.name, model: agent.model },
      linked_dataset_ids: [dataset.id],
      linked_dataset_versions: [{
        dataset_id: dataset.id,
        dataset_name: dataset.name,
        dataset_version_id: datasetVersion.id,
        version: datasetVersion.version,
        row_fingerprint: datasetVersion.row_fingerprint,
      }],
      cases: taskCases,
      metric_contract: ["需求理解准确性", "边界条件覆盖", "可执行性", "测试方案完整性", "风险识别", "输出结构清晰度", "是否胡编或遗漏"],
    },
    status: "启用",
  });
}

async function runSuiteWithAgent(suite, label) {
  const queued = await post(`/api/prompt-evaluation-assets/${suite.id}/agent-run`, null);
  if (!queued?.run?.id || !queued?.task_id) fail(`${label} Agent 运行入队响应不完整`, queued);
  const terminal = await poll(async () => {
    await post(`/api/prompt-evaluation-runs/${queued.run.id}/sync`, null);
    const runs = await get(`/api/prompt-evaluation-runs?asset_id=${encodeURIComponent(suite.id)}&limit=20`);
    const items = Array.isArray(runs) ? runs : runs.items ?? [];
    const found = items.find((item) => item.id === queued.run.id);
    if (!found || found.status === "已入队" || found.status === "运行中") return null;
    return found;
  }, taskTimeoutMs, `等待 ${label} 运行完成`);
  const runEvidence = await get(`/api/prompt-evaluation-runs/${queued.run.id}/evidence`);
  const output = extractModelOutput(runEvidence);
  const usage = Array.isArray(runEvidence.task_usage) ? runEvidence.task_usage[0] : null;
  return {
    label,
    suite_id: suite.id,
    run_id: queued.run.id,
    task_id: queued.task_id,
    model: queued.model || terminal.model || "",
    runtime_id: queued.runtime_id,
    status: terminal.status,
    output,
    evidence: runEvidence,
    summary: {
      label,
      suite_id: suite.id,
      run_id: queued.run.id,
      task_id: queued.task_id,
      agent_id: queued.agent_id,
      runtime_id: queued.runtime_id,
      model: queued.model || terminal.model || "",
      provider,
      status: terminal.status,
      total_cases: terminal.total_cases,
      failed_cases: terminal.failed_cases,
      input_tokens: Number(usage?.input_tokens || terminal.input_tokens || 0),
      output_tokens: Number(usage?.output_tokens || terminal.output_tokens || 0),
      duration_ms: Number(terminal.total_duration_ms || 0),
      output_chars: output.length,
      failure_reason: terminal.failure_reason || "",
    },
  };
}

function extractModelOutput(runEvidence) {
  const messages = Array.isArray(runEvidence?.task_messages) ? runEvidence.task_messages : [];
  const textMessages = messages
    .filter((item) => item.type === "text" && typeof item.content === "string" && item.content.trim())
    .sort((a, b) => Number(a.seq || 0) - Number(b.seq || 0));
  const nonPrompt = textMessages.filter((item) => !item.content.includes("你正在处理一个真实产品需求"));
  const chosen = nonPrompt.at(-1) || textMessages.at(-1);
  if (chosen?.content) return chosen.content.trim();
  return JSON.stringify(runEvidence?.execution_summary || runEvidence || {});
}

function modelRunHasAuditableOutput(run) {
  return Boolean(
    run?.summary?.run_id &&
    run?.summary?.task_id &&
    run?.summary?.model &&
    run?.summary?.status &&
    run.summary.status !== "失败" &&
    run.output.trim().length > 0,
  );
}

async function runCodexJudge(outputs) {
  const attempts = [];
  const prompt = buildJudgePrompt(outputs);
  for (const model of judgeModelPreference) {
    const outPath = path.join(artifactRoot, `judge-${model.replace(/[^a-zA-Z0-9_.-]/g, "_")}-${stamp}.json`);
    try {
      execFileSync("codex", [
        "exec",
        "--dangerously-bypass-approvals-and-sandbox",
        "--model",
        model,
        "--cd",
        repoRoot,
        "--output-last-message",
        outPath,
        prompt,
      ], { encoding: "utf8", stdio: ["ignore", "pipe", "pipe"], timeout: 420_000, maxBuffer: 10 * 1024 * 1024 });
      const raw = readFileSync(outPath, "utf8").trim();
      const parsed = parseJudgeJSON(raw);
      attempts.push({ model, status: "completed", output_path: outPath });
      parsed.judge_model = parsed.judge_model || model;
      return { model, attempts, result: parsed };
    } catch (error) {
      attempts.push({ model, status: "failed", error: String(error?.message || error).slice(0, 500) });
    }
  }
  fail("Codex/GPT judge 调用失败，不能伪造模型评分", { attempts });
}

function buildJudgePrompt(outputs) {
  return [
    "你是训练评估 judge。只输出 JSON，不要 Markdown，不要解释。",
    "请比较 DeepSeek V4 Pro 与 Kimi 在同一增强密码强度需求上的输出质量。",
    "评分维度固定为：需求理解准确性、边界条件覆盖、可执行性、测试方案完整性、风险识别、输出结构清晰度、是否胡编或遗漏。",
    "每个维度 0-100 分，总分 0-100 分。",
    "JSON schema:",
    '{"judge_model":"string","scores":[{"model":"string","total_score":0,"dimensions":{"需求理解准确性":0,"边界条件覆盖":0,"可执行性":0,"测试方案完整性":0,"风险识别":0,"输出结构清晰度":0,"是否胡编或遗漏":0},"strengths":["string"],"weaknesses":["string"],"recommendations":["string"]}],"conclusion":{"winner":"string","summary":"string","recommendation":"string"}}',
    "",
    "评测任务：增强密码强度。密码 8-32 位，至少包含大写字母、小写字母、数字、特殊字符四类中的三类；特殊字符范围为 !@#$%^&*()_+|~=`{}[]:\";'<>?,./。",
    "",
    "DeepSeek V4 Pro 输出：",
    outputs.deepseek.output,
    "",
    "Kimi 输出：",
    outputs.kimi.output,
  ].join("\n");
}

function parseJudgeJSON(raw) {
  try {
    return JSON.parse(raw);
  } catch {
    const start = raw.indexOf("{");
    const end = raw.lastIndexOf("}");
    if (start >= 0 && end > start) return JSON.parse(raw.slice(start, end + 1));
    throw new Error("judge output is not JSON");
  }
}

async function createExperiment(promptId, dataset, datasetVersion, suites, runs, judge) {
  const scores = judge.result.scores || [];
  return post("/api/prompt-evaluation-assets", {
    prompt_id: promptId,
    name: `增强密码强度模型对比实验 ${humanStamp}`,
    description: "同一密码强度需求下对比 DeepSeek V4 Pro 与 Kimi，并由 GPT judge 给出结构化评分。",
    asset_type: "实验",
    payload: {
      schema: "multica.training_evaluation.model_comparison.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.model_comparison.v1",
      实验对象: "增强密码强度",
      对比维度: ["需求理解准确性", "边界条件覆盖", "可执行性", "测试方案完整性", "风险识别", "输出结构清晰度", "是否胡编或遗漏"],
      数据集: { id: dataset.id, name: dataset.name, version_id: datasetVersion.id, version: datasetVersion.version },
      测试套件: {
        deepseek: { id: suites.deepseek.id, name: suites.deepseek.name },
        kimi: { id: suites.kimi.id, name: suites.kimi.name },
      },
      模型运行: {
        deepseek: runs.deepseek.summary,
        kimi: runs.kimi.summary,
      },
      模型输出: {
        deepseek: {
          model: runs.deepseek.summary.model,
          run_id: runs.deepseek.run_id,
          task_id: runs.deepseek.task_id,
          output: runs.deepseek.output,
        },
        kimi: {
          model: runs.kimi.summary.model,
          run_id: runs.kimi.run_id,
          task_id: runs.kimi.task_id,
          output: runs.kimi.output,
        },
      },
      GPT评审: {
        judge_model: judge.model,
        generated_at: new Date().toISOString(),
        scores,
        conclusion: judge.result.conclusion || {},
      },
      对比结论: judge.result.conclusion?.summary || "",
      后续优化建议: judge.result.conclusion?.recommendation || "",
      cases: taskCases,
    },
    status: "启用",
  });
}

async function createOptimizationAsset(promptId, experiment, judge, runs) {
  return post("/api/prompt-evaluation-assets", {
    prompt_id: promptId,
    name: `增强密码强度模型优化建议 ${humanStamp}`,
    description: "基于 DeepSeek 与 Kimi 对比评分沉淀后续提示词和评估改进建议。",
    asset_type: "优化运行",
    payload: {
      schema: "multica.training_evaluation.model_comparison_optimization.v1",
      schema_version: 1,
      来源实验: experiment.id,
      来源运行: [runs.deepseek.run_id, runs.kimi.run_id],
      GPT评审: judge.result,
      优化目标: ["补齐边界条件", "强化测试证据", "减少未说明假设", "保留风险识别"],
      后续动作: judge.result.conclusion?.recommendation || "根据低分维度补充提示词约束后复测。",
    },
    status: "启用",
  });
}

async function get(pathname) {
  return request("GET", pathname);
}

async function post(pathname, body, auth = true, workspaceHeader = true) {
  return request("POST", pathname, body, auth, workspaceHeader);
}

async function request(method, pathname, body, auth = true, workspaceHeader = true) {
  const headers = { "content-type": "application/json" };
  if (auth && token) headers.authorization = `Bearer ${token}`;
  if (workspaceHeader && workspace?.id) headers["x-workspace-id"] = workspace.id;
  const response = await fetch(`${apiURL}${pathname}`, {
    method,
    headers,
    body: body === null || body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await response.text();
  if (!response.ok) {
    fail(`${method} ${pathname} 返回 ${response.status}`, { body: text.slice(0, 2000) });
  }
  return text.trim() ? JSON.parse(text) : null;
}

async function poll(fn, timeoutMs, label) {
  const started = Date.now();
  let last = null;
  while (Date.now() - started < timeoutMs) {
    last = await fn();
    if (last) return last;
    await sleep(pollIntervalMs);
  }
  fail(`${label}超时`, { last });
}

function check(name, ok, details = {}) {
  evidence.checks.push({ name, ok: Boolean(ok), details });
}

function fail(message, details = {}) {
  evidence.error = message;
  if (Object.keys(details).length > 0) evidence.error_details = details;
  evidence.ok = false;
  evidence.status = "failed";
  writeEvidence();
  throw new Error(message);
}

function writeEvidence() {
  mkdirSync(artifactRoot, { recursive: true });
  writeFileSync(evidencePath, JSON.stringify(evidence, null, 2));
  writeFileSync(latestEvidencePath, JSON.stringify(evidence, null, 2));
}

function pick(value, keys) {
  const out = {};
  for (const key of keys) out[key] = value?.[key] ?? "";
  return out;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function trimEnv(name) {
  return (process.env[name] || "").trim();
}

function trimSlash(value) {
  return String(value || "").replace(/\/$/, "");
}

function readRunEnv(envName) {
  const file = path.join(repoRoot, ".run", "env", `goal-test-${envName}.env`);
  try {
    return Object.fromEntries(readFileSync(file, "utf8")
      .split(/\r?\n/)
      .map((line) => line.trim())
      .filter((line) => line && !line.startsWith("#") && line.includes("="))
      .map((line) => {
        const index = line.indexOf("=");
        return [line.slice(0, index), line.slice(index + 1)];
      }));
  } catch {
    return {};
  }
}
