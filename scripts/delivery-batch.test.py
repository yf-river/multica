import contextlib
import datetime
import http.server
import importlib.util
import json
import os
import subprocess
import tempfile
import threading
import time
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('worker', Path(__file__).with_name('issue-delivery-worker.py'))
w = importlib.util.module_from_spec(spec)
spec.loader.exec_module(w)
r = w.r


def delivery(identity, phase='approved'):
    return dict(id=identity, phase=phase, source_commit='a' * 40, worker_token='t-' + identity, revision=1, created_at='2026-10-23')


class BaselineMaintenanceTests(unittest.TestCase):
    def test_completed_slot_used_by_another_task_is_restored_before_completion(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);commit='a'*40;task_commit='b'*40;candidate='candidate-1';seed_id='prod-'+commit
            archive=root/'accepted-artifacts'/candidate/commit;archive.mkdir(parents=True)
            r.write(archive/'release.json',dict(source_commit=commit,source_digest='digest',files={'fixture':'hash'}))
            seeds=root/'seeds';destination=seeds/seed_id;destination.mkdir(parents=True)
            r.write(root/'published.json',dict(candidate=candidate,commit=commit,source_digest='digest'))
            journal=root/'environment-baseline-maintenance.json'
            r.write(journal,dict(candidate_id=candidate,commit=commit,source_digest='digest',completed_slots=['verify-1','verify-3']))
            slots=[dict(name='verify-1',state='verifying',enabled=True,delivery_id='task-a'),dict(name='verify-2',state='verifying',enabled=True,delivery_id='task-b'),dict(name='verify-3',state='idle',enabled=True,delivery_id=None)]
            for item in slots:
                name=item['name']
                r.write(root/'deployments'/('goal-test-'+name+'.json'),dict(commit=commit))
                r.write(root/'verification-install'/(name+'.json'),dict(stage='cleaned',baseline_cleanup_seed=seed_id))
            worker=object.__new__(w.Worker);worker.api=lambda path:slots
            restored=[]
            def ensure(name,*args):
                restored.append(name)
                r.write(root/'deployments'/('goal-test-'+name+'.json'),dict(commit=commit))
            def clear(name,*args):
                r.write(root/'verification-install'/(name+'.json'),dict(stage='cleaned',baseline_cleanup_seed=seed_id))
            with patch.object(r,'RUN',root),patch.object(w.seed,'SEEDS',seeds),patch.object(r,'lock',return_value=contextlib.nullcontext()),patch.object(r,'checksums',return_value={'fixture':'hash'}),patch.object(worker,'current_seed',return_value=dict(seed_id=seed_id,dump_sha256='seed-hash')),patch.object(w.seed,'read_seed',return_value=(destination,dict(build=dict(baseline_commit=commit)),dict(sha256='seed-hash'))),patch.object(w.seed,'verified_sql_seed',return_value=destination/'seed.sql'),patch.object(w.seed,'ensure',side_effect=ensure),patch.object(w.seed,'clear_idle_baseline',side_effect=clear):
                worker.reconcile_environment_baseline()
                self.assertEqual(r.read(journal)['completed_slots'],['verify-3'])
                self.assertFalse(r.read(journal).get('complete',False))
                # 已维护槽位完成另一个任务，保留了任务程序和旧清理标记。
                slots[0].update(state='idle',delivery_id=None)
                r.write(root/'deployments/goal-test-verify-1.json',dict(commit=task_commit))
                worker.reconcile_environment_baseline()
                self.assertEqual(restored,['verify-1'])
                self.assertEqual(r.read(root/'deployments/goal-test-verify-1.json')['commit'],commit)
                self.assertFalse(r.read(journal).get('complete',False))
                slots[1].update(state='idle',delivery_id=None)
                worker.reconcile_environment_baseline()
                worker.reconcile_environment_baseline()
                self.assertTrue(r.read(journal)['complete'])
                self.assertEqual(r.read(journal)['completed_slots'],list(r.VERIFICATION_ENVS))

    def test_claim_after_idle_snapshot_cannot_interleave_with_baseline_completion(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);commit='a'*40;candidate='candidate-1';seed_id='prod-'+commit
            archive=root/'accepted-artifacts'/candidate/commit;archive.mkdir(parents=True)
            r.write(archive/'release.json',dict(source_commit=commit,source_digest='digest',files={'fixture':'hash'}))
            seeds=root/'seeds';destination=seeds/seed_id;destination.mkdir(parents=True)
            r.write(root/'published.json',dict(candidate=candidate,commit=commit,source_digest='digest'))
            journal=root/'environment-baseline-maintenance.json'
            r.write(journal,dict(candidate_id=candidate,commit=commit,source_digest='digest',completed_slots=list(r.VERIFICATION_ENVS)))
            slots=[dict(name=name,state='idle',enabled=True,delivery_id=None) for name in r.VERIFICATION_ENVS]
            for row in slots:
                r.write(root/'deployments'/('goal-test-'+row['name']+'.json'),dict(commit=commit))
                r.write(root/'verification-install'/(row['name']+'.json'),dict(stage='cleaned',baseline_cleanup_seed=seed_id))
            maintainer=object.__new__(w.Worker);claimant=object.__new__(w.Worker)
            outcomes=[];errors=[]
            def claim():
                outcomes.append(('claimed',r.read(journal).get('complete',False)))
                slots[0].update(state='preparing',delivery_id='task-a')
                return dict(slots[0])
            def attempt():
                try:claimant.ensure_seed(claim)
                except w.Busy:outcomes.append(('busy',False))
                except Exception as error:errors.append(error)
            def snapshot(path):
                self.assertEqual(path,'delivery-queue/slots')
                captured=[dict(row) for row in slots]
                thread=threading.Thread(target=attempt);thread.start();thread.join(timeout=3)
                self.assertFalse(thread.is_alive(),'领取不能阻塞基线收尾')
                return captured
            maintainer.api=snapshot
            # 使用真实 flock 和完整 reconcile；只有种子归档及数据库恢复依赖受控替换。
            with patch.object(r,'RUN',root),patch.object(w.seed,'SEEDS',seeds),patch.object(r,'checksums',return_value={'fixture':'hash'}),patch.object(maintainer,'current_seed',return_value=dict(seed_id=seed_id,dump_sha256='seed-hash')),patch.object(w.seed,'read_seed',return_value=(destination,dict(build=dict(baseline_commit=commit)),dict(sha256='seed-hash'))),patch.object(w.seed,'verified_sql_seed',return_value=destination/'seed.sql'),patch.object(claimant,'ensure_seed_locked'):
                maintainer.reconcile_environment_baseline()
                self.assertEqual(errors,[])
                self.assertEqual(outcomes,[('busy',False)])
                self.assertEqual(slots[0]['state'],'idle')
                self.assertTrue(r.read(journal)['complete'])
                self.assertEqual(claimant.ensure_seed(claim)['state'],'preparing')
                self.assertEqual(outcomes,[('busy',False),('claimed',True)])

    def test_only_idle_slot_is_maintained_and_interrupted_enable_state_is_saved(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);commit='a'*40;candidate='candidate-1';digest='digest';seed_id='prod-'+commit
            archive=root/'accepted-artifacts'/candidate/commit;archive.mkdir(parents=True)
            (archive/'release.json').write_text(json.dumps(dict(source_commit=commit,source_digest=digest,files={'fixture':'hash'})))
            seeds=root/'seeds';destination=seeds/seed_id;destination.mkdir(parents=True)
            r.write(root/'published.json',dict(candidate=candidate,commit=commit,source_digest=digest))
            journal=root/'environment-baseline-maintenance.json';r.write(journal,dict(candidate_id=candidate,commit=commit,source_digest=digest))
            worker=object.__new__(w.Worker)
            slots=[dict(name='verify-1',state='verifying',enabled=True,delivery_id='other'),dict(name='verify-2',state='idle',enabled=True,delivery_id=None),dict(name='verify-3',state='verifying',enabled=True,delivery_id='other-2')]
            worker.api=lambda path:slots
            manifest=dict(build=dict(baseline_commit=commit));artifact=dict(sha256='seed-hash')
            def verify(name,*args,**kwargs):
                saved=r.read(journal)
                self.assertEqual((name,saved['slot'],saved['previous_enabled']),('verify-2','verify-2',True))
            with patch.object(r,'RUN',root),patch.object(w.seed,'SEEDS',seeds),patch.object(r,'lock',return_value=contextlib.nullcontext()),patch.object(r,'checksums',return_value={'fixture':'hash'}),patch.object(worker,'current_seed',return_value={}),patch.object(w.seed,'read_seed',return_value=(destination,manifest,artifact)),patch.object(w.seed,'verified_sql_seed',return_value=destination/'seed.sql'),patch.object(w.seed,'clear_idle_baseline') as clean,patch.object(w.seed,'verify_seed',side_effect=verify) as run:
                worker.reconcile_environment_baseline()
                run.assert_called_once()
                clean.assert_called_once_with('verify-2',seed_id,'seed-hash',root/'verification-postgres.json')
                self.assertNotIn('slot',r.read(journal))

    def test_retry_restores_previous_enabled_state(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);commit='a'*40;candidate='candidate-1';seed_id='prod-'+commit
            archive=root/'accepted-artifacts'/candidate/commit;archive.mkdir(parents=True)
            (archive/'release.json').write_text(json.dumps(dict(source_commit=commit,source_digest='digest',files={'fixture':'hash'})))
            seeds=root/'seeds';destination=seeds/seed_id;destination.mkdir(parents=True)
            r.write(root/'published.json',dict(candidate=candidate,commit=commit,source_digest='digest'))
            journal=root/'environment-baseline-maintenance.json';r.write(journal,dict(candidate_id=candidate,commit=commit,source_digest='digest',slot='verify-2',previous_enabled=True))
            worker=object.__new__(w.Worker)
            slots=[dict(name=name,state='idle',enabled=False,delivery_id=None) for name in r.VERIFICATION_ENVS]
            worker.api=lambda path:slots
            manifest=dict(build=dict(baseline_commit=commit));artifact=dict(sha256='seed-hash')
            with patch.object(r,'RUN',root),patch.object(w.seed,'SEEDS',seeds),patch.object(r,'lock',return_value=contextlib.nullcontext()),patch.object(r,'checksums',return_value={'fixture':'hash'}),patch.object(worker,'current_seed',return_value=dict(seed_id=seed_id,dump_sha256='seed-hash')),patch.object(w.seed,'read_seed',return_value=(destination,manifest,artifact)),patch.object(w.seed,'verified_sql_seed',return_value=destination/'seed.sql'),patch.object(w.seed,'clear_idle_baseline'),patch.object(w.seed,'ensure') as ensure,patch.object(w.seed,'resume') as resume:
                worker.reconcile_environment_baseline()
                ensure.assert_called_once_with('verify-2',root/'verification-postgres.json')
                resume.assert_called_once()
                self.assertNotIn('slot',r.read(journal))

    def test_interrupted_cleanup_resumes_before_any_seed_restore(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);commit='a'*40;candidate='candidate-1';seed_id='prod-'+commit
            archive=root/'accepted-artifacts'/candidate/commit;archive.mkdir(parents=True)
            (archive/'release.json').write_text(json.dumps(dict(source_commit=commit,source_digest='digest',files={'fixture':'hash'})))
            seeds=root/'seeds';destination=seeds/seed_id;destination.mkdir(parents=True)
            r.write(root/'published.json',dict(candidate=candidate,commit=commit,source_digest='digest'))
            journal=root/'environment-baseline-maintenance.json';r.write(journal,dict(candidate_id=candidate,commit=commit,source_digest='digest',slot='verify-2',previous_enabled=False))
            r.write(root/'verification-install/verify-2.json',dict(stage='baseline_cleaning',baseline_cleanup_seed=seed_id))
            worker=object.__new__(w.Worker);worker.api=lambda path:[dict(name=name,state='idle',enabled=False,delivery_id=None) for name in r.VERIFICATION_ENVS]
            with patch.object(r,'RUN',root),patch.object(w.seed,'SEEDS',seeds),patch.object(r,'lock',return_value=contextlib.nullcontext()),patch.object(r,'checksums',return_value={'fixture':'hash'}),patch.object(worker,'current_seed',return_value=dict(seed_id=seed_id,dump_sha256='seed-hash')),patch.object(w.seed,'read_seed',return_value=(destination,dict(build=dict(baseline_commit=commit)),dict(sha256='seed-hash'))),patch.object(w.seed,'verified_sql_seed',return_value=destination/'seed.sql'),patch.object(w.seed,'clear_idle_baseline') as clean,patch.object(w.seed,'ensure') as ensure:
                worker.reconcile_environment_baseline()
                ensure.assert_not_called();clean.assert_called_once()
                self.assertEqual(r.read(journal)['completed_slots'],['verify-2'])

    def test_cleaned_slot_from_task_is_restored_to_production_baseline(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);commit='a'*40;candidate='candidate-1';seed_id='prod-'+commit
            archive=root/'accepted-artifacts'/candidate/commit;archive.mkdir(parents=True)
            (archive/'release.json').write_text(json.dumps(dict(source_commit=commit,source_digest='digest',files={'fixture':'hash'})))
            seeds=root/'seeds';destination=seeds/seed_id;destination.mkdir(parents=True)
            r.write(root/'published.json',dict(candidate=candidate,commit=commit,source_digest='digest'))
            journal=root/'environment-baseline-maintenance.json';r.write(journal,dict(candidate_id=candidate,commit=commit,source_digest='digest',slot='verify-2',previous_enabled=False))
            r.write(root/'verification-install/verify-2.json',dict(stage='cleaned',baseline_cleanup_seed=seed_id))
            r.write(root/'deployments/goal-test-verify-2.json',dict(commit='b'*40))
            worker=object.__new__(w.Worker);worker.api=lambda path:[dict(name=name,state='idle',enabled=False,delivery_id=None) for name in r.VERIFICATION_ENVS]
            with patch.object(r,'RUN',root),patch.object(w.seed,'SEEDS',seeds),patch.object(r,'lock',return_value=contextlib.nullcontext()),patch.object(r,'checksums',return_value={'fixture':'hash'}),patch.object(worker,'current_seed',return_value=dict(seed_id=seed_id,dump_sha256='seed-hash')),patch.object(w.seed,'read_seed',return_value=(destination,dict(build=dict(baseline_commit=commit)),dict(sha256='seed-hash'))),patch.object(w.seed,'verified_sql_seed',return_value=destination/'seed.sql'),patch.object(w.seed,'clear_idle_baseline') as clean,patch.object(w.seed,'ensure') as ensure:
                worker.reconcile_environment_baseline()
                ensure.assert_called_once();clean.assert_called_once()


class BatchFlowTests(unittest.TestCase):
    """组批只在发布器空闲、窗口到期后冻结；一个批次只构建和发布一次。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        patcher = patch.object(r, 'RUN', self.root)
        patcher.start()
        self.addCleanup(patcher.stop)
        (self.root / 'delivery-batch.json').write_text(json.dumps(dict(enabled=True, controller_environment='prod', window_seconds=60)))
        self.worker = object.__new__(w.Worker)
        self.worker.env = 'prod'
        self.calls = []
        self.members = []
        self.state = dict(batch=None, members=[])
        self.worker.batch_call = self.batch_call
        self.worker.publication_owner = lambda: None
        self.worker.api = lambda path, *args: [delivery('d1'),delivery('d2')] if path == 'delivery-queue' else (_ for _ in ()).throw(AssertionError(path))

    def batch_call(self, body=None):
        self.calls.append(body)
        if body is None:
            return self.state
        action = body['action']
        if action == 'open':
            self.state = dict(batch=dict(id='b1', status='collecting', collect_until=time.time() + 60, worker_token=''), members=[])
        elif action == 'freeze':
            self.state = dict(batch=dict(id='b1', status='frozen', collect_until=time.time(), worker_token='w1'), members=self.members)
        else:
            if not self.state.get('batch'):
                self.state['batch'] = dict(id=body.get('batch_id', 'b1'), status='', worker_token=body.get('worker_token', 'w1'))
            self.state['batch']['status'] = dict(preparing='preparing', validating='validating', publishing='publishing', published='published', fail='needs_changes').get(action, self.state['batch']['status'])
        return self.state

    def actions(self):
        return [call['action'] for call in self.calls if call]

    def test_project_batch_only_starts_for_its_project(self):
        (self.root / 'delivery-batch.json').write_text(json.dumps(dict(enabled=True, controller_environment='prod', window_seconds=600, project_id='project-a')))
        other = dict(delivery('other'), project_id='project-b')
        self.assertFalse(self.worker.tick_batches([other]))
        self.assertEqual(self.actions(), [])
        ours = dict(delivery('ours'), project_id='project-a')
        self.worker.tick_batches([other, ours])
        opened = next(call for call in self.calls if call and call['action'] == 'open')
        self.assertEqual(opened['project_id'], 'project-a')
        self.assertEqual(opened['window_seconds'], 600)

    def test_managed_project_settings_control_batch_and_pause(self):
        settings = dict(configured=True, enabled=True, project_id='project-a')
        with patch.dict(os.environ, {}, clear=True), patch.object(self.worker, 'api', return_value=settings):
            self.worker.refresh_release_settings()
            self.assertEqual(self.worker.batch_config()['window_seconds'], 600)
            self.assertEqual(self.worker.batch_project(), 'project-a')
            settings['enabled'] = False
            self.worker.refresh_release_settings()
            self.assertIsNone(self.worker.batch_config())
            self.assertEqual(self.worker.batch_project(), 'project-a')

    def test_project_conflict_holds_batch_without_rebuilding(self):
        self.state = dict(batch=dict(id='b1', status='frozen', worker_token='w1', project_id='project-a', error='合并冲突'),
                          members=[dict(delivery_id='d1', source_commit='a' * 40, result='pending')])
        with patch.object(w.Worker, 'batch_prepare') as prepare:
            self.worker.tick_batches([delivery('d1')])
        prepare.assert_not_called()
        self.assertEqual(self.actions(), ['dispatch-agent', 'retry'])
        self.assertEqual(self.calls[-2]['stage'], 'conflict')
        self.assertTrue(self.calls[-1]['automatic'])

    def test_project_merge_conflict_preserves_the_batch(self):
        member = dict(delivery_id='d1', source_commit='a' * 40, result='pending')
        batch = dict(id='b1', status='frozen', worker_token='w1', project_id='project-a')
        self.state = dict(batch=batch, members=[member])
        self.worker.publication_guard = lambda d: contextlib.nullcontext((dict(generation=1), lambda: None))
        with patch.object(w.Worker, 'batch_source', return_value=(self.root, 'b' * 40, None, member)), \
                patch.object(w.Worker, 'finish_publication') as finished:
            self.worker.batch_prepare(batch, [member], [delivery('d1')])
        self.assertEqual(self.actions(), ['hold'])
        self.assertEqual(self.calls[-1]['conflict_delivery_id'], 'd1')
        finished.assert_called_once()

    def test_project_integration_does_not_reuse_old_evidence(self):
        candidate = dict(id='c1', commit='c' * 40, batch_id='b1', delivery_id='d1', status='verifying')
        batch = dict(id='b1', status='validating', worker_token='w1', project_id='project-a')
        row = dict(delivery('d1'), plan_snapshot={'required_checks': [{'name': '跨任务联调', 'stage': 'integration'}]},
                   evidence=[{'name': '跨任务联调', 'status': 'passed', 'evidence': '旧候选'}])
        (self.root / 'candidate.json').write_text('{}')
        self.worker.publication = lambda *args: None
        with patch.object(r, 'candidate_path', return_value=self.root / 'candidate.json'), \
                patch.object(r, 'candidate', return_value=candidate), \
                patch.object(w.Worker, 'notify_batch_ready'), \
                patch.object(r, 'accept') as accept:
            self.worker.batch_validate(batch, [dict(delivery_id='d1')], [row])
        accept.assert_not_called()

    def test_collect_until_accepts_api_rfc3339_serialization(self):
        # 真实响应格式取自 Go 对 deliveryBatch 的序列化：RFC3339Nano，9 位小数并带时区或 Z。
        now = datetime.datetime.now(datetime.timezone.utc)

        def literal(offset, hours, suffix):
            zone = datetime.timezone(datetime.timedelta(hours=hours))
            return (now + datetime.timedelta(seconds=offset)).astimezone(zone).strftime('%Y-%m-%dT%H:%M:%S') + '.123456789' + suffix
        for value, future in ((literal(600, 8, '+08:00'), True), (literal(-600, 0, 'Z'), False)):
            self.calls = []
            self.state = dict(batch=dict(id='b1', status='collecting', collect_until=value, worker_token=''), members=[])
            with patch.object(w.Worker, 'batch_prepare', return_value=True):
                self.worker.tick_batches([delivery('d1')])
            self.assertEqual(self.actions(), [] if future else ['freeze'], value)

    def test_collect_until_still_accepts_epoch_seconds(self):
        self.state = dict(batch=dict(id='b1', status='collecting', collect_until=time.time() + 60, worker_token=''), members=[])
        self.worker.tick_batches([delivery('d1')])
        self.assertEqual(self.actions(), [])

    def test_preparing_without_candidate_retries_build(self):
        self.state = dict(batch=dict(id='b1', status='preparing', collect_until=time.time(), worker_token='w1'),
                          members=[dict(delivery_id='d1', source_commit='a' * 40, result='pending')])
        with patch.object(r, 'candidate_path', return_value=self.root / 'candidate.json'), \
                patch.object(w.Worker, 'batch_prepare', return_value=True) as prepare:
            self.worker.tick_batches([delivery('d1')])
        prepare.assert_called_once()
        self.assertEqual([call for call in self.calls if call and call['action'] == 'open'], [], '准备中断后另开了新批次')

    def test_preparing_with_deployed_candidate_hands_off_to_validating(self):
        candidate = dict(id='c1', commit='c' * 40, batch_id='b1', status='verifying')
        (self.root / 'candidate.json').write_text('{}')
        self.state = dict(batch=dict(id='b1', status='preparing', collect_until=time.time(), worker_token='w1'),
                          members=[dict(delivery_id='d1', source_commit='a' * 40, result='pending')])
        self.worker.advance = lambda d, action, **extra: self.calls.append(dict(action=action)) or d
        with patch.object(r, 'candidate_path', return_value=self.root / 'candidate.json'), \
                patch.object(r, 'candidate', return_value=candidate), \
                patch.object(w.Worker, 'batch_prepare') as prepare:
            self.worker.tick_batches([delivery('d1')])
            prepare.assert_not_called()
        self.assertEqual(self.actions(), ['validating', 'notify-result'])

    def test_preparing_with_failed_candidate_stops_batch(self):
        candidate = dict(id='c1', commit='c' * 40, batch_id='b1', status='failed', error='构建失败')
        (self.root / 'candidate.json').write_text('{}')
        self.state = dict(batch=dict(id='b1', status='preparing', collect_until=time.time(), worker_token='w1'),
                          members=[dict(delivery_id='d1', source_commit='a' * 40, result='pending')])
        with patch.object(r, 'candidate_path', return_value=self.root / 'candidate.json'), \
                patch.object(r, 'candidate', return_value=candidate):
            self.worker.tick_batches([delivery('d1')])
        failure = [call for call in self.calls if call and call['action'] == 'fail']
        self.assertEqual(len(failure), 1)
        self.assertIn('构建失败', failure[0]['error'])

    def test_prepare_failure_releases_batch_instead_of_staying_preparing(self):
        self.state = dict(batch=dict(id='b1', status='frozen', collect_until=time.time(), worker_token='w1'),
                          members=[dict(delivery_id='d1', source_commit='a' * 40, result='pending')])
        self.worker.publication_guard = lambda d: contextlib.nullcontext((dict(generation=1), lambda: None))
        with patch.object(w.Worker, 'batch_source', return_value=(Path('/tmp'), 'b' * 40, 'c' * 40, None)), \
                patch.object(r, 'prepare_int', side_effect=RuntimeError('构建失败')), \
                patch.object(w.Worker, 'finish_publication') as finished:
            with self.assertRaises(RuntimeError):
                self.worker.batch_prepare(self.state['batch'], self.state['members'], [delivery('d1')])
        failure = [call for call in self.calls if call and call['action'] == 'fail']
        self.assertEqual(len(failure), 1)
        self.assertIn('构建失败', failure[0]['error'])
        finished.assert_called_once()
        self.assertNotIn('validating', self.actions())

    def test_prepare_exits_batch_when_first_member_revoked(self):
        self.check_prepare_exits_batch_when_member_revoked(0)

    def test_prepare_exits_batch_when_nonfirst_member_revoked(self):
        self.check_prepare_exits_batch_when_member_revoked(1)

    def check_prepare_exits_batch_when_member_revoked(self, revoked):
        # 代表 A 在准备复核时被撤权、只剩 B：A 的 delivery/租约不再代表本批，整批退出并释放，
        # 下一批由 B 重新领取租约重建，不能用 A 的租约构建只含 B 的候选。
        self.state = dict(batch=dict(id='b1', status='frozen', collect_until=time.time(), worker_token='w1'),
                          members=[dict(delivery_id='d1', source_commit='a' * 40, result='pending'),
                                   dict(delivery_id='d2', source_commit='b' * 40, result='pending')])
        self.worker.publication_guard = lambda d: contextlib.nullcontext((dict(generation=1), lambda: None))
        self.worker.advance = lambda d, action, **extra: self.calls.append(dict(action=action)) or d
        original = self.worker.batch_call

        def batch_call(body=None):
            state = original(body)
            if body and body['action'] == 'preparing':
                self.state = dict(batch=dict(id='b1', status='preparing', worker_token='w1'),
                                  members=[dict(delivery_id='d1', source_commit='a' * 40, result='withdrawn' if revoked==0 else 'pending'),
                                           dict(delivery_id='d2', source_commit='b' * 40, result='withdrawn' if revoked==1 else 'pending')])
                return self.state
            return state

        self.worker.batch_call = batch_call
        with patch.object(w.Worker, 'batch_source', return_value=(Path('/tmp'), 'b' * 40, 'c' * 40, None)) as source, \
                patch.object(r, 'prepare_int') as prepare, \
                patch.object(w.Worker, 'finish_publication') as finished:
            self.worker.batch_prepare(self.state['batch'], [dict(delivery_id='d1', source_commit='a' * 40, result='pending'),
                                                            dict(delivery_id='d2', source_commit='b' * 40, result='pending')],
                                      [delivery('d1'), delivery('d2')])
        prepare.assert_not_called()
        self.assertEqual(source.call_count, 1, '撤权后仍按旧名单重新合并')
        failure = [call for call in self.calls if call and call['action'] == 'fail']
        self.assertEqual(len(failure), 1)
        self.assertIn('重新组批', failure[0]['error'])
        finished.assert_called_once()
        self.assertNotIn('validating', self.actions())

    def test_cancelled_validation_discards_fixed_candidate(self):
        self.state = dict(batch=dict(id='b1', status='frozen', collect_until=time.time(), worker_token='w1'),
                          members=[dict(delivery_id='d1', source_commit='a' * 40, result='pending')])
        self.worker.publication_guard = lambda d: contextlib.nullcontext((dict(generation=1), lambda: None))
        self.worker.advance = lambda d, action, **extra: self.calls.append(dict(action=action)) or d
        original = self.worker.batch_call

        def batch_call(body=None):
            state = original(body)
            if body and body['action'] == 'validating':
                self.state = dict(batch=dict(id='b1', status='cancelled', error='成员发布授权已失效'), members=[])
                return self.state
            return state

        self.worker.batch_call = batch_call
        with patch.object(w.Worker, 'batch_source', return_value=(Path('/tmp'), 'b' * 40, 'c' * 40, None)), \
                patch.object(r, 'prepare_int', return_value=dict(id='c1', commit='c' * 40)), \
                patch.object(r, 'release_candidate') as release, \
                patch.object(w.Worker, 'finish_publication') as finished:
            self.worker.batch_prepare(self.state['batch'], self.state['members'], [delivery('d1')])
        release.assert_called_once_with('c1')
        finished.assert_called_once()
        self.assertNotIn('notify-result', self.actions())

    def test_window_opens_once_and_waits_before_freezing(self):
        self.worker.tick_batches([delivery('d1')])
        self.assertEqual(self.actions(), ['open'])
        self.state = dict(batch=dict(id='b1', status='collecting', collect_until=time.time() + 30, worker_token=''), members=[])
        self.worker.tick_batches([delivery('d1')])
        self.assertEqual(self.actions(), ['open'], '窗口未到期就冻结了批次')

    def test_window_freezes_after_deadline(self):
        self.members = [dict(delivery_id='d1', source_commit='a' * 40)]
        self.state = dict(batch=dict(id='b1', status='collecting', collect_until=time.time() - 1, worker_token=''), members=[])
        with patch.object(w.Worker, 'batch_prepare', return_value=True) as prepare:
            self.worker.tick_batches([delivery('d1')])
            prepare.assert_called_once()
        self.assertEqual(self.actions(), ['freeze'])

    def test_no_approved_delivery_does_not_open_batch(self):
        self.worker.tick_batches([delivery('d1', 'testing')])
        self.assertEqual(self.actions(), [])

    def test_validating_aborts_released_candidate(self):
        batch = dict(id='b1', status='validating', worker_token='w1')
        candidate = dict(id='c1', batch_id='b1', delivery_id='d1', status='released')
        (self.root / 'candidate.json').write_text('{}')
        with patch.object(r, 'candidate', return_value=candidate), patch.object(self.worker, 'finish_publication'), patch.object(self.worker, 'publication'), patch.object(self.worker, 'notify_batch_ready'):
            self.worker.batch_validate(batch, [dict(delivery_id='d1')], [delivery('d1')])
        self.assertEqual(self.actions(), ['abort'])

    def test_validating_aborts_terminal_member(self):
        for phase in ('cancelled', 'published'):
            with self.subTest(phase=phase):
                self.calls = []
                batch = dict(id='b1', status='validating', worker_token='w1')
                candidate = dict(id='c1', batch_id='b1', delivery_id='d1', status='verifying')
                (self.root / 'candidate.json').write_text('{}')
                with patch.object(r, 'candidate', return_value=candidate), patch.object(r, 'release_candidate') as release, patch.object(self.worker, 'finish_publication'), patch.object(self.worker, 'publication'), patch.object(self.worker, 'notify_batch_ready'):
                    self.worker.batch_validate(batch, [dict(delivery_id='d1')], [delivery('d1', phase)])
                self.assertEqual(self.actions(), ['abort'])
                release.assert_called_once_with('c1')

    def test_validating_reads_terminal_member_omitted_by_queue(self):
        batch = dict(id='b1', status='validating', worker_token='w1')
        candidate = dict(id='c1', batch_id='b1', delivery_id='d1', status='verifying')
        (self.root / 'candidate.json').write_text('{}')
        self.worker.api = lambda path: [delivery('d1', 'cancelled')] if path == 'delivery-queue?current=d1' else (_ for _ in ()).throw(AssertionError(path))
        with patch.object(r, 'candidate', return_value=candidate), patch.object(r, 'release_candidate'), patch.object(self.worker, 'finish_publication'):
            self.worker.batch_validate(batch, [dict(delivery_id='d1')], [])
        self.assertEqual(self.actions(), ['abort'])

    def test_conflict_fails_batch_without_building(self):
        self.state = dict(batch=dict(id='b1', status='frozen', collect_until=time.time(), worker_token='w1'),
                          members=[dict(delivery_id='d1', source_commit='a' * 40)])
        self.worker.publication_guard = lambda d: contextlib.nullcontext((dict(generation=1), lambda: None))
        with patch.object(w.Worker, 'batch_source', return_value=(Path('/tmp'), 'b' * 40, None, dict(delivery_id='d1', source_commit='a' * 40))), \
                patch.object(r, 'prepare_int') as prepare, patch.object(w.Worker, 'finish_publication'):
            self.worker.batch_prepare(self.state['batch'], self.state['members'], [delivery('d1')])
            prepare.assert_not_called()
        failure = [call for call in self.calls if call and call['action'] == 'fail']
        self.assertEqual(failure[0]['conflict_delivery_id'], 'd1')
        self.assertIn('无法合并', failure[0]['error'])

    def test_publish_result_backfills_every_member_once(self):
        candidate = dict(id='c1', commit='c' * 40, source='/tmp/source', batch_id='b1', delivery_id='d1', status='accepted')
        batch = dict(id='b1', status='publishing', worker_token='w1')
        (self.root / 'candidate.json').write_text('{}')
        request = self.root / 'publish-requests' / 'c1.json'
        request.parent.mkdir(parents=True)
        request.write_text(json.dumps(dict(status='succeeded')))
        self.worker.publication = lambda *a, **k: dict(generation=1)
        with patch.object(r, 'candidate_path', return_value=self.root / 'candidate.json'), \
                patch.object(r, 'candidate', return_value=candidate), \
                patch.object(r, 'request_path', return_value=request), \
                patch.object(r, 'release_candidate') as release, patch.object(w.Worker, 'finish_publication'):
            self.worker.batch_check_release(batch, [delivery('d1'), delivery('d2')])
            release.assert_called_once_with('c1')
        published = [call for call in self.calls if call and call['action'] == 'published']
        self.assertEqual(len(published), 1)
        self.assertEqual(published[0]['candidate_commit'], 'c' * 40)

    def test_project_release_cleans_int_before_finishing_batch(self):
        candidate = dict(id='c1', commit='c'*40, batch_id='b1', delivery_id='d1', status='succeeded')
        batch = dict(id='b1', status='publishing', worker_token='w1', project_id='project')
        request = self.root/'request.json';request.write_text(json.dumps({'status':'succeeded'}))
        events=[]
        self.worker.publication=lambda *a,**k:dict(generation=1)
        self.worker.publication_guard=lambda d:contextlib.nullcontext(({},lambda:events.append('fence')))
        self.worker.batch_call=lambda body=None:events.append(body['action'])
        with patch.object(r,'candidate_path',return_value=request),patch.object(r,'candidate',return_value=candidate),patch.object(r,'request_path',return_value=request),patch.object(w.Worker,'sync_environment_baselines',side_effect=lambda *a:events.append('sync')),patch.object(w.integration,'clear_environment',side_effect=lambda *a:events.append('clean')),patch.object(r,'release_candidate',side_effect=lambda *a:events.append('release')),patch.object(w.Worker,'finish_publication',side_effect=lambda *a:events.append('finish')):
            self.worker.batch_check_release(batch,[delivery('d1')])
        self.assertEqual(events,['sync','clean','published','finish','release'])

    def test_project_cleanup_failure_preserves_publication_and_batch(self):
        candidate = dict(id='c1', commit='c'*40, batch_id='b1', delivery_id='d1', status='succeeded')
        batch = dict(id='b1', status='publishing', worker_token='w1', project_id='project')
        request=self.root/'request.json';request.write_text(json.dumps({'status':'succeeded'}))
        self.worker.publication=lambda *a,**k:dict(generation=1)
        self.worker.publication_guard=lambda d:contextlib.nullcontext(({},lambda:None))
        with patch.object(r,'candidate_path',return_value=request),patch.object(r,'candidate',return_value=candidate),patch.object(r,'request_path',return_value=request),patch.object(w.Worker,'sync_environment_baselines'),patch.object(w.integration,'clear_environment',side_effect=r.RecoveryRequired('cleanup interrupted')),patch.object(r,'release_candidate') as release,patch.object(w.Worker,'finish_publication') as finish:
            with self.assertRaises(r.RecoveryRequired):self.worker.batch_check_release(batch,[delivery('d1')])
        self.assertNotIn('published',self.actions())
        release.assert_not_called();finish.assert_not_called()

    def test_unresolved_production_recovery_preserves_batch_and_publication(self):
        candidate=dict(id='c1',commit='c'*40,batch_id='b1',delivery_id='d1',status='recovery_required')
        batch=dict(id='b1',status='publishing',worker_token='w1',project_id='project')
        request=self.root/'request.json';request.write_text(json.dumps({'status':'recovery_required','error':'restore incomplete'}))
        self.worker.publication=lambda *a,**k:dict(generation=1)
        with patch.object(r,'candidate_path',return_value=request),patch.object(r,'candidate',return_value=candidate),patch.object(r,'request_path',return_value=request),patch.object(w.integration,'clear_environment') as clear,patch.object(r,'release_candidate') as release,patch.object(w.Worker,'finish_publication') as finish:
            with self.assertRaisesRegex(r.RecoveryRequired,'恢复尚未确认'):self.worker.batch_check_release(batch,[delivery('d1')])
        self.assertNotIn('fail',self.actions());self.assertNotIn('published',self.actions())
        clear.assert_not_called();release.assert_not_called();finish.assert_not_called()

    def test_failed_release_keeps_members_for_next_batch(self):
        candidate = dict(id='c1', commit='c' * 40, source='/tmp/source', batch_id='b1', delivery_id='d1', status='accepted')
        batch = dict(id='b1', status='publishing', worker_token='w1')
        (self.root / 'candidate.json').write_text('{}')
        request = self.root / 'publish-requests' / 'c1.json'
        request.parent.mkdir(parents=True)
        request.write_text(json.dumps(dict(status='failed', error='生产切换失败')))
        self.worker.publication = lambda *a, **k: dict(generation=1)
        with patch.object(r, 'candidate_path', return_value=self.root / 'candidate.json'), \
                patch.object(r, 'candidate', return_value=candidate), \
                patch.object(r, 'request_path', return_value=request), \
                patch.object(r, 'release_candidate'), patch.object(w.Worker, 'finish_publication'):
            self.worker.batch_check_release(batch, [delivery('d1')])
        failure = [call for call in self.calls if call and call['action'] == 'fail']
        self.assertEqual(len(failure), 1)
        self.assertIn('生产切换失败', failure[0]['error'])
        self.assertNotIn('conflict_delivery_id', failure[0])

    def test_pending_release_does_not_backfill(self):
        candidate = dict(id='c1', commit='c' * 40, source='/tmp/source', batch_id='b1', delivery_id='d1', status='accepted')
        batch = dict(id='b1', status='publishing', worker_token='w1')
        (self.root / 'candidate.json').write_text('{}')
        request = self.root / 'publish-requests' / 'c1.json'
        request.parent.mkdir(parents=True)
        request.write_text(json.dumps(dict(status='publishing')))
        self.worker.publication = lambda *a, **k: dict(generation=1)
        with patch.object(r, 'candidate_path', return_value=self.root / 'candidate.json'), \
                patch.object(r, 'candidate', return_value=candidate), \
                patch.object(r, 'request_path', return_value=request), \
                patch.object(r, 'release_candidate') as release:
            self.worker.batch_check_release(batch, [delivery('d1')])
            release.assert_not_called()
        self.assertEqual([call for call in self.calls if call and call['action'] in ('published', 'fail')], [])


class BatchDrillTests(unittest.TestCase):
    """演练：发布器进程在构建中被杀后重启，沿同一批次接续，不遗留 preparing 卡死。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        patcher = patch.object(r, 'RUN', self.root)
        patcher.start()
        self.addCleanup(patcher.stop)
        (self.root / 'delivery-batch.json').write_text(json.dumps(dict(enabled=True, controller_environment='prod', window_seconds=60)))
        (self.root / 'int-meta.json').write_text(json.dumps(dict(release_id='r0', backend_url='', commit='e' * 40)))
        meta = patch.object(r, 'meta_path', lambda env: self.root / (env + '-meta.json'))
        meta.start()
        self.addCleanup(meta.stop)
        self.state = dict(batch=dict(id='b1', status='preparing', window_seconds=60,
                                     collect_until='2026-10-23T17:30:00.123456789+08:00', worker_token='w1',
                                     base_commit='b' * 40, candidate_id='', candidate_commit='', error=''),
                          members=[dict(delivery_id='d1', issue_id='i1', number=1, source_commit='a' * 40,
                                        result='pending', error='')])
        drill = self

        class API(http.server.BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def reply(self, payload):
                body = json.dumps(payload).encode()
                self.send_response(200)
                self.send_header('Content-Type', 'application/json')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                self.reply(drill.rows() if self.path == '/api/delivery-queue' else drill.state)

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                batch = drill.state['batch']
                action = body['action']
                if action == 'preparing' and batch['status'] == 'frozen':
                    batch['status'] = 'preparing'
                    batch['base_commit'] = body['base_commit']
                elif action == 'validating' and batch['status'] == 'preparing':
                    batch.update(status='validating', candidate_id=body['candidate_id'], candidate_commit=body['candidate_commit'])
                elif action == 'fail':
                    batch.update(status='needs_changes', error=body['error'])
                self.reply(drill.state)

        self.server = http.server.HTTPServer(('127.0.0.1', 0), API)
        threading.Thread(target=self.server.serve_forever, daemon=True).start()

        def stop():
            self.server.shutdown()
            self.server.server_close()
        self.addCleanup(stop)

    def worker(self):
        instance = object.__new__(w.Worker)
        instance.env = 'prod'
        instance.meta = dict(backend_url='http://127.0.0.1:%d' % self.server.server_address[1])
        instance.profile = dict(token='t', workspace_id='ws')
        instance.publication_guard = lambda d: contextlib.nullcontext((dict(generation=1), lambda: None))
        instance.finish_publication = lambda d: None
        instance.advance = lambda d, action, **extra: dict(d, phase='testing')
        return instance

    def rows(self):
        return [delivery('d1')]

    def test_restart_after_build_crash_resumes_same_batch(self):
        def crash(*args, **kwargs):
            r.save_candidate(dict(id='c1', commit='c' * 40, source='/tmp/source', status='building', batch_id='b1',
                                  delivery_id='d1', publisher_pid=0, previous_int_release='r0'))
            raise KeyboardInterrupt('发布器在构建中被杀')

        with patch.object(w.Worker, 'batch_source', return_value=(Path('/tmp'), 'b' * 40, 'c' * 40, None)), \
                patch.object(r, 'prepare_int', side_effect=crash):
            with self.assertRaises(KeyboardInterrupt):
                self.worker().tick_batches(self.rows())
        self.assertEqual(self.state['batch']['status'], 'preparing', '崩溃后批次被留在了其他状态')
        self.assertEqual(r.candidate()['status'], 'building')

        built = []

        def rebuilt(source, delivery=None, fence=None, timeout=900):
            built.append(delivery['batch_id'])
            record = dict(id='c2', commit='c' * 40, source='/tmp/source', status='verifying', batch_id='b1',
                          delivery_id='d1', previous_int_release='r0')
            r.save_candidate(record)
            return record

        with patch.object(w.Worker, 'batch_source', return_value=(Path('/tmp'), 'b' * 40, 'c' * 40, None)), \
                patch.object(r, 'prepare_int', side_effect=rebuilt):
            self.worker().tick_batches(self.rows())
        self.assertEqual(built, ['b1'], '重启后没有沿用原批次重新构建')
        self.assertEqual(self.state['batch']['status'], 'validating')
        self.assertEqual(self.state['batch']['candidate_id'], 'c2')
        self.assertEqual(r.candidate()['status'], 'verifying')

    def test_restart_after_build_hands_off_without_rebuilding(self):
        r.save_candidate(dict(id='c3', commit='c' * 40, source='/tmp/source', status='verifying', batch_id='b1',
                              delivery_id='d1', previous_int_release='r0'))
        with patch.object(w.Worker, 'batch_source') as source, patch.object(r, 'prepare_int') as prepare:
            self.worker().tick_batches(self.rows())
            source.assert_not_called()
            prepare.assert_not_called()
        self.assertEqual(self.state['batch']['status'], 'validating')


class RecoveryOwnershipTests(unittest.TestCase):
    """回收只看交付自身阶段会误伤正在进行的组合批次：活动批次的成员与候选必须保留。"""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        patcher = patch.object(r, 'RUN', self.root)
        patcher.start()
        self.addCleanup(patcher.stop)
        (self.root / 'delivery-batch.json').write_text(json.dumps(dict(enabled=True, controller_environment='prod', window_seconds=60)))
        (self.root / 'int-meta.json').write_text(json.dumps(dict(release_id='r0', commit='e' * 40)))
        (self.root / 'prod-meta.json').write_text(json.dumps(dict(release_id='r0', commit='e' * 40)))
        meta = patch.object(r, 'meta_path', lambda env: self.root / (env + '-meta.json'))
        meta.start()
        self.addCleanup(meta.stop)

    def recover(self, batch):
        identity = '00000000-0000-0000-0000-0000000000a1'
        row = dict(id=identity, phase='approved', source_commit='a' * 40, worker_token='t1', revision=1)
        worker = object.__new__(w.Worker)
        worker.env = 'prod'
        worker.batch_call = lambda body=None: batch
        worker.api = lambda path, *a, **k: [dict(row)]
        worker.own_candidate = lambda d: dict(id='c1', status='verifying', delivery_id=identity, source='/tmp/source')
        worker.finish_publication = lambda d: self.finished.append(d['id'])
        self.finished = []
        (self.root / 'publication-leases').mkdir(exist_ok=True)
        (self.root / 'publication-leases' / (identity + '.json')).write_text(json.dumps(dict(delivery_id=identity, generation=1)))
        with patch.object(r, 'check_process_ownership'), patch.object(r, 'release_candidate') as release:
            worker.recover_finished_publications([row])
            return release

    def test_active_batch_member_keeps_candidate_and_lease(self):
        batch = dict(batch=dict(id='b1', status='validating', worker_token='w1'),
                     members=[dict(delivery_id='00000000-0000-0000-0000-0000000000a1', result='pending')])
        release = self.recover(batch)
        release.assert_not_called()
        self.assertEqual(self.finished, [])
        self.assertTrue((self.root / 'publication-leases' / '00000000-0000-0000-0000-0000000000a1.json').exists())

    def test_disabled_batch_feature_does_not_query_legacy_server(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        with patch.object(w.Worker,'batch_config',return_value=None), patch.object(w.Worker,'batch_call') as api:
            self.assertEqual(worker.active_batch_members(),set())
            api.assert_not_called()

    def test_finished_batch_member_releases_candidate_and_lease(self):
        batch = dict(batch=dict(id='b1', status='validating', worker_token='w1'),
                     members=[dict(delivery_id='00000000-0000-0000-0000-0000000000a1', result='withdrawn')])
        release = self.recover(batch)
        release.assert_called_once_with('c1')
        self.assertEqual(self.finished, ['00000000-0000-0000-0000-0000000000a1'])

    def test_no_active_batch_keeps_previous_approved_cleanup(self):
        release = self.recover(dict(batch=None, members=[]))
        release.assert_called_once_with('c1')
        self.assertEqual(self.finished, ['00000000-0000-0000-0000-0000000000a1'])


class BatchAuthorizationGateTests(unittest.TestCase):
    """组合候选在 push 主分支之前复核成员授权，拒绝时不能已经写进 main。"""

    def test_release_checks_approval_before_push(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        candidate = dict(id='c1', commit='c' * 40, source=str(root), batch_id='b1', delivery_id='d1', status='accepted',batch_members=[])
        batch = dict(id='b1', status='publishing', worker_token='w1')
        worker = object.__new__(w.Worker)
        worker.env = 'prod'
        worker.publication = lambda *a, **k: dict(generation=1)
        worker.batch_call = lambda body=None: dict(batch=batch, members=[])
        worker.finish_publication = lambda d: None
        calls = []
        with patch.object(r, 'RUN', root), patch.object(w.Worker, 'batch_config', return_value=dict(enabled=True)), \
                patch.object(r, 'run', side_effect=lambda args, **k: calls.append(args)), \
                patch.object(w.subprocess, 'run', return_value=type('R', (), {'returncode': 0})()), \
                patch.object(r, 'check_delivery_approval', side_effect=RuntimeError('成员授权已失效')), \
                patch.object(r, 'release_candidate'):
            worker.batch_release(batch, candidate, dict(id='d1'), [])
        self.assertEqual([call for call in calls if 'push' in call], [], '授权复核失败后仍然推送了主分支')


class BatchAcceptanceTests(unittest.TestCase):
    def test_project_initialization_checks_seed_before_cleaning(self):
        worker=object.__new__(w.Worker)
        with tempfile.TemporaryDirectory() as tmp,patch.object(r,'RUN',Path(tmp)),patch.object(worker,'require_sql_seed',side_effect=r.RecoveryRequired('unverified seed')),patch.object(w.integration,'clear_environment') as clear,patch.object(w.integration,'restore_environment') as restore:
            with self.assertRaises(r.RecoveryRequired):worker.ensure_int_data('batch',lambda:None,mandatory=True)
            clear.assert_not_called();restore.assert_not_called()

    def test_legacy_delivery_restores_an_already_cleaned_int(self):
        worker=object.__new__(w.Worker)
        with tempfile.TemporaryDirectory() as tmp,patch.object(r,'RUN',Path(tmp)),patch.object(worker,'require_sql_seed',return_value={'seed_id':'seed'}),patch.object(w.integration,'clear_environment') as clear,patch.object(w.integration,'restore_environment') as restore:
            r.write(Path(tmp)/'integration-data.json',{'stage':'cleaned','batch':'earlier'})
            fence=lambda:None
            worker.ensure_int_data('delivery',fence,mandatory=False)
            clear.assert_not_called()
            restore.assert_called_once_with(r,w.seed,'delivery',{'seed_id':'seed'},fence)

    def test_project_requires_evidence_from_the_current_candidate(self):
        worker=object.__new__(w.Worker)
        candidate=dict(id='combo',commit='c'*40,delivery_id='d1')
        row=dict(id='d1',candidate_id='combo',acceptance_submitted=True,evidence=[dict(name='check',status='passed',evidence='actual')],plan_snapshot=dict(candidate_commit='c'*40))
        self.assertIsNone(worker.batch_acceptance(candidate,[],[row],project=True))
        row['integration_accepted']=True
        self.assertEqual(worker.batch_acceptance(candidate,[],[row],project=True),row['evidence'])

    def test_independent_project_task_is_not_notified_for_integration(self):
        worker=object.__new__(w.Worker)
        worker.release_settings=dict(configured=True,enabled=True,project_id='p1')
        rows=[dict(id='d1',project_id='p1',plan_snapshot=dict(integration_skip_reason='独立文案',required_checks=[dict(stage='verification')])),
              dict(id='d2',project_id='p1',plan_snapshot=dict(required_checks=[dict(stage='integration')]))]
        with patch.object(worker,'api',return_value=rows),patch.object(worker,'advance') as advance:
            worker.notify_batch_ready([dict(delivery_id='d1'),dict(delivery_id='d2')])
        advance.assert_called_once_with(rows[1],'notify-result',stage='batch_ready')

    def test_only_exact_representative_evidence_accepts_combination(self):
        worker=object.__new__(w.Worker)
        candidate=dict(id='combo',commit='c'*40,delivery_id='d1')
        members=[dict(delivery_id='d1'),dict(delivery_id='d2')]
        evidence=[dict(name='combined',status='passed',evidence='actual fixture')]
        def row(identity,candidate_id='combo',commit='c'*40):
            return dict(id=identity,candidate_id=candidate_id,acceptance_submitted=True,evidence=evidence,plan_snapshot=dict(candidate_commit=commit))
        self.assertIsNone(worker.batch_acceptance(candidate,members,[row('d2')]))
        self.assertIsNone(worker.batch_acceptance(candidate,members,[row('d1','old-single')]))
        self.assertIsNone(worker.batch_acceptance(candidate,members,[row('d1',commit='e'*40)]))
        self.assertEqual(worker.batch_acceptance(candidate,members,[row('d1')]),evidence)

    def test_publication_phase_restart_resumes_request_after_fence(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        batch=dict(id='b1',worker_token='token')
        candidate=dict(id='combo',batch_id='b1',delivery_id='d1',status='accepted')
        state=dict(batch=dict(batch,status='publishing'),members=[dict(delivery_id='d1')])
        with tempfile.TemporaryDirectory() as root,patch.object(r,'RUN',Path(root)),patch.object(r,'candidate_path',return_value=Path(root)/'candidate') as path,patch.object(r,'candidate',return_value=candidate),patch.object(w.Worker,'publication'),patch.object(w.Worker,'batch_call',return_value=state) as api,patch.object(w.Worker,'batch_release',return_value=True) as release:
            path.return_value.write_text('{}')
            worker.batch_check_release(batch,[dict(id='d1')])
            self.assertEqual(api.call_args.args[0]['action'],'publishing')
            release.assert_called_once()


class EnvironmentBranchTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name);self.repo=self.root/'work';self.remote=self.root/'remote.git'
        subprocess.run(['git','init','--bare','-q',str(self.remote)],check=True)
        subprocess.run(['git','init','-q',str(self.repo)],check=True)
        self.git('config','user.name','Integration Fixture');self.git('config','user.email','fixture@example.invalid')
        self.git('symbolic-ref','HEAD','refs/heads/personal-studio-upstream')
        self.git('remote','add','origin',str(self.remote))
        (self.repo/'tracked').write_text('a');self.git('add','tracked');self.git('commit','-qm','base')
        self.base=self.git('rev-parse','HEAD')
        (self.repo/'tracked').write_text('b');self.git('commit','-qam','released')
        self.commit=self.git('rev-parse','HEAD')
        self.git('push','-q','origin',self.commit+':refs/heads/personal-studio-upstream')

    def git(self,*args):
        return subprocess.run(['git','-C',str(self.repo),*args],check=True,capture_output=True,text=True).stdout.strip()

    def remote_refs(self):
        return dict((ref,sha) for sha,ref in (line.split() for line in self.git('ls-remote','--heads','origin').splitlines()))

    def test_all_environment_refs_advance_without_changing_checkout(self):
        self.git('push','-q','origin',self.base+':refs/heads/multica-env/int')
        (self.repo/'tracked').write_text('uncommitted')
        before=self.git('status','--porcelain')
        refs=w.sync_environment_refs(self.repo,self.commit,lambda:None)
        self.assertEqual(set(refs),set(w.ENVIRONMENT_BRANCHES))
        self.assertTrue(all(value==self.commit for value in refs.values()))
        self.assertEqual(self.git('status','--porcelain'),before)
        self.assertEqual(self.git('rev-parse','HEAD'),self.commit)
        self.assertEqual(self.git('for-each-ref','--format=%(refname)','refs/multica-release-sync/'),'')
        self.assertEqual(w.sync_environment_refs(self.repo,self.commit,lambda:None),refs)

    def test_divergent_environment_ref_does_not_update_any_branch(self):
        tree=self.git('rev-parse',self.commit+'^{tree}')
        other=self.git('commit-tree',tree,'-m','unrelated')
        self.git('push','-q','origin',other+':refs/heads/multica-env/verify-2')
        before=self.remote_refs()
        with self.assertRaisesRegex(r.RecoveryRequired,'独立提交'):
            w.sync_environment_refs(self.repo,self.commit,lambda:None)
        self.assertEqual(self.remote_refs(),before)
        self.assertEqual(self.git('for-each-ref','--format=%(refname)','refs/multica-release-sync/'),'')

    def test_changed_main_prevents_environment_updates(self):
        before=self.remote_refs()
        with self.assertRaisesRegex(r.RecoveryRequired,'主分支'):
            w.sync_environment_refs(self.repo,self.base,lambda:None)
        self.assertEqual(self.remote_refs(),before)

    def test_lost_publication_fence_prevents_branch_push(self):
        self.git('push','-q','origin',self.base+':refs/heads/multica-env/int')
        before=self.remote_refs();checks=[]
        def fence():
            checks.append(True)
            if len(checks)==2:raise r.RecoveryRequired('lease lost')
        with self.assertRaisesRegex(r.RecoveryRequired,'lease lost'):
            w.sync_environment_refs(self.repo,self.commit,fence)
        self.assertEqual(self.remote_refs(),before)
        self.assertEqual(self.git('for-each-ref','--format=%(refname)','refs/multica-release-sync/'),'')

    def test_production_identity_is_checked_before_sync(self):
        worker=object.__new__(w.Worker)
        candidate=dict(id='candidate',commit=self.commit,source_digest='digest')
        runtime=self.root/'runtime';runtime.mkdir()
        files={'server/bin/server':'a','server/bin/multica':'b','apps/web/server.js':'c'}
        (runtime/'release.json').write_text(json.dumps(dict(source_commit=self.commit,source_digest='digest',files=files)))
        meta=dict(commit=self.commit,source_digest='digest',release_dir=str(runtime),backend_url='http://fixture')
        with patch.object(r,'RUN',self.root),patch.object(r,'ROOT',self.repo):
            r.write(r.RUN/'published.json',dict(candidate='candidate',commit=self.commit,source_digest='digest'))
            r.write(r.meta_path('prod'),meta);r.write(r.config_path('prod'),{})
            with patch.object(r,'check_process_ownership'),patch.object(r,'check_live_environment'),patch.object(r,'checksums',return_value=files),patch.object(r,'http',return_value={'commit':'unexpected'}),patch.object(w,'sync_environment_refs') as sync:
                with self.assertRaisesRegex(r.RecoveryRequired,'运行程序'):
                    worker.sync_environment_baselines(candidate,lambda:None)
                sync.assert_not_called()
            with patch.object(r,'check_process_ownership'),patch.object(r,'check_live_environment'),patch.object(r,'checksums',return_value=files),patch.object(r,'http',return_value={'commit':self.commit}),patch.object(w,'sync_environment_refs',return_value={'fixture':self.commit}) as sync:
                worker.sync_environment_baselines(candidate,lambda:None)
                sync.assert_called_once()
                saved=r.read(r.RUN/'environment-baselines.json')
                self.assertEqual((saved['candidate_id'],saved['commit']),('candidate',self.commit))


if __name__ == '__main__':
    unittest.main()
