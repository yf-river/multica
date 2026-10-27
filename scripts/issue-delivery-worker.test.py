import os
import importlib.util,json,subprocess,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('delivery_worker',Path(__file__).with_name('issue-delivery-worker.py'))
w=importlib.util.module_from_spec(spec);spec.loader.exec_module(w);r=w.r

class DeliveryTests(unittest.TestCase):
    def test_active_maintenance_holds_new_verification_admission(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder)
            with patch.object(r,'RUN',root):
                r.write(root/'verification-pool.json',dict(enabled=True,controller_environment='prod'))
                r.write(r.candidate_path(),dict(status='building',maintenance={'scope':'control'}))
                self.assertFalse(worker.pool_enabled())
                r.write(r.candidate_path(),dict(status='succeeded',maintenance={'scope':'control'}))
                self.assertTrue(worker.pool_enabled())
    # 修改 1. 验证历史结果通知不会阻塞已验收交付领取固定 int。
    def test_pending_integration_precedes_historical_result_notice(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        delivery=dict(id='integration',phase='awaiting_review',integration_pending=True,plan_snapshot={})
        rows=[dict(id='old',phase='published'),delivery]
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'recover_release_windows'),patch.object(r,'lock'),patch.object(r,'candidate_path',return_value=Path(folder)/'missing'),patch.object(worker,'api',return_value=rows),patch.object(worker,'recover_finished_publications'),patch.object(worker,'pool_enabled',return_value=False),patch.object(worker,'pool_dispatch',return_value=set()),patch.object(worker,'batch_config',return_value=False),patch.object(worker,'pin'),patch.object(worker,'busy',return_value=False),patch.object(worker,'advance') as advance,patch.object(worker,'prepare') as prepare:
            worker.tick()
            advance.assert_called_once_with(delivery,'claim-integration')
            prepare.assert_called_once_with(delivery)

    # Modification 1. The int-only consumer finalizes review and releases its lease without publishing.
    def test_int_only_accept_does_not_validate_or_publish(self):
        worker=object.__new__(w.Worker)
        d=dict(id='delivery',candidate_id='int',plan_snapshot=dict(integration_only=True),approved_by=None)
        with patch.object(worker,'own_candidate',return_value=dict(id='int',status='accepted')),patch.object(worker,'advance') as advance,patch.object(r,'release_candidate') as release,patch.object(worker,'finish_publication') as finish,patch.object(worker,'publish') as publish:
            worker.accept(d,release=True)
            advance.assert_called_once_with(d,'integrated',candidate_id='int')
            release.assert_called_once_with('int');finish.assert_called_once_with(d);publish.assert_not_called()



    # Modification 1. Real inventory scale and long evidence must stay below the actual slot API's 8KB contract.
    def test_large_repair_inventory_is_bounded_and_canonical(self):
        import hashlib
        files={('apps/web/chunks/'+str(i)+'x'*80):'a'*64 for i in range(6147)}
        with tempfile.TemporaryDirectory() as folder:
            artifact=Path(folder)/'artifact';artifact.mkdir()
            detail=json.loads(w.repair_detail(artifact,dict(id='candidate',source_commit='a'*40,files=files),'old','report'*20000))
            self.assertEqual(detail['files_count'],6147)
            self.assertEqual(detail['files_sha256'],hashlib.sha256(json.dumps(files,sort_keys=True,separators=(',',':'),ensure_ascii=False).encode()).hexdigest())
            self.assertLess(len(json.dumps(dict(detail=json.dumps(detail))).encode()),7000)
            self.assertEqual(Path(detail['compatibility']['ref']).read_text(),'report'*20000)
            self.assertNotIn('files',detail)

    # 1. A failed migration gate must leave the original quarantine and running candidate untouched.
    def test_repair_validates_before_recovering_original_lease(self):
        from unittest.mock import Mock
        worker=object.__new__(w.Worker)
        old=dict(name='verify-1',generation=29,worker_token='old',source_commit='old',delivery_id='delivery')
        repair=dict(environment='verify-1',generation=29,compatibility_evidence='reviewed report')
        d=dict(id='delivery',source_commit='new',worker_token='new-token',plan_snapshot=dict(verification_repair=dict(request=repair)))
        metadata=dict(release_dir='/old',release_id='old-release')
        manifest=dict(source_commit='new',files={'server':'exact-sha'})
        with patch.object(w,'repair_detail',return_value='validated fixture audit'),patch.object(r,'read',side_effect=lambda path:manifest if str(path).endswith('release.json') else metadata),patch.object(r,'check_process_ownership'),patch.object(worker,'seed_matches',return_value=True),patch.object(worker,'source',return_value=Path('/source')),patch.object(r,'build_verification',return_value=Path('/artifact')),patch.object(r,'checksums',return_value=manifest['files']),patch.object(r,'validate_pending_migrations') as gate,patch.object(r,'candidate_path') as candidate_path,patch.object(worker,'api',return_value=dict(old,generation=30)) as api:
            candidate_path.return_value.exists.return_value=False
            gate.side_effect=RuntimeError('changed migration')
            with self.assertRaisesRegex(RuntimeError,'changed migration'):worker.prepare_repair(d,dict(lease=old),Mock())
            api.assert_not_called()
            gate.side_effect=None
            saved=[]
            result=worker.prepare_repair(d,dict(lease=old),lambda stage,**kw:saved.append((stage,kw)))
            self.assertEqual(result[2]['generation'],30)
            self.assertEqual(api.call_args.args[1]['generation'],29)
            self.assertEqual(api.call_args.args[1]['action'],'repair')
            self.assertEqual(saved[-1][1]['repair_lease'],old)
            # Restart uses the persisted original lease rather than incrementing the requested generation again.
            worker.prepare_repair(d,dict(lease=dict(old,generation=30),repair_lease=old),Mock())
            self.assertEqual(api.call_args.args[1]['generation'],29)

    def test_external_verification_owner_defers_native_terminal_and_notification(self):
        worker=object.__new__(w.Worker)
        for attempt in ({},dict(id='native',settled=True,status='completed')):
            d=dict(verification_owner=dict(external=True),verification_run=attempt)
            with patch.object(worker,'advance') as advance:
                self.assertTrue(worker.verification_pending(d))
                advance.assert_not_called()

    def test_verification_lease_expiry_and_release_restore_native_work(self):
        worker=object.__new__(w.Worker)
        with patch.object(worker,'advance',return_value={}) as advance:
            self.assertTrue(worker.verification_pending({}))
            advance.assert_called_once_with({},'notify')
        d=dict(verification_run=dict(id='native',settled=True,status='completed'))
        with patch.object(worker,'advance',return_value={}) as advance:
            self.assertFalse(worker.verification_pending(d))
            self.assertEqual(advance.call_args.kwargs['verification_run_id'],'native')
        with patch.object(worker,'advance',return_value=dict(verification_deferred=True)):
            self.assertTrue(worker.verification_pending(d),'归属在队列读取之后变更时不能释放候选')

    def test_fenced_verification_write_keeps_slot_and_notifies_once(self):
        import uuid,urllib.error
        identity=str(uuid.uuid4());token='worker-token';commit='a'*40
        lease=dict(delivery_id=identity,worker_token=token,source_commit=commit,candidate_id='candidate',name='verify-1',generation=2)
        state={'runs':0,'phase':'testing'}
        def row():
            attempt={} if state['runs']==0 else dict(id='native',settled=True,status='failed')
            return dict(id=identity,phase=state['phase'],revision=4,source_commit=commit,worker_token=token,candidate_id='candidate',acceptance_submitted=False,evidence={},verification_run=attempt)
        calls=[]
        def api(path,body=None,method=None):
            if path.startswith('delivery-queue?current='):return [row()]
            if path.startswith('delivery-queue/slots?'):return dict(lease)
            if path=='delivery-queue/slots/advance':
                calls.append(('slot',body['action']))
                return dict(lease) if body['action']=='resume' else {}
            if path=='delivery-queue/advance':
                calls.append(('advance',body.get('action')))
                # 服务端围栏：该执行不是当前触发评论的最新运行，拒绝这次失败回填。
                if body.get('action')=='fail':raise urllib.error.HTTPError('fixture',409,'conflict',{},None)
                return {}
            raise AssertionError(path)
        def sleep(seconds):
            # 第一轮还没有执行，第二轮原生执行结束但被围栏拦下；之后交付自行离开 testing 阶段。
            if ('advance','fail') in calls:state['phase']='needs_changes'
            else:state['runs']=1
        worker=object.__new__(w.Worker);worker.env='prod'
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(r,'lock'),patch.object(r,'candidate',return_value=dict(id='candidate',delivery_id=identity,status='verifying',commit=commit)),patch.object(r,'check_process_ownership'),patch.object(r,'validate_candidate'),patch.object(r,'candidate_lock'),patch.object(r,'save_candidate'),patch.object(r,'drain'),patch.object(r,'release_candidate') as release,patch.object(worker,'api',side_effect=api),patch.object(w.time,'sleep',side_effect=sleep):
            job=worker.pool_job_path(identity);job.parent.mkdir(parents=True);job.write_text(json.dumps(dict(stage='verifying',delivery_id=identity,lease=lease)))
            meta=r.meta_path('verify-1');meta.parent.mkdir(parents=True);meta.write_text(json.dumps(dict(environment_id='verify-1',frontend_url='http://fixture')))
            worker.pool_delivery(identity)
            self.assertEqual([call for call in calls if call[0]=='slot' and call[1]=='quarantine'],[],'围栏不是槽位故障')
            self.assertEqual([call for call in calls if call[0]=='advance'],[('advance','notify'),('advance','fail')],'通知只发一次，失败回填只尝试一次')
            self.assertEqual([call for call in calls if call[0]=='slot'],[('slot','resume'),('slot','drain'),('slot','release')],'保留租约并在交付离开验收阶段后正常释放')
            self.assertEqual(json.loads(job.read_text())['stage'],'released')
            release.assert_called_once_with('candidate',env='verify-1')

    def pool_verification_fixture(self, states, error_action=None, error_code=409, defer_first=False, artifact_error=False, resume=False, slot_error=None, slot_error_code=409):
        import uuid,urllib.error
        identity=str(uuid.uuid4());commit='a'*40
        lease=dict(delivery_id=identity,worker_token='worker-token',source_commit=commit,candidate_id='candidate',name='verify-1',generation=2)
        state={'step':0,'notify':0,'errors':0};calls=[]
        def row():
            d=dict(id=identity,phase='testing',revision=4,source_commit=commit,worker_token='worker-token',candidate_id='candidate',acceptance_submitted=False,evidence={})
            d.update(states[min(state['step'],len(states)-1)]);return d
        def api(path,body=None,method=None):
            if path.startswith('delivery-queue?current='):return [row()]
            if path.startswith('delivery-queue/slots?'):return dict(lease)
            if path=='delivery-queue/slots/advance':
                calls.append(('slot',body['action']))
                if body['action']==slot_error:raise urllib.error.HTTPError('fixture',slot_error_code,'lease response',{},None)
                return dict(lease) if body['action']=='resume' else {}
            if path=='delivery-queue/advance':
                action=body['action'];calls.append(('advance',action))
                if action==error_action and state['errors']==0:
                    state['errors']+=1;raise urllib.error.HTTPError('fixture',error_code,'controlled response',{},None)
                if action=='notify':
                    state['notify']+=1
                    return dict(verification_deferred=True) if defer_first and state['notify']==1 else {}
                return {}
            raise AssertionError(path)
        def sleep(seconds):
            state['step']+=1
            if state['step']>len(states)+2:raise AssertionError('验收未正常收敛')
        worker=object.__new__(w.Worker);worker.env='prod'
        original_thread=w.threading.Thread
        class ImmediateHeartbeat:
            def __init__(self,target,**kwargs):self.target=target
            def start(self):
                with patch.object(w.threading.Event,'wait',return_value=False):self.target()
            def join(self,**kwargs):pass
        with patch.object(w.threading,'Thread',ImmediateHeartbeat if slot_error=='heartbeat' else original_thread),tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(r,'lock'),patch.object(r,'candidate',return_value=dict(id='candidate',delivery_id=identity,status='verifying',commit=commit)),patch.object(r,'check_process_ownership'),patch.object(r,'validate_candidate'),patch.object(r,'candidate_lock'),patch.object(r,'save_candidate'),patch.object(r,'drain'),patch.object(r,'accept',side_effect=RuntimeError('artifact failed') if artifact_error else None) as accept,patch.object(r,'release_candidate') as release,patch.object(worker,'api',side_effect=api),patch.object(w.time,'sleep',side_effect=sleep):
            job=worker.pool_job_path(identity);job.parent.mkdir(parents=True);job.write_text(json.dumps(dict(stage='verifying',delivery_id=identity,lease=lease)))
            meta=r.meta_path('verify-1');meta.parent.mkdir(parents=True);meta.write_text(json.dumps(dict(environment_id='verify-1',frontend_url='http://fixture')))
            error=None
            try:
                worker.pool_delivery(identity)
                previous=json.loads(job.read_text())
                if resume:
                    state['step']=1;worker.pool_delivery(identity)
            except Exception as e:error=e
            result=json.loads(job.read_text())
            if resume:result['before_resume']=previous
            return calls,result,accept.call_count,release.call_count,error

    def test_submitted_acceptance_precedes_stale_terminal_run(self):
        calls,job,accepted,released,error=self.pool_verification_fixture([dict(acceptance_submitted=True,verification_run=dict(id='old',settled=True,status='failed'))])
        self.assertIsNone(error);self.assertEqual(accepted,1);self.assertEqual(released,1)
        self.assertEqual([x for x in calls if x[0]=='advance'],[('advance','accepted')])
        self.assertEqual(job['stage'],'released')

    def test_external_owner_release_notifies_once_without_attempt(self):
        external=dict(verification_owner=dict(external=True))
        calls,job,_,_,error=self.pool_verification_fixture([external,external,{},{},{},dict(phase='awaiting_review')])
        self.assertIsNone(error);self.assertEqual([x for x in calls if x[0]=='advance'],[('advance','notify')]);self.assertEqual(job['stage'],'released')

    def test_deferred_notification_can_resume_after_owner_expiry(self):
        calls,job,_,_,error=self.pool_verification_fixture([{},dict(verification_owner=dict(external=True)),{},{},dict(phase='awaiting_review')],defer_first=True)
        self.assertIsNone(error);self.assertEqual([x for x in calls if x[0]=='advance'],[('advance','notify'),('advance','notify')]);self.assertEqual(job['stage'],'released')

    def test_existing_attempt_does_not_resend_notice(self):
        running=dict(verification_run=dict(id='native',status='running',settled=False))
        calls,job,_,_,error=self.pool_verification_fixture([running,running,dict(phase='awaiting_review')])
        self.assertIsNone(error);self.assertEqual([x for x in calls if x[0]=='advance'],[]);self.assertEqual(job['stage'],'released')

    def test_prepared_and_accepted_conflict_keep_candidate_for_resume(self):
        for action in ('prepared','accepted'):
            with self.subTest(action=action):
                overrides=dict(candidate_id='other') if action=='prepared' else dict(acceptance_submitted=True)
                calls,job,_,released,error=self.pool_verification_fixture([overrides],error_action=action)
                self.assertIsNone(error);self.assertEqual(job['stage'],'fenced');self.assertEqual(job['lease']['candidate_id'],'candidate');self.assertEqual(released,0)
                self.assertNotIn(('slot','quarantine'),calls);self.assertNotIn(('advance','fail'),calls)

    def test_notification_conflict_recovers_without_failure(self):
        calls,job,accepted,released,error=self.pool_verification_fixture([{},dict(acceptance_submitted=True)],error_action='notify')
        self.assertIsNone(error);self.assertEqual((accepted,released),(1,1));self.assertEqual(job['stage'],'released')
        self.assertNotIn(('slot','quarantine'),calls);self.assertNotIn(('advance','fail'),calls)

    def test_fenced_candidate_resumes_same_lease(self):
        for action in ('prepared','accepted'):
            with self.subTest(action=action):
                initial=dict(candidate_id='other') if action=='prepared' else dict(acceptance_submitted=True)
                calls,job,_,released,error=self.pool_verification_fixture([initial,dict(acceptance_submitted=True)],error_action=action,resume=True)
                self.assertIsNone(error);self.assertEqual(job['before_resume']['stage'],'fenced');self.assertEqual(job['stage'],'released');self.assertEqual(released,1)
                self.assertEqual([x for x in calls if x==('slot','resume')],[('slot','resume'),('slot','resume')])
                self.assertNotIn(('slot','quarantine'),calls);self.assertNotIn(('advance','fail'),calls)

    def test_real_http_api_preserves_conflict_status(self):
        import http.server,threading,urllib.error
        received=[]
        class Response(http.server.BaseHTTPRequestHandler):
            def do_POST(self):
                received.append((self.path,json.loads(self.rfile.read(int(self.headers['Content-Length'])))))
                self.send_response(409);self.end_headers();self.wfile.write(b'{"error":"verification run is not current; token=test-token"}')
            def log_message(self,*args):pass
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),http.server.HTTPServer(('127.0.0.1',0),Response) as server:
            thread=threading.Thread(target=server.handle_request);thread.start()
            worker=object.__new__(w.Worker);worker.env='int';worker.meta=dict(backend_url='http://127.0.0.1:'+str(server.server_port));worker.profile=dict(token='test-token',workspace_id='test-workspace')
            try:
                with self.assertRaises(w.urllib.error.HTTPError) as error:
                    worker.advance(dict(id='delivery',revision=4,source_commit='a'*40),'fail',verification_run_id='old-native')
                self.assertEqual(error.exception.code,409)
                diagnostic=json.loads(next((Path(folder)/'publisher').glob('http-failure-*.json')).read_text())
                self.assertNotIn('test-token',json.dumps(diagnostic))
                self.assertNotIn('test-token',str(error.exception))
                self.assertEqual(diagnostic['status'],409)
                self.assertEqual(diagnostic['failure_stage'],'api')
                self.assertIn(b'verification run',error.exception.read())
                self.assertEqual(received[0][0],'/api/delivery-queue/advance');self.assertEqual(received[0][1]['verification_run_id'],'old-native')
            finally:thread.join(timeout=2)

    def test_slot_and_heartbeat_conflicts_cannot_fail_latest_owner(self):
        for action in ('resume','heartbeat'):
            with self.subTest(action=action):
                calls,job,_,released,error=self.pool_verification_fixture([{}],slot_error=action)
                self.assertIsNone(error);self.assertEqual(job['stage'],'fenced');self.assertEqual(released,0)
                self.assertNotIn(('slot','quarantine'),calls);self.assertNotIn(('advance','fail'),calls)
                calls,job,_,released,error=self.pool_verification_fixture([{}],slot_error=action,slot_error_code=500)
                self.assertIsNotNone(error);self.assertEqual(job['stage'],'quarantined');self.assertIn(('slot','quarantine'),calls);self.assertIn(('advance','fail'),calls)

    def test_real_server_and_artifact_errors_remain_failures(self):
        for artifact in (False,True):
            with self.subTest(artifact=artifact):
                calls,job,_,released,error=self.pool_verification_fixture([dict(acceptance_submitted=artifact)],error_action=None if artifact else 'notify',error_code=500,artifact_error=artifact)
                self.assertIsNotNone(error);self.assertEqual(job['stage'],'quarantined');self.assertEqual(released,0)
                self.assertIn(('slot','quarantine'),calls);self.assertIn(('advance','fail'),calls)

    def test_serial_verification_and_integration_use_owner_protocol(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        for phase in ('testing','preparing_release'):
            d=dict(id='external',phase=phase,candidate_id='candidate',acceptance_submitted=False,source_commit='a'*40,verification_owner=dict(external=True))
            with tempfile.TemporaryDirectory() as folder,patch.object(r,'recover_release_windows'),patch.object(r,'lock'),patch.object(r,'candidate_path',return_value=Path(folder)/'missing'),patch.object(worker,'api',return_value=[d]),patch.object(worker,'recover_finished_publications'),patch.object(worker,'pool_enabled',return_value=False),patch.object(worker,'pool_dispatch',return_value=set()),patch.object(worker,'batch_config',return_value=False),patch.object(worker,'pin'),patch.object(worker,'publication_path',return_value=Path(folder)/'missing'),patch.object(worker,'publication'),patch.object(worker,'own_candidate',return_value={'id':'candidate'}),patch.object(worker,'advance') as advance:
                worker.tick()
                advance.assert_not_called()

    def test_serial_conflict_does_not_repeat_unbound_failure(self):
        import urllib.error
        worker=object.__new__(w.Worker);worker.env='prod'
        for phase in ('testing','preparing_release'):
            d=dict(id='delivery',phase=phase,candidate_id='candidate',acceptance_submitted=False,source_commit='a'*40,verification_run=dict(id='old-native',status='failed',settled=True))
            with tempfile.TemporaryDirectory() as folder,patch.object(r,'recover_release_windows'),patch.object(r,'lock'),patch.object(r,'candidate_path',return_value=Path(folder)/'missing'),patch.object(worker,'api',return_value=[d]),patch.object(worker,'recover_finished_publications'),patch.object(worker,'pool_enabled',return_value=False),patch.object(worker,'pool_dispatch',return_value=set()),patch.object(worker,'batch_config',return_value=False),patch.object(worker,'pin'),patch.object(worker,'publication_path',return_value=Path(folder)/'missing'),patch.object(worker,'publication'),patch.object(worker,'own_candidate',return_value={'id':'candidate'}),patch.object(worker,'advance',side_effect=urllib.error.HTTPError('fixture',409,'fenced',{},None)) as advance,patch.object(r,'release_candidate') as release:
                worker.tick();self.assertEqual(advance.call_count,1);self.assertEqual(advance.call_args.kwargs['verification_run_id'],'old-native');release.assert_not_called()

    def test_deferred_published_notice_does_not_starve_other_work(self):
        worker=w.Worker.__new__(w.Worker);worker.env='prod'
        rows=[dict(id='deferred',phase='published'),dict(id='next',phase='needs_changes')]
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'recover_release_windows'),patch.object(r,'lock'),patch.object(r,'candidate_path',return_value=Path(folder)/'missing'),patch.object(worker,'api',return_value=rows),patch.object(worker,'recover_finished_publications'),patch.object(worker,'pool_enabled',return_value=False),patch.object(worker,'pool_dispatch',return_value=set()),patch.object(worker,'batch_config',return_value=False),patch.object(worker,'pin'),patch.object(worker,'advance',side_effect=[{'notification_deferred':True},{}]) as advance:
            worker.tick()
            self.assertEqual([call.args[0]['id'] for call in advance.call_args_list],['deferred','next'])

    def test_install_bundles_exact_database_checker(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);units=root/'units';units.mkdir()
            def paths(value):
                return units if str(value)=='/etc/systemd/system' else Path(value)
            with patch.object(w,'Path',side_effect=paths),patch.object(r,'RUN',root),patch.object(r,'require_control_directory'),patch.object(r,'run') as run:
                w.install('prod')
            bundles=list((root/'publisher').glob('delivery-prod-*'))
            self.assertEqual(len(bundles),1)
            self.assertEqual((bundles[0]/'check-release-database.mjs').read_bytes(),Path(w.__file__).with_name('check-release-database.mjs').read_bytes())
            self.assertIn(str(bundles[0]/'issue-delivery-worker.py'),(units/'multica-delivery-prod.service').read_text())
            self.assertTrue(run.called)
    def git(self,root,*args):return subprocess.check_output(['git','-C',str(root),*args],stderr=subprocess.DEVNULL,text=True).strip()
    def test_merge_distinguishes_real_conflicts_from_environment_errors(self):
        for conflict in (False,True):
            with self.subTest(conflict=conflict),tempfile.TemporaryDirectory() as folder:
                root=Path(folder);self.git(root,'init');self.git(root,'config','user.name','Fixture');self.git(root,'config','user.email','fixture@example.test')
                (root/'common').write_text('base');self.git(root,'add','.');self.git(root,'commit','-qm','base');base=self.git(root,'rev-parse','HEAD')
                self.git(root,'checkout','-qb','side');(root/('common' if conflict else 'side')).write_text('side');self.git(root,'add','.');self.git(root,'commit','-qm','side')
                self.git(root,'checkout','-qb','main',base);(root/('common' if conflict else 'main')).write_text('main');self.git(root,'add','.');self.git(root,'commit','-qm','main')
                self.assertEqual(w.merge_source(root,['--no-ff','--no-edit','side']),not conflict)
                self.assertEqual(self.git(root,'status','--porcelain'),'')
                self.assertNotEqual(subprocess.run(['git','rev-parse','--verify','--quiet','MERGE_HEAD'],cwd=root,stdout=subprocess.DEVNULL).returncode,0)
                if not conflict:self.assertEqual(len(self.git(root,'show','-s','--format=%P','HEAD').split()),2)
                with patch.object(w.subprocess,'run',wraps=subprocess.run) as commands:
                    with self.assertRaisesRegex(RuntimeError,'missing-ref'):w.merge_source(root,['--no-edit','missing-ref'])
                    self.assertNotIn(['git','merge','--abort'],[list(c.args[0]) for c in commands.call_args_list])

    def test_merge_cleanup_error_preserves_original_failure(self):
        results=[subprocess.CompletedProcess([],1,'','ORIGINAL_GIT_FAILURE'),subprocess.CompletedProcess([],0,'conflicted-file',''),subprocess.CompletedProcess([],0,'merge-head',''),subprocess.CompletedProcess([],1,'','CLEANUP_FAILURE')]
        with patch.object(w.subprocess,'run',side_effect=results):
            with self.assertRaises(r.RecoveryRequired) as error:w.merge_source(Path('/controlled'),['target'])
        self.assertIn('ORIGINAL_GIT_FAILURE',str(error.exception));self.assertIn('CLEANUP_FAILURE',str(error.exception))

    def test_nonconflict_failure_with_merge_head_is_cleaned_but_not_mislabeled(self):
        results=[subprocess.CompletedProcess([],1,'','HOOK_REJECTED'),subprocess.CompletedProcess([],0,'',''),subprocess.CompletedProcess([],0,'merge-head',''),subprocess.CompletedProcess([],0,'','')]
        with patch.object(w.subprocess,'run',side_effect=results):
            with self.assertRaisesRegex(RuntimeError,'HOOK_REJECTED'):w.merge_source(Path('/controlled'),['target'])

    def test_pinned_source_survives_original_worktree_removal_and_branch_changes(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder)/'repo';root.mkdir();self.git(root,'init');self.git(root,'config','user.name','Fixture');self.git(root,'config','user.email','fixture@example.test')
            (root/'file').write_text('original');self.git(root,'add','.');self.git(root,'commit','-qm','base');sha=self.git(root,'rev-parse','HEAD')
            d=dict(id='test-delivery',source_commit=sha,source_branch='feature/example')
            worker=object.__new__(w.Worker)
            with patch.object(r,'ROOT',root),patch.object(r,'RUN',root/'.run'):
                path=worker.source(d)
                self.assertEqual((path/'file').read_text(),'original')
                self.git(root,'worktree','remove',str(path))
                (root/'file').write_text('later');self.git(root,'commit','-am','later','-q')
                restored=worker.source(d)
                self.assertEqual(self.git(restored,'rev-parse','HEAD'),sha)
                self.assertEqual((restored/'file').read_text(),'original')
                self.assertEqual(self.git(root,'rev-parse','refs/multica/deliveries/test-delivery'),sha)
    def test_busy_int_keeps_delivery_queued_without_launching_or_rerunning_agent(self):
        worker=object.__new__(w.Worker);worker.env='prod';d={'id':'one','phase':'waiting_int'}
        with patch.object(worker,'api',return_value=[d]),patch.object(worker,'pin'),patch.object(worker,'busy',return_value=True),patch.object(worker,'advance') as advance,patch.object(r,'candidate_path',return_value=Path('/does-not-exist')),tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            worker.tick();advance.assert_not_called()
    def test_int_worker_cannot_publish_production(self):
        worker=object.__new__(w.Worker);worker.env='int'
        with patch.object(r,'request_release') as publish:
            with self.assertRaisesRegex(RuntimeError,'不能更新生产'):worker.publish({})
            publish.assert_not_called()
    def test_invalid_authorization_stops_before_merge_and_push(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        candidate={'status':'accepted','source':'/fixture','commit':'a'*40,'id':'candidate'}
        with patch.object(worker,'own_candidate',return_value=candidate),patch.object(r,'run') as command,patch.object(r,'output',side_effect=['','personal-studio-upstream']),patch.object(subprocess,'run') as ancestry,patch.object(worker,'advance',side_effect=RuntimeError('authorization changed')),patch.object(r,'request_release') as request,patch.object(worker,'publication'):
            ancestry.return_value.returncode=0
            with self.assertRaisesRegex(RuntimeError,'authorization changed'):worker.publish({'id':'delivery'})
            self.assertEqual([call.args[0][1] for call in command.call_args_list],['fetch'])
            request.assert_not_called()

    def test_publishing_recovery_rechecks_then_finishes_git_before_request(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        candidate={'status':'accepted','source':'/fixture','commit':'a'*40,'id':'candidate'}
        events=[]
        with patch.object(worker,'own_candidate',return_value=candidate),patch.object(r,'run',side_effect=lambda args,**kw:events.append(args[1])),patch.object(r,'output',side_effect=['a'*40+' refs/heads/personal-studio-upstream']),patch.object(subprocess,'run') as ancestry,patch.object(worker,'advance') as advance,patch.object(r,'check_delivery_approval',side_effect=lambda c:events.append('authorization')),patch.object(r,'request_release',side_effect=lambda c:events.append('request')),patch.object(worker,'publication'):
            ancestry.return_value.returncode=0
            worker.publish({'id':'delivery','phase':'publishing'})
            self.assertEqual(events,['fetch','authorization','push','request'])
            advance.assert_not_called()

    def test_normal_request_requires_human_approved_delivery(self):
        with self.assertRaisesRegex(RuntimeError,'交付审核'):r.check_delivery_approval({'commit':'a'*40})
    def test_schema_change_rejected_before_mutation_and_stop(self):
        # 用例断言的是严格模式的通用拒绝原因；控制目录若开启 personal 策略会先报 down 迁移缺失。
        os.environ['MULTICA_MIGRATION_POLICY']='strict'
        self.addCleanup(os.environ.pop,'MULTICA_MIGRATION_POLICY',None)
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);old=root/'old';new=root/'new';(old/'server/migrations').mkdir(parents=True);(new/'server/migrations').mkdir(parents=True)
            (new/'server/migrations/999_bad.up.sql').write_text('ALTER TABLE issue DROP COLUMN title;')
            with patch.object(r,'migrate') as migrate,patch.object(r,'check_database') as check:
                with self.assertRaisesRegex(RuntimeError,'兼容方案'):r.prepare_database(new,{},old)
                migrate.assert_not_called();check.assert_not_called()
    def test_additive_migration_checks_old_and_new_queries(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);old=root/'old';new=root/'new';(old/'server/migrations').mkdir(parents=True);(new/'server/migrations').mkdir(parents=True)
            (new/'server/migrations/999_safe.up.sql').write_text('CREATE TABLE delivery_fixture(id uuid);')
            with patch.object(r,'migrate') as migrate,patch.object(r,'check_database') as check:
                r.prepare_database(new,{'identity':'int'},old)
                self.assertEqual(check.call_args_list[0].args[0],old);self.assertEqual(check.call_args_list[1].args[0],new);migrate.assert_called_once()

    def test_review_state_releases_previous_slot_after_process_restart(self):
        import uuid
        identity=str(uuid.uuid4());d={'id':identity,'phase':'awaiting_review','revision':2,'source_commit':'a'*40}
        current={'id':'candidate','delivery_id':identity,'status':'accepted'}
        def api(path,body=None,method=None):
            # 队列列表返回待复核交付；推进回执按普通对象处理，验证进程重启后仍会释放旧槽位。
            return {} if path.endswith('/advance') else [d]
        worker=object.__new__(w.Worker);worker.env='prod'
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(r,'candidate_path',return_value=Path(__file__)),patch.object(r,'candidate',return_value=current),patch.object(worker,'api',side_effect=api),patch.object(worker,'pin'),patch.object(r,'release_candidate') as release:
            worker.tick();release.assert_called_once_with('candidate')
    def test_restart_finishes_lease_even_after_candidate_was_released(self):
        import uuid
        identity=str(uuid.uuid4());d=dict(id=identity,phase='cancelled')
        worker=object.__new__(w.Worker)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            path=worker.publication_path(d);path.parent.mkdir();path.write_text('{}')
            with patch.object(worker,'api',return_value=[d]) as api,patch.object(worker,'own_candidate',return_value={'id':'candidate','status':'released'}),patch.object(r,'request_path',return_value=Path(folder)/'missing'),patch.object(r,'read',return_value={}),patch.object(r,'check_process_ownership'),patch.object(worker,'finish_publication') as finish:
                worker.recover_finished_publications([])
                api.assert_called_once_with('delivery-queue?current='+identity)
                finish.assert_called_once_with(d)
    def test_restart_keeps_lease_while_publication_is_still_running(self):
        import uuid
        identity=str(uuid.uuid4());d=dict(id=identity,phase='published')
        worker=object.__new__(w.Worker)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            path=worker.publication_path(d);path.parent.mkdir();path.write_text('{}')
            request=Path(folder)/'request';request.write_text('{}')
            with patch.object(worker,'own_candidate',return_value={'id':'candidate','status':'succeeded'}),patch.object(r,'request_path',return_value=request),patch.object(r,'read',return_value={'status':'publishing'}),patch.object(worker,'finish_publication') as finish:
                with self.assertRaises(r.RecoveryRequired):worker.recover_finished_publications([d])
                finish.assert_not_called()
    def test_release_response_loss_only_removes_already_released_local_lease(self):
        import uuid
        d=dict(id=str(uuid.uuid4()))
        worker=object.__new__(w.Worker)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            path=worker.publication_path(d);path.parent.mkdir();path.write_text(json.dumps({'generation':3}))
            with patch.object(worker,'api',return_value={'state':'idle','generation':3}),patch.object(worker,'publication') as publication:
                worker.finish_publication(d)
                self.assertFalse(path.exists());publication.assert_not_called()
    def test_claim_response_loss_reuses_current_generation_with_original_credentials(self):
        import uuid,urllib.error
        d=dict(id=str(uuid.uuid4()),worker_token=str(uuid.uuid4()),source_commit='a'*40)
        worker=object.__new__(w.Worker)
        current=dict(delivery_id=d['id'],generation=7,expired=False)
        receipt=dict(delivery_id=d['id'],worker_token=d['worker_token'],generation=7)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(worker,'api',side_effect=[urllib.error.HTTPError('fixture',409,'conflict',{},None),current,receipt]) as api:
            self.assertEqual(worker.publication(d),receipt)
            self.assertEqual(d['publication_generation'],7)
            check=api.call_args.args[1]
            self.assertEqual((check['action'],check['worker_token'],check['generation']),('check',d['worker_token'],7))
    def test_verifying_candidate_is_not_remerged_on_resume(self):
        worker=object.__new__(w.Worker);worker.env='prod';d={'id':'one','source_commit':'a'*40}
        candidate={'id':'candidate','status':'verifying','commit':'a'*40,'source':'/fixture'}
        with patch.object(worker,'busy',return_value=False),patch.object(worker,'own_candidate',return_value=candidate),patch.object(worker,'source') as source,patch.object(worker,'advance'),patch.object(r,'validate_candidate') as validate,patch.object(r,'read',return_value={'frontend_url':'http://int'}),patch.object(worker,'publication',return_value={'generation':1}):
            worker.prepare(d);source.assert_not_called();validate.assert_called_once_with(candidate)

    def test_dead_startup_process_fails_without_waiting_for_http_timeout(self):
        with patch.object(r,'exists',return_value=False),patch.object(r,'http') as http:
            with self.assertRaisesRegex(RuntimeError,'启动进程已退出'):r.ready('http://fixture',pid=123)
            http.assert_not_called()

    def test_coordinator_exit_keeps_application_processes_alive(self):
        files={}
        original=Path.write_text
        def write(path,text,*args,**kwargs):
            if str(path).startswith('/etc/systemd/system/'):
                files[path.name]=text;return len(text)
            return original(path,text,*args,**kwargs)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(r,'require_control_directory'),patch.object(r,'run') as command,patch.object(Path,'write_text',write):
            w.install('int')
            self.assertIn('KillMode=process',files['multica-delivery-int.service'])
            self.assertEqual(files['multica-delivery-int.service'].count('Environment=HOME='),1)
            self.assertIn('OnBootSec=30s',files['multica-delivery-int.timer'])
            self.assertIn('OnUnitInactiveSec=15s',files['multica-delivery-int.timer'])
            self.assertIn('--now',command.call_args.args[0])

if __name__=='__main__':unittest.main()
