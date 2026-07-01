import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const sessionPath = process.argv[2] || process.env.GOAL_TEST_SESSION_PATH || "";
const outputDir = path.resolve(process.env.GOAL_TEST_SESSION_RETRO_DIR || path.join(acceptanceDir(repoRoot), "session-retro"));

if (!sessionPath) fail("usage: node scripts/goal-test-session-retro.mjs <codex-session.jsonl>");
if (!existsSync(sessionPath)) fail(`session file not found: ${sessionPath}`);

const startedAt = Date.now();
const content = readFileSync(sessionPath, "utf8");
const lines = content.split(/\r?\n/).filter(Boolean);
const parsed = [];
const commandCounts = new Map();
const keywordHits = new Map();
const commandPatterns = [
  "pnpm typecheck",
  "pnpm exec playwright",
  "make goal-test-deploy-dev",
  "make goal-test-ui-audit",
  "make goal-test-training-performance-audit",
  "node scripts/goal-test-environments.mjs verify",
  "node scripts/goal-test-environments.mjs verify-logs",
  "go test",
  "git commit",
];
const keywords = [
  "Process exited with code 1",
  "Process exited with code 2",
  "context",
  "compaction",
  "interrupted",
  "rollback",
  "revert",
  "spawn_agent",
  "close_agent",
  "agent thread limit",
  "E2E_BASE_URL",
  "PLAYWRIGHT_BASE_URL",
  "DATABASE_URL",
  "ECONNREFUSED",
  "ERROR",
  "FATAL",
  "panic",
];

for (const [index, line] of lines.entries()) {
  const record = parseLine(line);
  if (record) parsed.push(record);
  for (const pattern of commandPatterns) {
    if (line.includes(pattern)) increment(commandCounts, pattern);
  }
  for (const keyword of keywords) {
    if (line.includes(keyword)) increment(keywordHits, keyword);
  }
  if ((index + 1) % 25_000 === 0) {
    // Keep the script responsive for very large sessions without noisy output.
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 1);
  }
}

const timestamps = parsed
  .map((record) => record.timestamp || record.time || record.created_at || record.createdAt)
  .filter(Boolean)
  .sort();
const summary = {
  schema: "multica.goal_test.session_retro.v1",
  generated_at: new Date().toISOString(),
  session_path: sessionPath,
  bytes: Buffer.byteLength(content),
  lines: lines.length,
  parsed_lines: parsed.length,
  first_timestamp: timestamps[0] || "",
  last_timestamp: timestamps[timestamps.length - 1] || "",
  elapsed_ms: Date.now() - startedAt,
  command_counts: entries(commandCounts),
  keyword_hits: entries(keywordHits),
  root_causes: buildRootCauses(commandCounts, keywordHits),
  ok: true,
};

mkdirSync(outputDir, { recursive: true });
writeFileSync(path.join(outputDir, "session-retro-summary.json"), `${JSON.stringify(summary, null, 2)}\n`);
writeFileSync(path.join(outputDir, "session-index.md"), renderSessionIndex(summary));
writeFileSync(path.join(outputDir, "root-cause-table.md"), renderRootCauseTable(summary));

console.log(JSON.stringify({
  ok: true,
  summary: path.join(outputDir, "session-retro-summary.json"),
  session_index: path.join(outputDir, "session-index.md"),
  root_cause_table: path.join(outputDir, "root-cause-table.md"),
}, null, 2));

function parseLine(line) {
  try {
    return JSON.parse(line);
  } catch {
    return null;
  }
}

function increment(map, key) {
  map.set(key, (map.get(key) || 0) + 1);
}

function entries(map) {
  return Array.from(map.entries())
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([key, count]) => ({ key, count }));
}

function count(map, key) {
  return map.get(key) || 0;
}

function buildRootCauses(commands, hits) {
  const roots = [];
  roots.push({
    id: "M1",
    cause: "E2E 环境真源漂移",
    signal: count(hits, "E2E_BASE_URL") + count(hits, "PLAYWRIGHT_BASE_URL") + count(hits, "DATABASE_URL"),
    fix: "先跑 goal-test-e2e-preflight，再运行 Playwright。",
  });
  roots.push({
    id: "M2",
    cause: "大门禁过频重跑",
    signal: count(commands, "make goal-test-deploy-dev") + count(commands, "make goal-test-ui-audit") + count(commands, "pnpm exec playwright"),
    fix: "用 goal-test-smoke 和定向测试先收敛，提交前再跑大门禁。",
  });
  roots.push({
    id: "M3",
    cause: "历史日志污染当前判断",
    signal: count(hits, "ECONNREFUSED") + count(hits, "ERROR") + count(commands, "node scripts/goal-test-environments.mjs verify-logs"),
    fix: "只看 deployment marker 后窗口，并在部署时归档旧日志。",
  });
  roots.push({
    id: "M4",
    cause: "上下文恢复成本过高",
    signal: count(hits, "compaction") + count(hits, "context"),
    fix: "首切片生成 session-index/root-cause-table/final-evidence。",
  });
  roots.push({
    id: "M5",
    cause: "子 Agent 生命周期治理不足",
    signal: Math.max(0, count(hits, "spawn_agent") + count(hits, "agent thread limit") - count(hits, "close_agent")),
    fix: "每个切片收集结果后立即 close；遇到并发上限先释放槽位。",
  });
  return roots.sort((a, b) => b.signal - a.signal);
}

function renderSessionIndex(summary) {
  const lines = [
    "# Session Index",
    "",
    `- Session: ${summary.session_path}`,
    `- Generated: ${summary.generated_at}`,
    `- Lines: ${summary.lines}`,
    `- Parsed lines: ${summary.parsed_lines}`,
    `- Bytes: ${summary.bytes}`,
    `- First timestamp: ${summary.first_timestamp || "unknown"}`,
    `- Last timestamp: ${summary.last_timestamp || "unknown"}`,
    "",
    "## Repeated Commands",
    "",
    "| Command | Count |",
    "| --- | ---: |",
    ...summary.command_counts.map((item) => `| \`${item.key}\` | ${item.count} |`),
    "",
    "## Keyword Signals",
    "",
    "| Signal | Count |",
    "| --- | ---: |",
    ...summary.keyword_hits.map((item) => `| \`${item.key}\` | ${item.count} |`),
    "",
  ];
  return `${lines.join("\n")}\n`;
}

function renderRootCauseTable(summary) {
  const lines = [
    "# Root Cause Table",
    "",
    "| ID | Root Cause | Signal | Fix |",
    "| --- | --- | ---: | --- |",
    ...summary.root_causes.map((item) => `| ${item.id} | ${item.cause} | ${item.signal} | ${item.fix} |`),
    "",
    "## Gate Recommendation",
    "",
    "1. Run `make goal-test-smoke` before broad UI/E2E audits.",
    "2. Run targeted tests for the changed surface.",
    "3. Run UI audit, training performance audit, full E2E, log scan, ledger update, and commit only after targeted checks pass.",
    "",
  ];
  return `${lines.join("\n")}\n`;
}

function fail(message) {
  console.error(message);
  process.exit(2);
}
