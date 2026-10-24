// 固定测试环境的存量模型维护；仅修改模型选择，不改变 Agent 权限或执行状态。
import fs from 'node:fs';
import path from 'node:path';
import pg from 'pg';

const [environment, mode = 'audit'] = process.argv.slice(2);
if (!['int', 'verify-1', 'verify-2', 'verify-3'].includes(environment) || !['audit', 'apply'].includes(mode)) throw Error('仅接受固定测试环境及 audit/apply');
const root = process.env.MULTICA_RELEASE_ROOT;
if (!root) throw Error('缺少控制目录 MULTICA_RELEASE_ROOT');
const read = file => JSON.parse(fs.readFileSync(path.join(root, '.run', file), 'utf8'));
const cfg = read('env/'+environment+'-services.json');
const prod = read('env/prod-services.json');
const meta = read('deployments/goal-test-'+environment+'.json');
const target = new URL(cfg.server.DATABASE_URL);
const production = new URL(prod.server.DATABASE_URL);
const identity = u => [u.hostname,u.port,u.pathname].join('|');
if (cfg.server.MULTICA_ENV_ID !== environment || identity(target) === identity(production)) throw Error('测试数据库身份不符或与生产重叠');
const port = meta.daemon_profile ? 19515 + [...Buffer.from(meta.daemon_profile)].reduce((a,b)=>a+b,0)%1000 : 19514;
const daemon = 'http://127.0.0.1:'+port;
const request = async (base, route, method='GET') => {
 const r = await fetch(base+route, {method, signal:AbortSignal.timeout(10000)});
 if (!r.ok) throw Error('环境核对失败 '+route+' HTTP '+r.status);
 return r.json();
};
const backend = await request(meta.backend_url,'/health');
const config = await request(meta.backend_url,'/api/config');
if (config.environment_id !== environment || backend.commit !== meta.commit) throw Error('运行环境与部署记录不符');
const health = await request(daemon,'/health');
let wasDraining = true;
const client = new pg.Client({connectionString:cfg.server.DATABASE_URL});
await client.connect();
try {
 if (mode === 'apply') {
  const before = await request(daemon,'/drain');
  if (before.active_tasks || before.claims_in_flight) throw Error('测试环境仍有运行，保留现场，空闲后再维护');
  wasDraining = before.draining;
  await request(daemon,'/drain','POST');
 }
 await client.query('BEGIN');
 await client.query("SET LOCAL lock_timeout='5s'");
 await client.query("SET LOCAL statement_timeout='15s'");
 // 阻止新的 Agent 变更与任务入队越过空闲检查；运行中的任务不被取消或更新。
 if (mode === 'apply') await client.query('LOCK TABLE agent_task_queue, agent IN SHARE ROW EXCLUSIVE MODE');
 const busy = Number((await client.query("SELECT count(*) FROM agent_task_queue WHERE status IN ('queued','dispatched','running')")).rows[0].count);
 if (mode === 'apply' && busy) throw Error('测试环境仍有待执行任务，保留现场，空闲后再维护');
 const before = (await client.query('SELECT id,workspace_id,runtime_id,model,thinking_level,service_tier,status,archived_at FROM agent ORDER BY id')).rows;
 const runtimes = (await client.query("SELECT id,workspace_id FROM agent_runtime WHERE provider='codebuddy' AND status='online' AND daemon_id=$1 AND profile_id IS NULL",[health.daemon_id])).rows;
 const bindings = new Map();
 for (const row of runtimes) {
  if (bindings.has(row.workspace_id)) throw Error('工作区 CodeBuddy runtime 不唯一');
  bindings.set(row.workspace_id,row.id);
 }
 if (mode === 'apply') {
  for (const row of before) {
   const runtime = bindings.get(row.workspace_id);
   if (!runtime) throw Error('工作区缺少本环境在线 CodeBuddy runtime，停止维护');
   await client.query("UPDATE agent SET runtime_id=$2,model='deepseek-v4.1-flash',thinking_level='',service_tier='',updated_at=now() WHERE id=$1 AND (runtime_id IS DISTINCT FROM $2::uuid OR model IS DISTINCT FROM 'deepseek-v4.1-flash' OR thinking_level IS DISTINCT FROM '' OR service_tier IS DISTINCT FROM '')",[row.id,runtime]);
  }
 }
 const after = (await client.query('SELECT id,workspace_id,runtime_id,model,thinking_level,service_tier,status,archived_at FROM agent ORDER BY id')).rows;
 await client.query('COMMIT');
 console.log(JSON.stringify({environment,mode,agents:before.length,busy,before,after}));
} catch(e) {
 await client.query('ROLLBACK');throw e;
} finally {
 await client.end();
 if (mode === 'apply' && !wasDraining) await request(daemon,'/drain','DELETE');
}
