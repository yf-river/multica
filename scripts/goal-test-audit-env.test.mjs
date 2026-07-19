import assert from "node:assert/strict";
import test from "node:test";
import { resolveGoalTestPlaywrightAPIBase } from "./lib/goal-test-audit-env.mjs";

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
