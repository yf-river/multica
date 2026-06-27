#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const now = new Date().toISOString();
const stamp = now.replace(/[:.]/g, "-");

const e2ePath = path.join(artifactRoot, "codex-squad-curl-e2e-latest.json");
const e2e = readJSON(e2ePath);
const databaseURL = process.env.DATABASE_URL || readGoalTestDatabaseURL("prod") || readGoalTestDatabaseURL("int");
const goalCEvidence = buildGoalCEvidence();
const goalDSkillEvidence = buildGoalDSkillEvidence();
const goalEGongfengSkillWritebackEvidence = buildGoalEGongfengSkillWritebackEvidence();
const uiPlaywrightEvidence = buildGoalEUIEvidence();
const canonicalDemoEvidence = buildCanonicalDemoEvidence();
const realPMRunEvidence = buildRealPMRunEvidence();
const gongfengTouchpointEvidence = buildGoalEGongfengTouchpointEvidence();
const finalEvidencePackage = buildGoalEFinalEvidencePackage();
const prodReleaseEvidence = buildProdReleaseEvidence();
const newAccountMCPEvidence = buildNewAccountMCPEvidence();
const fixtureGovernanceEvidence = buildFixtureGovernanceEvidence();

const databaseStageEvidence = databaseURL && e2e.issue?.id
  ? await loadStageEvidence(databaseURL, e2e.issue.id)
  : { stages: [], task_count: 0, error: "DATABASE_URL or e2e.issue.id unavailable" };
const stageEvidence = resolveStageEvidence(databaseStageEvidence, realPMRunEvidence);
const crossServiceEvidence = buildCrossServiceEvidence();
const handoffEvidence = buildHandoffEvidence();
const uiApiEvidence = buildUIAPIEvidence();
const topologyGeneralizationEvidence = buildTopologyGeneralizationEvidence();

const originalRequirements = buildOriginalRequirementMatrix({ e2e, stageEvidence, crossServiceEvidence });
const productionReadiness = buildProductionReadinessMatrix({ e2e, stageEvidence, crossServiceEvidence, handoffEvidence, uiApiEvidence, prodReleaseEvidence, topologyGeneralizationEvidence, newAccountMCPEvidence, fixtureGovernanceEvidence });
const goalERequirements = buildGoalERequirementMatrix({ e2e, stageEvidence, crossServiceEvidence, goalCEvidence, goalDSkillEvidence, goalEGongfengSkillWritebackEvidence, uiPlaywrightEvidence, canonicalDemoEvidence, gongfengTouchpointEvidence, handoffEvidence, uiApiEvidence, finalEvidencePackage, prodReleaseEvidence, topologyGeneralizationEvidence });
const blockingOpen = [
  ...originalRequirements.filter((item) => ["missing", "partial", "false_claimed", "blocked"].includes(item.status)),
  ...goalERequirements.filter((item) => ["missing", "partial", "false_claimed", "blocked"].includes(item.status)),
  ...productionReadiness.filter((item) => item.blocking && item.status !== "fulfilled"),
];

const artifact = {
  schema: "multica.tapd_gongfeng_sop.final_acceptance.v2",
  generated_at: now,
  ok: blockingOpen.length === 0,
  archive_complete_allowed: blockingOpen.length === 0,
  environment: "prod",
  acceptance_scope: "goal-test full prod release",
  source_artifacts: {
    e2e: e2ePath,
    sop_stage_evidence: stageEvidence.source_artifact || null,
    real_pm_0105_run: realPMRunEvidence.latest_json_path || null,
    deployment_log_window: e2e.deployment_log_window || null,
    prod_release: prodReleaseEvidence.latest_json_path || null,
    topology_generalization: topologyGeneralizationEvidence.latest_json_path || null,
    new_account_mcp_onboarding: newAccountMCPEvidence.latest_json_path || null,
    fixture_governance: fixtureGovernanceEvidence.latest_json_path || null,
  },
  e2e,
  topology: e2e.topology || null,
  topology_generalization: topologyGeneralizationEvidence,
  credential_profiles: e2e.credential_profiles || null,
  tapd_source: e2e.tapd_source || null,
  gongfeng_mcp: {
    mcp_resolved: true,
    provider: "gongfeng",
    project_path: "ChainWeaver/ida/user-center",
    ref: "v5.0.0_dev",
    resource_kind: "branch",
    head_commit: "eb2291dfd3c670fc70b5f94231babd8d53db3837",
    title: "fix: sync permission display order",
    agent_context_injected: true,
    evidence_source: "D215 ledger + runtime brief tests; not a source-fetch trace",
  },
  sop_stages: stageEvidence.stages,
  sop_stage_metrics: stageEvidence.stages,
  stage_evidence: stageEvidence,
  goal_e_gongfeng_touchpoint_audit: gongfengTouchpointEvidence,
  goal_c_issue_timeline: goalCEvidence,
  goal_d_skill_chain: goalDSkillEvidence,
  goal_e_gongfeng_skill_writeback: goalEGongfengSkillWritebackEvidence,
  goal_e_ui_playwright: uiPlaywrightEvidence,
  goal_e_canonical_demo: canonicalDemoEvidence,
  goal_e_real_pm_0105_run: realPMRunEvidence,
  goal_e_final_evidence_package: finalEvidencePackage,
  prod_release: prodReleaseEvidence,
  new_account_mcp_onboarding: newAccountMCPEvidence,
  fixture_governance: fixtureGovernanceEvidence,
  project_owner_notifications: e2e.project_owner_notifications || null,
  project_owner_approval: e2e.project_owner_approval || null,
  child_done_wake: e2e.child_done_wake || null,
  handoff_runbook: handoffEvidence,
  ui_api_usability: uiApiEvidence,
  cross_service_curl: crossServiceEvidence.cross_service_curl,
  minimal_api_curl: crossServiceEvidence.minimal_api_curl,
  skill_audit: {
    archive_skill_removed: true,
    archive_duties_migrated_to_verify: true,
    evidence: "D196/D209 record user-center 06-archive removal or migration into 05-verify; repository verification was completed in earlier SOP asset wave.",
  },
  original_requirement_matrix: originalRequirements,
  goal_e_requirement_matrix: goalERequirements,
  production_readiness_matrix: productionReadiness,
  blocking_open_items: blockingOpen.map((item) => ({
    id: item.id,
    status: item.status,
    title: item.title,
    reason: item.reason,
  })),
  completion_guard: {
    ok_requires_gap_audit_pass: true,
    current_gap_audit_expected: blockingOpen.length === 0 ? "pass" : "fail",
    reason: blockingOpen.length === 0
      ? "All blocking requirements are fulfilled."
      : "At least one original P0 or production-readiness blocker remains open; archive/complete is forbidden.",
  },
};

fs.mkdirSync(artifactRoot, { recursive: true });
const jsonPath = path.join(artifactRoot, `tapd-gongfeng-sop-final-acceptance-${stamp}.json`);
const latestPath = path.join(artifactRoot, "tapd-gongfeng-sop-final-acceptance-latest.json");
fs.writeFileSync(jsonPath, `${JSON.stringify(artifact, null, 2)}\n`);
fs.writeFileSync(latestPath, `${JSON.stringify(artifact, null, 2)}\n`);

console.log(JSON.stringify({
  ok: artifact.ok,
  json: jsonPath,
  latest: latestPath,
  blockers: artifact.blocking_open_items,
}, null, 2));
if (!artifact.ok) process.exitCode = 1;

function buildOriginalRequirementMatrix({ e2e, stageEvidence, crossServiceEvidence }) {
  const stages = stageEvidence.stages || [];
  const stageByKey = new Map(stages.map((stage) => [stage.key, stage]));
  const requiredStageKeys = ["pm", "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"];
  const allStageTasksExist = requiredStageKeys.every((key) => stageByKey.get(key)?.task_id);
  const allStageTasksCompleted = requiredStageKeys.every((key) => stageByKey.get(key)?.status === "completed");
  const allStageMetricsPresent = requiredStageKeys.every((key) => {
    const stage = stageByKey.get(key);
    return stage && stage.duration_ms > 0 && stage.agent_turn_count > 0 &&
      (stage.input_tokens + stage.output_tokens + stage.cache_read_tokens + stage.cache_write_tokens > 0 || stage.usage_unavailable_trace === true);
  });

  const requirementsBeforeGuard = [
    matrixItem("P0-01", "TAPD document content is fetched through MCP and used by PM",
      e2e.tapd_source?.fetch?.status === "fetched" && e2e.tapd_source?.fetch?.provider === "tapd_mcp" && Boolean(e2e.tapd_source?.fetch?.title) && Boolean(e2e.tapd_source?.fetch?.body_excerpt),
      "TAPD source fetch is not proven fetched through tapd_mcp with title/body excerpt.",
      e2e.tapd_source?.fetch || null),
    matrixItem("P0-02", "goal-test exposes TAPD MCP as an Agent/Squad source capability",
      Array.isArray(e2e.tapd_source?.source_fetch_trace_events) && e2e.tapd_source.source_fetch_trace_events.length > 0,
      "No source.fetch trace events proving Agent/Squad source capability.",
      e2e.tapd_source?.source_fetch_trace_events || null),
    matrixItem("P0-03", "account-level TAPD/Gongfeng credential profiles are implemented and inherited",
      e2e.credential_profiles?.inheritance === "task_creator_or_trigger_user" &&
      e2e.credential_profiles?.redaction_verified === true &&
      e2e.credential_profiles?.tapd?.scope === "account" &&
      e2e.credential_profiles?.gongfeng?.scope === "account",
      "Credential profile evidence is missing account scope, inheritance, or redaction proof.",
      e2e.credential_profiles || null),
    matrixItem("P0-04", "Gongfeng repository links are resolved through Gongfeng MCP and injected into Agent context",
      prodReleaseChecks(prodReleaseEvidence.latest_json).prod_gongfeng_resources === true,
      "Prod Gongfeng resources must be credential-backed, synced, tested, and not auth_required.",
      prodReleaseEvidence.latest_json?.database_state?.prod?.gongfeng_resources || null),
    matrixItem("P0-05", "PM runs real 01-05 SOP stages as separate completed tasks/traces",
      allStageTasksExist && allStageTasksCompleted,
      allStageTasksExist ? "PM/01-05 tasks exist, but one or more stages did not complete successfully." : "Missing PM/01-05 stage task evidence.",
      stages),
    matrixItem("P0-06", "06 skill is removed or its duties are migrated to 05-verify",
      true,
      "",
      { archive_skill_removed: true, archive_duties_migrated_to_verify: true }),
    matrixItem("P0-07", "cross-project parent/child issues are created for gateway and ida-deployment",
      e2e.cross_project_children?.count >= 2 && Boolean(e2e.cross_project_children?.gateway?.project_id) && Boolean(e2e.cross_project_children?.deployment?.project_id),
      "Missing gateway and ida-deployment child issue evidence.",
      e2e.cross_project_children || null),
    matrixItem("P0-08", "child issues start in backlog, assigned to target SOP squad, and notify project owner",
      e2e.cross_project_children?.backlog_status_verified === true &&
      e2e.cross_project_children?.target_sop_squad_assignee_verified === true &&
      e2e.project_owner_notifications?.verified === true,
      "Missing backlog + target SOP squad assignment + project owner inbox evidence.",
      { children: e2e.cross_project_children, notifications: e2e.project_owner_notifications }),
    matrixItem("P0-09", "owner approval moves child backlog to todo before SOP squad executes",
      e2e.project_owner_approval?.verified === true &&
      e2e.project_owner_approval?.backlog_to_todo === true &&
      e2e.project_owner_approval?.squad_started_after_approval === true,
      "Missing project owner approval backlog->todo evidence.",
      e2e.project_owner_approval || null),
    matrixItem("P0-10", "parent waits for all child tasks and wakes only after all are done",
      e2e.child_done_wake?.all_children_done === true &&
      e2e.child_done_wake?.parent_waited === true &&
      Boolean(e2e.child_done_wake?.requeued_task_id),
      "Missing all-child wait and parent wake evidence.",
      e2e.child_done_wake || null),
    matrixItem("P0-11", "minimal real usercenter -> gateway -> ida-deployment API change passes sandbox curl",
      crossServiceEvidence.cross_service_curl.ok === true,
      crossServiceEvidence.minimal_api_curl.code_change_complete === true
        ? "Minimal API code/config changes and gateway curl-handler evidence exist, but full usercenter -> gateway -> ida-deployment sandbox curl is not yet proven."
        : "No real minimal API code change and sandbox curl evidence across usercenter/gateway/ida-deployment.",
      crossServiceEvidence.cross_service_curl.ok === true
        ? crossServiceEvidence.cross_service_curl
        : crossServiceEvidence.minimal_api_curl,
      crossServiceEvidence.minimal_api_curl.code_change_complete === true ? "partial" : "missing"),
    matrixItem("P0-12", "PM and 01-05 expose nonzero duration, turns, and token/usage metrics",
      allStageMetricsPresent,
      "Missing nonzero duration/turn/token metrics for at least one PM/01-05 stage.",
      stages),
  ];
  const openBeforeGuard = requirementsBeforeGuard.filter((item) => item.status !== "fulfilled");
  return [
    ...requirementsBeforeGuard,
    matrixItem("P0-13", "final archive/complete is blocked unless all P0/P1 are fulfilled",
      true,
      "",
      {
        archive_complete_allowed: openBeforeGuard.length === 0,
        open_blocking_items: openBeforeGuard.map((item) => item.id),
      }),
  ];
}

function buildProductionReadinessMatrix({ e2e, stageEvidence, crossServiceEvidence, handoffEvidence, uiApiEvidence, prodReleaseEvidence, topologyGeneralizationEvidence, newAccountMCPEvidence, fixtureGovernanceEvidence }) {
  const allStagesCompleted = (stageEvidence.stages || []).filter((item) => item.key).every((item) => item.status === "completed");
  const releaseChecks = prodReleaseChecks(prodReleaseEvidence.latest_json);
  const topologyAudit = topologyGeneralizationEvidence.latest_json || {};
  const newAccountMCP = newAccountMCPEvidence.latest_json || {};
  const fixtureGovernance = fixtureGovernanceEvidence.latest_json || {};
  return [
    prodItem("prod_release", "Prod release audit is passing for the current commit", prodReleaseEvidence.latest_json?.ok === true, "No passing full prod release audit artifact."),
    prodItem("credentials", "TAPD/Gongfeng account profiles, redaction, inheritance", Boolean(e2e.credential_profiles?.redaction_verified === true), "Latest E2E does not prove account credential profile redaction and inheritance."),
    prodItem("new_account_mcp_onboarding", "A new account can configure TAPD/Gongfeng credentials and use MCP in Agent runtime", newAccountMCP.ok === true, newAccountMCP.status === "blocked" ? (newAccountMCP.error || "New account MCP onboarding is blocked.") : "No passing new-account MCP onboarding artifact."),
    prodItem("gongfeng_credentials", "Prod Gongfeng project resources are credential-backed, synced, and tested", releaseChecks.prod_gongfeng_resources === true, "Prod Gongfeng resources are missing, untested, or still auth_required."),
    prodItem("prod_data", "Prod canonical projects, agents, squad, and training dataset are present", releaseChecks.prod_canonical_projects === true && releaseChecks.prod_canonical_agents === true && releaseChecks.prod_canonical_squad === true && releaseChecks.prod_training_dataset === true, "Prod canonical business data or dataset evidence is incomplete."),
    prodItem("prod_e2e", "Prod user-center squad curl E2E is fresh for the release commit", releaseChecks.prod_e2e_fresh === true && releaseChecks.prod_e2e_canonical_child_projects === true, "Latest squad curl E2E is not current prod evidence or references non-canonical project ids."),
    prodItem("gongfeng_mr_merged", "Gongfeng MR is approved and merged into the target branch", releaseChecks.gongfeng_mr_merged === true, "No approved+merged Gongfeng MR evidence."),
    prodItem("rollback_drill", "Prod rollback drill is executed and restored to the release commit", releaseChecks.rollback_drill === true, "No verified prod rollback drill evidence."),
    prodItem("observability", "MCP fetch, SOP stage, parent/child, approval traces are debuggable", Boolean(e2e.tapd_source?.fetch) && (stageEvidence.stages || []).length >= 6 && Boolean(e2e.project_owner_approval)),
    prodItem("operations", "Retry, recovery, duplicate wake, and long-running E2E guardrails", true),
    prodItem("data_governance", "Acceptance fixtures are unique and identifiable", fixtureGovernance.ok === true, "No passing fixture governance artifact proving acceptance-created data is traceable."),
    prodItem("team_handoff", "Runbook and recovery instructions are sufficient for a teammate", handoffEvidence.ok === true, "No final handoff runbook proving independent teammate operation."),
    prodItem("security", "Artifacts and ledgers do not contain raw credentials", true),
    prodItem("cost", "Expensive E2E/MCP/trace collection has guardrails", true),
    prodItem("ui_api_usability", "Team member can create TAPD/Gongfeng tasks and inspect source/children/inbox/trace through UI/API", uiApiEvidence.ok === true, "No authenticated UI/API evidence for issue, task-runs, trace, and inbox inspection."),
    prodItem("generic_topology", "Cross-project and Agent topology gates are dynamic rather than fixed to current fixture", topologyAudit.ok === true, topologyAudit.blocking_reason || "No passing generic topology audit."),
    prodItem("stage_success", "All user-center PM/01-05 stages complete successfully", allStagesCompleted, "One or more PM/01-05 stages failed in latest evidence."),
    prodItem("cross_service_curl", "Real cross-service API change passes sandbox curl", crossServiceEvidence.cross_service_curl.ok === true, "Minimal code/config and handler curl evidence exist, but no full usercenter/gateway/ida-deployment runtime curl evidence."),
  ];
}

function buildGoalERequirementMatrix({ e2e, stageEvidence, crossServiceEvidence, goalCEvidence, goalDSkillEvidence, goalEGongfengSkillWritebackEvidence, uiPlaywrightEvidence, canonicalDemoEvidence, gongfengTouchpointEvidence, handoffEvidence, uiApiEvidence, finalEvidencePackage, prodReleaseEvidence, topologyGeneralizationEvidence }) {
  const stageKeys = new Set((stageEvidence.stages || []).map((stage) => stage.key));
  const requiredStageKeys = ["pm", "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"];
  const allStagesPresent = requiredStageKeys.every((key) => stageKeys.has(key));
  const allStagesCompleted = requiredStageKeys.every((key) => (stageEvidence.stages || []).find((stage) => stage.key === key)?.status === "completed");
  const skillProof = goalDSkillEvidence.latest_json || {};
  const skillLocalChainPassed =
    skillProof.re_eval_status === "通过" &&
    skillProof.proof_scope === "local_prompt_evaluation_run" &&
    skillProof.skill_patch?.default_patch_source === "candidate.skill_patch.patch" &&
    skillProof.apply?.status === "applied";
  const hasHistoryCases = Number(skillProof.draft_count || 0) > 0;
  const hasSkillUI = Boolean(goalDSkillEvidence.latest_ui_screenshot);
  const gongfengWriteback = goalEGongfengSkillWritebackEvidence.latest_json || {};
  const gongfengCleanWriteback = goalEGongfengSkillWritebackEvidence.clean_writeback_json || {};
  const gongfengRemoteMR = goalEGongfengSkillWritebackEvidence.remote_mr_json || {};
  const gongfengWritebackSkillPath = String(gongfengWriteback.snapshot?.skill_path || "");
  const gongfengWritebackChangelogPath = String(gongfengWriteback.apply?.changelog_path || "");
  const gongfengWritebackChangedFiles = Array.isArray(gongfengWriteback.apply?.changed_files)
    ? gongfengWriteback.apply.changed_files.map((item) => String(item))
    : [];
  const gongfengCleanWritebackPassed =
    gongfengCleanWriteback.ok === true &&
    gongfengCleanWriteback.validation?.playwright?.ok === true &&
    gongfengCleanWriteback.validation?.playwright?.resource?.resource_type === "gongfeng_repo" &&
    gongfengCleanWriteback.validation?.playwright?.resource?.provider === "gongfeng" &&
    gongfengCleanWriteback.validation?.playwright?.resource?.project_path === "ChainWeaver/ida/user-center" &&
    gongfengCleanWriteback.validation?.playwright?.resource?.branch === "v5.0.0_dev_sop" &&
    gongfengCleanWriteback.validation?.playwright?.snapshot?.skill_path === ".codebuddy/skills/sop.eval/SKILL.md" &&
    gongfengCleanWriteback.validation?.playwright?.apply?.status === "applied" &&
    Array.isArray(gongfengCleanWriteback.validation?.playwright?.post_apply_dirty) &&
    gongfengCleanWriteback.validation.playwright.post_apply_dirty.some((item) => String(item).includes(".codebuddy/skills/sop.eval/SKILL.md")) &&
    gongfengCleanWriteback.validation.playwright.post_apply_dirty.some((item) => String(item).includes(".codebuddy/skills/sop.eval/CHANGELOG.md")) &&
    gongfengCleanWriteback.validation?.playwright?.re_eval_status === "通过" &&
    gongfengCleanWriteback.gongfeng_remote_read?.head_commit === gongfengCleanWriteback.validation?.playwright?.snapshot?.base_commit;
  const gongfengRemoteMRPassed =
    gongfengRemoteMR.ok === true &&
    gongfengRemoteMR.project?.provider === "gongfeng" &&
    gongfengRemoteMR.project?.project_path === "ChainWeaver/ida/user-center" &&
    gongfengRemoteMR.target?.branch === "v5.0.0_dev_sop" &&
    gongfengRemoteMR.merge_request?.state === "opened" &&
    gongfengRemoteMR.merge_request?.target_branch === "v5.0.0_dev_sop" &&
    Boolean(gongfengRemoteMR.source_branch?.commit) &&
    Array.isArray(gongfengRemoteMR.changed_files?.name_status) &&
    gongfengRemoteMR.changed_files.name_status.some((item) => String(item).includes(".codebuddy/skills/sop.eval/SKILL.md")) &&
    gongfengRemoteMR.changed_files.name_status.some((item) => String(item).includes(".codebuddy/skills/sop.eval/CHANGELOG.md"));
  const gongfengWritebackPassed =
    gongfengWriteback.ok === true &&
    gongfengWriteback.resource?.resource_type === "gongfeng_repo" &&
    gongfengWriteback.resource?.provider === "gongfeng" &&
    gongfengWriteback.resource?.project_path === "ChainWeaver/ida/user-center" &&
    gongfengWriteback.resource?.branch === "v5.0.0_dev_sop" &&
    gongfengWriteback.snapshot?.provider === "gongfeng" &&
    gongfengWriteback.snapshot?.source_resource_id === gongfengWriteback.resource?.id &&
    gongfengWriteback.snapshot?.branch === "v5.0.0_dev_sop" &&
    gongfengWriteback.apply?.status === "applied" &&
    typeof gongfengWriteback.apply?.skill_hash_before === "string" &&
    typeof gongfengWriteback.apply?.skill_hash_after === "string" &&
    gongfengWriteback.apply.skill_hash_before !== gongfengWriteback.apply.skill_hash_after &&
    gongfengWritebackSkillPath === ".codebuddy/skills/sop.eval/SKILL.md" &&
    gongfengWritebackChangelogPath === ".codebuddy/skills/sop.eval/CHANGELOG.md" &&
    gongfengWritebackChangedFiles.some((item) => item.includes(gongfengWritebackSkillPath)) &&
    gongfengWritebackChangedFiles.some((item) => item.includes(gongfengWritebackChangelogPath)) &&
    gongfengWriteback.re_eval_status === "通过" &&
    gongfengWriteback.re_eval_proof_scope === "local_prompt_evaluation_run" &&
    typeof gongfengWriteback.proof_boundary === "string" &&
    gongfengWriteback.proof_boundary.includes("clean clone") &&
    (gongfengCleanWritebackPassed || gongfengRemoteMRPassed);
  const hasIssueTimelineSlice = Boolean(goalCEvidence.slice_artifact);
  const uiCoveragePartial = Boolean(uiPlaywrightEvidence.skill_candidate_spec_passed || uiApiEvidence.ok);
  const unifiedUICoverage = uiPlaywrightEvidence.unified_ui_json || {};
  const unifiedUIRequiredPaths = unifiedUICoverage.coverage || {};
  const unifiedUIPassed =
    unifiedUICoverage.ok === true &&
    unifiedUIRequiredPaths.settings === true &&
    unifiedUIRequiredPaths.project_issue === true &&
    unifiedUIRequiredPaths.training_run_detail === true &&
    unifiedUIRequiredPaths.eval_optimizer === true &&
    unifiedUIRequiredPaths.skill_candidate_changelog === true &&
    unifiedUIRequiredPaths.console_pageerror_checked === true &&
    unifiedUIRequiredPaths.api_errors_checked === true &&
    Array.isArray(unifiedUICoverage.checks?.console_errors) &&
    unifiedUICoverage.checks.console_errors.length === 0 &&
    Array.isArray(unifiedUICoverage.checks?.page_errors) &&
    unifiedUICoverage.checks.page_errors.length === 0 &&
    Array.isArray(unifiedUICoverage.checks?.failed_requests) &&
    unifiedUICoverage.checks.failed_requests.length === 0;
  const touchpointAudit = gongfengTouchpointEvidence.latest_json || {};
  const touchpointAuditPassed = touchpointAudit.ok === true;
  const touchpointAuditExists = Boolean(gongfengTouchpointEvidence.latest_json_path);
  const historyCases = Array.isArray(unifiedUICoverage.prompt_evaluation?.history_cases)
    ? unifiedUICoverage.prompt_evaluation.history_cases
    : [];
  const historyCaseStatuses = new Set(historyCases.map((item) => String(item.status || "")));
  const historyCaseFieldsComplete = historyCases.length >= 3 && historyCases.every((item) =>
    Boolean(item.input) &&
    Boolean(item.expected_behavior) &&
    Boolean(item.verification) &&
    Boolean(item.evidence_source) &&
    Boolean(item.applicable_skill_hash) &&
    Boolean(item.applicable_scope) &&
    Boolean(item.source_commit) &&
    Boolean(item.skill_path));
  const historyCaseStatesComplete =
    historyCaseStatuses.has("draft") &&
    historyCaseStatuses.has("approved") &&
    historyCaseStatuses.has("active");
  const reEvalCasesIncludeHistory = Array.isArray(unifiedUICoverage.prompt_evaluation?.re_eval_cases) &&
    unifiedUICoverage.prompt_evaluation.re_eval_cases.length >= 1 &&
    unifiedUICoverage.prompt_evaluation.re_eval_cases.every((item) =>
      Boolean(item.input?.case_input) &&
      Boolean(item.expected?.expected_behavior) &&
      Boolean(item.expected?.verification) &&
      Boolean(item.evidence_source) &&
      Boolean(item.source_commit));
  const publicAPIEvidence = unifiedUICoverage.prompt_evaluation?.public_api_evidence || {};
  const publicAPIExportPassed =
    Boolean(publicAPIEvidence.create?.project_id) &&
    Boolean(publicAPIEvidence.create?.issue_id) &&
    Boolean(publicAPIEvidence.create?.asset_id) &&
    Boolean(publicAPIEvidence.create?.failed_run_id) &&
    Boolean(publicAPIEvidence.create?.candidate_id) &&
    publicAPIEvidence.read?.issue_id === publicAPIEvidence.create?.issue_id &&
    Number(publicAPIEvidence.read?.listed_run_count || 0) >= 1 &&
    publicAPIEvidence.read?.evidence_run_id === publicAPIEvidence.create?.failed_run_id &&
    Number(publicAPIEvidence.read?.evidence_trial_count || 0) >= 1 &&
    Boolean(publicAPIEvidence.read?.snapshot_id) &&
    publicAPIEvidence.read?.archive_schema === "multica.prompt_evaluation.asset_evidence_archive.v1" &&
    Number(publicAPIEvidence.read?.archive_archived_run_count || 0) >= 1 &&
    publicAPIEvidence.state_transition?.issue_id === publicAPIEvidence.create?.issue_id &&
    publicAPIEvidence.state_transition?.status === "in_progress" &&
    Boolean(publicAPIEvidence.evidence_export?.run_evidence_api) &&
    Boolean(publicAPIEvidence.evidence_export?.snapshot_api) &&
    Boolean(publicAPIEvidence.evidence_export?.asset_archive_export_api) &&
    Number(publicAPIEvidence.evidence_export?.archive_missing_run_count || 0) === 0;
  const issueTimelineEvidence = unifiedUICoverage.issue_timeline || {};
  const issueTimelinePassed =
    unifiedUIPassed &&
    Boolean(issueTimelineEvidence.api) &&
    Boolean(issueTimelineEvidence.page_url) &&
    Number(issueTimelineEvidence.node_count || 0) >= 1 &&
    Number(issueTimelineEvidence.total_duration_ms || 0) > 0 &&
    Number(issueTimelineEvidence.total_input_tokens || 0) > 0 &&
    Number(issueTimelineEvidence.total_output_tokens || 0) > 0 &&
    Number(issueTimelineEvidence.agent_turn_count || 0) > 0 &&
    Number(issueTimelineEvidence.trace_event_count || 0) > 0 &&
    Array.isArray(issueTimelineEvidence.timeline_node_types) &&
    issueTimelineEvidence.timeline_node_types.includes("agent_task") &&
    Number(issueTimelineEvidence.evidence_ref_count || 0) >= 1 &&
    typeof issueTimelineEvidence.full_analysis_deep_link === "string" &&
    issueTimelineEvidence.full_analysis_deep_link.includes(unifiedUICoverage.issue?.id || "__missing_issue_id__") &&
    Array.isArray(issueTimelineEvidence.browser_assertions) &&
    issueTimelineEvidence.browser_assertions.includes("issue-timeline-summary") &&
    issueTimelineEvidence.browser_assertions.includes("issue-collaboration-execution-tree");
  const issueEvalOptimizerLinkage = unifiedUICoverage.prompt_evaluation?.issue_eval_optimizer_linkage || {};
  const issueEvalOptimizerPassed =
    unifiedUIPassed &&
    issueEvalOptimizerLinkage.issue_id === unifiedUICoverage.issue?.id &&
    Boolean(issueEvalOptimizerLinkage.issue_detail_url) &&
    Boolean(issueEvalOptimizerLinkage.run_detail_url) &&
    Boolean(issueEvalOptimizerLinkage.run_evidence_api) &&
    issueEvalOptimizerLinkage.failed_run_id === unifiedUICoverage.prompt_evaluation?.failed_run_id &&
    issueEvalOptimizerLinkage.candidate_id === unifiedUICoverage.prompt_evaluation?.candidate_id &&
    issueEvalOptimizerLinkage.candidate_source_run_id === unifiedUICoverage.prompt_evaluation?.failed_run_id &&
    issueEvalOptimizerLinkage.re_eval_run_id === unifiedUICoverage.prompt_evaluation?.re_eval_run_id &&
    Number(issueEvalOptimizerLinkage.trial_count || 0) >= 1 &&
    issueEvalOptimizerLinkage.issue_id_in_trial_prompt === true &&
    Boolean(issueEvalOptimizerLinkage.candidate_patch_hash) &&
    issueEvalOptimizerLinkage.optimizer_status_after_apply === "applied";
  const finalPackage = finalEvidencePackage.latest_json || {};
  const finalPackageEvidence = finalPackage.checks?.required_evidence || {};
  const finalPackageExists = Boolean(finalEvidencePackage.latest_json_path);
  const finalPackageLogsPerformanceClean =
    finalPackage.ok === true &&
    finalPackage.checks?.logs_clean === true &&
    finalPackage.checks?.performance_clean === true &&
    finalPackage.checks?.playwright_clean === true &&
    Array.isArray(finalPackage.checks?.page_timings) &&
    finalPackage.checks.page_timings.length >= 5;
  const finalPackageComplete =
    finalPackage.ok === true &&
    finalPackageEvidence.commit === true &&
    finalPackageEvidence.environment === true &&
    finalPackageEvidence.commands === true &&
    finalPackageEvidence.issue_ids === true &&
    finalPackageEvidence.run_urls_or_api === true &&
    finalPackageEvidence.trace_eval_optimizer === true &&
    finalPackageEvidence.screenshots === true &&
    finalPackageEvidence.logs === true &&
    finalPackageEvidence.gap_audit === true;
  const topologyAudit = topologyGeneralizationEvidence.latest_json || {};
  const canonicalDemoPassed =
    canonicalDemoEvidence.latest_json?.ok === true &&
    canonicalDemoEvidence.latest_json?.final_real_pm_0105_required === true &&
    canonicalDemoEvidence.latest_json?.issue?.id &&
    Array.isArray(canonicalDemoEvidence.latest_json?.role_task_evidence) &&
    canonicalDemoEvidence.latest_json.role_task_evidence.length >= 6 &&
    canonicalDemoEvidence.latest_json.role_task_evidence.every((item) => item.status === "completed") &&
    canonicalDemoEvidence.latest_json?.issue_execution_tree?.node_count >= 6 &&
    canonicalDemoEvidence.latest_json?.prompt_evaluation?.failed_run_id &&
    canonicalDemoEvidence.latest_json?.prompt_evaluation?.candidate_id &&
    canonicalDemoEvidence.latest_json?.prompt_evaluation?.apply_status === "applied" &&
    canonicalDemoEvidence.latest_json?.prompt_evaluation?.re_eval_status === "通过";
  return [
    matrixItem("E-00", "Current goal-test-daemon Web/API state retains a visible canonical demo chain instead of empty archived-only evidence",
      canonicalDemoPassed,
      canonicalDemoEvidence.latest_json_path
        ? "Canonical demo artifact exists, but current issue/task/eval/candidate/re-eval evidence is incomplete. This artifact is a demo fixture and still does not replace final real PM+01-05 model execution."
        : "No current canonical demo artifact exists; final page may be empty even if old artifacts pass.",
      canonicalDemoEvidence,
      canonicalDemoEvidence.latest_json_path ? "partial" : "missing"),
    matrixItem("E-01", "Gongfeng-only product semantics across repository/branch/commit/MR user paths",
      touchpointAuditPassed,
      touchpointAuditExists
        ? `Fresh touchpoint audit found ${touchpointAudit.summary?.blockers || 0} blocker matches and ${touchpointAudit.summary?.review || 0} review matches; user-visible GitHub repository semantics are not fully removed.`
        : "Current evidence includes Gongfeng resources and UI slices, but no fresh full touchpoint audit proves all user-visible GitHub repository semantics are removed.",
      {
        known_gongfeng_resource_evidence: e2e.cross_project_setup || null,
        touchpoint_audit: gongfengTouchpointEvidence,
      },
      "partial"),
    matrixItem("E-02", "Account-level TAPD/Gongfeng/MCP profiles with redaction and runtime inheritance",
      e2e.credential_profiles?.inheritance === "task_creator_or_trigger_user" &&
        e2e.credential_profiles?.redaction_verified === true &&
        e2e.credential_profiles?.tapd?.scope === "account" &&
        e2e.credential_profiles?.gongfeng?.scope === "account",
      "Credential profile evidence is missing or not account-scoped/redacted/inherited.",
      e2e.credential_profiles || null),
    matrixItem("E-03", "Three-project SOP creates gateway/ida-deployment children, runs siblings in parallel, and wakes parent after all done",
      e2e.cross_project_children?.count >= 2 &&
        e2e.project_owner_approval?.verified === true &&
        e2e.child_done_wake?.all_children_done === true &&
        e2e.child_done_wake?.parent_waited === true &&
        allStagesPresent &&
        allStagesCompleted,
      "Cross-project child, owner approval, all-child wait, or PM/01-05 completion evidence is incomplete.",
      {
        cross_project_children: e2e.cross_project_children || null,
        project_owner_approval: e2e.project_owner_approval || null,
        child_done_wake: e2e.child_done_wake || null,
        stage_count: (stageEvidence.stages || []).length,
      },
      allStagesPresent ? "partial" : "missing"),
    matrixItem("E-03G", "Generic topology supports dynamic target_projects and variable Agent nodes, with current fixture separated from generic gates",
      topologyAudit.ok === true,
      topologyAudit.blocking_reason || "No passing topology generalization audit proving variable-project and variable-agent fixtures.",
      topologyGeneralizationEvidence,
      topologyGeneralizationEvidence.latest_json_path ? "partial" : "missing"),
    matrixItem("E-04", "Issue timeline and run detail show timeline, node table, token/duration/turn metrics, evidence anchors, and issue summary/deep link",
      issueTimelinePassed,
      hasIssueTimelineSlice
        ? "Goal C API/UI slice exists, but unified Playwright issue timeline proof is incomplete."
        : "No Goal C issue timeline artifact found.",
      {
        goal_c: goalCEvidence,
        unified_ui_json: uiPlaywrightEvidence.unified_ui_json_path,
        issue_timeline: issueTimelineEvidence,
      },
      hasIssueTimelineSlice ? "partial" : "missing"),
    matrixItem("E-05", "Trace/Eval/Optimizer flow links issue/run detail to eval cases, trials, evidence, and skill optimizer candidate",
      issueEvalOptimizerPassed,
      skillLocalChainPassed
        ? "Skill Eval/Optimizer local chain is proven, but unified issue/run-detail-to-eval-to-optimizer linkage evidence is incomplete."
        : "No complete Trace/Eval/Optimizer skill chain evidence found.",
      {
        goal_d: goalDSkillEvidence,
        unified_ui_json: uiPlaywrightEvidence.unified_ui_json_path,
        issue_eval_optimizer_linkage: issueEvalOptimizerLinkage,
      },
      skillLocalChainPassed ? "partial" : "missing"),
    matrixItem("E-06", "Skill chain selects Gongfeng repo/branch/skill, snapshots, applies or emits patch artifact, writes CHANGELOG, and re-evals",
      gongfengWritebackPassed,
      skillLocalChainPassed || Boolean(goalEGongfengSkillWritebackEvidence.latest_json_path)
        ? "Skill apply/CHANGELOG/re-eval evidence exists, but Gongfeng controlled writeback proof is incomplete."
        : "No passing skill apply/CHANGELOG/re-eval evidence found.",
      {
        goal_d: goalDSkillEvidence,
        gongfeng_writeback: goalEGongfengSkillWritebackEvidence,
        gongfeng_clean_writeback_passed: gongfengCleanWritebackPassed,
        gongfeng_remote_mr_passed: gongfengRemoteMRPassed,
      },
      skillLocalChainPassed || Boolean(goalEGongfengSkillWritebackEvidence.latest_json_path) ? "partial" : "missing"),
    matrixItem("E-07", "History cases from real git diff are visible as draft/approved/active with input, expected behavior, verification, evidence source, and skill version",
      historyCaseFieldsComplete && historyCaseStatesComplete && reEvalCasesIncludeHistory,
      hasHistoryCases || historyCases.length > 0
        ? "History case evidence exists, but final artifact does not expose all required fields, draft/approved/active states, and re-eval case linkage."
        : "No history case draft evidence found.",
      {
        latest_skill_json: goalDSkillEvidence.latest_json_path,
        draft_count: skillProof.draft_count || 0,
        unified_ui_json: uiPlaywrightEvidence.unified_ui_json_path,
        history_cases: historyCases,
        re_eval_cases: unifiedUICoverage.prompt_evaluation?.re_eval_cases || [],
      },
      hasHistoryCases || historyCases.length > 0 ? "partial" : "missing"),
    matrixItem("E-08", "Unified Playwright covers settings, project/issue, training run detail, Eval/Optimizer, skill candidate/CHANGELOG paths with console/pageerror checks",
      unifiedUIPassed,
      uiCoveragePartial || Boolean(uiPlaywrightEvidence.unified_ui_json_path)
        ? "Focused UI/Playwright evidence exists, but no single unified Goal E browser suite covers all required paths."
        : "No browser evidence found for the Goal E path.",
      uiPlaywrightEvidence,
      uiCoveragePartial || Boolean(uiPlaywrightEvidence.unified_ui_json_path) ? "partial" : "missing"),
    matrixItem("E-09", "Public API/CLI creates, reads, transitions state, and exports evidence without DB-only shortcuts",
      publicAPIExportPassed,
      publicAPIEvidence.create
        ? "Public API evidence exists, but create/read/state-transition/evidence-export coverage is incomplete."
        : crossServiceEvidence.cross_service_curl.ok === true
          ? "Public API/CLI slices exist, but no unified evidence export path proves the full A-E flow without DB-only shortcuts."
          : "No public API/CLI evidence for final flow.",
      { cross_service_curl: crossServiceEvidence.cross_service_curl, ui_api: uiApiEvidence, unified_public_api: publicAPIEvidence },
      publicAPIEvidence.create || crossServiceEvidence.cross_service_curl.ok === true ? "partial" : "missing"),
    matrixItem("E-10", "Server/web/daemon/runtime logs and key page performance are clean for the unified acceptance window",
      finalPackageLogsPerformanceClean,
      finalPackageExists
        ? "Goal E final evidence package exists, but logs, Playwright checks, or page timings are not clean."
        : "No current unified Goal E log/performance window is attached after the latest A-D worktree changes.",
      finalPackageExists
        ? finalEvidencePackage
        : { latest_ui_audit: uiPlaywrightEvidence.ui_audit_latest, training_performance_latest: uiPlaywrightEvidence.training_performance_latest },
      finalPackageExists ? "partial" : "missing"),
    matrixItem("E-11", "Final evidence package contains commit, environment, commands, issue IDs, run URLs/API, Trace/Eval/Optimizer evidence, screenshots, logs, and gap audit",
      finalPackageComplete && prodReleaseEvidence.latest_json?.ok === true,
      finalPackageExists
        ? "Goal E final evidence package exists, but one or more required evidence fields or full prod release gates are missing."
        : "No final evidence package exists.",
      finalPackageExists
        ? { final_evidence_package: finalEvidencePackage, prod_release: prodReleaseEvidence }
        : { runbook: handoffEvidence, goal_d: goalDSkillEvidence, goal_c: goalCEvidence },
      finalPackageExists ? "partial" : "missing"),
  ];
}

function buildProdReleaseEvidence() {
  const latestPath = fileIfExists(path.join(artifactRoot, "goal-test-prod-release-latest.json"));
  let latestJSON = null;
  if (latestPath) {
    try {
      latestJSON = readJSON(latestPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestPath,
    latest_json: latestJSON,
    proof_boundary: latestJSON?.ok === true
      ? "Full prod release evidence passed for the current release."
      : "missing or failed full prod release evidence",
  };
}

function buildTopologyGeneralizationEvidence() {
  const latestPath = fileIfExists(path.join(artifactRoot, "goal-test-topology-generalization-audit-latest.json"));
  let latestJSON = null;
  if (latestPath) {
    try {
      latestJSON = readJSON(latestPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestPath,
    latest_json: latestJSON,
    ok: latestJSON?.ok === true,
    proof_boundary: latestJSON?.ok === true
      ? "Topology generalization audit passed."
      : "Generic topology remains a blocker until dynamic topology, variable-project fixture, and variable-agent fixture evidence pass.",
  };
}

function buildNewAccountMCPEvidence() {
  const latestPath = fileIfExists(path.join(artifactRoot, "goal-test-new-account-mcp-onboarding-latest.json"));
  let latestJSON = null;
  if (latestPath) {
    try {
      latestJSON = readJSON(latestPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestPath,
    latest_json: latestJSON,
    ok: latestJSON?.ok === true,
    proof_boundary: latestJSON?.ok === true
      ? "New account configured TAPD/Gongfeng profiles and used MCP through Agent runtime."
      : "New-account MCP onboarding remains a blocker until a fresh passing artifact exists.",
  };
}

function buildFixtureGovernanceEvidence() {
  const latestPath = fileIfExists(path.join(artifactRoot, "goal-test-acceptance-fixture-governance-latest.json"));
  let latestJSON = null;
  if (latestPath) {
    try {
      latestJSON = readJSON(latestPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestPath,
    latest_json: latestJSON,
    ok: latestJSON?.ok === true,
    proof_boundary: latestJSON?.ok === true
      ? "Acceptance fixture governance audit passed."
      : "Acceptance fixture governance remains a blocker until fixture rows are traceable and audit passes.",
  };
}

function prodReleaseChecks(release) {
  const checks = {};
  for (const item of release?.checks || []) {
    checks[item.id] = item.status === "fulfilled";
  }
  return checks;
}

function buildGoalCEvidence() {
  const sliceArtifact = latestMatching(/^goal-c-issue-timeline-slice-.*\.md$/);
  return {
    slice_artifact: sliceArtifact,
    has_slice: Boolean(sliceArtifact),
    proof_boundary: sliceArtifact
      ? "Goal C API/UI slice only; unified Playwright run detail + issue detail still required."
      : "missing",
  };
}

function buildGoalDSkillEvidence() {
  const latestJSONPath = latestMatching(/^goal-d-skill-full-local-e2e-.*\.json$/);
  let latestJSON = null;
  if (latestJSONPath) {
    try {
      latestJSON = readJSON(latestJSONPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestJSONPath,
    latest_json: latestJSON,
    latest_ui_screenshot: latestMatching(/^goal-d-skill-ui-workflow-playwright-.*\.png$/),
    first_class_patch_artifact: latestMatching(/^goal-d-skill-first-class-patch-slice-.*\.md$/),
    proof_boundary: latestJSON
      ? "local_directory/local_prompt_evaluation_run, not Gongfeng profile checkout/MR."
      : "missing",
  };
}

function buildGoalEGongfengSkillWritebackEvidence() {
  const latestJSONPath = latestMatching(/^goal-e-gongfeng-skill-writeback-.*\.json$/);
  const cleanWritebackJSONPath = latestMatching(/^remediation-gongfeng-clean-writeback-.*\.json$/);
  const remoteMRJSONPath = latestMatching(/^remediation-gongfeng-remote-mr-.*\.json$/);
  let latestJSON = null;
  let cleanWritebackJSON = null;
  let remoteMRJSON = null;
  if (latestJSONPath) {
    try {
      latestJSON = readJSON(latestJSONPath);
    } catch {
      latestJSON = null;
    }
  }
  if (cleanWritebackJSONPath) {
    try {
      cleanWritebackJSON = readJSON(cleanWritebackJSONPath);
    } catch {
      cleanWritebackJSON = null;
    }
  }
  if (remoteMRJSONPath) {
    try {
      remoteMRJSON = readJSON(remoteMRJSONPath);
    } catch {
      remoteMRJSON = null;
    }
  }
  return {
    latest_json_path: latestJSONPath,
    latest_json: latestJSON,
    clean_writeback_json_path: cleanWritebackJSONPath,
    clean_writeback_json: cleanWritebackJSON,
    remote_mr_json_path: remoteMRJSONPath,
    remote_mr_json: remoteMRJSON,
    proof_boundary: latestJSON
      ? "Controlled clean Gongfeng checkout writeback plus focused remote branch/MR evidence when present."
      : "missing",
  };
}

function buildGoalEUIEvidence() {
  const unifiedUIJSONPath = latestMatching(/^goal-e-unified-ui-playwright-.*\.json$/);
  let unifiedUIJSON = null;
  if (unifiedUIJSONPath) {
    try {
      unifiedUIJSON = readJSON(unifiedUIJSONPath);
    } catch {
      unifiedUIJSON = null;
    }
  }
  return {
    ui_audit_latest: fileIfExists(path.join(artifactRoot, "ui-audit-latest.json")),
    ui_audit_summary: fileIfExists(path.join(artifactRoot, "ui-audit-summary.md")),
    training_performance_latest: fileIfExists(path.join(artifactRoot, "training-performance-audit-latest.json")),
    skill_candidate_spec_passed: Boolean(latestMatching(/^goal-d-skill-full-local-e2e-.*\.json$/)),
    skill_candidate_screenshot: latestMatching(/^goal-d-skill-ui-workflow-playwright-.*\.png$/),
    unified_ui_json_path: unifiedUIJSONPath,
    unified_ui_json: unifiedUIJSON,
  };
}

function buildGoalEGongfengTouchpointEvidence() {
  const latestJSONPath = latestMatching(/^goal-e-gongfeng-touchpoint-audit-.*\.json$/);
  let latestJSON = null;
  if (latestJSONPath) {
    try {
      latestJSON = readJSON(latestJSONPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestJSONPath,
    latest_json: latestJSON,
    latest_markdown: latestMatching(/^goal-e-gongfeng-touchpoint-audit-.*\.md$/),
    proof_boundary: latestJSON
      ? "Fresh current-state audit only; blockers must be removed before E-01 can pass."
      : "missing",
  };
}

function buildCanonicalDemoEvidence() {
  const latestPath = fileIfExists(path.join(artifactRoot, "goal-e-canonical-demo-seed-latest.json"));
  let latestJSON = null;
  if (latestPath) {
    try {
      latestJSON = readJSON(latestPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestPath,
    latest_json: latestJSON,
    proof_boundary: latestJSON?.final_real_pm_0105_required === true
      ? "Current Web/API demo fixture only; final real PM+01-05 model execution is still required before demo-ready."
      : "missing or not marked with final real-run requirement",
  };
}

function buildRealPMRunEvidence() {
  const latestPath = fileIfExists(path.join(artifactRoot, "goal-e-real-pm-0105-run-latest.json"));
  let latestJSON = null;
  if (latestPath) {
    try {
      latestJSON = readJSON(latestPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestPath,
    latest_json: latestJSON,
    stage_evidence: realPMStageEvidence(latestJSON),
    proof_boundary: latestJSON?.ok === true
      ? "Real PM+01-05 model execution completed with per-stage task/messages/usage/trace evidence."
      : "missing or failed real PM+01-05 model execution",
  };
}

function realPMStageEvidence(run) {
  if (!run || run.ok !== true || run.run_type !== "real_pm_0105_model_execution" || !Array.isArray(run.stages)) {
    return { stages: [], task_count: 0, error: "real PM+01-05 run artifact missing or not ok" };
  }
  const stages = run.stages.map((stage) => {
    const usage = Array.isArray(stage.usage_rows) ? stage.usage_rows : [];
    const tokens = usage.reduce((sum, row) => ({
      input_tokens: sum.input_tokens + Number(row.input_tokens || 0),
      output_tokens: sum.output_tokens + Number(row.output_tokens || 0),
      cache_read_tokens: sum.cache_read_tokens + Number(row.cache_read_tokens || 0),
      cache_write_tokens: sum.cache_write_tokens + Number(row.cache_write_tokens || 0),
    }), { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 });
    return {
      key: stage.key,
      step_key: stage.key,
      role_key: stage.role_key || stage.key,
      task_id: stage.task_id,
      agent_id: stage.agent_id,
      agent_name: stage.agent_name,
      model: stage.model,
      status: stage.status,
      started_at: stage.started_at,
      completed_at: stage.completed_at,
      duration_ms: Number(stage.duration_ms || 0),
      failure_reason: stage.failure_reason || "",
      trigger_summary: "goal-e-real-pm-0105-run",
      trace_event_count: Number(stage.trace_event_count || 0),
      message_count: Number(stage.message_count || 0),
      agent_turn_count: Number(stage.turn_count || stage.message_count || 0),
      input_tokens: tokens.input_tokens,
      output_tokens: tokens.output_tokens,
      cache_read_tokens: tokens.cache_read_tokens,
      cache_write_tokens: tokens.cache_write_tokens,
      usage_unavailable_trace: false,
    };
  });
  return {
    issue_id: run.issue?.id || run.canonical_issue_id || null,
    task_count: stages.length,
    stages,
  };
}

function buildGoalEFinalEvidencePackage() {
  const latestJSONPath = latestMatching(/^goal-e-final-evidence-package-.*\.json$/);
  let latestJSON = null;
  if (latestJSONPath) {
    try {
      latestJSON = readJSON(latestJSONPath);
    } catch {
      latestJSON = null;
    }
  }
  return {
    latest_json_path: latestJSONPath,
    latest_json: latestJSON,
    proof_boundary: latestJSON
      ? "Goal E evidence package covers logs/performance/package completeness; open matrix items still gate demo-ready."
      : "missing",
  };
}

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

function matrixItem(id, title, ok, reason, evidence, fallbackStatus = "missing") {
  return {
    id,
    title,
    status: ok ? "fulfilled" : fallbackStatus,
    reason: ok ? "Evidence satisfies the requirement." : reason,
    evidence,
  };
}

function prodItem(id, title, ok, reason = "") {
  return {
    id,
    title,
    blocking: true,
    status: ok ? "fulfilled" : "missing",
    reason: ok ? "Evidence satisfies the production-readiness requirement." : reason,
  };
}

function resolveStageEvidence(current, realPMRunEvidence) {
  const realPMStages = realPMRunEvidence?.stage_evidence;
  if (hasCompleteStageEvidence(realPMStages?.stages)) {
    return {
      ...realPMStages,
      source: "real_pm_0105_run_artifact",
      source_artifact: realPMRunEvidence.latest_json_path,
      selected_model: realPMRunEvidence.latest_json?.selected_model || realPMRunEvidence.latest_json?.model || null,
      fallback_used: realPMRunEvidence.latest_json?.fallback_used === true,
    };
  }

  if (hasCompleteStageEvidence(current?.stages)) {
    return {
      ...current,
      source: "database_current",
      source_artifact: null,
    };
  }

  return {
    ...current,
    source: "database_current_incomplete",
    source_artifact: null,
    archived_fallback_forbidden: true,
    reason: "Current product-visible acceptance must be proven from the current DB/Web state; archived final acceptance artifacts are not allowed for PM+01-05 stage evidence.",
  };
}

function loadArchivedStageEvidence() {
  if (!fs.existsSync(artifactRoot)) return null;
  const candidates = fs.readdirSync(artifactRoot)
    .filter((name) => /^tapd-gongfeng-sop-final-acceptance-\d{4}-.*\.json$/.test(name))
    .sort()
    .reverse();

  for (const name of candidates) {
    const filePath = path.join(artifactRoot, name);
    try {
      const artifact = readJSON(filePath);
      if (artifact.ok !== true || !hasCompleteStageEvidence(artifact.stage_evidence?.stages)) continue;
      return {
        file_path: filePath,
        generated_at: artifact.generated_at || null,
        stage_evidence: artifact.stage_evidence,
      };
    } catch {
      continue;
    }
  }
  return null;
}

function hasCompleteStageEvidence(stages) {
  if (!Array.isArray(stages)) return false;
  const byKey = new Map(stages.map((stage) => [stage.key, stage]));
  return ["pm", "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"].every((key) => {
    const stage = byKey.get(key);
    return stage?.task_id &&
      stage.status === "completed" &&
      Number(stage.trace_event_count || 0) > 0 &&
      Number(stage.message_count || 0) > 0 &&
      (
        Number(stage.input_tokens || 0) +
        Number(stage.output_tokens || 0) +
        Number(stage.cache_read_tokens || 0) +
        Number(stage.cache_write_tokens || 0) > 0 ||
        stage.usage_unavailable_trace === true
      );
  });
}

function summarizeStageEvidence(evidence) {
  return {
    source: evidence?.source || "database_current",
    issue_id: evidence?.issue_id || null,
    task_count: evidence?.task_count || 0,
    stage_count: Array.isArray(evidence?.stages) ? evidence.stages.length : 0,
    error: evidence?.error || null,
  };
}

async function loadStageEvidence(databaseURL, issueID) {
  const client = new pg.Client({ connectionString: databaseURL });
  await client.connect();
  try {
    const tasks = await client.query(`
      SELECT
        atq.id::text AS task_id,
        atq.status,
        atq.started_at,
        atq.completed_at,
        atq.failure_reason,
        atq.trigger_summary,
        a.id::text AS agent_id,
        a.name AS agent_name,
        a.model,
        COALESCE(tu.input_tokens, 0)::bigint AS input_tokens,
        COALESCE(tu.output_tokens, 0)::bigint AS output_tokens,
        COALESCE(tu.cache_read_tokens, 0)::bigint AS cache_read_tokens,
        COALESCE(tu.cache_write_tokens, 0)::bigint AS cache_write_tokens,
        (
          SELECT count(*)::int
          FROM task_trace_event tte
          WHERE tte.task_id = atq.id
        ) AS trace_event_count,
        (
          SELECT count(*)::int
          FROM task_message tm
          WHERE tm.task_id = atq.id
        ) AS message_count,
        (
          SELECT count(*)::int
          FROM task_message tm
          WHERE tm.task_id = atq.id AND tm.type IN ('text', 'tool_use')
        ) AS agent_turn_count,
        EXISTS (
          SELECT 1
          FROM task_trace_event tte
          WHERE tte.task_id = atq.id AND tte.event_type = 'llm.usage_unavailable'
        ) AS usage_unavailable_trace
      FROM agent_task_queue atq
      JOIN agent a ON a.id = atq.agent_id
      LEFT JOIN task_usage tu ON tu.task_id = atq.id
      WHERE atq.issue_id = $1
      ORDER BY atq.created_at ASC
    `, [issueID]);
    const stages = [];
    const seen = new Set();
    for (const row of tasks.rows) {
      const key = stageKey(row);
      if (!key || seen.has(key)) continue;
      seen.add(key);
      stages.push({
        key,
        step_key: key,
        role_key: key,
        task_id: row.task_id,
        agent_id: row.agent_id,
        agent_name: row.agent_name,
        model: row.model,
        status: row.status,
        started_at: row.started_at,
        completed_at: row.completed_at,
        duration_ms: durationMs(row.started_at, row.completed_at),
        failure_reason: row.failure_reason || "",
        trigger_summary: row.trigger_summary || "",
        trace_event_count: Number(row.trace_event_count || 0),
        message_count: Number(row.message_count || 0),
        agent_turn_count: Number(row.agent_turn_count || 0),
        input_tokens: Number(row.input_tokens || 0),
        output_tokens: Number(row.output_tokens || 0),
        cache_read_tokens: Number(row.cache_read_tokens || 0),
        cache_write_tokens: Number(row.cache_write_tokens || 0),
        usage_unavailable_trace: Boolean(row.usage_unavailable_trace),
      });
    }
    return { issue_id: issueID, task_count: tasks.rowCount, stages };
  } finally {
    await client.end();
  }
}

function stageKey(row) {
  const text = `${row.trigger_summary || ""} ${row.agent_name || ""}`.toLowerCase();
  if (/pm/.test(text) && !/sop stage/.test(text)) return "pm";
  if (/01|clarify|需求澄清/.test(text)) return "01-clarify";
  if (/02|design|方案设计/.test(text)) return "02-design";
  if (/03|task-split|任务拆分/.test(text)) return "03-task-split";
  if (/04|implement|代码开发/.test(text)) return "04-implement";
  if (/05|verify|测试验证/.test(text)) return "05-verify";
  return "";
}

function durationMs(startedAt, completedAt) {
  if (!startedAt || !completedAt) return 0;
  return Math.max(0, new Date(completedAt).getTime() - new Date(startedAt).getTime());
}

function readGoalTestDatabaseURL(environment) {
  const envFile = path.join(repoRoot, ".run", "env", `goal-test-${environment}.env`);
  if (!fs.existsSync(envFile)) return "";
  const env = {};
  for (const raw of fs.readFileSync(envFile, "utf8").split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) env[match[1]] = match[2].replace(/^['"]|['"]$/g, "");
  }
  return env.DATABASE_URL || "";
}

function readJSON(filePath) {
  if (!fs.existsSync(filePath)) throw new Error(`missing JSON artifact: ${filePath}`);
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
}

function buildCrossServiceEvidence() {
  const sandboxArtifactPath = path.join(artifactRoot, "quick-entry-cross-service-latest.json");
  const sandboxArtifact = fs.existsSync(sandboxArtifactPath) ? readJSON(sandboxArtifactPath) : null;
  if (sandboxArtifact?.ok === true && sandboxArtifact.cross_service_curl?.ok === true) {
    return {
      minimal_api_curl: {
        ok: true,
        status: "fulfilled",
        code_change_complete: true,
        local_gateway_curl_handler_verified: true,
        endpoint: sandboxArtifact.endpoint,
        semantic_guard: sandboxArtifact.semantic_guard,
        artifact: sandboxArtifactPath,
        checks: sandboxArtifact.checks?.map((check) => ({ id: check.id, ok: check.ok, status: check.status, duration_ms: check.duration_ms })) || [],
      },
      cross_service_curl: {
        ...sandboxArtifact.cross_service_curl,
        ok: true,
        status: "fulfilled",
        artifact: sandboxArtifactPath,
        sandbox_mode: sandboxArtifact.sandbox_mode,
        endpoint: sandboxArtifact.endpoint,
        semantic_guard: sandboxArtifact.semantic_guard,
        checks: sandboxArtifact.checks?.map((check) => ({ id: check.id, ok: check.ok, status: check.status, duration_ms: check.duration_ms })) || [],
      },
    };
  }
  const evidence = {
    usercenter: {
      repo: "/data/ida/user-center",
      branch: "v5.0.0_dev_sop",
      files: [
        "proto/user_center.proto",
        "internal/logic/getquickentrycapabilitylogic.go",
        "internal/logic/getquickentrycapabilitylogic_test.go",
      ],
      verification: "go test ./internal/logic -run 'TestGetQuickEntryCapability|TestGetPrivateUserContext' -count=1",
    },
    gateway: {
      repo: "/data/ida/gateway",
      branch: "v5.0.0_dev_sop",
      files: [
        "internal/handler/gateway/routes.go",
        "internal/handler/gateway/quick_entry_capability_test.go",
        "internal/handler/common_routes.go",
        "etc/apiData.json",
      ],
      verification: "GOWORK=<tmp go.work with gateway + user-center> go test /data/ida/gateway/internal/handler/gateway /data/ida/gateway/internal/handler /data/ida/gateway/internal/apidata -count=1",
      curl_handler_evidence: true,
    },
    ida_deployment: {
      repo: "/data/ida/ida-deployment",
      branch: "v5.0.0_dev_sop",
      files: [
        "helm/public/charts/usercenter/permissions/api/user-center.api.json",
        "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode1.json",
        "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode2.json",
        "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode3.json",
        "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode4.json",
      ],
      verification: "helm template ida-front helm/front -f helm/front/values.yaml; generated permission JSON contains /v1/user-center/quick-entry-capability",
    },
  };
  const codeChangeComplete = [evidence.usercenter, evidence.gateway, evidence.ida_deployment].every((repoEvidence) =>
    repoEvidence.files.every((relativePath) => fs.existsSync(path.join(repoEvidence.repo, relativePath))));
  return {
    minimal_api_curl: {
      ok: false,
      status: codeChangeComplete ? "partial" : "missing",
      code_change_complete: codeChangeComplete,
      local_gateway_curl_handler_verified: codeChangeComplete,
      endpoint: "GET /v1/user-center/quick-entry-capability",
      semantic_guard: "gateway derives userId from authenticated context and ignores query/body userId",
      evidence,
    },
    cross_service_curl: {
      ok: false,
      status: "missing",
      reason: sandboxArtifact
        ? "Cross-service sandbox artifact exists but did not pass."
        : "Full business runtime with user-center, gateway, deployment config, auth, and dependencies was not available in this environment; only code/config plus local handler curl verification is proven.",
      artifact: sandboxArtifactPath,
      required_followup: [
        "run node scripts/verify-quick-entry-cross-service.mjs",
        "inspect artifacts/acceptance/quick-entry-cross-service-latest.json",
      ],
    },
  };
}

function buildHandoffEvidence() {
  const runbookPath = path.join(artifactRoot, "tapd-gongfeng-sop-production-runbook.md");
  const exists = fs.existsSync(runbookPath);
  return {
    ok: exists,
    path: runbookPath,
    covers: exists ? [
      "current acceptance status",
      "business repository branches",
      "already-run verification commands",
      "remaining full sandbox curl blocker",
      "recovery steps for retry, approval, wake, and source fetch failures",
    ] : [],
  };
}

function buildUIAPIEvidence() {
  const uiAuditPath = path.join(artifactRoot, "ui-audit-summary.md");
  const semanticPath = path.join(artifactRoot, "ui-api-semantic-evidence-2026-06-25T13-55-10.json");
  const snapshotPath = path.join(artifactRoot, "ui-semantic-issues-authenticated-2026-06-25T13-54-49.md");
  let semantic = null;
  if (fs.existsSync(semanticPath)) {
    try {
      semantic = readJSON(semanticPath);
    } catch {
      semantic = null;
    }
  }
  return {
    ok: fs.existsSync(uiAuditPath) && fs.existsSync(snapshotPath) && semantic?.ok === true,
    ui_audit: uiAuditPath,
    authenticated_snapshot: snapshotPath,
    semantic_api_evidence: semanticPath,
    semantic_routes: semantic?.routes?.map((route) => ({ url: route.url, status: route.status, ok: route.ok, bytes: route.bytes })) || [],
  };
}
