import { spawnSync } from "node:child_process";
import { createHmac, randomBytes } from "node:crypto";
import { chromium } from "@playwright/test";
import process from "node:process";
import { acceptanceDir } from "./acceptance-artifacts.mjs";
import { loadGoalTestIntEnv, repoRoot, resolveGoalTestAuditUrls } from "./goal-test-audit-env.mjs";

export function loadGoalTestBrowserAudit() {
  const env = loadGoalTestIntEnv();
  const urls = resolveGoalTestAuditUrls(env);
  const generatedAt = new Date().toISOString();
  return {
    env,
    ...urls,
    workspaceSlug: process.env.GOAL_TEST_WORKSPACE_SLUG || "ai-studio",
    account: process.env.GOAL_TEST_ACCOUNT || "develop",
    password: process.env.GOAL_TEST_PASSWORD || "develop123",
    artifactRoot: acceptanceDir(repoRoot),
    generatedAt,
    stamp: generatedAt.replace(/[:.]/g, "-"),
  };
}

export function loadLocalBrowserAudit() {
  const generatedAt = new Date().toISOString();
  const frontendPort = process.env.FRONTEND_PORT || "3000";
  const backendPort = process.env.PORT || process.env.BACKEND_PORT || "8080";
  return {
    env: process.env,
    frontendURL: `http://localhost:${frontendPort}`,
    browserURL: `http://localhost:${frontendPort}`,
    backendURL: `http://127.0.0.1:${backendPort}`,
    workspaceSlug: process.env.MULTICA_DEV_WORKSPACE_SLUG || "dev",
    account: process.env.MULTICA_DEV_ACCOUNT || "dev",
    password: process.env.MULTICA_DEV_PASSWORD || "Devpass1!",
    artifactRoot: acceptanceDir(repoRoot),
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

export async function launchGoalTestBrowser(browserURL, token) {
  const browser = await chromium.launch({
    headless: true,
    args: ["--no-proxy-server", "--proxy-server=direct://", "--proxy-bypass-list=*"],
  });
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    ignoreHTTPSErrors: true,
  });
  const csrfNonce = randomBytes(16);
  const csrfToken = `${csrfNonce.toString("hex")}.${createHmac("sha256", token).update(csrfNonce).digest("hex")}`;
  await context.addCookies([
    { name: "multica_auth", value: token, url: browserURL, httpOnly: true, sameSite: "Strict" },
    { name: "multica_csrf", value: csrfToken, url: browserURL, sameSite: "Strict" },
    { name: "multica_logged_in", value: "1", url: browserURL, sameSite: "Lax" },
  ]);
  await context.addInitScript(() => {
    localStorage.setItem("multica:chat:isOpen", "false");
  });
  return { browser, context };
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
  function countByPath(requests) {
    const counts = new Map();
    for (const request of requests) {
      const key = requestPath(request.url);
      counts.set(key, (counts.get(key) || 0) + 1);
    }
    return Array.from(counts.entries())
      .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
      .map(([path, count]) => ({ path, count }));
  }
  return {
    isAuditedRequest: (url) => urls.some((baseURL) => url.startsWith(baseURL)),
    countByPath,
    buildApiRequestBudget(requests) {
      const projectRequests = requests.filter((item) => /^\/api\/projects\/[^/]+\/resources$/.test(requestPath(item.url)));
      const uniqueProjectPaths = countByPath(projectRequests).length;
      const duplicateProjectRequests = projectRequests.length - uniqueProjectPaths;
      return {
        count: requests.length - projectRequests.length + Math.min(uniqueProjectPaths, 3) + duplicateProjectRequests,
        actual_count: requests.length,
        project_resource_actual_count: projectRequests.length,
        project_resource_unique_paths: uniqueProjectPaths,
        project_resource_budget: Math.min(uniqueProjectPaths, 3) + duplicateProjectRequests,
      };
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
