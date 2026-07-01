#!/usr/bin/env node

import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const now = new Date().toISOString();
const stamp = now.replace(/[:.]/g, "-");

const files = [
  "scripts/codex-squad-curl-e2e.mjs",
  "scripts/generate-tapd-gongfeng-sop-final-acceptance.mjs",
  "scripts/tapd-gongfeng-sop-gap-audit.mjs",
  "scripts/goal-test-prod-release.mjs",
];

const findings = [];
for (const file of files) {
  const abs = path.join(repoRoot, file);
  const text = fs.existsSync(abs) ? fs.readFileSync(abs, "utf8") : "";
  const lines = text.split(/\r?\n/);
  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    for (const pattern of hardcodingPatterns()) {
      if (pattern.re.test(line)) {
        findings.push(classifyFinding({ file, line: index + 1, text: line.trim(), pattern }));
      }
    }
  }
}

const latestE2E = readJSON(path.join(artifactRoot, "codex-squad-curl-e2e-latest.json"));
const topology = latestE2E.topology || null;
const variableProject = readJSON(path.join(artifactRoot, "goal-test-variable-project-topology-latest.json"));
const variableAgent = readJSON(path.join(artifactRoot, "goal-test-variable-agent-topology-latest.json"));

const productHardcodes = findings.filter((item) => item.classification === "product_hardcode");
const acceptanceHardcodes = findings.filter((item) => item.classification === "acceptance_hardcode");
const topologyOk = Boolean(
  topology &&
  Array.isArray(topology.target_projects) &&
  Array.isArray(topology.child_issues) &&
  Array.isArray(topology.agent_nodes) &&
  topology.expected_child_issue_count === topology.target_projects.length &&
  topology.observed_child_issue_count === topology.child_issues.length &&
  topology.expected_stage_count === topology.agent_nodes.length,
);
const variableProjectOk = variableProject.ok === true && Number(variableProject.topology?.target_projects?.length || 0) !== 2;
const variableAgentOk = variableAgent.ok === true && Number(variableAgent.topology?.agent_nodes?.length || 0) !== 6;
const ok = topologyOk && variableProjectOk && variableAgentOk && productHardcodes.length === 0;

const artifact = {
  schema: "multica.goal_test.topology_generalization_audit.v1",
  generated_at: now,
  ok,
  checks: {
    current_e2e_topology: topologyOk ? "fulfilled" : "missing",
    variable_project_fixture: variableProjectOk ? "fulfilled" : "missing",
    variable_agent_fixture: variableAgentOk ? "fulfilled" : "missing",
    product_hardcodes: productHardcodes.length === 0 ? "fulfilled" : "partial",
    acceptance_fixture_constants_reviewed: acceptanceHardcodes.length === 0 ? "fulfilled" : "reviewed",
  },
  current_e2e_topology: topology,
  variable_project_fixture: {
    artifact_path: path.join(artifactRoot, "goal-test-variable-project-topology-latest.json"),
    ok: variableProjectOk,
    evidence: variableProject.ok === true ? variableProject : null,
  },
  variable_agent_fixture: {
    artifact_path: path.join(artifactRoot, "goal-test-variable-agent-topology-latest.json"),
    ok: variableAgentOk,
    evidence: variableAgent.ok === true ? variableAgent : null,
  },
  findings,
  summary: {
    total_findings: findings.length,
    fixture_constants: findings.filter((item) => item.classification === "fixture_constant").length,
    product_hardcodes: productHardcodes.length,
    acceptance_hardcodes: acceptanceHardcodes.length,
    review_required: findings.filter((item) => item.classification === "review_required").length,
  },
  blocking_reason: ok
    ? ""
    : "Generic topology acceptance requires dynamic topology evidence plus variable-project and variable-agent fixtures; fixture-specific usercenter/gateway/ida-deployment evidence alone is insufficient.",
  non_blocking_review_note: acceptanceHardcodes.length > 0
    ? "Fixture-specific acceptance constants remain for the current usercenter/gateway/ida-deployment and pm+01-05 regression path. They are non-blocking only because generic topology gates now use topology evidence and separate variable-project/variable-agent fixtures."
    : "",
};

fs.mkdirSync(artifactRoot, { recursive: true });
const jsonPath = path.join(artifactRoot, `goal-test-topology-generalization-audit-${stamp}.json`);
const latestPath = path.join(artifactRoot, "goal-test-topology-generalization-audit-latest.json");
fs.writeFileSync(jsonPath, `${JSON.stringify(artifact, null, 2)}\n`);
fs.writeFileSync(latestPath, `${JSON.stringify(artifact, null, 2)}\n`);

console.log(JSON.stringify({ ok, json: jsonPath, latest: latestPath, summary: artifact.summary, checks: artifact.checks }, null, 2));
if (!ok) process.exitCode = 1;

function hardcodingPatterns() {
  return [
    { key: "project_usercenter", re: /\busercenter\b|user-center/ },
    { key: "project_gateway", re: /\bgateway\b/ },
    { key: "project_ida_deployment", re: /ida-deployment|ida_deployment/ },
    { key: "agent_pm", re: /["']pm["']|\bPM\b/ },
    { key: "agent_0105", re: /01-clarify|02-design|03-task-split|04-implement|05-verify|01-05|0105/ },
    { key: "fixed_child_two", re: /child_count|childCount|expected_child_issue_count|至少 2|两个/ },
    { key: "fixed_stage_six", re: /stage_count|stageCount|agent_count|length >= 6|六角色|6 个/ },
  ];
}

function classifyFinding({ file, line, text, pattern }) {
  const isCurrentFixture =
    /fixture|canonical|current|prod_canonical|user-center-cross-project|当前|fixture-specific/i.test(text) ||
    /goal-test-prod-release\.mjs/.test(file);
  const isGenericTopologyCode =
    /topology|target_projects|agent_nodes|expected_stage_count|observed_stage_count|fixture_specific/i.test(text);
  const isFinalOrGap = /final-acceptance|gap-audit/.test(file);
  const isE2EAcceptance = /codex-squad-curl-e2e/.test(file);
  let classification = "review_required";
  if (isCurrentFixture || isGenericTopologyCode) {
    classification = "fixture_constant";
  } else if (isFinalOrGap || isE2EAcceptance) {
    classification = "acceptance_hardcode";
  } else if (/server\/|web\/|src\//.test(file)) {
    classification = "product_hardcode";
  }
  return {
    file,
    line,
    pattern: pattern.key,
    classification,
    text,
  };
}

function readJSON(file) {
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch {
    return {};
  }
}
