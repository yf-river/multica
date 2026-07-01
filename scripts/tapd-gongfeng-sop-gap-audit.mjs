#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const defaultArtifact = path.join(artifactRoot, "tapd-gongfeng-sop-final-acceptance-latest.json");

const args = parseArgs(process.argv.slice(2));
const artifactPath = path.resolve(args.artifact || defaultArtifact);
const outputDir = path.resolve(args.outputDir || artifactRoot);
const now = new Date().toISOString();

const artifact = readJSON(artifactPath);
const e2e = artifact.clean_acceptance?.e2e || artifact.e2e || {};
const topologyGeneralization = artifact.topology_generalization?.latest_json || artifact.topology_generalization || {};
const topLevelOpenItems = acceptanceOpenItems(artifact);
const goalERequirements = Array.isArray(artifact.goal_e_requirement_matrix) && artifact.goal_e_requirement_matrix.length > 0
  ? artifact.goal_e_requirement_matrix
  : [{
      id: "E-00",
      title: "Goal E requirement matrix is present in final acceptance artifact",
      status: "missing",
      reason: "Final acceptance artifact does not include goal_e_requirement_matrix; old artifacts cannot prove unified Goal E acceptance.",
      evidence: { artifact_path: artifactPath },
    }];

const requirements = [
  check("P0-00", "latest final acceptance artifact is passing before gap audit can pass", () => {
    if (artifact.ok === true && topLevelOpenItems.length === 0) {
      return fulfilled({ artifact_ok: true, open_items: [] });
    }
    return missing("Latest final acceptance artifact is not passing; gap audit cannot override or mask failed acceptance.", {
      artifact_ok: artifact.ok === true,
      open_items: topLevelOpenItems.map(openItemSummary),
    });
  }),
  check("P0-01", "TAPD document content is fetched through MCP and used by PM", () => {
    const source = e2e.tapd_source || artifact.tapd_source || {};
    const hasMetadata = Boolean(source.persisted || source.metadata?.source_provider === "tapd");
    const fetched = source.fetched_content || source.fetch || source.mcp_fetch || artifact.tapd_mcp_fetch;
    const title = fetched?.title || source.title || artifact.tapd_document?.title;
    const body = fetched?.body || fetched?.markdown || fetched?.body_excerpt || source.body || source.markdown || artifact.tapd_document?.body;
    const mcpBacked = /mcp/i.test(String(fetched?.provider || fetched?.source || artifact.tapd_document?.provider || source.fetch?.provider || ""));
    if (title && body && mcpBacked) return fulfilled({ title_present: true, body_present: true, mcp_backed: true });
    if (hasMetadata) {
      return falseClaimed("TAPD URL metadata exists, but there is no MCP-fetched title/body/markdown evidence.", {
        metadata: source.metadata || source,
      });
    }
    return missing("No TAPD source evidence found.");
  }),
  check("P0-02", "goal-test exposes TAPD MCP as an Agent/Squad source capability", () => {
    const sourceFetch = artifact.tapd_mcp || artifact.source_fetch || artifact.capabilities?.tapd_mcp || e2e.tapd_source?.source_fetch_trace_events;
    if (Array.isArray(sourceFetch) && sourceFetch.length > 0) {
      return fulfilled({ source_fetch_trace_events: sourceFetch.length });
    }
    if (sourceFetch?.enabled && sourceFetch?.agent_runtime_available) return fulfilled(sourceFetch);
    return missing("No evidence that Agent/Squad runtime can call TAPD MCP or records source_fetch_failed.");
  }),
  check("P0-03", "account-level TAPD/Gongfeng credential profiles are implemented and inherited", () => {
    const profiles = artifact.credential_profiles || artifact.credentials || e2e.credential_profiles || {};
    const tapd = profiles.tapd || profiles.TAPD;
    const gongfeng = profiles.gongfeng || profiles.Gongfeng;
    const ok =
      tapd?.scope === "account" &&
      gongfeng?.scope === "account" &&
      profiles.inheritance === "task_creator_or_trigger_user" &&
      profiles.redaction_verified === true;
    if (ok) return fulfilled({ tapd, gongfeng, inheritance: profiles.inheritance });
    return missing("No account-level TAPD/Gongfeng credential profile evidence with redaction and task creator inheritance.");
  }),
  check("P0-04", "Gongfeng repository links are resolved through Gongfeng MCP and injected into Agent context", () => {
    const resource = artifact.gongfeng_mcp || artifact.gongfeng_resource || artifact.project_resources?.gongfeng_repo;
    if (resource?.mcp_resolved && resource?.project_path && resource?.head_commit && resource?.agent_context_injected) {
      return fulfilled(resource);
    }
    const setupResource = e2e.cross_project_setup?.usercenter?.resources?.find?.((item) => item.resource_type === "gongfeng_repo");
    if (setupResource?.resource_ref?.project_path && setupResource?.resource_ref?.ref) {
      return partial("Gongfeng repo is attached as project resource, but final artifact still needs MCP head_commit/context injection evidence.", setupResource);
    }
    const hasStatic = Boolean(resource?.url || artifact.repositories?.some?.((item) => item.key === "user-center"));
    return hasStatic
      ? falseClaimed("Gongfeng/static repository evidence exists, but no MCP resolution/head commit/context injection evidence.")
      : missing("No Gongfeng MCP resolution evidence found.");
  }),
  check("P0-05", "PM runs real 01-05 SOP stages as separate tasks/traces", () => {
    const stages = artifact.sop_stages || artifact.stage_tasks || e2e.sop_stages || [];
    const required = ["pm", "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"];
    const keys = new Set(Array.isArray(stages) ? stages.map((item) => item.step_key || item.role_key || item.key) : []);
    const missingKeys = required.filter((key) => !keys.has(key));
    const allHaveTask = Array.isArray(stages) && required.every((key) => {
      const stage = stages.find((item) => [item.step_key, item.role_key, item.key].includes(key));
      return stage?.task_id && stage?.trace_event_count > 0;
    });
    const failedStages = Array.isArray(stages) ? stages.filter((item) => required.includes(item.step_key || item.role_key || item.key) && item.status !== "completed") : [];
    if (missingKeys.length === 0 && allHaveTask && failedStages.length === 0) return fulfilled({ stages });
    if (missingKeys.length === 0 && allHaveTask && failedStages.length > 0) {
      return partial("PM/01-05 stage tasks and traces exist, but one or more stages did not complete successfully.", { failed_stages: failedStages, stages });
    }
    if (e2e.internal_template?.role_keys?.length >= 6) {
      return falseClaimed("The artifact proves only that six template roles exist, not that PM executed 01-05 as real tasks.", {
        template_roles: e2e.internal_template.role_keys,
        missing_stage_tasks: missingKeys,
      });
    }
    return missing(`Missing real SOP stage task evidence for: ${missingKeys.join(", ") || "stage trace/task ids"}.`);
  }),
  check("P0-06", "06 skill is removed or its duties are migrated to 05-verify", () => {
    const audit = artifact.skill_audit || artifact.sop_skill_audit || {};
    if (audit.archive_skill_removed === true || audit.archive_duties_migrated_to_verify === true) return fulfilled(audit);
    return missing("No evidence that legacy 06/archive skill was removed or migrated into 05-verify.");
  }),
  check("P0-07", "cross-project parent/child issues are created for gateway and ida-deployment", () => {
    const children = e2e.cross_project_children || {};
    const ok = children.count >= 2 && children.gateway?.project_id && children.deployment?.project_id;
    return ok ? fulfilled(children) : missing("No gateway + ida-deployment child issue evidence.");
  }),
  check("P0-08", "child issues start in backlog, assigned to target SOP squad, and notify project owner", () => {
    const children = e2e.cross_project_children || {};
    const childItems = [children.gateway, children.deployment].filter(Boolean);
    if (childItems.length < 2) return missing("No two child issues to verify backlog/project-owner notification.");
    const badStatus = childItems.filter((item) => item.status !== "backlog");
    const badAssignee = childItems.filter((item) => item.assignee_type !== "squad");
    const ownerNotice = artifact.project_owner_notifications || e2e.project_owner_notifications;
    if (badStatus.length === 0 && badAssignee.length === 0 && ownerNotice?.verified === true) {
      return fulfilled({ children: childItems, owner_notice: ownerNotice });
    }
    return falseClaimed("Child issues are not proven to be backlog + SOP squad + project owner inbox/review notification.", {
      child_statuses: childItems.map((item) => ({ id: item.id, status: item.status, assignee_type: item.assignee_type })),
      owner_notice: ownerNotice || null,
    });
  }),
  check("P0-09", "owner approval moves child backlog to todo before SOP squad executes", () => {
    const approval = artifact.project_owner_approval || e2e.project_owner_approval;
    if (approval?.verified && approval?.backlog_to_todo && approval?.squad_started_after_approval) return fulfilled(approval);
    return missing("No member inbox or agent review/approval task evidence proving backlog -> todo before SOP squad execution.");
  }),
  check("P0-10", "parent waits for all child tasks and wakes only after all are done", () => {
    const wake = e2e.child_done_wake || artifact.child_done_wake;
    if (wake?.all_children_done === true && wake?.parent_waited === true && wake?.requeued_task_id) return fulfilled(wake);
    if (wake?.requeued_task_id) {
      return partial("Artifact proves a child-done wake, but not that the parent waited for all children.", wake);
    }
    return missing("No child completion wake evidence found.");
  }),
  check("P0-11", "minimal real usercenter -> gateway -> ida-deployment API change passes sandbox curl", () => {
    const curl = artifact.cross_service_curl || artifact.sandbox_curl || artifact.minimal_api_curl;
    if (curl?.ok && curl?.usercenter_commit && curl?.gateway_commit && curl?.deployment_commit && curl?.public_gateway_url) {
      return fulfilled(curl);
    }
    const local = artifact.minimal_api_curl || {};
    if (local.code_change_complete === true && local.local_gateway_curl_handler_verified === true) {
      return partial("Minimal user-center/gateway/ida-deployment code and config changes are present with local gateway curl-handler evidence, but the full three-service sandbox curl is not proven.", {
        minimal_api_curl: local,
        cross_service_curl: artifact.cross_service_curl || null,
      });
    }
    return missing("No real minimal API change and sandbox curl evidence across usercenter/gateway/ida-deployment.");
  }),
  check("P0-12", "PM and 01-05 expose nonzero duration, turns, and token/usage metrics", () => {
    const stages = artifact.stage_metrics || artifact.sop_stage_metrics || [];
    const required = ["pm", "01-clarify", "02-design", "03-task-split", "04-implement", "05-verify"];
    const ok = Array.isArray(stages) && required.every((key) => {
      const stage = stages.find((item) => [item.step_key, item.role_key, item.key].includes(key));
      return stage && Number(stage.duration_ms) > 0 && Number(stage.agent_turn_count) > 0 &&
        (Number(stage.input_tokens) + Number(stage.output_tokens) + Number(stage.cache_read_tokens || 0) > 0 || stage.usage_unavailable_trace === true);
    });
    if (ok) return fulfilled({ stages });
    if (ok) return fulfilled({ stages });
    if (e2e.usage?.task_count === 1) {
      return falseClaimed("Artifact contains only aggregate leader-task usage, not per PM/01-05 stage metrics.", e2e.usage);
    }
    return missing("No per-stage PM/01-05 duration/turn/token evidence.");
  }),
  check("P0-13", "final archive/complete is blocked unless all P0/P1 are fulfilled", () => {
    const sourceMatrix = Array.isArray(artifact.original_requirement_matrix) ? artifact.original_requirement_matrix : [];
    const openBeforeGuard = sourceMatrix.filter((item) => item.id !== "P0-13" && item.status !== "fulfilled");
    if (artifact.ok === true) {
      if (openBeforeGuard.length === 0) {
        return fulfilled({ artifact_ok: true, open_blocking_items: [] });
      }
      return falseClaimed("Top-level artifact ok=true even though unfulfilled P0 items remain.", {
        artifact_ok: true,
        open_blocking_items: openBeforeGuard.map((item) => ({ id: item.id, status: item.status })),
      });
    }
    return missing("Final acceptance artifact is already failed, so archive/complete must remain blocked.", {
      artifact_ok: false,
      open_blocking_items: topLevelOpenItems.map(openItemSummary),
      source_matrix_open_items: openBeforeGuard.map((item) => ({ id: item.id, status: item.status })),
    });
  }),
  check("P0-14", "generic cross-project and Agent topology gates are dynamic and fixture-specific assertions are separated", () => {
    if (topologyGeneralization.ok === true) return fulfilled(topologyGeneralization);
    const currentTopology = artifact.topology || e2e.topology || null;
    if (currentTopology?.generic_contract?.fixture_specific_assertions_are_separate === true) {
      return partial(topologyGeneralization.blocking_reason || "Current E2E topology exists, but variable-project and variable-agent fixture evidence is not yet passing.", {
        current_topology: currentTopology,
        topology_generalization: topologyGeneralization,
      });
    }
    return missing("No topology generalization evidence proving dynamic target_projects, variable Agent nodes, and fixture-specific separation.", {
      topology_generalization: topologyGeneralization,
    });
  }),
];

const productionMatrix = Array.isArray(artifact.production_readiness_matrix) ? artifact.production_readiness_matrix : [];
const productionGaps = [
  prodGap("prod_release", "Prod release audit passes on the current release commit", productionStatus("prod_release", false)),
  prodGap("prod_data", "Prod canonical business data and training dataset are present", productionStatus("prod_data", false)),
  prodGap("prod_e2e", "Prod user-center squad curl E2E is fresh and canonical", productionStatus("prod_e2e", false)),
  prodGap("gongfeng_credentials", "Prod Gongfeng resources are credential-backed, synced, and tested", productionStatus("gongfeng_credentials", false)),
  prodGap("gongfeng_mr_merged", "Gongfeng MR is approved and merged into the target branch", productionStatus("gongfeng_mr_merged", false)),
  prodGap("rollback_drill", "Prod rollback drill is executed and restored to the release commit", productionStatus("rollback_drill", false)),
  prodGap("credentials", "TAPD/Gongfeng profile redaction, rotation, failure status, and per-user ownership", productionStatus("credentials", requirements[2].status === "fulfilled")),
  prodGap("new_account_mcp_onboarding", "A new account can configure TAPD/Gongfeng credentials and use MCP in Agent runtime", productionStatus("new_account_mcp_onboarding", false)),
  prodGap("observability", "MCP fetch, SOP stage, parent/child, approval, and curl traces are debuggable", productionStatus("observability", false)),
  prodGap("operations", "Retry, blocked recovery, parent wait timeout, and duplicate wake dedupe are documented/tested", productionStatus("operations", false)),
  prodGap("data_governance", "Fixtures are unique, marked, and do not pollute real TAPD/Gongfeng/team data", productionStatus("data_governance", false)),
  prodGap("team_handoff", "Runbook and evidence let a teammate configure profiles, rerun validation, and recover failures", productionStatus("team_handoff", false)),
  prodGap("security", "Artifacts and ledgers contain no secrets or Authorization/private tokens", productionStatus("security", true)),
  prodGap("cost", "Expensive E2E/MCP/trace collection has timing/cost guardrails", productionStatus("cost", false)),
  prodGap("ui_api_usability", "A team member can create TAPD/Gongfeng tasks and inspect source/children/inbox/trace through UI/API", productionStatus("ui_api_usability", false)),
  prodGap("generic_topology", "Cross-project and Agent topology are generic rather than fixed to the current fixture", productionStatus("generic_topology", topologyGeneralization.ok === true)),
  prodGap("cross_service_curl", "Real cross-service API change passes sandbox curl", productionStatus("cross_service_curl", false)),
];

const blockers = requirements.filter((item) => item.status !== "fulfilled");
const falseClaims = requirements.filter((item) => item.status === "false_claimed");
const goalEBlockers = goalERequirements.filter((item) => item.status !== "fulfilled");
const productionBlockers = productionGaps.filter((item) => item.blocking && item.status !== "fulfilled");
const ok = blockers.length === 0 && goalEBlockers.length === 0 && productionBlockers.length === 0;

const report = {
  schema: "multica.tapd_gongfeng_sop.gap_audit.v1",
  generated_at: now,
  ok,
  artifact_path: artifactPath,
  artifact_top_level_ok: artifact.ok === true,
  summary: {
    fulfilled: requirements.filter((item) => item.status === "fulfilled").length,
    partial: requirements.filter((item) => item.status === "partial").length,
    missing: requirements.filter((item) => item.status === "missing").length,
    false_claimed: falseClaims.length,
    blockers: blockers.length,
    goal_e_fulfilled: goalERequirements.filter((item) => item.status === "fulfilled").length,
    goal_e_partial: goalERequirements.filter((item) => item.status === "partial").length,
    goal_e_missing: goalERequirements.filter((item) => item.status === "missing").length,
    goal_e_blockers: goalEBlockers.length,
    production_blockers: productionBlockers.length,
  },
  requirements,
  goal_e_requirements: goalERequirements,
  production_readiness_gaps: productionGaps,
  archive_complete_allowed: ok,
};

fs.mkdirSync(outputDir, { recursive: true });
const stamp = now.replace(/[:.]/g, "-");
const jsonPath = path.join(outputDir, `tapd-gongfeng-sop-gap-audit-${stamp}.json`);
const mdPath = path.join(outputDir, `tapd-gongfeng-sop-gap-audit-${stamp}.md`);
const latestJSON = path.join(outputDir, "tapd-gongfeng-sop-gap-audit-latest.json");
const latestMD = path.join(outputDir, "tapd-gongfeng-sop-gap-audit-latest.md");
fs.writeFileSync(jsonPath, `${JSON.stringify(report, null, 2)}\n`);
fs.writeFileSync(latestJSON, `${JSON.stringify(report, null, 2)}\n`);
fs.writeFileSync(mdPath, renderMarkdown(report));
fs.writeFileSync(latestMD, renderMarkdown(report));

console.log(JSON.stringify({ ok, json: jsonPath, markdown: mdPath, summary: report.summary }, null, 2));
if (!ok) process.exit(1);

function check(id, title, fn) {
  try {
    return { id, title, ...fn() };
  } catch (error) {
    return {
      id,
      title,
      status: "missing",
      reason: error instanceof Error ? error.message : String(error),
      evidence: null,
    };
  }
}

function fulfilled(evidence) {
  return { status: "fulfilled", reason: "Evidence satisfies the requirement.", evidence };
}

function partial(reason, evidence) {
  return { status: "partial", reason, evidence };
}

function missing(reason, evidence = null) {
  return { status: "missing", reason, evidence };
}

function falseClaimed(reason, evidence) {
  return { status: "false_claimed", reason, evidence };
}

function prodGap(id, title, isFulfilled, blocking = true) {
  return {
    id,
    title,
    blocking,
    status: isFulfilled ? "fulfilled" : "missing",
    reason: isFulfilled ? "Evidence is present or not blocking in this audit slice." : "No sufficient production-readiness evidence found.",
  };
}

function productionStatus(id, fallback) {
  const item = productionMatrix.find((entry) => entry.id === id);
  if (!item) return fallback;
  return item.status === "fulfilled";
}

function readJSON(filePath) {
  if (!fs.existsSync(filePath)) {
    throw new Error(`Artifact not found: ${filePath}`);
  }
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
}

function acceptanceOpenItems(data) {
  const arrays = [
    data.blocking_open_items,
    data.blockers,
    data.open_goal_e_items,
    data.final_acceptance_open_items,
    data.open_items,
  ].filter(Array.isArray);
  const byKey = new Map();
  for (const items of arrays) {
    for (const item of items) {
      const summary = openItemSummary(item);
      byKey.set(`${summary.id}:${summary.title}:${summary.status}`, summary);
    }
  }
  if (Array.isArray(data.original_requirement_matrix)) {
    for (const item of data.original_requirement_matrix.filter((entry) => entry.status !== "fulfilled")) {
      const summary = openItemSummary(item);
      byKey.set(`${summary.id}:${summary.title}:${summary.status}`, summary);
    }
  }
  if (Array.isArray(data.goal_e_requirement_matrix)) {
    for (const item of data.goal_e_requirement_matrix.filter((entry) => entry.status !== "fulfilled")) {
      const summary = openItemSummary(item);
      byKey.set(`${summary.id}:${summary.title}:${summary.status}`, summary);
    }
  }
  return Array.from(byKey.values());
}

function openItemSummary(item) {
  if (typeof item === "string") return { id: item, status: "open", title: item };
  return {
    id: item?.id || item?.key || "",
    status: item?.status || item?.result || "open",
    title: item?.title || item?.requirement || item?.reason || "",
  };
}

function parseArgs(argv) {
  const parsed = {};
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--artifact") parsed.artifact = argv[++index];
    else if (arg.startsWith("--artifact=")) parsed.artifact = arg.slice("--artifact=".length);
    else if (arg === "--output-dir") parsed.outputDir = argv[++index];
    else if (arg.startsWith("--output-dir=")) parsed.outputDir = arg.slice("--output-dir=".length);
  }
  return parsed;
}

function renderMarkdown(data) {
  const lines = [
    "# TAPD/Gongfeng SOP Gap Audit",
    "",
    `- Generated: ${data.generated_at}`,
    `- Artifact: \`${data.artifact_path}\``,
    `- Result: ${data.ok ? "PASS" : "FAIL"}`,
    `- Archive/complete allowed: ${data.archive_complete_allowed ? "yes" : "no"}`,
    `- Summary: fulfilled=${data.summary.fulfilled}, partial=${data.summary.partial}, missing=${data.summary.missing}, false_claimed=${data.summary.false_claimed}`,
    `- Goal E: fulfilled=${data.summary.goal_e_fulfilled}, partial=${data.summary.goal_e_partial}, missing=${data.summary.goal_e_missing}, blockers=${data.summary.goal_e_blockers}`,
    "",
    "## Original Requirement Matrix",
    "",
    "| ID | Status | Requirement | Reason |",
    "| --- | --- | --- | --- |",
  ];
  for (const item of data.requirements) {
    lines.push(`| ${item.id} | ${item.status} | ${escapeCell(item.title)} | ${escapeCell(item.reason)} |`);
  }
  lines.push("", "## Goal E Requirement Matrix", "", "| ID | Status | Requirement | Reason |", "| --- | --- | --- | --- |");
  for (const item of data.goal_e_requirements) {
    lines.push(`| ${item.id} | ${item.status} | ${escapeCell(item.title)} | ${escapeCell(item.reason)} |`);
  }
  lines.push("", "## Production Readiness Gaps", "", "| ID | Status | Blocking | Gap |", "| --- | --- | --- | --- |");
  for (const item of data.production_readiness_gaps) {
    lines.push(`| ${item.id} | ${item.status} | ${item.blocking ? "yes" : "no"} | ${escapeCell(item.title)} |`);
  }
  lines.push("");
  return `${lines.join("\n")}\n`;
}

function escapeCell(value) {
  return String(value ?? "").replace(/\|/g, "\\|").replace(/\n/g, "<br>");
}
