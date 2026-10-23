import contextlib
import importlib.util
import json
import tempfile
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
        self.state = dict(batch=None, members=[])
        self.worker.batch_call = self.batch_call
        self.worker.publication_owner = lambda: None

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


if __name__ == '__main__':
    unittest.main()
