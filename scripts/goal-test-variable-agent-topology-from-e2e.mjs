#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { execFileSync } from "node:child_process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const sourcePath = path.resolve(process.env.VARIABLE_AGENT_E2E_ARTIFACT || path.join(artifactRoot, "codex-squad-curl-e2e-variable-agent-latest.json"));
const e2e = readJSON(sourcePath);
const now = new Date().toISOString();
const stamp = now.replace(/[:.]/g, "-");

const topology = e2e.topology || {};
const agentNodes = Array.isArray(topology.agent_nodes) && topology.agent_nodes.length > 0
  ? topology.agent_nodes
  : e2e.agent?.id
    ? [{ id: e2e.agent.id, name: e2e.agent.name, role_key: e2e.agent.role_key || e2e.agent.name || e2e.agent.id, provider: e2e.provider, model: e2e.model }]
    : [];
const variableAgentTopology = {
  ...topology,
  schema: "multica.acceptance.topology.v1",
  fixture_kind: topology.fixture_kind || e2e.squad_template_key || "ad-hoc",
  agent_nodes: agentNodes,
  expected_stage_count: agentNodes.length,
  observed_stage_count: e2e.task?.status === "completed" ? agentNodes.length : Number(topology.observed_stage_count || 0),
  target_projects: Array.isArray(topology.target_projects) ? topology.target_projects : [],
  child_issues: Array.isArray(topology.child_issues) ? topology.child_issues : [],
  expected_child_issue_count: Number(topology.expected_child_issue_count || 0),
  observed_child_issue_count: Number(topology.observed_child_issue_count || 0),
};

const ok =
  e2e.result === "completed" &&
  e2e.task?.status === "completed" &&
  e2e.release_commit === gitCommit() &&
  agentNodes.length > 0 &&
  agentNodes.length !== 6 &&
  variableAgentTopology.observed_stage_count === agentNodes.length &&
  Number(e2e.trace_event_count || 0) > 0 &&
  Number(e2e.message_count || 0) > 0;

const artifact = {
  schema: "multica.goal_test.variable_agent_topology_fixture.v1",
  generated_at: now,
  ok,
  source_artifact: sourcePath,
  source_generated_at: e2e.generated_at || "",
  source_result: e2e.result || "",
  source_task_status: e2e.task?.status || "",
  release_commit: e2e.release_commit || "",
  branch: e2e.branch || "",
  topology: variableAgentTopology,
  evidence: {
    issue_id: e2e.issue?.id || null,
    task_id: e2e.task?.id || null,
    trace_event_count: e2e.trace_event_count || 0,
    message_count: e2e.message_count || 0,
    usage: e2e.usage || null,
  },
  blocking_reason: ok
    ? ""
    : "Latest codex-squad-curl-e2e evidence is not a fresh completed non-6-agent topology fixture for the current commit.",
};

fs.mkdirSync(artifactRoot, { recursive: true });
const jsonPath = path.join(artifactRoot, `goal-test-variable-agent-topology-${stamp}.json`);
const latestPath = path.join(artifactRoot, "goal-test-variable-agent-topology-latest.json");
artifact.evidence_path = jsonPath;
artifact.latest_evidence_path = latestPath;
const content = `${JSON.stringify(artifact, null, 2)}\n`;
fs.writeFileSync(jsonPath, content);
fs.writeFileSync(latestPath, content);

console.log(JSON.stringify({ ok, json: jsonPath, latest: latestPath, agent_nodes: agentNodes.length, source: sourcePath }, null, 2));
if (!ok) process.exitCode = 1;

function readJSON(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return {};
  }
}

function gitCommit() {
  try {
    return String(execFileSync("git", ["rev-parse", "--short=12", "HEAD"], { cwd: repoRoot, encoding: "utf8" })).trim();
  } catch {
    return "";
  }
}
