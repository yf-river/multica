import pg from "pg";
import { existsSync, readFileSync, writeFileSync, mkdirSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const envName = process.env.GOAL_TEST_ENV || "int";
const runEnv = readEnvFile(path.join(repoRoot, ".run/env", `goal-test-${envName}.env`));
const startedAt = Date.now();

const frontendURL = trimSlash(process.env.PLAYWRIGHT_BASE_URL || runEnv.PLAYWRIGHT_BASE_URL || "");
const expectedFrontendURL = trimSlash(runEnv.FRONTEND_ORIGIN || "http://9.134.129.162:13682");
const apiBase = trimSlash(process.env.GOAL_TEST_BACKEND_URL || runEnv.NEXT_PUBLIC_API_URL || runEnv.REMOTE_API_URL || `http://127.0.0.1:${runEnv.PORT || "18762"}`);
const databaseURL = process.env.GOAL_TEST_DATABASE_URL || runEnv.DATABASE_URL || "";
const account = process.env.E2E_ACCOUNT || process.env.GOAL_TEST_ACCOUNT || "develop";
const password = process.env.E2E_PASSWORD || process.env.GOAL_TEST_PASSWORD || "develop123";
const workspaceSlug = process.env.E2E_WORKSPACE || process.env.GOAL_TEST_WORKSPACE_SLUG || "ai-studio";
const artifactRoot = acceptanceDir(repoRoot);

const checks = [];
const state = { token: "", user: null, workspaces: [] };

await check("PLAYWRIGHT_BASE_URL is explicit", async () => {
  if (!frontendURL) {
    throw new Error(`missing PLAYWRIGHT_BASE_URL; expected ${expectedFrontendURL}`);
  }
  return { value: frontendURL };
});

await check("API base is explicit or derived from goal-test env", async () => {
  if (!apiBase) throw new Error("missing API base");
  return { value: apiBase, source: process.env.GOAL_TEST_BACKEND_URL ? "GOAL_TEST_BACKEND_URL" : runEnv.NEXT_PUBLIC_API_URL ? "NEXT_PUBLIC_API_URL" : runEnv.REMOTE_API_URL ? "REMOTE_API_URL" : "PORT" };
});

await check("DATABASE_URL is available", async () => {
  if (!databaseURL) throw new Error("missing DATABASE_URL");
  return { value: redactDatabaseURL(databaseURL) };
});

await check("frontend login page responds", async () => {
  const response = await fetchWithRetry(`${frontendURL}/login`, { timeoutMs: 5_000, attempts: 3, delayMs: 1_000 });
  if (!response.ok) throw new Error(`GET /login returned ${response.status}`);
  return { status: response.status };
});

await check("backend health responds", async () => {
  const response = await fetchWithTimeout(`${apiBase}/health`, { timeoutMs: 2_000 });
  if (!response.ok) throw new Error(`GET /health returned ${response.status}`);
  return { status: response.status };
});

await check("login API returns token", async () => {
  const response = await fetchWithTimeout(`${apiBase}/auth/login`, {
    timeoutMs: 3_000,
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ account, password }),
  });
  if (!response.ok) throw new Error(`POST /auth/login returned ${response.status}: ${await response.text()}`);
  const data = await response.json();
  if (!data.token) throw new Error("login response did not include token");
  state.token = data.token;
  state.user = data.user ?? null;
  return { status: response.status, user_account: data.user?.account || account };
});

await check("workspace is readable", async () => {
  const response = await fetchWithTimeout(`${apiBase}/api/workspaces`, {
    timeoutMs: 3_000,
    headers: { authorization: `Bearer ${state.token}` },
  });
  if (!response.ok) throw new Error(`GET /api/workspaces returned ${response.status}: ${await response.text()}`);
  const data = await response.json();
  const items = Array.isArray(data) ? data : data.items ?? [];
  state.workspaces = items;
  const workspace = items.find((item) => item.slug === workspaceSlug);
  if (!workspace?.id) throw new Error(`workspace ${workspaceSlug} not found`);
  return { workspace_id: workspace.id, slug: workspace.slug };
});

await check("database user/workspace membership is ready", async () => {
  const client = new pg.Client({ connectionString: databaseURL, connectionTimeoutMillis: 2_000 });
  await client.connect();
  try {
    const result = await client.query(
      `
        SELECT u.account, w.slug AS workspace_slug, m.role
        FROM "user" u
        JOIN member m ON m.user_id = u.id
        JOIN workspace w ON w.id = m.workspace_id
        WHERE u.account = $1 AND w.slug = $2
        LIMIT 1
      `,
      [account, workspaceSlug],
    );
    if (result.rowCount !== 1) throw new Error(`missing user/workspace membership for ${account}/${workspaceSlug}`);
    const row = result.rows[0];
    return { account: row.account, workspace_slug: row.workspace_slug, role: row.role };
  } finally {
    await client.end();
  }
});

const payload = {
  schema: "multica.goal_test.e2e_preflight.v1",
  generated_at: new Date().toISOString(),
  environment: envName,
  frontend_url: frontendURL,
  expected_frontend_url: expectedFrontendURL,
  api_base: apiBase,
  database_url_redacted: redactDatabaseURL(databaseURL),
  account,
  workspace_slug: workspaceSlug,
  elapsed_ms: Date.now() - startedAt,
  checks,
  ok: checks.every((item) => item.ok),
};

mkdirSync(artifactRoot, { recursive: true });
writeFileSync(path.join(artifactRoot, "goal-test-e2e-preflight-latest.json"), `${JSON.stringify(payload, null, 2)}\n`);
console.log(JSON.stringify(payload, null, 2));
if (!payload.ok) process.exit(2);

async function check(name, fn) {
  const started = Date.now();
  try {
    const evidence = await fn();
    checks.push({ name, ok: true, elapsed_ms: Date.now() - started, evidence });
  } catch (error) {
    checks.push({ name, ok: false, elapsed_ms: Date.now() - started, error: error instanceof Error ? error.message : String(error) });
  }
}

async function fetchWithTimeout(url, init = {}) {
  const controller = new AbortController();
  const { timeoutMs = 3_000, ...requestInit } = init;
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await fetch(url, { ...requestInit, signal: controller.signal });
  } finally {
    clearTimeout(timeout);
  }
}

async function fetchWithRetry(url, init = {}) {
  const { attempts = 3, delayMs = 500, ...requestInit } = init;
  let lastError;
  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    try {
      return await fetchWithTimeout(url, requestInit);
    } catch (error) {
      lastError = error;
      if (attempt < attempts) await new Promise((resolve) => setTimeout(resolve, delayMs));
    }
  }
  throw lastError;
}

function readEnvFile(file) {
  if (!existsSync(file)) return {};
  const values = {};
  for (const raw of readFileSync(file, "utf8").split(/\r?\n/)) {
    const line = raw.trim();
    if (!line || line.startsWith("#")) continue;
    const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
    if (match) values[match[1]] = match[2].replace(/^['"]|['"]$/g, "");
  }
  return values;
}

function trimSlash(value) {
  return String(value || "").replace(/\/+$/, "");
}

function redactDatabaseURL(value) {
  return String(value || "").replace(/:\/\/([^:]+):([^@]+)@/, "://$1:<redacted>@");
}
