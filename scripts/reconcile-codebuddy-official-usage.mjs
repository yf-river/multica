#!/usr/bin/env node
import crypto from "node:crypto";
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";

const args = parseArgs(process.argv.slice(2));

if (args["self-test"]) {
  runSelfTest();
  process.exit(0);
}

const officialFile = args["official-file"] ? String(args["official-file"]) : "";
if (!officialFile) {
  fail("Pass --official-file=<usage-details.json>. Do not pass cookies or page tokens.");
}

const envName = String(args.env || "int");
const databaseURL = args["database-url"] ? String(args["database-url"]) : readDatabaseURL(envName);
if (!databaseURL) fail(`DATABASE_URL not found for env ${envName}.`);

const timeWindowMinutes = Number(args["time-window-minutes"] || 30);
const maxCandidates = Number(args["max-candidates"] || 5);
const officialRows = loadOfficialRows(officialFile);
if (officialRows.length === 0) fail(`No official usage rows found in ${officialFile}.`);

const range = officialRange(officialRows, timeWindowMinutes);
const client = new pg.Client({ connectionString: databaseURL });
await client.connect();
try {
  const multicaRows = await loadMulticaCodebuddyRows(client, range.start, range.end);
  const report = reconcileRows(officialRows, multicaRows, { timeWindowMinutes, maxCandidates });
  const output = JSON.stringify(report, null, 2);
  if (args.out) {
    fs.writeFileSync(String(args.out), output + "\n", "utf8");
    console.log(String(args.out));
  } else {
    console.log(output);
  }
} finally {
  await client.end();
}

function parseArgs(rawArgs) {
  const result = {};
  for (const raw of rawArgs) {
    if (raw === "--self-test") {
      result["self-test"] = true;
      continue;
    }
    if (!raw.startsWith("--")) continue;
    const [key, ...valueParts] = raw.slice(2).split("=");
    result[key] = valueParts.length > 0 ? valueParts.join("=") : true;
  }
  return result;
}

function readDatabaseURL(envName) {
  const envFile = path.join(".run", "env", `goal-test-${envName}.env`);
  if (!fs.existsSync(envFile)) return process.env.DATABASE_URL || "";
  const env = Object.fromEntries(fs.readFileSync(envFile, "utf8")
    .split(/\r?\n/)
    .filter((line) => line && !line.trim().startsWith("#"))
    .map((line) => {
      const index = line.indexOf("=");
      return [line.slice(0, index), line.slice(index + 1)];
    }));
  return env.DATABASE_URL || process.env.DATABASE_URL || "";
}

function loadOfficialRows(file) {
  const parsed = JSON.parse(fs.readFileSync(file, "utf8"));
  const rows = Array.isArray(parsed)
    ? parsed
    : Array.isArray(parsed?.data)
      ? parsed.data
      : Array.isArray(parsed?.samples)
        ? parsed.samples
        : [];
  return rows
    .filter((row) => {
      const platform = String(row.platform_raw || row.platform || "").toLowerCase();
      return !platform || platform.includes("workbuddy") || platform.includes("codebuddy");
    })
    .map((row, index) => normalizeOfficialRow(row, index));
}

function normalizeOfficialRow(row, index) {
  const inputTokens = parseInteger(row.input_tokens ?? row.input);
  const outputTokens = parseInteger(row.output_tokens ?? row.output);
  const totalTokens = parseInteger(row.total_tokens ?? row.total);
  const cacheTokens = parseInteger(row.cache_tokens ?? row.cache);
  const cacheWriteTokens = parseInteger(row.cache_write_tokens ?? row.cache_write);
  const costCny = parseMoney(row.cost);
  const requestAt = parseOfficialRequestTime(row.request_time);
  const userInput = String(row.user_input || "");
  return {
    index,
    request_time: row.request_time || "",
    request_at: requestAt.toISOString(),
    request_at_ms: requestAt.getTime(),
    user_name: String(row.user_name || ""),
    model_name: String(row.model_name || row.model || "DeepSeek-V4-Pro"),
    model_key: modelKey(row.model_name || row.model || "DeepSeek-V4-Pro"),
    platform: String(row.platform || ""),
    platform_raw: String(row.platform_raw || ""),
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    total_tokens: totalTokens,
    cache_tokens: cacheTokens,
    cache_write_tokens: cacheWriteTokens,
    cost_cny: costCny,
    message_id: String(row.message_id || ""),
    user_input_sha256: sha256(userInput),
    user_input_prefix: userInput.slice(0, 160),
    official_total_consistent: totalTokens === inputTokens + outputTokens,
  };
}

async function loadMulticaCodebuddyRows(client, start, end) {
  const res = await client.query(`
    SELECT
      tu.id::text AS usage_id,
      tu.task_id::text AS task_id,
      tu.provider,
      tu.model,
      tu.input_tokens::bigint AS input_tokens,
      tu.output_tokens::bigint AS output_tokens,
      tu.cache_read_tokens::bigint AS cache_read_tokens,
      tu.cache_write_tokens::bigint AS cache_write_tokens,
      tu.created_at,
      tu.updated_at,
      atq.issue_id::text AS issue_id,
      atq.agent_id::text AS agent_id,
      atq.runtime_id::text AS runtime_id,
      atq.session_id,
      atq.status,
      atq.created_at AS task_created_at,
      atq.started_at,
      atq.completed_at,
      atq.trigger_comment_id::text AS trigger_comment_id,
      atq.trigger_summary,
      i.number AS issue_number,
      i.title AS issue_title,
      a.name AS agent_name,
      ar.name AS runtime_name
    FROM task_usage tu
    JOIN agent_task_queue atq ON atq.id = tu.task_id
    LEFT JOIN issue i ON i.id = atq.issue_id
    LEFT JOIN agent a ON a.id = atq.agent_id
    LEFT JOIN agent_runtime ar ON ar.id = atq.runtime_id
    WHERE LOWER(tu.provider) = 'codebuddy'
      AND COALESCE(atq.started_at, tu.created_at, atq.created_at) >= $1::timestamptz
      AND COALESCE(atq.started_at, tu.created_at, atq.created_at) <= $2::timestamptz
    ORDER BY COALESCE(atq.started_at, tu.created_at, atq.created_at), tu.id
  `, [start.toISOString(), end.toISOString()]);
  return res.rows.map(normalizeMulticaRow);
}

function normalizeMulticaRow(row, index) {
  const inputTokens = Number(row.input_tokens || 0);
  const outputTokens = Number(row.output_tokens || 0);
  const cacheReadTokens = Number(row.cache_read_tokens || 0);
  const cacheWriteTokens = Number(row.cache_write_tokens || 0);
  const effectiveInputTokens = codeBuddyEffectiveInputTokens(inputTokens, cacheReadTokens, cacheWriteTokens);
  const eventAt = new Date(row.started_at || row.created_at || row.task_created_at);
  const costUSD = estimateCodeBuddyCostUSD(row.model, effectiveInputTokens, outputTokens, cacheReadTokens);
  return {
    index,
    usage_id: row.usage_id,
    task_id: row.task_id,
    issue_id: row.issue_id,
    issue_number: row.issue_number,
    issue_title: row.issue_title,
    agent_id: row.agent_id,
    agent_name: row.agent_name,
    runtime_id: row.runtime_id,
    runtime_name: row.runtime_name,
    session_id: row.session_id,
    trigger_comment_id: row.trigger_comment_id,
    trigger_summary: row.trigger_summary,
    status: row.status,
    provider: String(row.provider || ""),
    model: String(row.model || ""),
    model_key: modelKey(row.model),
    input_tokens: inputTokens,
    effective_input_tokens: effectiveInputTokens,
    output_tokens: outputTokens,
    total_tokens: effectiveInputTokens + outputTokens,
    cache_read_tokens: cacheReadTokens,
    cache_write_tokens: cacheWriteTokens,
    cost_usd: costUSD,
    event_at: eventAt.toISOString(),
    event_at_ms: eventAt.getTime(),
    created_at: new Date(row.created_at).toISOString(),
    updated_at: row.updated_at ? new Date(row.updated_at).toISOString() : null,
  };
}

function reconcileRows(officialRows, multicaRows, options) {
  const usedMultica = new Set();
  const matches = [];
  const unmatchedOfficial = [];

  for (const official of officialRows) {
    const candidates = multicaRows
      .filter((row) => !usedMultica.has(row.usage_id))
      .map((row) => scoreCandidate(official, row, options.timeWindowMinutes))
      .filter((item) => item.score > 0)
      .toSorted((a, b) => b.score - a.score || a.time_delta_seconds - b.time_delta_seconds);

    const best = candidates[0];
    if (!best || best.score < 45) {
      unmatchedOfficial.push({
        official,
        candidates: candidates.slice(0, options.maxCandidates).map(stripCandidate),
      });
      continue;
    }

    usedMultica.add(best.multica.usage_id);
    matches.push({
      confidence: confidenceLabel(best.score),
      score: best.score,
      official,
      multica: best.multica,
      deltas: candidateDeltas(official, best.multica, best.time_delta_seconds),
      alternates: candidates.slice(1, options.maxCandidates).map(stripCandidate),
    });
  }

  const unmatchedMultica = multicaRows
    .filter((row) => !usedMultica.has(row.usage_id))
    .map((row) => ({
      task_id: row.task_id,
      issue_number: row.issue_number,
      model: row.model,
      event_at: row.event_at,
      total_tokens: row.total_tokens,
      cache_read_tokens: row.cache_read_tokens,
      cache_write_tokens: row.cache_write_tokens,
      cost_usd: row.cost_usd,
    }));

  return {
    schema: "multica.codebuddy_official_reconcile.v1",
    generated_at: new Date().toISOString(),
    official_rows: officialRows.length,
    multica_rows: multicaRows.length,
    matched_rows: matches.length,
    unmatched_official_rows: unmatchedOfficial.length,
    unmatched_multica_rows: unmatchedMultica.length,
    summary: summarizeMatches(matches, officialRows, multicaRows),
    matches,
    unmatched_official: unmatchedOfficial,
    unmatched_multica: unmatchedMultica,
  };
}

function scoreCandidate(official, multica, timeWindowMinutes) {
  const timeDeltaSeconds = Math.abs(official.request_at_ms - multica.event_at_ms) / 1000;
  if (timeDeltaSeconds > timeWindowMinutes * 60) return { score: 0, official, multica, time_delta_seconds: timeDeltaSeconds };

  let score = 0;
  if (official.model_key && official.model_key === multica.model_key) score += 25;
  if (official.total_tokens === multica.total_tokens) score += 35;
  if (official.input_tokens === multica.effective_input_tokens) score += 20;
  if (official.output_tokens === multica.output_tokens) score += 15;
  if (official.cache_tokens === multica.cache_read_tokens) score += 15;
  if (timeDeltaSeconds <= 120) score += 15;
  else if (timeDeltaSeconds <= 600) score += 8;
  else score += 3;
  if (official.user_input_prefix && multica.trigger_summary && official.user_input_prefix.includes(multica.trigger_summary.slice(0, 40))) score += 10;
  return { score, official, multica, time_delta_seconds: Math.round(timeDeltaSeconds) };
}

function candidateDeltas(official, multica, timeDeltaSeconds) {
  return {
    time_delta_seconds: timeDeltaSeconds,
    input_tokens: multica.effective_input_tokens - official.input_tokens,
    output_tokens: multica.output_tokens - official.output_tokens,
    total_tokens: multica.total_tokens - official.total_tokens,
    cache_read_tokens: multica.cache_read_tokens - official.cache_tokens,
    cache_write_tokens: multica.cache_write_tokens - official.cache_write_tokens,
    cost_usd: multica.cost_usd,
    official_cost_cny: official.cost_cny,
  };
}

function summarizeMatches(matches, officialRows, multicaRows) {
  const tokenMatched = matches.filter((match) =>
    match.deltas.input_tokens === 0 &&
    match.deltas.output_tokens === 0 &&
    match.deltas.total_tokens === 0 &&
    match.deltas.cache_read_tokens === 0);
  return {
    official_total_consistent_rows: officialRows.filter((row) => row.official_total_consistent).length,
    token_exact_matches: tokenMatched.length,
    token_exact_match_rate: officialRows.length > 0 ? round(tokenMatched.length / officialRows.length, 6) : 0,
    official_total_tokens: sum(officialRows, (row) => row.total_tokens),
    multica_matched_total_tokens: sum(matches, (match) => match.multica.total_tokens),
    total_token_delta: sum(matches, (match) => match.deltas.total_tokens),
    official_cache_tokens: sum(officialRows, (row) => row.cache_tokens),
    multica_matched_cache_read_tokens: sum(matches, (match) => match.multica.cache_read_tokens),
    multica_window_total_tokens: sum(multicaRows, (row) => row.total_tokens),
    official_cost_cny: round(sum(officialRows, (row) => row.cost_cny), 6),
    multica_matched_cost_usd: round(sum(matches, (match) => match.multica.cost_usd), 6),
  };
}

function stripCandidate(candidate) {
  return {
    score: candidate.score,
    time_delta_seconds: candidate.time_delta_seconds,
    task_id: candidate.multica.task_id,
    issue_number: candidate.multica.issue_number,
    model: candidate.multica.model,
    event_at: candidate.multica.event_at,
    total_tokens: candidate.multica.total_tokens,
    cache_read_tokens: candidate.multica.cache_read_tokens,
  };
}

function confidenceLabel(score) {
  if (score >= 105) return "exact";
  if (score >= 80) return "strong";
  if (score >= 60) return "medium";
  return "weak";
}

function officialRange(rows, padMinutes) {
  const times = rows.map((row) => row.request_at_ms);
  return {
    start: new Date(Math.min(...times) - padMinutes * 60 * 1000),
    end: new Date(Math.max(...times) + padMinutes * 60 * 1000),
  };
}

function parseOfficialRequestTime(value) {
  const raw = String(value || "").trim();
  const match = raw.match(/^(\d{4})-(\d{2})-(\d{2})[ T](\d{2}):(\d{2}):(\d{2})$/);
  if (!match) {
    const parsed = new Date(raw);
    if (!Number.isNaN(parsed.getTime())) return parsed;
    fail(`Invalid official request_time: ${raw}`);
  }
  const [, y, mo, d, h, mi, s] = match;
  return new Date(`${y}-${mo}-${d}T${h}:${mi}:${s}+08:00`);
}

function modelKey(value) {
  const raw = String(value || "").toLowerCase();
  if (raw.includes("deepseek") && raw.includes("v4") && raw.includes("pro")) return "deepseek-v4-pro";
  if (raw.includes("deepseek") && raw.includes("v4") && raw.includes("flash")) return "deepseek-v4-flash";
  if (raw.includes("kimi") && raw.includes("k2.7")) return "kimi-k2.7";
  if (raw.includes("kimi") && raw.includes("k2.6")) return "kimi-k2.6";
  return raw.replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "");
}

function parseInteger(value) {
  if (value == null || value === "") return 0;
  const normalized = String(value).replace(/,/g, "").trim();
  const parsed = Number.parseInt(normalized, 10);
  return Number.isFinite(parsed) ? parsed : 0;
}

function parseMoney(value) {
  if (value == null || value === "") return 0;
  const normalized = String(value).replace(/[¥,\s]/g, "");
  const parsed = Number.parseFloat(normalized);
  return Number.isFinite(parsed) ? parsed : 0;
}

function codeBuddyEffectiveInputTokens(inputTokens, cacheReadTokens, cacheWriteTokens) {
  if (inputTokens < cacheReadTokens + cacheWriteTokens) {
    return inputTokens + cacheReadTokens + cacheWriteTokens;
  }
  return inputTokens;
}

function estimateCodeBuddyCostUSD(model, inputTokens, outputTokens, cacheReadTokens) {
  const price = priceForModel(model);
  if (!price) return 0;
  const uncachedInputTokens = Math.max(inputTokens - cacheReadTokens, 0);
  const input = roundCost(uncachedInputTokens * price.input / 1_000_000);
  const output = roundCost(outputTokens * price.output / 1_000_000);
  const cacheRead = roundCost(cacheReadTokens * price.cacheRead / 1_000_000);
  return roundCost(input + output + cacheRead);
}

function priceForModel(model) {
  const key = modelKey(model);
  if (key === "deepseek-v4-pro") return { input: 0.435, cacheRead: 0.003625, output: 0.87 };
  if (key === "deepseek-v4-flash") return { input: 0.14, cacheRead: 0.0028, output: 0.28 };
  if (key === "kimi-k2.6") return { input: 0.95, cacheRead: 0.16, output: 4.00 };
  if (key === "kimi-k2.7") return { input: 0.95, cacheRead: 0.19, output: 4.00 };
  return null;
}

function roundCost(value) {
  return Math.round(value * 1_000_000) / 1_000_000;
}

function round(value, digits) {
  const factor = 10 ** digits;
  return Math.round(value * factor) / factor;
}

function sha256(value) {
  return crypto.createHash("sha256").update(value).digest("hex");
}

function sum(rows, fn) {
  return rows.reduce((acc, row) => acc + Number(fn(row) || 0), 0);
}

function runSelfTest() {
  const official = [normalizeOfficialRow({
    request_time: "2026-07-09 10:47:51",
    user_name: "yunfeihu",
    model_name: "DeepSeek-V4-Pro",
    platform: "CodeBuddy IDE/WorkBuddy",
    platform_raw: "workbuddy-suite",
    input_tokens: "59,738",
    output_tokens: "186",
    total_tokens: "59,924",
    cache_tokens: 29440,
    cache_write_tokens: 0,
    cost: "¥0.09",
    user_input: "You are running as a local coding agent for a Multica workspace.",
    message_id: "4b3dd7d0de504823abfbf9b41a65c95a",
  }, 0)];
  const multica = [normalizeMulticaRow({
    usage_id: "usage-1",
    task_id: "task-1",
    provider: "codebuddy",
    model: "deepseek-v4-pro-ioa",
    input_tokens: "59738",
    output_tokens: "186",
    cache_read_tokens: "29440",
    cache_write_tokens: "0",
    created_at: "2026-07-09T02:47:51.000Z",
    updated_at: "2026-07-09T02:47:51.000Z",
    started_at: "2026-07-09T02:47:51.000Z",
    task_created_at: "2026-07-09T02:47:50.000Z",
  }, 0)];
  const report = reconcileRows(official, multica, { timeWindowMinutes: 30, maxCandidates: 5 });
  if (report.matched_rows !== 1) fail(`self-test matched_rows=${report.matched_rows}, want 1`);
  if (report.summary.token_exact_matches !== 1) fail(`self-test token_exact_matches=${report.summary.token_exact_matches}, want 1`);
  if (report.matches[0].deltas.total_tokens !== 0) fail("self-test total token delta mismatch");
  console.log("reconcile-codebuddy-official-usage self-test ok");
}

function fail(message) {
  console.error(message);
  process.exit(1);
}
