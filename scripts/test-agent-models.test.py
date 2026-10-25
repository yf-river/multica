import contextlib
import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location('models', Path(__file__).with_name('test-agent-models.py'))
m = importlib.util.module_from_spec(spec)
spec.loader.exec_module(m)


class ScopeTests(unittest.TestCase):
    # 1. Keep the original global guard and the narrow entry's consumer/ownership checks independently testable.
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.candidate = Path(self.temporary.name) / 'candidate.json'
        self.candidate.write_text('{}')
        self.scope = dict(environment='verify-1', candidate_id='candidate', delivery_id='delivery',
                          source_commit='source', execution_id='execution')
        self.current = dict(status='verifying', id='candidate', delivery_id='delivery', commit='source', release_id='release')
        self.meta = dict(commit='source', release_id='release', pids={'server': 123})
        self.cfg = {'server': {'DATABASE_URL': 'target'}}
        self.scope_path = Path(self.temporary.name) / 'scope.json'
        self.patches = [patch.object(m.r, 'lock', side_effect=lambda *a, **k: contextlib.nullcontext()),
                        patch.object(m.r, 'check_process_ownership'), patch.object(m.r, 'check_live_environment'),
                        patch.object(m.r, 'read', side_effect=self.read),
                        patch.object(m.r, 'candidate', side_effect=lambda **k: self.current),
                        patch.object(m.r, 'candidate_path', return_value=self.candidate),
                        patch.object(m.r, 'process_env', return_value={'DATABASE_URL': 'target'}),
                        patch.object(m.r, 'run'), patch.dict(m.os.environ, {'MULTICA_TASK_ID': 'execution'})]
        for p in self.patches:
            p.start()
            self.addCleanup(p.stop)

    def read(self, path):
        if path == self.scope_path:
            return self.scope
        if path == self.candidate:
            return self.current
        return self.cfg if str(path).endswith('-services.json') else self.meta

    def test_global_apply_still_rejects_active_candidate(self):
        with self.assertRaisesRegex(RuntimeError, '未释放'):
            m.run('verify-1', True)
        m.r.run.assert_not_called()

    def test_target_requires_apply_and_control_source(self):
        for apply, policy in [(False, None), (True, Path('untrusted'))]:
            with self.assertRaises(RuntimeError):
                m.run('verify-1', apply, policy, self.scope_path)
        m.r.run.assert_not_called()

    def test_target_rejects_invalid_scope(self):
        for key in self.scope:
            original = self.scope[key]
            self.scope[key] = 'stale'
            with self.subTest(key=key), self.assertRaises(RuntimeError):
                m.run('verify-1', True, scope_file=self.scope_path)
            self.scope[key] = original
        m.r.run.assert_not_called()

    def test_target_rejects_wrong_live_database(self):
        m.r.process_env.return_value = {'DATABASE_URL': 'different'}
        with self.assertRaises(RuntimeError):
            m.run('verify-1', True, scope_file=self.scope_path)
        m.r.run.assert_not_called()

    def test_target_enters_both_locks(self):
        m.run('verify-1', True, scope_file=self.scope_path)
        self.assertEqual(m.r.lock.call_count, 2)
        self.assertIn('target', m.r.run.call_args.args[0])

    def test_production_always_refused(self):
        with self.assertRaises(RuntimeError):
            m.run('prod', True, scope_file=self.scope_path)
        m.r.run.assert_not_called()


if __name__ == '__main__':
    unittest.main()
