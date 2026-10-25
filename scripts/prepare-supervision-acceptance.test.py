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
    # 1. Accept the real source's default syntax without executing shell or using unresolved expressions.
    def test_skill_token_defaults_and_literals(self):
        for assignment in ('TOKEN=${AGENTLENS_TOKEN:-test-token}',
                           'TOKEN="${AGENTLENS_TOKEN:-test-token}"', "TOKEN='test-token'"):
            self.assertEqual(setup.skill_token(assignment), 'test-token')
        for assignment in ('TOKEN=${AGENTLENS_TOKEN}', 'TOKEN=$(command)',
                           'TOKEN=`command`', 'TOKEN=a b', 'OTHER=value'):
            with self.subTest(assignment=assignment), self.assertRaises(RuntimeError):
                setup.skill_token(assignment)

    def test_skill_credential_source_removed_on_setup_failure(self):
        with tempfile.TemporaryDirectory() as folder:
            release = Mock(RUN=Path(folder))
            def fail(*args):
                source = args[-1]
                self.assertEqual(source.stat().st_mode & 0o777, 0o600)
                self.assertEqual(setup.json.loads(source.read_text()),
                                 dict(workspace_id='test-workspace', token='test-token'))
                raise RuntimeError('test setup failure')
            with patch.object(setup, 'api', return_value='TOKEN=${KEY:-test-token}'), \
                    patch.object(setup, 'configure_service', side_effect=fail):
                with self.assertRaisesRegex(RuntimeError, 'test setup failure'):
                    setup.configure_from_skill(release, 'verify-1', {}, {}, 'test-workspace', {}, {})
            self.assertEqual(list(Path(folder).rglob('source-*.json')), [])

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

    # 1. Check the candidate repository layout, so a nonexistent built-in skill path cannot regress.
    def test_pinned_candidate_skill_source(self):
        root = Path(__file__).resolve().parent.parent
        source, content = setup.candidate_skill({'source': str(root)})
        self.assertEqual(source, root / 'skills/delivery-supervision')
        self.assertEqual(content, (source / 'SKILL.md').read_text())
        self.assertTrue(content)

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

    # 1. Real int-only phase and publication generation must agree before configuration writes.
    def test_int_only_publication_contract(self):
        values = copy.deepcopy(self.values)
        values.update(environment='int')
        values['delivery'].update(phase='preparing_release', plan_snapshot=dict(integration_only=True))
        values['candidate'].update(environment='int', publication_lease=dict(generation=9))
        values['lease'] = dict(state='active', delivery_id='delivery', generation=9,
                              expires_at=values['owner']['expires_at'])
        setup.validate_identity(**values)
        for root, field, value in [('delivery', 'phase', 'testing'),
                                  ('delivery', 'plan_snapshot', {}),
                                  ('lease', 'state', 'idle'), ('lease', 'generation', 8),
                                  ('lease', 'expires_at', '2020-01-01T00:00:00Z'),
                                  ('lease', 'delivery_id', 'foreign'),
                                  ('candidate', 'commit', 'foreign'),
                                  ('installation', 'seed_sha256', 'foreign')]:
            with self.subTest(root=root, field=field), self.assertRaises(RuntimeError):
                changed = copy.deepcopy(values)
                changed[root][field] = value
                setup.validate_identity(**changed)

    def test_active_or_unavailable_task_inventory_refuses_restart(self):
        for code, output in ((0, '1'), (1, ''), (0, '')):
            with self.subTest(code=code, output=output), patch.object(setup.subprocess, 'run',
                    return_value=Mock(returncode=code, stdout=output)):
                with self.assertRaises(RuntimeError):
                    setup.require_idle({'server': {'DATABASE_URL': 'test-connection'}})

    def test_idle_inventory_allows_configuration(self):
        # 1. Pin the checked-in direct client so an ambient psql wrapper cannot redirect this safety check.
        with patch.object(setup.subprocess, 'run', return_value=Mock(returncode=0, stdout='0\n')) as run:
            setup.require_idle({'server': {'DATABASE_URL': 'test-connection'}})
        self.assertEqual(run.call_args.args[0][:2], ['node', '--input-type=module'])
        self.assertEqual(run.call_args.kwargs['env']['PGDATABASE'], 'test-connection')

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
