import assert from "node:assert/strict";
import fs from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { resolveGoalTestPlaywrightAPIBase } from "./lib/goal-test-audit-env.mjs";

const scriptsDir = path.dirname(fileURLToPath(import.meta.url));

test("deployed goal-test API wins over a worktree-local API", () => {
  assert.equal(
    resolveGoalTestPlaywrightAPIBase(
      { NEXT_PUBLIC_API_URL: "http://localhost:18464" },
      { REMOTE_API_URL: "http://127.0.0.1:18762" },
    ),
    "http://127.0.0.1:18762",
  );
});

test("an explicit goal-test backend override wins over deployment metadata", () => {
  assert.equal(
    resolveGoalTestPlaywrightAPIBase(
      { GOAL_TEST_BACKEND_URL: "http://127.0.0.1:19000" },
      { REMOTE_API_URL: "http://127.0.0.1:18762" },
    ),
    "http://127.0.0.1:19000",
  );
});

test("a local API remains the final fallback without deployment metadata", () => {
  assert.equal(
    resolveGoalTestPlaywrightAPIBase(
      { NEXT_PUBLIC_API_URL: "http://localhost:18464/" },
      {},
    ),
    "http://localhost:18464",
  );
});

test("verification and fast actions preserve the deployed environment file", () => {
  const source = fs.readFileSync(path.join(scriptsDir, "goal-test-environments.mjs"), "utf8");
  const functionBody = (name, nextName) => source.slice(
    source.indexOf(`function ${name}`),
    source.indexOf(`function ${nextName}`),
  );

  for (const [name, nextName] of [
    ["verifyTarget", "deployedFrontendMode"],
    ["verifyLogsTarget", "verifyLogsForEnvironment"],
    ["describeEnvironment", "envPath"],
  ]) {
    assert.doesNotMatch(functionBody(name, nextName), /ensureEnvironment\(/);
  }
  for (const [name, nextName] of [
    ["restartDevServer", "prewarmDevWebOnly"],
    ["prewarmDevWebOnly", "restartDevDaemon"],
    ["restartDevDaemon", "runDevCheck"],
  ]) {
    assert.match(functionBody(name, nextName), /buildDeployedEnvironmentRuntime\(item\)/);
  }
});

test("goal-test environments keep local uploads site-relative", () => {
  const source = fs.readFileSync(path.join(scriptsDir, "goal-test-environments.mjs"), "utf8");
  const start = source.indexOf("function ensureEnvironment");
  const end = source.indexOf("function ensureStableSecretKey");
  assert.ok(start >= 0 && end > start);
  assert.match(source.slice(start, end), /`LOCAL_UPLOAD_BASE_URL=`/);
  assert.match(source.slice(start, end), /`CODEBUDDY_INTERNET_ENVIRONMENT=\$\{/);
});

test("goal-test service processes use an explicit inherited environment allowlist", () => {
  const source = fs.readFileSync(path.join(scriptsDir, "goal-test-environments.mjs"), "utf8");
  const start = source.indexOf("function runtimeFromEnvironmentFile");
  const end = source.indexOf("function startWebProcess");
  assert.ok(start >= 0 && end > start);
  const runtimeBody = source.slice(start, end);
  assert.doesNotMatch(runtimeBody, /\.\.\.process\.env/);
  assert.match(source, /const runtimeInheritedEnvKeys = \[/);
  for (const key of [
    "GONGFENG_ACCESS_TOKEN",
    "GONGFENG_PRIVATE_TOKEN",
    "TENCENT_DOCS_TOKEN",
    "TENCENT_MEETING_TOKEN",
    "OPENAI_API_KEY",
    "HAI_API_KEY",
    "SSH_AUTH_SOCK",
    "SSH_CLIENT",
    "SSH_CONNECTION",
    "CODEX_REMOTE_PAYLOAD",
    "CODEX_SESSION_ID",
    "CODEX_THREAD_ID",
  ]) {
    assert.doesNotMatch(runtimeBody, new RegExp(`['\"]${key}['\"]`));
  }
  assert.match(source, /serverEnv: serviceEnvironment\(env, "server"\)/);
  assert.match(source, /webEnv: serviceEnvironment\(env, "web"\)/);
  assert.match(source, /daemonEnv: serviceEnvironment\(env, "daemon"\)/);
  const webKeys = source.slice(source.indexOf("  web: ["), source.indexOf("  daemon: ["));
  const daemonKeys = source.slice(source.indexOf("  daemon: ["), source.indexOf("};\nconst serviceInheritedEnvKeys"));
  for (const key of ["DATABASE_URL", "POSTGRES_PASSWORD", "JWT_SECRET", "MULTICA_EXTERNAL_CREDENTIAL_KEY"]) {
    assert.doesNotMatch(webKeys, new RegExp(`\"${key}\"`));
    assert.doesNotMatch(daemonKeys, new RegExp(`\"${key}\"`));
  }
  const detachedBody = source.slice(source.indexOf("function startDetached"), source.indexOf("function waitForHTTP"));
  assert.match(detachedBody, /--noprofile/);
  assert.match(detachedBody, /--norc/);
});
