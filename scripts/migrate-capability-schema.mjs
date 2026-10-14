// 快照初始化环境的能力库增量升级；只执行列出的迁移，登记原始迁移版本。
import fs from 'node:fs';
import path from 'node:path';
import pg from 'pg';
const env=process.argv[2];
if(!['int','prod'].includes(env))throw Error('Specify int or prod');
const root=process.env.MULTICA_RELEASE_ROOT ?? process.cwd();
const config=JSON.parse(fs.readFileSync(path.join(root,'.run/env',`${env}-services.json`))).server;
const client=new pg.Client({connectionString:config.DATABASE_URL});
await client.connect();
try {
 await client.query("SET lock_timeout='10s'");
 await client.query("SET statement_timeout='120s'");
 await client.query('SELECT pg_advisory_lock($1::bigint)',['7244554146635925501']);
 const versions=new Set((await client.query('SELECT version FROM schema_migrations')).rows.map(r=>r.version));
 for(const version of ['513_skill_assets','514_skill_asset_key','515_workspace_mcp_checks','516_workspace_mcp_check_unique']) {
  if(versions.has(version))continue;
  const sql=fs.readFileSync(new URL(`../server/migrations/${version}.up.sql`,import.meta.url),'utf8');
  if(/CREATE UNIQUE INDEX CONCURRENTLY/i.test(sql)) {
   await client.query(sql);
   await client.query('INSERT INTO schema_migrations(version) VALUES ($1)',[version]);
  } else {
   await client.query('BEGIN');
   try {await client.query(sql);await client.query('INSERT INTO schema_migrations(version) VALUES ($1)',[version]);await client.query('COMMIT');}
   catch(error){await client.query('ROLLBACK');throw error;}
  }
  console.log(`${env}: applied ${version}`);
 }
 const indexes=(await client.query("SELECT c.relname,i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relname IN ('skill_asset_key','workspace_mcp_check_server_runtime')")).rows;
 if(indexes.length!==2||indexes.some(i=>!i.indisvalid))throw Error('Required index missing or invalid');
 console.log(JSON.stringify({environment:env,indexes}));
} finally {await client.end();}
