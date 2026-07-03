import { readFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";

export const repoRoot = path.resolve(import.meta.dirname, "../..");

export function loadGoalTestIntEnv(file = path.join(repoRoot, ".run/env/goal-test-int.env")) {
  return {
    ...process.env,
    ...readEnvFile(file),
  };
}

export function resolveGoalTestAuditUrls(env) {
  return {
    frontendURL: trimSlash(process.env.GOAL_TEST_FRONTEND_URL || env.FRONTEND_ORIGIN || "http://9.134.129.162:13682"),
    browserURL: trimSlash(process.env.GOAL_TEST_BROWSER_URL || `http://127.0.0.1:${env.FRONTEND_PORT || "13682"}`),
    backendURL: trimSlash(process.env.GOAL_TEST_BACKEND_URL || env.REMOTE_API_URL || "http://127.0.0.1:18762"),
  };
}

function readEnvFile(file) {
  try {
    return Object.fromEntries(
      readFileSync(file, "utf8")
        .split(/\r?\n/)
        .map((line) => line.trim())
        .filter((line) => line && !line.startsWith("#") && line.includes("="))
        .map((line) => {
          const index = line.indexOf("=");
          return [line.slice(0, index), line.slice(index + 1)];
        }),
    );
  } catch {
    return {};
  }
}

function trimSlash(value) {
  return value.replace(/\/+$/, "");
}
