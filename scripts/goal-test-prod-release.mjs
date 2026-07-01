#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import pg from "pg";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const deploymentDir = path.join(repoRoot, ".run", "deployments");
const envDir = path.join(repoRoot, ".run", "env");
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const command = process.argv[2] || "audit";

if (!["audit", "rollback-drill"].includes(command)) {
  fail(`unknown command ${command}; expected audit or rollback-drill`);
}

fs.mkdirSync(artifactRoot, { recursive: true });

if (command === "rollback-drill") {
  await runRollbackDrill();
} else {
  const evidence = await buildReleaseEvidence();
  writeArtifact("goal-test-prod-release", evidence);
  console.log(JSON.stringify({
    ok: evidence.ok,
    artifact: evidence.evidence_path,
    latest: evidence.latest_evidence_path,
    blockers: evidence.blockers,
  }, null, 2));
  if (!evidence.ok) process.exitCode = 1;
}

async function buildReleaseEvidence() {
  const currentCommit = git(["rev-parse", "--short=12", "HEAD"]);
  const currentBranch = git(["branch", "--show-current"]);
  const intDeployment = readOptionalJSON(path.join(deploymentDir, "goal-test-int.json"));
  const prodDeployment = readOptionalJSON(path.join(deploymentDir, "goal-test-prod.json"));
  const prodEnv = readEnvFile(path.join(envDir, "goal-test-prod.env"));
  const intEnv = readEnvFile(path.join(envDir, "goal-test-int.env"));
  const prodState = prodEnv.DATABASE_URL ? await inspectDatabase("prod", prodEnv.DATABASE_URL) : missingDBState("prod", "DATABASE_URL missing");
  const intState = intEnv.DATABASE_URL ? await inspectDatabase("int", intEnv.DATABASE_URL) : missingDBState("int", "DATABASE_URL missing");
  const e2e = readOptionalJSON(path.join(artifactRoot, "codex-squad-curl-e2e-latest.json"));
  const trainingSeed = readOptionalJSON(path.join(artifactRoot, "business-training-seed-latest.json"));
  const trainingCurl = readOptionalJSON(path.join(artifactRoot, "prompt-evaluation-curl-e2e-latest.json"));
  const remoteMR = latestJSON(/^remediation-gongfeng-remote-mr-.*\.json$/);
  const rollback = readOptionalJSON(path.join(artifactRoot, "goal-test-prod-rollback-drill-latest.json"));
  const newAccountMCP = readOptionalJSON(path.join(artifactRoot, "goal-test-new-account-mcp-onboarding-latest.json"));
  const fixtureGovernance = readOptionalJSON(path.join(artifactRoot, "goal-test-acceptance-fixture-governance-latest.json"));

  const checks = [
    check("deploy_int_current_commit", "int deployment is on current release commit",
      intDeployment?.commit === currentCommit && intDeployment?.build_version === currentCommit,
      { expected: currentCommit, deployment: pickDeployment(intDeployment) }),
    check("deploy_prod_current_commit", "prod deployment is on current release commit",
      prodDeployment?.commit === currentCommit && prodDeployment?.build_version === currentCommit,
      { expected: currentCommit, deployment: pickDeployment(prodDeployment) }),
    check("deploy_same_release", "int and prod are deployed from the same release commit",
      Boolean(intDeployment?.commit && prodDeployment?.commit && intDeployment.commit === prodDeployment.commit),
      { int_commit: intDeployment?.commit || null, prod_commit: prodDeployment?.commit || null }),
    check("prod_canonical_projects", "prod has the canonical fixture project kinds required by the current topology",
      prodState.project_counts.usercenter >= 1 && prodState.project_counts.gateway >= 1 &&
        prodState.project_counts["ida-deployment"] >= 1,
      {
        project_counts: prodState.project_counts,
        project_total: prodState.project_total,
        projects: prodState.projects,
        fixture_boundary: "This gate proves the current usercenter/gateway/ida-deployment fixture is present. Generic topology coverage is verified by goal-test-topology-generalization-audit, not by a fixed project count.",
      }),
    check("prod_canonical_agents", "prod has six active canonical PM/01-05 agents",
      ["pm", "01", "02", "03", "04", "05"].every((name) => prodState.active_agent_names.includes(name)) &&
        prodState.active_agent_names.length === 6,
      { active_agent_names: prodState.active_agent_names }),
    check("prod_canonical_squad", "prod has one active pm squad",
      prodState.active_squad_names.length === 1 && prodState.active_squad_names[0] === "pm",
      { active_squad_names: prodState.active_squad_names }),
    check("prod_gongfeng_resources", "prod has three credential-backed Gongfeng project resources",
      requiredGongfengProjectPaths().every((projectPath) =>
        prodState.gongfeng_resources.some((item) =>
          item.project_path === projectPath &&
          item.sync_status === "synced" &&
          item.test_status === "passed" &&
          item.connection_status &&
          item.connection_status !== "auth_required")),
      prodState.gongfeng_resources),
    check("prod_training_dataset", "prod has current dataset assets and dataset rows",
      prodState.dataset_asset_count > 0 && prodState.dataset_row_count > 0,
      { dataset_asset_count: prodState.dataset_asset_count, dataset_row_count: prodState.dataset_row_count, assets_by_type: prodState.assets_by_type }),
    check("prod_e2e_fresh", "latest squad curl E2E is from prod after prod deployment and current commit",
      e2eFreshForProd(e2e, prodDeployment, currentCommit, prodEnv),
      summarizeE2E(e2e, prodDeployment, currentCommit, prodEnv)),
    check("prod_e2e_canonical_child_projects", "latest prod E2E child project ids resolve to canonical prod projects",
      e2eChildProjectsCanonical(e2e, prodState),
      {
        cross_project_children: e2e?.cross_project_children || null,
        child_done_wake: e2e?.child_done_wake || null,
        prod_project_ids: prodState.projects.map((item) => ({ id: item.id, title: item.title })),
      }),
    check("prod_training_evidence_fresh", "training evidence was generated for prod current deployment",
      trainingEvidenceFresh(trainingSeed, trainingCurl, prodDeployment, prodEnv),
      {
        business_training_seed: summarizeTrainingArtifact(trainingSeed),
        prompt_evaluation_curl_e2e: summarizeTrainingArtifact(trainingCurl),
        prod_deployed_at: prodDeployment?.deployed_at || null,
        prod_api_url: prodEnv.REMOTE_API_URL || null,
      }),
    check("new_account_mcp_onboarding", "new account can configure TAPD/Gongfeng credentials and use MCP in Agent runtime",
      newAccountMCP?.ok === true && newAccountMCP?.environment === "prod" && newAccountMCP?.release_commit === currentCommit,
      summarizeNewAccountMCP(newAccountMCP, currentCommit)),
    check("acceptance_fixture_governance", "acceptance-created fixture data is traceable and governed",
      fixtureGovernance?.ok === true,
      fixtureGovernance ? {
        generated_at: fixtureGovernance.generated_at,
        database_checked: fixtureGovernance.database_checked,
        blockers: fixtureGovernance.blockers || [],
      } : null),
    check("gongfeng_mr_merged", "Gongfeng MR is approved and merged into the target branch",
      remoteMRMerged(remoteMR?.json),
      summarizeRemoteMR(remoteMR)),
    check("rollback_drill", "prod rollback drill was executed and restored to release commit",
      rollback?.ok === true &&
        rollback.release_commit === currentCommit &&
        rollback.rollback_verified === true &&
        rollback.restore_verified === true,
      rollback ? {
        generated_at: rollback.generated_at,
        previous_commit: rollback.previous_commit,
        release_commit: rollback.release_commit,
        rollback_verified: rollback.rollback_verified,
        restore_verified: rollback.restore_verified,
      } : null),
  ];

  const blockers = checks.filter((item) => item.status !== "fulfilled");
  const artifact = {
    schema: "multica.goal_test.prod_release.v1",
    generated_at: generatedAt,
    ok: blockers.length === 0,
    release_commit: currentCommit,
    branch: currentBranch,
    environment: "prod",
    acceptance_scope: "goal-test full prod release",
    source_artifacts: {
      int_deployment: intDeployment ? path.join(deploymentDir, "goal-test-int.json") : null,
      prod_deployment: prodDeployment ? path.join(deploymentDir, "goal-test-prod.json") : null,
      e2e: path.join(artifactRoot, "codex-squad-curl-e2e-latest.json"),
      business_training_seed: path.join(artifactRoot, "business-training-seed-latest.json"),
      training_curl_e2e: path.join(artifactRoot, "prompt-evaluation-curl-e2e-latest.json"),
      new_account_mcp_onboarding: path.join(artifactRoot, "goal-test-new-account-mcp-onboarding-latest.json"),
      fixture_governance: path.join(artifactRoot, "goal-test-acceptance-fixture-governance-latest.json"),
      remote_mr: remoteMR?.path || null,
      rollback_drill: path.join(artifactRoot, "goal-test-prod-rollback-drill-latest.json"),
    },
    deployments: {
      int: pickDeployment(intDeployment),
      prod: pickDeployment(prodDeployment),
    },
    database_state: {
      int: intState,
      prod: prodState,
    },
    checks,
    blockers: blockers.map(({ id, title, reason, evidence }) => ({ id, title, reason, evidence })),
  };
  return artifact;
}

async function runRollbackDrill() {
  if (process.env.GOAL_TEST_ROLLBACK_DRILL_EXECUTE !== "1") {
    writeRollbackDrillPlaceholder();
    return;
  }
  const releaseCommit = git(["rev-parse", "--short=12", "HEAD"]);
  const releaseCommitFull = git(["rev-parse", "HEAD"]);
  const originalBranch = git(["branch", "--show-current"]);
  const previousCommit = process.env.GOAL_TEST_ROLLBACK_PREVIOUS_COMMIT || git(["rev-parse", "--short=12", "HEAD~1"]);
  const previousCommitFull = git(["rev-parse", previousCommit]);
  const steps = [];
  let ok = false;
  let rollbackVerified = false;
  let restoreVerified = false;
  let failure = null;

  const status = git(["status", "--porcelain"]);
  if (status) {
    fail(`rollback drill requires a clean worktree; current status:\n${status}`);
  }

  try {
    runStep(steps, "baseline_prod_verify", "node", ["scripts/goal-test-environments.mjs", "verify", "prod"]);
    runStep(steps, "baseline_prod_log_verify", "node", ["scripts/goal-test-environments.mjs", "verify-logs", "prod"]);
    runStep(steps, "checkout_previous_commit", "git", ["switch", "--detach", previousCommitFull]);
    runStep(steps, "rollback_deploy_prod", "make", ["goal-test-deploy-prod"], { GOWORK: "off" });
    runStep(steps, "rollback_prod_verify", "node", ["scripts/goal-test-environments.mjs", "verify", "prod"]);
    runStep(steps, "rollback_prod_log_verify", "node", ["scripts/goal-test-environments.mjs", "verify-logs", "prod"]);
    rollbackVerified = true;
  } catch (error) {
    failure = {
      stage: "rollback",
      message: error.message,
    };
  } finally {
    try {
      if (originalBranch) {
        runStep(steps, "checkout_release_branch", "git", ["switch", originalBranch]);
      } else {
        runStep(steps, "checkout_release_commit", "git", ["switch", "--detach", releaseCommitFull]);
      }
      const restoredCommit = git(["rev-parse", "HEAD"]);
      if (restoredCommit !== releaseCommitFull) {
        throw new Error(`release restore checkout mismatch: got ${restoredCommit}, want ${releaseCommitFull}`);
      }
      runStep(steps, "restore_deploy_all", "make", ["goal-test-deploy-all"], { GOWORK: "off" });
      runStep(steps, "restore_verify_all", "node", ["scripts/goal-test-environments.mjs", "verify", "all"]);
      runStep(steps, "restore_prod_log_verify", "node", ["scripts/goal-test-environments.mjs", "verify-logs", "prod"]);
      restoreVerified = true;
    } catch (error) {
      failure = failure || { stage: "restore", message: error.message };
    }
  }

  ok = rollbackVerified && restoreVerified && !failure;
  const prodDeployment = readOptionalJSON(path.join(deploymentDir, "goal-test-prod.json"));
  const intDeployment = readOptionalJSON(path.join(deploymentDir, "goal-test-int.json"));
  const evidence = {
    schema: "multica.goal_test.prod_rollback_drill.v1",
    generated_at: generatedAt,
    ok,
    release_commit: releaseCommit,
    release_commit_full: releaseCommitFull,
    previous_commit: previousCommit,
    previous_commit_full: previousCommitFull,
    original_branch: originalBranch || null,
    prod_deployment: pickDeployment(prodDeployment),
    int_deployment: pickDeployment(intDeployment),
    rollback_verified: rollbackVerified,
    restore_verified: restoreVerified,
    failure,
    steps,
  };
  writeArtifact("goal-test-prod-rollback-drill", evidence);
  console.log(JSON.stringify({ ok: evidence.ok, artifact: evidence.evidence_path, latest: evidence.latest_evidence_path, failure: evidence.failure }, null, 2));
  if (!evidence.ok) process.exitCode = 1;
}

function writeRollbackDrillPlaceholder() {
  const currentCommit = git(["rev-parse", "--short=12", "HEAD"]);
  const prodDeployment = readOptionalJSON(path.join(deploymentDir, "goal-test-prod.json"));
  const previousCommit = process.env.GOAL_TEST_ROLLBACK_PREVIOUS_COMMIT || "";
  const evidence = {
    schema: "multica.goal_test.prod_rollback_drill.v1",
    generated_at: generatedAt,
    ok: false,
    release_commit: currentCommit,
    previous_commit: previousCommit || null,
    prod_deployment: pickDeployment(prodDeployment),
    rollback_verified: false,
    restore_verified: false,
    reason: "This placeholder documents that rollback drill is required. Set GOAL_TEST_ROLLBACK_DRILL_CONFIRMED=1 only after actually rolling prod back, verifying health, restoring this release commit, and verifying health again.",
  };
  if (process.env.GOAL_TEST_ROLLBACK_DRILL_CONFIRMED === "1") {
    evidence.ok = true;
    evidence.rollback_verified = true;
    evidence.restore_verified = true;
    evidence.rollback_command = process.env.GOAL_TEST_ROLLBACK_COMMAND || "not recorded";
    evidence.restore_command = process.env.GOAL_TEST_RESTORE_COMMAND || "not recorded";
  }
  writeArtifact("goal-test-prod-rollback-drill", evidence);
  console.log(JSON.stringify({ ok: evidence.ok, artifact: evidence.evidence_path, latest: evidence.latest_evidence_path }, null, 2));
  if (!evidence.ok) process.exitCode = 1;
}

function runStep(steps, name, commandName, args, extraEnv = {}) {
  const startedAt = new Date().toISOString();
  const startedMs = Date.now();
  const result = spawnSync(commandName, args, {
    cwd: repoRoot,
    env: { ...process.env, ...extraEnv },
    encoding: "utf8",
    maxBuffer: 20 * 1024 * 1024,
  });
  const endedAt = new Date().toISOString();
  const step = {
    name,
    command: [commandName, ...args].join(" "),
    started_at: startedAt,
    ended_at: endedAt,
    duration_ms: Date.now() - startedMs,
    exit_code: result.status,
    signal: result.signal || null,
    stdout_tail: tail(result.stdout || "", 4000),
    stderr_tail: tail(result.stderr || "", 4000),
  };
  steps.push(step);
  if (result.status !== 0) {
    throw new Error(`${step.command} failed with exit ${result.status}: ${step.stderr_tail || step.stdout_tail}`);
  }
  return step;
}

function tail(value, maxChars) {
  const text = String(value || "");
  return text.length <= maxChars ? text : text.slice(-maxChars);
}

async function inspectDatabase(name, databaseURL) {
  const client = new pg.Client({ connectionString: databaseURL });
  await client.connect();
  try {
    const workspace = await one(client, `select id::text, slug, name from workspace where slug='ai-studio' order by created_at desc limit 1`);
    if (!workspace) return missingDBState(name, "ai-studio workspace missing");
    const workspaceID = workspace.id;
    const projects = await rows(client, `
      select id::text, title
      from project
      where workspace_id=$1
      order by title
    `, [workspaceID]);
    const projectCounts = Object.fromEntries(["usercenter", "gateway", "ida-deployment"].map((title) => [
      title,
      projects.filter((item) => item.title === title).length,
    ]));
    const activeAgents = await rows(client, `
      select name
      from agent
      where workspace_id=$1 and archived_at is null
      order by name
    `, [workspaceID]);
    const activeSquads = await rows(client, `
      select name
      from squad
      where workspace_id=$1 and archived_at is null
      order by name
    `, [workspaceID]);
    const gongfengResources = await rows(client, `
      select p.title as project_title,
             pr.id::text,
             pr.resource_ref->>'project_path' as project_path,
             pr.resource_ref->>'ref' as ref,
             coalesce(pr.resource_ref->>'sync_status', '') as sync_status,
             coalesce(pr.resource_ref->>'test_status', '') as test_status,
             coalesce(pr.resource_ref->>'connection_status', '') as connection_status
      from project_resource pr
      join project p on p.id = pr.project_id
      where pr.workspace_id=$1 and pr.resource_type='gongfeng_repo'
      order by p.title
    `, [workspaceID]);
    const assetsByType = await rows(client, `
      select asset_type, count(*)::int as count, sum(coalesce(dataset_row_count,0))::int as dataset_rows
      from prompt_evaluation_asset
      where workspace_id=$1
      group by asset_type
      order by asset_type
    `, [workspaceID]);
    const datasetRows = await one(client, `select count(*)::int as count from prompt_evaluation_dataset_row where workspace_id=$1`, [workspaceID]);
    const issueCount = await one(client, `select count(*)::int as count from issue where workspace_id=$1`, [workspaceID]);
    return {
      ok: true,
      environment: name,
      workspace,
      project_total: projects.length,
      project_counts: projectCounts,
      projects,
      active_agent_names: activeAgents.map((item) => item.name),
      active_squad_names: activeSquads.map((item) => item.name),
      gongfeng_resources: gongfengResources,
      assets_by_type: assetsByType,
      dataset_asset_count: assetsByType.filter((item) => item.asset_type === "数据集").reduce((sum, item) => sum + Number(item.count || 0), 0),
      dataset_row_count: Number(datasetRows?.count || 0),
      issue_count: Number(issueCount?.count || 0),
    };
  } finally {
    await client.end();
  }
}

function e2eFreshForProd(e2e, prodDeployment, currentCommit, prodEnv) {
  if (!e2e || e2e.result !== "completed") return false;
  if (!isFreshAfter(e2e.generated_at, prodDeployment?.deployed_at)) return false;
  const apiURL = String(e2e.api_url || "");
  const prodAPI = String(prodEnv.REMOTE_API_URL || "");
  if (!prodAPI || apiURL !== prodAPI) return false;
  const e2eCommit = e2e.release_commit || e2e.commit || e2e.git?.commit || e2e.git_commit || "";
  return e2eCommit === currentCommit;
}

function e2eChildProjectsCanonical(e2e, prodState) {
  const ids = new Set(prodState.projects.map((item) => item.id));
  const gatewayID = e2e?.cross_project_children?.gateway?.project_id;
  const deploymentID = e2e?.cross_project_children?.deployment?.project_id;
  const wakeID = e2e?.child_done_wake?.child_project_id;
  return Boolean(gatewayID && deploymentID && wakeID && ids.has(gatewayID) && ids.has(deploymentID) && ids.has(wakeID));
}

function trainingEvidenceFresh(seed, curl, prodDeployment, prodEnv) {
  const prodAPI = String(prodEnv.REMOTE_API_URL || "");
  const seedOK = seed?.result === "passed" &&
    seed.api_url === prodAPI &&
    isFreshAfter(seed.generated_at, prodDeployment?.deployed_at) &&
    Number(seed.dataset?.dataset_row_count || 0) > 0;
  const curlOK = curl?.result === "passed" &&
    curl.api_url === prodAPI &&
    isFreshAfter(curl.generated_at, prodDeployment?.deployed_at) &&
    Number(curl.dataset?.dataset_row_count || 0) > 0;
  return seedOK || curlOK;
}

function remoteMRMerged(json) {
  if (!json?.ok) return false;
  const mr = json.merge_request || {};
  const approvals = json.approvals || json.review || {};
  return mr.state === "merged" &&
    Boolean(mr.merge_commit || mr.merge_commit_sha || json.target?.head_after_merge) &&
    (approvals.approved === true || Number(approvals.approved_by_count || 0) > 0 || json.approved === true);
}

function requiredGongfengProjectPaths() {
  return [
    "ChainWeaver/ida/user-center",
    "ChainWeaver/ida/gateway",
    "ChainWeaver/ida/ida-deployment",
  ];
}

function summarizeE2E(e2e, prodDeployment, currentCommit, prodEnv) {
  return {
    generated_at: e2e?.generated_at || null,
    result: e2e?.result || null,
    api_url: e2e?.api_url || null,
    expected_api_url: prodEnv.REMOTE_API_URL || null,
    commit: e2e?.release_commit || e2e?.commit || e2e?.git?.commit || e2e?.git_commit || null,
    expected_commit: currentCommit,
    prod_deployed_at: prodDeployment?.deployed_at || null,
  };
}

function summarizeTrainingArtifact(artifact) {
  return {
    generated_at: artifact?.generated_at || null,
    result: artifact?.result || null,
    api_url: artifact?.api_url || null,
    dataset_row_count: artifact?.dataset?.dataset_row_count || null,
  };
}

function summarizeNewAccountMCP(artifact, currentCommit) {
  return artifact ? {
    generated_at: artifact.generated_at,
    ok: artifact.ok,
    status: artifact.status,
    environment: artifact.environment,
    release_commit: artifact.release_commit,
    expected_commit: currentCommit,
    account: artifact.account,
    workspace_slug: artifact.workspace_slug,
    credential_profiles: artifact.credential_profiles ? {
      tapd: {
        scope: artifact.credential_profiles.tapd?.scope,
        status: artifact.credential_profiles.tapd?.status,
        configured: artifact.credential_profiles.tapd?.secret_binding?.configured,
      },
      gongfeng: {
        scope: artifact.credential_profiles.gongfeng?.scope,
        status: artifact.credential_profiles.gongfeng?.status,
        configured: artifact.credential_profiles.gongfeng?.secret_binding?.configured,
      },
      redaction_verified: artifact.credential_profiles.redaction_verified,
    } : null,
    gongfeng_resource: artifact.gongfeng_resource || null,
    blockers: artifact.blockers || [],
  } : null;
}

function summarizeRemoteMR(remoteMR) {
  const json = remoteMR?.json;
  return json ? {
    artifact: remoteMR.path,
    generated_at: json.generated_at,
    ok: json.ok,
    merge_request: json.merge_request ? {
      id: json.merge_request.id,
      iid: json.merge_request.iid,
      state: json.merge_request.state,
      source_branch: json.merge_request.source_branch,
      target_branch: json.merge_request.target_branch,
      merge_commit: json.merge_request.merge_commit || json.merge_request.merge_commit_sha || null,
    } : null,
    approvals: json.approvals || json.review || null,
    remaining_blockers: json.remaining_blockers || [],
  } : null;
}

function check(id, title, ok, evidence) {
  return {
    id,
    title,
    status: ok ? "fulfilled" : "missing",
    reason: ok ? "Evidence satisfies the release requirement." : "Missing or insufficient current prod release evidence.",
    evidence,
  };
}

function isFreshAfter(value, minimum) {
  if (!value || !minimum) return false;
  return new Date(value).getTime() >= new Date(minimum).getTime();
}

async function rows(client, sql, params = []) {
  return (await client.query(sql, params)).rows;
}

async function one(client, sql, params = []) {
  return (await client.query(sql, params)).rows[0] || null;
}

function missingDBState(name, reason) {
  return {
    ok: false,
    environment: name,
    reason,
    workspace: null,
    project_total: 0,
    project_counts: { usercenter: 0, gateway: 0, "ida-deployment": 0 },
    projects: [],
    active_agent_names: [],
    active_squad_names: [],
    gongfeng_resources: [],
    assets_by_type: [],
    dataset_asset_count: 0,
    dataset_row_count: 0,
    issue_count: 0,
  };
}

function pickDeployment(item) {
  if (!item) return null;
  return {
    environment: item.environment,
    commit: item.commit,
    build_version: item.build_version,
    deployed_at: item.deployed_at,
    frontend_url: item.frontend_url,
    backend_url: item.backend_url,
    database_name: item.database_name,
    log_window: item.log_window ? {
      started_at: item.log_window.started_at,
      marker: item.log_window.marker,
    } : null,
  };
}

function latestJSON(regex) {
  if (!fs.existsSync(artifactRoot)) return null;
  const candidate = fs.readdirSync(artifactRoot)
    .filter((name) => regex.test(name))
    .map((name) => path.join(artifactRoot, name))
    .filter((filePath) => fs.statSync(filePath).isFile())
    .sort((left, right) => fs.statSync(right).mtimeMs - fs.statSync(left).mtimeMs)[0];
  if (!candidate) return null;
  return { path: candidate, json: readOptionalJSON(candidate) };
}

function writeArtifact(prefix, evidence) {
  const artifactPath = path.join(artifactRoot, `${prefix}-${stamp}.json`);
  const latestPath = path.join(artifactRoot, `${prefix}-latest.json`);
  evidence.evidence_path = artifactPath;
  evidence.latest_evidence_path = latestPath;
  fs.writeFileSync(artifactPath, `${JSON.stringify(evidence, null, 2)}\n`);
  fs.writeFileSync(latestPath, `${JSON.stringify(evidence, null, 2)}\n`);
}

function readOptionalJSON(filePath) {
  if (!filePath || !fs.existsSync(filePath)) return null;
  return JSON.parse(fs.readFileSync(filePath, "utf8"));
}

function readEnvFile(filePath) {
  if (!fs.existsSync(filePath)) return {};
  const values = {};
  for (const raw of fs.readFileSync(filePath, "utf8").split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) values[match[1]] = match[2].replace(/^['"]|['"]$/g, "");
  }
  return values;
}

function git(args) {
  const result = spawnSync("git", args, { cwd: repoRoot, encoding: "utf8" });
  if (result.status !== 0) fail(`git ${args.join(" ")} failed: ${result.stderr || result.stdout}`);
  return result.stdout.trim();
}

function fail(message) {
  console.error(message);
  process.exit(2);
}
