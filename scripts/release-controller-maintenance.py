#!/usr/bin/env python3
"""Explicitly authorized controller maintenance through the existing release builder and publisher."""
import contextlib
import hashlib
import json
import os
from pathlib import Path
import urllib.request

# 1. Maintenance is confined to control-plane recovery; application migrations and supervision implementation are excluded.
ALLOWED={
 # Existing installed target-Agent maintenance and bounded repair protocol are preserved byte-for-byte.
 'scripts/test-agent-models.py','scripts/test-agent-models.mjs','scripts/test-agent-models.test.py','scripts/test-agent-models.test.mjs',
 # Modification 1. Integration scheduling is control-plane maintenance, with no supervision or migration changes.
 'server/internal/handler/delivery_authorization_review_test.go','server/internal/handler/issue_review_sweeper_test.go',
 'server/internal/handler/issue_delivery_worker.go','server/internal/handler/issue_delivery_checks_test.go',
 'scripts/release-controller-maintenance.py','scripts/release-controller-maintenance.test.py',
 'scripts/delivery-operations.json','scripts/release-environments.py','scripts/release-environments.test.py',
 'scripts/issue-delivery-worker.py','scripts/issue-delivery-worker.test.py',
 'server/internal/handler/issue_delivery.go','server/internal/handler/issue_delivery_repair.go',
 'server/internal/handler/issue_delivery_repair_test.go','server/internal/handler/verification_slot.go',
 'server/internal/verification/slots.go','server/cmd/multica/cmd_issue_delivery.go',
 'docs/operations/environment-releases.md',
 'scripts/fixtures/trace-compatibility/643_supervision_material_trace_usage.up.sql',
 'scripts/fixtures/trace-compatibility/645_supervision_round_preparation_budget.up.sql',
 'scripts/fixtures/trace-compatibility/649_supervision_trace_scope.up.sql',
}

def api(r,path):
 m=r.read(r.meta_path('prod'));p=r.read(m['daemon_profile_path'])
 request=urllib.request.Request(m['backend_url']+'/api/'+path,headers={'Authorization':'Bearer '+p['token'],'X-Workspace-ID':p['workspace_id']})
 with urllib.request.build_opener(urllib.request.ProxyHandler({})).open(request,timeout=15) as response:return json.load(response)

def validate_source(r,source,base):
 source,commit=r.checked_source(source)
 if r.read(r.RUN/'published.json')['commit']!=base:raise RuntimeError('维护基线已变化')
 changed=r.output(['git','diff','--name-only',base,commit],cwd=source).splitlines()
 if not changed or set(changed)-ALLOWED:raise RuntimeError('维护包包含授权范围之外的文件')
 old=Path(r.read(r.meta_path('prod'))['release_dir'])/'server/migrations'
 current=source/'server/migrations'
 identities=lambda folder:{p.name:hashlib.sha256(p.read_bytes()).hexdigest() for p in folder.glob('*.up.sql')}
 if identities(old)!=identities(current):raise RuntimeError('控制端维护禁止改变业务迁移')
 return source,commit

def authorize(r,record):
 auth=record['maintenance']
 rows=api(r,'issues/'+auth['issue_id']+'/comments?thread='+auth['comment_id']+'&tail=1')
 if isinstance(rows,dict):rows=rows.get('comments',rows.get('data',[]))
 comment=next((x for x in rows if x['id']==auth['comment_id']),None)
 if not comment or comment.get('author_type')!='member' or comment.get('revision')!=auth['comment_revision'] or hashlib.sha256(comment['content'].encode()).hexdigest()!=auth['comment_sha256']:
  raise RuntimeError('维护授权评论已变化或不是用户决定')
 status=api(r,'issues/'+auth['issue_id']+'/deliveries')
 if status['user_comments_version']!=auth['user_comments_version']:raise RuntimeError('出现新的用户决定，维护前需要重新核对')
 if not auth.get('scope'):raise RuntimeError('缺少维护授权范围说明')
 if not auth.get('checks') or any(c.get('status')!='passed' or not c.get('name') or not c.get('evidence') for c in auth['checks']):raise RuntimeError('缺少准确维护候选检查证据')
 validate_source(r,record['source'],auth['base_commit'])

def idle(r,production=False):
 rows=api(r,'delivery-queue')
 if any(d['phase'] in ('waiting_int','testing','approved','preparing_release','publishing') for d in rows):raise RuntimeError('其他交付正在使用控制端，维护保持等待')
 for env in ('prod','int'):
  m=r.read(r.meta_path(env));r.check_process_ownership(env,m);r.check_live_environment(env,m,r.read(r.config_path(env)))
 for env in (('int','prod') if production else ('int',)):
  status=r.http(r.health_url(env)+'/drain')
  if status.get('active_tasks')!=0 or status.get('claims_in_flight')!=0:raise RuntimeError('环境仍有活动任务，维护不暂停其他任务')
 current=r.candidate() if r.candidate_path().exists() else None
 if current and not current.get('maintenance') and current['status'] not in ('released','failed','succeeded'):raise RuntimeError('int 已有活动候选')

# 2. Keep the normal durable candidate/request records, full operations bundle, locks, release windows and rollback.
def prepare(r,source,evidence):
 with r.lock('delivery-worker-prod'),r.lock('publisher'):
  idle(r)
  source,commit=validate_source(r,source,evidence['base_commit'])
  # Modification 1. An explicit failed-request recovery retains its identity and immutable attempt history.
  version=evidence.get('retry_request_id') or 'maintenance-'+commit
  if evidence.get('retry_request_id'):
   previous=r.read(r.request_path(version))
   if previous['status']!='failed' or previous.get('maintenance',{}).get('issue_id')!=evidence['issue_id'] or previous['maintenance']['base_commit']!=evidence['base_commit']:raise RuntimeError('不是同一任务的失败维护请求')
   if r.subprocess.run(['systemctl','is-active','--quiet',previous['unit']]).returncode==0:raise RuntimeError('原发布器仍在运行')
  record=dict(id=version,commit=commit,source=str(source),maintenance=evidence,status='building',environment='int',
   git_common=r.output(['git','rev-parse','--path-format=absolute','--git-common-dir'],cwd=source),branch=r.output(['git','symbolic-ref','HEAD'],cwd=source))
  authorize(r,record)
  current=r.candidate() if r.candidate_path().exists() else None
  if current and current['id']==version and current['status'] in ('verifying','accepted','queued'):
   r.validate_candidate(current);return current
  r.save_candidate(record)
  try:
   artifact=r.RUN/'verification-builds'/'maintenance'/commit
   if (artifact/'release.json').exists():
    manifest=r.read(artifact/'release.json')
    if manifest['files']!=r.checksums(artifact):raise RuntimeError('维护制品指纹不符')
   else:manifest=r.build(source,artifact)
   r.publish('int',artifact,90)
   record.update(status='verifying',release_id=manifest['id'],source_digest=manifest['source_digest'],validation_identity=r.validation_identity(source),deployed_at=r.read(r.meta_path('int'))['deployed_at'])
  except Exception as error:
   record.update(status='failed',error=str(error));raise
  finally:r.save_candidate(record)
  return record

@contextlib.contextmanager
def guard(r,record):
 with r.lock('delivery-worker-prod'):
  authorize(r,record);idle(r,production=True)
  # Ordinary release guards are database-backed; this bounded maintenance window instead holds the same queue and publisher locks.
  yield lambda:authorize(r,record)
