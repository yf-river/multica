import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('worker',Path(__file__).with_name('issue-delivery-worker.py'))
w=importlib.util.module_from_spec(spec);spec.loader.exec_module(w);r=w.r

class PoolFlowTests(unittest.TestCase):
    def run_flow(self, *, failed=False, resume=False,busy=False,project=False):
        identity='00000000-0000-0000-0000-000000000001'
        d=dict(id=identity,phase='testing',source_commit='a'*40,worker_token='token',candidate_id='candidate' if resume else '',acceptance_submitted=False,evidence=[dict(name='flow',status='passed',evidence='fixture')])
        if project:
            d['project_id']='project-a'
            d['source_work_dir']='/fixture/task-worktree'
            d['final_review']=dict(state='passed',head_sha=d['source_commit'])
        lease=dict(name='verify-1',generation=1,delivery_id=identity,worker_token='token',source_commit=d['source_commit'],candidate_id='candidate' if resume else '',state='verifying' if resume else 'preparing')
        candidate=dict(id='candidate',status='verifying',environment='verify-1',delivery_id=identity)
        events=[]
        worker=object.__new__(w.Worker);worker.env='prod'
        def api(path,body=None,method=None):
            if path=='release-agent/settings':return dict(configured=False,enabled=False)
            if path.startswith('delivery-queue?'):return [dict(d)]
            if path.startswith('delivery-queue/slots?'):return dict(lease)
            if path=='delivery-queue/slots/advance':
                action=body['action'];events.append(action)
                if action=='prepared':lease.update(candidate_id=body['candidate_id'],state='verifying')
                if action=='resume':lease['generation']+=1
                return dict(lease)
            raise AssertionError(path)
        def advance(current,action,**extra):
            events.append('delivery:'+action)
            d.update(extra)
            if action=='notify':
                if failed:d['verification_run']=dict(id='00000000-0000-0000-0000-000000000002',status='failed',settled=True,error='model capacity')
                else:d['acceptance_submitted']=True
            if action=='accepted':d['phase']='awaiting_review'
            if action=='fail':d['phase']='needs_changes'
            current.update(d)
            return current
        with tempfile.TemporaryDirectory() as tmp,patch.object(r,'RUN',Path(tmp)):
            if project:r.write(Path(tmp)/'delivery-batch.json',dict(enabled=True,controller_environment='prod',project_id='project-a'))
            artifact=Path(tmp)/'artifact';artifact.mkdir()
            source=Path(tmp)/'source';source.mkdir()
            if resume:r.write(worker.pool_job_path(identity),dict(stage='verifying',delivery_id=identity,lease=lease,candidate_id='candidate'))
            with patch.object(worker,'api',side_effect=api),patch.object(worker,'advance',side_effect=advance),patch.object(worker,'source',return_value=source) as checkout,patch.object(r,'build_verification',side_effect=lambda *a,**k:events.append('build') or artifact) as build,patch.object(r,'prepare_verification',return_value=candidate) as prepare,patch.object(r,'read',wraps=r.read) as read,patch.object(r,'accept',side_effect=lambda *a,**k:events.append('accept')),patch.object(r,'drain',side_effect=lambda *a:events.append('stop')),patch.object(r,'release_candidate',side_effect=lambda *a,**k:events.append('release_candidate')),patch.object(r,'candidate',return_value=candidate),patch.object(r,'check_process_ownership'),patch.object(r,'validate_candidate'),patch.object(r,'save_candidate'),patch.object(worker,'require_sql_seed'),patch.object(w.seed,'clear_slot',side_effect=lambda *a:events.append('clear')),patch.object(worker,'ensure_seed',side_effect=lambda claim:claim()),patch.object(worker,'seed_matches',return_value=True):
                r.write(r.meta_path('verify-1'),dict(frontend_url='http://fixture'))
                if busy:
                    with patch.object(worker,'ensure_seed',side_effect=w.Busy('三个槽位均在验收')):
                        worker.pool_delivery(identity)
                    self.assertEqual(d['phase'],'testing')
                    self.assertEqual(r.read(worker.pool_job_path(identity))['stage'],'waiting_slot')
                    self.assertNotIn('delivery:fail',events)
                if resume:
                    with patch.object(worker,'ensure_seed',side_effect=AssertionError('同一候选续接不得恢复数据')):
                        worker.pool_delivery(identity)
                else:
                    worker.pool_delivery(identity)
                if busy:
                    self.assertEqual(build.call_count,1,'D续接重复构建')
                    self.assertEqual(prepare.call_args.args[0],source,'D续接丢失固定源码')
                self.assertEqual(r.read(worker.pool_job_path(identity))['stage'],'released')
                if resume:build.assert_not_called();prepare.assert_not_called()
                elif not busy:
                    self.assertEqual(checkout.call_count,1)
                    self.assertEqual(checkout.call_args.args[0]['source_commit'],d['source_commit'])
                    self.assertEqual(checkout.call_args.kwargs['integrate'],not project)
        self.assertLess(events.index('stop'),events.index('release'))
        self.assertNotIn('quarantine',events)
        return d,events

    def test_build_before_slot_and_accept_before_release(self):
        d,events=self.run_flow()
        self.assertLess(events.index('build'),events.index('claim'))
        self.assertLess(events.index('delivery:accepted'),events.index('release'))
        self.assertEqual(d['phase'],'awaiting_review')

    def test_project_verifies_pinned_task_commit_without_merging_main(self):
        self.run_flow(project=True)

    def test_model_failure_releases_slot_without_acceptance(self):
        d,events=self.run_flow(failed=True)
        self.assertEqual(d['phase'],'needs_changes')
        self.assertNotIn('accept',events)
        self.assertEqual(d['error'],'model capacity')

    def test_fourth_delivery_waits_then_uses_original_build(self):
        d,events=self.run_flow(busy=True)
        self.assertEqual(d['phase'],'awaiting_review')
        self.assertNotIn('delivery:fail',events)

    def test_restart_resumes_candidate_without_rebuild(self):
        d,events=self.run_flow(resume=True)
        self.assertIn('resume',events)
        self.assertEqual(d['phase'],'awaiting_review')

    def test_restart_after_cleanup_does_not_drain_stopped_daemon(self):
        identity='00000000-0000-0000-0000-000000000001'
        worker=object.__new__(w.Worker);worker.env='prod'
        delivery=dict(id=identity,phase='awaiting_review',project_id='project-a',source_commit='a'*40,worker_token='token')
        lease=dict(name='verify-1',generation=1,delivery_id=identity,worker_token='token',source_commit='a'*40,candidate_id='candidate',state='draining')
        candidate=dict(id='candidate',status='accepted',delivery_id=identity)
        actions=[]
        def api(path,body=None,method=None):
            if path=='release-agent/settings':return dict(configured=False,enabled=False)
            if path.startswith('delivery-queue?'):return [dict(delivery)]
            if path.startswith('delivery-queue/slots?'):return dict(lease)
            if path=='delivery-queue/slots/advance':
                actions.append(body['action'])
                if body['action']=='resume':lease['generation']+=1
                return dict(lease)
            raise AssertionError(path)
        with tempfile.TemporaryDirectory() as tmp,patch.object(r,'RUN',Path(tmp)):
            r.write(Path(tmp)/'delivery-batch.json',dict(enabled=True,controller_environment='prod',project_id='project-a'))
            r.write(worker.pool_job_path(identity),dict(stage='draining',lease=dict(lease),candidate_id='candidate'))
            r.write(Path(tmp)/'verification-install/verify-1.json',dict(stage='cleaned',cleanup_delivery=identity,cleanup_generation=1,cleanup_candidate='candidate'))
            r.write(r.meta_path('verify-1'),dict(pids={}))
            with patch.object(worker,'api',side_effect=api),patch.object(worker,'require_sql_seed'),patch.object(r,'candidate',return_value=candidate),patch.object(r,'check_process_ownership'),patch.object(r,'save_candidate'),patch.object(r,'release_candidate'),patch.object(r,'drain') as drain,patch.object(w.dev,'stop') as stop,patch.object(w.seed,'clear_slot') as clear:
                worker.pool_delivery(identity)
            drain.assert_not_called();stop.assert_not_called();clear.assert_called_once()
            self.assertEqual(actions,['resume','drain','release'])
            self.assertEqual(r.read(Path(tmp)/'verification-install/verify-1.json')['cleanup_generation'],2)
            self.assertEqual(r.read(worker.pool_job_path(identity))['stage'],'released')



class SeedGateTests(unittest.TestCase):
    def worker(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        return worker

    def test_baseline_completion_snapshot_excludes_a_new_claim(self):
        worker=self.worker()
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(worker,'ensure_seed_locked'):
            state=dict(slot='idle',complete=False)
            def claim():
                state['slot']='preparing'
                return dict(name='verify-1')
            # 真实文件锁模拟维护器已读空闲快照、尚未写完成标记的交错。
            with r.lock('verification-seed-maintenance'):
                snapshot=dict(state)
                with self.assertRaises(w.Busy):worker.ensure_seed(claim)
                self.assertEqual(snapshot['slot'],'idle')
                self.assertEqual(state['slot'],'idle')
                state['complete']=True
            self.assertEqual(worker.ensure_seed(claim),dict(name='verify-1'))
            self.assertTrue(state['complete'])
            self.assertEqual(state['slot'],'preparing')

    def test_seed_check_and_claim_hold_the_same_real_maintenance_lock(self):
        worker=self.worker()
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            stages=[]
            def check_locked(stage):
                with self.assertRaises(BlockingIOError):
                    with r.lock('verification-seed-maintenance'):pass
                stages.append(stage)
            def claim():
                check_locked('claim')
                return dict(name='verify-1')
            with patch.object(worker,'ensure_seed_locked',side_effect=lambda:check_locked('seed')):
                self.assertEqual(worker.ensure_seed(claim),dict(name='verify-1'))
            self.assertEqual(stages,['seed','claim'])
            with r.lock('verification-seed-maintenance'):pass

    def test_project_requires_matching_verified_sql_seed(self):
        worker=self.worker()
        with patch.object(worker,'current_seed',return_value={'seed_id':'a','dump_sha256':'digest'}),patch.object(w.seed,'read_seed',return_value=(Path('/seed'),{}, {'sha256':'digest'})),patch.object(w.seed,'verified_sql_seed',return_value=None):
            with self.assertRaises(r.RecoveryRequired):worker.require_sql_seed()
        with patch.object(worker,'current_seed',return_value={'seed_id':'a','dump_sha256':'digest'}),patch.object(w.seed,'read_seed',return_value=(Path('/seed'),{}, {'sha256':'other'})),patch.object(w.seed,'verified_sql_seed',return_value=Path('/seed/seed.sql')):
            with self.assertRaises(r.RecoveryRequired):worker.require_sql_seed()

    def test_without_current_seed_keeps_previous_behaviour(self):
        worker=self.worker()
        with patch.object(r,'current_seed',return_value=None),patch.object(worker,'api') as api:
            worker.ensure_seed()
            api.assert_not_called()
            self.assertTrue(worker.seed_matches('verify-1'))

    def test_unused_verified_seed_does_not_need_restore(self):
        worker=self.worker()
        slot=dict(name='verify-1',enabled=True,state='idle',seed_id='seed-a',seed_sha256='a'*64,seed_verified=True)
        # 单槽位查询的实际响应是 verified，列表接口才是 seed_verified；两种字段名不能混用。
        record=dict(name='verify-1',seed_id='seed-a',seed_sha256='a'*64,verified=True,restored_at='',updated_at='')
        def api(path,*a,**k):return dict(record) if path.startswith('delivery-queue/slots/seed?') else [slot]
        with patch.object(r,'current_seed',return_value=dict(seed_id='seed-a',dump_sha256='a'*64)),patch.object(worker,'api',side_effect=api),patch.object(w.subprocess,'run') as run:
            worker.ensure_seed()
            run.assert_not_called()
            self.assertTrue(worker.seed_matches('verify-1'))



    def test_concurrent_seed_maintenance_is_normal_wait(self):
        worker=self.worker()
        with patch.object(r,'current_seed',return_value=dict(seed_id='seed-a')),patch.object(r,'lock',side_effect=BlockingIOError),patch.object(worker,'api') as api:
            with self.assertRaises(w.Busy):worker.ensure_seed()
            api.assert_not_called()

    def test_seed_maintenance_conflict_waits_instead_of_failing_delivery(self):
        worker=self.worker()
        slot=dict(name='verify-1',enabled=True,state='idle',seed_verified=False)
        done=type('R',(),{'returncode':1,'stdout':'','stderr':'urllib.error.HTTPError: HTTP Error 409: Conflict'})()
        with tempfile.TemporaryDirectory() as tmp,patch.object(r,'RUN',Path(tmp)),patch.object(r,'current_seed',return_value=dict(seed_id='seed-a')),patch.object(worker,'api',return_value=[slot]),patch.object(w.subprocess,'run',return_value=done):
            with self.assertRaises(w.Busy):worker.ensure_seed()

    def test_released_task_same_seed_is_restored_before_new_claim(self):
        worker=self.worker()
        slot=dict(name='verify-1',enabled=True,state='idle',seed_id='seed-a',seed_sha256='a'*64,seed_verified=False)
        done=type('R',(),{'returncode':0,'stdout':'{}','stderr':''})()
        with patch.object(r,'current_seed',return_value=dict(seed_id='seed-a',dump_sha256='a'*64)),patch.object(worker,'api',return_value=[slot]),patch.object(w.subprocess,'run',return_value=done) as run:
            worker.ensure_seed()
            self.assertEqual(run.call_count,1)
            self.assertIn('ensure',run.call_args.args[0])

    def test_unverified_or_other_seed_slot_is_rejected(self):
        worker=self.worker()
        for record,expected in ((dict(name='verify-1',seed_id='seed-a',seed_sha256='a'*64,verified=False),False),
                                (dict(name='verify-1',seed_id='seed-old',seed_sha256='b'*64,verified=True),False),
                                (dict(name='verify-1',seed_id='seed-a',seed_sha256='a'*64,verified=True),True)):
            with patch.object(r,'current_seed',return_value=dict(seed_id='seed-a',dump_sha256='a'*64)),patch.object(worker,'api',return_value=dict(record)):
                self.assertIs(worker.seed_matches('verify-1'),expected,record)

    def test_repair_accepts_original_verified_seed_after_global_seed_changes(self):
        worker=self.worker()
        original=dict(seed_id='seed-old',seed_sha256='a'*64,verified=True)
        with patch.object(r,'current_seed',return_value=dict(seed_id='seed-new',dump_sha256='b'*64)),patch.object(worker,'api',return_value=original):
            self.assertFalse(worker.seed_matches('verify-1'))
            self.assertTrue(worker.seed_matches('verify-1',original))

    def test_stale_slot_is_restored_before_claim(self):
        worker=self.worker()
        slots=[dict(name='verify-1',enabled=True,state='idle',seed_id='seed-old',seed_sha256='b'*64,seed_verified=True)]
        done=type('R',(),{'returncode':0,'stdout':'{}','stderr':''})()
        with patch.object(r,'current_seed',return_value=dict(seed_id='seed-a',dump_sha256='a'*64)),patch.object(worker,'api',side_effect=lambda path,*a,**k: slots if path=='delivery-queue/slots' else (_ for _ in ()).throw(AssertionError(path))),patch.object(w.subprocess,'run',return_value=done) as run:
            worker.ensure_seed()
            self.assertEqual(run.call_count,1)
            self.assertIn('ensure',run.call_args[0][0])

    def test_busy_or_disabled_slots_are_not_used(self):
        worker=self.worker()
        slots=[dict(name='verify-1',enabled=False,state='idle',seed_id='seed-old',seed_sha256='b'*64,seed_verified=False),
               dict(name='verify-2',enabled=True,state='verifying',seed_id='seed-old',seed_sha256='b'*64,seed_verified=False)]
        with patch.object(r,'current_seed',return_value=dict(seed_id='seed-a',dump_sha256='a'*64)),patch.object(worker,'api',return_value=slots),patch.object(w.subprocess,'run') as run:
            with self.assertRaises(w.Busy):
                worker.ensure_seed()
            run.assert_not_called()

    def test_restore_failure_raises_recovery_required(self):
        worker=self.worker()
        slots=[dict(name='verify-1',enabled=True,state='idle',seed_id='',seed_sha256='',seed_verified=False)]
        failed=type('R',(),{'returncode':1,'stdout':'','stderr':'种子校验失败'})()
        with patch.object(r,'current_seed',return_value=dict(seed_id='seed-a',dump_sha256='a'*64)),patch.object(worker,'api',return_value=slots),patch.object(w.subprocess,'run',return_value=failed):
            with self.assertRaises(r.RecoveryRequired):
                worker.ensure_seed()



class PoolServiceLifetimeTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name)
        self.enterContext(patch.object(r,'ROOT',self.root))
        self.enterContext(patch.object(r,'RUN',self.root/'.run'))
        self.worker=object.__new__(w.Worker);self.worker.env='prod'
        self.identity='00000000-0000-0000-0000-000000000003'
        self.current=self.worker.pool_unit_name(self.identity)
        self.legacy='multica-verify-'+self.identity+'.service'
        self.units={}
        self.enterContext(patch.object(r,'output',side_effect=self.output))

    def unit(self,script=None,active='inactive',**extra):
        script=str(script or Path(w.__file__).resolve())
        return dict(LoadState='loaded',ActiveState=active,MainPID='0',ControlGroup='',
            WorkingDirectory=str(r.ROOT),Type='exec',KillMode='process',
            command=[w.sys.executable,script,'--env','prod','--pool-delivery',self.identity],
            environment=['MULTICA_RELEASE_ROOT='+str(r.ROOT),'HOME='+w.pwd.getpwuid(w.os.getuid()).pw_dir],**extra)

    def output(self,args):
        import json
        if args[:2]==['systemctl','list-units']:return '\n'.join(self.units)
        if args[:2]==['systemctl','show']:
            state=self.units.get(args[2],dict(LoadState='not-found'))
            return '\n'.join(k+'='+v for k,v in state.items() if k not in ('command','environment'))
        if 'GetUnit' in args:return json.dumps({'data':[args[-1]]})
        state=self.units[args[-3]]
        if args[-1]=='ExecStart':return json.dumps({'data':[[w.sys.executable,state['command'],False]]})
        if args[-1]=='Environment':return json.dumps({'data':state['environment']})
        self.fail('unexpected output '+str(args))

    def old_script(self):
        script=r.RUN/'publisher'/'delivery-prod-0123456789abcdef'/'issue-delivery-worker.py'
        script.parent.mkdir(parents=True);script.write_text('# immutable old controller')
        return script

    def test_new_unit_uses_controller_version_and_persistent_services(self):
        with patch.object(r,'run') as launch:
            self.worker.start_pool_unit(self.identity)
        command=launch.call_args.args[0]
        self.assertEqual(command[command.index('--unit')+1],self.current)
        self.assertIn('--property=Type=exec',command)
        self.assertIn('--property=KillMode=process',command)
        self.assertIn('--setenv=HOME='+w.pwd.getpwuid(w.os.getuid()).pw_dir,command)

    def test_same_controller_inactive_restarts_exact_unit(self):
        self.units[self.current]=self.unit()
        with patch.object(r,'run') as launch:
            self.worker.start_pool_unit(self.identity)
        launch.assert_called_once_with(['systemctl','start',self.current])

    def test_upgrade_keeps_legacy_services_and_resumes_in_versioned_unit(self):
        self.units[self.legacy]=self.unit(self.old_script())
        with patch.object(r,'run') as launch,patch.object(self.worker,'check_idle_pool_services') as ownership:
            self.worker.start_pool_unit(self.identity)
        ownership.assert_called_once()
        command=launch.call_args.args[0]
        self.assertEqual(command[0],'systemd-run')
        self.assertEqual(command[command.index('--unit')+1],self.current)
        self.assertNotIn('stop',command)
        self.assertNotIn('reset-failed',command)

    def test_active_old_or_current_controller_is_not_duplicated(self):
        old=self.old_script()
        for unit,script in ((self.legacy,old),(self.current,Path(w.__file__).resolve())):
            for active in ('active','activating','deactivating'):
                self.units={unit:self.unit(script,active)}
                with self.subTest(unit=unit,state=active),patch.object(r,'run') as launch:
                    self.worker.start_pool_unit(self.identity);launch.assert_not_called()

    def test_wrong_parameters_home_or_untrusted_script_are_rejected(self):
        bad=[]
        state=self.unit();state['command'][-1]='wrong-delivery';bad.append(state)
        state=self.unit();state['environment']=['MULTICA_RELEASE_ROOT='+str(r.ROOT)];bad.append(state)
        state=self.unit('/untrusted/issue-delivery-worker.py');bad.append(state)
        for state in bad:
            self.units={self.current:state}
            with self.subTest(state=state),patch.object(r,'run') as launch:
                with self.assertRaises(r.RecoveryRequired):self.worker.start_pool_unit(self.identity)
                launch.assert_not_called()

    def test_idle_unknown_process_blocks_handoff(self):
        self.units[self.legacy]=self.unit(self.old_script())
        with patch.object(self.worker,'check_idle_pool_services',side_effect=r.RecoveryRequired('unknown process')),patch.object(r,'run') as launch:
            with self.assertRaisesRegex(r.RecoveryRequired,'unknown process'):self.worker.start_pool_unit(self.identity)
            launch.assert_not_called()

    def test_dispatch_counts_active_old_version_and_continues_after_bad_delivery(self):
        import io
        active=dict(id=self.identity,phase='testing',created_at='1')
        bad=dict(id='00000000-0000-0000-0000-000000000004',phase='waiting_int',created_at='2')
        next_delivery=dict(id='00000000-0000-0000-0000-000000000005',phase='waiting_int',created_at='3')
        def states(identity):
            if identity==active['id']:return [dict(ActiveState='active')]
            if identity==bad['id']:raise r.RecoveryRequired('identity mismatch')
            return []
        with patch.object(self.worker,'pool_units',side_effect=states),patch.object(self.worker,'start_pool_unit') as launch,patch.object(w.sys,'stderr',new_callable=io.StringIO) as errors:
            self.assertEqual(self.worker.pool_dispatch([active,bad,next_delivery],None),{d['id'] for d in (active,bad,next_delivery)})
            launch.assert_called_once_with(next_delivery['id'])
            self.assertIn(bad['id'],errors.getvalue())
            self.assertIn('identity mismatch',errors.getvalue())

    def test_project_waiting_for_source_or_review_is_reserved_without_dispatch(self):
        base=dict(id=self.identity,phase='waiting_int',project_id='project-a',source_commit='a'*40,
                  source_ready=True,issue_status='in_progress',final_review=dict(state='passed',head_sha='a'*40))
        for changes in (dict(source_ready=False),dict(issue_status='in_review'),
                        dict(final_review=dict(state='pending',head_sha='a'*40)),
                        dict(final_review=dict(state='passed',head_sha='b'*40))):
            with self.subTest(changes=changes),patch.object(self.worker,'batch_project',return_value='project-a'), \
                    patch.object(self.worker,'start_pool_unit') as launch:
                self.assertEqual(self.worker.pool_dispatch([dict(base,**changes)],None),{self.identity})
                launch.assert_not_called()
        with patch.object(self.worker,'batch_project',return_value='project-a'), patch.object(self.worker,'start_pool_unit') as launch:
            self.assertEqual(self.worker.pool_dispatch([base],None,allow_new=False),{self.identity})
            launch.assert_not_called()

    def test_project_quarantined_testing_remains_managed_without_launch(self):
        d=dict(id=self.identity,phase='testing',project_id='project-a')
        r.write(self.worker.pool_job_path(self.identity),dict(delivery_id=self.identity,stage='quarantined',lease={}))
        with patch.object(self.worker,'batch_project',return_value='project-a'), patch.object(self.worker,'start_pool_unit') as launch:
            self.assertEqual(self.worker.pool_dispatch([d],None),{self.identity})
            launch.assert_not_called()

    def test_disabled_admission_reserves_project_testing_including_current(self):
        d=dict(id=self.identity,phase='testing',project_id='project-a')
        other=dict(id='other',phase='testing',project_id='other-project')
        with patch.object(self.worker,'batch_project',return_value='project-a'), patch.object(self.worker,'start_pool_unit') as launch:
            self.assertEqual(self.worker.pool_dispatch([d,other],self.identity,allow_new=False),{self.identity})
            launch.assert_not_called()

    def test_start_failure_is_reported_without_stopping_other_deliveries(self):
        import io
        rows=[dict(id='00000000-0000-0000-0000-00000000000'+str(i),phase='testing',created_at=str(i)) for i in (4,5)]
        def start(identity):
            if identity==rows[0]['id']:raise r.RecoveryRequired('unknown cgroup child')
        with patch.object(self.worker,'pool_units',return_value=[]),patch.object(self.worker,'start_pool_unit',side_effect=start) as launch,patch.object(w.sys,'stderr',new_callable=io.StringIO) as errors:
            self.worker.pool_dispatch(rows,None)
            self.assertEqual([c.args[0] for c in launch.call_args_list],[d['id'] for d in rows])
            self.assertIn('unknown cgroup child',errors.getvalue())

    def test_inactive_controller_with_main_pid_cannot_be_replaced(self):
        self.units[self.current]=self.unit();self.units[self.current]['MainPID']='123'
        with patch.object(r,'run') as launch:
            with self.assertRaisesRegex(r.RecoveryRequired,'仍有控制进程'):self.worker.start_pool_unit(self.identity)
            launch.assert_not_called()

class MigrationGateTests(unittest.TestCase):
    """通用迁移路径只接受新建表和并发索引；普通 CREATE INDEX 不做新表豁免。"""

    def test_table_and_concurrent_index_are_additive(self):
        self.assertTrue(r.additive_statement('CREATE TABLE IF NOT EXISTS seed_a (name text primary key)'))
        self.assertTrue(r.additive_statement('CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS seed_a_key ON seed_a(name)'))

    def test_plain_index_is_rejected_even_for_a_new_table(self):
        self.assertFalse(r.additive_statement('CREATE INDEX IF NOT EXISTS seed_a_key ON seed_a(name)'))
        self.assertFalse(r.additive_statement('CREATE UNIQUE INDEX IF NOT EXISTS seed_a_key ON seed_a(name)'))

if __name__=='__main__':unittest.main()
