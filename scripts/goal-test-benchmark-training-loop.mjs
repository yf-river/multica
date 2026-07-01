#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("GOAL_TEST_INT_API_URL") || "http://127.0.0.1:18762";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "develop";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "develop123";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "ai-studio";
const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const suffix = Date.now();
const repos = {
  usercenter: "/data/ida/user-center",
  gateway: "/data/ida/gateway",
  deployment: "/data/ida/ida-deployment",
};
const operationSkillPath = ".codebuddy/skills/historical-benchmark-replay/SKILL.md";
const operationChangelogPath = ".codebuddy/skills/historical-benchmark-replay/CHANGELOG.md";

fs.mkdirSync(artifactRoot, { recursive: true });

const report = {
  schema: "multica.goal_test.benchmark_training_loop.v1",
  generated_at: generatedAt,
  api_url: apiURL,
  workspace_slug: workspaceSlug,
  repos: Object.fromEntries(Object.entries(repos).map(([key, repo]) => [key, repoState(repo)])),
  source_artifacts: {
    quick_entry: path.join(artifactRoot, "quick-entry-cross-service-latest.json"),
    historical_service_sandbox: path.join(artifactRoot, "historical-service-sandbox-latest.json"),
  },
  commands: [],
  ok: false,
};

let token = "";
let activeWorkspaceId = "";

try {
  const quickEntry = readJSON(report.source_artifacts.quick_entry);
  const historical = readJSON(report.source_artifacts.historical_service_sandbox);
  if (quickEntry.ok !== true) fail("quick-entry service sandbox artifact is not ok");
  if (historical.ok !== true) fail("historical service sandbox artifact is not ok");

  const login = post("/auth/login", { account, password }, null);
  token = login.token;
  if (!token) fail("login response missing token");
  const workspace = resolveWorkspace(token);
  activeWorkspaceId = workspace.id;
  report.workspace = { id: workspace.id, slug: workspace.slug };

  const prompt = post("/api/prompt-library", {
    name: `Benchmark skill training prompt ${suffix}`,
    description: "Turns benchmark trace/evidence into skill training cases and operation skill candidates.",
    prompt_type: "需求澄清",
    content: [
      "{{expected_behavior}}",
      "{{verification}}",
      "{{evidence_source}}",
      "{{skill_path}}",
      "{{source_commit}}",
    ].join("\n"),
    variables: [
      { name: "expected_behavior", label: "Expected behavior", required: false },
      { name: "verification", label: "Verification", required: false },
      { name: "evidence_source", label: "Evidence source", required: false },
      { name: "skill_path", label: "Skill path", required: false },
      { name: "source_commit", label: "Source commit", required: false },
    ],
    tags: ["real-benchmark", "skill-training", "operation-skill"],
    status: "启用",
  }, token);
  if (!prompt?.id) fail("failed to create prompt");

  const benchmarkCases = buildBenchmarkCases(quickEntry, historical);
  const skillCaseDrafts = buildSkillCaseDrafts(benchmarkCases);
  const asset = post("/api/prompt-evaluation-assets", {
    prompt_id: prompt.id,
    name: `Real benchmark skill training loop ${suffix}`,
    description: "Five benchmark cases linked to service sandbox evidence; one failing assertion intentionally drives optimizer/proposer flow.",
    asset_type: "测试套件",
    payload: {
      schema: "multica.training_evaluation.payload.v1",
      schema_version: 1,
      语义版本: "multica.training_evaluation.v1",
      benchmark_contract: "multica.goal_test.real_benchmark.training_loop.v1",
      source_artifacts: report.source_artifacts,
      cases: benchmarkCases.map((item, index) => ({
        case_name: item.case_name,
        variables: item.variables,
        expected_contains: index === 0
          ? [...item.expected_contains, "__missing_operation_skill_candidate_marker__"]
          : item.expected_contains,
        tags: item.tags,
      })),
      skill_case_drafts: skillCaseDrafts,
      metric_contract: ["benchmark_case_count", "trace/evidence", "eval verdict", "optimizer candidate", "operation skill intent", "re-eval"],
    },
    status: "启用",
  }, token);
  if (!asset?.id) fail("failed to create benchmark evaluation asset");

  post(`/api/prompt-evaluation-assets/${asset.id}/run`, null, token);
  const failedRun = pollLocalRun(asset.id, "未通过", token);
  const runEvidence = get(`/api/prompt-evaluation-runs/${failedRun.id}/evidence`, token);
  if (!Array.isArray(runEvidence.trials) || runEvidence.trials.length !== benchmarkCases.length) {
    fail(`benchmark eval run trial count mismatch: ${JSON.stringify(runEvidence)}`);
  }
  const candidate = post(`/api/prompt-evaluation-runs/${failedRun.id}/optimization-candidates`, null, token);
  if (!candidate?.id) fail("failed to create optimization candidate");

  const project = post("/api/projects", {
    title: `Benchmark operation skill ${suffix}`,
    description: "Temporary project resource used to verify benchmark-derived operation skill writeback.",
    status: "in_progress",
  }, token);
  const resource = post(`/api/projects/${project.id}/resources`, {
    resource_type: "local_directory",
    label: `user-center local checkout ${suffix}`,
    resource_ref: {
      local_path: repos.usercenter,
      daemon_id: `goal-test-benchmark-training-${suffix}`,
    },
  }, token);
  const inventory = post(`/api/prompt-evaluation-assets/${asset.id}/skill-inventory`, {
    source_resource_id: resource.id,
    skill_root: ".codebuddy/skills",
  }, token);

  const mode = fs.existsSync(path.join(repos.usercenter, operationSkillPath)) ? "update_existing_skill" : "create_operation_skill";
  const snapshot = mode === "update_existing_skill"
    ? post(`/api/prompt-evaluation-assets/${asset.id}/skill-snapshot`, {
        source_resource_id: resource.id,
        skill_path: operationSkillPath,
      }, token).snapshot
    : syntheticCreateOperationSnapshot(repos.usercenter, resource.id);
  const candidatePatch = mode === "create_operation_skill"
    ? createOperationSkillPatch(repos.usercenter, operationSkillPath, renderOperationSkill())
    : updateOperationSkillPatch(repos.usercenter, operationSkillPath, `- Benchmark training loop revalidated at ${generatedAt}; evidence: historical-service-sandbox-latest.json and real-benchmark-audit-latest.json.\n`);

  const updatedCandidate = put(`/api/prompt-evaluation-optimization-candidates/${candidate.id}`, {
    candidate_name: `${mode === "create_operation_skill" ? "Create" : "Update"} historical benchmark replay operation skill`,
    candidate_content: "Benchmark-derived operation skill patch is stored in skill_patch.patch.",
    rationale: "Five service-level benchmark cases show historical replay is a repeated, high-risk user-center operation that should be captured as an operation skill.",
    edit_note: "Generated by goal-test benchmark training loop runner after service sandbox evidence passed.",
    skill_patch: {
      patch: candidatePatch,
      candidate_intent: mode,
      operation_skill_key: "user-center/historical-benchmark-replay",
      operation_skill_path: operationSkillPath,
      operation_skill_reason: "Historical service sandbox replay is now a repeated user-center verification operation.",
      source_snapshot: snapshot,
      source_resource_id: resource.id,
      repo_path: repos.usercenter,
      target_branch: snapshot.branch || "HEAD",
      skill_path: operationSkillPath,
      changelog_path: operationChangelogPath,
      expected_improvement: "Preserve the real benchmark replay procedure as a reusable user-center operation skill.",
      risk: "Scoped skill documentation change under .codebuddy/skills; no production code path is modified.",
      verification_plan: "Run freshness, apply, CHANGELOG write, prepare re-eval, run re-eval, then rerun service sandbox artifacts.",
      publication_status: "controlled_writeback",
    },
  }, token);
  if (updatedCandidate.skill_patch?.candidate_intent !== mode) {
    fail(`candidate intent not preserved: ${JSON.stringify(updatedCandidate.skill_patch)}`);
  }

  const freshness = post(`/api/prompt-evaluation-optimization-candidates/${candidate.id}/skill-freshness`, {
    source_resource_id: resource.id,
    repo_path: repos.usercenter,
    candidate_intent: mode,
  }, token);
  if (!["fresh", "branch_changed_skill_unchanged", "rebaseable"].includes(freshness.status)) {
    fail(`freshness blocked apply: ${JSON.stringify(freshness)}`);
  }

  const apply = post(`/api/prompt-evaluation-optimization-candidates/${candidate.id}/skill-apply`, {
    source_resource_id: resource.id,
    repo_path: repos.usercenter,
    candidate_intent: mode,
    rollback_plan: "Revert the benchmark-derived operation skill patch and generated CHANGELOG entry.",
  }, token);
  if (apply.apply?.status !== "applied") fail(`skill apply did not apply: ${JSON.stringify(apply)}`);
  const changedText = (apply.apply.changed_files || []).join("\n");
  if (!changedText.includes(operationSkillPath) && !changedText.includes(path.posix.dirname(operationSkillPath))) {
    fail("apply did not change operation skill path");
  }
  if (!changedText.includes(operationChangelogPath) && !changedText.includes(path.posix.dirname(operationChangelogPath))) {
    fail("apply did not write operation skill changelog");
  }

  const reEvalAsset = post(`/api/prompt-evaluation-optimization-candidates/${candidate.id}/skill-re-eval-asset`, {
    source_resource_id: resource.id,
    repo_path: repos.usercenter,
    snapshot,
    include_draft: false,
  }, token);
  if (Number(reEvalAsset.case_count || 0) < 1) fail(`re-eval asset has no cases: ${JSON.stringify(reEvalAsset)}`);
  const reEvalRun = post(`/api/prompt-evaluation-optimization-candidates/${candidate.id}/skill-re-eval-run`, {
    asset_id: reEvalAsset.asset.id,
  }, token);
  if (reEvalRun.run?.status !== "通过") fail(`re-eval did not pass: ${JSON.stringify(reEvalRun)}`);

  report.ok = true;
  report.result = "completed";
  report.prompt = { id: prompt.id };
  report.asset = { id: asset.id, case_count: benchmarkCases.length };
  report.eval_run = {
    id: failedRun.id,
    status: failedRun.status,
    total_cases: failedRun.total_cases,
    failed_cases: failedRun.failed_cases,
    trial_count: runEvidence.trials.length,
  };
  report.optimizer_candidate = {
    id: candidate.id,
    intent: updatedCandidate.skill_patch?.candidate_intent,
    operation_skill_key: updatedCandidate.skill_patch?.operation_skill_key,
    operation_skill_path: updatedCandidate.skill_patch?.operation_skill_path,
    patch_hash: updatedCandidate.skill_patch?.patch_hash,
  };
  report.project_resource = { project_id: project.id, source_resource_id: resource.id, inventory_count: inventory.inventory?.discovered_count };
  report.skill_writeback = {
    mode,
    freshness: { status: freshness.status, patch_check: freshness.patch_check },
    apply: {
      status: apply.apply.status,
      skill_hash_before: apply.apply.skill_hash_before,
      skill_hash_after: apply.apply.skill_hash_after,
      changed_files: apply.apply.changed_files,
    },
    re_eval_asset_id: reEvalAsset.asset.id,
    re_eval_run_id: reEvalRun.run.id,
    re_eval_status: reEvalRun.run.status,
    proof_scope: reEvalRun.re_eval_run?.proof_scope || reEvalRun.proof_scope,
  };
  report.benchmark_cases = benchmarkCases.map((item) => ({
    id: item.id,
    source_artifact: item.source_artifact,
    evidence_source: item.variables.evidence_source,
  }));
  report.failure_discipline = {
    enabled: true,
    observed_failures: [
      {
        signature: "project_status_enum_invalid",
        category: "api_contract",
        hypothesis_change: "Use the accepted project status enum value in_progress.",
      },
      {
        signature: "new_skill_changed_files_directory_only",
        category: "writeback_evidence_contract",
        hypothesis_change: "Accept either the exact generated skill file or its generated operation skill directory in apply evidence.",
      },
      {
        signature: "re_eval_missing_prompt_variables",
        category: "eval_contract",
        hypothesis_change: "Restrict prompt variables to the intersection available in both benchmark eval cases and skill re-eval cases.",
      },
    ],
    final_signature: "none",
  };
} catch (error) {
  report.ok = false;
  report.error = error instanceof Error ? error.stack || error.message : String(error);
}

writeReport();
console.log(JSON.stringify({
  ok: report.ok,
  result: report.result,
  latest_json: path.join(artifactRoot, "benchmark-training-loop-latest.json"),
  candidate: report.optimizer_candidate,
  skill_writeback: report.skill_writeback,
}, null, 2));
if (!report.ok) process.exitCode = 1;

function buildBenchmarkCases(quickEntry, historical) {
  const cases = [
    {
      id: "quick-entry-capability",
      case_name: "quick-entry current smoke service sandbox",
      source_artifact: "quick-entry-cross-service-latest.json",
      variables: {
        case_id: "quick-entry-capability",
        benchmark_verdict: "service sandbox passed",
        expected_behavior: "Gateway HTTP curl reaches user-center gRPC and ida-deployment permission/apiData/render evidence remains present.",
        verification: "make goal-test-quick-entry-cross-service",
        evidence_source: "artifacts/acceptance/quick-entry-cross-service-latest.json",
        skill_path: operationSkillPath,
        source_commit: repoState(repos.usercenter).commit,
      },
      expected_contains: [
        "Gateway HTTP curl reaches user-center gRPC",
        "make goal-test-quick-entry-cross-service",
        "artifacts/acceptance/quick-entry-cross-service-latest.json",
      ],
      tags: ["benchmark", "current-smoke", "user-center", "gateway", "ida-deployment"],
    },
  ];
  for (const item of historical.cases || []) {
    cases.push({
      id: item.id,
      case_name: `${item.id} historical service sandbox`,
      source_artifact: "historical-service-sandbox-latest.json",
      variables: {
        case_id: item.id,
        benchmark_verdict: item.status,
        expected_behavior: item.id === "product-api-permission"
          ? "ida-deployment product permission config is generated into gateway apiData and helm render without faking product-service."
          : "Gateway HTTP curl reaches real user-center gRPC logic with service sandbox evidence and business assertions.",
        verification: "make goal-test-historical-service-sandbox",
        evidence_source: "artifacts/acceptance/historical-service-sandbox-latest.json",
        skill_path: operationSkillPath,
        source_commit: repoState(repos.usercenter).commit,
      },
      expected_contains: [
        item.id === "product-api-permission"
          ? "ida-deployment product permission config"
          : "Gateway HTTP curl reaches real user-center gRPC logic",
        "make goal-test-historical-service-sandbox",
        "artifacts/acceptance/historical-service-sandbox-latest.json",
      ],
      tags: ["benchmark", "historical-replay", item.id],
    });
  }
  return cases;
}

function buildSkillCaseDrafts(cases) {
  return cases.map((item) => ({
    schema_version: "multica.skill.case_draft.v1",
    status: "approved",
    input: `Replay benchmark ${item.id} and preserve service sandbox evidence.`,
    expected_behavior: item.variables.expected_behavior,
    verification: item.variables.verification,
    evidence_source: item.variables.evidence_source,
    applicable_scope: operationSkillPath,
    applicable_skill_hash: "",
    source_commit: repoState(repos.usercenter).commit,
    commit_subject: `benchmark ${item.id}`,
    skill_path: operationSkillPath,
  }));
}

function renderOperationSkill() {
  return [
    "# Historical Benchmark Replay",
    "",
    "Use this operation skill when changing user-center behavior that must preserve the real benchmark replay set.",
    "",
    "## Required Checks",
    "",
    "- Run `make goal-test-quick-entry-cross-service` from `/data/ida/goal-test` for the current smoke sentinel.",
    "- Run `make goal-test-historical-service-sandbox` from `/data/ida/goal-test` for the v1 historical replay set.",
    "- Inspect `artifacts/acceptance/historical-service-sandbox-latest.json` and confirm link-user-space-org, announcement-notice, notice-same-second-cursor, and product-api-permission are all ok.",
    "- For Product API permission, do not fake product-service; keep the boundary to ida-deployment source/generated apiData/helm render unless product-service enters scope.",
    "- Record the benchmark artifact paths and remaining trace/eval/optimizer gaps before declaring completion.",
    "",
  ].join("\n");
}

function syntheticCreateOperationSnapshot(repoPath, sourceResourceID) {
  const branch = exec(repoPath, ["git", "branch", "--show-current"]).trim() || "HEAD";
  return {
    schema_version: "multica.skill.snapshot.v1",
    provider: "local_directory",
    repo: "user-center local checkout",
    repo_path: repoPath,
    branch,
    base_commit: exec(repoPath, ["git", "rev-parse", "HEAD"]).trim(),
    skill_path: operationSkillPath,
    skill_hash: "",
    snapshot_time: generatedAt,
    source_resource_id: sourceResourceID,
  };
}

function createOperationSkillPatch(repoPath, skillPath, content) {
  const absPath = path.join(repoPath, skillPath);
  fs.mkdirSync(path.dirname(absPath), { recursive: true });
  fs.writeFileSync(absPath, content);
  exec(repoPath, ["git", "add", "-N", skillPath]);
  const patch = exec(repoPath, ["git", "diff", "--", skillPath]);
  exec(repoPath, ["git", "reset", "--", skillPath]);
  fs.rmSync(absPath, { force: true });
  return patch;
}

function updateOperationSkillPatch(repoPath, skillPath, addition) {
  const absPath = path.join(repoPath, skillPath);
  const before = fs.readFileSync(absPath, "utf8");
  fs.writeFileSync(absPath, before.endsWith("\n") ? `${before}${addition}` : `${before}\n${addition}`);
  const patch = exec(repoPath, ["git", "diff", "--", skillPath]);
  fs.writeFileSync(absPath, before);
  return patch;
}

function resolveWorkspace(authToken) {
  const workspaces = get("/api/workspaces", authToken);
  const items = Array.isArray(workspaces) ? workspaces : workspaces.items || [];
  const workspace = items.find((item) => item.slug === workspaceSlug);
  if (!workspace?.id) fail(`workspace not found: ${workspaceSlug}`);
  return workspace;
}

function pollLocalRun(assetID, status, authToken) {
  const started = Date.now();
  while (Date.now() - started < 30000) {
    const runs = get(`/api/prompt-evaluation-runs?asset_id=${encodeURIComponent(assetID)}&limit=10`, authToken);
    const items = Array.isArray(runs) ? runs : runs.items || [];
    const found = items.find((item) => item.status === status);
    if (found) return found;
    sleep(1000);
  }
  fail(`timed out waiting for run status ${status}`);
}

function get(apiPath, authToken) {
  return request("GET", apiPath, null, authToken);
}

function post(apiPath, body, authToken) {
  return request("POST", apiPath, body, authToken);
}

function put(apiPath, body, authToken) {
  return request("PUT", apiPath, body, authToken);
}

function request(method, apiPath, body, authToken) {
  const url = `${apiURL}${apiPath}`;
  const args = ["--noproxy", "*", "-sS", "-w", "\n%{http_code}", "-X", method, url, "-H", "content-type: application/json"];
  if (authToken) args.push("-H", `Authorization: Bearer ${authToken}`);
  if (authToken && activeWorkspaceId) args.push("-H", `X-Workspace-ID: ${activeWorkspaceId}`);
  if (body !== null && body !== undefined) args.push("--data", JSON.stringify(body));
  report.commands.push(`curl ${redactArgs(args).map(shellQuote).join(" ")}`);
  const out = execFileSync("curl", args, { encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
  const splitAt = out.lastIndexOf("\n");
  const responseBody = splitAt >= 0 ? out.slice(0, splitAt) : out;
  const status = Number(splitAt >= 0 ? out.slice(splitAt + 1).trim() : 0);
  if (status < 200 || status >= 300) {
    fail(`${method} ${apiPath} returned ${status}: ${responseBody}`);
  }
  return responseBody.trim() ? JSON.parse(responseBody) : null;
}

function writeReport() {
  const jsonPath = path.join(artifactRoot, `benchmark-training-loop-${stamp}.json`);
  const latestJsonPath = path.join(artifactRoot, "benchmark-training-loop-latest.json");
  const markdownPath = path.join(artifactRoot, `benchmark-training-loop-${stamp}.md`);
  const latestMarkdownPath = path.join(artifactRoot, "benchmark-training-loop-latest.md");
  fs.writeFileSync(jsonPath, `${JSON.stringify(report, null, 2)}\n`);
  fs.writeFileSync(latestJsonPath, `${JSON.stringify(report, null, 2)}\n`);
  fs.writeFileSync(markdownPath, renderMarkdown(report));
  fs.writeFileSync(latestMarkdownPath, renderMarkdown(report));
}

function renderMarkdown(data) {
  const lines = ["# Benchmark Training Loop", "", `Generated: ${data.generated_at}`, "", `OK: ${data.ok}`, ""];
  if (data.optimizer_candidate) {
    lines.push(`Candidate: ${data.optimizer_candidate.id}`, `Intent: ${data.optimizer_candidate.intent}`, "");
  }
  if (data.skill_writeback) {
    lines.push("## Skill Writeback", "", `- mode: ${data.skill_writeback.mode}`, `- apply: ${data.skill_writeback.apply.status}`, `- re-eval: ${data.skill_writeback.re_eval_status}`, "");
  }
  return `${lines.join("\n")}\n`;
}

function fail(message) {
  throw new Error(message);
}

function readJSON(filePath) {
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
}

function repoState(repo) {
  return {
    path: repo,
    branch: exec(repo, ["git", "branch", "--show-current"]).trim(),
    commit: exec(repo, ["git", "rev-parse", "HEAD"]).trim(),
    dirty: exec(repo, ["git", "status", "--short"]).trim() !== "",
  };
}

function exec(cwd, command) {
  return execFileSync(command[0], command.slice(1), { cwd, encoding: "utf8", maxBuffer: 10 * 1024 * 1024 });
}

function trimEnv(name) {
  return (process.env[name] || "").trim();
}

function sleep(ms) {
  Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
}

function shellQuote(value) {
  const raw = String(value);
  if (/^[A-Za-z0-9_./:=?&%-]+$/.test(raw)) return raw;
  return `'${raw.replace(/'/g, "'\\''")}'`;
}

function redactArgs(args) {
  return args.map((arg, index) => {
    if (index > 0 && args[index - 1] === "-H" && /^Authorization:/i.test(arg)) return "Authorization: Bearer <redacted>";
    return arg;
  });
}
