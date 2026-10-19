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
