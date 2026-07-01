#!/usr/bin/env node
import pg from "pg";

const CONFIRM = "DELETE_AGENT_SQUAD";
const args = new Set(process.argv.slice(2));
const execute = args.has("--execute");
const confirmValue = process.argv.find((arg) => arg.startsWith("--confirm="))?.slice("--confirm=".length);
const databaseURL = process.env.DATABASE_URL || process.env.GOAL_TEST_DATABASE_URL || "";

if (!databaseURL) {
  console.error("DATABASE_URL or GOAL_TEST_DATABASE_URL is required");
  process.exit(1);
}
if (execute && confirmValue !== CONFIRM) {
  console.error(`Refusing to execute. Pass --confirm=${CONFIRM}`);
  process.exit(1);
}

const client = new pg.Client({ connectionString: databaseURL });
await client.connect();

try {
  await client.query("BEGIN");

  const before = await counts(client);
  console.log(JSON.stringify({ mode: execute ? "execute" : "dry-run", before }, null, 2));

  if (execute) {
    await client.query(`
      UPDATE issue
      SET assignee_type = NULL, assignee_id = NULL, updated_at = now()
      WHERE assignee_type IN ('agent', 'squad')
    `);
    await client.query(`
      DELETE FROM autopilot_run
      WHERE autopilot_id IN (
        SELECT id FROM autopilot WHERE assignee_type IN ('agent', 'squad')
      )
    `);
    await client.query(`DELETE FROM autopilot WHERE assignee_type IN ('agent', 'squad')`);
    await client.query(`DELETE FROM agent_task_queue`);
    await client.query(`DELETE FROM squad_member`);
    await client.query(`DELETE FROM squad`);
    await client.query(`DELETE FROM agent`);
  }

  const after = await counts(client);
  console.log(JSON.stringify({ mode: execute ? "execute" : "dry-run", after }, null, 2));

  if (execute) {
    await client.query("COMMIT");
  } else {
    await client.query("ROLLBACK");
    console.log(`Dry-run only. Re-run with --execute --confirm=${CONFIRM} to delete Agent/Squad data.`);
  }
} catch (err) {
  await client.query("ROLLBACK").catch(() => {});
  throw err;
} finally {
  await client.end();
}

async function counts(client) {
  const { rows } = await client.query(`
    SELECT
      (SELECT count(*)::int FROM agent) AS agents,
      (SELECT count(*)::int FROM squad) AS squads,
      (SELECT count(*)::int FROM squad_member) AS squad_members,
      (SELECT count(*)::int FROM agent_task_queue) AS agent_tasks,
      (SELECT count(*)::int FROM issue WHERE assignee_type IN ('agent', 'squad')) AS assigned_issues,
      (SELECT count(*)::int FROM autopilot WHERE assignee_type IN ('agent', 'squad')) AS autopilots
  `);
  return rows[0];
}
