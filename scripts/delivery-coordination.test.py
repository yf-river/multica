#!/usr/bin/env python3
import importlib.util, io, json, subprocess, tempfile, unittest
from pathlib import Path
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('release',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

class CoordinationTests(unittest.TestCase):
    def test_verify_identity_uses_occupied_slot_seed_when_global_seed_changes(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);source=root/'source';source.mkdir();config=root/'verify.json';config.write_text('{}')
            subprocess.run(['git','init','-q',source],check=True)
            (source/'handler.go').write_text('package handler\n')
            pinned={'seed_id':'seed-old','seed_sha256':'a'*64}
            with patch.object(r,'config_path',return_value=config),patch.object(r,'current_seed',return_value={'seed_id':'seed-new','dump_sha256':'b'*64}):
                identity=r.validation_identity(source,'verify-1',pinned)
            self.assertEqual(identity['seed'],'seed-old')
            self.assertEqual(identity['seed_sha256'],'a'*64)

    def test_validation_identity_reuses_docs_but_invalidates_code_and_configuration(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);source=root/'source';source.mkdir();config=root/'int.json';config.write_text('{}')
            subprocess.run(['git','init','-q',source],check=True)
            (source/'handler.go').write_text('package handler\n');(source/'README.md').write_text('first')
            with patch.object(r,'config_path',return_value=config):
                first=r.validation_identity(source)
                (source/'README.md').write_text('second')
                self.assertEqual(first,r.validation_identity(source))
                self.assertEqual(r.validation_plan(source,{'validation_identity':first})['mode'],'reuse')
                (source/'handler.go').write_text('package another\n')
                self.assertNotEqual(first,r.validation_identity(source))
                code=r.validation_identity(source);config.write_text('{"changed":true}')
                self.assertNotEqual(code,r.validation_identity(source))

    def test_long_task_requests_pause_once_and_then_allows_deployment(self):
        now=[0];sleeps=[0]
        def sleep(_):
            sleeps[0]+=1;now[0]+=121 if sleeps[0]==1 else 1
        identity={'profile':'test','pid':123}
        busy={'ready_to_stop':False,'release_pause_supported':True,'claims_in_flight':0,'active_tasks':1}
        with patch.object(r,'health_url',return_value='http://fixture'),patch.object(r,'http',side_effect=[identity,{},busy,busy,dict(busy,ready_to_stop=True)]),patch.object(r.time,'monotonic',side_effect=lambda:now[0]),patch.object(r.time,'sleep',side_effect=sleep),patch.object(r,'read',return_value={'id':'window'}),patch.object(r,'release_window_api',side_effect=[{}, {'tasks':[]}]) as api:
            self.assertTrue(r.drain('int',{'daemon_profile':'test','pids':{'daemon':123}}))
            self.assertEqual(api.call_args_list[0].args,('int',{'id':'window','action':'pause'}));self.assertEqual(api.call_count,2)

    def test_failed_publish_still_resumes_tasks(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);directory=root/'artifact';directory.mkdir();r.write(directory/'release.json',{'id':'candidate'})
            meta={'daemon_id':'daemon','pids':{'daemon':123},'daemon_profile':'test'}
            r.write(root/'deployments/goal-test-int.json',meta)
            with patch.object(r,'RUN',root),patch.object(r,'http',side_effect=[{'pid':123,'profile':'test','daemon_id':'daemon'},{'release_pause_supported':True}]),patch.object(r,'release_window_api') as api,patch.object(r,'resume_release_window') as resume:
                with self.assertRaisesRegex(RuntimeError,'failure'):
                    with r.release_task_window('int',directory):raise RuntimeError('failure')
                self.assertEqual(api.call_args.args[1]['action'],'begin');resume.assert_called_once_with('int')

    def test_restart_recovery_does_not_confuse_reused_pid_with_original_publisher(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder)
            r.write(root/'release-windows/int.json',dict(state='pausing',publisher_pid=123,publisher_started='old'))
            with patch.object(r,'RUN',root),patch.object(r,'exists',return_value=True),patch.object(r,'process_identity',return_value='new'),patch.object(r,'resume_release_window') as resume:
                r.recover_release_windows();resume.assert_called_once_with('int')

    def test_lost_ack_is_recovered_from_checkpoint_only_after_processes_stop(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);work=root/'tasks/workspace/original/worktree';work.mkdir(parents=True)
            checkpoint=dict(task={'id':'source'},root=str(work.parent),work_dir=str(work),session_id='actual-session')
            r.write(work.parent/'.release-pause.json',checkpoint,True)
            profile=root/'profile.json';r.write(profile,dict(token='fixture',workspace_id='fixture'),True)
            meta=dict(daemon_workspaces_root=str(root/'tasks'),daemon_profile_path=str(profile),backend_url='http://fixture')
            r.write(root/'deployments/goal-test-int.json',meta)
            window={'tasks':[dict(task_id='source',state='requested',task_status='deferred',failure_reason='release_pause')]}
            with patch.object(r,'RUN',root),patch.object(r,'health_url',return_value='http://fixture'),patch.object(r,'http',return_value={'active_tasks':0,'claims_in_flight':0}),patch.object(r,'process_env',return_value={}),patch.object(r.urllib.request,'build_opener') as opener:
                opener.return_value.open.return_value=io.BytesIO(b'{}')
                self.assertTrue(r.recover_pause_acknowledgements('int',window))
                request=opener.return_value.open.call_args.args[0]
                self.assertEqual(json.loads(request.data)['session_id'],'actual-session')
                self.assertTrue(request.full_url.endswith('/source/release-pause-ack'))
            with patch.object(r,'RUN',root),patch.object(r,'health_url',return_value='http://fixture'),patch.object(r,'http',return_value={'active_tasks':0,'claims_in_flight':0}),patch.object(r,'process_env',return_value={'MULTICA_TASK_ID':'source'}),patch.object(r,'exists',return_value=True),patch.object(r.urllib.request,'build_opener') as opener:
                with self.assertRaisesRegex(r.RecoveryRequired,'仍存活'):r.recover_pause_acknowledgements('int',window)
                opener.assert_not_called()

if __name__=='__main__':unittest.main()
