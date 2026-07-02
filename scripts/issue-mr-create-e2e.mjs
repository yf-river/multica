#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import process from "node:process";

const apiURL = trimEnv("ACCEPTANCE_API_URL") || trimEnv("GOAL_TEST_INT_API_URL") || "http://127.0.0.1:18762";
const account = trimEnv("ACCEPTANCE_DEMO_ACCOUNT") || "develop";
const password = trimEnv("ACCEPTANCE_DEMO_PASSWORD") || "develop123";
const workspaceSlug = trimEnv("ACCEPTANCE_WORKSPACE_SLUG") || "ai-studio";
const projectPath = trimEnv("ACCEPTANCE_REPO_PROJECT_PATH") || "ChainWeaver/ida/user-center";
const targetBranch = trimEnv("ACCEPTANCE_REPO_REF") || "v5.0.0_dev_sop";
const suffix = new Date().toISOString().replace(/[-:.TZ]/g, "").slice(0, 14);
const sourceBranch = trimEnv("ACCEPTANCE_SOURCE_BRANCH") || `agent/mr-platform-${suffix}`;
const filePath = `docs/platform-mr-handoff/${suffix}.md`;

const evidence = {
  schema: "multica.issue_mr_create_e2e.v1",
  generated_at: new Date().toISOString(),
  api_url: apiURL,
  workspace_slug: workspaceSlug,
  project_path: projectPath,
  target_branch: targetBranch,
  source_branch: sourceBranch,
  file_path: filePath,
  ok: false,
  checks: [],
};

try {
  const login = post("/auth/login", { account, password }, null);
  const token = login.token;
  if (!token) fail("login response missing token");
  const workspace = findWorkspace(token, workspaceSlug);
  evidence.workspace = { id: workspace.id, slug: workspace.slug, name: workspace.name };

  const profile = ensureGongfengCredentialProfile(token);
  evidence.credential_profile = profileEvidence(profile);
  check("account_gongfeng_credential_configured", profile?.secret_binding?.configured === true, evidence.credential_profile);

  const issue = post(workspaceRoute("/api/issues", workspace.id), {
    workspace_id: workspace.id,
    title: `平台 MR 创建与任务关联验收 ${suffix}`,
    description: [
      "验证平台创建 MR 后，任务详情能够同步看到关联 MR。",
      `仓库：${projectPath}`,
      `目标分支：${targetBranch}`,
      "该任务只写入一份 docs 验收文件，不修改产品业务代码。",
    ].join("\n"),
    status: "todo",
    priority: "medium",
  }, token);
  if (!issue?.id) fail("issue creation response missing id");
  evidence.issue = { id: issue.id, identifier: issue.identifier || "", title: issue.title };
  check("issue_created", Boolean(issue.id), evidence.issue);

  createGongfengBranchAndFile(issue);
  check("gongfeng_source_branch_prepared", true, {
    project_path: projectPath,
    source_branch: sourceBranch,
    file_path: filePath,
  });

  const descriptionFile = path.join(mkdtempSync(path.join(tmpdir(), "multica-mr-e2e-")), "mr.md");
  writeFileSync(descriptionFile, [
    `Multica issue: ${issue.identifier || issue.id}`,
    "",
    "05 验证已完成，提交人工 CodeReview。",
    "",
    `验证文件：${filePath}`,
  ].join("\n"), "utf8");

  const cliJSON = execFileSync(path.join("server", "bin", "multica"), [
    "issue", "mr", "create", issue.id,
    "--provider", "gongfeng",
    "--project-path", projectPath,
    "--source-branch", sourceBranch,
    "--target-branch", targetBranch,
    "--title", `${issue.identifier || "AI-STUDIO"}: platform MR create handoff`,
    "--description-file", descriptionFile,
    "--output", "json",
  ], {
    cwd: path.resolve(import.meta.dirname, ".."),
    encoding: "utf8",
    env: {
      ...process.env,
      MULTICA_SERVER_URL: apiURL,
      MULTICA_TOKEN: token,
      MULTICA_WORKSPACE_ID: workspace.id,
    },
    maxBuffer: 1024 * 1024,
  });
  const createdMR = JSON.parse(cliJSON);
  evidence.created_mr = {
    linked: createdMR.linked === true,
    url: createdMR.pull_request?.html_url || createdMR.merge_request?.url || "",
    number: createdMR.pull_request?.number || createdMR.merge_request?.iid || 0,
    state: createdMR.pull_request?.state || "",
    title: createdMR.pull_request?.title || "",
  };
  check("platform_created_and_linked_mr", evidence.created_mr.linked === true && Boolean(evidence.created_mr.url), evidence.created_mr);

  const linked = get(workspaceRoute(`/api/issues/${issue.id}/pull-requests`, workspace.id), token);
  const prs = Array.isArray(linked?.pull_requests) ? linked.pull_requests : [];
  const matched = prs.find((item) => item.html_url === evidence.created_mr.url || Number(item.number) === Number(evidence.created_mr.number));
  evidence.linked_pull_requests = prs.map((item) => ({
    number: item.number,
    state: item.state,
    title: item.title,
    html_url: item.html_url,
    branch: item.branch || "",
  }));
  check("issue_pull_requests_reads_created_mr", Boolean(matched), evidence.linked_pull_requests);

  evidence.ok = evidence.checks.every((item) => item.ok);
  evidence.status = evidence.ok ? "passed" : "failed";
  console.log(JSON.stringify(evidence, null, 2));
  process.exit(evidence.ok ? 0 : 1);
} catch (error) {
  evidence.ok = false;
  evidence.status = "failed";
  evidence.error = String(error?.stack || error?.message || error);
  console.error(JSON.stringify(evidence, null, 2));
  process.exit(1);
}

function ensureGongfengCredentialProfile(token) {
  const existing = get("/api/external-credential-profiles?provider=gongfeng", token);
  const profiles = Array.isArray(existing?.profiles) ? existing.profiles : [];
  const configured = profiles.find((item) => item.provider === "gongfeng" && item.secret_binding?.configured);
  const body = {
    provider: "gongfeng",
    name: "gongfeng-platform-mr",
    capabilities: { mcp_server: "gongfeng", source: "platform-mr-create-e2e" },
    verify_now: true,
  };
  const tokenValue = trimEnv("GONGFENG_ACCESS_TOKEN") || trimEnv("GONGFENG_PRIVATE_TOKEN");
  if (tokenValue) {
    body.token = tokenValue;
  } else {
    body.secret_ref = "env:GONGFENG_ACCESS_TOKEN";
  }
  return configured?.id
    ? put(`/api/external-credential-profiles/${configured.id}`, body, token)
    : post("/api/external-credential-profiles", body, token);
}

function createGongfengBranchAndFile(issue) {
  const apiProjectID = resolveGongfengProjectAPIID(projectPath);
  const content = [
    `# ${issue.identifier || issue.id} platform MR handoff`,
    "",
    "This document proves the platform-created MR handoff path.",
    "",
    `- Multica issue: ${issue.identifier || issue.id}`,
    `- Source branch: ${sourceBranch}`,
    `- Target branch: ${targetBranch}`,
    `- Created at: ${new Date().toISOString()}`,
    "",
    "No product code is changed by this verification file.",
  ].join("\n");
  gongfengRequest("POST", `projects/${encodeGongfengProjectID(apiProjectID)}/repository/branches`, {
    branch_name: sourceBranch,
    ref: targetBranch,
  });
  gongfengRequest("POST", `projects/${encodeGongfengProjectID(apiProjectID)}/repository/files`, {
    file_path: filePath,
    branch_name: sourceBranch,
    content,
    commit_message: `${issue.identifier || "AI-STUDIO"}: add platform MR handoff evidence`,
  });
}

function resolveGongfengProjectAPIID(value) {
  if (/^\d+$/.test(value)) return value;
  const query = value.split("/").pop() || value;
  const found = gongfengRequest("GET", `projects?search=${encodeURIComponent(query)}&per_page=100`);
  const projects = Array.isArray(found) ? found : [];
  const matched = projects.find((item) => String(item.path_with_namespace || "").replace(/^\/+|\/+$/g, "").toLowerCase() === value.toLowerCase());
  if (!matched?.id) fail(`gongfeng project ${value} not found by search`);
  return String(matched.id);
}

function profileEvidence(profile) {
  const serialized = JSON.stringify(profile);
  if (containsSecretText(serialized)) fail("credential profile response contains raw secret material");
  return {
    id: profile?.id || "",
    provider: profile?.provider || "",
    status: profile?.status || "",
    configured: profile?.secret_binding?.configured === true,
    mode: profile?.secret_binding?.mode || "",
    hint: profile?.secret_binding?.display_hint || "",
  };
}

function gongfengRequest(method, pathPart, body) {
  const token = trimEnv("GONGFENG_ACCESS_TOKEN") || trimEnv("GONGFENG_PRIVATE_TOKEN");
  if (!token) fail("missing GONGFENG_ACCESS_TOKEN/GONGFENG_PRIVATE_TOKEN");
  const url = `${gongfengAPIBase()}/${pathPart.replace(/^\/+/, "")}`;
  const res = fetchSync(url, {
    method,
    headers: {
      "content-type": "application/json",
      accept: "application/json",
      "PRIVATE-TOKEN": token,
    },
    body,
  });
  if (!res.ok) fail(`gongfeng ${method} ${pathPart} failed: ${res.status} ${redact(String(res.text || ""))}`);
  return res.json;
}

function gongfengAPIBase() {
  const raw = trimEnv("GONGFENG_API_BASE") || "https://git.code.tencent.com/api/v3";
  const base = raw.replace(/\/+$/, "");
  return base.endsWith("/api/v3") ? base : `${base}/api/v3`;
}

function encodeGongfengProjectID(value) {
  return encodeURIComponent(value).replace(/%2F/g, "%2F");
}

function workspaceRoute(route, workspaceID) {
  const joiner = route.includes("?") ? "&" : "?";
  return `${route}${joiner}workspace_id=${encodeURIComponent(workspaceID)}`;
}

function findWorkspace(token, slug) {
  const payload = get("/api/workspaces", token);
  const list = Array.isArray(payload) ? payload : payload.items || payload.workspaces || [];
  const workspace = list.find((item) => item.slug === slug) || list[0];
  if (!workspace?.id) fail(`workspace ${slug} not found`);
  return workspace;
}

function get(route, token) {
  return request("GET", route, null, token);
}

function post(route, body, token) {
  return request("POST", route, body, token);
}

function put(route, body, token) {
  return request("PUT", route, body, token);
}

function request(method, route, body, token) {
  const res = fetchSync(`${apiURL}${route}`, {
    method,
    headers: {
      "content-type": "application/json",
      ...(token ? { authorization: `Bearer ${token}` } : {}),
    },
    body,
  });
  if (!res.ok) fail(`${method} ${route} failed: ${res.status} ${res.text}`);
  return res.json;
}

function fetchSync(url, options) {
  const script = `
const url = ${JSON.stringify(url)};
const options = ${JSON.stringify({
    method: options.method,
    headers: options.headers,
    body: options.body === undefined || options.body === null ? undefined : JSON.stringify(options.body),
  })};
(async () => {
  const res = await fetch(url, options);
  const text = await res.text();
  let json = null;
  try { json = text ? JSON.parse(text) : null; } catch {}
  process.stdout.write(JSON.stringify({ ok: res.ok, status: res.status, text, json }));
})().catch((error) => {
  process.stderr.write(error.stack || error.message || String(error));
  process.exit(1);
});
`;
  const out = execFileSync(process.execPath, ["-e", script], { encoding: "utf8", maxBuffer: 4 * 1024 * 1024 });
  return JSON.parse(out);
}

function check(name, ok, details = null) {
  evidence.checks.push({ name, ok: Boolean(ok), details });
  if (!ok) fail(`check failed: ${name}`);
}

function fail(message) {
  throw new Error(message);
}

function trimEnv(name) {
  return String(process.env[name] || "").trim();
}

function redact(text) {
  return text.replace(/(PRIVATE-TOKEN|Private-Token|Authorization|token)["'\s:=]+[^"',\s}]+/gi, "$1=<redacted>");
}

function containsSecretText(text) {
  return /GONGFENG_ACCESS_TOKEN=\S+|GONGFENG_PRIVATE_TOKEN=\S+|PRIVATE-TOKEN["'\s:=]+[A-Za-z0-9._-]+|Authorization["'\s:=]+Bearer\s+[A-Za-z0-9._-]+/i.test(text);
}
