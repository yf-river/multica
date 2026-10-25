import copy
import datetime
import importlib.util
from pathlib import Path
import unittest
from unittest.mock import Mock, patch
import tempfile

spec = importlib.util.spec_from_file_location('setup', Path(__file__).with_name('prepare-supervision-acceptance.py'))
setup = importlib.util.module_from_spec(spec)
spec.loader.exec_module(setup)


class OwnershipTests(unittest.TestCase):
    # 1. Exercise the safety boundary independently of real accounts, processes, and service credentials.
    def setUp(self):
        self.values = dict(environment='verify-1', execution='run',
            delivery=dict(id='delivery', phase='testing', candidate_id='candidate', source_commit='commit'),
            candidate=dict(id='candidate', status='verifying', delivery_id='delivery', environment='verify-1',
                           commit='commit', release_id='artifact'),
            metadata=dict(commit='commit', release_id='artifact'),
            owner=dict(execution_id='run', expires_at=(datetime.datetime.now(datetime.timezone.utc)
                       + datetime.timedelta(minutes=1)).isoformat()),
            lease=dict(name='verify-1', state='verifying', delivery_id='delivery', candidate_id='candidate', source_commit='commit'),
            seed=dict(seed_verified=True, seed_id='seed', seed_sha256='sha'),
            installation=dict(seed_id='seed', seed_sha256='sha'))

    def test_distinct_candidate_and_release_ids_are_valid(self):
        setup.validate_identity(**self.values)

    def test_rejects_unsafe_identities(self):
        cases = [('environment', None, 'prod'), ('execution', None, 'another-run'),
                 ('owner', 'expires_at', '2020-01-01T00:00:00Z'), ('delivery', 'phase', 'needs_changes'),
                 ('candidate', 'status', 'released'), ('candidate', 'release_id', 'other-artifact'),
                 ('metadata', 'commit', 'other-commit'), ('lease', 'delivery_id', 'other-delivery'),
                 ('lease', 'state', 'idle'), ('lease', 'candidate_id', 'old-candidate'),
                 ('lease', 'source_commit', 'old-commit'), ('seed', 'seed_verified', False),
                 ('installation', 'seed_id', 'old-seed'), ('installation', 'seed_sha256', 'old-sha')]
        for root, field, value in cases:
            with self.subTest(root=root, field=field):
                values = copy.deepcopy(self.values)
                if field is None:
                    values[root] = value
                else:
                    values[root][field] = value
                with self.assertRaises(RuntimeError):
                    setup.validate_identity(**values)

    def test_active_or_unavailable_task_inventory_refuses_restart(self):
        for code, output in ((0, '1'), (1, ''), (0, '')):
            with self.subTest(code=code, output=output), patch.object(setup.subprocess, 'run',
                    return_value=Mock(returncode=code, stdout=output)):
                with self.assertRaises(RuntimeError):
                    setup.require_idle({'server': {'DATABASE_URL': 'test-connection'}})

    def test_idle_inventory_allows_configuration(self):
        with patch.object(setup.subprocess, 'run', return_value=Mock(returncode=0, stdout='0\n')):
            setup.require_idle({'server': {'DATABASE_URL': 'test-connection'}})

    def test_invalid_credentials_do_not_mutate_services(self):
        with tempfile.TemporaryDirectory() as folder:
            credential = Path(folder) / 'credential.json'
            credential.write_text('{"workspace_id":"other-workspace","token":"test-only"}')
            service = Mock()
            for mode in (0o644, 0o600):
                credential.chmod(mode)
                with self.subTest(mode=mode), self.assertRaises(RuntimeError):
                    setup.configure_service(service, 'verify-1', {}, {}, 'test-workspace', credential)
                self.assertEqual(service.mock_calls, [])
            link = Path(folder) / 'link.json'
            link.symlink_to(credential)
            with self.assertRaises(RuntimeError):
                setup.configure_service(service, 'verify-1', {}, {}, 'test-workspace', link)
            self.assertEqual(service.mock_calls, [])


if __name__ == '__main__':
    unittest.main()
