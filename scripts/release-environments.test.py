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
            (current/'server/bin/server').write_text('compiled server')
            (current/'apps/web/server.js').write_text('compiled web')
            inputs={key:r.hashlib.sha256().hexdigest() for key in ('go','web')}
            # 产物复用同时核对注入的版本串；测试源码无版本脚本，使用提交前缀。
            r.write(current/'release.json',{'version':'a'*12,'inputs':inputs,'files':r.checksums(current)})
            with patch.object(r,'RUN',root/'state'), patch.object(r,'checked_source',return_value=(source,'a'*40)), patch.object(r,'source_digest',return_value=('b'*64,[])):
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
            m={'release_id':'old','release_dir':str(old),'daemon_profile':'p','pids':{'server':11,'web':12,'daemon':13},'log_paths':{}}
            r.write(r.meta_path('prod'),m);r.write(r.config_path('prod'),{'config':'unchanged'})
            def launching(env,release,cfg,metadata):
                if release!=old:
                    metadata['pids']={'server':21};raise RuntimeError('startup failed')
                return {'server':31,'web':32,'daemon':33}
            try:
                with patch.object(r,'prepare_database'),patch.object(r,'start_maintenance'),patch.object(r,'check_live_environment'),patch.object(r,'check_process_ownership'),patch.object(r,'isolation_check'),patch.object(r,'copy_tree'),patch.object(r,'process_env',return_value={'secret':'original'}),patch.object(r,'drain'),patch.object(r,'http'),patch.object(r,'exists',side_effect=lambda pid:pid in (11,12)),patch.object(r,'owned_stop'),patch.object(r,'launch',side_effect=launching),patch.object(r.Path,'resolve',autospec=True,side_effect=lambda p:old if str(p).startswith('/proc/') else p):
                    with self.assertRaisesRegex(RuntimeError,'startup failed'):r.deploy('prod',artifact)
                restored=r.read(r.meta_path('prod'))
                self.assertEqual(restored['release_id'],'old');self.assertEqual(restored['pids']['daemon'],33)
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

    def test_reviewed_usage_rollup_selfheal_migration_is_exact_and_rejects_changed_sql(self):
        # 620 是函数体替换，不属于"新增表/并发索引"的自动迁移范围，只有这份准确 SQL
        # 可以放行；任何改动（哪怕多一个分号）都必须重新评审。
        from tempfile import TemporaryDirectory
        source=Path(__file__).parent.parent/'server/migrations'
        name='620_task_usage_hourly_rollup_selfheal.up.sql'
        with TemporaryDirectory() as folder:
            target=Path(folder)/name;target.write_bytes((source/name).read_bytes())
            self.assertTrue(r.reviewed_usage_rollup_selfheal(target))
            target.write_text(target.read_text()+"\nUPDATE task_usage SET input_tokens = 0;\n")
            self.assertFalse(r.reviewed_usage_rollup_selfheal(target))

    def test_task_provider_trace_migration_is_additive_only(self):
        # 621 必须只由新增表构成：发布器按语句匹配 CREATE TABLE / 并发索引，
        # 内联唯一约束让建表语句本身就是唯一允许的形状。
        import re
        from pathlib import Path as P
        sql=(P(__file__).parent.parent/'server/migrations/621_task_provider_trace.up.sql').read_text()
        sql=re.sub(r'--[^\n]*','',sql)
        statements=[part.strip() for part in sql.split(';') if part.strip()]
        self.assertEqual(len(statements),1)
        self.assertTrue(re.match(r'^CREATE\s+TABLE\b',statements[0],re.I))

if __name__=='__main__':unittest.main()
