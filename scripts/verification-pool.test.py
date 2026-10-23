import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('worker',Path(__file__).with_name('issue-delivery-worker.py'))
w=importlib.util.module_from_spec(spec);spec.loader.exec_module(w);r=w.r

class PoolFlowTests(unittest.TestCase):
    def run_flow(self, *, failed=False, resume=False,busy=False):
        identity='00000000-0000-0000-0000-000000000001'
        d=dict(id=identity,phase='testing',source_commit='a'*40,worker_token='token',candidate_id='candidate' if resume else '',acceptance_submitted=False,evidence=[dict(name='flow',status='passed',evidence='fixture')])
        lease=dict(name='verify-1',generation=1,delivery_id=identity,worker_token='token',source_commit=d['source_commit'],candidate_id='candidate' if resume else '',state='verifying' if resume else 'preparing')
        candidate=dict(id='candidate',status='verifying',environment='verify-1',delivery_id=identity)
        events=[]
        worker=object.__new__(w.Worker);worker.env='prod'
        def api(path,body=None,method=None):
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
                if failed:d['verification_run']=dict(status='failed',settled=True,error='model capacity')
                else:d['acceptance_submitted']=True
            if action=='accepted':d['phase']='awaiting_review'
            if action=='fail':d['phase']='needs_changes'
            current.update(d)
        with tempfile.TemporaryDirectory() as tmp,patch.object(r,'RUN',Path(tmp)):
            artifact=Path(tmp)/'artifact';artifact.mkdir()
            source=Path(tmp)/'source';source.mkdir()
            if resume:r.write(worker.pool_job_path(identity),dict(stage='verifying',delivery_id=identity,lease=lease,candidate_id='candidate'))
            with patch.object(worker,'api',side_effect=api),patch.object(worker,'advance',side_effect=advance),patch.object(worker,'source',return_value=source),patch.object(r,'build_verification',side_effect=lambda *a:events.append('build') or artifact) as build,patch.object(r,'prepare_verification',return_value=candidate) as prepare,patch.object(r,'read',wraps=r.read) as read,patch.object(r,'accept',side_effect=lambda *a,**k:events.append('accept')),patch.object(r,'drain',side_effect=lambda *a:events.append('stop')),patch.object(r,'release_candidate',side_effect=lambda *a,**k:events.append('release_candidate')),patch.object(r,'candidate',return_value=candidate),patch.object(r,'check_process_ownership'),patch.object(r,'validate_candidate'),patch.object(r,'save_candidate'):
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
        self.assertLess(events.index('stop'),events.index('release'))
        self.assertNotIn('quarantine',events)
        return d,events

    def test_build_before_slot_and_accept_before_release(self):
        d,events=self.run_flow()
        self.assertLess(events.index('build'),events.index('claim'))
        self.assertLess(events.index('delivery:accepted'),events.index('release'))
        self.assertEqual(d['phase'],'awaiting_review')

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



class SeedGateTests(unittest.TestCase):
    def worker(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        return worker

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
    def test_dispatch_keeps_persistent_slot_services_after_worker_exit(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        identity='00000000-0000-0000-0000-000000000002'
        delivery=dict(id=identity,phase='waiting_int',created_at='2026-10-23T00:00:00Z')
        inactive=type('R',(),{'returncode':1})()
        with tempfile.TemporaryDirectory() as tmp,patch.object(r,'RUN',Path(tmp)),patch.object(w.subprocess,'run',return_value=inactive),patch.object(r,'run') as launch:
            self.assertEqual(worker.pool_dispatch([delivery],None),{identity})
            command=launch.call_args.args[0]
            self.assertIn('--property=Type=exec',command)
            self.assertIn('--property=KillMode=process',command)
            self.assertNotIn('--property=KillMode=control-group',command)
            self.assertEqual(command[-2:],['--pool-delivery',identity])

class MigrationGateTests(unittest.TestCase):
    """通用迁移路径只接受新建表和并发索引；普通 CREATE INDEX 不做新表豁免。"""

    def test_table_and_concurrent_index_are_additive(self):
        self.assertTrue(r.additive_statement('CREATE TABLE IF NOT EXISTS seed_a (name text primary key)'))
        self.assertTrue(r.additive_statement('CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS seed_a_key ON seed_a(name)'))

    def test_plain_index_is_rejected_even_for_a_new_table(self):
        self.assertFalse(r.additive_statement('CREATE INDEX IF NOT EXISTS seed_a_key ON seed_a(name)'))
        self.assertFalse(r.additive_statement('CREATE UNIQUE INDEX IF NOT EXISTS seed_a_key ON seed_a(name)'))

if __name__=='__main__':unittest.main()
