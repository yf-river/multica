import { existsSync, readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const envName = process.env.GOAL_TEST_ENV || "int";
const runEnv = readEnvFile(path.join(repoRoot, ".run/env", `goal-test-${envName}.env`));

const cliArgs = process.argv.slice(2);
const specArgs = splitWords(process.env.SPEC || process.env.E2E_SPEC || "");
const extraArgs = splitWords(process.env.ARGS || process.env.E2E_ARGS || "");
const defaultArgs = [
  "e2e/squad-sop-ui.spec.ts",
  "e2e/coding-squad-ui.spec.ts",
  "--project=chromium",
];
const args = [...(cliArgs.length > 0 ? cliArgs : specArgs.length > 0 ? specArgs : defaultArgs), ...extraArgs];

const frontendURL = trimSlash(
  process.env.PLAYWRIGHT_BASE_URL ||
    runEnv.PLAYWRIGHT_BASE_URL ||
    runEnv.FRONTEND_ORIGIN ||
    "http://9.134.129.162:13682",
);
const apiBase = trimSlash(
  process.env.NEXT_PUBLIC_API_URL ||
    process.env.GOAL_TEST_BACKEND_URL ||
    runEnv.NEXT_PUBLIC_API_URL ||
    runEnv.REMOTE_API_URL ||
    `http://127.0.0.1:${runEnv.PORT || "18762"}`,
);
const databaseURL = process.env.GOAL_TEST_DATABASE_URL || runEnv.DATABASE_URL || process.env.DATABASE_URL || "";
if (!databaseURL) {
  console.error(`missing DATABASE_URL for goal-test ${envName}`);
  process.exit(2);
}

const childEnv = {
  ...process.env,
  PLAYWRIGHT_BASE_URL: frontendURL,
  FRONTEND_ORIGIN: frontendURL,
  NEXT_PUBLIC_API_URL: apiBase,
  REMOTE_API_URL: apiBase,
  DATABASE_URL: databaseURL,
  E2E_ACCOUNT: process.env.E2E_ACCOUNT || process.env.GOAL_TEST_ACCOUNT || "develop",
  E2E_PASSWORD: process.env.E2E_PASSWORD || process.env.GOAL_TEST_PASSWORD || "develop123",
  E2E_NAME: process.env.E2E_NAME || "AI Studio 开发账号",
  E2E_WORKSPACE: process.env.E2E_WORKSPACE || process.env.GOAL_TEST_WORKSPACE_SLUG || "ai-studio",
  E2E_WORKSPACE_NAME: process.env.E2E_WORKSPACE_NAME || "AI Studio 工作区",
  NO_PROXY: mergeNoProxy(process.env.NO_PROXY || process.env.no_proxy || "", [frontendURL, apiBase]),
};
childEnv.no_proxy = childEnv.NO_PROXY;

console.log(
  JSON.stringify(
    {
      schema: "multica.goal_test.playwright_command.v1",
      environment: envName,
      frontend_url: frontendURL,
      api_base: apiBase,
      database_url_redacted: redactDatabaseURL(databaseURL),
      account: childEnv.E2E_ACCOUNT,
      workspace_slug: childEnv.E2E_WORKSPACE,
      args,
    },
    null,
    2,
  ),
);

const result = spawnSync("pnpm", ["exec", "playwright", "test", ...args], {
  cwd: repoRoot,
  env: childEnv,
  stdio: "inherit",
});
process.exit(result.status ?? 1);

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

function splitWords(value) {
  return String(value || "")
    .split(/\s+/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function trimSlash(value) {
  return String(value || "").replace(/\/+$/, "");
}

function mergeNoProxy(current, urls) {
  const hosts = new Set(
    String(current || "")
      .split(",")
      .map((item) => item.trim())
      .filter(Boolean),
  );
  for (const url of urls) {
    try {
      const parsed = new URL(url);
      if (parsed.hostname) hosts.add(parsed.hostname);
    } catch {
      // ignore non-URL values
    }
  }
  hosts.add("127.0.0.1");
  hosts.add("localhost");
  return Array.from(hosts).join(",");
}

function redactDatabaseURL(value) {
  return String(value || "").replace(/:\/\/([^:]+):([^@]+)@/, "://$1:<redacted>@");
}
