import { readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

export const repoRoot = path.resolve(import.meta.dirname, "../..");

export function loadGoalTestIntEnv(file = path.join(repoRoot, ".run/env/goal-test-int.env")) {
  return {
    ...process.env,
    ...readGoalTestEnvFile(file),
  };
}

export function readGoalTestEnvFile(file) {
  try {
    const values = {};
    for (const raw of readFileSync(file, "utf8").split(/\r?\n/)) {
      const line = raw.trim();
      if (!line || line.startsWith("#")) continue;
      const match = line.match(/^([A-Za-z_][A-Za-z0-9_]*)=(.*)$/);
      if (match) values[match[1]] = match[2].replace(/^['"]|['"]$/g, "");
    }
    return values;
  } catch {
    return {};
  }
}

export function resolveGoalTestAuditUrls(env) {
  return {
    frontendURL: trimSlash(env.FRONTEND_ORIGIN || "http://9.134.129.162:13682"),
    browserURL: trimSlash(process.env.GOAL_TEST_BROWSER_URL || `http://127.0.0.1:${env.FRONTEND_PORT || "13682"}`),
    backendURL: trimSlash(process.env.GOAL_TEST_BACKEND_URL || env.REMOTE_API_URL || "http://127.0.0.1:18762"),
  };
}

export function resolveGoalTestPlaywrightAPIBase(processEnv, runEnv) {
  return trimSlash(
    processEnv.GOAL_TEST_BACKEND_URL ||
      runEnv.NEXT_PUBLIC_API_URL ||
      runEnv.REMOTE_API_URL ||
      (runEnv.PORT ? `http://127.0.0.1:${runEnv.PORT}` : "") ||
      processEnv.NEXT_PUBLIC_API_URL ||
      "http://127.0.0.1:18762",
  );
}

function trimSlash(value) {
  return value.replace(/\/+$/, "");
}
