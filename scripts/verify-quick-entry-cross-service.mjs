#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = path.join(repoRoot, "artifacts", "acceptance");
const now = new Date().toISOString();
const stamp = now.replace(/[:.]/g, "-");

const repos = {
  usercenter: "/data/ida/user-center",
  gateway: "/data/ida/gateway",
  deployment: "/data/ida/ida-deployment",
};

const evidence = {
  schema: "ida.quick_entry_cross_service_sandbox.v1",
  generated_at: now,
  sandbox_mode: "hermetic-process-boundary",
  endpoint: "GET /v1/user-center/quick-entry-capability",
  semantic_guard: "gateway derives userId from authenticated request context and ignores supplied query/body userId",
  repos: {},
  checks: [],
  cross_service_curl: {
    ok: false,
    sandbox_gateway_url: "httptest://gateway/v1/user-center/quick-entry-capability",
    public_gateway_url: "httptest://gateway/v1/user-center/quick-entry-capability",
    curl_path: "/v1/user-center/quick-entry-capability?userId=999",
    boundary: "curl -> gateway HTTP handler -> generated user-center gRPC client -> TCP user-center gRPC server -> generated user-center protobuf response",
  },
};

fs.mkdirSync(artifactRoot, { recursive: true });

try {
  for (const [key, repo] of Object.entries(repos)) {
    evidence.repos[key] = repoState(repo);
  }

  runCheck("usercenter_logic", repos.usercenter, [
    "go", "test", "./internal/logic",
    "-run", "TestGetQuickEntryCapability|TestGetPrivateUserContext",
    "-count=1",
  ]);

  runCheck("usercenter_grpc_server", repos.usercenter, [
    "go", "test", "./internal/server",
    "-run", "TestUserCenterServerGetQuickEntryCapabilityGRPC",
    "-count=1",
  ]);

  runCheck("gateway_http_to_usercenter_grpc_curl", repos.gateway, [
    "bash", "-lc",
    [
      "tmp=$(mktemp -d)",
      "cd \"$tmp\"",
      "go work init /data/ida/gateway /data/ida/user-center",
      "GOWORK=\"$tmp/go.work\" go test -v /data/ida/gateway/internal/handler/gateway -run 'TestQuickEntryCapabilityCurlThroughUserCenterGRPC|TestQuickEntryCapabilityHandlerCurlUsesContextUser' -count=1",
      "rc=$?",
      "rm -rf \"$tmp\"",
      "exit $rc",
    ].join("; "),
  ]);

  runCheck("gateway_route_and_apidata", repos.gateway, [
    "bash", "-lc",
    [
      "tmp=$(mktemp -d)",
      "cd \"$tmp\"",
      "go work init /data/ida/gateway /data/ida/user-center",
      "GOWORK=\"$tmp/go.work\" go test /data/ida/gateway/internal/handler /data/ida/gateway/internal/apidata -count=1",
      "rc=$?",
      "rm -rf \"$tmp\"",
      "exit $rc",
    ].join("; "),
  ]);

  runCheck("ida_deployment_permission_render", repos.deployment, [
    "bash", "-lc",
    [
      "node -e \"for (const f of ['helm/public/charts/usercenter/permissions/api/user-center.api.json','helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode1.json','helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode2.json','helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode3.json','helm/front/charts/gateway/apiData/permissions_file/generated_apiData_mode4.json']) { const j=require('./'+f); const s=JSON.stringify(j); if (!s.includes('/v1/user-center/quick-entry-capability')) throw new Error(f + ' missing quick-entry-capability'); }\"",
      "helm template ida-front helm/front -f helm/front/values.yaml >/tmp/ida-front-quick-entry-render.yaml",
      "rg -n '/v1/user-center/quick-entry-capability' /tmp/ida-front-quick-entry-render.yaml",
    ].join(" && "),
  ]);

  evidence.ok = evidence.checks.every((check) => check.ok);
  evidence.cross_service_curl.ok = evidence.ok;
  evidence.cross_service_curl.usercenter_commit = evidence.repos.usercenter.commit;
  evidence.cross_service_curl.gateway_commit = evidence.repos.gateway.commit;
  evidence.cross_service_curl.deployment_commit = evidence.repos.deployment.commit;
  evidence.cross_service_curl.usercenter_branch = evidence.repos.usercenter.branch;
  evidence.cross_service_curl.gateway_branch = evidence.repos.gateway.branch;
  evidence.cross_service_curl.deployment_branch = evidence.repos.deployment.branch;
} catch (error) {
  evidence.ok = false;
  evidence.error = error instanceof Error ? error.stack || error.message : String(error);
}

const jsonPath = path.join(artifactRoot, `quick-entry-cross-service-${stamp}.json`);
const latestPath = path.join(artifactRoot, "quick-entry-cross-service-latest.json");
fs.writeFileSync(jsonPath, `${JSON.stringify(evidence, null, 2)}\n`);
fs.writeFileSync(latestPath, `${JSON.stringify(evidence, null, 2)}\n`);

console.log(JSON.stringify({
  ok: evidence.ok,
  json: jsonPath,
  latest: latestPath,
  checks: evidence.checks.map((check) => ({ id: check.id, ok: check.ok, status: check.status })),
}, null, 2));
if (!evidence.ok) process.exitCode = 1;

function runCheck(id, cwd, command) {
  const started = Date.now();
  const check = {
    id,
    cwd,
    command: command.map(shellQuote).join(" "),
    ok: false,
    status: "failed",
    duration_ms: 0,
    stdout: "",
    stderr: "",
  };
  evidence.checks.push(check);
  try {
    const output = execFileSync(command[0], command.slice(1), {
      cwd,
      encoding: "utf8",
      maxBuffer: 20 * 1024 * 1024,
      stdio: ["ignore", "pipe", "pipe"],
    });
    check.stdout = output;
    check.ok = true;
    check.status = "passed";
  } catch (error) {
    check.stdout = String(error.stdout || "");
    check.stderr = String(error.stderr || "");
    check.error = error instanceof Error ? error.message : String(error);
  } finally {
    check.duration_ms = Date.now() - started;
  }
  if (!check.ok) {
    throw new Error(`${id} failed: ${check.error || check.stderr || check.stdout}`);
  }
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
  return execFileSync(command[0], command.slice(1), {
    cwd,
    encoding: "utf8",
    maxBuffer: 1024 * 1024,
  });
}

function shellQuote(value) {
  const s = String(value);
  if (/^[A-Za-z0-9_./:=@+-]+$/.test(s)) return s;
  return `'${s.replace(/'/g, "'\\''")}'`;
}
