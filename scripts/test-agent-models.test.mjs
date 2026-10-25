// Run only against a newly created disposable PostgreSQL instance; no installed agent CLI is executed.
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import http from 'node:http';
import { spawn } from 'node:child_process';
import pg from 'pg';

// 1. Refuse shared database names and build both sides from the real control migrations.
const url=new URL(process.env.TEST_AGENT_MODELS_DATABASE_URL || 'invalid:');
if (!/^\/test_agent_models_[a-z0-9_]+$/.test(url.pathname)) throw Error('Use a fresh test_agent_models_* database');
const control=new pg.Client({connectionString:url.href});await control.connect();
const targetURL=new URL(url);targetURL.pathname+='_target';
await control.query('CREATE DATABASE '+targetURL.pathname.slice(1));
const db=new pg.Client({connectionString:targetURL.href});await db.connect();
const root=fs.mkdtempSync(path.join(os.tmpdir(),'target-policy-test-'));
const run=path.join(root,'.run');
const write=(name,value)=>{const p=path.join(run,name);fs.mkdirSync(path.dirname(p),{recursive:true});fs.writeFileSync(p,JSON.stringify(value));};
const id=Object.fromEntries(['delivery','workspace','issue','agent','other','worker','runtime','codex','operation','controlWorkspace'].map(k=>[k,randomUUID()]));
const flash={provider:'codebuddy',model:'deepseek-v4.1-flash',thinking_level:'',service_tier:''};
const codex={provider:'codex',model:'gpt-6.1-sol',thinking_level:'low',service_tier:'default'};
let drains=0, testEnvironment='verify-1';
const profile='target-policy-tests-'+process.pid;
const port=19515+[...Buffer.from(profile)].reduce((a,b)=>a+b,0)%1000;
const server=http.createServer((req,res)=>{res.setHeader('Content-Type','application/json');if(req.url==='/drain')drains++;res.end(JSON.stringify(req.url==='/api/config'?{environment_id:testEnvironment}:{commit:'commit',daemon_id:'daemon-test'}));});
await new Promise(resolve=>server.listen(port,'127.0.0.1',resolve));
const scope={environment:'verify-1',execution_id:'test-execution',delivery_id:id.delivery,workspace_id:id.workspace,
 agent_id:id.agent,operation_id:id.operation,candidate_id:'candidate',source_commit:'commit',seed_id:'seed',seed_sha256:'sha',owner_generation:1,generation:1,revision:2};
const scopePath=path.join(root,'scope.json');
const scopeWrite=()=>fs.writeFileSync(scopePath,JSON.stringify(scope));scopeWrite();
const profilePath=path.join(root,'profile.json');fs.writeFileSync(profilePath,JSON.stringify({workspace_id:id.workspace,daemon_id:'daemon-test'}));
write('env/prod-services.json',{server:{DATABASE_URL:url.href,MULTICA_TEST_AGENT_CONTROL_WORKSPACE_ID:id.controlWorkspace}});
write('env/verify-1-services.json',{server:{DATABASE_URL:targetURL.href,MULTICA_ENV_ID:'verify-1'}});
write('deployments/goal-test-verify-1.json',{backend_url:'http://127.0.0.1:'+port,commit:'commit',release_id:'artifact',daemon_profile:profile,daemon_profile_path:profilePath});
const candidate={id:'candidate',commit:'commit',release_id:'artifact',generation:1,status:'verifying',delivery_id:id.delivery};
write('candidates/verify-1/current.json',candidate);
write('verification-install/verify-1.json',{seed_id:'seed',seed_sha256:'sha'});
write('verification-seed.json',{seed_id:'seed',dump_sha256:'sha'});
const script=path.join(import.meta.dirname,'test-agent-models.mjs');
function launch() {
 const child=spawn(process.execPath,[script,scope.environment,'target','-',scopePath],{env:{...process.env,MULTICA_RELEASE_ROOT:root,MULTICA_TASK_ID:'test-execution'},stdio:['ignore','pipe','pipe']});
 let stdout='',stderr='';child.stdout.on('data',v=>stdout+=v);child.stderr.on('data',v=>stderr+=v);
 return {child,result:new Promise(resolve=>child.on('close',(code,signal)=>resolve({code,signal,stdout,stderr})))};
}
let count=0;
async function succeeds(label) {const r=await launch().result;assert.equal(r.code,0,r.stderr);count++;console.log('PASS '+label);return JSON.parse(r.stdout);}
async function refuses(label) {const r=await launch().result;assert.notEqual(r.code,0,r.stdout);count++;console.log('PASS '+label);}
async function rows(){return (await db.query('SELECT * FROM agent ORDER BY id')).rows;}
async function waiting() {
 const deadline=Date.now()+5000;
 while(Date.now()<deadline){if((await db.query("SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND pid<>pg_backend_pid()" )).rowCount)return;await new Promise(r=>setTimeout(r,10));}
 throw Error('Expected blocked maintenance transaction');
}
try {
 for(const file of ['542_issue_delivery.up.sql','601_delivery_coordination.up.sql','571_verification_slot.up.sql','627_verification_seed_lifecycle.up.sql'])await control.query(fs.readFileSync(path.join(import.meta.dirname,'../server/migrations',file),'utf8'));
 const schema=`CREATE TABLE test_agent_control(workspace_id uuid,name text,desired jsonb,observed jsonb,revision bigint DEFAULT 1,updated_at timestamptz DEFAULT now(),PRIMARY KEY(workspace_id,name))`;
 await control.query(schema);await db.query(schema);
 await control.query(`INSERT INTO issue_delivery(id,workspace_id,issue_id,operation_id,plan_id,plan_snapshot,source_commit,source_branch,base_commit,phase,verification_comment_id,release_comment_id,candidate_id,worker_token)
 VALUES($1,$2,$3,$1,$1,'{}','commit','branch','base','testing',$1,$1,'candidate',$4)`,[id.delivery,id.controlWorkspace,id.issue,id.worker]);
 await control.query("INSERT INTO delivery_owner VALUES($1,$2,'test-execution',$3,1,now()+interval '1 hour','',now())",[id.controlWorkspace,id.issue,randomUUID()]);
 await control.query("INSERT INTO verification_slot(name,enabled,state,generation,workspace_id,delivery_id,worker_token,source_commit,candidate_id,lease_expires_at) VALUES('verify-1',true,'verifying',1,$1,$2,$3,'commit','candidate',now()+interval '1 hour')",[id.controlWorkspace,id.delivery,id.worker]);
 await control.query("INSERT INTO verification_slot_seed(name,seed_id,seed_sha256,verified) VALUES('verify-1','seed','sha',true)");
 await control.query("INSERT INTO test_agent_control(workspace_id,name,desired,observed,revision) VALUES($1,'verify-1',$2,'{}',2)",[id.controlWorkspace,{policy:codex,overrides:{[id.agent]:flash}}]);
 await db.query('CREATE TABLE agent(id uuid PRIMARY KEY,workspace_id uuid,runtime_id uuid,runtime_mode text,model text,thinking_level text,service_tier text,status text,archived_at timestamptz,name text,custom_args jsonb,updated_at timestamptz DEFAULT now())');
 await db.query('CREATE TABLE agent_runtime(id uuid,workspace_id uuid,provider text,runtime_mode text,status text,daemon_id text,profile_id uuid)');
 await db.query('CREATE TABLE agent_task_queue(id uuid DEFAULT gen_random_uuid(),agent_id uuid,status text)');
 await db.query("INSERT INTO agent_runtime VALUES($1,$2,'codebuddy','local','online','daemon-test',NULL),($3,$2,'codex','local','online','daemon-test',NULL)",[id.runtime,id.workspace,id.codex]);
 for(const agent of [id.agent,id.other])await db.query("INSERT INTO agent(id,workspace_id,runtime_id,runtime_mode,model,thinking_level,service_tier,status,name,custom_args) VALUES($1::uuid,$2,$3,'local','gpt-6.1-sol','low','default','idle',$1::text,'[]')",[agent,id.workspace,id.codex]);
 await db.query("INSERT INTO test_agent_control(workspace_id,name,desired,observed) VALUES($1,'verify-1',$2,$3)",[id.workspace,{policy:codex,overrides:{[id.other]:codex}},{daemon_id:'daemon-test'}]);
 const before=await rows();
 // 全环境维护也必须拒绝等待目录和 deferred，不能仅保护定向入口。
 const applyPolicy=path.join(root,'apply-policy.json');fs.writeFileSync(applyPolicy,JSON.stringify({policy:flash,overrides:{}}));
 for(const status of ['queued','dispatched','running','waiting_local_directory','deferred']) {
  await db.query('INSERT INTO agent_task_queue(agent_id,status) VALUES($1,$2)',[id.other,status]);
  const applied=spawn(process.execPath,[script,'verify-1','apply',applyPolicy],{env:{...process.env,MULTICA_RELEASE_ROOT:root},stdio:['ignore','pipe','pipe']});
  let error='';applied.stderr.on('data',data=>error+=data);applied.stdout.resume();
  const code=await new Promise(resolve=>applied.on('close',resolve));assert.notEqual(code,0,error);
  assert.deepEqual(await rows(),before);await db.query('DELETE FROM agent_task_queue');count++;console.log('PASS global apply refuses '+status);
 }
 drains=0;

 for(const [key,value] of [['environment','prod'],['workspace_id',randomUUID()],['agent_id',randomUUID()],['execution_id','stale'],['owner_generation',2],['generation',2],['revision',1],['candidate_id','stale'],['source_commit','stale'],['seed_id','stale'],['seed_sha256','stale']]) {
  const old=scope[key];scope[key]=value;scopeWrite();await refuses('invalid '+key);scope[key]=old;scopeWrite();
 }
 for(const [sql,restore] of [["UPDATE delivery_owner SET expires_at=now()-interval '1 second'","UPDATE delivery_owner SET expires_at=now()+interval '1 hour'"],["UPDATE verification_slot SET lease_expires_at=now()-interval '1 second'","UPDATE verification_slot SET lease_expires_at=now()+interval '1 hour'"],["UPDATE issue_delivery SET phase='needs_changes'","UPDATE issue_delivery SET phase='testing'"]]){await control.query(sql);await refuses('inactive ownership');await control.query(restore);}
 for(const status of ['queued','dispatched','running','waiting_local_directory','deferred']){await db.query('INSERT INTO agent_task_queue(agent_id,status) VALUES($1,$2)',[id.agent,status]);await refuses('busy '+status);await db.query('DELETE FROM agent_task_queue');}
 candidate.status='released';write('candidates/verify-1/current.json',candidate);await refuses('released candidate');candidate.status='verifying';write('candidates/verify-1/current.json',candidate);
 assert.deepEqual(await rows(),before);
 // 2. Interrupt after queue locks but before writes, then prove the same operation can safely resume.
 const blocker=new pg.Client({connectionString:targetURL.href});await blocker.connect();await blocker.query('BEGIN');await blocker.query('LOCK agent_runtime IN SHARE MODE');
 const interrupted=launch();await waiting();interrupted.child.kill('SIGTERM');await interrupted.result;await blocker.query('ROLLBACK');assert.deepEqual(await rows(),before);count++;console.log('PASS interrupted transaction rolls back');
 await blocker.query('BEGIN');await blocker.query('LOCK agent_runtime IN SHARE MODE');const pending=launch();await waiting();
 const enqueue=new pg.Client({connectionString:targetURL.href});await enqueue.connect();await enqueue.query("SET lock_timeout='150ms'");
 await assert.rejects(enqueue.query("INSERT INTO agent_task_queue(agent_id,status) VALUES($1,'queued')",[id.agent]),{code:'55P03'});
 await assert.rejects(enqueue.query("UPDATE agent SET name='concurrent' WHERE id=$1",[id.agent]),{code:'55P03'});
 const editor=new pg.Client({connectionString:url.href});await editor.connect();await editor.query("SET lock_timeout='150ms'");
 await assert.rejects(editor.query("UPDATE test_agent_control SET revision=revision+1 WHERE name='verify-1'"),{code:'55P03'});
 count+=3;console.log('PASS concurrent enqueue, Agent edit, desired edit are fenced');
 await blocker.query('ROLLBACK');const applied=await pending.result;assert.equal(applied.code,0,applied.stderr);count++;
 const after=await rows();assert.deepEqual(after.find(x=>x.id===id.other),before.find(x=>x.id===id.other));assert.equal(after.find(x=>x.id===id.agent).model,flash.model);
 let mirror=(await db.query('SELECT * FROM test_agent_control')).rows[0];assert.deepEqual(mirror.desired.policy,codex);assert.deepEqual(mirror.desired.overrides[id.other],codex);assert.equal(Object.keys(mirror.observed.targeted_operations).length,1);
 assert.equal((await succeeds('idempotent retry after commit')).replayed,true);
 // 3. Cleanup follows a new desired revision and operation, preserving the first immutable receipt.
 await control.query("UPDATE test_agent_control SET revision=3,desired=jsonb_set(desired,ARRAY['overrides',$1::text],$2::jsonb)",[id.agent,JSON.stringify(codex)]);
 scope.revision=3;scope.operation_id=randomUUID();scopeWrite();await succeeds('cleanup via formal override');
 assert.deepEqual((await rows()).map(({updated_at,...a})=>a),before.map(({updated_at,...a})=>a));
 mirror=(await db.query('SELECT * FROM test_agent_control')).rows[0];assert.equal(Object.keys(mirror.observed.targeted_operations).length,2);assert.equal(drains,0);
 // 4. Integration uses the real int-prod publication identity and the integrated candidate's approved source.
 testEnvironment='int';scope.environment='int';scope.revision=2;scope.operation_id=randomUUID();scopeWrite();
 const readLocal=name=>JSON.parse(fs.readFileSync(path.join(run,name),'utf8'));
 const intCfg=readLocal('env/verify-1-services.json');intCfg.server.MULTICA_ENV_ID='int';write('env/int-services.json',intCfg);
 write('deployments/goal-test-int.json',readLocal('deployments/goal-test-verify-1.json'));
 write('candidate.json',{...candidate,commit:'commit',approved_source:'commit',publication_lease:{generation:1}});
 await control.query("INSERT INTO delivery_publication(resource,workspace_id,delivery_id,worker_token,generation,expires_at,state) VALUES('int-prod',$1,$2,$3,1,now()+interval '1 hour','active')",[id.controlWorkspace,id.delivery,id.worker]);
 await control.query("INSERT INTO test_agent_control(workspace_id,name,desired,observed,revision) VALUES($1,'int',$2,'{}',2)",[id.controlWorkspace,{policy:codex,overrides:{[id.agent]:flash}}]);
 await db.query("INSERT INTO test_agent_control(workspace_id,name,desired,observed) VALUES($1,'int',$2,$3)",[id.workspace,{policy:codex,overrides:{[id.other]:codex}},{daemon_id:'daemon-test'}]);
 // 5. The production int-only phase must pass without weakening any scoped boundary.
 await control.query("UPDATE issue_delivery SET phase='preparing_release',plan_snapshot='{\"integration_only\":true}'");
 await succeeds('fixed int-only preparing_release publication lease');
 const intBefore=await rows();
 for(const [sql,restore,label] of [
  ["UPDATE issue_delivery SET plan_snapshot='{}'", "UPDATE issue_delivery SET plan_snapshot='{\"integration_only\":true}'", 'release preparation without int-only scope'],
  ["UPDATE issue_delivery SET phase='publishing'", "UPDATE issue_delivery SET phase='preparing_release'", 'publishing phase'],
  ["UPDATE delivery_publication SET expires_at=now()-interval '1 second'", "UPDATE delivery_publication SET expires_at=now()+interval '1 hour'", 'expired int publication'],
  ["UPDATE delivery_publication SET worker_token=gen_random_uuid()", null, 'foreign publication worker']
 ]) {
  await control.query(sql);await refuses(label);
  if(restore)await control.query(restore);else await control.query('UPDATE delivery_publication SET worker_token=$1',[id.worker]);
 }
 for(const [key,value] of [['environment','prod'],['agent_id',id.other],['candidate_id','foreign'],['owner_generation',2],['seed_sha256','foreign']]) {
  const old=scope[key];scope[key]=value;scopeWrite();await refuses('int-only invalid '+key);scope[key]=old;scopeWrite();
 }
 await db.query("INSERT INTO agent_task_queue(agent_id,status) VALUES($1,'queued')",[id.agent]);
 await refuses('int-only selected Agent busy');await db.query('DELETE FROM agent_task_queue');
 assert.deepEqual(await rows(),intBefore);

 await control.query("UPDATE delivery_publication SET state='idle'");await refuses('inactive int publication lease');
 const receipt=JSON.parse(fs.readFileSync(path.join(run,'test-agent-maintenance',scope.operation_id+'.json'),'utf8'));assert.equal(receipt.state,'applied');
 await blocker.end();await enqueue.end();await editor.end();console.log('PASS '+count+' checks; no drain, unrelated Agent/default preserved');
} finally {
 await db.end();await control.query('DROP DATABASE '+targetURL.pathname.slice(1)+' WITH (FORCE)');await control.end();await new Promise(resolve=>server.close(resolve));fs.rmSync(root,{recursive:true,force:true});
}
