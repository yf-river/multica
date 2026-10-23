import importlib.util
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('worker',Path(__file__).with_name('issue-delivery-worker.py'))
w=importlib.util.module_from_spec(spec);spec.loader.exec_module(w);r=w.r

class PoolFlowTests(unittest.TestCase):
    def run_flow(self, *, failed=False, resume=False):
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
            if resume:r.write(worker.pool_job_path(identity),dict(stage='verifying',delivery_id=identity,lease=lease,candidate_id='candidate'))
            with patch.object(worker,'api',side_effect=api),patch.object(worker,'advance',side_effect=advance),patch.object(worker,'source',return_value=Path('/fixture')),patch.object(r,'build_verification',side_effect=lambda *a:events.append('build') or Path('/artifact')) as build,patch.object(r,'prepare_verification',return_value=candidate) as prepare,patch.object(r,'read',wraps=r.read) as read,patch.object(r,'accept',side_effect=lambda *a,**k:events.append('accept')),patch.object(r,'drain',side_effect=lambda *a:events.append('stop')),patch.object(r,'release_candidate',side_effect=lambda *a,**k:events.append('release_candidate')),patch.object(r,'candidate',return_value=candidate),patch.object(r,'check_process_ownership'),patch.object(r,'validate_candidate'),patch.object(r,'save_candidate'):
                r.write(r.meta_path('verify-1'),dict(frontend_url='http://fixture'))
                worker.pool_delivery(identity)
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

    def test_verified_current_seed_slot_is_reused(self):
        worker=self.worker()
        slot=dict(name='verify-1',enabled=True,state='idle',seed_id='seed-a',seed_sha256='a'*64,seed_verified=True)
        def api(path,*a,**k):return dict(slot) if path.startswith('delivery-queue/slots/seed?') else [slot]
        with patch.object(r,'current_seed',return_value=dict(seed_id='seed-a',dump_sha256='a'*64)),patch.object(worker,'api',side_effect=api),patch.object(w.subprocess,'run') as run:
            worker.ensure_seed()
            run.assert_not_called()
            self.assertTrue(worker.seed_matches('verify-1'))

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


class MigrationGateTests(unittest.TestCase):
    """新表索引只加新对象；对已有表建普通索引仍然拒绝。"""

    def test_index_on_table_created_in_same_migration(self):
        statements=['CREATE TABLE IF NOT EXISTS seed_a (name text primary key)','CREATE UNIQUE INDEX IF NOT EXISTS seed_a_key ON seed_a(name)']
        self.assertTrue(r.reviewed_new_table_index(Path('/m/623_seed.up.sql'),statements))

    def test_index_on_existing_table_is_rejected(self):
        statements=['CREATE INDEX IF NOT EXISTS issue_idx ON issue(workspace_id)']
        self.assertFalse(r.reviewed_new_table_index(Path('/m/624_issue.up.sql'),statements))

    def test_index_on_another_table_is_rejected(self):
        statements=['CREATE TABLE seed_b (id uuid)','CREATE INDEX IF NOT EXISTS issue_idx ON issue(workspace_id)']
        self.assertFalse(r.reviewed_new_table_index(Path('/m/625_mix.up.sql'),statements))

if __name__=='__main__':unittest.main()
