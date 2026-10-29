import importlib.util
import contextlib
import io
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import Mock, patch


spec = importlib.util.spec_from_file_location('integration_data', __file__.replace('.test.py', '.py'))
data = importlib.util.module_from_spec(spec)
spec.loader.exec_module(data)


class FakeRelease:
    class RecoveryRequired(RuntimeError):
        pass

    def __init__(self):
        self.config = {
            'int': {'server': {'MULTICA_ENV_ID': 'int', 'DATABASE_URL': 'postgres://multica:secret@localhost:55434/multica_goal_test_int'}},
            'prod': {'server': {'MULTICA_ENV_ID': 'prod', 'DATABASE_URL': 'postgres://multica:secret@localhost:55434/multica_goal_test_680'}},
        }
        self.meta = {'environment': 'int', 'release_id': 'release'}

    def config_path(self, env): return env
    def meta_path(self, env): return 'metadata'
    def read(self, name): return self.meta if name == 'metadata' else self.config[name]
    def check_process_ownership(self, *args): pass
    def check_live_environment(self, *args): pass
    def output(self, cmd):
        if cmd[1] == 'ps': return 'container'
        return json.dumps([{'Id': 'full-container-id', 'Config': {'Labels': {'com.docker.compose.project': 'multica-postgres', 'com.docker.compose.service': 'postgres'}}, 'NetworkSettings': {'Ports': {'5432/tcp': [{'HostIp': '127.0.0.1', 'HostPort': '55434'}]}}}])


class IntegrationIdentityTests(unittest.TestCase):
    def test_valid_target_has_no_credentials(self):
        target = data.identity(FakeRelease())
        self.assertEqual(target['database'], 'multica_goal_test_int')
        self.assertEqual(target['container_id'], 'full-container-id')
        self.assertNotIn('secret', str(target))

    def test_production_or_wrong_server_is_rejected(self):
        for url in ('postgres://multica:secret@localhost:55434/multica_goal_test_680',
                    'postgres://multica:secret@localhost:55433/multica_goal_test_int',
                    'postgres://multica:secret@example.com:55434/multica_goal_test_int'):
            release = FakeRelease()
            release.config['int']['server']['DATABASE_URL'] = url
            with self.assertRaises(FakeRelease.RecoveryRequired): data.identity(release)

    def test_shared_database_and_ambiguous_container_are_rejected(self):
        release = FakeRelease()
        release.config['prod']['server']['DATABASE_URL'] = release.config['int']['server']['DATABASE_URL']
        with self.assertRaises(FakeRelease.RecoveryRequired): data.identity(release)
        release = FakeRelease()
        with patch.object(release, 'output', return_value='container-a\ncontainer-b'):
            with self.assertRaises(FakeRelease.RecoveryRequired): data.identity(release)

    def test_cleaning_requires_empty_int_connections(self):
        target = data.identity(FakeRelease())
        with patch.object(data, 'sql', return_value='1') as sql:
            with self.assertRaises(RuntimeError): data.clear_database(target)
            self.assertEqual(sql.call_count, 1)
        with patch.object(data, 'sql', side_effect=['0', '']) as sql:
            data.clear_database(target)
            self.assertEqual(sql.call_args.args[1], 'multica_goal_test_int')
            self.assertIn("c.relname<>'schema_migrations'", sql.call_args.args[2])

    def test_clear_rejects_other_database_before_sql(self):
        with patch.object(data, 'sql') as sql:
            with self.assertRaises(RuntimeError): data.clear_database({'environment': 'prod', 'database': 'multica_goal_test_680'})
            sql.assert_not_called()

    def test_initialization_stops_when_fence_changes_after_drop(self):
        release = FakeRelease()
        target = data.identity(release)
        class Seed:
            @staticmethod
            def read_seed(name): return Path('/seed'), {}, {'sha256': 'digest'}
            @staticmethod
            def verified_sql_seed(path, manifest): return Path('/seed/seed.sql')
            @staticmethod
            def validate_seed_release(manifest, path): pass
        calls = []
        def fence():
            calls.append('fence')
            if len(calls) == 2: raise RuntimeError('lease lost')
        with tempfile.TemporaryDirectory() as tmp, patch.object(data, 'identity', return_value=target), patch.object(data, 'sql', side_effect=['0', '']) as sql:
            with self.assertRaisesRegex(RuntimeError, 'lease lost'):
                data.initialize_database(release, Seed, target, {'seed_id': 'seed', 'dump_sha256': 'digest'}, tmp, 'develop', 'password', fence)
            self.assertEqual(sql.call_count, 2)
            self.assertEqual(sql.call_args.args[2], 'DROP DATABASE IF EXISTS multica_goal_test_int')

    def test_cleanup_resumes_after_processes_stopped(self):
        target = data.identity(FakeRelease())
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            class R:
                RUN = root
                RecoveryRequired = RuntimeError
                drain = Mock()
                http = Mock()
                owned_stop = Mock()
                start_maintenance = Mock()
                check_process_ownership = Mock()
                @staticmethod
                def lock(*args, **kwargs): return contextlib.nullcontext()
                @staticmethod
                def health_url(env): return 'http://int'
                @staticmethod
                def exists(pid): return False if pid == 0 else pid == 11
                @staticmethod
                def meta_path(env): return root/'meta.json'
                @staticmethod
                def config_path(env): return root/'config.json'
                @staticmethod
                def read(path): return json.loads(path.read_text())
                @staticmethod
                def write(path, value, private=False): path.write_text(json.dumps(value))
            R.meta_path('int').write_text(json.dumps({'release_dir': '/release', 'pids': {'daemon': 11, 'web': 12, 'server': 13}}))
            R.config_path('int').write_text(json.dumps({'server': {'ALLOWED_ACCOUNTS':'develop'}}))
            # Daemon exits immediately after shutdown.
            with patch.object(data, 'identity', return_value=target), patch.object(R, 'exists', side_effect=[True, True, False, False]), patch.object(data, 'sql', side_effect=['0','[{"account":"actual","password_hash":"old-hash"}]']), patch.object(data, 'clear_database') as clear, patch.object(data, 'clear_uploads', side_effect=[RuntimeError('interrupted'), None]):
                with self.assertRaisesRegex(RuntimeError, 'interrupted'): data.clear_environment(R, 'batch', lambda: None)
                self.assertEqual(R.read(root/'integration-data.json')['stage'], 'cleaning')
                self.assertEqual(R.read(R.meta_path('int'))['pids'], {})
                data.clear_environment(R, 'batch', lambda: None)
            self.assertEqual(R.drain.call_count, 1)
            self.assertEqual(clear.call_count, 2)
            self.assertEqual(R.read(root/'integration-data.json')['stage'], 'cleaned')
            self.assertEqual(R.read(root/'integration-data.json')['login']['account'], 'actual')

    def test_restored_login_reuses_only_the_same_int_account(self):
        target = data.identity(FakeRelease())
        with patch.object(data,'sql',return_value='1') as sql:
            data.restore_login(FakeRelease(),target,{'login':{'account':'develop','password_hash':'old-hash'}},'develop',lambda:None)
            self.assertIn("password_hash='old-hash'",sql.call_args.args[2])
            with self.assertRaisesRegex(RuntimeError,'身份不一致'):
                data.restore_login(FakeRelease(),target,{'login':{'account':'other','password_hash':'old-hash'}},'develop',lambda:None)

    def test_cleanup_does_not_take_over_another_batch(self):
        target = data.identity(FakeRelease())
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root/'integration-data.json').write_text(json.dumps({'stage':'cleaning','batch':'other','target':target}))
            class R:
                RUN = root
                RecoveryRequired = RuntimeError
                @staticmethod
                def lock(*args, **kwargs): return contextlib.nullcontext()
                @staticmethod
                def read(path): return json.loads(path.read_text())
            with patch.object(data, 'clear_database') as clear:
                with self.assertRaisesRegex(RuntimeError, '其他批次'): data.clear_environment(R, 'batch', lambda: None)
                clear.assert_not_called()

    def test_cleanup_returns_daemon_to_service_when_tasks_are_queued(self):
        target=data.identity(FakeRelease())
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            (root/'meta.json').write_text(json.dumps({'pids':{'daemon':11}}))
            (root/'config.json').write_text(json.dumps({'server':{}}))
            class R:
                RUN=root
                RecoveryRequired=RuntimeError
                drain=Mock();http=Mock()
                @staticmethod
                def lock(*args,**kwargs):return contextlib.nullcontext()
                @staticmethod
                def health_url(env):return 'http://int'
                @staticmethod
                def exists(pid):return True
                @staticmethod
                def meta_path(env):return root/'meta.json'
                @staticmethod
                def config_path(env):return root/'config.json'
                @staticmethod
                def read(path):return json.loads(path.read_text())
                @staticmethod
                def write(path,value,private=False):path.write_text(json.dumps(value))
            with patch.object(data,'identity',return_value=target),patch.object(data,'sql',return_value='1'),patch.object(data,'clear_database') as clear:
                with self.assertRaisesRegex(RuntimeError,'未完成任务'):data.clear_environment(R,'batch',lambda:None)
            R.http.assert_called_once_with('http://int/drain','DELETE')
            clear.assert_not_called()

    def test_account_binding_restores_profile_and_stops_bootstrap_server(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp); profile = root/'profile.json'
            profile.write_text(json.dumps({'server_url':'http://int', 'workspaces_root':'/int/workspaces', 'token':'old'}))
            meta = {'release_dir':str(root/'release'), 'pids':{}, 'daemon_profile_path':str(profile), 'daemon_workspaces_root':'/int/workspaces', 'backend_url':'http://int', 'log_paths':{'server':'/log'}}
            (root/'meta.json').write_text(json.dumps(meta));(root/'config.json').write_text(json.dumps({'server':{}}))
            class R:
                RecoveryRequired = RuntimeError
                start = Mock(return_value=123)
                ready = Mock()
                owned_stop = Mock()
                @staticmethod
                def exists(pid): return False
                @staticmethod
                def meta_path(env): return root/'meta.json'
                @staticmethod
                def config_path(env): return root/'config.json'
                @staticmethod
                def read(path): return json.loads(path.read_text())
                @staticmethod
                def write(path, value, private=False): path.write_text(json.dumps(value))
            class Seed:
                class i:
                    api = Mock(side_effect=[{'token':'new-token'}, {}])
            opener = Mock();opener.open.return_value = io.BytesIO(b'[{"id":"workspace"}]')
            with patch.object(data.urllib.request, 'build_opener', return_value=opener):
                result=data.bind_account(R, Seed, root/'release', 'develop', 'password', lambda: None)
            self.assertEqual(result, 'workspace')
            self.assertEqual(json.loads(profile.read_text())['token'], 'new-token')
            self.assertEqual(json.loads(profile.read_text())['workspace_id'], 'workspace')
            self.assertEqual(R.read(root/'meta.json')['pids'], {})
            R.owned_stop.assert_called_once()

    def test_restore_retries_same_seed_and_credentials_after_interruption(self):
        target = data.identity(FakeRelease())
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);release=root/'release';release.mkdir()
            (release/'release.json').write_text(json.dumps({'id':'release','files':{'artifact':'hash'}}))
            (root/'integration-data.json').write_text(json.dumps({'stage':'cleaned','batch':'previous','target':target}))
            (root/'meta.json').write_text(json.dumps({'release_id':'release','release_dir':str(release),'pids':{}}))
            (root/'config.json').write_text(json.dumps({'server':{'ALLOWED_ACCOUNTS':'develop'}}))
            class R:
                RUN=root
                RecoveryRequired=RuntimeError
                check_process_ownership=Mock()
                check_live_environment=Mock()
                verify_browser=Mock()
                launch=Mock(return_value={'server':1,'web':2,'daemon':3})
                @staticmethod
                def lock(*args, **kwargs):return contextlib.nullcontext()
                @staticmethod
                def read(path):return json.loads(Path(path).read_text())
                @staticmethod
                def write(path,value,private=False):Path(path).write_text(json.dumps(value))
                @staticmethod
                def config_path(env):return root/'config.json'
                @staticmethod
                def meta_path(env):return root/'meta.json'
                @staticmethod
                def exists(pid):return False
                @staticmethod
                def checksums(path):return {'artifact':'hash'}
            class Seed:
                @staticmethod
                def read_seed(name):return root,{}, {'sha256':'digest'}
                @staticmethod
                def verified_sql_seed(path,manifest):return root/'seed.sql'
                @staticmethod
                def restore_release(manifest,running):return running
                @staticmethod
                def validate_seed_release(manifest,path):pass
            pointer={'seed_id':'seed','dump_sha256':'digest'}
            with patch.object(data,'identity',return_value=target),patch.object(data,'initialize_database') as initialize,patch.object(data,'bind_account',side_effect=[RuntimeError('interrupted'),None]),patch.object(data,'apply_test_runtime') as apply_runtime:
                with self.assertRaisesRegex(RuntimeError,'interrupted'):data.restore_environment(R,Seed,'batch',pointer,lambda:None)
                saved=R.read(root/'integration-data.json');self.assertEqual(saved['stage'],'initializing')
                with self.assertRaisesRegex(RuntimeError,'种子'):data.restore_environment(R,Seed,'batch',{'seed_id':'other','dump_sha256':'digest'},lambda:None)
                result=data.restore_environment(R,Seed,'batch',pointer,lambda:None)
            self.assertEqual(result['stage'],'ready')
            self.assertEqual(result['password'],saved['password'])
            self.assertEqual(initialize.call_count,2)
            apply_runtime.assert_called_once_with(R)
            R.verify_browser.assert_called_once_with('int')

    def test_running_batch_cannot_switch_seed(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            (root/'integration-data.json').write_text(json.dumps({'stage':'ready','batch':'batch','seed':{'seed_id':'old'}}))
            class R:
                RUN=root
                RecoveryRequired=RuntimeError
                @staticmethod
                def lock(*args,**kwargs):return contextlib.nullcontext()
                @staticmethod
                def read(path):return json.loads(path.read_text())
            with patch.object(data,'identity') as identity:
                with self.assertRaisesRegex(RuntimeError,'不能更换'):data.restore_environment(R,None,'batch',{'seed_id':'new'},lambda:None)
                identity.assert_not_called()


if __name__ == '__main__': unittest.main()
