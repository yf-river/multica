#!/usr/bin/env node

import { execFileSync } from "node:child_process";
import fs from "node:fs";
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
  sopAgent: "/data/ida/sopAgent",
};

fs.mkdirSync(artifactRoot, { recursive: true });

const report = {
  schema: "multica.goal_test.historical_benchmark_readiness.v1",
  generated_at: generatedAt,
  repos: Object.fromEntries(Object.entries(repos).map(([key, repo]) => [key, repoState(repo)])),
  cases: [],
};

caseLinkUserSpaceOrg();
caseProductApiPermission();
caseAnnouncementNotice();
caseNoticeSameSecondCursor();

report.summary = summarize(report.cases);
report.ok = report.cases.every((item) => item.status === "ready_for_service_sandbox");

const jsonPath = path.join(artifactRoot, `historical-benchmark-readiness-${stamp}.json`);
const markdownPath = path.join(artifactRoot, `historical-benchmark-readiness-${stamp}.md`);
const latestJsonPath = path.join(artifactRoot, "historical-benchmark-readiness-latest.json");
const latestMarkdownPath = path.join(artifactRoot, "historical-benchmark-readiness-latest.md");

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
  summary: report.summary,
}, null, 2));

function caseLinkUserSpaceOrg() {
  const checks = [];
  checks.push(fileExists("sopagent_task", path.join(repos.sopAgent, "benchmarks/user-center/add-api/link-user-space-org/task.yaml")));
  checks.push(fileExists("sopagent_expected", path.join(repos.sopAgent, "benchmarks/user-center/add-api/link-user-space-org/expected.md")));
  checks.push(contains("proto_rpc", repos.usercenter, "proto/user_center.proto", "rpc LinkUserSpaceOrg(LinkUserSpaceOrgReq)"));
  checks.push(contains("logic_dedup", repos.usercenter, "internal/logic/linkuserspaceorglogic.go", "target := make(map[string]*models.UserSpaceOrgRel"));
  checks.push(contains("logic_transaction", repos.usercenter, "internal/logic/linkuserspaceorglogic.go", "DBHandle.Transaction"));
  checks.push(run("focused_go_test_probe", repos.usercenter, [
    "go", "test", "./internal/logic", "-run", "TestLinkUserSpaceOrg", "-count=1",
  ], { allowNoTests: true }));

  const hasDirectTest = checks.at(-1).stdout.includes("no tests to run") === false;
  report.cases.push({
    id: "link-user-space-org",
    suite: "historical-replay",
    source: "/data/ida/sopAgent/benchmarks/user-center/add-api/link-user-space-org",
    status: hasDirectTest ? statusFromChecks(checks) : "needs_test_or_service_sandbox",
    checks,
    missing: hasDirectTest ? [] : [
      "当前 user-center 未发现 TestLinkUserSpaceOrg 直接测试；不能只用静态实现存在证明历史 benchmark 可复跑",
      "尚未形成 gateway HTTP/public API curl 或 user-center gRPC service-process verifier",
    ],
  });
}

function caseProductApiPermission() {
  const checks = [];
  checks.push(contains("source_product_api", repos.deployment, "helm/public/charts/usercenter/permissions/api/product.api.json", "/v1/product/example-operation"));
  checks.push(run("generate_permissions_dry_run", repos.deployment, [
    "bash", "-lc", productPermissionDryRunCommand(),
  ]));
  checks.push(run("helm_front_template", repos.deployment, [
    "bash", "-lc", "helm template ida-front helm/front -f helm/front/values.yaml >/tmp/ida-front-product-benchmark-render.yaml",
  ]));
  report.cases.push({
    id: "product-api-permission",
    suite: "historical-replay",
    source: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计v1/35-real-demand-pilot-and-deployment-permission-benchmark-20260616.md",
    status: statusFromChecks(checks),
    checks,
    missing: checks.every((check) => check.ok) ? [
      "仍需接入统一 benchmark trace/eval artifact；当前只证明 deployment permission/apiData dry-run 可复跑",
    ] : [],
  });
}

function caseAnnouncementNotice() {
  const checks = [];
  checks.push(contains("proto_save_announcement", repos.usercenter, "proto/user_center.proto", "rpc SaveAnnouncement(SaveAnnouncementRequest)"));
  checks.push(run("announcement_logic_tests", repos.usercenter, [
    "go", "test", "./internal/logic", "-run", "Test(SaveAnnouncement|GetAnnouncementList|GetAnnouncementDetail)", "-count=1", "-v",
  ]));
  report.cases.push({
    id: "announcement-notice",
    suite: "historical-replay",
    source: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计v1/35-real-demand-pilot-and-deployment-permission-benchmark-20260616.md",
    status: statusFromChecks(checks),
    checks,
    missing: checks.every((check) => check.ok) ? [
      "尚未形成 gateway/deployment service-level curl/API verifier",
      "尚未接入 trace/eval/optimizer artifact",
    ] : [],
  });
}

function caseNoticeSameSecondCursor() {
  const checks = [];
  checks.push(contains("dao_same_second_query", repos.usercenter, "internal/dao/notice.go", "同一时间"));
  checks.push(run("notice_same_second_dao_test", repos.usercenter, [
    "go", "test", "./internal/dao", "-run", "TestListNoticesCreatedAfterIncludesSameSecondAndOrdersByID", "-count=1", "-v",
  ]));
  checks.push(run("sync_notice_logic_probe", repos.usercenter, [
    "go", "test", "./internal/logic", "-run", "TestSyncNotice", "-count=1",
  ], { allowNoTests: true }));
  const hasSyncLogicTest = checks.at(-1).stdout.includes("no tests to run") === false;
  report.cases.push({
    id: "notice-same-second-cursor",
    suite: "historical-replay",
    source: "/data/ida/docs/tapd/20260605-ai设计/全流程sop设计v1/35-real-demand-pilot-and-deployment-permission-benchmark-20260616.md",
    status: hasSyncLogicTest ? statusFromChecks(checks) : "needs_logic_or_service_sandbox",
    checks,
    missing: hasSyncLogicTest ? [] : [
      "DAO same-second ordering test exists and passes, but SyncNotice logic has no direct TestSyncNotice coverage",
      "尚未形成 controlled same-second service-level sync curl verifier",
    ],
  });
}

function productPermissionDryRunCommand() {
  return [
    "tmp=$(mktemp -d)",
    "trap 'rm -rf \"$tmp\"' EXIT",
    "cd helm/public/charts/usercenter/permissions",
    "./generate_permissions.sh all all \"$tmp/usercenter\" \"$tmp/gateway\" >/tmp/goal-test-product-permission-generate.log",
    "node -e \"const fs=require('fs'); const modes=[1,2,3,4]; for (const m of modes) { const p=process.argv[1] + '/gateway/generated_apiData_mode' + m + '.json'; const s=fs.readFileSync(p,'utf8'); const has=s.includes('/v1/product/example-operation'); if ((m===1 || m===2) && !has) throw new Error('mode'+m+' missing product example operation'); if ((m===3 || m===4) && has) throw new Error('mode'+m+' unexpectedly contains product example operation'); }\" \"$tmp\"",
  ].join(" && ");
}

function fileExists(id, filePath) {
  return {
    id,
    kind: "file_exists",
    path: filePath,
    ok: fs.existsSync(filePath),
    status: fs.existsSync(filePath) ? "passed" : "failed",
  };
}

function contains(id, repo, relativePath, needle) {
  const filePath = path.join(repo, relativePath);
  const stdout = fs.existsSync(filePath) ? fs.readFileSync(filePath, "utf8") : "";
  const ok = stdout.includes(needle);
  return {
    id,
    kind: "contains",
    cwd: repo,
    path: relativePath,
    needle,
    ok,
    status: ok ? "passed" : "failed",
  };
}

function run(id, cwd, command, options = {}) {
  const check = {
    id,
    kind: "command",
    cwd,
    command: command.map(shellQuote).join(" "),
    ok: false,
    status: "failed",
    stdout: "",
    stderr: "",
  };
  try {
    const output = execFileSync(command[0], command.slice(1), {
      cwd,
      encoding: "utf8",
      maxBuffer: 20 * 1024 * 1024,
      env: { ...process.env, GOWORK: "off" },
    });
    check.stdout = output;
    check.ok = true;
    check.status = output.includes("no tests to run") && !options.allowNoTests ? "no_tests" : "passed";
    if (check.status === "no_tests") check.ok = false;
  } catch (error) {
    check.stdout = String(error.stdout || "");
    check.stderr = String(error.stderr || "");
    check.error = error instanceof Error ? error.message : String(error);
  }
  return check;
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

function statusFromChecks(checks) {
  return checks.every((check) => check.ok) ? "ready_for_service_sandbox" : "blocked_or_missing_check";
}

function summarize(cases) {
  return cases.reduce((acc, item) => {
    acc[item.status] = (acc[item.status] || 0) + 1;
    return acc;
  }, {});
}

function renderMarkdown(data) {
  const lines = [
    "# Historical Benchmark Readiness",
    "",
    `Generated: ${data.generated_at}`,
    "",
    `OK: ${data.ok}`,
    "",
    "## Summary",
    "",
    "```json",
    JSON.stringify(data.summary, null, 2),
    "```",
    "",
    "## Cases",
    "",
  ];
  for (const item of data.cases) {
    lines.push(`### ${item.id}`, "");
    lines.push(`- status: ${item.status}`);
    lines.push(`- source: ${item.source}`);
    lines.push(`- missing: ${item.missing.length ? item.missing.join("; ") : "none"}`);
    lines.push("- checks:");
    for (const check of item.checks) {
      lines.push(`  - ${check.id}: ${check.status}`);
    }
    lines.push("");
  }
  return `${lines.join("\n")}\n`;
}

function shellQuote(value) {
  const s = String(value);
  if (/^[A-Za-z0-9_./:=@+-]+$/.test(s)) return s;
  return `'${s.replace(/'/g, "'\\''")}'`;
}
