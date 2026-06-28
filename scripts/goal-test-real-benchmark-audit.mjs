#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const strict = process.argv.includes("--strict");

const paths = {
  goalTest: repoRoot,
  ledger: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计-v4/01-主决策账本.md",
  stream: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计-v4/02-决策流水.jsonl",
  v1Pilot: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计v1/35-real-demand-pilot-and-deployment-permission-benchmark-20260616.md",
  v1Controller: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计v1/07-第一版执行总控.md",
  sopAgent: "/data/ida/sopAgent",
  usercenter: "/data/ida/user-center",
  gateway: "/data/ida/gateway",
  deployment: "/data/ida/ida-deployment",
  evoSkill: "/data/ida/EvoSkill",
};

const benchmarkCases = [
  {
    id: "quick-entry-capability",
    suite: "current-smoke",
    repo_scope: ["user-center", "gateway", "ida-deployment"],
    source_kind: "v4 current engineered sentinel",
    source_paths: [
      "scripts/verify-quick-entry-cross-service.mjs",
      "artifacts/acceptance/quick-entry-cross-service-latest.json",
    ],
    expected_public_entry: "GET /v1/user-center/quick-entry-capability",
    required_sandbox: "gateway HTTP -> usercenter gRPC -> ida-deployment permission/apiData/render evidence",
    cannot_prove_with: ["httptest-only artifact", "static permission grep", "unit tests only"],
  },
  {
    id: "link-user-space-org",
    suite: "historical-replay",
    repo_scope: ["user-center", "gateway", "ida-deployment"],
    source_kind: "v1 historical real demand benchmark",
    source_paths: [
      "/data/ida/sopAgent/benchmarks/user-center/add-api/link-user-space-org/task.yaml",
      "/data/ida/sopAgent/benchmarks/user-center/add-api/link-user-space-org/input.md",
      "/data/ida/sopAgent/benchmarks/user-center/add-api/link-user-space-org/expected.md",
    ],
    expected_public_entry: "LinkUserSpaceOrg plus gateway/deployment boundary evidence after scoped replay",
    required_sandbox: "service-level usercenter/gateway/deployment validation with endpoint or equivalent public API curl",
    cannot_prove_with: ["old B3 eval only", "user-center-only unit test", "TAPD excerpt without replay"],
  },
  {
    id: "product-api-permission",
    suite: "historical-replay",
    repo_scope: ["ida-deployment", "gateway"],
    source_kind: "v1 historical permission-chain sample",
    source_paths: [paths.v1Pilot],
    expected_public_entry: "Product example API permission propagated through generated gateway apiData",
    required_sandbox: "generate_permissions.sh all, helm render, gateway permission/apiData curl or render evidence",
    cannot_prove_with: ["manual generated file edit", "product.api.json edit without generate", "mode policy unexplained"],
  },
  {
    id: "announcement-notice",
    suite: "historical-replay",
    repo_scope: ["user-center", "gateway", "ida-deployment"],
    source_kind: "v1 historical real demand pilot",
    source_paths: [paths.v1Pilot],
    expected_public_entry: "announcement save/list/detail API through usercenter and deployment permission exposure",
    required_sandbox: "service-level create/list/detail curl and permission/apiData/render evidence",
    cannot_prove_with: ["logic test only", "old commit existence", "unisolated tenant data"],
  },
  {
    id: "notice-same-second-cursor",
    suite: "historical-replay",
    repo_scope: ["user-center", "gateway", "ida-deployment"],
    source_kind: "v1 historical boundary bug",
    source_paths: [paths.v1Pilot],
    expected_public_entry: "SyncNotice same-second cursor returns stable non-duplicated tenant-scoped results",
    required_sandbox: "service-level sync curl with controlled same-second notices and permission/apiData evidence",
    cannot_prove_with: ["flaky wall-clock test", "DAO-only test", "single-tenant happy path"],
  },
];

mkdirSync(artifactRoot, { recursive: true });

const checks = [];
const audit = {
  schema: "multica.goal_test.real_benchmark_audit.v1",
  generated_at: generatedAt,
  strict,
  paths,
  repo_state: {
    goal_test: repoState(paths.goalTest),
    usercenter: repoState(paths.usercenter),
    gateway: repoState(paths.gateway),
    ida_deployment: repoState(paths.deployment),
    sop_agent: repoState(paths.sopAgent),
    evo_skill: repoState(paths.evoSkill),
  },
  harness_audit: {
    usercenter: harnessAudit(paths.usercenter),
    gateway: harnessAudit(paths.gateway),
    ida_deployment: harnessAudit(paths.deployment),
  },
  decisions: decisionEvidence(),
  benchmark_cases: benchmarkCases.map(auditCase),
  existing_artifacts: existingArtifacts(),
  training_loop: trainingLoopEvidence(),
  p0_matrix: [],
  production_gap_matrix: [],
};

audit.p0_matrix = buildP0Matrix(audit);
audit.production_gap_matrix = buildGapMatrix(audit);
audit.ok = audit.p0_matrix.every((item) => item.status === "fulfilled" || item.status === "out_of_scope_by_user")
  && audit.production_gap_matrix.every((item) => !item.blocking || item.status === "fulfilled" || item.status === "out_of_scope_by_user");

const jsonPath = path.join(artifactRoot, `real-benchmark-audit-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `real-benchmark-audit-${stamp}.md`);
const latestJsonPath = path.join(artifactRoot, "real-benchmark-audit-latest.json");
const latestMarkdownPath = path.join(artifactRoot, "real-benchmark-audit-latest.md");

writeFileSync(jsonPath, `${JSON.stringify(audit, null, 2)}\n`);
writeFileSync(markdownPath, renderMarkdown(audit));
writeFileSync(latestJsonPath, `${JSON.stringify(audit, null, 2)}\n`);
writeFileSync(latestMarkdownPath, renderMarkdown(audit));

console.log(JSON.stringify({
  ok: audit.ok,
  json: jsonPath,
  markdown: markdownPath,
  latest_json: latestJsonPath,
  latest_markdown: latestMarkdownPath,
  p0: summarizeStatuses(audit.p0_matrix),
  gaps: summarizeStatuses(audit.production_gap_matrix),
}, null, 2));

if (strict && !audit.ok) process.exitCode = 1;

function auditCase(item) {
  const sourceChecks = item.source_paths.map((sourcePath) => {
    const resolved = path.isAbsolute(sourcePath) ? sourcePath : path.join(repoRoot, sourcePath);
    return {
      path: sourcePath,
      exists: existsSync(resolved),
      resolved,
    };
  });
  const sourceReady = sourceChecks.every((check) => check.exists);
  return {
    ...item,
    source_checks: sourceChecks,
    source_status: sourceReady ? "fulfilled" : "missing",
    sandbox_status: "missing",
    trace_eval_status: "missing",
    operation_skill_status: "missing",
    current_verdict: sourceReady ? "source_ready_but_not_replayed" : "source_missing",
  };
}

function buildP0Matrix(data) {
  const allSourcesReady = data.benchmark_cases.every((item) => item.source_status === "fulfilled");
  const serviceSandboxReady = data.existing_artifacts.quick_entry?.ok === true
    && [
      "service-container",
      "service-level-container",
      "service-process",
    ].includes(data.existing_artifacts.quick_entry?.sandbox_mode);
  const historicalServiceSandboxReady = data.existing_artifacts.historical_service_sandbox?.ok === true
    && data.existing_artifacts.historical_service_sandbox?.sandbox_mode === "service-process";
  const operationIntentReady = data.training_loop.has_operation_skill_intent_fields;
  return [
    {
      id: "P0-1",
      requirement: "五条 benchmark 全部具备真实来源、case contract、sandbox run、curl verifier、trace、eval verdict、evidence 和复跑命令。",
      status: allSourcesReady ? "partial" : "missing",
      evidence: {
        sources: data.benchmark_cases.map((item) => ({ id: item.id, source_status: item.source_status, current_verdict: item.current_verdict })),
        historical_readiness_summary: data.existing_artifacts.historical_readiness?.summary || null,
        historical_readiness_cases: data.existing_artifacts.historical_readiness?.cases?.map((item) => ({
          id: item.id,
          status: item.status,
          missing: item.missing,
        })) || null,
        historical_service_sandbox_cases: data.existing_artifacts.historical_service_sandbox?.cases?.map((item) => ({
          id: item.id,
          status: item.status,
          ok: item.ok,
        })) || null,
      },
      missing: serviceSandboxReady && historicalServiceSandboxReady
        ? ["五条尚未全部形成 trace/eval verdict 和训练闭环复跑命令"]
        : ["五条尚未全部形成服务级 sandbox run、curl verifier、trace/eval verdict 和复跑命令"],
    },
    {
      id: "P0-2",
      requirement: "服务级 sandbox 必须真实启动相关服务，curl 从 gateway HTTP 进入并触达 usercenter gRPC 与 ida-deployment 权限/apiData/render。",
      status: serviceSandboxReady && historicalServiceSandboxReady ? "fulfilled" : (serviceSandboxReady || historicalServiceSandboxReady ? "partial" : "missing"),
      evidence: {
        quick_entry: data.existing_artifacts.quick_entry || null,
        historical_service_sandbox: data.existing_artifacts.historical_service_sandbox || null,
      },
      missing: serviceSandboxReady && historicalServiceSandboxReady
        ? []
        : [
            ...(serviceSandboxReady ? [] : ["当前 quick-entry latest 仍不是 service-level container/process 口径"]),
            ...(historicalServiceSandboxReady ? [] : ["历史四条尚无服务级 sandbox artifact"]),
          ],
    },
    {
      id: "P0-3",
      requirement: "运行复盘到训练闭环：trace -> eval -> optimizer -> validation rerun -> apply/MR -> CHANGELOG -> re-eval。",
      status: data.training_loop.has_skill_patch_api && data.training_loop.has_re_eval_api ? "partial" : "missing",
      evidence: data.training_loop,
      missing: ["尚未证明五条 benchmark trace 进入 eval 并驱动 optimizer/re-eval"],
    },
    {
      id: "P0-4",
      requirement: "operation skill 机制支持 update_existing_skill 与 create_operation_skill，并写回 active skill/harness 前完成评审、snapshot、benchmark/eval、freshness、CHANGELOG、re-eval。",
      status: operationIntentReady ? "partial" : "missing",
      evidence: data.training_loop.operation_intent_scan,
      missing: operationIntentReady ? ["尚未用五条 benchmark 证明"] : ["缺少一等 update_existing_skill/create_operation_skill 意图字段或等价 schema"],
    },
    {
      id: "P0-5",
      requirement: "纪律化硬磨成功，失败分类、最小验证、连续同签名失败审计、blocked 边界可追溯。",
      status: data.decisions.has_l325 ? "partial" : "missing",
      evidence: { ledger_l325: data.decisions.has_l325 },
      missing: ["执行脚本尚未记录 failure_signature retry discipline"],
    },
    {
      id: "P0-6",
      requirement: "最终可接手：账本、evidence package、final gap audit、commit、复跑说明完整。",
      status: "missing",
      evidence: null,
      missing: ["本 goal 刚启动，尚未 final"],
    },
  ];
}

function buildGapMatrix(data) {
  const quickEntryServiceReady = data.existing_artifacts.quick_entry?.ok === true
    && ["service-container", "service-level-container", "service-process"].includes(data.existing_artifacts.quick_entry?.sandbox_mode);
  const historicalServiceReady = data.existing_artifacts.historical_service_sandbox?.ok === true
    && data.existing_artifacts.historical_service_sandbox?.sandbox_mode === "service-process";
  return [
    {
      id: "GAP-1",
      gap: "datasets/cases 为空或不可复跑",
      blocking: true,
      status: data.benchmark_cases.every((item) => item.source_status === "fulfilled") ? "partial" : "missing",
      verification: "real-benchmark-audit, historical-benchmark-readiness, plus future active eval cases",
    },
    {
      id: "GAP-2",
      gap: "sandbox 不是服务级，curl 未穿过 gateway -> usercenter -> ida-deployment",
      blocking: true,
      status: quickEntryServiceReady && historicalServiceReady ? "fulfilled" : (quickEntryServiceReady || historicalServiceReady ? "partial" : "missing"),
      verification: "quick-entry-cross-service-latest.json and historical-service-sandbox-latest.json",
    },
    {
      id: "GAP-3",
      gap: "optimizer 无法产出结构化 skill_patch 或无法写回后 re-eval",
      blocking: true,
      status: data.training_loop.has_skill_patch_api && data.training_loop.has_re_eval_api ? "partial" : "missing",
      verification: "skill-candidate workflow APIs and five-case run evidence",
    },
    {
      id: "GAP-4",
      gap: "operation skill 不能新建",
      blocking: true,
      status: data.training_loop.has_operation_skill_intent_fields ? "partial" : "missing",
      verification: "schema/UI/API supports create_operation_skill",
    },
    {
      id: "GAP-5",
      gap: "live 集群 curl、客户生产发布、正式回滚、正式权限运维、模型权重微调、自动 merge MR",
      blocking: false,
      status: "out_of_scope_by_user",
      verification: "ledger L323-L325",
    },
  ];
}

function decisionEvidence() {
  const ledgerText = safeRead(paths.ledger);
  const streamText = safeRead(paths.stream);
  return {
    ledger_exists: existsSync(paths.ledger),
    stream_exists: existsSync(paths.stream),
    has_l320: streamText.includes('"id":"L320"'),
    has_l321: streamText.includes('"id":"L321"'),
    has_l322: streamText.includes('"id":"L322"'),
    has_l323: streamText.includes('"id":"L323"'),
    has_l324: streamText.includes('"id":"L324"'),
    has_l325: streamText.includes('"id":"L325"'),
    sandbox_only_recorded: ledgerText.includes("服务级 sandbox") && ledgerText.includes("live 集群验证"),
  };
}

function existingArtifacts() {
  const quickEntryPath = path.join(artifactRoot, "quick-entry-cross-service-latest.json");
  const historicalReadinessPath = path.join(artifactRoot, "historical-benchmark-readiness-latest.json");
  const historicalServiceSandboxPath = path.join(artifactRoot, "historical-service-sandbox-latest.json");
  const promptCurlPath = path.join(artifactRoot, "prompt-evaluation-curl-e2e-latest.json");
  const optimizerPath = path.join(artifactRoot, "optimizer-workbench-latest.json");
  const gapPath = path.join(artifactRoot, "tapd-gongfeng-sop-gap-audit-latest.json");
  return {
    quick_entry: readJSONArtifact(quickEntryPath),
    historical_readiness: readJSONArtifact(historicalReadinessPath),
    historical_service_sandbox: readJSONArtifact(historicalServiceSandboxPath),
    prompt_evaluation_curl: readJSONArtifact(promptCurlPath),
    optimizer_workbench: readJSONArtifact(optimizerPath),
    final_gap_audit: readJSONArtifact(gapPath),
  };
}

function trainingLoopEvidence() {
  const files = [
    "packages/core/types/prompt-evaluation.ts",
    "packages/core/api/schemas.ts",
    "packages/core/api/client.ts",
    "packages/views/prompt-library/components/prompt-library-page.tsx",
    "server/internal/handler/prompt_evaluation_skill.go",
    "server/internal/handler/prompt_evaluation_asset.go",
    "e2e/skill-candidate-workflow.spec.ts",
    "e2e/optimizer-workbench.spec.ts",
    "e2e/run-reviews.spec.ts",
  ];
  const haystack = files.map((file) => safeRead(path.join(repoRoot, file))).join("\n");
  const operationIntentScan = {
    update_existing_skill: haystack.includes("update_existing_skill"),
    create_operation_skill: haystack.includes("create_operation_skill"),
  };
  return {
    inspected_files: files,
    has_skill_patch_api: haystack.includes("skill_patch") && haystack.includes("PromptEvaluationSkillPatch"),
    has_freshness_api: haystack.includes("skill-freshness") || haystack.includes("CheckPromptEvaluationSkillCandidateFreshness"),
    has_apply_api: haystack.includes("ApplyPromptEvaluationSkillCandidate") || haystack.includes("Apply + CHANGELOG"),
    has_re_eval_api: haystack.includes("skill-re-eval-run") || haystack.includes("PreparePromptEvaluationSkillReEval"),
    has_run_review_eval_entry: haystack.includes("生成评测 Draft") || haystack.includes("加入评测"),
    has_operation_skill_intent_fields: operationIntentScan.update_existing_skill && operationIntentScan.create_operation_skill,
    operation_intent_scan: operationIntentScan,
  };
}

function harnessAudit(repoPath) {
  const scriptPath = "/data/ida/docs/skill/custom/project-harness-builder/scripts/audit_project_harness.py";
  if (!existsSync(scriptPath)) return { ok: false, error: `missing ${scriptPath}` };
  try {
    return JSON.parse(execFileSync("python3", [scriptPath, repoPath], { encoding: "utf8", maxBuffer: 10 * 1024 * 1024 }));
  } catch (error) {
    return {
      ok: false,
      error: error instanceof Error ? error.message : String(error),
      stdout: String(error.stdout || ""),
      stderr: String(error.stderr || ""),
    };
  }
}

function repoState(repoPath) {
  if (!existsSync(repoPath)) return { path: repoPath, exists: false };
  return {
    path: repoPath,
    exists: true,
    branch: safeExec(repoPath, ["git", "branch", "--show-current"]).trim(),
    commit: safeExec(repoPath, ["git", "rev-parse", "HEAD"]).trim(),
    dirty: safeExec(repoPath, ["git", "status", "--short"]).trim() !== "",
  };
}

function readJSONArtifact(filePath) {
  if (!existsSync(filePath)) return { path: filePath, exists: false };
  try {
    return { path: filePath, exists: true, ...JSON.parse(readFileSync(filePath, "utf8")) };
  } catch (error) {
    return { path: filePath, exists: true, parse_error: error instanceof Error ? error.message : String(error) };
  }
}

function summarizeStatuses(items) {
  return items.reduce((acc, item) => {
    const status = item.status || "unknown";
    acc[status] = (acc[status] || 0) + 1;
    return acc;
  }, {});
}

function safeRead(filePath) {
  try {
    return readFileSync(filePath, "utf8");
  } catch {
    return "";
  }
}

function safeExec(cwd, command) {
  try {
    return execFileSync(command[0], command.slice(1), { cwd, encoding: "utf8", maxBuffer: 1024 * 1024 });
  } catch {
    return "";
  }
}

function renderMarkdown(data) {
  const lines = [];
  lines.push("# Real Benchmark Audit");
  lines.push("");
  lines.push(`Generated: ${data.generated_at}`);
  lines.push(`Overall ok: ${data.ok ? "yes" : "no"}`);
  lines.push("");
  lines.push("## Repository State");
  for (const [name, state] of Object.entries(data.repo_state)) {
    lines.push(`- ${name}: ${state.exists ? `${state.branch || "<no-branch>"} @ ${state.commit || "<unknown>"} dirty=${state.dirty}` : "missing"}`);
  }
  lines.push("");
  lines.push("## Benchmark Cases");
  for (const item of data.benchmark_cases) {
    lines.push(`- ${item.id}: ${item.source_status}; ${item.current_verdict}`);
    for (const source of item.source_checks) {
      lines.push(`  - ${source.exists ? "found" : "missing"} ${source.path}`);
    }
  }
  lines.push("");
  lines.push("## P0 Matrix");
  for (const item of data.p0_matrix) {
    lines.push(`- ${item.id}: ${item.status} - ${item.requirement}`);
    for (const missing of item.missing || []) lines.push(`  - missing: ${missing}`);
  }
  lines.push("");
  lines.push("## Production Gap Matrix");
  for (const item of data.production_gap_matrix) {
    lines.push(`- ${item.id}: ${item.status}; blocking=${item.blocking} - ${item.gap}`);
  }
  lines.push("");
  return `${lines.join("\n")}\n`;
}
