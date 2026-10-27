// 将联调种子的数据装入当前版本正常创建的验收库；迁移记录由当前建库流程生成。
import fs from 'node:fs';
import path from 'node:path';
import pg from 'pg';
import { loadRules, uncoveredTables, staleExemptions } from './lib/verification-scrub.mjs';
const args = process.argv.slice(2);
const targetIndex = args.indexOf('--target-database');
const [environment, staging, postgresConfig] = args.slice(0, 3);
const scratch = targetIndex > 0 ? { database: args[targetIndex+1], host: args[targetIndex+2], port: Number(args[targetIndex+3]) } : null;
if (!['verify-1', 'verify-2', 'verify-3', 'build'].includes(environment) || !/^verification_seed_import_[a-f0-9]{16}$/.test(staging || '')) throw Error('未知验收环境或种子副本');
if ((environment === 'build') !== Boolean(scratch)) throw Error('构建库与验收槽位身份不匹配');
if (scratch && (!/^multica_seed_build_[a-z0-9_]+$/.test(scratch.database || '') || !scratch.host || !Number.isInteger(scratch.port) || scratch.port < 1)) throw Error('非法构建库目标');
const root = process.env.MULTICA_RELEASE_ROOT;
if (!root) throw Error('缺少固定控制目录');
const record = JSON.parse(fs.readFileSync(postgresConfig));
const secret = fs.readFileSync(record.credentials_file, 'utf8').trim();
if (!secret.startsWith('POSTGRES_PASSWORD=')) throw Error('验收实例管理凭据格式错误');
const password = secret.slice('POSTGRES_PASSWORD='.length);
// 1. 构建种子时目标是本轮新建的隔离库；槽位恢复时目标由槽位安装记录决定，两者都不接受任意连接。
const cfg = scratch ? null : JSON.parse(fs.readFileSync(path.join(root, '.run/env', environment+'-services.json')));
const url = scratch ? null : new URL(cfg.server.DATABASE_URL);
if (!scratch && (cfg.server.MULTICA_ENV_ID !== environment || url.pathname !== '/multica_'+environment.replace('-', '_') || url.username !== environment.replace('-', '_'))) throw Error('槽位身份不匹配');
const host = scratch ? scratch.host : url.hostname;
const port = scratch ? scratch.port : Number(url.port);
const target = new pg.Client({host,port,database:scratch ? scratch.database : url.pathname.slice(1),user:'postgres',password});
const source = new pg.Client({host,port,database:staging,user:'postgres',password});
const quote = value => '"'+value.replaceAll('"','""')+'"';
const result = [];
await source.connect();await target.connect();
try {
  await source.query('BEGIN READ ONLY');
  await target.query('BEGIN');
  // 只有基表可以 TRUNCATE/INSERT；视图同样出现在 information_schema.columns 里，
  // 按列直接取会把视图也算进搬运清单，TRUNCATE 时报 "is not a table"。
  const columns = async client => (await client.query("SELECT c.table_name,c.column_name,c.data_type FROM information_schema.columns c JOIN information_schema.tables t ON t.table_schema=c.table_schema AND t.table_name=c.table_name WHERE c.table_schema='public' AND c.is_generated='NEVER' AND t.table_type='BASE TABLE' ORDER BY c.table_name,c.ordinal_position")).rows;
  const from = await columns(source);const to = await columns(target);
  const sourceNames = new Set(from.map(x=>x.table_name));
  const tables = [...new Set(to.map(x=>x.table_name))].filter(x=>sourceNames.has(x) && x!=='schema_migrations' && !x.startsWith('verification_'));
  if (!tables.includes('workspace') || !tables.includes('issue')) throw Error('种子缺少工作区或任务');
  // 副本导入允许跨表顺序；提交前逐项检查既有外键，历史样本不触发业务投递。
  await target.query('SET LOCAL session_replication_role=replica');
  await target.query('TRUNCATE '+tables.map(t=>'public.'+quote(t)).join(','));
  for (const table of tables) {
    const available = new Set(from.filter(x=>x.table_name===table).map(x=>x.column_name));
    const selected = to.filter(x=>x.table_name===table && available.has(x.column_name));
    const names = selected.map(x=>quote(x.column_name)).join(',');
    const rows = (await source.query(`SELECT ${names} FROM public.${quote(table)}`)).rows;
    const placeholders = selected.map((_, n)=>'$'+(n+1)).join(',');
    for (const row of rows) {
      const values = selected.map(c=>(c.data_type==='json' || c.data_type==='jsonb') && row[c.column_name]!==null ? JSON.stringify(row[c.column_name]) : row[c.column_name]);
      await target.query(`INSERT INTO public.${quote(table)} (${names}) OVERRIDING SYSTEM VALUE VALUES (${placeholders})`,values);
    }
    for (const col of selected) {
      const seq = (await target.query('SELECT pg_get_serial_sequence($1,$2) AS name',['public.'+quote(table),col.column_name])).rows[0]?.name;
      if (seq) await target.query(`SELECT setval($1::regclass,COALESCE((SELECT max(${quote(col.column_name)}) FROM public.${quote(table)}),1),EXISTS(SELECT 1 FROM public.${quote(table)}))`,[seq]);
    }
    result.push({table,rows:rows.length});

  }
  // 2. 导入阶段需要关闭外键顺序检查；清理前恢复真实约束，删除被引用时立即失败而不是留下悬空记录。
  await target.query('SET LOCAL session_replication_role=origin');
  // 验收种子只带业务样本，不携带联调环境的认证、外部集成或租约。
  // 3. 清理规则集中在 scripts/verification-scrub.json：新增执行型表却没有规则或显式豁免时，
  // 恢复在这里直接失败并打印表名，不把新功能留下的自动执行状态带进下一个任务。
  const scrub = loadRules(path.join(import.meta.dirname, 'verification-scrub.json'));
  const present = new Set((await target.query("SELECT table_name FROM information_schema.tables WHERE table_schema='public' AND table_type='BASE TABLE'")).rows.map(x=>x.table_name));
  const applied = [];
  for (const rule of scrub.rules) {
    if (!present.has(rule.table)) {
      applied.push({table: rule.table, skipped: 'missing'});
      continue;
    }
    await target.query(rule.sql);
    applied.push({table: rule.table});
  }
  const uncovered = uncoveredTables([...present], scrub);
  if (uncovered.length) throw Error('清理清单未覆盖执行型表: '+uncovered.join(', ')+'；请在 scripts/verification-scrub.json 增加规则或豁免');
  const stale = staleExemptions([...present], scrub);
  const constraints = (await target.query(`SELECT c.conname, c.conrelid::regclass::text AS child, c.confrelid::regclass::text AS parent, c.confmatchtype AS match,
    ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(num,pos) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.num ORDER BY k.pos) AS child_keys,
    ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(num,pos) JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=k.num ORDER BY k.pos) AS parent_keys
    FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE c.contype='f' AND n.nspname='public'`)).rows;
  for (const fk of constraints) {
    const nonNull = fk.child_keys.map(k=>'c.'+quote(k)+' IS NOT NULL').join(' AND ');
    const matches = fk.child_keys.map((k,n)=>'c.'+quote(k)+'=p.'+quote(fk.parent_keys[n])).join(' AND ');
    const partial = fk.match==='f' ? ' OR (('+fk.child_keys.map(k=>'c.'+quote(k)+' IS NULL').join(' OR ')+') AND ('+fk.child_keys.map(k=>'c.'+quote(k)+' IS NOT NULL').join(' OR ')+'))' : '';
    const invalid = (await target.query(`SELECT EXISTS(SELECT 1 FROM ${fk.child} c WHERE ((${nonNull}) AND NOT EXISTS(SELECT 1 FROM ${fk.parent} p WHERE ${matches})) ${partial}) AS invalid`)).rows[0].invalid;
    if (invalid) throw Error('种子关联校验失败: '+fk.conname);
  }
  await target.query('COMMIT');
  console.log(JSON.stringify({environment,status:'copied',tables:result,scrub:applied,stale_exemptions:stale}));
} catch (error) {
  await target.query('ROLLBACK');throw error;
} finally {await source.query('ROLLBACK');await source.end();await target.end();}
