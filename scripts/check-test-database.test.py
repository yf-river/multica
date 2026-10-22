import importlib.util
from pathlib import Path
import unittest
import os
import subprocess
import tempfile

spec=importlib.util.spec_from_file_location('test_database_guard',Path(__file__).with_name('check-test-database.py'))
guard=importlib.util.module_from_spec(spec);spec.loader.exec_module(guard)

class TargetChecks(unittest.TestCase):
    def test_explicit_isolated_target_is_allowed(self):
        target='postgres://test:secret@127.0.0.1:55435/upstream_test'
        self.assertEqual(guard.validate(target,target,[],set()),('loopback',55435,'upstream_test'))
    def test_fixed_instance_is_denied_even_with_another_database_name(self):
        target='postgres://test:secret@localhost:55434/new_test_db'
        with self.assertRaisesRegex(ValueError,'固定生产'):
            guard.validate(target,target,['postgres://owner:secret@127.0.0.1:55434/live'],set())
    def test_paused_instance_is_denied(self):
        target='postgres://test:secret@localhost:55433/own_test'
        with self.assertRaisesRegex(ValueError,'冻结'):
            guard.validate(target,target,[],{55433})
    def test_implicit_or_mismatched_target_is_denied(self):
        target='postgres://test:secret@localhost:55435/own_test'
        for declared in ['',target.replace('own_test','other')]:
            with self.assertRaises(ValueError):guard.validate(target,declared,[],set())
    def test_connection_parameters_cannot_override_the_checked_host(self):
        for key in ['host','hostaddr','port','dbname','service']:
            with self.assertRaises(ValueError):guard.identity('postgres://test@localhost/own?'+key+'=elsewhere')

    def test_shell_guard_uses_the_same_file_target_as_database_preparation(self):
        with tempfile.TemporaryDirectory() as tmp:
            env_file=Path(tmp)/'test target.env'
            env_file.write_text('DATABASE_URL=postgres://test:private@example.invalid/from_file\n')
            result=subprocess.run(['bash',str(Path(__file__).with_name('check-test-database.sh')),str(env_file)],
                env={**os.environ,'DATABASE_URL':'postgres://test:private@example.invalid/from_environment','MULTICA_TEST_DATABASE_URL':'postgres://test:private@example.invalid/from_environment'},
                capture_output=True,text=True)
            self.assertEqual(result.returncode,2)
            self.assertIn('MULTICA_TEST_DATABASE_URL',result.stderr)
            self.assertNotIn('private',result.stderr)

if __name__=='__main__':unittest.main()
