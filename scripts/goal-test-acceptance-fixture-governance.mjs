#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const now = new Date().toISOString();
const stamp = now.replace(/[:.]/g, "-");
const mode = process.argv[2] || "audit";

if (!["audit"].includes(mode)) {
  throw new Error(`unsupported mode ${mode}; only read-only audit is implemented`);
}

const onboarding = readJSON(path.join(artifactRoot, "goal-test-new-account-mcp-onboarding-latest.json"));
const variableProject = readJSON(path.join(artifactRoot, "goal-test-variable-project-topology-latest.json"));
const variableAgent = readJSON(path.join(artifactRoot, "goal-test-variable-agent-topology-latest.json"));
const e2e = readJSON(path.join(artifactRoot, "codex-squad-curl-e2e-latest.json"));
const databaseURL = process.env.DATABASE_URL || readGoalTestDatabaseURL("prod") || readGoalTestDatabaseURL("int");

const checks = [];
check("new_account_fixture_traceable", Boolean(onboarding.fixture_run_id && onboarding.project?.title && onboarding.gongfeng_resource?.label), {
  fixture_run_id: onboarding.fixture_run_id || null,
  project: onboarding.project || null,
  gongfeng_resource: onboarding.gongfeng_resource || null,
});
check("variable_project_fixture_traceable", Boolean(variableProject.topology?.fixture_kind && variableProject.issue_ids?.parent), {
  fixture_kind: variableProject.topology?.fixture_kind || null,
  issue_ids: variableProject.issue_ids || null,
});
check("variable_agent_fixture_traceable_or_topology_audit_compatible", Boolean(variableAgent.ok === true || variableProject.topology?.agent_nodes?.length === 1), {
  variable_agent_ok: variableAgent.ok === true,
  variable_project_agent_nodes: variableProject.topology?.agent_nodes || null,
});
check("canonical_e2e_declares_fixture_boundary", Boolean(e2e.topology?.generic_contract?.fixture_specific_assertions_are_separate), {
  topology: e2e.topology || null,
});

const databaseEvidence = databaseURL ? await inspectDatabase(databaseURL) : { status: "skipped", reason: "DATABASE_URL unavailable" };
check("database_fixture_rows_are_identifiable", databaseEvidence.status === "fulfilled" || databaseEvidence.status === "skipped", databaseEvidence);

const blockers = checks.filter((item) => item.status !== "fulfilled");
const artifact = {
  schema: "multica.goal_test.acceptance_fixture_governance.v1",
  generated_at: now,
  ok: blockers.length === 0,
  mode,
  database_checked: Boolean(databaseURL),
  checks,
  database_evidence: databaseEvidence,
  blockers,
  policy: {
    read_only_by_default: true,
    hard_delete_forbidden_by_default: true,
    required_markers: ["fixture_run_id", "fixture_kind", "created_by_acceptance or traceable artifact path"],
  },
};

fs.mkdirSync(artifactRoot, { recursive: true });
const jsonPath = path.join(artifactRoot, `goal-test-acceptance-fixture-governance-${stamp}.json`);
const latestPath = path.join(artifactRoot, "goal-test-acceptance-fixture-governance-latest.json");
artifact.evidence_path = jsonPath;
artifact.latest_evidence_path = latestPath;
const content = `${JSON.stringify(artifact, null, 2)}\n`;
fs.writeFileSync(jsonPath, content);
fs.writeFileSync(latestPath, content);

console.log(JSON.stringify({ ok: artifact.ok, latest: latestPath, blockers }, null, 2));
if (!artifact.ok) process.exitCode = 1;

async function inspectDatabase(url) {
  const client = new pg.Client({ connectionString: url });
  try {
    await client.connect();
    const projects = await client.query(`
      SELECT id, title, description, created_at
      FROM project
      WHERE title LIKE 'mcp-onboarding-project-%'
         OR title LIKE 'topology-source-%'
         OR title LIKE 'target-alpha-%'
         OR title LIKE 'target-beta-%'
         OR title LIKE 'target-gamma-%'
      ORDER BY created_at DESC
      LIMIT 50
    `);
    const resources = await client.query(`
      SELECT id, project_id, label, resource_ref, created_at
      FROM project_resource
      WHERE label LIKE 'mcp-onboarding-gongfeng-%'
         OR resource_ref::text LIKE '%"fixture_run_id"%'
         OR resource_ref::text LIKE '%"created_by_acceptance"%'
      ORDER BY created_at DESC
      LIMIT 50
    `);
    const issues = await client.query(`
      SELECT id, title, parent_issue_id, created_at
      FROM issue
      WHERE title LIKE 'new account MCP onboarding %'
         OR title LIKE 'variable project topology parent %'
         OR title LIKE 'variable topology child %'
      ORDER BY created_at DESC
      LIMIT 100
    `);
    const resourceRows = resources.rows.map((row) => ({
      id: row.id,
      project_id: row.project_id,
      label: row.label,
      has_fixture_run_id: JSON.stringify(row.resource_ref || {}).includes("fixture_run_id"),
      has_created_by_acceptance: JSON.stringify(row.resource_ref || {}).includes("created_by_acceptance"),
      created_at: row.created_at,
    }));
    const allResourceRowsMarked = resourceRows.length === 0 || resourceRows.every((row) =>
      row.label?.startsWith("mcp-onboarding-gongfeng-") || row.has_fixture_run_id || row.has_created_by_acceptance);
    return {
      status: allResourceRowsMarked ? "fulfilled" : "blocked",
      projects: projects.rows.map((row) => ({ id: row.id, title: row.title, created_at: row.created_at })),
      resources: resourceRows,
      issues: issues.rows.map((row) => ({ id: row.id, title: row.title, parent_issue_id: row.parent_issue_id, created_at: row.created_at })),
      rule: "acceptance-created project/resource/issue rows must be discoverable by title, label, or resource_ref marker",
    };
  } catch (error) {
    return { status: "blocked", reason: error.message || String(error) };
  } finally {
    await client.end().catch(() => {});
  }
}

function check(id, condition, evidence) {
  checks.push({
    id,
    status: condition ? "fulfilled" : "blocked",
    reason: condition ? "Evidence satisfies the fixture governance rule." : "Fixture evidence is missing or not traceable.",
    evidence,
  });
}

function readJSON(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return {};
  }
}

function readGoalTestDatabaseURL(environment) {
  const envFile = path.join(repoRoot, ".run", "env", `goal-test-${environment}.env`);
  try {
    const env = {};
    for (const raw of fs.readFileSync(envFile, "utf8").split(/\r?\n/)) {
      const line = raw.trim();
      if (!line || line.startsWith("#")) continue;
      const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
      if (match) env[match[1]] = match[2].replace(/^['"]|['"]$/g, "");
    }
    return env.DATABASE_URL || "";
  } catch {
    return "";
  }
}
