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
