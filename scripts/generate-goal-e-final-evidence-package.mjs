#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const now = new Date().toISOString();
const stamp = now.replace(/[:.]/g, "-");

fs.mkdirSync(artifactRoot, { recursive: true });

const unifiedUIPath = latestMatching(/^goal-e-unified-ui-playwright-.*\.json$/);
const finalAcceptancePath = fileIfExists(path.join(artifactRoot, "tapd-gongfeng-sop-final-acceptance-latest.json"));
const gapAuditPath = fileIfExists(path.join(artifactRoot, "tapd-gongfeng-sop-gap-audit-latest.json"));
const runbookPath = fileIfExists(path.join(artifactRoot, "tapd-gongfeng-sop-production-runbook.md"));
const goalCSlicePath = latestMatching(/^goal-c-issue-timeline-slice-.*\.md$/);
const goalDSkillPath = latestMatching(/^goal-d-skill-full-local-e2e-.*\.json$/);
const gongfengAuditPath = latestMatching(/^goal-e-gongfeng-touchpoint-audit-.*\.json$/);

const unifiedUI = unifiedUIPath ? readJSON(unifiedUIPath) : null;
const finalAcceptance = finalAcceptancePath ? readJSON(finalAcceptancePath) : null;
const gapAudit = gapAuditPath ? readJSON(gapAuditPath) : null;
const goalDSkill = goalDSkillPath ? readJSON(goalDSkillPath) : null;
const logEvidence = runJSONCommand(["node", "scripts/goal-test-environments.mjs", "verify-logs", "int"]);
const gitCommit = runTextCommand(["git", "rev-parse", "HEAD"]).trim();
const gitStatus = runTextCommand(["git", "status", "--short"])
  .split(/\r?\n/)
  .map((line) => line.trimEnd())
  .filter(Boolean);

const pageTimings = unifiedUI?.checks?.page_timings_ms || {};
const pageTimingEntries = Object.entries(pageTimings).map(([page, durationMs]) => ({
  page,
  duration_ms: Number(durationMs),
  ok: Number(durationMs) > 0 && Number(durationMs) <= 5000,
}));
const playwrightClean =
  unifiedUI?.ok === true &&
  emptyArray(unifiedUI?.checks?.console_errors) &&
  emptyArray(unifiedUI?.checks?.page_errors) &&
  emptyArray(unifiedUI?.checks?.failed_requests);
const logsClean = logEvidence.ok === true && logEvidence.json?.ok === true;
const performanceClean = pageTimingEntries.length >= 5 && pageTimingEntries.every((item) => item.ok);

const requiredEvidence = {
  commit: Boolean(gitCommit),
  environment: logEvidence.ok === true && logEvidence.json?.target === "int",
  commands: true,
  issue_ids: Boolean(unifiedUI?.issue?.id || finalAcceptance?.e2e?.issue?.id),
  run_urls_or_api: Boolean(unifiedUI?.prompt_evaluation?.failed_run_id || finalAcceptance?.goal_d_skill_chain?.latest_json?.failed_run_id),
  trace_eval_optimizer: Boolean(unifiedUI?.prompt_evaluation?.candidate_id && unifiedUI?.prompt_evaluation?.re_eval_run_id),
  screenshots: Object.keys(unifiedUI?.screenshots || {}).length >= 5,
  logs: logsClean,
  gap_audit: Boolean(gapAuditPath && gapAudit?.summary),
};

const packageOk = playwrightClean && logsClean && performanceClean && Object.values(requiredEvidence).every(Boolean);
const finalAcceptanceOpenItems = finalAcceptance?.blocking_open_items || [];
const gapSummary = gapAudit?.summary || null;
const gapHasNoBlockers =
  gapAudit?.ok === true &&
  Number(gapSummary?.blockers || 0) === 0 &&
  Number(gapSummary?.goal_e_blockers || 0) === 0 &&
  Number(gapSummary?.production_blockers || 0) === 0;
const archiveCompleteAllowed =
  packageOk &&
  finalAcceptance?.ok === true &&
  finalAcceptanceOpenItems.length === 0 &&
  gapHasNoBlockers &&
  gitStatus.length === 0;

const artifact = {
  schema: "multica.goal_e.final_evidence_package.v1",
  generated_at: now,
  ok: packageOk,
  archive_complete_allowed: archiveCompleteAllowed,
  proof_boundary: archiveCompleteAllowed
    ? "Evidence package, final acceptance, gap audit, clean git status, and log checks all passed; Goal E archive/complete is allowed."
    : finalAcceptanceOpenItems.length > 0
    ? "Evidence package is complete for logs/performance/package requirements, but final Goal E acceptance remains blocked by open matrix items."
    : "Evidence package is complete and no final acceptance blockers were present when this package was generated.",
  git: {
    commit: gitCommit,
    status_short: gitStatus,
  },
  environment: {
    target: "int",
    log_evidence: logEvidence.json,
  },
  commands: [
    "node scripts/goal-test-environments.mjs verify-logs int",
    "NEXT_PUBLIC_API_URL=http://127.0.0.1:18760 PLAYWRIGHT_BASE_URL=http://127.0.0.1:13680 FRONTEND_ORIGIN=http://127.0.0.1:13680 pnpm exec playwright test e2e/goal-e-unified-ui.spec.ts --project=chromium",
    "node scripts/generate-tapd-gongfeng-sop-final-acceptance.mjs",
    "node scripts/tapd-gongfeng-sop-gap-audit.mjs",
    "node scripts/generate-goal-e-final-evidence-package.mjs",
  ],
  linked_artifacts: {
    unified_ui: unifiedUIPath,
    final_acceptance: finalAcceptancePath,
    gap_audit: gapAuditPath,
    runbook: runbookPath,
    goal_c_issue_timeline: goalCSlicePath,
    goal_d_skill_chain: goalDSkillPath,
    gongfeng_touchpoint_audit: gongfengAuditPath,
  },
  ids: {
    issue_id: unifiedUI?.issue?.id || finalAcceptance?.e2e?.issue?.id || null,
    project_id: unifiedUI?.project?.id || null,
    workspace_slug: unifiedUI?.workspace?.slug || null,
    prompt_evaluation_asset_id: unifiedUI?.prompt_evaluation?.asset_id || goalDSkill?.asset_id || null,
    failed_run_id: unifiedUI?.prompt_evaluation?.failed_run_id || goalDSkill?.failed_run_id || null,
    candidate_id: unifiedUI?.prompt_evaluation?.candidate_id || goalDSkill?.candidate_id || null,
    re_eval_run_id: unifiedUI?.prompt_evaluation?.re_eval_run_id || goalDSkill?.re_eval_run_id || null,
  },
  urls_or_api_paths: {
    issue: unifiedUI?.issue?.id && unifiedUI?.workspace?.slug ? `/${unifiedUI.workspace.slug}/issues/${unifiedUI.issue.id}` : null,
    run_history: unifiedUI?.prompt_evaluation?.failed_run_id && unifiedUI?.workspace?.slug
      ? `/${unifiedUI.workspace.slug}/training/run-history?run=${unifiedUI.prompt_evaluation.failed_run_id}`
      : null,
    optimization_runs: unifiedUI?.workspace?.slug ? `/${unifiedUI.workspace.slug}/training/optimization-runs` : null,
    run_evidence_api: unifiedUI?.prompt_evaluation?.failed_run_id
      ? `/api/prompt-evaluation-runs/${unifiedUI.prompt_evaluation.failed_run_id}/evidence`
      : null,
  },
  trace_eval_optimizer: {
    coverage: unifiedUI?.coverage || null,
    prompt_evaluation: unifiedUI?.prompt_evaluation || null,
    goal_d_skill_summary: goalDSkill ? {
      proof_scope: goalDSkill.proof_scope,
      draft_count: goalDSkill.draft_count,
      re_eval_status: goalDSkill.re_eval_status,
      apply_status: goalDSkill.apply?.status,
      changelog_path: goalDSkill.apply?.changelog_path,
    } : null,
  },
  screenshots: unifiedUI?.screenshots || {},
  checks: {
    playwright_clean: playwrightClean,
    logs_clean: logsClean,
    performance_clean: performanceClean,
    page_timings: pageTimingEntries,
    console_errors: unifiedUI?.checks?.console_errors || [],
    page_errors: unifiedUI?.checks?.page_errors || [],
    failed_requests: unifiedUI?.checks?.failed_requests || [],
    required_evidence: requiredEvidence,
  },
  final_acceptance_open_items: finalAcceptanceOpenItems.map((item) => ({
    id: item.id,
    status: item.status,
    title: item.title,
  })),
  gap_summary: gapSummary,
};

const jsonPath = path.join(artifactRoot, `goal-e-final-evidence-package-${stamp}.json`);
const latestPath = path.join(artifactRoot, "goal-e-final-evidence-package-latest.json");
fs.writeFileSync(jsonPath, `${JSON.stringify(artifact, null, 2)}\n`);
fs.writeFileSync(latestPath, `${JSON.stringify(artifact, null, 2)}\n`);

console.log(JSON.stringify({
  ok: artifact.ok,
  json: jsonPath,
  latest: latestPath,
  open_goal_e_items: artifact.final_acceptance_open_items,
}, null, 2));
if (!artifact.ok) process.exitCode = 1;

function latestMatching(regex) {
  if (!fs.existsSync(artifactRoot)) return null;
  return fs.readdirSync(artifactRoot)
    .filter((name) => regex.test(name))
    .map((name) => path.join(artifactRoot, name))
    .filter((filePath) => fs.statSync(filePath).isFile())
    .sort((left, right) => fs.statSync(right).mtimeMs - fs.statSync(left).mtimeMs)[0] || null;
}

function fileIfExists(filePath) {
  return fs.existsSync(filePath) ? filePath : null;
}

function readJSON(filePath) {
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
}

function emptyArray(value) {
  return Array.isArray(value) && value.length === 0;
}

function runTextCommand(command) {
  const result = spawnSync(command[0], command.slice(1), {
    cwd: repoRoot,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    throw new Error(`${command.join(" ")} failed: ${result.stderr || result.stdout}`);
  }
  return result.stdout;
}

function runJSONCommand(command) {
  const result = spawnSync(command[0], command.slice(1), {
    cwd: repoRoot,
    encoding: "utf8",
  });
  if (result.status !== 0) {
    return {
      ok: false,
      command: command.join(" "),
      error: result.stderr || result.stdout,
      json: null,
    };
  }
  try {
    return {
      ok: true,
      command: command.join(" "),
      json: JSON.parse(result.stdout),
    };
  } catch (error) {
    return {
      ok: false,
      command: command.join(" "),
      error: error instanceof Error ? error.message : String(error),
      json: null,
      stdout: result.stdout,
    };
  }
}
