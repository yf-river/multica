#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import pg from "pg";

const args = parseArgs(process.argv.slice(2));
const apply = Boolean(args.apply);
const envName = String(args.env || "int");
const issueId = args["issue-id"] ? String(args["issue-id"]) : "";
const all = Boolean(args.all);
const includeSingletons = Boolean(args["include-singletons"]);

if (!issueId && !all) {
  fail("Pass --issue-id=<uuid> for a focused repair or --all for the whole database.");
}

if (includeSingletons && !apply) {
  console.warn("Including single-row sessions in dry-run. This mode is intended for one-time legacy repair only.");
}

const databaseURL = args["database-url"] ? String(args["database-url"]) : readDatabaseURL(envName);
if (!databaseURL) fail(`DATABASE_URL not found for env ${envName}.`);

const client = new pg.Client({ connectionString: databaseURL });
await client.connect();
try {
  const rows = await loadCodebuddyRows(client, issueId, all);
  const repairs = computeRepairs(rows, { includeSingletons });
  const changed = repairs.filter((item) => item.changed);
  const skippedGroups = [...new Set(repairs.filter((item) => item.skipped).map((item) => item.group_key))];
  const before = sumRows(rows);
  const after = sumRepairs(repairs);

  if (apply && changed.length > 0) {
    await applyRepairs(client, changed);
    await rollupUsage(client, changed);
  }

  console.log(JSON.stringify({
    schema: "multica.codebuddy_usage_repair.v1",
    env: envName,
    issue_id: issueId || null,
    mode: apply ? "apply" : "dry-run",
    include_singletons: includeSingletons,
    scanned_rows: rows.length,
    changed_rows: changed.length,
    skipped_groups: skippedGroups.length,
    skipped_group_keys: skippedGroups.slice(0, 20),
    before,
    after,
    delta: diffTotals(before, after),
    top_changes: changed
      .slice()
      .sort((a, b) => Number((totalOf(b.before) - totalOf(b.after)) - (totalOf(a.before) - totalOf(a.after))))
      .slice(0, 20)
      .map((item) => ({
        task_id: item.task_id,
        issue_id: item.issue_id,
        session_id: item.session_id,
        model: item.model,
        before: stringifyTotals(item.before),
        after: stringifyTotals(item.after),
      })),
  }, null, 2));
} finally {
  await client.end();
}

function parseArgs(rawArgs) {
  const result = {};
  for (const raw of rawArgs) {
    if (raw === "--apply") {
      result.apply = true;
      continue;
    }
    if (raw === "--all") {
      result.all = true;
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

async function loadCodebuddyRows(client, issueId, all) {
  const params = [];
  const where = ["tu.provider = 'codebuddy'", "atq.session_id IS NOT NULL", "atq.session_id <> ''"];
  if (!all) {
    params.push(issueId);
    where.push(`atq.issue_id IN (
      WITH RECURSIVE tree AS (
        SELECT id FROM issue WHERE id = $1
        UNION ALL
        SELECT i.id FROM issue i JOIN tree t ON i.parent_issue_id = t.id
      )
      SELECT id FROM tree
    )`);
  }
  const res = await client.query(`
    SELECT
      tu.id::text usage_id,
      tu.task_id::text task_id,
      atq.issue_id::text issue_id,
      atq.session_id,
      tu.provider,
      tu.model,
      tu.input_tokens::bigint input_tokens,
      tu.output_tokens::bigint output_tokens,
      tu.cache_read_tokens::bigint cache_read_tokens,
      tu.cache_write_tokens::bigint cache_write_tokens,
      atq.created_at,
      COALESCE(atq.started_at, atq.created_at) order_at
    FROM task_usage tu
    JOIN agent_task_queue atq ON atq.id = tu.task_id
    WHERE ${where.join(" AND ")}
    ORDER BY atq.session_id, tu.model, COALESCE(atq.started_at, atq.created_at), atq.created_at, atq.id
  `, params);
  return res.rows.map((row) => ({
    ...row,
    input_tokens: BigInt(row.input_tokens),
    output_tokens: BigInt(row.output_tokens),
    cache_read_tokens: BigInt(row.cache_read_tokens),
    cache_write_tokens: BigInt(row.cache_write_tokens),
  }));
}

function computeRepairs(rows, options = {}) {
  const includeSingletons = Boolean(options.includeSingletons);
  const groups = new Map();
  for (const row of rows) {
    const key = `${row.session_id}\u0000${row.provider}\u0000${row.model}`;
    if (!groups.has(key)) groups.set(key, []);
    groups.get(key).push(row);
  }

  const repairs = [];
  for (const [groupKey, groupRows] of groups) {
    const monotonic = isMonotonicCumulative(groupRows);
    let previous = zeroTotals();
    for (const row of groupRows) {
      const current = rowTotals(row);
      if (groupRows.length === 1 && !includeSingletons) {
        repairs.push(repairForSkippedRow(row, groupKey));
        continue;
      }
      const rawDelta = groupRows.length > 1 && monotonic
        ? subtractTotals(current, previous)
        : current;
      previous = current;
      const after = normalizeCodebuddyTotals(rawDelta);
      repairs.push({
        group_key: groupKey,
        usage_id: row.usage_id,
        task_id: row.task_id,
        issue_id: row.issue_id,
        session_id: row.session_id,
        provider: row.provider,
        model: row.model,
        before: current,
        after,
        changed: !sameTotals(current, after),
        skipped: false,
      });
    }
  }
  return repairs;
}

function normalizeCodebuddyTotals(rawDelta) {
  let input = rawDelta.input;
  if (rawDelta.input < rawDelta.cache_read + rawDelta.cache_write) {
    input = rawDelta.input + rawDelta.cache_read + rawDelta.cache_write;
  }
  return {
    input,
    output: rawDelta.output,
    cache_read: rawDelta.cache_read,
    cache_write: 0n,
  };
}

function isMonotonicCumulative(rows) {
  let previous = zeroTotals();
  for (const row of rows) {
    const current = rowTotals(row);
    if (current.input < previous.input || current.output < previous.output || current.cache_read < previous.cache_read || current.cache_write < previous.cache_write) {
      return false;
    }
    previous = current;
  }
  return true;
}

function repairForSkippedRow(row, groupKey) {
  const totals = rowTotals(row);
  return {
    group_key: groupKey,
    usage_id: row.usage_id,
    task_id: row.task_id,
    issue_id: row.issue_id,
    session_id: row.session_id,
    provider: row.provider,
    model: row.model,
    before: totals,
    after: totals,
    changed: false,
    skipped: true,
  };
}

async function applyRepairs(client, repairs) {
  await client.query("BEGIN");
  try {
    for (const item of repairs) {
      await client.query(`
        UPDATE task_usage
        SET input_tokens = $2,
            output_tokens = $3,
            cache_read_tokens = $4,
            cache_write_tokens = $5,
            updated_at = now()
        WHERE id = $1
      `, [item.usage_id, item.after.input.toString(), item.after.output.toString(), item.after.cache_read.toString(), item.after.cache_write.toString()]);
      await client.query(`
        UPDATE task_trace_event
        SET input_tokens = $2,
            output_tokens = $3,
            cache_read_tokens = $4,
            cache_write_tokens = $5
        WHERE task_id = $1
          AND event_type = 'llm.usage_reported'
          AND provider = 'codebuddy'
          AND model = $6
      `, [item.task_id, item.after.input.toString(), item.after.output.toString(), item.after.cache_read.toString(), item.after.cache_write.toString(), item.model]);
    }
    await client.query("COMMIT");
  } catch (error) {
    await client.query("ROLLBACK");
    throw error;
  }
}

async function rollupUsage(client, repairs) {
  const taskIds = repairs.map((item) => item.task_id);
  if (taskIds.length === 0) return;
  const res = await client.query(`
    SELECT min(tu.created_at) - interval '1 hour' AS start_at,
           max(tu.created_at) + interval '1 hour' AS end_at
    FROM task_usage tu
    WHERE tu.task_id = ANY($1::uuid[])
  `, [taskIds]);
  const row = res.rows[0];
  if (row?.start_at && row?.end_at) {
    await client.query(`SELECT rollup_task_usage_hourly_window($1::timestamptz, $2::timestamptz)`, [row.start_at, row.end_at]);
  }
}

function rowTotals(row) {
  return {
    input: row.input_tokens,
    output: row.output_tokens,
    cache_read: row.cache_read_tokens,
    cache_write: row.cache_write_tokens,
  };
}

function zeroTotals() {
  return { input: 0n, output: 0n, cache_read: 0n, cache_write: 0n };
}

function subtractTotals(left, right) {
  return {
    input: maxBigInt(left.input - right.input, 0n),
    output: maxBigInt(left.output - right.output, 0n),
    cache_read: maxBigInt(left.cache_read - right.cache_read, 0n),
    cache_write: maxBigInt(left.cache_write - right.cache_write, 0n),
  };
}

function sameTotals(left, right) {
  return left.input === right.input &&
    left.output === right.output &&
    left.cache_read === right.cache_read &&
    left.cache_write === right.cache_write;
}

function sumRows(rows) {
  return stringifyTotals(rows.reduce((acc, row) => addTotals(acc, rowTotals(row)), zeroTotals()));
}

function sumRepairs(repairs) {
  return stringifyTotals(repairs.reduce((acc, item) => addTotals(acc, item.after), zeroTotals()));
}

function addTotals(left, right) {
  return {
    input: left.input + right.input,
    output: left.output + right.output,
    cache_read: left.cache_read + right.cache_read,
    cache_write: left.cache_write + right.cache_write,
  };
}

function diffTotals(before, after) {
  return {
    input: String(BigInt(after.input) - BigInt(before.input)),
    output: String(BigInt(after.output) - BigInt(before.output)),
    cache_read: String(BigInt(after.cache_read) - BigInt(before.cache_read)),
    cache_write: String(BigInt(after.cache_write) - BigInt(before.cache_write)),
    total: String(BigInt(after.total) - BigInt(before.total)),
    billable_total: String(BigInt(after.billable_total) - BigInt(before.billable_total)),
  };
}

function stringifyTotals(totals) {
  const total = totals.input + totals.output + totals.cache_read + totals.cache_write;
  const billableTotal = totals.input + totals.output;
  return {
    input: totals.input.toString(),
    output: totals.output.toString(),
    cache_read: totals.cache_read.toString(),
    cache_write: totals.cache_write.toString(),
    total: total.toString(),
    billable_total: billableTotal.toString(),
  };
}

function totalOf(totals) {
  return totals.input + totals.output + totals.cache_read + totals.cache_write;
}

function maxBigInt(left, right) {
  return left > right ? left : right;
}

function fail(message) {
  console.error(message);
  process.exit(1);
}
