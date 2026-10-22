#!/usr/bin/env python3
"""仅在固定测试环境验证真实交付 API；创建并回收本次成员测试任务。"""
import concurrent.futures, importlib.util, json, os, urllib.request, urllib.error, uuid
from pathlib import Path

spec=importlib.util.spec_from_file_location('release',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

def verify():
    env=os.environ.get('GOAL_TEST_ENV','int')
    if env not in ('int',*r.VERIFICATION_ENVS):raise RuntimeError('业务验收禁止使用生产环境')
    meta=r.read(r.meta_path(env));profile=r.read(meta['daemon_profile_path'])
    evidence=r.RUN/'delivery-evidence'/('coordination-'+meta['release_id']+'.json')
    checks=[];created=[];owner={}
    def api(method,route,body=None,headers=None,want=200):
        req=urllib.request.Request(meta['backend_url']+'/api/'+route,data=None if body is None else json.dumps(body).encode(),method=method,
            headers={'Authorization':'Bearer '+profile['token'],'X-Workspace-ID':profile['workspace_id'],'Content-Type':'application/json',**(headers or {})})
        opener=urllib.request.build_opener(urllib.request.ProxyHandler({}))
        try:
            with opener.open(req,timeout=30) as response:
                status=response.status;raw=response.read();result=json.loads(raw) if raw else None
        except urllib.error.HTTPError as error:
            status=error.code;result=error.read().decode()
        if status!=want:raise RuntimeError(f'{method} {route}: expected {want}, received {status}; {result}')
        return result
    config=api('GET','config')
    if config.get('environment_id')!=env:raise RuntimeError('服务环境身份不匹配')
    me=api('GET','me')
    try:
        issue=api('POST','issues',dict(title='交付协调自动验收 '+str(uuid.uuid4())[:8],status='in_progress',assignee_type='member',assignee_id=me['id']),want=201)
        identity=issue['id'];created.append(identity);r.write(evidence,dict(created=created,checks=checks,environment=env),True)
        route='issues/'+identity
        comment=api('POST',route+'/comments',dict(content='自动验收样本：交付幂等、执行归属及验收证据持久化。仅测试环境，不发布此任务。'),want=201)
        owner=api('POST',route+'/delivery-owner',dict(action='claim',execution_id='api-verifier'))
        headers={'X-Delivery-Token':owner['token'],'X-Delivery-Generation':str(owner['generation'])}
        api('POST',route+'/delivery-owner',dict(action='claim',execution_id='competing-session'),want=409)
        checks.append('竞争会话无法领取同一任务')
        plan=api('PUT',route+'/delivery-plan',dict(comment_id=comment['id'],revision=comment['revision']),headers)['plan']
        api('POST',route+'/delivery-plan/confirm',dict(plan_id=plan['id'],revision=plan['comment_revision']))
        payload=dict(source_commit=meta['commit'],base_commit=meta['commit'],source_branch='feature/delivery-coordination',evidence=[dict(name='API fixture',status='passed',evidence='实际固定测试环境 API 请求')])
        def submit(_):return api('POST',route+'/deliveries',dict(payload,operation_id=str(uuid.uuid4())),headers)
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:responses=list(executor.map(submit,range(2)))
        ids={value['deliveries'][0]['id'] for value in responses}
        if len(ids)!=1:raise RuntimeError('并发登记产生了多个交付')
        checks.append('并发登记不同操作号、相同提交只产生一个交付')
        d=api('GET',route+'/deliveries')['deliveries'][0]
        api('POST',route+'/deliveries',dict(payload,operation_id=str(uuid.uuid4()),source_commit='0'*40),headers,want=409)
        checks.append('新提交无法隐式覆盖当前候选')
        claim=api('POST','delivery-queue/advance',dict(id=d['id'],revision=d['revision'],action='claim',source_commit=d['source_commit']))
        d.update(claim)
        prepared=api('POST','delivery-queue/advance',dict(id=d['id'],revision=d['revision'],action='prepared',worker_token=d['worker_token'],candidate_id='api-fixture-'+identity,candidate_url=meta['frontend_url'],candidate_identity={'runtime_inputs':meta['commit']},candidate_commit=meta['commit']))
        d.update(prepared)
        api('POST',route+'/deliveries/'+d['id']+'/action',dict(action='accept',revision=d['revision'],evidence=payload['evidence']),headers)
        state=api('GET',route+'/deliveries')
        if len(state['acceptances'])!=1 or state['acceptances'][0]['source_commit']!=meta['commit']:raise RuntimeError('验收历史没有绑定实际提交')
        checks.append('验收证据绑定实际候选并持久保存')
        report=dict(environment=env,release_id=meta['release_id'],created=created,checks=checks,status='passed')
        r.write(evidence,report,True);print(json.dumps(report,ensure_ascii=False));return report
    finally:
        for identity in created:api('DELETE','issues/'+identity,want=204)

if __name__=='__main__':verify()
