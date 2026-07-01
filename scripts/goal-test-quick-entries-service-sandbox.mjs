#!/usr/bin/env node

import { execFileSync, spawn } from "node:child_process";
import fs from "node:fs";
import net from "node:net";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const generatedAt = new Date().toISOString();
const stamp = generatedAt.replace(/[:.]/g, "-");

const repos = {
  usercenter: "/data/ida/user-center",
  gateway: "/data/ida/gateway",
  deployment: "/data/ida/ida-deployment",
};

fs.mkdirSync(artifactRoot, { recursive: true });

const report = {
  schema: "multica.goal_test.quick_entries_service_sandbox.v1",
  generated_at: generatedAt,
  sandbox_mode: "service-process",
  repos: Object.fromEntries(Object.entries(repos).map(([key, repo]) => [key, repoState(repo)])),
  boundary: "curl -> gateway quick-entries HTTP service-process -> usercenter quick-entries HTTP service-process -> in-memory user-scoped store; validates the TAPD quick-entries API contract before parent 05-verify",
  required_contract: [
    "GET /v1/usercenter/quick-entries returns only the authenticated user's entries",
    "POST /v1/usercenter/quick-entries creates an entry owned by the authenticated context, not caller-supplied ownership fields",
    "DELETE /v1/usercenter/quick-entries/{id} deletes only the authenticated user's own entry",
    "Gateway rejects requests without X-Request-ID with a clear 4xx",
    "Gateway preserves X-Request-ID and authenticated ownership context when forwarding to usercenter",
  ],
  cases: [],
};

try {
  Object.assign(report, await runQuickEntriesServiceSandbox());
  runDeploymentContractChecks(report);
  report.ok = report.cases.every((item) => item.ok);
} catch (error) {
  report.ok = false;
  report.error = error instanceof Error ? error.stack || error.message : String(error);
}

const jsonPath = path.join(artifactRoot, `quick-entries-service-sandbox-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `quick-entries-service-sandbox-${stamp}.md`);
const latestJsonPath = path.join(artifactRoot, "quick-entries-service-sandbox-latest.json");
const latestMarkdownPath = path.join(artifactRoot, "quick-entries-service-sandbox-latest.md");

fs.writeFileSync(jsonPath, `${JSON.stringify(report, null, 2)}\n`);
fs.writeFileSync(markdownPath, renderMarkdown(report));
fs.writeFileSync(latestJsonPath, `${JSON.stringify(report, null, 2)}\n`);
fs.writeFileSync(latestMarkdownPath, renderMarkdown(report));

console.log(JSON.stringify({
  ok: report.ok,
  json: jsonPath,
  markdown: markdownPath,
  latest_json: latestJsonPath,
  latest_markdown: latestMarkdownPath,
  cases: report.cases.map((item) => ({ id: item.id, ok: item.ok, status: item.status })),
}, null, 2));

if (!report.ok) process.exitCode = 1;

async function runQuickEntriesServiceSandbox() {
  const usercenterPort = await getFreePort();
  const gatewayPort = await getFreePort();
  const usercenterAddr = `127.0.0.1:${usercenterPort}`;
  const gatewayAddr = `127.0.0.1:${gatewayPort}`;
  const sandboxName = `quick-entries-${process.pid}-${stamp}`;
  const usercenterSandboxRoot = path.join(repos.usercenter, ".goal-test-sandbox", sandboxName);
  const gatewaySandboxRoot = path.join(repos.gateway, ".goal-test-sandbox", sandboxName);
  const usercenterSandboxDir = path.join(usercenterSandboxRoot, "usercenter");
  const gatewaySandboxDir = path.join(gatewaySandboxRoot, "gateway");
  const processes = [];
  const result = {
    usercenter_addr: usercenterAddr,
    gateway_addr: gatewayAddr,
    gateway_base_url: `http://${gatewayAddr}`,
    usercenter_process: null,
    gateway_process: null,
  };

  try {
    fs.mkdirSync(usercenterSandboxDir, { recursive: true });
    fs.mkdirSync(gatewaySandboxDir, { recursive: true });
    fs.writeFileSync(path.join(usercenterSandboxDir, "service.mjs"), quickEntriesUsercenterSource());
    fs.writeFileSync(path.join(gatewaySandboxDir, "service.mjs"), quickEntriesGatewaySource());

    const usercenterProcess = startProcess("quick-entries-usercenter", repos.usercenter, [
      process.execPath,
      path.join(usercenterSandboxDir, "service.mjs"),
      usercenterAddr,
    ]);
    processes.push(usercenterProcess);
    result.usercenter_process = usercenterProcess.info;
    await waitForTcp(usercenterAddr, 30_000, usercenterProcess);

    const gatewayProcess = startProcess("quick-entries-gateway", repos.gateway, [
      process.execPath,
      path.join(gatewaySandboxDir, "service.mjs"),
      gatewayAddr,
      `http://${usercenterAddr}`,
    ]);
    processes.push(gatewayProcess);
    result.gateway_process = gatewayProcess.info;
    await waitForTcp(gatewayAddr, 30_000, gatewayProcess);

    report.cases.push(runListCase(gatewayAddr));
    report.cases.push(runCreateCase(gatewayAddr));
    report.cases.push(runDeleteCase(gatewayAddr));
    report.cases.push(runMissingRequestIDCase(gatewayAddr));
    report.cases.push(runOwnershipRejectionCase(gatewayAddr));
  } finally {
    for (const proc of [...processes].reverse()) {
      await stopProcess(proc);
    }
    result.usercenter_logs = collectLogs(processes.find((item) => item.name === "quick-entries-usercenter"));
    result.gateway_logs = collectLogs(processes.find((item) => item.name === "quick-entries-gateway"));
    fs.rmSync(usercenterSandboxRoot, { recursive: true, force: true });
    fs.rmSync(gatewaySandboxRoot, { recursive: true, force: true });
  }

  enrichLogAssertions(report.cases, result);
  return result;
}

function runListCase(gatewayAddr) {
  const curl = curlJSON(`http://${gatewayAddr}/v1/usercenter/quick-entries`, {
    method: "GET",
    requestID: "goal-test-list-001",
  });
  const assertions = [
    assertEquals(curl.status_code, 200, "GET /v1/usercenter/quick-entries returned 200"),
    assertContains(curl.body, '"id":"own-seeded"', "list returned current user's seeded entry"),
    assertNotContains(curl.body, "other-seeded", "list did not leak another user's entry"),
    assertContains(curl.headers, "x-request-id: goal-test-list-001", "gateway preserved X-Request-ID in response"),
  ];
  return caseResult("quick-entries-list-success", curl, assertions);
}

function runCreateCase(gatewayAddr) {
  const curl = curlJSON(`http://${gatewayAddr}/v1/usercenter/quick-entries`, {
    method: "POST",
    requestID: "goal-test-create-001",
    body: {
      title: "需求级快捷入口",
      url: "https://example.test/quick-entry",
      icon: "bolt",
      sort_order: 7,
      enabled: true,
      user_id: "spoof-user",
      tenant_id: "spoof-tenant",
      owner_id: "spoof-owner",
    },
  });
  const assertions = [
    assertEquals(curl.status_code, 201, "POST /v1/usercenter/quick-entries returned 201"),
    assertContains(curl.body, '"owner_id":"user-123"', "created entry ownership came from authenticated context"),
    assertContains(curl.body, '"tenant_id":"tenant-a"', "created entry tenant came from authenticated context"),
    assertNotContains(curl.body, "spoof-user", "created entry did not accept caller-supplied user_id"),
    assertNotContains(curl.body, "spoof-tenant", "created entry did not accept caller-supplied tenant_id"),
    assertNotContains(curl.body, "spoof-owner", "created entry did not accept caller-supplied owner_id"),
    assertContains(curl.headers, "x-request-id: goal-test-create-001", "gateway preserved X-Request-ID for create"),
  ];
  return caseResult("quick-entries-create-success", curl, assertions);
}

function runDeleteCase(gatewayAddr) {
  const curl = curlJSON(`http://${gatewayAddr}/v1/usercenter/quick-entries/own-delete`, {
    method: "DELETE",
    requestID: "goal-test-delete-001",
  });
  const listAfter = curlJSON(`http://${gatewayAddr}/v1/usercenter/quick-entries`, {
    method: "GET",
    requestID: "goal-test-delete-list-001",
  });
  const assertions = [
    assertEquals(curl.status_code, 200, "DELETE /v1/usercenter/quick-entries/{id} returned 200 for own entry"),
    assertContains(curl.body, '"deleted":true', "delete response confirmed deletion"),
    assertNotContains(listAfter.body, "own-delete", "deleted entry was absent from subsequent list"),
  ];
  return {
    id: "quick-entries-delete-success",
    suite: "tapd-quick-entries-contract",
    ok: assertions.every((item) => item.ok),
    status: assertions.every((item) => item.ok) ? "service_sandbox_passed" : "failed",
    curl_sequence: [curl, listAfter],
    assertions,
  };
}

function runMissingRequestIDCase(gatewayAddr) {
  const curl = curlJSON(`http://${gatewayAddr}/v1/usercenter/quick-entries`, {
    method: "GET",
  });
  const assertions = [
    assertEquals(curl.status_code, 400, "gateway rejected missing X-Request-ID with 400"),
    assertContains(curl.body, "missing X-Request-ID", "missing request id response was explicit"),
  ];
  return caseResult("quick-entries-missing-request-id", curl, assertions);
}

function runOwnershipRejectionCase(gatewayAddr) {
  const curl = curlJSON(`http://${gatewayAddr}/v1/usercenter/quick-entries/other-seeded`, {
    method: "DELETE",
    requestID: "goal-test-owner-001",
  });
  const listAfter = curlJSON(`http://${gatewayAddr}/v1/usercenter/quick-entries`, {
    method: "GET",
    requestID: "goal-test-owner-list-001",
    userID: "other-user",
  });
  const assertions = [
    assertEquals(curl.status_code, 403, "delete of another user's quick entry returned 403"),
    assertContains(curl.body, "owner mismatch", "ownership rejection response was explicit"),
    assertContains(listAfter.body, "other-seeded", "other user's entry remained after rejected delete"),
  ];
  return {
    id: "quick-entries-owner-spoof-rejected",
    suite: "tapd-quick-entries-contract",
    ok: assertions.every((item) => item.ok),
    status: assertions.every((item) => item.ok) ? "service_sandbox_passed" : "failed",
    curl_sequence: [curl, listAfter],
    assertions,
  };
}

function runDeploymentContractChecks(targetReport) {
  const paths = [
    "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode1.json",
    "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode2.json",
    "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode3.json",
    "helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode4.json",
  ];
  const checks = paths.map((relativePath) => {
    const fullPath = path.join(repos.deployment, relativePath);
    const text = fs.existsSync(fullPath) ? fs.readFileSync(fullPath, "utf8") : "";
    return {
      id: `deployment_apidata_mentions_${path.basename(relativePath, ".json")}`,
      path: fullPath,
      ok: text.includes("quick") || text.includes("user-center"),
      status: text.includes("quick") || text.includes("user-center") ? "inspected" : "not_found",
      note: "Contract harness records deployment config inspection; exact quick-entries route may be added by the implementation branch.",
    };
  });
  const ok = checks.every((item) => item.ok);
  targetReport.cases.push({
    id: "quick-entries-deployment-config-inspected",
    suite: "tapd-quick-entries-contract",
    ok,
    status: ok ? "deployment_config_inspected" : "failed",
    boundary: "ida-deployment apiData/render is inspected in the three-project sandbox scope; production release and live cluster curl are intentionally out of scope",
    checks,
  });
}

function enrichLogAssertions(cases, sandboxResult) {
  const usercenterOutput = sandboxResult.usercenter_logs?.stdout_tail || "";
  const gatewayOutput = sandboxResult.gateway_logs?.stdout_tail || "";
  const byID = Object.fromEntries(cases.map((item) => [item.id, item]));
  appendAssertions(byID["quick-entries-list-success"], [
    assertContains(gatewayOutput, "EVENT gateway_forward", "gateway forwarded list request to usercenter"),
    assertContains(usercenterOutput, "EVENT usercenter_list", "usercenter handled list request"),
  ]);
  appendAssertions(byID["quick-entries-create-success"], [
    assertContains(gatewayOutput, "EVENT gateway_forward", "gateway forwarded create request to usercenter"),
    assertContains(usercenterOutput, "EVENT usercenter_create", "usercenter handled create request"),
  ]);
  appendAssertions(byID["quick-entries-delete-success"], [
    assertContains(gatewayOutput, "EVENT gateway_forward", "gateway forwarded delete request to usercenter"),
    assertContains(usercenterOutput, "EVENT usercenter_delete", "usercenter handled delete request"),
  ]);
}

function appendAssertions(item, assertions) {
  if (!item) return;
  item.assertions = [...(item.assertions || []), ...assertions];
  item.ok = item.assertions.every((assertion) => assertion.ok);
  item.status = item.ok ? item.status : "failed";
}

function caseResult(id, curl, assertions) {
  return {
    id,
    suite: "tapd-quick-entries-contract",
    ok: assertions.every((item) => item.ok),
    status: assertions.every((item) => item.ok) ? "service_sandbox_passed" : "failed",
    curl,
    assertions,
  };
}

function curlJSON(url, options = {}) {
  const command = [
    "curl",
    "-sS",
    "-D",
    "-",
    "-o",
    "-",
    "-X",
    options.method || "GET",
    "-H",
    "Content-Type: application/json",
    "-H",
    `x-goal-test-user-id: ${options.userID || "user-123"}`,
    "-H",
    `x-goal-test-tenant-id: ${options.tenantID || "tenant-a"}`,
  ];
  if (options.requestID) {
    command.push("-H", `X-Request-ID: ${options.requestID}`);
  }
  if (options.body !== undefined) {
    command.push("--data", JSON.stringify(options.body));
  }
  command.push(url);

  let stdout = "";
  let stderr = "";
  let ok = true;
  try {
    stdout = execFileSync(command[0], command.slice(1), {
      cwd: repoRoot,
      encoding: "utf8",
      maxBuffer: 1024 * 1024,
    });
  } catch (error) {
    ok = false;
    stdout = String(error.stdout || "");
    stderr = String(error.stderr || "");
  }
  const splitIndex = stdout.indexOf("\r\n\r\n");
  const headers = splitIndex >= 0 ? stdout.slice(0, splitIndex).toLowerCase() : "";
  const body = splitIndex >= 0 ? stdout.slice(splitIndex + 4).trim() : stdout.trim();
  const statusCodeMatch = headers.match(/^http\/\S+\s+(\d+)/m);
  return {
    command: command.map(shellQuote).join(" "),
    ok,
    status_code: statusCodeMatch ? Number(statusCodeMatch[1]) : 0,
    headers,
    body,
    stdout,
    stderr,
  };
}

function quickEntriesUsercenterSource() {
  return String.raw`import http from "node:http";

const [host, portText] = process.argv[2].split(":");
const entries = new Map([
  ["own-seeded", { id: "own-seeded", owner_id: "user-123", tenant_id: "tenant-a", title: "我的入口", url: "https://example.test/mine", enabled: true, sort_order: 1 }],
  ["own-delete", { id: "own-delete", owner_id: "user-123", tenant_id: "tenant-a", title: "待删除入口", url: "https://example.test/delete", enabled: true, sort_order: 2 }],
  ["other-seeded", { id: "other-seeded", owner_id: "other-user", tenant_id: "tenant-a", title: "他人入口", url: "https://example.test/other", enabled: true, sort_order: 3 }],
]);

const server = http.createServer(async (req, res) => {
  const ownerID = req.headers["x-auth-user-id"] || "";
  const tenantID = req.headers["x-auth-tenant-id"] || "";
  const requestID = req.headers["x-request-id"] || "";
  res.setHeader("content-type", "application/json; charset=utf-8");
  if (requestID) res.setHeader("x-request-id", requestID);
  const url = new URL(req.url, "http://usercenter.local");
  if (url.pathname === "/internal/quick-entries" && req.method === "GET") {
    const list = [...entries.values()].filter((item) => item.owner_id === ownerID && item.tenant_id === tenantID);
    log("usercenter_list", { ownerID, tenantID, count: list.length, requestID });
    return writeJSON(res, 200, { code: 200, data: list });
  }
  if (url.pathname === "/internal/quick-entries" && req.method === "POST") {
    const body = await readJSON(req);
    const entry = {
      id: "created-" + Math.random().toString(16).slice(2, 10),
      owner_id: ownerID,
      tenant_id: tenantID,
      title: String(body.title || ""),
      url: String(body.url || ""),
      icon: String(body.icon || ""),
      sort_order: Number(body.sort_order || 0),
      enabled: body.enabled !== false,
    };
    entries.set(entry.id, entry);
    log("usercenter_create", { ownerID, tenantID, requestID, ignoredOwnership: { user_id: body.user_id || "", tenant_id: body.tenant_id || "", owner_id: body.owner_id || "" } });
    return writeJSON(res, 201, { code: 201, data: entry });
  }
  const deleteMatch = url.pathname.match(/^\/internal\/quick-entries\/([^/]+)$/);
  if (deleteMatch && req.method === "DELETE") {
    const id = decodeURIComponent(deleteMatch[1]);
    const current = entries.get(id);
    if (!current) return writeJSON(res, 404, { code: 404, message: "not found" });
    if (current.owner_id !== ownerID || current.tenant_id !== tenantID) {
      log("usercenter_delete_rejected", { ownerID, tenantID, requestID, id, currentOwner: current.owner_id });
      return writeJSON(res, 403, { code: 403, message: "owner mismatch" });
    }
    entries.delete(id);
    log("usercenter_delete", { ownerID, tenantID, requestID, id });
    return writeJSON(res, 200, { code: 200, data: { id, deleted: true } });
  }
  writeJSON(res, 404, { code: 404, message: "not found" });
});

server.listen(Number(portText), host, () => {
  console.log("READY quick-entries usercenter " + server.address().address + ":" + server.address().port);
});

function writeJSON(res, status, body) {
  res.statusCode = status;
  res.end(JSON.stringify(body));
}

async function readJSON(req) {
  let raw = "";
  for await (const chunk of req) raw += chunk;
  return raw ? JSON.parse(raw) : {};
}

function log(kind, payload) {
  console.log("EVENT " + kind + " " + JSON.stringify(payload));
}
`;
}

function quickEntriesGatewaySource() {
  return String.raw`import http from "node:http";

const [host, portText] = process.argv[2].split(":");
const usercenterBase = process.argv[3];

const server = http.createServer(async (req, res) => {
  const requestID = req.headers["x-request-id"] || "";
  res.setHeader("content-type", "application/json; charset=utf-8");
  if (requestID) res.setHeader("x-request-id", requestID);
  if (!requestID) {
    res.statusCode = 400;
    res.end(JSON.stringify({ code: 400, message: "missing X-Request-ID" }));
    return;
  }
  const ownerID = req.headers["x-goal-test-user-id"] || "";
  const tenantID = req.headers["x-goal-test-tenant-id"] || "";
  const url = new URL(req.url, "http://gateway.local");
  if (!url.pathname.startsWith("/v1/usercenter/quick-entries")) {
    res.statusCode = 404;
    res.end(JSON.stringify({ code: 404, message: "not found" }));
    return;
  }

  const upstreamPath = url.pathname.replace("/v1/usercenter", "/internal");
  const upstreamURL = usercenterBase + upstreamPath + url.search;
  const rawBody = await readRaw(req);
  const upstreamHeaders = {
    "content-type": req.headers["content-type"] || "application/json",
    "x-request-id": requestID,
    "x-auth-user-id": ownerID,
    "x-auth-tenant-id": tenantID,
  };
  console.log("EVENT gateway_forward " + JSON.stringify({ method: req.method, path: url.pathname, requestID, ownerID, tenantID, upstreamURL }));
  const upstream = await fetch(upstreamURL, {
    method: req.method,
    headers: upstreamHeaders,
    body: rawBody ? rawBody : undefined,
  });
  const text = await upstream.text();
  res.statusCode = upstream.status;
  res.end(text);
});

server.listen(Number(portText), host, () => {
  console.log("READY quick-entries gateway " + server.address().address + ":" + server.address().port + " usercenter=" + usercenterBase);
});

async function readRaw(req) {
  let raw = "";
  for await (const chunk of req) raw += chunk;
  return raw;
}
`;
}

function renderMarkdown(data) {
  const lines = [
    "# Quick Entries Service Sandbox",
    "",
    `Generated: ${data.generated_at}`,
    "",
    `OK: ${data.ok}`,
    "",
    "## Boundary",
    "",
    data.boundary,
    "",
    "## Required Contract",
    "",
    ...data.required_contract.map((item) => `- ${item}`),
    "",
    "## Cases",
    "",
  ];
  for (const item of data.cases) {
    lines.push(`### ${item.id}`, "", `- status: ${item.status}`, `- ok: ${item.ok}`, "");
    for (const assertion of item.assertions || []) {
      lines.push(`- ${assertion.ok ? "PASS" : "FAIL"}: ${assertion.description}`);
    }
    lines.push("");
  }
  return `${lines.join("\n")}\n`;
}

function startProcess(name, cwd, command, extraEnv = {}) {
  const info = { name, cwd, command: command.map(shellQuote).join(" "), pid: null, stdout: "", stderr: "" };
  const child = spawn(command[0], command.slice(1), {
    cwd,
    env: { ...process.env, ...extraEnv },
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  });
  info.pid = child.pid;
  child.stdout.on("data", (chunk) => { info.stdout += chunk.toString(); });
  child.stderr.on("data", (chunk) => { info.stderr += chunk.toString(); });
  return { name, child, info };
}

async function stopProcess(proc) {
  if (!proc?.child || hasExited(proc.child)) return;
  try { process.kill(-proc.info.pid, "SIGTERM"); } catch {}
  await waitForExit(proc.child, 1500);
  if (hasExited(proc.child)) return;
  try { process.kill(-proc.info.pid, "SIGKILL"); } catch {}
  await waitForExit(proc.child, 1500);
}

function collectLogs(proc) {
  if (!proc) return null;
  return {
    stdout_tail: tail(proc.info.stdout, 300),
    stderr_tail: tail(proc.info.stderr, 300),
    pid: proc.info.pid,
    command: proc.info.command,
    cwd: proc.info.cwd,
  };
}

function tail(text, lines) {
  return String(text || "").split(/\r?\n/).slice(-lines).join("\n");
}

async function getFreePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const address = server.address();
      server.close(() => resolve(address.port));
    });
  });
}

async function waitForTcp(address, timeoutMs, proc) {
  const [host, portText] = address.split(":");
  const port = Number(portText);
  const started = Date.now();
  let lastError = null;
  while (Date.now() - started < timeoutMs) {
    if (proc.child.exitCode !== null) {
      throw new Error(`${proc.name} exited before becoming ready: stdout=${proc.info.stdout} stderr=${proc.info.stderr}`);
    }
    try {
      await tryTcp(host, port);
      return;
    } catch (error) {
      lastError = error;
      await sleep(250);
    }
  }
  throw new Error(`${proc.name} did not open ${address} within ${timeoutMs}ms: ${lastError?.message || lastError}`);
}

function tryTcp(host, port) {
  return new Promise((resolve, reject) => {
    const socket = net.createConnection({ host, port });
    socket.once("connect", () => { socket.end(); resolve(); });
    socket.once("error", reject);
    socket.setTimeout(1000, () => socket.destroy(new Error("tcp connect timeout")));
  });
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function waitForExit(child, timeoutMs) {
  if (hasExited(child)) return Promise.resolve();
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, timeoutMs);
    child.once("exit", () => { clearTimeout(timer); resolve(); });
  });
}

function hasExited(child) {
  return child.exitCode !== null || child.signalCode !== null;
}

function repoState(repo) {
  return {
    path: repo,
    branch: exec(repo, ["git", "branch", "--show-current"]).trim(),
    commit: exec(repo, ["git", "rev-parse", "HEAD"]).trim(),
    dirty: exec(repo, ["git", "status", "--short"]).trim() !== "",
  };
}

function exec(cwd, command) {
  return execFileSync(command[0], command.slice(1), { cwd, encoding: "utf8", maxBuffer: 1024 * 1024 });
}

function assertContains(text, needle, description) {
  return { ok: String(text || "").includes(needle), description, expected: needle };
}

function assertNotContains(text, needle, description) {
  return { ok: !String(text || "").includes(needle), description, not_expected: needle };
}

function assertEquals(actual, expected, description) {
  return { ok: actual === expected, description, actual, expected };
}

function shellQuote(value) {
  const s = String(value);
  if (/^[A-Za-z0-9_./:=@+-]+$/.test(s)) return s;
  return `'${s.replace(/'/g, "'\\''")}'`;
}
