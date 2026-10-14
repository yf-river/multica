// 数据库快照与技能对象共同备份；与服务端回收器互斥，避免复制期间删除快照所引用的对象。
import fs from 'node:fs';
import path from 'node:path';
import crypto from 'node:crypto';
import { spawnSync } from 'node:child_process';
import pg from 'pg';

const root = path.resolve(process.env.MULTICA_RELEASE_ROOT || path.join(import.meta.dirname, '..'));
const environment = process.argv[2];
if (!['int','prod'].includes(environment)) throw Error('指定 int 或 prod');
const config = JSON.parse(fs.readFileSync(path.join(root, '.run/env', `${environment}-services.json`))).server;
if (config.S3_BUCKET) throw Error('该备份入口用于本地/NAS 目录；S3 部署需使用桶版本化备份');
const client = new pg.Client({connectionString:config.DATABASE_URL});
await client.connect();
const destination = path.join(root,'.run/backups',`${environment}-assets-${Date.now()}`);
const uploads = path.resolve(config.LOCAL_UPLOAD_DIR);
try {
 await client.query('SELECT pg_advisory_lock(5145131)');
 fs.mkdirSync(destination,{recursive:true,mode:0o700});
 const database = new URL(config.DATABASE_URL);
 const dump = fs.openSync(path.join(destination,'database.dump'),'wx',0o600);
 let command='pg_dump',args=['--format=custom','--host',database.hostname,'--port',database.port||'5432','--username',decodeURIComponent(database.username),database.pathname.slice(1)];
 if (spawnSync('pg_dump',['--version']).error?.code==='ENOENT') {
  const ids=spawnSync('docker',['ps','-q'],{encoding:'utf8'}).stdout.trim().split('\n');
  const containers=JSON.parse(spawnSync('docker',['inspect',...ids],{encoding:'utf8'}).stdout);
  const matches=containers.filter(c=>['localhost','127.0.0.1'].includes(database.hostname)&&(c.NetworkSettings.Ports['5432/tcp']||[]).some(b=>b.HostPort===(database.port||'5432')&&['127.0.0.1','0.0.0.0',''].includes(b.HostIp)));
  if(matches.length!==1)throw Error('无法唯一识别 PostgreSQL 容器');
  command='docker';args=['exec','-e','PGPASSWORD',matches[0].Id,'pg_dump','--format=custom','--host','127.0.0.1','--username',decodeURIComponent(database.username),database.pathname.slice(1)];
 }
 const result = spawnSync(command,args,{env:{...process.env,PGPASSWORD:decodeURIComponent(database.password)},stdio:['ignore',dump,'pipe']});
 fs.closeSync(dump);
 if (result.status !== 0) throw Error(`pg_dump failed: ${result.error?.message || result.stderr?.toString()}`);
 // 首次升级前技能仍全部在数据库，快照中没有附件台账。
 const hasAssets = (await client.query("SELECT to_regclass('public.skill_asset') IS NOT NULL AS present")).rows[0].present;
 const rows = hasAssets ? (await client.query("SELECT storage_key FROM skill_asset ORDER BY storage_key")).rows : [];
 const manifest=[];
 for (const {storage_key:key} of rows) {
  if (!key.startsWith('private/skills/') || key.includes('..')) throw Error('非法存储键');
  const from=path.join(uploads,key),to=path.join(destination,'objects',key);
  // 上传意图可能尚未产生对象，数据库有引用的对象则必须存在。
  if (!fs.existsSync(from)) {
   if ((await client.query('SELECT 1 FROM skill_file WHERE storage_key=$1 LIMIT 1',[key])).rowCount) throw Error(`缺失附件: ${key}`);
   continue;
  }
  fs.mkdirSync(path.dirname(to),{recursive:true});fs.copyFileSync(from,to);
  manifest.push({key,sha256:crypto.createHash('sha256').update(fs.readFileSync(to)).digest('hex'),size:fs.statSync(to).size});
 }
 fs.writeFileSync(path.join(destination,'manifest.json'),JSON.stringify({environment,created_at:new Date().toISOString(),objects:manifest},null,2),{mode:0o600});
 console.log(JSON.stringify({backup:destination,objects:manifest.length}));
} finally {await client.end();}
