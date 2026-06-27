#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("GOAL_TEST_INT_API_URL") || "http://127.0.0.1:18762";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "goal-test-daemon";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "e2e-password";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "goal-test-daemon";
const provider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codex";
const model = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "gpt-5.3-codex-spark";
const suffix = Date.now();
const now = new Date().toISOString();

const artifact = {
  schema: "multica.goal_test.variable_project_topology_fixture.v1",
  generated_at: now,
  api_url: apiURL,
  environment: apiURL.includes("18760") ? "prod" : "int",
  ok: false,
  commands: ["public API: login, workspace/runtime/project/agent/squad/issue/children"],
};

try {
  const login = post("/auth/login", { account, password }, { auth: false });
  const token = login.token;
  if (!token) fail("login response missing token");
  const currentUser = login.user?.id ? login.user : get("/api/me", token);
  if (!currentUser?.id) fail("current user missing");
  const workspace = findWorkspace(token, workspaceSlug);
  const runtime = findRuntime(token, workspace.id);

  const leader = post(`/api/agents?workspace_id=${encodeURIComponent(workspace.id)}`, {
    name: `topology fixture leader ${suffix}`,
    description: "Variable project topology fixture leader; no production data.",
    instructions: "Respond in Chinese with concise topology evidence.",
    runtime_id: runtime.id,
    workspace_id: workspace.id,
    visibility: "private",
    max_concurrent_tasks: 1,
    model,
  }, token);
  if (!leader?.id) fail("leader agent missing id");
  const squad = post(`/api/squads?workspace_id=${encodeURIComponent(workspace.id)}`, {
    workspace_id: workspace.id,
    name: `topology fixture squad ${suffix}`,
    description: "Public API topology fixture squad for variable target project evidence.",
    leader_id: leader.id,
    sop_profile: {
      profile_key: "variable-project-topology-fixture",
      project: "goal-test",
      mode: "public_api_topology_fixture",
      roles: [{ key: "lead", name: "Lead", responsibility: "Owns fixture children." }],
      steps: [{ key: "route", name: "Route", role_key: "lead" }],
      acceptance: ["source project has three target project children"],
    },
  }, token);
  if (!squad?.id) fail("squad missing id");

  const source = post(workspaceRoute("/api/projects", workspace.id), {
    workspace_id: workspace.id,
    title: `topology-source-${suffix}`,
    description: "Variable topology source project created through public API.",
    status: "in_progress",
    lead_type: "member",
    lead_id: currentUser.id,
  }, token);
  if (!source?.id) fail("source project missing id");

  const targetProjects = [];
  for (const key of ["target-alpha", "target-beta", "target-gamma"]) {
    const project = post(workspaceRoute("/api/projects", workspace.id), {
      workspace_id: workspace.id,
      title: `${key}-${suffix}`,
      description: `Variable topology target project ${key} created through public API.`,
      status: "in_progress",
      lead_type: "member",
      lead_id: currentUser.id,
    }, token);
    if (!project?.id) fail(`target project ${key} missing id`);
    targetProjects.push({ key, project });
  }

  const parent = post(workspaceRoute("/api/issues", workspace.id), {
    workspace_id: workspace.id,
    project_id: source.id,
    title: `variable project topology parent ${suffix}`,
    description: "Public API fixture: one source project routes to three target projects.",
    status: "todo",
    priority: "medium",
    assignee_type: "squad",
    assignee_id: squad.id,
  }, token);
  if (!parent?.id) fail("parent issue missing id");

  const children = [];
  for (const target of targetProjects) {
    const child = post(workspaceRoute("/api/issues", workspace.id), {
      workspace_id: workspace.id,
      project_id: target.project.id,
      parent_issue_id: parent.id,
      title: `variable topology child ${target.key} ${suffix}`,
      description: `Child routed to ${target.key}; routing reason is explicit fixture target mapping.`,
      status: "backlog",
      priority: "medium",
      assignee_type: "squad",
      assignee_id: squad.id,
    }, token);
    if (!child?.id) fail(`child issue ${target.key} missing id`);
    children.push({ target, child });
  }

  const listed = get(workspaceRoute(`/api/issues/${parent.id}/children`, workspace.id), token);
  const listedChildren = Array.isArray(listed?.issues) ? listed.issues : Array.isArray(listed) ? listed : [];
  const listedByProject = new Map(listedChildren.map((item) => [item.project_id, item]));
  const allTargetsObserved = targetProjects.every((target) => listedByProject.has(target.project.id));

  artifact.topology = {
    schema: "multica.acceptance.topology.v1",
    fixture_kind: "variable-project-public-api",
    source_project: { project_id: source.id, project_title: source.title },
    target_projects: targetProjects.map((target) => ({
      key: target.key,
      project_id: target.project.id,
      project_title: target.project.title,
      target_squad_id: squad.id,
      target_squad_name: squad.name,
      routing_reason: "explicit fixture target mapping",
    })),
    child_issues: children.map(({ target, child }) => ({
      id: child.id,
      identifier: child.identifier || "",
      title: child.title,
      status: child.status,
      parent_issue_id: child.parent_issue_id || null,
      project_id: child.project_id || null,
      assignee_type: child.assignee_type || null,
      assignee_id: child.assignee_id || null,
      target_key: target.key,
      routing_reason: "explicit fixture target mapping",
    })),
    squad_id: squad.id,
    squad_name: squad.name,
    template_id: "variable-project-public-api",
    agent_nodes: [{ id: leader.id, name: leader.name, role_key: "lead", provider, model }],
    expected_stage_count: 1,
    observed_stage_count: 1,
    expected_child_issue_count: targetProjects.length,
    observed_child_issue_count: children.length,
    routing_rules_evidence: targetProjects.map((target) => ({
      target_key: target.key,
      target_project_id: target.project.id,
      routing_reason: "explicit fixture target mapping",
      source: "public_api_fixture",
    })),
  };
  artifact.issue_ids = {
    parent: parent.id,
    children: children.map(({ child }) => child.id),
  };
  artifact.ok = targetProjects.length === 3 && children.length === 3 && allTargetsObserved;
  if (!artifact.ok) fail("variable project topology verification failed");
} catch (error) {
  artifact.error = error?.message || String(error);
}

writeArtifact(artifact);
console.log(JSON.stringify({ ok: artifact.ok, latest: path.join(artifactRoot, "goal-test-variable-project-topology-latest.json"), error: artifact.error || "" }, null, 2));
if (!artifact.ok) process.exitCode = 1;

function findWorkspace(token, slug) {
  const data = get("/api/workspaces", token);
  const items = Array.isArray(data) ? data : data.items ?? [];
  const workspace = items.find((item) => item.slug === slug);
  if (!workspace?.id) fail(`workspace ${slug} not found`);
  return workspace;
}

function findRuntime(token, workspaceID) {
  const data = get(`/api/runtimes?workspace_id=${encodeURIComponent(workspaceID)}`, token);
  const items = (Array.isArray(data) ? data : data.items ?? [])
    .filter((item) => item.provider === provider && item.status === "online")
    .sort((a, b) => new Date(b.last_seen_at || 0).getTime() - new Date(a.last_seen_at || 0).getTime());
  if (!items[0]?.id) fail(`no online ${provider} runtime`);
  return items[0];
}

function workspaceRoute(route, workspaceID) {
  return `${route}${route.includes("?") ? "&" : "?"}workspace_id=${encodeURIComponent(workspaceID)}`;
}

function get(route, token) {
  return request("GET", route, undefined, token);
}

function post(route, body, tokenOrOptions) {
  const token = typeof tokenOrOptions === "string" ? tokenOrOptions : "";
  return request("POST", route, body, token);
}

function request(method, route, body, token) {
  const headers = { "content-type": "application/json" };
  if (token) headers.authorization = `Bearer ${token}`;
  const response = fetchSync(`${apiURL}${route}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  if (!response.ok) fail(`${method} ${route} failed: ${response.status} ${response.text}`);
  if (!response.text) return {};
  return JSON.parse(response.text);
}

function fetchSync(url, options) {
  const args = ["-sS", "-X", options.method || "GET", "-w", "\n%{http_code}", url];
  for (const [key, value] of Object.entries(options.headers || {})) args.splice(1, 0, "-H", `${key}: ${value}`);
  if (options.body !== undefined) args.splice(1, 0, "--data", options.body);
  const output = execFileSync("curl", args, { encoding: "utf8", maxBuffer: 10 * 1024 * 1024 });
  const idx = output.lastIndexOf("\n");
  const text = idx >= 0 ? output.slice(0, idx) : "";
  const status = Number(idx >= 0 ? output.slice(idx + 1) : 0);
  return { ok: status >= 200 && status < 300, status, text };
}

function writeArtifact(data) {
  mkdirSync(artifactRoot, { recursive: true });
  const stamp = new Date(data.generated_at).toISOString().replace(/[:.]/g, "-");
  const jsonPath = path.join(artifactRoot, `goal-test-variable-project-topology-${stamp}.json`);
  const latestPath = path.join(artifactRoot, "goal-test-variable-project-topology-latest.json");
  data.evidence_path = jsonPath;
  data.latest_evidence_path = latestPath;
  const content = `${JSON.stringify(data, null, 2)}\n`;
  writeFileSync(jsonPath, content);
  writeFileSync(latestPath, content);
}

function fail(message) {
  throw new Error(message);
}

function trimEnv(name) {
  return (process.env[name] || "").trim();
}
