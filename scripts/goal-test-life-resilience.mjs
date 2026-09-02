import { randomUUID } from "node:crypto";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";

import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";
import { readGoalTestEnvFile } from "./lib/goal-test-audit-env.mjs";
import { launchGoalTestBrowser } from "./lib/goal-test-browser-audit.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const runEnv = readGoalTestEnvFile(path.join(repoRoot, ".run/env/goal-test-int.env"));
const deploymentPath = path.join(repoRoot, ".run/deployments/goal-test-int.json");
const deployment = JSON.parse(readFileSync(deploymentPath, "utf8"));
const deploymentCommit = String(deployment.commit || deployment.build_version || "").trim();
const apiBase = runEnv.REMOTE_API_URL || "http://127.0.0.1:18762";
const browserURL = runEnv.LOCAL_FRONTEND_URL || "http://127.0.0.1:13682";
const databaseURL = runEnv.DATABASE_URL;
const account = process.env.GOAL_TEST_ACCOUNT || "develop";
const password = process.env.GOAL_TEST_PASSWORD || "develop123";
const workspaceSlug = process.env.GOAL_TEST_WORKSPACE_SLUG || "ai-studio";
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const outputDir = acceptanceDir(repoRoot);

if (!databaseURL) throw new Error("missing integration DATABASE_URL");
if (!deploymentCommit) throw new Error("deployment metadata is missing commit");
if (runEnv.GOAL_TEST_ENV !== "int" || new URL(databaseURL).pathname !== "/multica_goal_test_int") {
  throw new Error("refusing life resilience run outside the canonical integration database");
}

mkdirSync(outputDir, { recursive: true });
const outputPath = path.join(outputDir, `life-resilience-${stamp}.json`);
const latestPath = path.join(outputDir, "life-resilience-latest.json");
const db = new pg.Pool({ connectionString: databaseURL, max: 4 });
const evidence = {
  schema: "multica.goal_test.life_resilience.v1",
  generated_at: generatedAt,
  deployment_commit: deploymentCommit,
  workspace_slug: workspaceSlug,
  checks: [],
  ok: false,
};

let token = "";
let workspace;
let memberFixture = null;
let workspaceFixture = null;

try {
  token = (await request("/auth/login", { method: "POST", body: { account, password } }, "", "")).body.token;
  if (!token) throw new Error("login returned no token");
  const workspaces = (await request("/api/workspaces")).body;
  workspace = workspaces.find((item) => item.slug === workspaceSlug);
  if (!workspace) throw new Error(`workspace ${workspaceSlug} not found`);

  await check("two independent browsers serialize confirmation", exerciseConcurrentConfirmation);
  await check("rejected and expired proposals never execute", exerciseInactiveProposals);
  await check("failed proposal execution rolls back every write", exerciseTransactionRollback);
  await check("life data is isolated by user and workspace", () => exerciseIsolation(workspaces));

  evidence.ok = true;
  persist();
  console.log(JSON.stringify({ ok: true, artifact: outputPath, checks: evidence.checks }, null, 2));
} catch (error) {
  evidence.error = error instanceof Error ? error.stack : String(error);
  persist();
  throw error;
} finally {
  if (memberFixture) {
    await request(`/api/workspaces/${workspace.id}/members/${memberFixture.id}`, { method: "DELETE" }).catch(() => {});
  }
  if (workspaceFixture) {
    await request(`/api/workspaces/${workspaceFixture.id}`, { method: "DELETE" }).catch(() => {});
  }
  await db.end();
}

async function check(name, action) {
  const started = Date.now();
  const detail = await action();
  evidence.checks.push({ name, ok: true, elapsed_ms: Date.now() - started, detail });
  persist();
}

async function exerciseConcurrentConfirmation() {
  const title = `双浏览器并发确认-${stamp}`;
  const proposal = await createProposal("workspace_issue", title, {
    issue_title: title,
    issue_description: "两个独立浏览器同时确认时只能执行一次。",
  });
  const clients = await Promise.all([
    launchGoalTestBrowser(browserURL, token),
    launchGoalTestBrowser(browserURL, token),
  ]);
  let statuses;
  try {
    const pages = await Promise.all(clients.map(({ context }) => context.newPage()));
    await Promise.all(pages.map((page) => page.goto(`${browserURL}/${workspace.slug}/life`, {
      waitUntil: "domcontentloaded",
      timeout: 15_000,
    })));
    statuses = await Promise.all(pages.map((page) => browserConfirm(page, proposal.id)));
  } finally {
    await Promise.all(clients.map(({ browser }) => browser.close()));
  }
  statuses.sort((left, right) => left - right);
  if (statuses[0] !== 201 || statuses[1] !== 409) {
    throw new Error(`concurrent confirmation statuses ${statuses.join(",")}, want 201,409`);
  }
  const duplicate = await request(`/api/life/proposals/${proposal.id}/confirm`, { method: "POST" }, token, workspace.id, false);
  if (duplicate.status !== 409) throw new Error(`duplicate confirmation returned ${duplicate.status}, want 409`);
  const { rows } = await db.query(
    `SELECT
       (SELECT count(*)::int FROM issue WHERE workspace_id=$1 AND title=$2) AS issue_count,
       (SELECT status FROM life_action_proposal WHERE id=$3) AS proposal_status`,
    [workspace.id, title, proposal.id],
  );
  if (rows[0].issue_count !== 1 || rows[0].proposal_status !== "executed") {
    throw new Error(`concurrent confirmation persisted issue=${rows[0].issue_count} proposal=${rows[0].proposal_status}`);
  }
  return { proposal_id: proposal.id, statuses, duplicate_status: duplicate.status, issue_count: 1 };
}

async function browserConfirm(page, proposalID) {
  return page.evaluate(async ({ id, workspaceID }) => {
    const csrf = document.cookie
      .split("; ")
      .find((item) => item.startsWith("multica_csrf="))
      ?.slice("multica_csrf=".length);
    const response = await fetch(`/api/life/proposals/${id}/confirm`, {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": decodeURIComponent(csrf || ""),
        "X-Workspace-ID": workspaceID,
      },
    });
    return response.status;
  }, { id: proposalID, workspaceID: workspace.id });
}

async function exerciseInactiveProposals() {
  const rejectedTitle = `拒绝提案不执行-${stamp}`;
  const rejected = await createProposal("workspace_issue", rejectedTitle, { issue_title: rejectedTitle });
  const rejection = await request(`/api/life/proposals/${rejected.id}/reject`, { method: "POST" });
  if (rejection.status !== 200) throw new Error(`reject proposal returned ${rejection.status}`);
  const rejectedConfirm = await request(`/api/life/proposals/${rejected.id}/confirm`, { method: "POST" }, token, workspace.id, false);
  if (rejectedConfirm.status !== 409) throw new Error(`confirm rejected proposal returned ${rejectedConfirm.status}`);

  const expiredTitle = `过期提案不执行-${stamp}`;
  const expired = await createProposal("workspace_issue", expiredTitle, { issue_title: expiredTitle });
  await db.query(`UPDATE life_action_proposal SET expires_at=now()-interval '1 minute' WHERE id=$1`, [expired.id]);
  const deadline = Date.now() + 30_000;
  let expiredStatus = "";
  while (Date.now() < deadline) {
    const { rows } = await db.query(`SELECT status FROM life_action_proposal WHERE id=$1`, [expired.id]);
    expiredStatus = rows[0]?.status || "";
    if (expiredStatus === "expired") break;
    await delay(1_000);
  }
  if (expiredStatus !== "expired") throw new Error(`proposal scheduler left expired proposal as ${expiredStatus}`);
  const expiredConfirm = await request(`/api/life/proposals/${expired.id}/confirm`, { method: "POST" }, token, workspace.id, false);
  if (expiredConfirm.status !== 409) throw new Error(`confirm expired proposal returned ${expiredConfirm.status}`);
  const { rows } = await db.query(
    `SELECT count(*)::int AS count FROM issue WHERE workspace_id=$1 AND title=ANY($2::text[])`,
    [workspace.id, [rejectedTitle, expiredTitle]],
  );
  if (rows[0].count !== 0) throw new Error(`inactive proposals created ${rows[0].count} issues`);
  return {
    rejected_proposal_id: rejected.id,
    expired_proposal_id: expired.id,
    rejected_confirmation_status: rejectedConfirm.status,
    expired_confirmation_status: expiredConfirm.status,
    issue_count: 0,
  };
}

async function exerciseTransactionRollback() {
  const title = `事务失败不留半成品-${stamp}`;
  const issueTitle = `${title}-任务`;
  const startsAt = new Date(Date.now() + 60_000);
  const proposal = await createProposal("experiment_start", title, {
    problem: "验证提案事务失败不会留下实验、任务或轮次",
    hypothesis: "引用不存在的记忆会使整次确认回滚",
    method: { collection: "自然材料" },
    plan: { burden: "无" },
    starts_at: startsAt.toISOString(),
    ends_at: new Date(startsAt.getTime() + 86_400_000).toISOString(),
    memory_ids: [randomUUID()],
    issue_title: issueTitle,
  });
  const before = await db.query(`SELECT issue_counter FROM workspace WHERE id=$1`, [workspace.id]);
  const confirmation = await request(`/api/life/proposals/${proposal.id}/confirm`, { method: "POST" }, token, workspace.id, false);
  if (confirmation.status !== 400) throw new Error(`invalid experiment confirmation returned ${confirmation.status}`);
  const { rows } = await db.query(
    `SELECT
       (SELECT count(*)::int FROM life_experiment WHERE workspace_id=$1 AND title=$2) AS experiments,
       (SELECT count(*)::int FROM issue WHERE workspace_id=$1 AND title=$3) AS issues,
       (SELECT count(*)::int FROM life_experiment_round WHERE proposal_id=$4) AS rounds,
       (SELECT issue_counter FROM workspace WHERE id=$1) AS issue_counter`,
    [workspace.id, title, issueTitle, proposal.id],
  );
  if (rows[0].experiments !== 0 || rows[0].issues !== 0 || rows[0].rounds !== 0 || rows[0].issue_counter !== before.rows[0].issue_counter) {
    throw new Error(`proposal rollback leaked ${JSON.stringify(rows[0])}`);
  }
  return { proposal_id: proposal.id, confirmation_status: confirmation.status, ...rows[0] };
}

async function exerciseIsolation(workspaces) {
  const primary = await request("/api/life/memories");
  const primaryIDs = new Set((primary.body.memories || []).map((item) => item.id));
  if (primaryIDs.size === 0) throw new Error("primary decade user has no life memories to isolate");

  let otherWorkspace = workspaces.find((item) => item.id !== workspace.id);
  if (!otherWorkspace) {
    const created = await request("/api/workspaces", {
      method: "POST",
      headers: { "Idempotency-Key": randomUUID() },
      body: { name: "人生隔离验收工作区", slug: `life-isolation-${Date.now()}` },
    });
    workspaceFixture = created.body;
    otherWorkspace = workspaceFixture;
  }
  const otherWorkspaceMemories = await request("/api/life/memories", {}, token, otherWorkspace.id);
  const leakedAcrossWorkspace = (otherWorkspaceMemories.body.memories || []).filter((item) => primaryIDs.has(item.id));
  if (leakedAcrossWorkspace.length > 0) throw new Error("life memories leaked across workspaces");

  const fixtureAccount = `life_resilience_${Date.now()}`;
  const fixturePassword = `Life9a-${randomUUID().slice(0, 12)}`;
  const created = await request(`/api/workspaces/${workspace.id}/members`, {
    method: "POST",
    headers: { "Idempotency-Key": randomUUID() },
    body: { account: fixtureAccount, name: "人生隔离验收用户", password: fixturePassword, role: "member" },
  });
  memberFixture = created.body;
  const memberLogin = await request("/auth/login", {
    method: "POST",
    body: { account: fixtureAccount, password: fixturePassword },
  }, "", "");
  const memberMemories = await request("/api/life/memories", {}, memberLogin.body.token, workspace.id);
  const leakedAcrossUsers = (memberMemories.body.memories || []).filter((item) => primaryIDs.has(item.id));
  if (leakedAcrossUsers.length > 0) throw new Error("life memories leaked across users");
  return {
    primary_memory_count: primaryIDs.size,
    second_workspace_id: otherWorkspace.id,
    shared_memory_ids_across_workspaces: 0,
    fixture_user_id: memberFixture.user_id,
    shared_memory_ids_across_users: 0,
  };
}

async function createProposal(proposalType, title, payload) {
  return (await request("/api/life/proposals", {
    method: "POST",
    body: { proposal_type: proposalType, title, summary: "联调环境破坏性验收", payload },
  })).body;
}

async function request(pathname, options = {}, authToken = token, workspaceID = workspace?.id || "", requireOK = true) {
  const headers = { "Content-Type": "application/json", ...(options.headers || {}) };
  if (authToken) headers.Authorization = `Bearer ${authToken}`;
  if (workspaceID) headers["X-Workspace-ID"] = workspaceID;
  const response = await fetch(`${apiBase}${pathname}`, {
    method: options.method || "GET",
    headers,
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  const text = await response.text();
  let body = null;
  if (text) {
    try { body = JSON.parse(text); } catch { body = text; }
  }
  if (requireOK && !response.ok) {
    throw new Error(`${options.method || "GET"} ${pathname}: ${response.status} ${typeof body === "string" ? body : JSON.stringify(body)}`);
  }
  return { status: response.status, body };
}

function persist() {
  const raw = `${JSON.stringify(evidence, null, 2)}\n`;
  writeFileSync(outputPath, raw);
  writeFileSync(latestPath, raw);
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
