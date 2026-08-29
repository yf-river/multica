import { spawn, spawnSync } from "node:child_process";
import { randomUUID } from "node:crypto";
import {
  closeSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  openSync,
  readFileSync,
  readdirSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import pg from "pg";

import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";
import { readGoalTestEnvFile } from "./lib/goal-test-audit-env.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const runEnv = readGoalTestEnvFile(path.join(repoRoot, ".run/env/goal-test-int.env"));
const apiBase = runEnv.REMOTE_API_URL || "http://127.0.0.1:18762";
const browserURL = runEnv.LOCAL_FRONTEND_URL || "http://127.0.0.1:13682";
const databaseURL = runEnv.DATABASE_URL;
const account = process.env.GOAL_TEST_ACCOUNT || "develop";
const password = process.env.GOAL_TEST_PASSWORD || "develop123";
const workspaceSlug = process.env.GOAL_TEST_WORKSPACE_SLUG || "ai-studio";
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");
const outputDir = acceptanceDir(repoRoot);
const deploymentPath = path.join(repoRoot, ".run/deployments/goal-test-int.json");
const environmentScript = path.join(repoRoot, "scripts/goal-test-environments.mjs");

if (!databaseURL) throw new Error("missing integration DATABASE_URL");
if (runEnv.GOAL_TEST_ENV !== "int" || new URL(databaseURL).pathname !== "/multica_goal_test_int") {
  throw new Error("refusing life chaos run outside the canonical integration database");
}

mkdirSync(outputDir, { recursive: true });
const outputPath = path.join(outputDir, `life-chaos-${stamp}.json`);
const latestPath = path.join(outputDir, "life-chaos-latest.json");
const db = new pg.Pool({ connectionString: databaseURL, max: 4 });
const fixtureRoot = mkdtempSync(path.join(os.tmpdir(), "multica-life-chaos-"));
const evidence = {
  schema: "multica.goal_test.life_chaos.v1",
  generated_at: generatedAt,
  deployment_commit: runEnv.BUILD_VERSION || "",
  workspace_slug: workspaceSlug,
  checks: [],
  ok: false,
};

let token = "";
let workspace;
let runtime;
let agent;
let project;
let projectResource;
const issues = [];

try {
  token = (await request("/auth/login", { method: "POST", body: { account, password } }, "", "")).body.token;
  if (!token) throw new Error("login returned no token");
  const workspaces = (await request("/api/workspaces")).body;
  workspace = workspaces.find((item) => item.slug === workspaceSlug);
  if (!workspace) throw new Error(`workspace ${workspaceSlug} not found`);
  runtime = await findCodeBuddyRuntime(60_000);

  await establishExecutionFixture();
  await check("database pool reconnects after every server connection is terminated", exerciseDatabaseDisconnect);
  await check("frontend backend and daemon restart without losing persisted life state", exerciseServiceRestarts);
  await check("daemon crash during a real Bash tool call reaches a recoverable terminal state", exerciseDaemonCrashDuringTool);
  await check("deleting a running task source interrupts the old execution and prevents write-back", exerciseDeleteRunningSource);
  await check("current frontend reads from an older backend and new writes fail explicitly", exerciseVersionSkew);
  await check("a backup created after permanent deletion cannot restore the deleted memory", exerciseBackupAfterPermanentDelete);

  evidence.ok = true;
  persist();
  console.log(JSON.stringify({ ok: true, artifact: outputPath, checks: evidence.checks }, null, 2));
} catch (error) {
  evidence.error = error instanceof Error ? error.stack : String(error);
  persist();
  throw error;
} finally {
  await ensureCurrentServices().catch(() => {});
  await cleanupFixtures().catch(() => {});
  await db.end();
  rmSync(fixtureRoot, { recursive: true, force: true });
}

async function check(name, action) {
  const started = Date.now();
  const detail = await action();
  evidence.checks.push({ name, ok: true, elapsed_ms: Date.now() - started, detail });
  persist();
}

async function establishExecutionFixture() {
  const repoDir = path.join(fixtureRoot, "repo");
  mkdirSync(repoDir, { recursive: true });
  writeFileSync(path.join(repoDir, "README.md"), "# Multica chaos acceptance fixture\n");
  run("git", ["init", "-q"], { cwd: repoDir });
  run("git", ["add", "README.md"], { cwd: repoDir });
  run("git", ["-c", "user.name=Multica Acceptance", "-c", "user.email=acceptance@localhost", "commit", "-qm", "fixture"], { cwd: repoDir });

  agent = (await request("/api/agents", {
    method: "POST",
    headers: { "Idempotency-Key": randomUUID() },
    body: {
      name: `破坏性验收智能体-${stamp}`,
      description: "只用于联调环境真实进程与取消恢复验收",
      instructions: "按用户要求执行；当要求等待时必须通过 Bash 工具执行给定的 sleep 命令。不要修改任何文件。",
      runtime_id: runtime.id,
      runtime_config: {},
      custom_env: {},
      custom_args: [],
      scope: "workspace",
      max_concurrent_tasks: 1,
      model: "deepseek-v4-pro-ioa",
    },
  })).body;
  project = (await request("/api/projects", {
    method: "POST",
    headers: { "Idempotency-Key": randomUUID() },
    body: { title: `破坏性验收项目-${stamp}` },
  })).body;
  projectResource = (await request(`/api/projects/${project.id}/resources`, {
    method: "POST",
    headers: { "Idempotency-Key": randomUUID() },
    body: {
      resource_type: "local_directory",
      resource_ref: { local_path: repoDir, daemon_id: "goal-test-codex-int", label: "破坏性验收临时仓库" },
      label: "破坏性验收临时仓库",
    },
  })).body;
}

async function exerciseDatabaseDisconnect() {
  const before = await request("/api/life/memories");
  const client = await db.connect();
  let terminated;
  try {
    const result = await client.query(
      `SELECT count(*)::int AS count FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`,
    );
    terminated = result.rows[0].count;
    await client.query(
      `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()`,
    );
  } finally {
    client.release();
  }
  const after = await waitForRequest("/api/life/memories", 20_000);
  if ((before.body.memories || []).length !== (after.body.memories || []).length) {
    throw new Error("life memory count changed after database reconnection");
  }
  return { terminated_connections: terminated, memory_count: (after.body.memories || []).length };
}

async function exerciseServiceRestarts() {
  const before = await databaseCounts();
  await killDeployedProcess("web", "next-server");
  runEnvironment("dev-ui-start");
  await waitForHTTP(`${browserURL}/login`, 60_000);

  await killDeployedProcess("server", "server/bin/server");
  runEnvironment("dev-server");
  await waitForHTTP(`${apiBase}/health`, 60_000);

  await killDeployedProcess("daemon", "multica daemon start");
  runEnvironment("dev-daemon");
  await waitForRuntimeOnline(runtime.id, 60_000);
  const after = await databaseCounts();
  if (JSON.stringify(before) !== JSON.stringify(after)) {
    throw new Error(`persisted life counts changed across restarts: before=${JSON.stringify(before)} after=${JSON.stringify(after)}`);
  }
  return { counts: after, pids: readDeployment().pids };
}

async function exerciseDaemonCrashDuringTool() {
  const { issue, task } = await startSleepingIssue("Daemon 崩溃中途恢复");
  const deployment = readDeployment();
  const sleepPID = await waitForSleepDescendant(deployment.pids.daemon, 180_000);
  const oldDescendants = descendantsOf(deployment.pids.daemon);
  process.kill(deployment.pids.daemon, "SIGKILL");
  await waitForProcessExit(deployment.pids.daemon, 15_000);
  await delay(1_000);
  const surviving = oldDescendants.filter(isProcessRunning);
  if (surviving.length > 0) {
    for (const pid of surviving.reverse()) safeKill(pid, "SIGKILL");
    runEnvironment("dev-daemon");
    throw new Error(`daemon crash left child processes running: ${surviving.join(",")}`);
  }
  runEnvironment("dev-daemon");
  await waitForRuntimeOnline(runtime.id, 60_000);
  const terminal = await waitForTaskTerminal(task.id, 8 * 60_000);
  await waitForNoActiveTasks(agent.id, 8 * 60_000);
  const active = await countActiveTasks(agent.id);
  issues.push(issue.id);
  return { issue_id: issue.id, task_id: task.id, sleep_pid: sleepPID, terminal_status: terminal.status, active_tasks: active };
}

async function exerciseDeleteRunningSource() {
  const { issue, task } = await startSleepingIssue("运行中删除来源");
  const daemonPID = readDeployment().pids.daemon;
  const sleepPID = await waitForSleepDescendant(daemonPID, 180_000);
  const deletion = await request(`/api/issues/${issue.id}`, { method: "DELETE" });
  if (deletion.status !== 204) throw new Error(`delete running issue returned ${deletion.status}`);
  await waitForProcessExit(sleepPID, 30_000);
  const { rows } = await db.query(
    `SELECT
       (SELECT count(*)::int FROM issue WHERE id=$1) AS issues,
       (SELECT count(*)::int FROM agent_task_queue WHERE id=$2) AS tasks,
       (SELECT count(*)::int FROM comment WHERE issue_id=$1) AS comments`,
    [issue.id, task.id],
  );
  if (rows[0].issues !== 0 || rows[0].tasks !== 0 || rows[0].comments !== 0) {
    throw new Error(`deleted task source was recreated: ${JSON.stringify(rows[0])}`);
  }
  return { issue_id: issue.id, task_id: task.id, sleep_pid: sleepPID, ...rows[0] };
}

async function exerciseVersionSkew() {
  const oldCommit = "3966fb74e569f1c178c3bb78731aa0b062bc4df1";
  const sourceDir = path.join(fixtureRoot, "old-server-source");
  const archive = path.join(fixtureRoot, "old-server.tar");
  mkdirSync(sourceDir, { recursive: true });
  run("git", ["archive", "--format=tar", `--output=${archive}`, oldCommit], { cwd: repoRoot });
  run("tar", ["-xf", archive, "-C", sourceDir]);
  run("go", ["build", "-buildvcs=false", "-o", path.join(fixtureRoot, "old-server"), "./cmd/server"], {
    cwd: path.join(sourceDir, "server"),
    env: { ...process.env, GOWORK: "off", TMPDIR: "/data/tmp/goal-test" },
  });

  await killDeployedProcess("daemon", "multica daemon start");
  await killDeployedProcess("server", "server/bin/server");
  const oldLog = path.join(outputDir, `version-skew-old-server-${stamp}.log`);
  const oldPID = startDetached(path.join(fixtureRoot, "old-server"), [], oldLog);
  try {
    await waitForHTTP(`${apiBase}/health`, 60_000);
    const login = await fetch(`${browserURL}/auth/login`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ account, password }),
    });
    const loginBody = await login.json();
    if (!login.ok || !loginBody.token) throw new Error(`old backend login failed with ${login.status}`);
    const headers = { Authorization: `Bearer ${loginBody.token}`, "X-Workspace-ID": workspace.id };
    const read = await fetch(`${browserURL}/api/workspaces`, { headers });
    const write = await fetch(`${browserURL}/api/life/proposals`, {
      method: "POST",
      headers: { ...headers, "Content-Type": "application/json", "Idempotency-Key": randomUUID() },
      body: JSON.stringify({ proposal_type: "workspace_issue", title: "版本错位写入", payload: { issue_title: "不应创建" } }),
    });
    if (read.status !== 200) throw new Error(`older backend safe read returned ${read.status}`);
    if (write.status !== 404) throw new Error(`new write against older backend returned ${write.status}, want explicit 404`);
    return { old_commit: oldCommit.slice(0, 12), old_server_pid: oldPID, read_status: read.status, write_status: write.status, old_server_log: oldLog };
  } finally {
    safeKill(oldPID, "SIGTERM");
    await waitForProcessExit(oldPID, 15_000).catch(() => safeKill(oldPID, "SIGKILL"));
    runEnvironment("dev-server");
    runEnvironment("dev-daemon");
    await waitForRuntimeOnline(runtime.id, 60_000);
  }
}

async function exerciseBackupAfterPermanentDelete() {
  const memories = (await request("/api/life/memories")).body.memories || [];
  const source = memories.find((item) => item.status !== "archived");
  if (!source) throw new Error("backup deletion acceptance needs one existing active memory as evidence");
  const content = `这条记忆永久删除后不得出现在新备份中-${randomUUID()}`;
  const memory = (await request("/api/life/memories", {
    method: "POST",
    body: {
      kind: "fact",
      content,
      confidence: 1,
      urgency: 0,
      uncertainty: "",
      evidence: [{ source_type: "memory", source_id: source.id, excerpt: source.content, stance: "context" }],
    },
  })).body;
  const deletion = await request(`/api/life/memories/${memory.id}`, { method: "DELETE" });
  if (deletion.status !== 204) throw new Error(`permanent memory deletion returned ${deletion.status}`);
  const remaining = await db.query(`SELECT count(*)::int AS count FROM life_memory WHERE id=$1 OR content=$2`, [memory.id, content]);
  if (remaining.rows[0].count !== 0) throw new Error("permanently deleted memory still exists before backup");

  const backupDir = path.join(fixtureRoot, "backup");
  const backupFile = path.join(backupDir, "after-delete.dump");
  mkdirSync(backupDir, { recursive: true });
  const version = await db.query("SHOW server_version_num");
  const postgresMajor = Math.floor(Number(version.rows[0].server_version_num) / 10_000);
  dockerPostgres(postgresMajor, ["pg_dump", "--format=custom", "--file=/backup/after-delete.dump", databaseURL], backupDir);

  const restoreName = `multica_goal_test_restore_${process.pid}_${Date.now()}`;
  const adminURL = databaseURLFor(databaseURL, "postgres");
  const restoreURL = databaseURLFor(databaseURL, restoreName);
  const admin = new pg.Client({ connectionString: adminURL });
  await admin.connect();
  try {
    await admin.query(`CREATE DATABASE ${quoteIdentifier(restoreName)}`);
    dockerPostgres(postgresMajor, ["pg_restore", "--no-owner", "--no-privileges", `--dbname=${restoreURL}`, "/backup/after-delete.dump"], backupDir);
    const restored = new pg.Client({ connectionString: restoreURL });
    await restored.connect();
    try {
      const result = await restored.query(
        `SELECT
           (SELECT count(*)::int FROM life_memory WHERE id=$1 OR content=$2) AS deleted_count,
           (SELECT count(*)::int FROM workspace) AS workspace_count,
           (SELECT count(*)::int FROM life_material) AS material_count`,
        [memory.id, content],
      );
      if (result.rows[0].deleted_count !== 0 || result.rows[0].workspace_count < 1) {
        throw new Error(`restored backup failed deletion invariant: ${JSON.stringify(result.rows[0])}`);
      }
      return { memory_id: memory.id, postgres_major: postgresMajor, backup_file: backupFile, restore_database: restoreName, ...result.rows[0] };
    } finally {
      await restored.end();
    }
  } finally {
    await admin.query(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1`, [restoreName]).catch(() => {});
    await admin.query(`DROP DATABASE IF EXISTS ${quoteIdentifier(restoreName)}`).catch(() => {});
    await admin.end();
  }
}

async function startSleepingIssue(label) {
  const issue = (await request("/api/issues", {
    method: "POST",
    headers: { "Idempotency-Key": randomUUID() },
    body: {
      title: `${label}-${randomUUID().slice(0, 8)}`,
      description: "必须先调用 Bash 工具执行命令 `sleep 90`，等待结束后只回复：验收完成。不要读取或修改文件。",
      status: "todo",
      priority: "medium",
      assignee_type: "agent",
      assignee_id: agent.id,
      project_id: project.id,
    },
  })).body;
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    const { rows } = await db.query(
      `SELECT id::text, status FROM agent_task_queue WHERE issue_id=$1 AND agent_id=$2 ORDER BY created_at DESC LIMIT 1`,
      [issue.id, agent.id],
    );
    if (rows[0] && ["running", "completed", "failed"].includes(rows[0].status)) return { issue, task: rows[0] };
    await delay(500);
  }
  throw new Error(`task for issue ${issue.id} did not start`);
}

async function waitForSleepDescendant(daemonPID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    for (const pid of descendantsOf(daemonPID)) {
      if (/(^|\s|\/)sleep\s+90(\s|$)/.test(processCommand(pid))) return pid;
    }
    await delay(500);
  }
  throw new Error(`daemon ${daemonPID} never entered the required sleep 90 Bash tool call`);
}

async function waitForTaskTerminal(taskID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const { rows } = await db.query(`SELECT status, failure_reason, error FROM agent_task_queue WHERE id=$1`, [taskID]);
    if (!rows[0]) return { status: "deleted" };
    if (["completed", "failed", "cancelled"].includes(rows[0].status)) return rows[0];
    await delay(1_000);
  }
  throw new Error(`task ${taskID} did not reach terminal state`);
}

async function countActiveTasks(agentID) {
  const { rows } = await db.query(
    `SELECT count(*)::int AS count FROM agent_task_queue WHERE agent_id=$1 AND status IN ('queued','dispatched','running')`,
    [agentID],
  );
  return rows[0].count;
}

async function waitForNoActiveTasks(agentID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await countActiveTasks(agentID) === 0) return;
    await delay(1_000);
  }
  throw new Error(`daemon recovery left active tasks for agent ${agentID}`);
}

async function databaseCounts() {
  const { rows } = await db.query(
    `SELECT
       (SELECT count(*)::int FROM life_memory WHERE workspace_id=$1) AS memories,
       (SELECT count(*)::int FROM life_observer WHERE workspace_id=$1) AS observers,
       (SELECT count(*)::int FROM life_experiment WHERE workspace_id=$1) AS experiments,
       (SELECT count(*)::int FROM life_chronicle_entry WHERE workspace_id=$1) AS chronicles`,
    [workspace.id],
  );
  return rows[0];
}

async function killDeployedProcess(kind, expectedCommand) {
  const pid = Number(readDeployment().pids?.[kind]);
  const command = processCommand(pid);
  if (!Number.isInteger(pid) || pid < 2 || !command.includes(expectedCommand)) {
    throw new Error(`refusing to stop unexpected ${kind} process pid=${pid} command=${command}`);
  }
  process.kill(pid, "SIGKILL");
  await waitForProcessExit(pid, 15_000);
}

async function ensureCurrentServices() {
  const checks = [
    ["server", "server/bin/server", "dev-server"],
    ["web", "next-server", "dev-ui-start"],
    ["daemon", "multica daemon start", "dev-daemon"],
  ];
  for (const [kind, expected, command] of checks) {
    const pid = Number(readDeployment().pids?.[kind]);
    if (!isProcessRunning(pid) || !processCommand(pid).includes(expected)) runEnvironment(command);
  }
}

function runEnvironment(command) {
  run(process.execPath, [environmentScript, command, "int"], { cwd: repoRoot });
}

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd || repoRoot,
    env: options.env || process.env,
    encoding: "utf8",
    timeout: options.timeout || 15 * 60_000,
  });
  if (result.status !== 0) {
    const output = redact(`${result.stdout || ""}\n${result.stderr || ""}`).slice(-8_000);
    throw new Error(`${command} failed with ${result.status}: ${output}`);
  }
  return result;
}

function dockerPostgres(major, args, backupDir) {
  run("docker", ["run", "--rm", "-v", `${backupDir}:/backup`, `postgres:${major}`, ...args], { timeout: 20 * 60_000 });
}

function startDetached(command, args, logFile) {
  const fd = openSync(logFile, "a");
  const child = spawn(command, args, { cwd: repoRoot, env: { ...process.env, ...runEnv }, detached: true, stdio: ["ignore", fd, fd] });
  child.unref();
  closeSync(fd);
  return child.pid;
}

function readDeployment() {
  if (!existsSync(deploymentPath)) throw new Error("integration deployment metadata is missing");
  return JSON.parse(readFileSync(deploymentPath, "utf8"));
}

function processCommand(pid) {
  if (!Number.isInteger(Number(pid)) || !existsSync(`/proc/${pid}/cmdline`)) return "";
  return readFileSync(`/proc/${pid}/cmdline`).toString().replaceAll("\0", " ").trim();
}

function processParent(pid) {
  try {
    const match = readFileSync(`/proc/${pid}/status`, "utf8").match(/^PPid:\s+(\d+)/m);
    return match ? Number(match[1]) : 0;
  } catch {
    return 0;
  }
}

function descendantsOf(rootPID) {
  const all = readdirSync("/proc").map(Number).filter(Number.isInteger);
  const descendants = [];
  const parents = new Set([Number(rootPID)]);
  let changed = true;
  while (changed) {
    changed = false;
    for (const pid of all) {
      if (parents.has(pid)) continue;
      if (parents.has(processParent(pid))) {
        parents.add(pid);
        descendants.push(pid);
        changed = true;
      }
    }
  }
  return descendants;
}

function isProcessRunning(pid) {
  if (!Number.isInteger(Number(pid)) || Number(pid) < 2) return false;
  try {
    process.kill(Number(pid), 0);
    return true;
  } catch {
    return false;
  }
}

function safeKill(pid, signal) {
  if (!isProcessRunning(pid)) return;
  try { process.kill(Number(pid), signal); } catch {}
}

async function waitForProcessExit(pid, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (!isProcessRunning(pid)) return;
    await delay(100);
  }
  throw new Error(`process ${pid} did not exit`);
}

async function waitForHTTP(url, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {}
    await delay(250);
  }
  throw new Error(`${url} did not become ready`);
}

async function waitForRuntimeOnline(runtimeID, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const { rows } = await db.query(`SELECT status FROM agent_runtime WHERE id=$1`, [runtimeID]);
    if (rows[0]?.status === "online") return;
    await delay(500);
  }
  throw new Error(`runtime ${runtimeID} did not return online`);
}

async function findCodeBuddyRuntime(timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const runtimes = (await request("/api/runtimes?owner=me")).body;
    const item = runtimes.find((candidate) => candidate.provider === "codebuddy" && candidate.status === "online");
    if (item) return item;
    await delay(500);
  }
  throw new Error("no online CodeBuddy runtime found");
}

async function waitForRequest(pathname, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let last;
  while (Date.now() < deadline) {
    try { return await request(pathname); } catch (error) { last = error; }
    await delay(250);
  }
  throw last || new Error(`${pathname} did not recover`);
}

async function cleanupFixtures() {
  for (const id of issues) await request(`/api/issues/${id}`, { method: "DELETE" }).catch(() => {});
  if (projectResource?.id) await request(`/api/projects/${project.id}/resources/${projectResource.id}`, { method: "DELETE" }).catch(() => {});
  if (project?.id) await request(`/api/projects/${project.id}`, { method: "DELETE" }).catch(() => {});
  if (agent?.id) await request(`/api/agents/${agent.id}`, { method: "DELETE" }).catch(() => {});
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
  if (requireOK && !response.ok) throw new Error(`${options.method || "GET"} ${pathname}: ${response.status} ${typeof body === "string" ? body : JSON.stringify(body)}`);
  return { status: response.status, body };
}

function databaseURLFor(value, databaseName) {
  const url = new URL(value);
  url.pathname = `/${databaseName}`;
  return url.toString();
}

function quoteIdentifier(value) {
  return `"${String(value).replaceAll('"', '""')}"`;
}

function redact(value) {
  return String(value).replace(/:\/\/([^:]+):([^@]+)@/g, "://$1:<redacted>@");
}

function persist() {
  writeFileSync(outputPath, `${JSON.stringify(evidence, null, 2)}\n`);
  writeFileSync(latestPath, `${JSON.stringify(evidence, null, 2)}\n`);
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
