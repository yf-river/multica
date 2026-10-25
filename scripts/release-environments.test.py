import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

class ReleaseTests(unittest.TestCase):
    def setUp(self):
        # 每个用例显式设置任务身份，避免继承执行它的 Agent 上下文。
        self.enterContext(patch.dict(r.os.environ, {"MULTICA_TASK_ID": ""}))

    def test_new_verification_server_persists_its_baseline_mode(self):
        import ast
        import base64
        import secrets
        source=Path(__file__).with_name('verification-environments.py')
        install=next(n for n in ast.parse(source.read_text()).body if isinstance(n,ast.FunctionDef) and n.name=='install')
        assignment=next(n for n in install.body if isinstance(n,ast.Assign) and any(isinstance(t,ast.Name) and t.id=='cfg' for t in n.targets))
        scope=dict(appbase={},agentbase={},dbuser='owned',dbpass='fixture',pgport='1234',dbname='owned',backend='1235',pprof='1236',front='http://fixture',frontend='1237',back='http://backend',slotroot=Path('/owned-slot'),secrets=secrets,base64=base64)
        # 只执行真实安装的配置生产语句，不创建账户、数据库或启动服务。
        exec(compile(ast.Module(body=[assignment],type_ignores=[]),str(source),'exec'),scope)
        self.assertEqual(scope['cfg']['server']['MULTICA_MIGRATION_BASELINE'],'true')
        self.assertNotIn('MULTICA_MIGRATION_BASELINE',scope['cfg']['daemon'])

    def test_database_check_uses_same_pinned_controller_bundle(self):
        with tempfile.TemporaryDirectory() as tmp:
            pinned=Path(tmp)/'pinned/release.py'
            with patch.object(r,'__file__',str(pinned)),patch.object(r,'ROOT',Path(tmp)/'stale-control'),patch.object(r,'run') as run:
                r.check_database(Path('/release'),{'server':{'DATABASE_URL':'owned-test','MULTICA_MIGRATION_BASELINE':'true'}})
                self.assertEqual(run.call_args.args[0],['node',pinned.with_name('check-release-database.mjs'),Path('/release/server/queries.json')])
                self.assertEqual(run.call_args.kwargs['env']['MULTICA_MIGRATION_BASELINE'],'true')

    def test_readiness_snapshot_and_restore_use_real_http_status(self):
        import json
        import threading
        from http.server import BaseHTTPRequestHandler,HTTPServer
        state={'status':'not_ready','checks':{'db':'ok','migrations':'out_of_date'}}
        class Endpoint(BaseHTTPRequestHandler):
            def do_GET(self):
                self.send_response(503);self.end_headers();self.wfile.write(json.dumps(state).encode())
            def log_message(self,*args): pass
        server=HTTPServer(('127.0.0.1',0),Endpoint)
        thread=threading.Thread(target=lambda:server.serve_forever(poll_interval=.05),daemon=True);thread.start()
        try:
            metadata={'backend_url':'http://127.0.0.1:'+str(server.server_port)}
            previous=r.readiness_snapshot(metadata['backend_url']+'/healthz')
            self.assertEqual(previous['http_status'],503)
            r.check_readiness(metadata,prior_readiness=previous)
            self.assertTrue(metadata['readiness']['restored_not_ready'])
            state['checks']={'db':'error','migrations':'unknown'}
            with self.assertRaisesRegex(RuntimeError,'新增故障'):r.check_readiness(metadata,prior_readiness=previous)
        finally:server.shutdown();server.server_close();thread.join()

    def test_only_exact_recorded_unready_state_can_be_restored(self):
        previous={'http_status':503,'body':{'status':'not_ready','checks':{'db':'ok','migrations':'out_of_date'}}}
        metadata={'backend_url':'http://old-api'}
        with patch.object(r,'readiness_snapshot',return_value=previous),patch.object(r,'ready') as ready:
            r.check_readiness(metadata,prior_readiness=previous)
            self.assertTrue(metadata['readiness']['restored_not_ready'])
            self.assertEqual(metadata['readiness']['http_status'],503)
            ready.assert_not_called()
        changed={'http_status':503,'body':{'status':'not_ready','checks':{'db':'error','migrations':'unknown'}}}
        with patch.object(r,'readiness_snapshot',return_value=changed):
            with self.assertRaisesRegex(RuntimeError,'新增故障'): r.check_readiness(metadata,prior_readiness=previous)
        for prior in (None,{'http_status':200,'body':{'status':'ok'}}):
            with patch.object(r,'ready',side_effect=RuntimeError('not ready')):
                with self.assertRaisesRegex(RuntimeError,'not ready'): r.check_readiness(metadata,prior_readiness=prior)
        improved={'http_status':200,'body':{'status':'ok','checks':{'db':'ok','migrations':'ok'}}}
        with patch.object(r,'readiness_snapshot',return_value=improved):
            r.check_readiness(metadata,prior_readiness=previous)
            self.assertEqual(metadata['readiness'],improved)

    def test_unready_backend_does_not_start_web_or_daemon(self):
        metadata={'backend_url':'http://api','log_paths':{'server':'server.log'}}
        def check(url,**kwargs):
            if url.endswith('/healthz'): raise RuntimeError('migrations out_of_date')
            return {'status':'ok'}
        with patch.object(r,'start',return_value=123) as start, patch.object(r,'ready',side_effect=check), patch.object(r,'stop_maintenance') as maintenance, patch.object(r,'run') as run:
            with self.assertRaisesRegex(RuntimeError,'migrations out_of_date'):
                r.launch('int',Path('/release'),{'server':{}},metadata)
            start.assert_called_once()
            maintenance.assert_not_called()
            run.assert_not_called()

    def test_unready_backend_prevents_browser_acceptance(self):
        with patch.object(r,'read',return_value={'backend_url':'http://api'}), patch.object(r,'ready',side_effect=RuntimeError('migrations out_of_date')) as ready, patch.object(r,'run') as run:
            with self.assertRaisesRegex(RuntimeError,'migrations out_of_date'):
                r.verify_browser('int')
            ready.assert_called_once_with('http://api/healthz',timeout=10)
            run.assert_not_called()

    def test_ready_backend_continues_to_real_browser_check(self):
        with patch.object(r,'read',return_value={'backend_url':'http://api'}), patch.object(r,'ready',return_value={'status':'ok'}), patch.object(r,'run') as run:
            r.verify_browser('int')
            self.assertEqual(run.call_args.kwargs['env']['GOAL_TEST_ENV'],'int')
            self.assertEqual(run.call_args.args[0][0],'node')

    def test_build_supplies_execution_user_home_and_preserves_custom_paths(self):
        class ReachedGoBuild(Exception): pass
        for home in (None,'','/custom/home'):
            with self.subTest(home=home),tempfile.TemporaryDirectory() as folder:
                base=Path(folder);source=base/'source';source.mkdir();commands=[]
                inherited={'GOPATH':'/custom/go','GOMODCACHE':'/custom/modcache'}
                if home is not None: inherited['HOME']=home
                def command(args,**kwargs):
                    commands.append((args,kwargs['env'].copy()))
                    if args[0]=='go': raise ReachedGoBuild()
                # 1. build() 先跑执行器预检，本用例只验证构建环境变量，预检在这里 mock 掉。
                with patch.dict(r.os.environ,inherited,clear=True),patch.object(r,'operation_files',return_value=[]),patch.object(r,'executor_preflight',return_value={'fixture':'preflight mocked'}),patch.object(r,'checked_source',return_value=(source,'a'*40)),patch.object(r,'build_version',return_value='v-fixture'),patch.object(r,'source_digest',return_value=('digest',[])),patch.object(r,'read',return_value={'release_dir':str(base/'previous')}),patch.object(r,'copy_tree'),patch.object(r,'run',side_effect=command):
                    with self.assertRaises(ReachedGoBuild): r.build(source,base/'target')
                self.assertEqual([args[0] for args,env in commands],['pnpm','pnpm','go'])
                for args,env in commands:
                    self.assertEqual(env['HOME'],home or r.pwd.getpwuid(r.os.getuid()).pw_dir)
                    self.assertEqual(env['GOPATH'],'/custom/go')
                    self.assertEqual(env['GOMODCACHE'],'/custom/modcache')

    def test_batch_checks_every_members_publication_generation(self):
        import io
        import json
        record={'id':'candidate','commit':'c'*40,'git_common':'/controlled/git','publication_lease':{'generation':7},'batch_members':[{'delivery_id':i,'source_commit':i*40} for i in ('a','b')]}
        records=[dict(m,id=m['delivery_id'],phase='publishing',approved_by='human',release_validated=True,candidate_id='candidate') for m in record['batch_members']]
        profile={'token':'controlled-token','workspace_id':'controlled-workspace'}
        for second_generation in (7,8):
            with self.subTest(second_generation=second_generation):
                responses=[io.BytesIO(json.dumps({'ready':True,'publication_generation':g}).encode()) for g in (7,second_generation)]
                with patch.object(r.urllib.request,'build_opener') as opener, patch.object(r.subprocess,'run') as git:
                    opener.return_value.open.side_effect=responses
                    git.return_value.returncode=0
                    if second_generation==7:
                        r.batch_approval_records(record,records,{'backend_url':'http://controlled'},profile)
                        self.assertEqual(git.call_count,2)
                    else:
                        with self.assertRaisesRegex(RuntimeError,'代次不匹配'):
                            r.batch_approval_records(record,records,{'backend_url':'http://controlled'},profile)
                        self.assertEqual(git.call_count,1)

    def test_external_publish_bundle_contains_matching_browser_dependencies(self):
        with tempfile.TemporaryDirectory() as tmp:
            r.copy_browser_operations(Path(tmp))
            for name in r.BROWSER_OPERATIONS:
                self.assertEqual((Path(tmp)/name).read_bytes(), (Path(r.__file__).parent/name).read_bytes())

    def test_candidates_are_scoped_and_history_survives_reuse(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(r, 'RUN', Path(tmp)):
            for env in ('int', *r.VERIFICATION_ENVS):
                r.save_candidate({'id':env+'-one', 'environment':env, 'status':'verifying'})
            r.save_candidate({'id':'verify-1-two', 'environment':'verify-1', 'status':'verifying'})
            self.assertEqual(r.candidate(env='verify-2')['id'], 'verify-2-one')
            self.assertEqual(r.candidate()['id'], 'int-one')
            self.assertEqual(r.read(r.candidate_record_path('verify-1-one'))['id'], 'verify-1-one')
            with self.assertRaisesRegex(RuntimeError, '其他候选'):
                r.release_candidate('verify-1-one', env='verify-1')
            self.assertEqual(r.candidate(env='verify-1')['status'], 'verifying')

    def test_verification_locks_are_independent(self):
        with tempfile.TemporaryDirectory() as tmp, patch.object(r, 'RUN', Path(tmp)):
            with r.lock(r.candidate_lock('verify-1')):
                with r.lock(r.candidate_lock('verify-2')):
                    with r.lock(r.candidate_lock('int')):
                        with self.assertRaises(BlockingIOError):
                            with r.lock(r.candidate_lock('verify-1')): pass

    def test_verification_candidate_cannot_publish(self):
        with patch.object(r, 'read') as read:
            with self.assertRaisesRegex(RuntimeError, 'int 集成验收'):
                r.check_delivery_approval({'environment':'verify-1'})
            read.assert_not_called()
        for env in ('prod', '../int', '', 'verify-4'):
            with self.assertRaises(RuntimeError): r.candidate_path(env)

    def test_supervision_index_replacement_is_exact_and_preserves_general_gate(self):
        with tempfile.TemporaryDirectory() as tmp:
            release=Path(tmp); migrations=release/'server/migrations'; migrations.mkdir(parents=True)
            replacement=migrations/'550_supervision_active_issue.up.sql'
            replacement.write_text("CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS supervision_active_issue ON supervision_batch(workspace_id,issue_id) WHERE status IN ('preparing','running');\n")
            migration=migrations/'551_supervision_parallel.up.sql'
            statement='DROP INDEX CONCURRENTLY IF EXISTS supervision_active_workspace'
            self.assertTrue(r.reviewed_supervision_index_change(release,migration,[statement]))
            self.assertFalse(r.reviewed_supervision_index_change(release,migration,[statement,'DROP TABLE issue']))
            self.assertFalse(r.reviewed_supervision_index_change(release,migrations/'552_other.up.sql',[statement]))
            replacement.write_text('CREATE INDEX something_else ON issue(id);')
            self.assertFalse(r.reviewed_supervision_index_change(release,migration,[statement]))

    def test_snapshot_column_compatibility_requires_exact_reviewed_sql(self):
        source=Path(__file__).parent.parent/'server/migrations/564_snapshot_column_compatibility.up.sql'
        with tempfile.TemporaryDirectory() as tmp:
            migration=Path(tmp)/source.name
            migration.write_bytes(source.read_bytes())
            self.assertTrue(r.reviewed_snapshot_column_change(migration))
            migration.write_text(migration.read_text()+'DROP TABLE issue;')
            self.assertFalse(r.reviewed_snapshot_column_change(migration))

    def test_version_change_requires_rebuilding_artifacts(self):
        self.assertFalse(r.reusable_build({'version':'v1.0.0','files':{}},{},'v1.0.1'))
        self.assertTrue(r.reusable_build({'version':'v1.0.0','files':{}},{},'v1.0.0'))

    def test_task_cannot_restore_or_switch_production(self):
        with patch.dict(r.os.environ, {'MULTICA_TASK_ID':'task'}), patch.object(r,'read') as read, patch.object(r,'owned_stop') as stop:
            with self.assertRaisesRegex(RuntimeError,'request'): r.deploy('prod','missing')
            with self.assertRaisesRegex(RuntimeError,'外部'): r.restore('prod')
            read.assert_not_called(); stop.assert_not_called()

    def test_checkout_cannot_select_its_own_control_directory(self):
        with patch.object(r,'ROOT',Path('/tmp/task')):
            with self.assertRaisesRegex(RuntimeError,'固定发布入口'): r.require_control_directory()

    def test_copied_script_cannot_use_control_state(self):
        with patch.object(r,'__file__','/tmp/task/scripts/release-environments.py'):
            with self.assertRaisesRegex(RuntimeError,'固定发布入口'): r.require_control_directory()

    def test_reused_components_get_a_new_artifact_identity_and_build_time(self):
        with tempfile.TemporaryDirectory() as d, patch.object(r.time,'time_ns',side_effect=[1,2]):
            root=Path(d); current=root/'current'; source=root/'source'
            (current/'server/bin').mkdir(parents=True)
            (current/'apps/web').mkdir(parents=True)
            (source/'server/migrations').mkdir(parents=True)
            for name in ('verify-release-browser.mjs','lib/goal-test-audit-env.mjs','lib/goal-test-browser-audit.mjs','lib/acceptance-artifacts.mjs'):
                fixture=source/'scripts'/name;fixture.parent.mkdir(parents=True,exist_ok=True)
                fixture.write_text('audit fixture')
            for name in ('issue-delivery-worker.py','release-environments.py'):(source/'scripts'/name).write_text('operation fixture')
            r.write(source/'scripts/delivery-operations.json',{'version':1,'files':['issue-delivery-worker.py','release-environments.py',*r.BROWSER_OPERATIONS]})
            (current/'server/bin/server').write_text('compiled server')
            (current/'apps/web/server.js').write_text('compiled web')
            inputs={key:r.hashlib.sha256().hexdigest() for key in ('go','web')}
            # 产物复用同时核对注入的版本串；测试源码无版本脚本，使用提交前缀。
            r.write(current/'release.json',{'version':'a'*12,'inputs':inputs,'files':r.checksums(current)})
            # 2. 预检依赖真实执行器与源码清单，产物身份用例只需要构建结果，改为 mock。
            with patch.object(r,'executor_preflight',return_value={'fixture':'preflight mocked'}), patch.object(r,'RUN',root/'state'), patch.object(r,'checked_source',return_value=(source,'a'*40)), patch.object(r,'source_digest',return_value=('b'*64,[])):
                r.write(r.meta_path('int'),{'release_dir':str(current)})
                first=r.build(source,root/'first'); second=r.build(source,root/'second')
            self.assertNotEqual(first['id'],second['id'])
            r.time.strptime(first['built_at'],'%Y-%m-%dT%H:%M:%SZ')
            self.assertEqual(first['source_commit'],'a'*40)
            self.assertEqual((root/'first/apps/web/server.js').read_text(),'compiled web')
            self.assertEqual(r.read(root/'first/release.json')['built_at'],first['built_at'])

    def test_both_environments_are_required_for_isolation(self):
        original=r.RUN
        with tempfile.TemporaryDirectory() as d:
            r.RUN=Path(d)
            try:
                config=r.config_path('prod');r.write(config,{
                    'server':{'MULTICA_ENV_ID':'prod','PORT':'42','PPROF_PORT':'44'},
                    'web':{'MULTICA_ENV_ID':'prod','PORT':'43','REMOTE_API_URL':'http://127.0.0.1:42'},
                    'daemon':{'MULTICA_ENV_ID':'prod'}},True)
                before=config.read_bytes()
                with self.assertRaisesRegex(RuntimeError, '缺少 int'):
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

    def test_int_is_supported_and_task_synchronous_deploy_is_rejected(self):
        with self.assertRaises(FileNotFoundError): r.deploy('int','version')
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
            r.write(r.candidate_path(),{'id':'v','status':'accepted'})
            try:
                with patch.object(r,'run') as launch, patch.object(r,'validate_candidate'),patch.object(r,'check_delivery_approval'):
                    first=r.request_release('v');second=r.request_release('v')
                    self.assertEqual(first['id'],second['id']);launch.assert_called_once()
                    self.assertEqual(launch.call_args.args[0][0],'systemd-run')
                    self.assertEqual((r.RUN/'publisher/v/check-release-database.mjs').read_bytes(),Path(r.__file__).with_name('check-release-database.mjs').read_bytes())
                failed=r.read(r.request_path('v'));failed['status']='failed';r.write(r.request_path('v'),failed)
                with patch.object(r,'run') as launch, patch.object(r,'validate_candidate'),patch.object(r,'check_delivery_approval'):
                    with self.assertRaisesRegex(RuntimeError,'重新联调'): r.request_release('v')
                    launch.assert_not_called()
            finally:r.RUN=original

    def test_business_failure_restores_exact_previous_program(self):
        with patch.object(r,'deploy') as deploy, patch.object(r,'restore') as restore, patch.object(r,'verify_browser',side_effect=RuntimeError('bad page')):
            with self.assertRaisesRegex(RuntimeError,'bad page'): r.publish_in_window('prod','new')
            deploy.assert_called_once_with('prod','new',900)
            restore.assert_called_once_with('prod',900)

    def test_database_failure_does_not_drain_or_stop_services(self):
        with tempfile.TemporaryDirectory() as d, patch.object(r,'RUN',Path(d)):
            artifact=Path(d)/'candidate';artifact.mkdir()
            r.write(artifact/'release.json',{'id':'new','files':{}})
            r.write(r.meta_path('prod'),{'release_id':'old','release_dir':'old'})
            r.write(r.config_path('prod'),{})
            with patch.object(r,'check_process_ownership'), patch.object(r,'isolation_check'), patch.object(r,'check_live_environment'), patch.object(r,'prepare_database',side_effect=RuntimeError('schema mismatch')), patch.object(r,'drain') as drain, patch.object(r,'owned_stop') as stop:
                with self.assertRaisesRegex(RuntimeError,'schema mismatch'): r.deploy('prod',artifact)
                drain.assert_not_called();stop.assert_not_called()
                self.assertFalse((r.RUN/'deployments/prod-previous.json').exists())

    def test_database_identity_mismatch_rejects_before_switch(self):
        with patch.object(r,'process_env',return_value={'DATABASE_URL':'other','PORT':'80'}):
            with self.assertRaisesRegex(RuntimeError,'运行配置'):
                r.check_live_environment('int',{'pids':{'server':1}}, {'server':{'DATABASE_URL':'expected','PORT':'80'}})

    def test_startup_failure_restores_old_program_and_config(self):
        original=r.RUN
        with tempfile.TemporaryDirectory() as d:
            r.RUN=Path(d);artifact=r.RUN/'artifacts/new';artifact.mkdir(parents=True)
            r.write(artifact/'release.json',{'id':'new','files':{},'source_commit':'sha','source_digest':'digest'})
            old=r.RUN/'releases/prod/old';old.mkdir(parents=True)
            m={'backend_url':'http://old-api','release_id':'old','release_dir':str(old),'daemon_profile':'p','pids':{'server':11,'web':12,'daemon':13},'log_paths':{}}
            r.write(r.meta_path('prod'),m);r.write(r.config_path('prod'),{'config':'unchanged'})
            def launching(env,release,cfg,metadata,prior_readiness=None):
                if release!=old:
                    metadata['pids']={'server':21};raise RuntimeError('startup failed')
                return {'server':31,'web':32,'daemon':33}
            try:
                with patch.object(r,'readiness_snapshot',return_value={'http_status':200,'body':{'status':'ok'}}),patch.object(r,'prepare_database'),patch.object(r,'start_maintenance'),patch.object(r,'check_live_environment'),patch.object(r,'check_process_ownership'),patch.object(r,'isolation_check'),patch.object(r,'copy_tree'),patch.object(r,'process_env',return_value={'secret':'original'}),patch.object(r,'drain'),patch.object(r,'http'),patch.object(r,'exists',side_effect=lambda pid:pid in (11,12)),patch.object(r,'owned_stop'),patch.object(r,'launch',side_effect=launching),patch.object(r.Path,'resolve',autospec=True,side_effect=lambda p:old if str(p).startswith('/proc/') else p):
                    with self.assertRaisesRegex(RuntimeError,'startup failed'):r.deploy('prod',artifact)
                restored=r.read(r.meta_path('prod'))
                self.assertEqual(restored['release_id'],'old');self.assertEqual(restored['pids']['daemon'],33)
                self.assertEqual(restored['readiness_before'],{'http_status':200,'body':{'status':'ok'}})
                self.assertEqual(r.read(r.config_path('prod')),{'config':'unchanged'})
            finally:r.RUN=original

    def test_reuse_requires_the_same_injected_version(self):
        # 源码没变但注入的版本串变了（例如上一轮是用裸哈希规则构建的），必须重新
        # 编译：否则候选目录里躺着的是没有修复的二进制，版本门槛依旧把它当旧运行时。
        previous={'version':'v0.4.37-0-g9283f784db88','files':{'server/bin/multica':'sum'}}
        files={'server/bin/multica':'sum'}
        self.assertTrue(r.reusable_build(previous,files,'v0.4.37-0-g9283f784db88'))
        self.assertFalse(r.reusable_build(previous,files,'v0.4.38-0-g9283f784db88'))
        self.assertFalse(r.reusable_build({'version':'v0.4.37-0-g9283f784db88','files':{'server/bin/multica':'other'}},files,'v0.4.37-0-g9283f784db88'))

    def test_build_version_rejects_bare_commit_hash(self):
        # 裸提交哈希是这次线上问题的形态：daemon 上报它以后，前端和服务端都判不出
        # 新旧，交接说明被当成旧运行时、快速创建被 422 拒绝。构建阶段就该失败。
        with tempfile.TemporaryDirectory() as d:
            source=Path(d);(source/'scripts').mkdir()
            (source/'scripts/build-version.sh').write_text('')
            with patch.object(r,'output',return_value='9283f784db88'):
                with self.assertRaisesRegex(RuntimeError,'不可被版本门槛解析'):
                    r.build_version(source,'9283f784db8872c7d5f83577e832bd84f131cfad')

    def test_build_version_accepts_forms_the_gates_can_read(self):
        with tempfile.TemporaryDirectory() as d:
            source=Path(d);(source/'scripts').mkdir()
            (source/'scripts/build-version.sh').write_text('')
            for version in ('v0.4.37','v0.4.37-0-g9283f784db88','0.4.37-12-g9283f784-dirty'):
                with patch.object(r,'output',return_value=version):
                    self.assertEqual(r.build_version(source,'9'*40),version)

    def test_build_version_falls_back_to_commit_without_helper(self):
        # 发布器可能对着没有 build-version.sh 的更早任务分支构建；此时行为与改动前一致。
        with tempfile.TemporaryDirectory() as d:
            with patch.object(r,'output') as output:
                self.assertEqual(r.build_version(Path(d),'9'*40),'9'*12)
                output.assert_not_called()

    def test_reviewed_supervision_settings_migrations_are_exact_and_reject_changed_sql(self):
        from tempfile import TemporaryDirectory
        source=Path(__file__).parent.parent/'server/migrations'
        for name in ('617_supervision_instructions.up.sql','618_supervision_managed_agent.up.sql'):
            with TemporaryDirectory() as folder:
                target=Path(folder)/name;target.write_bytes((source/name).read_bytes())
                self.assertTrue(r.reviewed_supervision_settings_change(target))
                target.write_text(target.read_text()+"\nDELETE FROM agent;\n")
                self.assertFalse(r.reviewed_supervision_settings_change(target))

    # Modification 1. A changed or unknown migration cannot inherit trace compatibility approval.
    def test_reviewed_trace_migrations_reject_changed_sql(self):
        source=Path(__file__).parent.parent/'server/migrations'
        names=('643_supervision_material_trace_usage.up.sql', '645_supervision_round_preparation_budget.up.sql', '646_supervision_manual_preparation.up.sql', '649_supervision_trace_scope.up.sql', '650_supervision_preparation_recheck.up.sql')
        for name in names:
            with tempfile.TemporaryDirectory() as folder:
                target=Path(folder)/name
                target.write_bytes((source/name).read_bytes())
                self.assertTrue(r.reviewed_trace_preparation_change(target))
                target.write_text(target.read_text()+"\nDELETE FROM supervision_material;\n")
                self.assertFalse(r.reviewed_trace_preparation_change(target))
        with tempfile.TemporaryDirectory() as folder:
            target=Path(folder)/'999_unknown.up.sql'
            target.write_bytes((source/names[0]).read_bytes())
            self.assertFalse(r.reviewed_trace_preparation_change(target))

    def test_task_provider_trace_migrations_follow_index_and_fk_rules(self):
        # 硬规则：不新增外键/级联；新增索引一律并发建、独立单语句迁移。
        import re
        from pathlib import Path as P
        migrations=P(__file__).parent.parent/'server/migrations'
        table_sql=(migrations/'632_task_provider_trace.up.sql').read_text()
        stripped=re.sub(r'--[^\n]*','',table_sql)
        statements=[part.strip() for part in stripped.split(';') if part.strip()]
        self.assertEqual(len(statements),1)
        self.assertTrue(re.match(r'^CREATE\s+TABLE\b',statements[0],re.I))
        self.assertNotIn('REFERENCES',statements[0].upper())
        self.assertNotIn('CASCADE',statements[0].upper())
        self.assertNotIn('UNIQUE (',statements[0].upper())

        index_sql=(migrations/'633_task_provider_trace_unique.up.sql').read_text()
        stripped=re.sub(r'--[^\n]*','',index_sql)
        statements=[part.strip() for part in stripped.split(';') if part.strip()]
        self.assertEqual(len(statements),1)
        self.assertTrue(re.match(r'^CREATE\s+UNIQUE\s+INDEX\s+CONCURRENTLY\b',statements[0],re.I))

if __name__=='__main__':unittest.main()
