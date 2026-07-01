import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import process from "node:process";
import { acceptanceDir } from "./lib/acceptance-artifacts.mjs";

const repoRoot = path.resolve(import.meta.dirname, "..");
const artifactRoot = acceptanceDir(repoRoot);
const sessionSummaryPath = path.join(artifactRoot, "session-retro/session-retro-summary.json");
const timingPath = path.join(artifactRoot, "command-timings.jsonl");
const outputJSON = path.join(artifactRoot, "token-audit-latest.json");
const outputMD = path.join(artifactRoot, "token-audit-summary.md");

const commandProfiles = [
  {
    pattern: "pnpm exec playwright",
    risk: "medium",
    recommendation: "Summarize passing output; preserve failure trace, screenshots, assertions, and raw log.",
    estimatedSavings: 0.55,
  },
  {
    pattern: "go test",
    risk: "medium",
    recommendation: "Summarize passing package output; preserve failing test names, stacks, and raw log.",
    estimatedSavings: 0.6,
  },
  {
    pattern: "make goal-test-ui-audit",
    risk: "medium",
    recommendation: "Summarize route status and artifact paths; keep audit JSON/screenshots as source of truth.",
    estimatedSavings: 0.4,
  },
  {
    pattern: "make goal-test-training-performance-audit",
    risk: "medium",
    recommendation: "Summarize thresholds and failing routes; keep full JSON evidence.",
    estimatedSavings: 0.4,
  },
  {
    pattern: "make goal-test-deploy-dev",
    risk: "high",
    recommendation: "Do not hide failure windows; summarize only successful deploy/verify chatter.",
    estimatedSavings: 0.25,
  },
  {
    pattern: "node scripts/goal-test-environments.mjs verify",
    risk: "low",
    recommendation: "Summarize repeated successful checks; preserve non-OK service/log evidence.",
    estimatedSavings: 0.35,
  },
  {
    pattern: "node scripts/goal-test-environments.mjs verify-logs",
    risk: "medium",
    recommendation: "Preserve current marker error windows; summarize clean scans.",
    estimatedSavings: 0.25,
  },
  {
    pattern: "pnpm typecheck",
    risk: "medium",
    recommendation: "Summarize success; preserve all TypeScript error lines on failure.",
    estimatedSavings: 0.45,
  },
  {
    pattern: "git commit",
    risk: "low",
    recommendation: "Summarize status; raw log is enough for hook failures.",
    estimatedSavings: 0.25,
  },
];

const sessionSummary = readJSON(sessionSummaryPath);
const timings = readJSONL(timingPath);
const commandCounts = new Map((sessionSummary?.command_counts || []).map((item) => [item.key, Number(item.count || 0)]));
const timingByCommand = summarizeTimings(timings);
const rows = commandProfiles.map((profile) => {
  const count = commandCounts.get(profile.pattern) || 0;
  const timing = timingByCommand.find((item) => item.command.includes(profile.pattern) || item.command_id.includes(profile.pattern)) || null;
  return {
    command_pattern: profile.pattern,
    session_count: count,
    observed_runs: timing?.runs || 0,
    observed_failures: timing?.failures || 0,
    observed_avg_duration_ms: timing?.avg_duration_ms || 0,
    estimated_savings_ratio: profile.estimatedSavings,
    priority_score: Number((count * profile.estimatedSavings * riskMultiplier(profile.risk)).toFixed(2)),
    quality_risk: profile.risk,
    recommendation: profile.recommendation,
  };
}).sort((a, b) => b.priority_score - a.priority_score);

const sessionBytes = Number(sessionSummary?.bytes || 0);
const conservativeSavings = sessionBytes ? Math.round(sessionBytes * 0.4) : 0;
const expectedSavings = sessionBytes ? Math.round(sessionBytes * 0.5) : 0;
const aggressiveSavings = sessionBytes ? Math.round(sessionBytes * 0.6) : 0;
const payload = {
  schema: "multica.goal_test.token_audit.v1",
  generated_at: new Date().toISOString(),
  session_summary_path: existsSync(sessionSummaryPath) ? sessionSummaryPath : "",
  command_timings_path: existsSync(timingPath) ? timingPath : "",
  session_bytes: sessionBytes,
  session_lines: Number(sessionSummary?.lines || 0),
  estimated_session_savings: {
    conservative_bytes: conservativeSavings,
    expected_bytes: expectedSavings,
    aggressive_bytes: aggressiveSavings,
    conservative_ratio: 0.4,
    expected_ratio: 0.5,
    aggressive_ratio: 0.6,
  },
  policy: {
    default_optimizer: process.env.GOAL_TEST_TOKEN_OPTIMIZER || "builtin",
    rtk_mode: "optional; requested with GOAL_TEST_TOKEN_OPTIMIZER=rtk, but raw-preserving wrapper still keeps full logs",
    never_compress: ["rg/find/ls path discovery", "git diff", "failed test detail windows", "deploy failure windows", "panic/FATAL/ERROR windows"],
  },
  rows,
};

mkdirSync(artifactRoot, { recursive: true });
writeFileSync(outputJSON, `${JSON.stringify(payload, null, 2)}\n`);
writeFileSync(outputMD, renderMarkdown(payload));
console.log(JSON.stringify({ ok: true, json: outputJSON, markdown: outputMD, expected_savings_ratio: 0.5, top: rows.slice(0, 5) }, null, 2));

function summarizeTimings(items) {
  const byCommand = new Map();
  for (const item of items) {
    const key = item.command || item.command_id || "unknown";
    const existing = byCommand.get(key) || { command: key, command_id: item.command_id || "", runs: 0, failures: 0, total_duration_ms: 0 };
    existing.runs += 1;
    existing.failures += Number(item.exit_code || 0) === 0 ? 0 : 1;
    existing.total_duration_ms += Number(item.duration_ms || 0);
    byCommand.set(key, existing);
  }
  return Array.from(byCommand.values()).map((item) => ({
    ...item,
    avg_duration_ms: item.runs > 0 ? Math.round(item.total_duration_ms / item.runs) : 0,
  }));
}

function renderMarkdown(payload) {
  const lines = [
    "# Goal-Test Token Audit",
    "",
    `- Generated: ${payload.generated_at}`,
    `- Session bytes: ${payload.session_bytes}`,
    `- Expected savings: ${payload.estimated_session_savings.expected_bytes} bytes (${payload.estimated_session_savings.expected_ratio * 100}%)`,
    `- Optimizer: ${payload.policy.default_optimizer}`,
    "",
    "## Priorities",
    "",
    "| Command pattern | Session count | Risk | Est. savings | Recommendation |",
    "| --- | ---: | --- | ---: | --- |",
    ...payload.rows.map((row) => `| \`${row.command_pattern}\` | ${row.session_count} | ${row.quality_risk} | ${Math.round(row.estimated_savings_ratio * 100)}% | ${row.recommendation} |`),
    "",
    "## Guardrails",
    "",
    ...payload.policy.never_compress.map((item) => `- Never auto-compress ${item}.`),
    "",
  ];
  return `${lines.join("\n")}\n`;
}

function readJSON(file) {
  if (!existsSync(file)) return null;
  try {
    return JSON.parse(readFileSync(file, "utf8"));
  } catch {
    return null;
  }
}

function readJSONL(file) {
  if (!existsSync(file)) return [];
  return readFileSync(file, "utf8")
    .split(/\r?\n/)
    .filter(Boolean)
    .map((line) => {
      try {
        return JSON.parse(line);
      } catch {
        return null;
      }
    })
    .filter(Boolean);
}

function riskMultiplier(risk) {
  if (risk === "low") return 1;
  if (risk === "medium") return 0.8;
  return 0.55;
}
