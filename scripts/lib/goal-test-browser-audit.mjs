import { spawnSync } from "node:child_process";
import process from "node:process";
import { acceptanceDir } from "./acceptance-artifacts.mjs";
import { loadGoalTestIntEnv, repoRoot, resolveGoalTestAuditUrls } from "./goal-test-audit-env.mjs";

export function loadGoalTestBrowserAudit(artifactDir) {
  const env = loadGoalTestIntEnv();
  const urls = resolveGoalTestAuditUrls(env);
  const generatedAt = new Date().toISOString();
  return {
    env,
    ...urls,
    workspaceSlug: process.env.GOAL_TEST_WORKSPACE_SLUG || "ai-studio",
    account: process.env.GOAL_TEST_ACCOUNT || "develop",
    password: process.env.GOAL_TEST_PASSWORD || "develop123",
    artifactRoot: acceptanceDir(repoRoot, artifactDir),
    generatedAt,
    stamp: generatedAt.replace(/[:.]/g, "-"),
  };
}

export async function loginGoalTest({ backendURL, account, password }) {
  const response = await fetch(`${backendURL}/auth/login`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ account, password }),
  });
  if (!response.ok) {
    throw new Error(`login failed: ${response.status} ${await response.text()}`);
  }
  const data = await response.json();
  if (!data.token) throw new Error("login response did not include token");
  return data.token;
}

export function verifyGoalTestDeploymentLogs(env) {
  const target = env.GOAL_TEST_ENV || "int";
  const result = spawnSync("node", ["scripts/goal-test-environments.mjs", "verify-logs", target], {
    cwd: repoRoot,
    encoding: "utf8",
  });
  const raw = result.stdout || result.stderr || "";
  let evidence = null;
  try {
    evidence = raw ? JSON.parse(raw) : null;
  } catch {
    evidence = null;
  }
  return {
    ok: result.status === 0 && evidence?.ok === true,
    target,
    exit_code: result.status,
    evidence,
    error: result.status === 0 ? "" : (result.stderr || result.stdout || "").slice(0, 2000),
  };
}

export function createBrowserRequestTools(urls, requestPath) {
  return {
    isAuditedRequest: (url) => urls.some((baseURL) => url.startsWith(baseURL)),
    countByPath(requests) {
      const counts = new Map();
      for (const request of requests) {
        const key = requestPath(request.url);
        counts.set(key, (counts.get(key) || 0) + 1);
      }
      return Array.from(counts.entries())
        .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
        .map(([path, count]) => ({ path, count }));
    },
  };
}

export function configureGoalTestNoProxy(urls) {
  const noProxy = mergeNoProxy(process.env.NO_PROXY || process.env.no_proxy || "", urls);
  process.env.NO_PROXY = noProxy;
  process.env.no_proxy = noProxy;
}

export function mergeNoProxy(current, urls) {
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
      // Ignore non-URL values.
    }
  }
  hosts.add("127.0.0.1");
  hosts.add("localhost");
  return Array.from(hosts).join(",");
}
