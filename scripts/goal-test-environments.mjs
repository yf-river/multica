import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const runDir = path.join(repoRoot, ".run");
const envDir = path.join(runDir, "env");
const deploymentDir = path.join(runDir, "deployments");
const publicHost = process.env.GOAL_TEST_PUBLIC_HOST || "9.134.129.162";

const profiles = {
  prod: {
    name: "prod",
    label: "生产稳定环境",
    frontendPort: "13680",
    backendPort: "18760",
    databaseName: "multica_goal_test_680",
    daemonProfile: "goal-test-prod",
    daemonID: "goal-test-codex-prod",
    runtimeName: "Goal Test Codex Prod",
    frontendMode: "next-start",
  },
  int: {
    name: "int",
    label: "联调开发环境",
    frontendPort: "13682",
    backendPort: "18762",
    databaseName: "multica_goal_test_int",
    daemonProfile: "goal-test-int",
    daemonID: "goal-test-codex-int",
    runtimeName: "Goal Test Codex Int",
    frontendMode: "next-dev",
  },
};

const command = process.argv[2] || "verify";
const profileName = process.argv[3] && !process.argv[3].startsWith("--") ? process.argv[3] : "prod";
const profile = profiles[profileName];
if (!profile && !(command === "verify" && profileName === "all")) fail(`unknown environment ${profileName}; expected prod, int, or all`);

if (command === "ensure") {
  ensureEnvironment(profile);
  console.log(JSON.stringify(describeEnvironment(profile), null, 2));
} else if (command === "deploy") {
  deployEnvironment(profile, process.argv.includes("--build"));
} else if (command === "verify") {
  const evidence = profileName === "all" ? verifyAll() : verifyTarget(profile);
  console.log(JSON.stringify(evidence, null, 2));
} else {
  fail(`unknown command ${command}`);
}

function ensureEnvironment(item) {
  mkdirSync(envDir, { recursive: true });
  mkdirSync(deploymentDir, { recursive: true });
  const file = envPath(item);
  const base = readEnvFile(path.join(repoRoot, ".env.worktree"));
  const databaseURL = deriveDatabaseURL(base.DATABASE_URL, item.databaseName);
  const frontendURL = `http://${publicHost}:${item.frontendPort}`;
  const lines = [
    `GOAL_TEST_ENV=${item.name}`,
    `GOAL_TEST_ENV_LABEL=${item.label}`,
    `GOAL_TEST_STABLE_COMMIT=${gitText(["rev-parse", "--short=12", "HEAD"])}`,
    `POSTGRES_DB=${item.databaseName}`,
    `POSTGRES_USER=${base.POSTGRES_USER || "multica"}`,
    `POSTGRES_PASSWORD=${base.POSTGRES_PASSWORD || "multica"}`,
    `POSTGRES_PORT=${base.POSTGRES_PORT || "5432"}`,
    `DATABASE_URL=${databaseURL}`,
    `PORT=${item.backendPort}`,
    `JWT_SECRET=${base.JWT_SECRET || "change-me-in-production"}`,
    `MULTICA_SERVER_URL=ws://127.0.0.1:${item.backendPort}/ws`,
    `MULTICA_APP_URL=${frontendURL}`,
    `FRONTEND_PORT=${item.frontendPort}`,
    `FRONTEND_ORIGIN=${frontendURL}`,
    `REMOTE_API_URL=http://127.0.0.1:${item.backendPort}`,
    `NEXT_PUBLIC_API_URL=`,
    `NEXT_PUBLIC_WS_URL=`,
    `CORS_ALLOWED_ORIGINS=${frontendURL},http://127.0.0.1:${item.frontendPort},http://localhost:${item.frontendPort}`,
    `ALLOW_SIGNUP=false`,
    `ALLOWED_ACCOUNTS=goal-test-daemon`,
  ];
  writeFileSync(file, `${lines.join("\n")}\n`);
  return file;
}

function deployEnvironment(item, build) {
  const envFile = ensureEnvironment(item);
  const env = { ...process.env, ...readEnvFile(envFile), HOSTNAME: "0.0.0.0", NEXT_PUBLIC_APP_VERSION: gitText(["rev-parse", "--short=12", "HEAD"]) };
  mkdirSync(runDir, { recursive: true });
  ensureDatabase(env.DATABASE_URL, item.databaseName);
  if (build) {
    run("make", ["build"], env);
    run("pnpm", ["--filter", "@multica/web", "build"], env);
  }
  run("bash", ["-lc", "cd server && ./bin/migrate up"], env);
  if (item.name !== "prod") seedDemoIdentity(env.DATABASE_URL);
  const daemonProfilePath = ensureDaemonProfile(item, env);
  stopPid(pidPath(item, "server"));
  stopPid(pidPath(item, "web"));
  stopPid(pidPath(item, "daemon"));
  killPort(item.backendPort);
  killPort(item.frontendPort);
  killStaleProcesses(item);
  waitForPortFree(item.backendPort, 15_000);
  waitForPortFree(item.frontendPort, 15_000);

  let serverPID = startDetached("./server/bin/server", [], env, logPath(item, "server"));
  waitForHTTP(`http://127.0.0.1:${item.backendPort}/health`, 60_000);
  serverPID = listeningPID(item.backendPort) || serverPID;
  if (item.name !== "prod") refreshDaemonProfileToken(item);
  const webArgs = item.frontendMode === "next-start"
    ? ["--dir", "apps/web", "exec", "next", "start", "-p", item.frontendPort, "-H", "0.0.0.0"]
    : ["--dir", "apps/web", "dev"];
  let webPID = startDetached("pnpm", webArgs, env, logPath(item, "web"));
  waitForHTTP(`http://127.0.0.1:${item.frontendPort}/login`, 90_000);
  webPID = listeningPID(item.frontendPort) || webPID;
  const daemonPID = startDetached("./server/bin/multica", [
    "daemon",
    "start",
    "--foreground",
    "--daemon-id",
    item.daemonID,
    "--runtime-name",
    item.runtimeName,
    "--agent-timeout",
    "5m0s",
    "--max-concurrent-tasks",
    "1",
    "--no-auto-update",
    "--server-url",
    `http://127.0.0.1:${item.backendPort}`,
    "--profile",
    item.daemonProfile,
  ], env, logPath(item, "daemon"));

  writeFileSync(pidPath(item, "server"), `${serverPID}\n`);
  writeFileSync(pidPath(item, "web"), `${webPID}\n`);
  writeFileSync(pidPath(item, "daemon"), `${daemonPID}\n`);
  const metadata = {
    schema: "multica.goal_test.deployment.v1",
    environment: item.name,
    label: item.label,
    commit: gitText(["rev-parse", "--short=12", "HEAD"]),
    branch: gitText(["branch", "--show-current"]),
    frontend_url: `http://${publicHost}:${item.frontendPort}`,
    backend_url: `http://127.0.0.1:${item.backendPort}`,
    frontend_port: item.frontendPort,
    backend_port: item.backendPort,
    database_name: item.databaseName,
    daemon_profile: item.daemonProfile,
    daemon_profile_path: daemonProfilePath,
    daemon_id: item.daemonID,
    frontend_mode: item.frontendMode,
    binary_versions: {
      multica: binaryVersion("./server/bin/multica", ["version"], env),
    },
    build_version: env.NEXT_PUBLIC_APP_VERSION,
    env_file: envFile,
    log_paths: {
      server: logPath(item, "server"),
      web: logPath(item, "web"),
      daemon: logPath(item, "daemon"),
    },
    pids: { server: serverPID, web: webPID, daemon: daemonPID },
    deployed_at: new Date().toISOString(),
  };
  writeFileSync(deploymentPath(item), `${JSON.stringify(metadata, null, 2)}\n`);
  console.log(JSON.stringify(metadata, null, 2));
}

function verifyAll() {
  ensureEnvironment(profiles.prod);
  ensureEnvironment(profiles.int);
  const prod = verifyEnvironment(profiles.prod, true);
  const intEnv = verifyEnvironment(profiles.int, true);
  const isolation = {
    status: prod.ok && intEnv.ok && intEnv.frontend_port !== prod.frontend_port && intEnv.backend_port !== prod.backend_port && intEnv.database_name !== prod.database_name ? "通过" : "失败",
    prod_environment: "prod",
    integration_environment: "int",
    prod_frontend_port: prod.frontend_port,
    integration_frontend_port: intEnv.frontend_port,
    prod_backend_port: prod.backend_port,
    integration_backend_port: intEnv.backend_port,
    prod_database_name: prod.database_name,
    integration_database_name: intEnv.database_name,
  };
  return {
    schema: "multica.goal_test.environment_evidence.v1",
    generated_at: new Date().toISOString(),
    current_commit: gitText(["rev-parse", "--short=12", "HEAD"]),
    prod,
    integration: intEnv,
    isolation,
    ok: prod.ok && isolation.status === "通过",
  };
}

function verifyTarget(item) {
  ensureEnvironment(item);
  const result = verifyEnvironment(item, true);
  return {
    schema: "multica.goal_test.environment_evidence.v1",
    generated_at: new Date().toISOString(),
    current_commit: gitText(["rev-parse", "--short=12", "HEAD"]),
    target: item.name,
    [item.name === "prod" ? "prod" : "integration"]: result,
    ok: result.ok,
  };
}

function verifyEnvironment(item, requireRunning) {
  const env = describeEnvironment(item);
  const metadata = existsSync(deploymentPath(item)) ? JSON.parse(readFileSync(deploymentPath(item), "utf8")) : null;
  const pids = metadata?.pids || {};
  const server = inspectPID(pids.server);
  const web = inspectPID(pids.web);
  const daemon = inspectPID(pids.daemon);
  const currentCommit = gitText(["rev-parse", "--short=12", "HEAD"]);
  const checks = {
    deployment_metadata_exists: Boolean(metadata),
    commit_matches: metadata?.commit === currentCommit,
    server_running: server.running,
    web_running: web.running,
    daemon_running: daemon.running,
    web_is_production_start: item.frontendMode === "next-start"
      ? (/next .*start|next\/dist\/bin\/next start|next-server/.test(web.command) && web.cwd.endsWith("/apps/web") && !web.command.includes("next dev"))
      : true,
    server_binary: server.command.includes("server/bin/server"),
    daemon_profile: daemon.command.includes(`--profile ${item.daemonProfile}`),
    frontend_mode_matches: metadata?.frontend_mode === item.frontendMode,
    database_matches: metadata?.database_name === item.databaseName,
    binary_version_recorded: Boolean(metadata?.binary_versions?.multica || metadata?.binary_versions?.server),
  };
  const ok = Object.values(checks).every(Boolean) || (!requireRunning && Boolean(env));
  return {
    ...env,
    deployment: metadata,
    process: { server, web, daemon },
    checks,
    ok,
    status: ok ? "通过" : "失败",
  };
}

function describeEnvironment(item) {
  ensureEnvironment(item);
  const env = readEnvFile(envPath(item));
  return {
    environment: item.name,
    label: item.label,
    frontend_url: `http://${publicHost}:${item.frontendPort}`,
    backend_url: `http://127.0.0.1:${item.backendPort}`,
    frontend_port: item.frontendPort,
    backend_port: item.backendPort,
    database_name: item.databaseName,
    database_url_redacted: redactDatabaseURL(env.DATABASE_URL || ""),
    daemon_profile: item.daemonProfile,
    daemon_id: item.daemonID,
    env_file: envPath(item),
    frontend_mode: item.frontendMode,
  };
}

function envPath(item) {
  return path.join(envDir, `goal-test-${item.name}.env`);
}

function deploymentPath(item) {
  return path.join(deploymentDir, `goal-test-${item.name}.json`);
}

function pidPath(item, name) {
  return path.join(runDir, `${item.name}-${name}.pid`);
}

function logPath(item, name) {
  return path.join(runDir, `${item.name}-${name}.log`);
}

function readEnvFile(file) {
  if (!existsSync(file)) return {};
  const env = {};
  for (const raw of readFileSync(file, "utf8").split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) env[match[1]] = match[2].replace(/^['"]|['"]$/g, "");
  }
  return env;
}

function deriveDatabaseURL(baseURL, databaseName) {
  if (!baseURL) return `postgres://multica:multica@127.0.0.1:5432/${databaseName}?sslmode=disable`;
  try {
    const url = new URL(baseURL);
    url.pathname = `/${databaseName}`;
    return url.toString();
  } catch {
    return baseURL.replace(/\/[^/?]+(\?.*)?$/, `/${databaseName}$1`);
  }
}

function redactDatabaseURL(value) {
  return value.replace(/:\/\/([^:]+):([^@]+)@/, "://$1:<redacted>@");
}

function ensureDatabase(databaseURL, databaseName) {
  if (!databaseURL) fail("DATABASE_URL is required");
  const adminURL = adminDatabaseURL(databaseURL);
  const escapedName = databaseName.replace(/'/g, "''");
  const res = spawnSync("bash", ["-lc", `node - <<'NODE'\nconst pg = require('pg');\nconst adminUrl = ${JSON.stringify(adminURL)};\nconst databaseName = ${JSON.stringify(databaseName)};\n(async () => {\n  const client = new pg.Client(adminUrl);\n  await client.connect();\n  try {\n    const exists = await client.query('SELECT 1 FROM pg_database WHERE datname = $1', [databaseName]);\n    if (exists.rowCount === 0) {\n      await client.query('CREATE DATABASE \"' + databaseName.replace(/\"/g, '\"\"') + '\"');\n    }\n  } finally {\n    await client.end();\n  }\n})().catch((error) => {\n  console.error(error.stack || error.message || String(error));\n  process.exit(1);\n});\nNODE`], { cwd: repoRoot, encoding: "utf8" });
  if (res.status !== 0) fail(`ensure database ${escapedName} failed\n${res.stderr || res.stdout}`);
}

function seedDemoIdentity(targetDatabaseURL) {
  const sourceEnv = readEnvFile(ensureEnvironment(profiles.prod));
  const sourceDatabaseURL = sourceEnv.DATABASE_URL;
  const res = spawnSync("bash", ["-lc", `node - <<'NODE'\nconst pg = require('pg');\nconst sourceUrl = ${JSON.stringify(sourceDatabaseURL)};\nconst targetUrl = ${JSON.stringify(targetDatabaseURL)};\nconst account = 'goal-test-daemon';\nconst workspaceSlug = 'goal-test-daemon';\nasync function copyTableRow(source, target, table, whereSql, params) {\n  const src = await source.query('SELECT * FROM ' + table + ' WHERE ' + whereSql + ' LIMIT 1', params);\n  if (src.rowCount === 0) throw new Error('missing seed row in ' + table);\n  const row = src.rows[0];\n  const columns = Object.keys(row);\n  const values = columns.map((c) => row[c]);\n  const quoted = columns.map((c) => '\"' + c.replace(/\"/g, '\"\"') + '\"').join(', ');\n  const placeholders = columns.map((_, i) => '$' + (i + 1)).join(', ');\n  const key = table === '\"user\"' ? 'account' : table === 'workspace' ? 'slug' : 'id';\n  await target.query('INSERT INTO ' + table + ' (' + quoted + ') VALUES (' + placeholders + ') ON CONFLICT (\"' + key + '\") DO NOTHING', values);\n  return row;\n}\n(async () => {\n  const source = new pg.Client(sourceUrl);\n  const target = new pg.Client(targetUrl);\n  await source.connect();\n  await target.connect();\n  try {\n    await target.query('BEGIN');\n    const user = await copyTableRow(source, target, '\"user\"', 'account = $1', [account]);\n    const workspace = await copyTableRow(source, target, 'workspace', 'slug = $1', [workspaceSlug]);\n    const member = await source.query('SELECT * FROM member WHERE user_id = $1 AND workspace_id = $2 LIMIT 1', [user.id, workspace.id]);\n    if (member.rowCount === 0) throw new Error('missing seed row in member');\n    const row = member.rows[0];\n    const columns = Object.keys(row);\n    const quoted = columns.map((c) => '\"' + c.replace(/\"/g, '\"\"') + '\"').join(', ');\n    const placeholders = columns.map((_, i) => '$' + (i + 1)).join(', ');\n    await target.query('INSERT INTO member (' + quoted + ') VALUES (' + placeholders + ') ON CONFLICT (workspace_id, user_id) DO UPDATE SET role = EXCLUDED.role', columns.map((c) => row[c]));\n    await target.query('COMMIT');\n  } catch (error) {\n    await target.query('ROLLBACK');\n    throw error;\n  } finally {\n    await source.end();\n    await target.end();\n  }\n})().catch((error) => {\n  console.error(error.stack || error.message || String(error));\n  process.exit(1);\n});\nNODE`], { cwd: repoRoot, encoding: "utf8" });
  if (res.status !== 0) fail(`seed demo identity failed\n${res.stderr || res.stdout}`);
}

function ensureDaemonProfile(item, env) {
  const sourcePath = path.join(process.env.HOME || "/root", ".multica", "profiles", "goal-test", "config.json");
  const targetDir = path.join(process.env.HOME || "/root", ".multica", "profiles", item.daemonProfile);
  const targetPath = path.join(targetDir, "config.json");
  if (!existsSync(sourcePath)) fail(`source daemon profile missing: ${sourcePath}`);
  const cfg = existsSync(targetPath)
    ? JSON.parse(readFileSync(targetPath, "utf8"))
    : JSON.parse(readFileSync(sourcePath, "utf8"));
  const next = {
    ...cfg,
    server_url: `http://127.0.0.1:${item.backendPort}`,
    app_url: `http://${publicHost}:${item.frontendPort}`,
  };
  mkdirSync(targetDir, { recursive: true });
  writeFileSync(targetPath, `${JSON.stringify(next, null, 2)}\n`, { mode: 0o600 });
  return targetPath;
}

function refreshDaemonProfileToken(item) {
  const profilePath = path.join(process.env.HOME || "/root", ".multica", "profiles", item.daemonProfile, "config.json");
  const res = spawnSync("bash", ["-lc", `node - <<'NODE'\nconst fs = require('fs');\nconst url = 'http://127.0.0.1:${item.backendPort}';\nconst profilePath = ${JSON.stringify(profilePath)};\n(async () => {\n  const loginRes = await fetch(url + '/auth/login', {\n    method: 'POST',\n    headers: { 'content-type': 'application/json' },\n    body: JSON.stringify({ account: 'goal-test-daemon', password: 'e2e-password' }),\n  });\n  if (!loginRes.ok) throw new Error('login failed ' + loginRes.status + ': ' + await loginRes.text());\n  const login = await loginRes.json();\n  const wsRes = await fetch(url + '/api/workspaces', { headers: { authorization: 'Bearer ' + login.token } });\n  if (!wsRes.ok) throw new Error('workspaces failed ' + wsRes.status + ': ' + await wsRes.text());\n  const workspaces = await wsRes.json();\n  const workspace = (Array.isArray(workspaces) ? workspaces : workspaces.items || []).find((item) => item.slug === 'goal-test-daemon');\n  if (!workspace?.id) throw new Error('goal-test-daemon workspace missing');\n  const cfg = fs.existsSync(profilePath) ? JSON.parse(fs.readFileSync(profilePath, 'utf8')) : {};\n  cfg.server_url = url;\n  cfg.app_url = 'http://${publicHost}:${item.frontendPort}';\n  cfg.workspace_id = workspace.id;\n  cfg.token = login.token;\n  fs.writeFileSync(profilePath, JSON.stringify(cfg, null, 2) + '\\n', { mode: 0o600 });\n})().catch((error) => {\n  console.error(error.stack || error.message || String(error));\n  process.exit(1);\n});\nNODE`], { cwd: repoRoot, encoding: "utf8" });
  if (res.status !== 0) fail(`refresh daemon profile token failed\n${res.stderr || res.stdout}`);
}

function adminDatabaseURL(databaseURL) {
  try {
    const url = new URL(databaseURL);
    url.pathname = "/postgres";
    return url.toString();
  } catch {
    return databaseURL.replace(/\/[^/?]+(\?.*)?$/, "/postgres$1");
  }
}

function binaryVersion(command, args, env) {
  if (!existsSync(path.join(repoRoot, command.replace(/^\.\//, "")))) return "";
  const res = spawnSync(command, args, { cwd: repoRoot, env, encoding: "utf8" });
  return `${res.stdout || ""}${res.stderr || ""}`.trim().split(/\r?\n/).slice(0, 3).join(" | ");
}

function run(command, args, env) {
  const res = spawnSync(command, args, { cwd: repoRoot, env, stdio: "inherit", shell: false });
  if (res.status !== 0) fail(`${command} ${args.join(" ")} failed with ${res.status}`);
}

function startDetached(command, args, env, logFile) {
  const shellCommand = `setsid ${shellQuote(command)} ${args.map(shellQuote).join(" ")} >> ${shellQuote(logFile)} 2>&1 < /dev/null & echo $!`;
  const res = spawnSync("bash", ["-lc", shellCommand], { cwd: repoRoot, env, encoding: "utf8" });
  if (res.status !== 0) fail(`start failed: ${shellCommand}\n${res.stderr}`);
  return Number(res.stdout.trim().split(/\s+/).pop());
}

function waitForHTTP(url, timeoutMs) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    const res = spawnSync("curl", ["--noproxy", "*", "-fsS", url], { encoding: "utf8" });
    if (res.status === 0) return;
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 1000);
  }
  fail(`timeout waiting for ${url}`);
}

function stopPid(file) {
  if (!existsSync(file)) return;
  const pid = Number(readFileSync(file, "utf8").trim());
  if (!pid) return;
  spawnSync("bash", ["-lc", `kill ${pid} 2>/dev/null || true; sleep 1; kill -9 ${pid} 2>/dev/null || true`]);
}

function killPort(port) {
  const numericPort = Number(port);
  spawnSync("bash", ["-lc", `fuser -k ${numericPort}/tcp 2>/dev/null || true`]);
  spawnSync("bash", ["-lc", `lsof -ti:${numericPort} | xargs -r kill -9 2>/dev/null || true`]);
  const pid = listeningPID(port);
  if (pid) spawnSync("kill", ["-9", String(pid)]);
}

function waitForPortFree(port, timeoutMs) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    const res = spawnSync("bash", ["-lc", `lsof -iTCP:${Number(port)} -sTCP:LISTEN -n -P >/dev/null 2>&1`]);
    if (res.status !== 0) return;
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 500);
  }
  fail(`timeout waiting for port ${port} to become free`);
}

function killStaleProcesses(item) {
  const patterns = [
    `next dev .*--port ${item.frontendPort}`,
    `next start .*${item.frontendPort}`,
    `multica daemon start .*--profile ${item.daemonProfile}`,
  ];
  for (const pattern of patterns) {
    spawnSync("bash", ["-lc", `pgrep -f ${shellQuote(pattern)} | xargs -r kill -9 2>/dev/null || true`]);
  }
}

function listeningPID(port) {
  const res = spawnSync("bash", ["-lc", `ss -ltnp '( sport = :${Number(port)} )' 2>/dev/null | sed -n 's/.*pid=\\([0-9][0-9]*\\).*/\\1/p' | head -1`], { encoding: "utf8" });
  const pid = Number(res.stdout.trim());
  return Number.isFinite(pid) && pid > 0 ? pid : 0;
}

function inspectPID(pid) {
  if (!pid) return { pid: null, running: false, command: "" };
  const res = spawnSync("ps", ["-p", String(pid), "-o", "args="], { encoding: "utf8" });
  const cwd = spawnSync("readlink", [`/proc/${pid}/cwd`], { encoding: "utf8" });
  return { pid, running: res.status === 0 && Boolean(res.stdout.trim()), command: res.stdout.trim(), cwd: cwd.stdout.trim() };
}

function gitText(args) {
  const res = spawnSync("git", args, { cwd: repoRoot, encoding: "utf8" });
  return res.status === 0 ? res.stdout.trim() : "";
}

function shellQuote(value) {
  return `'${String(value).replace(/'/g, "'\\''")}'`;
}

function fail(message) {
  console.error(message);
  process.exit(1);
}
