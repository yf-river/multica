// 固定测试环境的存量模型维护；仅修改模型选择，不改变 Agent 权限或执行状态。
import fs from 'node:fs';
import path from 'node:path';
import pg from 'pg';
import { createHash } from 'node:crypto';

const [environment, mode = 'audit', policyFile, scopeFile] = process.argv.slice(2);
const policyRoot = path.join(process.env.MULTICA_RELEASE_ROOT || '', '.run');
const cachedPolicy = path.join(policyRoot,'test-agent-policy-'+environment+'.json');
const desired = mode === 'target' ? null : JSON.parse(fs.readFileSync(policyFile || (fs.existsSync(cachedPolicy) ? cachedPolicy : path.join(policyRoot,'test-agent-policy.json')),'utf8'));
// 与 server/internal/testagents 一致：只校验形状，运行时是否支持该模型由运行时在派发时判定。
const policyValid = p => Boolean(p) && /^[a-z][a-z0-9-]*$/.test(p.runtime || p.provider || '') && /^[A-Za-z0-9._/-]+$/.test(p.model || '');
const valid = p => policyValid(p);
if (desired && (!valid(desired.policy) || Object.values(desired.overrides || {}).some(p => !valid(p)))) throw Error('测试模型配置不受支持');
if (!['int', 'verify-1', 'verify-2', 'verify-3'].includes(environment) || !['audit', 'apply', 'target'].includes(mode)) throw Error('仅接受固定测试环境及 audit/apply/target');
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
// 1. The targeted path shares the existing inventory and provider selection but never drains an environment.
if (mode === 'target') {
 await targeted(JSON.parse(fs.readFileSync(scopeFile,'utf8')));
 process.exit(0);
}
let wasDraining = true;
const client = new pg.Client({connectionString:cfg.server.DATABASE_URL});
await client.connect();
try {
 const catalogRuntimes = (await client.query("SELECT id,workspace_id,provider,runtime_mode FROM agent_runtime WHERE status='online' AND daemon_id=$1 AND profile_id IS NULL",[health.daemon_id])).rows;
 const requested = new Set([desired?.policy, ...Object.values(desired?.overrides || {})].filter(Boolean).map(p => p.runtime || p.provider));
 await publishCatalogs(mode === 'apply' ? catalogRuntimes.filter(r => requested.has(r.provider)) : catalogRuntimes);
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
 const busy = Number((await client.query("SELECT count(*) FROM agent_task_queue WHERE status NOT IN ('completed','failed','cancelled')")).rows[0].count);
 if (mode === 'apply' && busy) throw Error('测试环境仍有待执行任务，保留现场，空闲后再维护');
 const before = (await client.query('SELECT id,workspace_id,runtime_id,model,thinking_level,service_tier,status,archived_at,name,custom_args FROM agent ORDER BY id')).rows;
 const runtimes = (await client.query("SELECT id,workspace_id,provider,runtime_mode FROM agent_runtime WHERE status='online' AND daemon_id=$1 AND profile_id IS NULL",[health.daemon_id])).rows;
 for (const runtime of runtimes) {
  const source = catalogRuntimes.find(item => item.id === runtime.id);
  if (source?.models?.length) { runtime.models = source.models; runtime.catalog_observed_at = source.catalog_observed_at; }
 }
 const bindings = new Map(), ambiguous = new Set();
 for (const row of runtimes) {
  const key = row.workspace_id+'|'+row.provider;
  if (bindings.has(key)) ambiguous.add(key);
  bindings.set(key,row);
 }
 if (mode === 'apply') {
  for (const row of before) {
   const p = desired.overrides?.[row.id] || desired.policy;
   if ((row.custom_args || []).some(x => /^(?:-m|-c)|^--(?:model|config)(?:=|$)|fast_mode/.test(x))) throw Error('Agent 含覆盖模型的启动参数，请先核对：'+row.name);
   const key = row.workspace_id+'|'+(p.runtime || p.provider);
   if (ambiguous.has(key)) throw Error('工作区所选运行时不唯一');
   const runtime = bindings.get(key);
   if (!runtime) throw Error('工作区缺少所选的在线运行时，停止维护');
   if (!runtime.models?.length) throw Error('所选运行时模型目录暂不可用，停止维护');
   const model = runtime.models.find(item => item.id === p.model);
   if (!model) throw Error('所选模型不在本环境运行时目录中，停止维护');
   if (p.thinking_level && !model.thinking?.supported_levels?.some(level => level.value === p.thinking_level)) throw Error('所选思考强度不受本环境模型支持，停止维护');
   if (p.service_tier && !model.service_tiers?.some(tier => tier.id === p.service_tier) && !(p.service_tier === 'default' && model.supports_explicit_standard_service_tier)) throw Error('所选速度不受本环境模型支持，停止维护');
   await client.query("UPDATE agent SET runtime_id=$2,runtime_mode=$6,model=$3,thinking_level=$4,service_tier=$5,updated_at=now() WHERE id=$1 AND (runtime_id IS DISTINCT FROM $2::uuid OR model IS DISTINCT FROM $3 OR thinking_level IS DISTINCT FROM $4 OR service_tier IS DISTINCT FROM $5 OR runtime_mode IS DISTINCT FROM $6)",[row.id,runtime.id,p.model,p.thinking_level,p.service_tier,runtime.runtime_mode]);
  }
 }
 if (mode === 'apply' && (await client.query("SELECT to_regclass('public.test_agent_control') AS name")).rows[0].name) {
  for (const workspace of new Set((await client.query('SELECT id FROM workspace')).rows.map(w => w.id))) {
   await client.query(`INSERT INTO test_agent_control(workspace_id,name,desired,observed) VALUES($1,$2,$3,$4) ON CONFLICT(workspace_id,name) DO UPDATE SET desired=EXCLUDED.desired,observed=EXCLUDED.observed,updated_at=now()`,[workspace,environment,desired,{daemon_id:health.daemon_id}]);
  }
 }
 const after = (await client.query('SELECT id,workspace_id,runtime_id,model,thinking_level,service_tier,status,archived_at,name,custom_args FROM agent ORDER BY id')).rows;
 await client.query('COMMIT');
 console.log(JSON.stringify({environment,mode,agents:before.length,busy,daemon_id:health.daemon_id,runtimes,before,after}));
} catch(e) {
 await client.query('ROLLBACK');throw e;
} finally {
 await client.end();
 if (mode === 'apply' && !wasDraining) await request(daemon,'/drain','DELETE');
}

async function targeted(scope) {
 // 2. Pin control rows for the entire target transaction; lease expiry is rechecked immediately before commit.
 const uuid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;
 for (const key of ['delivery_id','workspace_id','agent_id','operation_id']) {
  if (!uuid.test(scope[key] || '')) throw Error('Invalid scope '+key);
 }
 if (scope.environment !== environment || scope.execution_id !== process.env.MULTICA_TASK_ID || !scope.execution_id
     || !scope.candidate_id || !scope.source_commit || !scope.seed_id || !scope.seed_sha256
     || !Number.isSafeInteger(scope.owner_generation) || !Number.isSafeInteger(scope.revision)) throw Error('Invalid maintenance scope');
 const candidate=read(environment==='int'?'candidate.json':'candidates/'+environment+'/current.json');
 if (candidate.status!=='verifying' || candidate.id!==scope.candidate_id || candidate.delivery_id!==scope.delivery_id
     || (candidate.source_commit || candidate.approved_source || candidate.commit)!==scope.source_commit || candidate.commit!==meta.commit
     || candidate.release_id!==meta.release_id
     || (environment==='int'?candidate.publication_lease?.generation:candidate.generation)!==scope.generation) throw Error('Deployed candidate differs');
 const control = new pg.Client({connectionString:prod.server.DATABASE_URL});
 const db = new pg.Client({connectionString:cfg.server.DATABASE_URL});
 let controlConnected=false, targetConnected=false;
 try {
  await control.connect();controlConnected=true;
  await control.query('BEGIN');
  await control.query("SET LOCAL lock_timeout='3s'");
  await control.query("SET LOCAL statement_timeout='10s'");
  await control.query("SET LOCAL idle_in_transaction_session_timeout='20s'");
  const one = async (sql,args) => {
   const result=await control.query(sql,args);
   if (result.rows.length !== 1) throw Error('Control identity missing or ambiguous');
   return result.rows[0];
  };
  const delivery=await one('SELECT * FROM issue_delivery WHERE id=$1 FOR SHARE',[scope.delivery_id]);
  // Modification 1. Int-only acceptance holds preparing_release; retain every candidate, owner and publication fence.
  const activePhase=delivery.phase==='testing' || (environment==='int' && delivery.phase==='preparing_release' && delivery.plan_snapshot?.integration_only===true);
  if (!activePhase || delivery.candidate_id!==scope.candidate_id || delivery.source_commit!==scope.source_commit) throw Error('Inactive delivery/candidate/source');
  const owner=await one('SELECT * FROM delivery_owner WHERE workspace_id=$1 AND issue_id=$2 FOR SHARE',[delivery.workspace_id,delivery.issue_id]);
  if (owner.execution_id!==scope.execution_id || Number(owner.generation)!==scope.owner_generation) throw Error('Stale owner');
  const deadlines=[owner.expires_at];
  if (environment==='int') {
   const lease=await one("SELECT * FROM delivery_publication WHERE resource='int-prod' FOR SHARE",[]);
   if (lease.delivery_id!==delivery.id || lease.workspace_id!==delivery.workspace_id || lease.worker_token!==delivery.worker_token || lease.state!=='active'
       || Number(lease.generation)!==scope.generation) throw Error('Stale integration lease');
   deadlines.push(lease.expires_at);
  } else {
   const lease=await one('SELECT * FROM verification_slot WHERE name=$1 FOR SHARE',[environment]);
   if (!lease.enabled || lease.state!=='verifying' || lease.delivery_id!==delivery.id || lease.workspace_id!==delivery.workspace_id
       || lease.worker_token!==delivery.worker_token || lease.candidate_id!==scope.candidate_id
       || lease.source_commit!==scope.source_commit || Number(lease.generation)!==scope.generation) throw Error('Stale verification lease');
   deadlines.push(lease.lease_expires_at);
   const seed=await one('SELECT * FROM verification_slot_seed WHERE name=$1 FOR SHARE',[environment]);
   if (!seed.verified || seed.seed_id!==scope.seed_id || seed.seed_sha256!==scope.seed_sha256) throw Error('Stale seed');
   const installation=read('verification-install/'+environment+'.json');
   if (installation.seed_id!==scope.seed_id || installation.seed_sha256!==scope.seed_sha256) throw Error('Installed seed differs');
  }
  const seed=read('verification-seed.json');
  if (seed.seed_id!==scope.seed_id || seed.dump_sha256!==scope.seed_sha256) throw Error('Current seed differs');
  const checkExpiry=()=>{
   if (deadlines.some(value=>!value || !Number.isFinite(new Date(value).getTime()) || new Date(value).getTime()<=Date.now()+1000)) throw Error('Expired maintenance ownership');
   const currentSeed=read('verification-seed.json');
   if (currentSeed.seed_id!==scope.seed_id || currentSeed.dump_sha256!==scope.seed_sha256) throw Error('Current seed changed during maintenance');
  };
  checkExpiry();
  const policy=await one('SELECT * FROM test_agent_control WHERE workspace_id=$1 AND name=$2 FOR SHARE',[prod.server.MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID,environment]);
  const selected=policy.desired?.overrides?.[scope.agent_id];
  if (Number(policy.revision)!==scope.revision || !valid(selected)) throw Error('Desired single-Agent override missing or changed');
  const profile=JSON.parse(fs.readFileSync(meta.daemon_profile_path,'utf8'));
  if (profile.workspace_id!==scope.workspace_id || health.daemon_id!==profile.daemon_id && profile.daemon_id) throw Error('Target workspace/daemon differs');
  await db.connect();targetConnected=true;
  await db.query('BEGIN');
  await db.query("SET LOCAL lock_timeout='3s'");
  await db.query("SET LOCAL statement_timeout='10s'");
  await db.query("SET LOCAL idle_in_transaction_session_timeout='20s'");
  // 3. Existing enqueue/claim/update SQL takes conflicting table locks; a short transaction closes all three races.
  await db.query('LOCK TABLE test_agent_control IN ACCESS EXCLUSIVE MODE');
  await db.query('LOCK TABLE agent_task_queue, agent, agent_runtime IN SHARE ROW EXCLUSIVE MODE');
  const agents=await db.query('SELECT * FROM agent WHERE id=$1 AND workspace_id=$2',[scope.agent_id,scope.workspace_id]);
  if (agents.rows.length!==1) throw Error('Agent does not belong to target test workspace');
  const before=agents.rows[0];
  const busy=await db.query("SELECT id FROM agent_task_queue WHERE agent_id=$1 AND status NOT IN ('completed','failed','cancelled') LIMIT 1",[scope.agent_id]);
  if (busy.rows.length || before.status==='running') throw Error('Target Agent has queued/running/claiming work');
  if ((before.custom_args || []).some(x=>/^(?:-m|-c)|^--(?:model|config)(?:=|$)|fast_mode/.test(x))) throw Error('Target Agent has model launch overrides');
  const runtimes=await db.query("SELECT id,runtime_mode FROM agent_runtime WHERE workspace_id=$1 AND provider=$2 AND daemon_id=$3 AND status='online' AND profile_id IS NULL",[scope.workspace_id,selected.runtime || selected.provider,health.daemon_id]);
  if (runtimes.rows.length!==1) throw Error('Target runtime missing or ambiguous');
  const runtime=runtimes.rows[0];
  const mirrors=await db.query('SELECT * FROM test_agent_control WHERE workspace_id=$1 AND name=$2 FOR UPDATE',[scope.workspace_id,environment]);
  if (mirrors.rows.length!==1 || mirrors.rows[0].observed?.daemon_id!==health.daemon_id) throw Error('Target policy mirror not initialized for daemon');
  const mirror=mirrors.rows[0];
  const digest=createHash('sha256').update(JSON.stringify(scope)).digest('hex');
  const previous=mirror.observed?.targeted_operations?.[scope.operation_id];
  const persistReceipt=(receipt,state)=>{
   const directory=path.join(root,'.run','test-agent-maintenance');
   fs.mkdirSync(directory,{recursive:true,mode:0o700});
   const file=path.join(directory,scope.operation_id+'.json');
   const temporary=file+'.'+process.pid+'.tmp';
   fs.writeFileSync(temporary,JSON.stringify({state,...receipt},null,2),{mode:0o600});
   const fd=fs.openSync(temporary,'r');try{fs.fsyncSync(fd);}finally{fs.closeSync(fd);}
   fs.renameSync(temporary,file);
   const dir=fs.openSync(directory,'r');try{fs.fsyncSync(dir);}finally{fs.closeSync(dir);}
  };
  const fields=a=>Object.fromEntries(['runtime_id','runtime_mode','model','thinking_level','service_tier'].map(k=>[k,a[k]]));
  const after={runtime_id:runtime.id,runtime_mode:runtime.runtime_mode,model:selected.model,thinking_level:selected.thinking_level,service_tier:selected.service_tier};
  if (previous) {
   if (previous.scope_sha256!==digest || JSON.stringify(fields(before))!==JSON.stringify(after)
       || JSON.stringify(mirror.desired?.overrides?.[scope.agent_id])!==JSON.stringify(selected)) throw Error('Operation replay differs from committed state');
   await db.query('ROLLBACK');checkExpiry();
   persistReceipt(previous,'applied');
   console.log(JSON.stringify({environment,mode:'target',replayed:true,receipt:previous}));return;
  }
  // 4. Agent, single override and audit receipt commit together; disconnects roll back or replay the same operation.
  const receipt={operation_id:scope.operation_id,scope_sha256:digest,scope,source_revision:Number(policy.revision),
   maintenance_script_sha256:createHash('sha256').update(fs.readFileSync(new URL(import.meta.url))).digest('hex'),
   before:fields(before),after,previous_override:mirror.desired?.overrides?.[scope.agent_id] ?? null,
   applied_override:selected,at:new Date().toISOString()};
  await db.query('UPDATE agent SET runtime_id=$3,runtime_mode=$4,model=$5,thinking_level=$6,service_tier=$7,updated_at=now() WHERE id=$1 AND workspace_id=$2',
   [scope.agent_id,scope.workspace_id,runtime.id,runtime.runtime_mode,selected.model,selected.thinking_level,selected.service_tier]);
  await db.query(`UPDATE test_agent_control SET desired=jsonb_set(desired,'{overrides}',COALESCE(desired->'overrides','{}'::jsonb)||jsonb_build_object($3::text,$4::jsonb)),
   observed=jsonb_set(observed,'{targeted_operations}',COALESCE(observed->'targeted_operations','{}'::jsonb)||jsonb_build_object($5::text,$6::jsonb)),updated_at=now()
   WHERE workspace_id=$1 AND name=$2`,[scope.workspace_id,environment,scope.agent_id,JSON.stringify(selected),scope.operation_id,JSON.stringify(receipt)]);
  checkExpiry();
  persistReceipt(receipt,'prepared');
  checkExpiry();
  await db.query('COMMIT');
  persistReceipt(receipt,'applied');
  console.log(JSON.stringify({environment,mode:'target',replayed:false,receipt}));
 } finally {
  if (targetConnected) {await db.query('ROLLBACK').catch(()=>{});await db.end();}
  if (controlConnected) {await control.query('ROLLBACK').catch(()=>{});await control.end();}
 }
}

// 使用该验收槽位已有身份读取实际运行时目录，不越过运行时权限。
async function publishCatalogs(runtimes) {
 if ((mode !== 'apply' && process.env.MULTICA_TEST_AGENT_CATALOG !== '1') || !runtimes.length) return;
 const profile = JSON.parse(fs.readFileSync(meta.daemon_profile_path, 'utf8'));
 if (!profile.token) throw Error('验收槽位缺少目录读取凭据');
 const catalogs = new Map();
 let next = 0;
 await Promise.all(Array.from({length: Math.min(8, runtimes.length)}, async () => {
  while (next < runtimes.length) {
   const runtime = runtimes[next++];
   try { catalogs.set(runtime.id, await runtimeCatalog(profile.token, runtime)); }
   catch (error) { console.error('模型目录获取失败', runtime.id, String(error).slice(0, 200)); }
  }
 }));
 for (const runtime of runtimes) {
  runtime.models = catalogs.get(runtime.id) || [];
  if (runtime.models.length) runtime.catalog_observed_at = new Date().toISOString();
 }
}

async function runtimeCatalog(token, runtime) {
 const headers = {Authorization: 'Bearer ' + token, 'X-Workspace-ID': runtime.workspace_id};
 const response = await fetch(meta.backend_url + '/api/runtimes/' + runtime.id + '/models', {
  method: 'POST', headers, signal: AbortSignal.timeout(20000),
 });
 if (!response.ok) throw Error('模型目录 HTTP ' + response.status);
 let body = await response.json();
 const deadline = Date.now() + 12000;
 while (['pending', 'running'].includes(body.status) && Date.now() < deadline) {
  await new Promise(resolve => setTimeout(resolve, 300));
  const poll = await fetch(meta.backend_url + '/api/runtimes/' + runtime.id + '/models/' + body.id, {headers, signal: AbortSignal.timeout(5000)});
  if (!poll.ok) throw Error('模型目录读取失败 HTTP ' + poll.status);
  body = await poll.json();
 }
 if (body.status !== 'completed') throw Error('模型目录未就绪');
 return (body.models || []).map(model => ({
  id: model.id,
  label: model.label || model.id,
  ...(model.thinking ? {thinking: model.thinking} : {}),
  ...(model.service_tiers ? {service_tiers: model.service_tiers} : {}),
  ...(model.supports_explicit_standard_service_tier ? {supports_explicit_standard_service_tier: true} : {}),
 }));
}
