import fs from "node:fs";
import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import pg from "pg";

// 固定 int 中以事务验证，所有夹具和迁移在退出前回滚。
const config = JSON.parse(fs.readFileSync("/data/ida/multica/.run/env/int-services.json", "utf8"));
assert.equal(config.server.MULTICA_ENV_ID, "int");
const client = new pg.Client({ connectionString: config.server.DATABASE_URL });
await client.connect();
try {
  await client.query("BEGIN");
  await client.query("SET LOCAL lock_timeout='3s'");
  const exists = await client.query("SELECT to_regclass('supervision_config') AS table_name");
  if (!exists.rows[0].table_name) {
    await client.query(fs.readFileSync("server/migrations/526_quality_supervision.up.sql", "utf8"));
    for (const name of fs.readdirSync("server/migrations").filter((x) => /^5(27|28|29|30|31)_.*\.up\.sql$/.test(x)).sort()) {
      // 事务夹具中的索引无需发布迁移的并发创建方式；真实迁移仍执行原文件。
      await client.query(fs.readFileSync(`server/migrations/${name}`, "utf8").replace(" CONCURRENTLY", ""));
    }
  }
  const { rows: [member] } = await client.query("SELECT workspace_id,user_id FROM member ORDER BY created_at LIMIT 1");
  assert(member, "int 需要至少一个成员夹具");
  const ws = member.workspace_id;
  await client.query("INSERT INTO supervision_config(workspace_id,owner_id) VALUES($1,$2) ON CONFLICT(workspace_id) DO NOTHING", [ws, member.user_id]);
  const ids = Array.from({ length: 100 }, () => randomUUID());
  for (const [i, id] of ids.entries()) {
    await client.query("INSERT INTO issue(id,workspace_id,creator_type,creator_id,title,number) VALUES($1,$2,'member',$3,$4,$5)", [id, ws, member.user_id, `监督事务验收 ${i}`, -10000 - i]);
  }
  const subjects = await client.query("SELECT count(*)::int n FROM supervision_subject WHERE workspace_id=$1 AND issue_id=ANY($2::uuid[])", [ws, ids]);
  assert.equal(subjects.rows[0].n, 100);
  const issue = ids[0];
  const before = (await client.query("SELECT desired_version FROM supervision_subject WHERE issue_id=$1", [issue])).rows[0].desired_version;
  const comment = randomUUID();
  await client.query("INSERT INTO comment(id,workspace_id,issue_id,author_type,author_id,content) VALUES($1,$2,$3,'member',$4,$5)", [comment, ws, issue, member.user_id, "/note 这里感觉开发不完整，用起来费力，请核验。"]);
  assert.equal(Number((await client.query("SELECT desired_version FROM supervision_subject WHERE issue_id=$1", [issue])).rows[0].desired_version), Number(before) + 1);
  assert.equal((await client.query("SELECT count(*)::int n FROM supervision_material WHERE issue_id=$1 AND kind='comment' AND source_id=$2", [issue, comment])).rows[0].n, 1);
  await client.query("UPDATE comment SET content=content||' 补充备注。' WHERE id=$1", [comment]);
  assert.equal(Number((await client.query("SELECT generation FROM supervision_material WHERE source_id=$1 AND kind='comment'", [comment])).rows[0].generation), 2);
  const { rows: [agent] } = await client.query("SELECT id,runtime_id FROM agent WHERE workspace_id=$1 AND runtime_id IS NOT NULL LIMIT 1", [ws]);
  assert(agent, "int 需要已绑定运行时的 Agent");
  const task = randomUUID();
  await client.query("INSERT INTO agent_task_queue(id,agent_id,runtime_id,issue_id,status) VALUES($1,$2,$3,$4,'queued')", [task, agent.id, agent.runtime_id, issue]);
  for (const seq of [20, 19]) await client.query("INSERT INTO task_message(id,task_id,seq,type,content) VALUES($1,$2,$3,'text',$4)", [randomUUID(), task, seq, "测试消息 " + seq]);
  assert.equal((await client.query("SELECT count(*)::int n FROM supervision_material WHERE issue_id=$1 AND kind='message'", [issue])).rows[0].n, 2);
  const batch = randomUUID();
  const target = Number((await client.query("SELECT desired_version FROM supervision_subject WHERE issue_id=$1", [issue])).rows[0].desired_version);
  await client.query("INSERT INTO supervision_batch(id,workspace_id,issue_id,target_version) VALUES($1,$2,$3,$4)", [batch, ws, issue, target]);
  await client.query("SAVEPOINT duplicate_batch");
  let duplicateRejected = false;
  try { await client.query("INSERT INTO supervision_batch(workspace_id,issue_id,target_version) VALUES($1,$2,$3)", [ws, ids[1], target]); }
  catch (error) { duplicateRejected = error.code === "23505"; await client.query("ROLLBACK TO SAVEPOINT duplicate_batch"); }
  assert(duplicateRejected, "同工作区不得有两个在途批次");
  await client.query("UPDATE issue SET description='审查期间新增要求' WHERE id=$1", [issue]);
  await client.query("UPDATE supervision_subject SET reviewed_version=$2 WHERE issue_id=$1", [issue, target]);
  const versions = (await client.query("SELECT desired_version,reviewed_version FROM supervision_subject WHERE issue_id=$1", [issue])).rows[0];
  assert(Number(versions.desired_version) > Number(versions.reviewed_version));
  await client.query("DELETE FROM comment WHERE id=$1", [comment]);
  assert.equal((await client.query("SELECT count(*)::int n FROM supervision_material WHERE source_id=$1 AND kind='comment'", [comment])).rows[0].n, 0);
  let prepared = 0;
  for (const file of ["server/internal/supervision/supervision.go", "server/internal/handler/supervision.go"]) {
    for (const match of fs.readFileSync(file, "utf8").matchAll(/`([\s\S]*?)`/g)) {
      const sql = match[1];
      if (!/^\s*(SELECT|INSERT|UPDATE|DELETE|WITH)\b/i.test(sql) || sql.endsWith("(")) continue;
      await client.query(`PREPARE supervision_contract_${++prepared} AS ${sql}`);
    }
  }
  console.log(JSON.stringify({ environment: "int", passed: true, prepared, checks: ["100 task inventory", "supervisor note registration", "comment edits", "out-of-order message IDs", "single active batch", "changes during review", "deleted source invalidation", "all static supervision SQL contracts"], rollback: true }));
} finally {
  await client.query("ROLLBACK");
  await client.end();
}
