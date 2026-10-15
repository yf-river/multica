"""固定环境发布：真实临时 Git 仓库验证源码与回收，进程切换使用 Mock。"""
import importlib.util
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('release', Path(__file__).with_name('release-environments.py'))
r = importlib.util.module_from_spec(spec)
spec.loader.exec_module(r)

class ReleaseTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.base = Path(self.tmp.name)
        self.root = self.base/'repo'
        self.root.mkdir()
        self.git('init','-q')
        self.git('config','user.email','test@example.invalid')
        self.git('config','user.name','test')
        (self.root/'server/cmd/server').mkdir(parents=True)
        (self.root/'server/cmd/server/main.go').write_text('package main\n')
        (self.root/'apps/web').mkdir(parents=True)
        (self.root/'apps/web/page.ts').write_text('first\n')
        (self.root/'.gitignore').write_text('node_modules/\n.env\n')
        self.commit()
        self.sha=self.git('rev-parse','HEAD')
        self.run=self.base/'state'
        self.patches=[patch.object(r,'RUN',self.run),patch.object(r,'ROOT',self.root)]
        for p in self.patches:p.start()
        r.write(self.run/'published.json',{'commit':self.sha})
    def tearDown(self):
        for p in reversed(self.patches):p.stop()
        self.tmp.cleanup()
    def git(self,*args,cwd=None):
        return subprocess.check_output(['git',*args],cwd=cwd or self.root,stderr=subprocess.DEVNULL).decode().strip()
    def commit(self):
        self.git('add','.')
        self.git('-c','commit.gpgsign=false','commit','-qm','test')
    def worktree(self):
        source=self.base/'task'
        self.git('worktree','add','-qb','feature/test/task',str(source))
        return source
    def record(self,source):
        return {'id':'candidate','commit':self.sha,'source':str(source),
                'branch':'refs/heads/feature/test/task','git_common':str(self.root/'.git'),
                'release_id':self.sha,'source_digest':r.source_digest(source)[0],'deployed_at':'today'}

    def test_migrate_only_new_version_files(self):
        previous=self.base/'old';target=self.base/'new'
        for folder in (previous,target):
            (folder/'server/migrations').mkdir(parents=True)
            (folder/'server/migrations/518_existing.up.sql').write_text('existing')
        (target/'server/migrations/519_repositories.up.sql').write_text('new')
        with patch.object(r,'run') as execute:
            r.migrate(target,{'server':{}},previous=previous)
            self.assertEqual(execute.call_args.args[0][-2:],['--versions','519_repositories'])
        with patch.object(r,'run') as execute:
            r.migrate(previous,{'server':{}},previous=previous)
            execute.assert_not_called()

    def test_clean_source_and_digest(self):
        source=self.worktree()
        self.assertEqual(r.checked_source(source)[1],self.sha)
        self.assertEqual(r.source_digest(source)[0],r.git_digest(source,self.sha))
    def test_dirty_source_is_rejected(self):
        (self.root/'new.txt').write_text('unsaved')
        with self.assertRaisesRegex(RuntimeError,'干净'):r.checked_source(self.root)
    def test_outdated_branch_cannot_overwrite_production(self):
        source=self.worktree()
        (self.root/'new.txt').write_text('published')
        self.commit()
        r.write(self.run/'published.json',{'commit':self.git('rev-parse','HEAD')})
        with self.assertRaisesRegex(RuntimeError,'最新已发布'):r.checked_source(source)
    def test_uncommitted_legacy_baseline_blocks_migration(self):
        (self.run/'published.json').unlink()
        r.write(r.meta_path('prod'),{'commit':self.sha,'source_digest':'dirty-snapshot'})
        with self.assertRaisesRegex(RuntimeError,'未提交内容'):r.checked_source(self.root)
    def test_only_clean_linked_worktree_is_removed(self):
        source=self.worktree();record=self.record(source)
        self.assertEqual(r.cleanup_worktree(record),'removed')
        self.assertFalse(source.exists())
        self.assertEqual(self.git('rev-parse','feature/test/task'),self.sha)
        self.assertEqual(r.cleanup_worktree(record),'already_removed')
    def test_main_checkout_is_retained(self):
        self.assertEqual(r.cleanup_worktree(self.record(self.root)),'main_checkout_retained')
    def test_dirty_worktree_is_retained(self):
        source=self.worktree();(source/'unsaved.txt').write_text('user data')
        with self.assertRaisesRegex(RuntimeError,'未保存'):r.cleanup_worktree(self.record(source))
        self.assertTrue(source.exists())
    def test_ignored_user_file_is_retained(self):
        source=self.worktree();(source/'.env').write_text('private configuration')
        with self.assertRaisesRegex(RuntimeError,'忽略文件'):r.cleanup_worktree(self.record(source))
    def test_build_dependencies_do_not_prevent_cleanup(self):
        source=self.worktree();(source/'node_modules').mkdir();(source/'node_modules/a').write_text('cache')
        self.assertEqual(r.cleanup_worktree(self.record(source)),'removed')
    def test_generated_document_indexes_do_not_prevent_cleanup(self):
        source=self.worktree()
        (self.root/'.git/info/exclude').write_text('.source/\n')
        for name in ('apps/docs/.source/index.ts','apps/web/.source/index.ts','apps/web/.source/source.config.mjs'):
            path=source/name;path.parent.mkdir(parents=True,exist_ok=True);path.write_text('// generated')
        self.assertEqual(r.cleanup_worktree(self.record(source)),'removed')
    def test_other_source_directory_is_retained(self):
        source=self.worktree()
        (self.root/'.git/info/exclude').write_text('.source/\n')
        path=source/'notes/.source/draft';path.parent.mkdir(parents=True);path.write_text('user work')
        with self.assertRaisesRegex(RuntimeError,'忽略文件'):r.cleanup_worktree(self.record(source))
    def test_running_process_prevents_worktree_removal(self):
        source=self.worktree()
        process=subprocess.Popen(['sleep','30'],cwd=source)
        try:
            with self.assertRaisesRegex(RuntimeError,'进程'):r.cleanup_worktree(self.record(source))
        finally:process.terminate();process.wait()
    def test_finalized_worktree_does_not_block_publication(self):
        source=self.worktree();record=self.record(source)
        r.write(r.meta_path('int'),{k:record[k] for k in ('release_id','source_digest','deployed_at')})
        self.git('worktree','remove',str(source))
        self.assertEqual(r.validate_candidate(record,allow_finalized=True)['release_id'],self.sha)
        with self.assertRaisesRegex(RuntimeError,'不存在'):r.validate_candidate(record)
    def test_changed_int_identity_invalidates_acceptance(self):
        record=self.record(self.worktree());r.write(r.meta_path('int'),{'release_id':'other'})
        with self.assertRaisesRegex(RuntimeError,'发生变化'):r.validate_candidate(record)
    def test_failed_feature_check_cannot_be_accepted(self):
        record=self.record(self.worktree());record['status']='verifying';r.write(r.candidate_path(),record)
        evidence=self.base/'evidence.json'
        r.write(evidence,{'candidate':'candidate','summary':'failure','checks':[{'name':'feature','status':'failed','evidence':'bad'}]})
        with patch.object(r,'validate_candidate'),self.assertRaisesRegex(RuntimeError,'passed'):r.accept('candidate',evidence)
    def test_successful_acceptance_records_feature_evidence(self):
        record=self.record(self.worktree());record['status']='verifying';r.write(r.candidate_path(),record)
        evidence=self.base/'evidence.json'
        r.write(evidence,{'candidate':'candidate','summary':'verified','checks':[{'name':'feature','status':'passed','evidence':'actual result'}]})
        with patch.object(r,'validate_candidate'):r.accept('candidate',evidence)
        self.assertEqual(r.candidate()['status'],'accepted')
    def test_queued_candidate_cannot_be_released(self):
        r.write(r.candidate_path(),{'id':'candidate','status':'queued'})
        with self.assertRaisesRegex(RuntimeError,'不能释放'):r.release_candidate('candidate')
    def test_failed_deployment_check_restores_previous(self):
        with patch.object(r,'deploy') as deploy,patch.object(r,'verify_browser',side_effect=RuntimeError('browser')),patch.object(r,'restore') as restore:
            with self.assertRaisesRegex(RuntimeError,'browser'):r.publish('prod',self.base/'slot',10)
            restore.assert_called_once_with('prod',10)
    def test_failed_task_blocks_production(self):
        with patch.object(r,'task_api',return_value={'status':'failed'}):
            with self.assertRaisesRegex(RuntimeError,'failed'):r.wait_requested_task({'task_id':'task','timeout':1})
    def test_manual_request_does_not_fake_task(self):
        with patch.object(r,'task_api') as api:r.wait_requested_task({});api.assert_not_called()
    def test_cleanup_failure_does_not_change_success(self):
        record=self.record(self.root);record['status']='succeeded'
        with patch.object(r,'report_result'),patch.object(r,'discard_previous'),patch.object(r,'cleanup_worktree',side_effect=RuntimeError('busy')):
            r.finish_cleanup(record)
        result=r.read(r.request_path(record['id']))
        self.assertEqual(result['status'],'succeeded');self.assertEqual(result['cleanup_error'],'busy')
    def test_report_is_idempotent(self):
        record={'id':'candidate','task_id':'task','status':'succeeded'}
        with patch.object(r,'task_api',side_effect=[{'status':'completed'},[{'seq':3,'content':'生产更新 candidate：成功'}]]) as api:
            r.report_result(record);self.assertEqual(api.call_count,2)
    def test_old_cleanup_does_not_delete_newer_recovery_directory(self):
        record=self.record(self.root);record['status']='succeeded'
        current=self.run/'runtime/prod/a';current.mkdir(parents=True)
        previous=self.run/'runtime/prod/b';previous.mkdir()
        r.write(r.meta_path('prod'),{'release_dir':str(current),'commit':'newer'})
        r.write(self.run/'published.json',{'commit':'newer','candidate':'newer-request'})
        r.write(self.run/'deployments/prod-previous.json',{'release_dir':str(previous)})
        with patch.object(r,'report_result'):r.finish_cleanup(record)
        self.assertTrue(previous.exists())
        self.assertTrue((self.run/'deployments/prod-previous.json').exists())

    def test_old_worktree_cleanup_after_newer_publish(self):
        source=self.worktree();record=self.record(source)
        (self.root/'later.txt').write_text('next release');self.commit()
        r.write(self.run/'published.json',{'commit':self.git('rev-parse','HEAD')})
        self.assertEqual(r.cleanup_worktree(record),'removed')

    def test_idle_slot_failure_does_not_leave_building_state(self):
        with patch.object(r,'idle_slot',side_effect=RuntimeError('busy directory')):
            with self.assertRaisesRegex(RuntimeError,'busy directory'):r.prepare_int(self.root,1)
        self.assertEqual(r.candidate()['status'],'failed')

    def test_restore_when_new_processes_have_exited(self):
        old=self.base/'old';old.mkdir()
        r.write(r.meta_path('prod'),{'release_dir':str(self.base/'new'),'pids':{'server':900001,'web':900002,'daemon':900003}})
        r.write(self.run/'deployments/prod-previous.json',{'release_dir':str(old),'published_before':{'commit':self.sha}})
        r.write(r.config_path('prod'),{})
        with patch.object(r,'exists',return_value=False),patch.object(r,'drain') as drain, \
             patch.object(r,'launch',return_value={'server':1,'web':2,'daemon':3}) as launch,patch.object(r,'verify_browser'):
            r.restore('prod')
        drain.assert_not_called()
        launch.assert_called_once()
        self.assertEqual(r.read(r.meta_path('prod'))['release_dir'],str(old))
        self.assertEqual(r.read(self.run/'published.json')['commit'],self.sha)

    def test_reused_web_keeps_nested_next_server_modules(self):
        with (self.root/'.gitignore').open('a') as stream:stream.write('apps/web/.next/\n')
        (self.root/'apps/web/public').mkdir();(self.root/'apps/web/public/favicon.ico').write_text('icon')
        (self.root/'server/migrations').mkdir();(self.root/'server/migrations/001.sql').write_text('select 1')
        self.commit()
        empty=self.base/'initial';empty.mkdir()
        r.write(r.meta_path('int'),{'release_dir':str(empty)})
        original_run=r.run
        def compiler(args, **kw):
            args=[str(x) for x in args]
            if args[0]=='go':
                Path(args[args.index('-o')+1]).write_text('binary');return
            if args[0]=='pnpm':
                if 'build' in args:
                    module=self.root/'apps/web/.next/standalone/apps/web/node_modules/next/dist/server/next-server.js'
                    module.parent.mkdir(parents=True);module.write_text('next server')
                    static=self.root/'apps/web/.next/static';static.mkdir(parents=True);(static/'a.js').write_text('asset')
                return
            return original_run(args,**kw)
        with patch.object(r,'run',side_effect=compiler):r.build(self.root,self.base/'first')
        r.write(r.meta_path('int'),{'release_dir':str(self.base/'first')})
        (self.root/'README.md').write_text('documentation update');self.commit()
        with patch.object(r,'run',side_effect=compiler) as commands:r.build(self.root,self.base/'second')
        self.assertFalse(any(call.args[0][0] in ('go','pnpm') for call in commands.call_args_list))
        self.assertTrue((self.base/'second/apps/web/node_modules/next/dist/server/next-server.js').exists())

    def test_recovery_failure_is_distinct_from_safe_failure(self):
        with patch.object(r,'deploy'),patch.object(r,'verify_browser',side_effect=RuntimeError('browser')), \
             patch.object(r,'restore',side_effect=RuntimeError('old service cannot start')):
            with self.assertRaises(r.RecoveryRequired):r.publish('prod',self.base/'slot')

    def test_contended_publisher_lock_returns_without_waiting(self):
        with r.lock('publisher'):
            with self.assertRaisesRegex(RuntimeError,'结束当前运行'):r.prepare_int(self.root,1)

    def test_previous_slot_cleanup_preserves_current_and_legacy(self):
        current=self.run/'runtime/prod/a';current.mkdir(parents=True)
        prior=self.run/'runtime/prod/b';prior.mkdir()
        r.write(r.meta_path('prod'),{'release_dir':str(current)})
        r.write(self.run/'deployments/prod-previous.json',{'release_dir':str(prior)})
        r.discard_previous('prod');self.assertFalse(prior.exists());self.assertTrue(current.exists())
        legacy=self.run/'releases/old';legacy.mkdir(parents=True)
        r.write(self.run/'deployments/prod-previous.json',{'release_dir':str(legacy)})
        r.discard_previous('prod');self.assertTrue(legacy.exists())

class PublicationFlowTest(unittest.TestCase):
    setUp = ReleaseTest.setUp
    tearDown = ReleaseTest.tearDown
    git = ReleaseTest.git
    commit = ReleaseTest.commit
    worktree = ReleaseTest.worktree
    record = ReleaseTest.record
    def test_worker_promotes_tested_directory_and_records_commit(self):
        source=self.worktree();record=self.record(source)
        record.update(status='queued',timeout=1)
        current=self.base/'int-runtime';current.mkdir()
        r.write(r.meta_path('int'),{**{k:record[k] for k in ('release_id','source_digest','deployed_at')},'release_dir':str(current)})
        r.write(r.candidate_path(),record);r.write(r.request_path(record['id']),record)
        target=self.base/'prod-runtime'
        with patch.object(r,'idle_slot',return_value=target),patch.object(r,'copy_tree') as copy, \
             patch.object(r,'publish') as publish,patch.object(r,'finish_cleanup') as cleanup:
            r.worker(record['id'])
        publish.assert_called_once_with('prod',target,1)
        self.assertEqual(copy.call_args.args,(current,target))
        self.assertEqual(r.read(self.run/'published.json')['commit'],self.sha)
        self.assertEqual(r.read(r.request_path(record['id']))['status'],'succeeded')
        cleanup.assert_called_once()

    def test_worker_does_not_publish_changed_candidate(self):
        source=self.worktree();record=self.record(source);record.update(status='queued',timeout=1)
        r.write(r.meta_path('int'),{'release_id':'other'})
        r.write(r.candidate_path(),record);r.write(r.request_path(record['id']),record)
        with patch.object(r,'publish') as publish:r.worker(record['id'])
        publish.assert_not_called()
        self.assertEqual(r.read(r.request_path(record['id']))['status'],'failed')

    def test_busy_int_does_not_build(self):
        r.write(r.candidate_path(),{'id':'other','status':'verifying'})
        with patch.object(r,'build') as build, self.assertRaisesRegex(RuntimeError,'其他候选'):
            r.prepare_int(self.root,1)
        build.assert_not_called()

if __name__=='__main__':unittest.main()
