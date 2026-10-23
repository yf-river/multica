// 只准备查询，不执行读写；使用运行产物随附的 sqlc 查询核对数据库契约。
import fs from 'node:fs';
import pg from 'pg';

const queries = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
if (!queries.length || !process.env.DATABASE_URL) throw Error('缺少数据库契约或连接配置');
const client = new pg.Client({ connectionString: process.env.DATABASE_URL });
await client.connect();
try {
  await client.query('BEGIN READ ONLY');
  await client.query("SET LOCAL statement_timeout='15s'");
  const failures = [];
  // 快照初始化与逐条历史迁移使用不同版本集合，配置必须与真实台账一致。
  const baseline = await client.query("SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version='000_initial_schema') AS present");
  const configuredBaseline = process.env.MULTICA_MIGRATION_BASELINE?.trim().toLowerCase() === 'true';
  if (baseline.rows[0].present !== configuredBaseline) {
    failures.push({ query: 'schema_migrations.baseline', message: 'MULTICA_MIGRATION_BASELINE 与数据库 000_initial_schema 台账不一致' });
  }
  // 事件写入依赖数据库生成序号；PREPARE 不会验证 INSERT 时的默认值。
  const identity = await client.query("SELECT attidentity FROM pg_attribute WHERE attrelid=to_regclass('public.domain_event_outbox') AND attname='sequence_no'");
  if (identity.rows[0]?.attidentity !== 'a') failures.push({ query: 'domain_event_outbox.sequence_no', message: '缺少 GENERATED ALWAYS AS IDENTITY' });
  const invalid = await client.query("SELECT indexrelid::regclass::text AS name FROM pg_index WHERE NOT indisvalid OR NOT indisready");
  for (const index of invalid.rows) failures.push({ query: index.name, message: '索引未完成或无效' });
  for (const [index, query] of queries.entries()) {
    await client.query('SAVEPOINT contract_check');
    try {
      await client.query(`PREPARE release_check_${index} AS ${query.sql}`);
    } catch (error) {
      failures.push({ query: query.name, code: error.code, message: error.message });
      await client.query('ROLLBACK TO SAVEPOINT contract_check');
    }
    await client.query('RELEASE SAVEPOINT contract_check');
  }
  console.log(JSON.stringify({ checked: queries.length, failures }));
  if (failures.length) process.exitCode = 1;
} finally {
  await client.query('ROLLBACK');
  await client.end();
}
