import hashlib
import importlib.util
import json
import os
import subprocess
from contextlib import nullcontext
import tempfile
from types import SimpleNamespace
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('seed', Path(__file__).with_name('verification-seed.py'))
s = importlib.util.module_from_spec(spec)
spec.loader.exec_module(s)

SHA = 'b' * 64


def write_seed(directory, manifest, payload=b'dump'):
    directory.mkdir(parents=True, exist_ok=True)
    (directory / 'seed.dump').write_bytes(payload)
    manifest.setdefault('artifact', dict(dump='seed.dump', sha256=hashlib.sha256(payload).hexdigest()))
    (directory / 'seed.json').write_text(json.dumps(manifest))


class SeedManifestTests(unittest.TestCase):
    def test_bound_runtime_does_not_reapply_seed_placeholder_model_rule(self):
        def count(_container, _database, statement):
            return int("COALESCE(model,'')='deepseek-v4.1-flash'" in statement or 'workspace_mcp_check' in statement)
        with patch.object(s, 'table_exists', return_value=True), patch.object(s, 'counter', side_effect=count):
            before = s.scrub_checks('fixture', 'fixture')
            after = s.scrub_checks('fixture', 'fixture', model_bound=True)
        self.assertEqual(next(x for x in before if x['name'] == '测试 Agent 显式使用受支持模型')['status'], 'failed')
        self.assertFalse(any(x['name'] == '测试 Agent 显式使用受支持模型' for x in after))
        self.assertEqual(next(x for x in after if x['name'] == 'MCP 检查已清空')['status'], 'failed')

    def test_sql_seed_checks_file_identity_and_checksum(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp)
            payload = b'SELECT 1;\n'
            (path / 'seed.sql').write_bytes(payload)
            manifest = {'sql_artifact': {'file': 'seed.sql', 'sha256': hashlib.sha256(payload).hexdigest()}}
            self.assertEqual(s.read_sql_seed(path, manifest), path / 'seed.sql')
            self.assertIsNone(s.verified_sql_seed(path, manifest))
            manifest['sql_verification'] = {'passed': True, 'sha256': manifest['sql_artifact']['sha256']}
            self.assertEqual(s.verified_sql_seed(path, manifest), path / 'seed.sql')
            manifest['sql_verification']['sha256'] = 'stale'
            with self.assertRaisesRegex(RuntimeError, '需要重新验收'):
                s.verified_sql_seed(path, manifest)
            manifest['sql_verification']['sha256'] = manifest['sql_artifact']['sha256']
            (path / 'seed.sql').write_bytes(b'SELECT 2;\n')
            with self.assertRaisesRegex(RuntimeError, 'SQL 种子文件校验失败'):
                s.read_sql_seed(path, manifest)
            manifest['sql_artifact']['file'] = '../seed.sql'
            with self.assertRaisesRegex(RuntimeError, '种子缺少 SQL 文件或校验和'):
                s.read_sql_seed(path, manifest)

    def test_read_seed_rejects_modified_dump(self):
        with tempfile.TemporaryDirectory() as tmp:
            seed = Path(tmp) / 'seed-1'
            write_seed(seed, dict(seed_id='seed-1', version=2))
            (seed / 'seed.dump').write_bytes(b'tampered')
            with self.assertRaisesRegex(RuntimeError, '种子文件校验失败'):
                s.read_seed(seed)

    def test_legacy_manifest_keeps_source_and_checksum(self):
        with tempfile.TemporaryDirectory() as tmp:
            seed = Path(tmp) / 'seed-20261022-final-v1'
            write_seed(seed, dict(source_environment='int-sanitized-from-verify-1', source_commit='86be17e7', collected_at='2026-10-22T00:00:00Z', database='multica_verify_1', dump_sha256=hashlib.sha256(b'dump').hexdigest()))
            path, manifest, artifact = s.read_seed(seed)
            self.assertEqual(artifact['sha256'], hashlib.sha256(b'dump').hexdigest())
            source = s.source_identity(manifest)
            self.assertEqual(source['environment'], 'int-sanitized-from-verify-1')
            self.assertEqual(source['commit'], '86be17e7')
            self.assertEqual(source['database'], 'multica_verify_1')

    def test_scrub_identity_tracks_rules_file(self):
        identity = s.scrub_identity()
        self.assertEqual(identity['version'], json.loads(s.SCRUB.read_text())['version'])
        self.assertEqual(len(identity['sha256']), 64)

    def test_sql_fixture_source_identity_survives_verification(self):
        source = s.source_identity({'source': {'environment': 'fixtures-sql', 'fixture_sql_sha256': SHA}})
        self.assertEqual(source['environment'], 'fixtures-sql')
        self.assertEqual(source['fixture_sql_sha256'], SHA)


class UploadCleanupTests(unittest.TestCase):
    def test_only_registered_slot_uploads_are_cleared(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'verify-1'
            uploads = root / 'app' / 'uploads'
            uploads.mkdir(parents=True)
            (uploads / 'nested').mkdir()
            (uploads / 'nested' / 'image').write_text('fixture')
            outside = Path(tmp) / 'other'
            outside.mkdir()
            (outside / 'retained').write_text('keep')
            (uploads / 'link').symlink_to(outside, target_is_directory=True)
            cfg = {'server': {'LOCAL_UPLOAD_DIR': str(uploads), 'MULTICA_SERVICE_USER': 'fixture'}}
            record = {'environment': 'verify-1', 'data_root': str(root)}
            with patch.object(s.i.pwd, 'getpwnam', return_value=SimpleNamespace(pw_uid=os.getuid())):
                s.clear_verification_uploads('verify-1', cfg, record)
            self.assertEqual(list(uploads.iterdir()), [])
            self.assertEqual((outside / 'retained').read_text(), 'keep')

    def test_mismatched_or_symlinked_root_is_rejected(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp) / 'verify-1'
            uploads = root / 'app' / 'uploads'
            uploads.mkdir(parents=True)
            cfg = {'server': {'LOCAL_UPLOAD_DIR': str(uploads), 'MULTICA_SERVICE_USER': 'fixture'}}
            record = {'environment': 'verify-2', 'data_root': str(root)}
            with self.assertRaisesRegex(RuntimeError, '身份不匹配'):
                s.clear_verification_uploads('verify-1', cfg, record)
            alias = Path(tmp) / 'alias'
            alias.symlink_to(root, target_is_directory=True)
            cfg['server']['LOCAL_UPLOAD_DIR'] = str(alias / 'app' / 'uploads')
            record = {'environment': 'verify-1', 'data_root': str(alias)}
            with self.assertRaisesRegex(RuntimeError, '不一致'):
                s.clear_verification_uploads('verify-1', cfg, record)


class EnsureTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        patcher = patch.object(s.r, 'RUN', Path(self.tmp.name))
        patcher.start()
        self.addCleanup(patcher.stop)

    def seed(self):
        self.addCleanup(patch.stopall)
        patcher = patch.object(s, 'SEEDS', Path(self.tmp.name) / 'seeds')
        patcher.start()
        seed = Path(self.tmp.name) / 'seeds' / 'seed-current'
        write_seed(seed, dict(seed_id='seed-current', version=2, restore_verification=dict(passed=True)))
        s.r.write(s.current_path(), dict(seed_id='seed-current', dump_sha256=hashlib.sha256(b'dump').hexdigest()))
        return seed

    def test_without_current_pointer_keeps_existing_behaviour(self):
        with patch.object(s, 'slot_seed') as slot, patch.object(s, 'reset') as reset:
            self.assertEqual(s.ensure('verify-1', Path('/pg'))['status'], 'skipped')
            slot.assert_not_called()
            reset.assert_not_called()

    def test_matching_verified_slot_is_reused(self):
        self.seed()
        with patch.object(s, 'slot_seed', return_value=dict(seed_id='seed-current', seed_sha256=hashlib.sha256(b'dump').hexdigest(), verified=True)), patch.object(s, 'reset') as reset:
            self.assertEqual(s.ensure('verify-1', Path('/pg'))['status'], 'current')
            reset.assert_not_called()

    def test_stale_or_unverified_slot_is_restored_before_claim(self):
        self.seed()
        with patch.object(s, 'slot_seed', return_value=dict(seed_id='old', seed_sha256=SHA, verified=True)), patch.object(s, 'reset') as reset:
            self.assertEqual(s.ensure('verify-1', Path('/pg'))['status'], 'restored')
            reset.assert_called_once()

    def test_unverified_same_seed_is_restored_again(self):
        self.seed()
        with patch.object(s, 'slot_seed', return_value=dict(seed_id='seed-current', seed_sha256=hashlib.sha256(b'dump').hexdigest(), verified=False)), patch.object(s, 'reset') as reset:
            s.ensure('verify-1', Path('/pg'))
            reset.assert_called_once()

    def test_missing_slot_record_is_restored(self):
        self.seed()
        with patch.object(s, 'slot_seed', return_value=None), patch.object(s, 'reset') as reset:
            s.ensure('verify-1', Path('/pg'))
            reset.assert_called_once()


original_worker_check = s.candidate_worker_running


class RetiredCandidateTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        patcher = patch.object(s.r, 'RUN', Path(self.tmp.name))
        patcher.start()
        self.addCleanup(patcher.stop)
        self.current = dict(id='old-candidate', environment='verify-2', status='recovery_required',
                            delivery_id='old-delivery', commit='failed-source',
                            previous_release='live-release', error='migration rejected')
        self.meta = dict(commit='live-source', release_id='live-release')
        self.delivery = dict(id='old-delivery', phase='needs_changes', revision=3,
                             verification_run=None, verification_owner=None)
        self.slot = dict(name='verify-2', state='idle', enabled=False, delivery_id=None)
        s.r.save_candidate(self.current)
        for name in ('check_process_ownership', 'check_live_environment'):
            check = patch.object(s.r, name)
            check.start()
            self.addCleanup(check.stop)
        s.r.write(s.r.config_path('verify-2'), {})
        self.api = patch.object(s, 'control_api', side_effect=self.control)
        self.api.start()
        self.addCleanup(self.api.stop)
        process = patch.object(s, 'candidate_worker_running', return_value=False)
        process.start()
        self.addCleanup(process.stop)

    def control(self, route):
        if route == 'delivery-queue/slots':
            return [self.slot]
        if route == 'delivery-queue?current=old-delivery':
            return [self.delivery]
        raise AssertionError(route)

    def test_owned_live_worker_process_is_detected(self):
        with subprocess.Popen(['python3', '-c', 'import time; time.sleep(10)',
                               '--pool-delivery', 'old-delivery']) as child:
            try:
                with patch.object(s, 'candidate_worker_running', wraps=original_worker_check):
                    self.assertTrue(s.candidate_worker_running(self.current))
            finally:
                child.terminate()
                child.wait(timeout=5)

    def test_failed_undeployed_candidate_is_archived_after_lease_reset(self):
        s.retire_failed_candidate('verify-2', self.meta)
        current = s.r.candidate(env='verify-2')
        self.assertEqual(current['status'], 'failed')
        self.assertEqual(current['error'], 'migration rejected')
        self.assertEqual(current['maintenance_recovery']['previous_status'], 'recovery_required')
        self.assertEqual(s.r.read(s.r.RUN/'candidates/recovered/old-candidate.json'), self.current)
        s.retire_failed_candidate('verify-2', self.meta)

    def test_active_or_unproven_candidates_remain_untouched(self):
        changes = [('status', 'verifying'), ('commit', 'live-source'), ('previous_release', 'different')]
        for key, value in changes:
            current = dict(self.current, **{key: value})
            s.r.save_candidate(current)
            with self.subTest(key=key), self.assertRaisesRegex(RuntimeError, '候选'):
                s.retire_failed_candidate('verify-2', self.meta)
            self.assertEqual(s.r.candidate(env='verify-2'), current)

    def test_live_owner_task_worker_or_slot_blocks_recovery(self):
        for field, value in [('phase', 'testing'), ('verification_owner', {'execution_id': 'live'}),
                             ('verification_run', {'status': 'running'})]:
            previous = self.delivery.get(field)
            self.delivery[field] = value
            with self.subTest(field=field), self.assertRaises(RuntimeError):
                s.retire_failed_candidate('verify-2', self.meta)
            self.delivery[field] = previous
        self.slot['enabled'] = True
        with self.assertRaises(RuntimeError):
            s.retire_failed_candidate('verify-2', self.meta)
        self.slot['enabled'] = False
        with patch.object(s, 'candidate_worker_running', return_value=True), self.assertRaises(RuntimeError):
            s.retire_failed_candidate('verify-2', self.meta)
        self.assertEqual(s.r.candidate(env='verify-2'), self.current)


class ResetTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        patcher = patch.object(s.r, 'RUN', Path(self.tmp.name))
        patcher.start()
        self.addCleanup(patcher.stop)
        self.seed = Path(self.tmp.name) / 'seed'
        write_seed(self.seed, dict(seed_id='seed-x', version=2, restore_verification=dict(passed=True)))

    def test_success_marks_seed_and_resumes_slot(self):
        with patch.object(s, 'begin_maintenance', return_value=True) as maintain, patch.object(s, 'restore', return_value=dict(seed_id='seed-x')), patch.object(s, 'record_slot_seed') as record, patch.object(s, 'resume') as resume:
            s.reset('verify-1', self.seed, Path('/pg'))
            maintain.assert_called_once()
            record.assert_called_once()
            self.assertTrue(record.call_args[0][3])
            resume.assert_called_once()

    def test_failure_keeps_slot_in_maintenance_and_marks_unverified(self):
        with patch.object(s, 'begin_maintenance', return_value=True), patch.object(s, 'restore', side_effect=RuntimeError('恢复失败')), patch.object(s, 'record_slot_seed') as record, patch.object(s, 'resume') as resume:
            with self.assertRaisesRegex(RuntimeError, '槽位保持维护'):
                s.reset('verify-1', self.seed, Path('/pg'))
            self.assertFalse(record.call_args[0][3])
            resume.assert_not_called()

    def test_disabled_slot_stays_disabled_after_restore(self):
        with patch.object(s, 'begin_maintenance', return_value=False), patch.object(s, 'restore', return_value=dict(seed_id='seed-x')), patch.object(s, 'record_slot_seed') as record, patch.object(s, 'resume') as resume:
            s.reset('verify-1', self.seed, Path('/pg'))
            record.assert_called_once()
            resume.assert_not_called()

    def test_seed_verification_keeps_disabled_slot_disabled(self):
        restored=dict(container='fixture',database='fixture')
        with patch.object(s,'begin_maintenance',return_value=False),patch.object(s,'restore',return_value=restored),patch.object(s,'api_checks',return_value=[]),patch.object(s,'data_checks',return_value=[]),patch.object(s,'scrub_checks',return_value=[]),patch.object(s,'record_slot_seed'),patch.object(s,'resume') as resume:
            s.verify_seed('verify-1',self.seed,Path('/pg'))
            resume.assert_not_called()

    def test_maintenance_uses_atomic_previous_state_and_old_server_fallback(self):
        slots=[dict(name='verify-1',enabled=True)]
        with patch.object(s,'control_api',return_value=slots),patch.object(s,'maintain',return_value={'previous_enabled':False}):
            self.assertFalse(s.begin_maintenance('verify-1','test'))
        with patch.object(s,'control_api',return_value=slots),patch.object(s,'maintain',return_value={'enabled':False}):
            self.assertTrue(s.begin_maintenance('verify-1','test'))



def write_release(directory, migration=b'CREATE TABLE sample(id int);'):
    (directory / 'server/migrations').mkdir(parents=True)
    (directory / 'server/bin').mkdir(parents=True)
    (directory / 'server/migrations/001_sample.up.sql').write_bytes(migration)
    (directory / 'server/bin/migrate').write_bytes(b'test-migrate')
    (directory / 'release.json').write_text(json.dumps({'id':directory.name,'source_commit':'c'*40,'source_digest':'d'*64,'files': s.r.checksums(directory)}))


class CompatibilityTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.release = self.root / 'release'
        write_release(self.release)
        self.manifest = {'schema': {'fingerprint': SHA,
                         'migration_files_sha256': s.release_schema_identity(self.release)}}

    def test_same_migrations_allow_different_application_release(self):
        other = self.root / 'other'
        write_release(other)
        s.validate_seed_release(self.manifest, other)

    def test_new_task_migrations_select_verified_full_seed_baseline(self):
        other=self.root/'task-a';write_release(other,b'CREATE TABLE task_a(id int);')
        manifest=dict(self.manifest,build={'release':str(self.release)})
        self.assertEqual(s.restore_release(manifest,other),self.release)
        self.assertEqual(s.restore_release(manifest,self.release),self.release)

    def test_missing_seed_release_uses_only_matching_published_archive(self):
        other = self.root/'task-a'; write_release(other,b'CREATE TABLE task_a(id int);')
        commit = 'c'*40
        archive = self.root/'accepted-artifacts'/'candidate-1'/commit
        write_release(archive)
        (self.root/'published.json').write_text(json.dumps({'candidate':'candidate-1','commit':commit,'source_digest':'d'*64}))
        manifest = dict(self.manifest, build={'release':str(self.root/'retired')})
        with patch.object(s.r, 'RUN', self.root):
            self.assertEqual(s.restore_release(manifest,other),archive)
            (archive/'server/bin/migrate').write_bytes(b'tampered')
            with self.assertRaisesRegex(RuntimeError,'完整制品校验失败'):
                s.restore_release(manifest,other)

    def test_baseline_full_package_or_current_migration_tampering_stops_restore(self):
        other=self.root/'task-a';write_release(other,b'CREATE TABLE task_a(id int);')
        manifest=dict(self.manifest,build={'release':str(self.release)})
        (self.release/'untracked-program').write_text('unexpected')
        with self.assertRaisesRegex(RuntimeError,'完整制品校验失败'):s.restore_release(manifest,other)
        (other/'server/bin/migrate').write_bytes(b'tampered')
        with self.assertRaisesRegex(RuntimeError,'校验失败'):s.restore_release(manifest,other)

    def test_legacy_built_seed_uses_immutable_release_identity(self):
        s.validate_seed_release({'schema': {'fingerprint': SHA},
                                 'build': {'release': str(self.release)}}, self.release)

    def test_unbuilt_seed_and_mismatched_migrations_fail_closed(self):
        with self.assertRaisesRegex(RuntimeError, '缺少候选迁移身份'):
            s.validate_seed_release({}, self.release)
        other = self.root / 'other'
        write_release(other, b'CREATE TABLE other(id int);')
        with self.assertRaisesRegex(RuntimeError, '不兼容'):
            s.validate_seed_release(self.manifest, other)

    def test_tampered_migration_program_is_rejected(self):
        (self.release / 'server/bin/migrate').write_bytes(b'tampered')
        with self.assertRaisesRegex(RuntimeError, '校验失败'):
            s.validate_seed_release(self.manifest, self.release)

    def test_incompatible_restore_stops_before_drain_or_database_write(self):
        other = self.root / 'other'
        write_release(other, b'CREATE TABLE other(id int);')
        seed = self.root / 'seed'
        write_seed(seed, self.manifest)
        cfg = {'server': {'DATABASE_URL': 'postgres://verify_1@127.0.0.1:54321/multica_verify_1'}}
        original_read = s.r.read
        def read_fixture(path):
            if 'deployments' in Path(path).parts:
                return {'release_dir': str(other)}
            if Path(path).name.endswith('-services.json'):
                return cfg
            if 'verification-install' in Path(path).parts:
                return {}
            return original_read(path)
        with patch.dict(os.environ, {}, clear=True), patch.object(s, 'postgres', return_value=('own-container', '54321', 'fake')), \
             patch.object(s.r, 'RUN', self.root), patch.object(s.r, 'lock', return_value=nullcontext()), \
             patch.object(s.r, 'read', side_effect=read_fixture), \
             patch.object(s.r, 'validate_verification_config'), patch.object(s.r, 'drain') as drain, patch.object(s, 'sql') as sql:
            with self.assertRaisesRegex(RuntimeError, '不兼容'):
                s.restore('verify-1', seed, self.root / 'pg.json')
            drain.assert_not_called()
            sql.assert_not_called()

    def test_active_candidate_or_interrupted_different_seed_stops_before_mutation(self):
        seed=self.root/'seed';write_seed(seed,dict(self.manifest,seed_id='seed-x'))
        cfg={'server':{'DATABASE_URL':'postgres://verify_1@127.0.0.1:54321/multica_verify_1'}}
        original_read=s.r.read
        for active in (True,False):
            with self.subTest(active=active):
                def read_fixture(path):
                    if 'deployments' in Path(path).parts:return {'release_dir':str(self.release)}
                    if Path(path).name.endswith('-services.json'):return cfg
                    if 'verification-install' in Path(path).parts:return {'stage':'resetting','seed_id':'other','seed_sha256':SHA}
                    return original_read(path)
                with patch.dict(os.environ,{},clear=True),patch.object(s,'postgres',return_value=('own','54321','fake')),patch.object(s.r,'RUN',self.root),patch.object(s.r,'lock',return_value=nullcontext()),patch.object(s.r,'read',side_effect=read_fixture),patch.object(s.r,'validate_verification_config'),patch.object(s.r,'candidate_path') as cp,patch.object(s.r,'candidate',return_value={'status':'verifying'}),patch.object(s,'sql') as sql,patch.object(s.r,'drain') as drain:
                    cp.return_value.exists.return_value=active
                    with self.assertRaisesRegex(RuntimeError,'未完成候选|不能切换恢复目标'):s.restore('verify-1',seed,self.root/'pg.json')
                    sql.assert_not_called();drain.assert_not_called()

    def test_restored_schema_or_scrub_failure_keeps_slot_in_maintenance(self):
        seed = self.root / 'seed'
        write_seed(seed, self.manifest)
        for failure in ['数据库结构与种子一致', 'MCP 凭据配置已清空']:
            with self.subTest(failure=failure), \
                 patch.object(s, 'begin_maintenance', return_value=True), patch.object(s, 'resume') as resume, \
                 patch.object(s, 'record_slot_seed') as record, \
                 patch.object(s, 'data_checks', return_value=[{'name': failure, 'status': 'failed'}]), \
                 patch.object(s, 'scrub_checks', return_value=[]), \
                 patch.object(s, 'restore', side_effect=lambda *args: s.check_restored_data('own', 'own', self.manifest)):
                with self.assertRaisesRegex(RuntimeError, '保持维护'):
                    s.reset('verify-1', seed, self.root / 'pg.json')
                self.assertFalse(record.call_args.args[-1])
                resume.assert_not_called()


@unittest.skipUnless(os.environ.get('MULTICA_SEED_TEST_CONTAINER'), '需要显式指定本次独占隔离容器')
class MCPScrubDatabaseTests(unittest.TestCase):
    def test_fake_mcp_secrets_bindings_and_pending_checks_are_removed(self):
        container = os.environ['MULTICA_SEED_TEST_CONTAINER']
        inspected = json.loads(subprocess.check_output(['docker', 'inspect', container]))[0]
        self.assertEqual(inspected['Config']['Labels'].get('multica.purpose'), 'seed-hardening-test')
        s.sql(container, 'postgres', """
          CREATE TABLE workspace_mcp_server(id int,config jsonb);
          CREATE TABLE agent_mcp_server(agent_id int,server_id int,enabled bool);
          CREATE TABLE workspace_mcp_check(id int,status text);
          CREATE TABLE plugin_remote_mcp_oauth_state(secret_ciphertext bytea);
          INSERT INTO workspace_mcp_server VALUES(1,'{"url":"https://fake.invalid/mcp","headers":{"Authorization":"fake-seed-secret"}}');
          INSERT INTO agent_mcp_server VALUES(2,1,true);
          INSERT INTO workspace_mcp_check VALUES(3,'pending');
          INSERT INTO plugin_remote_mcp_oauth_state VALUES(decode('abcd','hex'));
        """)
        rules = json.loads(s.SCRUB.read_text())['rules']
        statements = [rule['sql'] for rule in rules if s.table_exists(container, 'postgres', rule['table'])]
        s.sql(container, 'postgres', 'BEGIN;'+ ';'.join(statements) + ';COMMIT;')
        for table in ('workspace_mcp_server', 'agent_mcp_server', 'workspace_mcp_check', 'plugin_remote_mcp_oauth_state'):
            self.assertEqual(s.counter(container, 'postgres', 'SELECT count(*) FROM '+table), 0)
        self.assertTrue(all(item['status'] == 'passed' for item in s.scrub_checks(container, 'postgres')))
        s.sql(container, 'postgres', "INSERT INTO workspace_mcp_server VALUES(4,'{\"url\":\"https://fake.invalid\"}');")
        checks = s.scrub_checks(container, 'postgres')
        self.assertEqual(next(item for item in checks if item['name'] == 'MCP 凭据配置已清空')['status'], 'failed')


    def test_real_schema_drift_is_rejected_before_slot_resume(self):
        container = os.environ['MULTICA_SEED_TEST_CONTAINER']
        inspected = json.loads(subprocess.check_output(['docker', 'inspect', container]))[0]
        self.assertEqual(inspected['Config']['Labels'].get('multica.purpose'), 'seed-hardening-test')
        s.sql(container, 'postgres', """
          CREATE TABLE schema_migrations(version text);
          INSERT INTO schema_migrations VALUES('001_fixture');
          CREATE TABLE workspace(id int);
          CREATE TABLE "user"(id int);
          CREATE TABLE issue(id int);
          CREATE TABLE comment(id int);
          CREATE SEQUENCE fixture_sequence;
          INSERT INTO workspace VALUES(1);
          INSERT INTO "user" VALUES(1);
          INSERT INTO issue VALUES(1);
          INSERT INTO comment VALUES(1);
          DELETE FROM workspace_mcp_server;
        """)
        manifest = {'schema': s.schema_identity(container, 'postgres')}
        self.assertTrue(all(c['status'] == 'passed' for c in s.check_restored_data(container, 'postgres', manifest)))
        s.sql(container, 'postgres', 'ALTER TABLE workspace ADD COLUMN drift text;')
        with self.assertRaisesRegex(RuntimeError, '数据库结构与种子一致'):
            s.check_restored_data(container, 'postgres', manifest)



if __name__ == '__main__':
    unittest.main()
