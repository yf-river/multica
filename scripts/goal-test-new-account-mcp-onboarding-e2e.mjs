#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("GOAL_TEST_INT_API_URL") || "http://127.0.0.1:18762";
const ownerAccount = trimEnv("GOAL_TEST_ONBOARDING_OWNER_ACCOUNT") || trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "goal-test-daemon";
const ownerPassword = trimEnv("GOAL_TEST_ONBOARDING_OWNER_PASSWORD") || trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "e2e-password";
const workspaceSlug = trimEnv("GOAL_TEST_ONBOARDING_WORKSPACE_SLUG") || trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "goal-test-daemon";
const runID = trimEnv("GOAL_TEST_ONBOARDING_RUN_ID") || `mcp-onboarding-${Date.now()}`;
const newAccount = normalizeAccount(trimEnv("GOAL_TEST_ONBOARDING_ACCOUNT") || `goal-test-mcp-${Date.now()}`);
const newPassword = trimEnv("GOAL_TEST_ONBOARDING_PASSWORD") || `mcp-e2e-${Date.now()}`;
const onboardingRole = trimEnv("GOAL_TEST_ONBOARDING_MEMBER_ROLE") || "admin";
const provider = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_PROVIDER") || "codex";
const model = trimEnv("MULTICA_PROMPT_EVALUATION_AGENT_MODEL") || "gpt-5.3-codex-spark";
const taskTimeoutMs = Number(trimEnv("GOAL_TEST_ONBOARDING_TASK_TIMEOUT_MS") || "600000");
const tapdSourceURL = trimEnv("ACCEPTANCE_TAPD_SOURCE_URL") || "https://www.tapd.cn/47654106/markdown_wikis/show/#1147654106001004154";
const gongfengProjectPath = trimEnv("GOAL_TEST_ONBOARDING_GONGFENG_PROJECT_PATH") || "ChainWeaver/ida/user-center";
const gongfengRef = trimEnv("GOAL_TEST_ONBOARDING_GONGFENG_REF") || "v5.0.0_dev";

const artifact = {
  schema: "multica.goal_test.new_account_mcp_onboarding_e2e.v1",
  generated_at: new Date().toISOString(),
  api_url: apiURL,
  environment: apiURL.includes("18760") ? "prod" : "int",
  release_commit: gitText(["rev-parse", "--short=12", "HEAD"]),
  branch: gitText(["branch", "--show-current"]),
  fixture_run_id: runID,
  owner_account: ownerAccount,
  account: newAccount,
  workspace_slug: workspaceSlug,
  onboarding_role: onboardingRole,
  provider,
  model,
  ok: false,
  status: "running",
  checks: [],
};

try {
  const ownerLogin = post("/auth/login", { account: ownerAccount, password: ownerPassword }, null);
  const ownerToken = ownerLogin.token;
  if (!ownerToken) fail("owner login response missing token");
  const workspace = findWorkspace(ownerToken, workspaceSlug);
  artifact.workspace = pickWorkspace(workspace);

  const memberResult = ensureWorkspaceMember(ownerToken, workspace.id);
  artifact.member_setup = memberResult;
  check("new_account_member_created_or_existing", memberResult.status === "created" || memberResult.status === "already_member", memberResult);

  const userLogin = post("/auth/login", { account: newAccount, password: newPassword }, null);
  const userToken = userLogin.token;
  if (!userToken) fail("new account login response missing token");
  const currentUser = userLogin.user?.id ? userLogin.user : get("/api/me", userToken);
  artifact.user = { id: currentUser.id, account: currentUser.account, name: currentUser.name };
  const userWorkspace = findWorkspace(userToken, workspaceSlug);
  check("new_account_can_access_workspace", userWorkspace.id === workspace.id, { workspace: pickWorkspace(userWorkspace) });

  const profiles = {
    inheritance: "task_creator_or_trigger_user",
    redaction_verified: true,
    tapd: ensureExternalCredentialProfile(userToken, "tapd"),
    gongfeng: ensureExternalCredentialProfile(userToken, "gongfeng"),
  };
  artifact.credential_profiles = profiles;
  check("account_level_profiles_configured", profiles.tapd.secret_binding?.configured === true && profiles.gongfeng.secret_binding?.configured === true, profiles);
  check("credential_profile_responses_redacted", profiles.redaction_verified === true, profiles);

  const runtime = findRuntime(userToken, workspace.id);
  artifact.runtime = runtime ? { id: runtime.id, name: runtime.name, provider: runtime.provider, last_seen_at: runtime.last_seen_at } : null;
  check("workspace_has_online_runtime", Boolean(runtime?.id), artifact.runtime);

  const project = post(workspaceRoute("/api/projects", workspace.id), {
    workspace_id: workspace.id,
    title: `mcp-onboarding-project-${runID}`,
    description: `Acceptance fixture ${runID}: new account MCP onboarding project.`,
    status: "in_progress",
    lead_type: "member",
    lead_id: currentUser.id,
  }, userToken);
  if (!project?.id) fail("project creation response missing id");
  artifact.project = { id: project.id, title: project.title };

  const resource = post(workspaceRoute(`/api/projects/${project.id}/resources`, workspace.id), {
    resource_type: "gongfeng_repo",
    label: `mcp-onboarding-gongfeng-${runID}`,
    resource_ref: {
      provider: "gongfeng",
      url: `https://git.code.tencent.com/${gongfengProjectPath}/commits/${gongfengRef}`,
      project_path: gongfengProjectPath,
      resource_kind: "commits",
      ref: gongfengRef,
      fixture_run_id: runID,
      created_by_acceptance: true,
    },
  }, userToken);
  if (!resource?.id) fail("project resource creation response missing id");
  const tested = post(workspaceRoute(`/api/projects/${project.id}/resources/${resource.id}/test`, workspace.id), {}, userToken);
  const synced = post(workspaceRoute(`/api/projects/${project.id}/resources/${resource.id}/sync`, workspace.id), {}, userToken);
  artifact.gongfeng_resource = resourceEvidence(synced?.id ? synced : tested);
  check(
    "gongfeng_resource_credential_backed",
    artifact.gongfeng_resource.connection_status === "credential_backed" &&
      artifact.gongfeng_resource.test_status === "passed",
    artifact.gongfeng_resource,
  );

  const agent = post(workspaceRoute("/api/agents", workspace.id), {
    name: `mcp onboarding agent ${runID}`,
    description: "Acceptance fixture agent for new-account TAPD/Gongfeng MCP inheritance.",
    instructions: [
      "你是 MCP onboarding 验收智能体。",
      "必须先运行 `multica issue source-fetch <当前 issue id> --provider tapd --auto-fetch --output json`，由平台使用账号级 TAPD MCP 凭据读取来源并记录 trace。",
      "必须使用 gongfeng MCP 或 `multica repo checkout` 只读检查项目或分支。",
      "回复时只写结论和证据，不输出任何 token、Authorization header 或 secret_ref。",
    ].join("\n"),
    runtime_id: runtime.id,
    workspace_id: workspace.id,
    visibility: "private",
    max_concurrent_tasks: 1,
    model,
  }, userToken);
  const squad = post(workspaceRoute("/api/squads", workspace.id), {
    workspace_id: workspace.id,
    name: `mcp onboarding squad ${runID}`,
    description: "Acceptance fixture squad for new-account MCP inheritance.",
    leader_id: agent.id,
    sop_profile: {
      profile_key: "new-account-mcp-onboarding",
      project: "goal-test",
      mode: "mcp_onboarding_e2e",
      roles: [{ key: "lead", name: "Lead", responsibility: "Verify account-level MCP credentials." }],
      steps: [{ key: "verify", name: "Verify MCP", role_key: "lead" }],
      acceptance: ["TAPD MCP is callable", "Gongfeng MCP is callable", "No secrets are printed"],
    },
  }, userToken);
  if (!agent?.id || !squad?.id) fail("agent/squad creation response missing id");
  artifact.agent = { id: agent.id, name: agent.name, provider, model };
  artifact.squad = { id: squad.id, name: squad.name };

  const issue = post(workspaceRoute("/api/issues", workspace.id), {
    workspace_id: workspace.id,
    project_id: project.id,
    title: `new account MCP onboarding ${runID}`,
    description: [
      `Fixture run: ${runID}`,
      `TAPD source: ${tapdSourceURL}`,
      `Gongfeng project: ${gongfengProjectPath} @ ${gongfengRef}`,
      "请用账号级 MCP 凭据做只读验证：TAPD 通过 multica issue source-fetch --auto-fetch 记录 source.fetch，Gongfeng 通过只读仓库检查证明可用。",
    ].join("\n"),
    status: "todo",
    priority: "medium",
    assignee_type: "squad",
    assignee_id: squad.id,
    metadata: buildTapdSourceMetadata(tapdSourceURL),
  }, userToken);
  if (!issue?.id) fail("issue creation response missing id");
  artifact.issue = { id: issue.id, identifier: issue.identifier || "", title: issue.title };

  const terminalTask = await waitForTerminalTask(issue.id, agent.id, workspace.id, userToken);
  artifact.task = {
    id: terminalTask.id,
    status: terminalTask.status,
    runtime_id: terminalTask.runtime_id,
    failure_reason: terminalTask.failure_reason || "",
    error: terminalTask.error || "",
  };
  check("agent_task_completed", terminalTask.status === "completed", artifact.task);

  const fetchedIssue = get(workspaceRoute(`/api/issues/${issue.id}`, workspace.id), userToken);
  const trace = get(workspaceRoute(`/api/issues/${issue.id}/trace`, workspace.id), userToken);
  const messages = get(workspaceRoute(`/api/tasks/${terminalTask.id}/messages`, workspace.id), userToken);
  artifact.tapd_source = {
    provider: fetchedIssue.metadata?.source_fetch_provider || "",
    status: fetchedIssue.metadata?.source_fetch_status || "",
    title_present: Boolean(fetchedIssue.metadata?.source_fetch_title),
    body_excerpt_present: Boolean(fetchedIssue.metadata?.source_fetch_body_excerpt),
  };
  artifact.trace = {
    event_count: Array.isArray(trace?.events) ? trace.events.length : Number(trace?.total || 0),
    source_fetch_events: (trace?.events || []).filter((event) => event?.event_type === "source.fetch").length,
  };
  artifact.messages = {
    count: Array.isArray(messages) ? messages.length : Number(messages?.items?.length || 0),
    mcp_mentions: countMCPMentions(messages),
  };
  check("tapd_mcp_source_fetch", artifact.tapd_source.provider === "tapd_mcp" && artifact.tapd_source.status === "fetched", artifact.tapd_source);
  check("agent_context_contains_mcp_evidence", artifact.trace.source_fetch_events > 0 || artifact.messages.mcp_mentions > 0, {
    trace: artifact.trace,
    messages: artifact.messages,
  });
  check("artifact_contains_no_secret_material", !containsSecretMaterial(artifact), { redaction: "passed" });

  const failed = artifact.checks.filter((item) => item.status !== "fulfilled");
  artifact.ok = failed.length === 0;
  artifact.status = artifact.ok ? "fulfilled" : "blocked";
  artifact.blockers = failed;
} catch (error) {
  artifact.status = "blocked";
  artifact.error = error?.message || String(error);
  artifact.blockers = artifact.checks.filter((item) => item.status !== "fulfilled");
  if (!artifact.blockers.length) {
    artifact.blockers = [{ id: "script_error", status: "blocked", reason: artifact.error }];
  }
}

writeArtifact(artifact);
console.log(JSON.stringify({
  ok: artifact.ok,
  status: artifact.status,
  latest: path.join(artifactRoot, "goal-test-new-account-mcp-onboarding-latest.json"),
  blockers: artifact.blockers || [],
  error: artifact.error || "",
}, null, 2));
if (!artifact.ok) process.exitCode = 1;

function ensureWorkspaceMember(ownerToken, workspaceID) {
  const response = requestRaw("POST", `/api/workspaces/${workspaceID}/members`, {
    account: newAccount,
    name: newAccount,
    password: newPassword,
    role: onboardingRole,
  }, ownerToken);
  if (response.ok) {
    const member = parseJSON(response.text);
    return { status: "created", member: memberSummary(member) };
  }
  if (response.status === 409) {
    return { status: "already_member", account: newAccount };
  }
  fail(`create workspace member failed: ${response.status} ${response.text}`);
}

function ensureExternalCredentialProfile(token, providerName) {
  const existing = get(`/api/external-credential-profiles?provider=${providerName}`, token);
  const profiles = Array.isArray(existing?.profiles) ? existing.profiles : [];
  const configured = profiles.find((item) => item.provider === providerName && item.secret_binding?.configured);
  const body = {
    provider: providerName,
    name: `${providerName}-new-account-onboarding`,
    capabilities: {
      mcp_server: providerName === "tapd" ? "mcp-server-tapd" : "gongfeng",
      source: "goal-test-new-account-mcp-onboarding",
      fixture_run_id: runID,
    },
    verify_now: true,
  };
  const tokenValue = providerName === "tapd" ? resolveTapdAccessTokenFromCodexMCP() : trimEnv("GONGFENG_ACCESS_TOKEN") || trimEnv("GONGFENG_PRIVATE_TOKEN");
  if (tokenValue) {
    body.token = tokenValue;
  } else {
    body.secret_ref = providerName === "tapd" ? "env:TAPD_ACCESS_TOKEN" : "env:GONGFENG_ACCESS_TOKEN";
  }
  const saved = configured?.id
    ? put(`/api/external-credential-profiles/${configured.id}`, body, token)
    : post("/api/external-credential-profiles", body, token);
  return profileEvidence(saved);
}

function profileEvidence(profile) {
  const serialized = JSON.stringify(profile);
  if (containsSecretText(serialized)) {
    fail(`credential profile response may contain raw credential material: ${serialized}`);
  }
  return {
    id: profile.id,
    scope: profile.scope,
    provider: profile.provider,
    name: profile.name,
    status: profile.status,
    secret_binding: profile.secret_binding,
    capabilities: profile.capabilities,
    last_verified_at: profile.last_verified_at || null,
    last_error: profile.last_error || "",
  };
}

function findWorkspace(token, slug) {
  const data = get("/api/workspaces", token);
  const items = Array.isArray(data) ? data : data.items ?? [];
  const workspace = items.find((item) => item.slug === slug);
  if (!workspace?.id) fail(`workspace ${slug} not found for current account`);
  return workspace;
}

function findRuntime(token, workspaceID) {
  const data = get(`/api/runtimes?workspace_id=${encodeURIComponent(workspaceID)}`, token);
  const items = (Array.isArray(data) ? data : data.items ?? [])
    .filter((item) => item.provider === provider && item.status === "online")
    .sort((a, b) => new Date(b.last_seen_at || 0).getTime() - new Date(a.last_seen_at || 0).getTime());
  const runtime = items[0];
  if (!runtime?.id) fail(`no online ${provider} runtime in workspace ${workspaceID}`);
  return runtime;
}

async function waitForTerminalTask(issueID, agentID, workspaceID, token) {
  const started = Date.now();
  let lastSnapshot = null;
  while (Date.now() - started < taskTimeoutMs) {
    const tasks = get(workspaceRoute(`/api/issues/${issueID}/task-runs`, workspaceID), token);
    const items = Array.isArray(tasks?.items) ? tasks.items : Array.isArray(tasks) ? tasks : [];
    const task = items
      .filter((item) => item.agent_id === agentID || item.assignee_id === agentID)
      .sort((a, b) => new Date(b.created_at || 0).getTime() - new Date(a.created_at || 0).getTime())[0];
    lastSnapshot = {
      total_tasks: items.length,
      newest_task: task ? {
        id: task.id,
        status: task.status,
        attempt: task.attempt,
        max_attempts: task.max_attempts,
        created_at: task.created_at,
        started_at: task.started_at,
        completed_at: task.completed_at,
        failure_reason: task.failure_reason || "",
        error: task.error || "",
      } : null,
    };
    artifact.task_poll_snapshot = lastSnapshot;
    if (task && !["queued", "dispatched", "running", "waiting_local_directory"].includes(task.status)) return task;
    await sleep(3000);
  }
  fail(`timed out waiting for new-account MCP task after ${taskTimeoutMs}ms; last_snapshot=${JSON.stringify(lastSnapshot)}`);
}

function buildTapdSourceMetadata(rawURL) {
  const url = new URL(rawURL);
  const parts = url.pathname.split("/").filter(Boolean);
  const workspaceID = parts[0] || "";
  const resourceID = (url.hash.match(/#?(\d{8,})/) || [])[1] || "";
  const resourceType = url.pathname.includes("/markdown_wikis/") ? "markdown_wiki" : "tapd_resource";
  return {
    source_provider: "tapd",
    source_url: rawURL,
    tapd_workspace_id: workspaceID,
    tapd_resource_id: resourceID,
    tapd_resource_type: resourceType,
  };
}

function resourceEvidence(resource) {
  const ref = resource.resource_ref || {};
  return {
    id: resource.id,
    project_id: resource.project_id,
    label: resource.label,
    provider: ref.provider,
    project_path: ref.project_path,
    ref: ref.ref,
    resource_kind: ref.resource_kind,
    connection_status: ref.connection_status,
    sync_status: ref.sync_status,
    test_status: ref.test_status,
    credential_status: ref.credential_status,
    credential_profile_status: ref.credential_profile_status,
    credential_secret_hint: ref.credential_secret_hint,
    last_tested_at: ref.last_tested_at || null,
    last_synced_at: ref.last_synced_at || null,
  };
}

function countMCPMentions(messages) {
  const text = JSON.stringify(messages || "");
  const matches = text.match(/mcp-server-tapd|gongfeng|TAPD MCP|Gongfeng MCP/gi);
  return matches ? matches.length : 0;
}

function check(id, condition, evidence) {
  artifact.checks.push({
    id,
    status: condition ? "fulfilled" : "blocked",
    reason: condition ? "Evidence satisfies the requirement." : "Requirement is not satisfied.",
    evidence,
  });
}

function get(route, token) {
  return request("GET", route, undefined, token);
}

function post(route, body, token) {
  return request("POST", route, body, token);
}

function put(route, body, token) {
  return request("PUT", route, body, token);
}

function request(method, route, body, token) {
  const response = requestRaw(method, route, body, token);
  if (!response.ok) fail(`${method} ${route} failed: ${response.status} ${response.text}`);
  return response.text ? parseJSON(response.text) : {};
}

function requestRaw(method, route, body, token) {
  const headers = { "content-type": "application/json" };
  if (token) headers.authorization = `Bearer ${token}`;
  const args = ["-sS", "-X", method, "-w", "\n%{http_code}", `${apiURL}${route}`];
  for (const [key, value] of Object.entries(headers)) args.splice(1, 0, "-H", `${key}: ${value}`);
  if (body !== undefined && body !== null) args.splice(1, 0, "--data", JSON.stringify(body));
  const output = execFileSync("curl", args, { encoding: "utf8", maxBuffer: 20 * 1024 * 1024 });
  const idx = output.lastIndexOf("\n");
  const text = idx >= 0 ? output.slice(0, idx) : "";
  const status = Number(idx >= 0 ? output.slice(idx + 1) : 0);
  return { ok: status >= 200 && status < 300, status, text };
}

function parseJSON(text) {
  try {
    return JSON.parse(text);
  } catch (error) {
    fail(`invalid JSON response: ${error.message}: ${text.slice(0, 500)}`);
  }
}

function resolveTapdAccessTokenFromCodexMCP() {
  if (process.env.TAPD_ACCESS_TOKEN) return process.env.TAPD_ACCESS_TOKEN.trim();
  try {
    const raw = execFileSync("codex", ["mcp", "get", "mcp-server-tapd", "--json"], { encoding: "utf8", maxBuffer: 1024 * 1024 });
    const parsed = JSON.parse(raw);
    const token = parsed?.transport?.env?.TAPD_ACCESS_TOKEN;
    return typeof token === "string" ? token.trim() : "";
  } catch {
    return "";
  }
}

function writeArtifact(data) {
  mkdirSync(artifactRoot, { recursive: true });
  const stamp = new Date(data.generated_at).toISOString().replace(/[:.]/g, "-");
  const jsonPath = path.join(artifactRoot, `goal-test-new-account-mcp-onboarding-${stamp}.json`);
  const latestPath = path.join(artifactRoot, "goal-test-new-account-mcp-onboarding-latest.json");
  data.evidence_path = jsonPath;
  data.latest_evidence_path = latestPath;
  const content = `${JSON.stringify(data, null, 2)}\n`;
  writeFileSync(jsonPath, content);
  writeFileSync(latestPath, content);
}

function workspaceRoute(route, workspaceID) {
  return `${route}${route.includes("?") ? "&" : "?"}workspace_id=${encodeURIComponent(workspaceID)}`;
}

function memberSummary(member) {
  return {
    id: member.id,
    user_id: member.user_id,
    account: member.account,
    role: member.role,
  };
}

function pickWorkspace(workspace) {
  return {
    id: workspace.id,
    slug: workspace.slug,
    name: workspace.name,
  };
}

function containsSecretMaterial(value) {
  return containsSecretText(JSON.stringify(value));
}

function containsSecretText(text) {
  return /Bearer\s+[A-Za-z0-9._-]+|tapd_session=|private[-_ ]?token\s*[:=]|authorization\s*[:=]\s*Bearer|GONGFENG_ACCESS_TOKEN=\S+|TAPD_ACCESS_TOKEN=\S+/i.test(String(text || ""));
}

function normalizeAccount(value) {
  return value.toLowerCase().trim().replace(/[^a-z0-9_.-]/g, "-").slice(0, 64);
}

function gitText(args) {
  try {
    return execFileSync("git", args, { cwd: repoRoot, encoding: "utf8" }).trim();
  } catch {
    return "";
  }
}

function readGoalTestDatabaseURL(environment) {
  const envFile = path.join(repoRoot, ".run", "env", `goal-test-${environment}.env`);
  if (!existsSync(envFile)) return "";
  return readFileSync(envFile, "utf8");
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function fail(message) {
  throw new Error(message);
}

function trimEnv(name) {
  return (process.env[name] || "").trim();
}
