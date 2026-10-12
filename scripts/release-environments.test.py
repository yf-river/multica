import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

class ReleaseTests(unittest.TestCase):
    def test_snapshot_relocates_absolute_workspace_links(self):
        with tempfile.TemporaryDirectory() as d:
            source=Path(d)/'source';stage=Path(d)/'stage'
            for root in (source,stage):
                (root/'packages/core').mkdir(parents=True)
            modules=stage/'packages/views/node_modules/@multica';modules.mkdir(parents=True)
            link=modules/'core';link.symlink_to(source/'packages/core')
            r.relocate_dependency_links(stage,source)
            self.assertEqual(link.resolve(),stage/'packages/core')

    def test_production_configuration_is_verified_without_int(self):
        original=r.RUN
        with tempfile.TemporaryDirectory() as d:
            r.RUN=Path(d)
            try:
                config=r.config_path('prod');r.write(config,{
                    'server':{'MULTICA_ENV_ID':'prod','PORT':'42','PPROF_PORT':'44'},
                    'web':{'MULTICA_ENV_ID':'prod','PORT':'43','REMOTE_API_URL':'http://127.0.0.1:42'},
                    'daemon':{'MULTICA_ENV_ID':'prod'}},True)
                before=config.read_bytes()
                r.isolation_check(config_only=True)
                self.assertEqual(config.read_bytes(),before)
                self.assertEqual(config.stat().st_mode&0o777,0o600)
            finally:r.RUN=original

    def test_checksum_detects_changed_files_and_symlink_targets(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d);(root/'app').write_text('first');(root/'link').symlink_to('app')
            before=r.checksums(root);(root/'app').write_text('second')
            self.assertNotEqual(before,r.checksums(root))
            self.assertEqual(before['link'],'link:app')

    def test_package_cannot_depend_on_external_mutable_directories(self):
        with tempfile.TemporaryDirectory() as d:
            root=Path(d)/'release';root.mkdir()
            outside=Path(d)/'development';outside.mkdir()
            (root/'node_modules').symlink_to(outside,target_is_directory=True)
            with self.assertRaisesRegex(RuntimeError,'符号链接'):r.checksums(root)

    def test_same_environment_deployments_are_serialized(self):
        original=r.RUN
        with tempfile.TemporaryDirectory() as d:
            r.RUN=Path(d)
            try:
                with r.lock('int'):
                    with self.assertRaises(BlockingIOError):
                        with r.lock('int'):pass
                    with r.lock('prod'):pass
            finally:r.RUN=original

    def test_disabled_int_and_task_synchronous_deploy_are_rejected(self):
        with self.assertRaisesRegex(RuntimeError,'int 已停用'): r.deploy('int','version')
        with patch.dict(r.os.environ,{'MULTICA_TASK_ID':'task'}):
            with self.assertRaisesRegex(RuntimeError,'request'): r.deploy('prod','version')

    def test_health_port_uses_actual_profile(self):
        with patch.object(r,'read',return_value={'daemon_profile':'goal-test-prod'}):
            self.assertEqual(r.health_url('prod'),'http://127.0.0.1:19909')

    def test_drain_waits_for_inflight_claims_and_tasks(self):
        calls=[]
        def http(url,method='GET'):
            calls.append((url,method))
            if url.endswith('/health'): return {'profile':'p','pid':1}
            if method=='GET': return {'ready_to_stop':len(calls)>3,'active_tasks':1,'claims_in_flight':1}
            return {}
        with patch.object(r,'http',side_effect=http),patch.object(r,'health_url',return_value='http://daemon'),patch.object(r.time,'sleep'):
            self.assertTrue(r.drain('prod',{'daemon_profile':'p','pids':{'daemon':1}}))
        self.assertGreater(len(calls),3)
        self.assertNotIn(('http://daemon/shutdown','POST'),calls)

    def test_drain_timeout_and_failure_resume_claiming(self):
        for failure in (None,RuntimeError('probe failed')):
            calls=[]
            def http(url,method='GET'):
                calls.append((url,method))
                if url.endswith('/health'): return {'profile':'p','pid':1}
                if method=='GET' and failure: raise failure
                return {'ready_to_stop':False}
            with patch.object(r,'http',side_effect=http),patch.object(r,'health_url',return_value='http://daemon'),patch.object(r.time,'sleep'),patch('builtins.print'):
                with self.assertRaises(RuntimeError): r.drain('prod',{'daemon_profile':'p','pids':{'daemon':1}},.001)
            self.assertEqual(calls[-1],('http://daemon/drain','DELETE'))

    def test_duplicate_requests_launch_only_one_external_publisher(self):
        original=r.RUN
        with tempfile.TemporaryDirectory() as d:
            r.RUN=Path(d)
            artifact=r.RUN/'artifacts'/'v';artifact.mkdir(parents=True)
            r.write(artifact/'release.json',{'files':{}})
            try:
                with patch.object(r,'run') as launch:
                    first=r.request_release('v');second=r.request_release('v')
                    self.assertEqual(first['id'],second['id']);launch.assert_called_once()
                    self.assertEqual(launch.call_args.args[0][0],'systemd-run')
                failed=r.read(r.request_path('v'));failed['status']='failed';r.write(r.request_path('v'),failed)
                with patch.object(r,'run') as launch:
                    r.request_release('v',retry=True);r.request_release('v',retry=True)
                    launch.assert_called_once()
            finally:r.RUN=original

    def test_business_failure_restores_exact_previous_program(self):
        with patch.object(r,'read',return_value={'release_id':'old'}),patch.object(r,'deploy') as deploy,patch.object(r,'verify_browser',side_effect=[RuntimeError('bad page'),None]):
            with self.assertRaisesRegex(RuntimeError,'数据库未回滚'):r.publish('new')
            self.assertEqual(deploy.call_args_list[0].args,('prod','new',900))
            self.assertEqual(deploy.call_args_list[1].args,('prod','old',900))

    def test_startup_failure_restores_old_program_and_config(self):
        original=r.RUN
        with tempfile.TemporaryDirectory() as d:
            r.RUN=Path(d);artifact=r.RUN/'artifacts/new';artifact.mkdir(parents=True)
            r.write(artifact/'release.json',{'files':{},'source_commit':'sha','source_digest':'digest'})
            old=r.RUN/'releases/prod/old';old.mkdir(parents=True)
            m={'release_id':'old','release_dir':str(old),'daemon_profile':'p','pids':{'server':11,'web':12,'daemon':13},'log_paths':{}}
            r.write(r.meta_path('prod'),m);r.write(r.config_path('prod'),{'config':'unchanged'})
            def launching(env,release,cfg,metadata):
                if release!=old:
                    metadata['pids']={'server':21};raise RuntimeError('startup failed')
                return {'server':31,'web':32,'daemon':33}
            try:
                with patch.object(r,'check_process_ownership'),patch.object(r,'isolation_check'),patch.object(r,'copy_tree'),patch.object(r,'process_env',return_value={'secret':'original'}),patch.object(r,'drain'),patch.object(r,'http'),patch.object(r,'exists',side_effect=lambda pid:pid in (11,12)),patch.object(r,'owned_stop'),patch.object(r,'launch',side_effect=launching),patch.object(r.Path,'resolve',autospec=True,side_effect=lambda p:old if str(p).startswith('/proc/') else p):
                    with self.assertRaisesRegex(RuntimeError,'startup failed'):r.deploy('prod','new')
                restored=r.read(r.meta_path('prod'))
                self.assertEqual(restored['release_id'],'old');self.assertEqual(restored['pids']['daemon'],33)
                self.assertEqual(r.read(r.config_path('prod')),{'config':'unchanged'})
            finally:r.RUN=original

if __name__=='__main__':unittest.main()
