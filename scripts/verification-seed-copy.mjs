// 将联调种子的数据装入当前版本正常创建的验收库；迁移记录由当前建库流程生成。
import fs from 'node:fs';
import path from 'node:path';
import pg from 'pg';
const [environment, staging, postgresConfig] = process.argv.slice(2);
if (!['verify-1', 'verify-2', 'verify-3'].includes(environment) || !/^verification_seed_import_[a-f0-9]{16}$/.test(staging || '')) throw Error('未知验收环境或种子副本');
const root = process.env.MULTICA_RELEASE_ROOT;
if (!root) throw Error('缺少固定控制目录');
const cfg = JSON.parse(fs.readFileSync(path.join(root, '.run/env', environment+'-services.json')));
const record = JSON.parse(fs.readFileSync(postgresConfig));
const secret = fs.readFileSync(record.credentials_file, 'utf8').trim();
if (!secret.startsWith('POSTGRES_PASSWORD=')) throw Error('验收实例管理凭据格式错误');
const url = new URL(cfg.server.DATABASE_URL);
if (cfg.server.MULTICA_ENV_ID !== environment || url.pathname !== '/multica_'+environment.replace('-', '_') || url.username !== environment.replace('-', '_')) throw Error('槽位身份不匹配');
const target = new pg.Client({host:url.hostname,port:Number(url.port),database:url.pathname.slice(1),user:'postgres',password:secret.slice('POSTGRES_PASSWORD='.length)});
const source = new pg.Client({host:url.hostname,port:Number(url.port),database:staging,user:'postgres',password:secret.slice('POSTGRES_PASSWORD='.length)});
const quote = value => '"'+value.replaceAll('"','""')+'"';
const result = [];
await source.connect();await target.connect();
try {
  await source.query('BEGIN READ ONLY');
  await target.query('BEGIN');
  const columns = async client => (await client.query("SELECT table_name,column_name,data_type FROM information_schema.columns WHERE table_schema='public' AND is_generated='NEVER' ORDER BY table_name,ordinal_position")).rows;
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
  // 验收种子只带业务样本，不携带联调环境的认证、外部集成或租约。
  // 这些表即使源库为空也按存在性执行，保证以后新增凭据表时不会把旧凭据带入验收环境。
  const scrub = async (table, statement) => {
    if ((await target.query('SELECT to_regclass($1) AS name', ['public.'+table])).rows[0].name) {
      await target.query(statement);
    }
  };
  for (const table of [
    'personal_access_token', 'daemon_token', 'task_token', 'channel_binding_token',
    'lark_binding_token', 'external_credential_profile', 'vcs_connection',
    'daemon_connection', 'sys_cron_executions', 'channel_inbound_message_dedup',
    'lark_inbound_message_dedup'
  ]) await scrub(table, `DELETE FROM public.${quote(table)}`);
  await scrub('autopilot_trigger', 'UPDATE public."autopilot_trigger" SET signing_secret=NULL, webhook_token=NULL');
  await scrub('channel_installation', 'UPDATE public."channel_installation" SET ws_lease_token=NULL');
  await scrub('plugin_installation', 'UPDATE public."plugin_installation" SET token_hash=NULL');
  await scrub('issue_delivery', 'UPDATE public."issue_delivery" SET worker_token=NULL');
  await scrub('webhook_delivery', 'UPDATE public."webhook_delivery" SET lease_token=NULL');
  await scrub('life_cognition_job', 'UPDATE public."life_cognition_job" SET claim_token=NULL');
  await scrub('issue_source_context_object_intent', 'UPDATE public."issue_source_context_object_intent" SET lease_token=NULL');
  // lark_installation 的应用密钥为非空字段，直接丢弃安装记录比写入占位密钥安全。
  await scrub('lark_installation', 'DELETE FROM public."lark_installation"');
  // vcs_connection 的两个密文字段为非空约束，记录已在上面删除；历史 webhook/租约可以清空。
  await target.query('SET LOCAL session_replication_role=origin');
  const constraints = (await target.query(`SELECT c.conname, c.conrelid::regclass::text AS child, c.confrelid::regclass::text AS parent, c.confmatchtype AS match,
    ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(num,pos) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.num ORDER BY k.pos) AS child_keys,
    ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(num,pos) JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=k.num ORDER BY k.pos) AS parent_keys
    FROM pg_constraint c JOIN pg_namespace n ON n.oid=c.connamespace WHERE c.contype='f' AND n.nspname='public'`)).rows;
  for (const fk of constraints) {
    const present = fk.child_keys.map(k=>'c.'+quote(k)+' IS NOT NULL').join(' AND ');
    const matches = fk.child_keys.map((k,n)=>'c.'+quote(k)+'=p.'+quote(fk.parent_keys[n])).join(' AND ');
    const partial = fk.match==='f' ? ' OR (('+fk.child_keys.map(k=>'c.'+quote(k)+' IS NULL').join(' OR ')+') AND ('+fk.child_keys.map(k=>'c.'+quote(k)+' IS NOT NULL').join(' OR ')+'))' : '';
    const invalid = (await target.query(`SELECT EXISTS(SELECT 1 FROM ${fk.child} c WHERE ((${present}) AND NOT EXISTS(SELECT 1 FROM ${fk.parent} p WHERE ${matches})) ${partial}) AS invalid`)).rows[0].invalid;
    if (invalid) throw Error('种子关联校验失败: '+fk.conname);
  }
  await target.query('COMMIT');
  console.log(JSON.stringify({environment,status:'copied',tables:result}));
} catch (error) {
  await target.query('ROLLBACK');throw error;
} finally {await source.query('ROLLBACK');await source.end();await target.end();}
