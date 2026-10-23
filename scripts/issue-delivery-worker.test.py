import importlib.util,json,subprocess,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('delivery_worker',Path(__file__).with_name('issue-delivery-worker.py'))
w=importlib.util.module_from_spec(spec);spec.loader.exec_module(w);r=w.r

class DeliveryTests(unittest.TestCase):
    def test_deferred_published_notice_does_not_starve_other_work(self):
        worker=w.Worker.__new__(w.Worker);worker.env='prod'
        rows=[dict(id='deferred',phase='published'),dict(id='next',phase='needs_changes')]
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'recover_release_windows'),patch.object(r,'lock'),patch.object(r,'candidate_path',return_value=Path(folder)/'missing'),patch.object(worker,'api',return_value=rows),patch.object(worker,'recover_finished_publications'),patch.object(worker,'pool_enabled',return_value=False),patch.object(worker,'pool_dispatch',return_value=set()),patch.object(worker,'batch_config',return_value=False),patch.object(worker,'pin'),patch.object(worker,'advance',side_effect=[{'notification_deferred':True},{}]) as advance:
            worker.tick()
            self.assertEqual([call.args[0]['id'] for call in advance.call_args_list],['deferred','next'])

    def test_install_bundles_exact_database_checker(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp);units=root/'units';units.mkdir()
            def paths(value):
                return units if str(value)=='/etc/systemd/system' else Path(value)
            with patch.object(w,'Path',side_effect=paths),patch.object(r,'RUN',root),patch.object(r,'require_control_directory'),patch.object(r,'run') as run:
                w.install('prod')
            bundles=list((root/'publisher').glob('delivery-prod-*'))
            self.assertEqual(len(bundles),1)
            self.assertEqual((bundles[0]/'check-release-database.mjs').read_bytes(),Path(w.__file__).with_name('check-release-database.mjs').read_bytes())
            self.assertIn(str(bundles[0]/'issue-delivery-worker.py'),(units/'multica-delivery-prod.service').read_text())
            self.assertTrue(run.called)
    def git(self,root,*args):return subprocess.check_output(['git','-C',str(root),*args],stderr=subprocess.DEVNULL,text=True).strip()
    def test_merge_distinguishes_real_conflicts_from_environment_errors(self):
        for conflict in (False,True):
            with self.subTest(conflict=conflict),tempfile.TemporaryDirectory() as folder:
                root=Path(folder);self.git(root,'init');self.git(root,'config','user.name','Fixture');self.git(root,'config','user.email','fixture@example.test')
                (root/'common').write_text('base');self.git(root,'add','.');self.git(root,'commit','-qm','base');base=self.git(root,'rev-parse','HEAD')
                self.git(root,'checkout','-qb','side');(root/('common' if conflict else 'side')).write_text('side');self.git(root,'add','.');self.git(root,'commit','-qm','side')
                self.git(root,'checkout','-qb','main',base);(root/('common' if conflict else 'main')).write_text('main');self.git(root,'add','.');self.git(root,'commit','-qm','main')
                self.assertEqual(w.merge_source(root,['--no-ff','--no-edit','side']),not conflict)
                self.assertEqual(self.git(root,'status','--porcelain'),'')
                self.assertNotEqual(subprocess.run(['git','rev-parse','--verify','--quiet','MERGE_HEAD'],cwd=root,stdout=subprocess.DEVNULL).returncode,0)
                if not conflict:self.assertEqual(len(self.git(root,'show','-s','--format=%P','HEAD').split()),2)
                with patch.object(w.subprocess,'run',wraps=subprocess.run) as commands:
                    with self.assertRaisesRegex(RuntimeError,'missing-ref'):w.merge_source(root,['--no-edit','missing-ref'])
                    self.assertNotIn(['git','merge','--abort'],[list(c.args[0]) for c in commands.call_args_list])

    def test_merge_cleanup_error_preserves_original_failure(self):
        results=[subprocess.CompletedProcess([],1,'','ORIGINAL_GIT_FAILURE'),subprocess.CompletedProcess([],0,'conflicted-file',''),subprocess.CompletedProcess([],0,'merge-head',''),subprocess.CompletedProcess([],1,'','CLEANUP_FAILURE')]
        with patch.object(w.subprocess,'run',side_effect=results):
            with self.assertRaises(r.RecoveryRequired) as error:w.merge_source(Path('/controlled'),['target'])
        self.assertIn('ORIGINAL_GIT_FAILURE',str(error.exception));self.assertIn('CLEANUP_FAILURE',str(error.exception))

    def test_nonconflict_failure_with_merge_head_is_cleaned_but_not_mislabeled(self):
        results=[subprocess.CompletedProcess([],1,'','HOOK_REJECTED'),subprocess.CompletedProcess([],0,'',''),subprocess.CompletedProcess([],0,'merge-head',''),subprocess.CompletedProcess([],0,'','')]
        with patch.object(w.subprocess,'run',side_effect=results):
            with self.assertRaisesRegex(RuntimeError,'HOOK_REJECTED'):w.merge_source(Path('/controlled'),['target'])

    def test_pinned_source_survives_original_worktree_removal_and_branch_changes(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder)/'repo';root.mkdir();self.git(root,'init');self.git(root,'config','user.name','Fixture');self.git(root,'config','user.email','fixture@example.test')
            (root/'file').write_text('original');self.git(root,'add','.');self.git(root,'commit','-qm','base');sha=self.git(root,'rev-parse','HEAD')
            d=dict(id='test-delivery',source_commit=sha,source_branch='feature/example')
            worker=object.__new__(w.Worker)
            with patch.object(r,'ROOT',root),patch.object(r,'RUN',root/'.run'):
                path=worker.source(d)
                self.assertEqual((path/'file').read_text(),'original')
                self.git(root,'worktree','remove',str(path))
                (root/'file').write_text('later');self.git(root,'commit','-am','later','-q')
                restored=worker.source(d)
                self.assertEqual(self.git(restored,'rev-parse','HEAD'),sha)
                self.assertEqual((restored/'file').read_text(),'original')
                self.assertEqual(self.git(root,'rev-parse','refs/multica/deliveries/test-delivery'),sha)
    def test_busy_int_keeps_delivery_queued_without_launching_or_rerunning_agent(self):
        worker=object.__new__(w.Worker);worker.env='prod';d={'id':'one','phase':'waiting_int'}
        with patch.object(worker,'api',return_value=[d]),patch.object(worker,'pin'),patch.object(worker,'busy',return_value=True),patch.object(worker,'advance') as advance,patch.object(r,'candidate_path',return_value=Path('/does-not-exist')),tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            worker.tick();advance.assert_not_called()
    def test_int_worker_cannot_publish_production(self):
        worker=object.__new__(w.Worker);worker.env='int'
        with patch.object(r,'request_release') as publish:
            with self.assertRaisesRegex(RuntimeError,'不能更新生产'):worker.publish({})
            publish.assert_not_called()
    def test_invalid_authorization_stops_before_merge_and_push(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        candidate={'status':'accepted','source':'/fixture','commit':'a'*40,'id':'candidate'}
        with patch.object(worker,'own_candidate',return_value=candidate),patch.object(r,'run') as command,patch.object(r,'output',side_effect=['','personal-studio-upstream']),patch.object(subprocess,'run') as ancestry,patch.object(worker,'advance',side_effect=RuntimeError('authorization changed')),patch.object(r,'request_release') as request,patch.object(worker,'publication'):
            ancestry.return_value.returncode=0
            with self.assertRaisesRegex(RuntimeError,'authorization changed'):worker.publish({'id':'delivery'})
            self.assertEqual([call.args[0][1] for call in command.call_args_list],['fetch'])
            request.assert_not_called()

    def test_publishing_recovery_rechecks_then_finishes_git_before_request(self):
        worker=object.__new__(w.Worker);worker.env='prod'
        candidate={'status':'accepted','source':'/fixture','commit':'a'*40,'id':'candidate'}
        events=[]
        with patch.object(worker,'own_candidate',return_value=candidate),patch.object(r,'run',side_effect=lambda args,**kw:events.append(args[1])),patch.object(r,'output',side_effect=['a'*40+' refs/heads/personal-studio-upstream']),patch.object(subprocess,'run') as ancestry,patch.object(worker,'advance') as advance,patch.object(r,'check_delivery_approval',side_effect=lambda c:events.append('authorization')),patch.object(r,'request_release',side_effect=lambda c:events.append('request')),patch.object(worker,'publication'):
            ancestry.return_value.returncode=0
            worker.publish({'id':'delivery','phase':'publishing'})
            self.assertEqual(events,['fetch','authorization','push','request'])
            advance.assert_not_called()

    def test_normal_request_requires_human_approved_delivery(self):
        with self.assertRaisesRegex(RuntimeError,'交付审核'):r.check_delivery_approval({'commit':'a'*40})
    def test_schema_change_rejected_before_mutation_and_stop(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);old=root/'old';new=root/'new';(old/'server/migrations').mkdir(parents=True);(new/'server/migrations').mkdir(parents=True)
            (new/'server/migrations/999_bad.up.sql').write_text('ALTER TABLE issue DROP COLUMN title;')
            with patch.object(r,'migrate') as migrate,patch.object(r,'check_database') as check:
                with self.assertRaisesRegex(RuntimeError,'兼容方案'):r.prepare_database(new,{},old)
                migrate.assert_not_called();check.assert_not_called()
    def test_additive_migration_checks_old_and_new_queries(self):
        with tempfile.TemporaryDirectory() as folder:
            root=Path(folder);old=root/'old';new=root/'new';(old/'server/migrations').mkdir(parents=True);(new/'server/migrations').mkdir(parents=True)
            (new/'server/migrations/999_safe.up.sql').write_text('CREATE TABLE delivery_fixture(id uuid);')
            with patch.object(r,'migrate') as migrate,patch.object(r,'check_database') as check:
                r.prepare_database(new,{'identity':'int'},old)
                self.assertEqual(check.call_args_list[0].args[0],old);self.assertEqual(check.call_args_list[1].args[0],new);migrate.assert_called_once()

    def test_review_state_releases_previous_slot_after_process_restart(self):
        import uuid
        identity=str(uuid.uuid4());d={'id':identity,'phase':'awaiting_review','revision':2,'source_commit':'a'*40}
        current={'id':'candidate','delivery_id':identity,'status':'accepted'}
        def api(path,body=None,method=None):
            # 队列列表返回待复核交付；推进回执按普通对象处理，验证进程重启后仍会释放旧槽位。
            return {} if path.endswith('/advance') else [d]
        worker=object.__new__(w.Worker);worker.env='prod'
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(r,'candidate_path',return_value=Path(__file__)),patch.object(r,'candidate',return_value=current),patch.object(worker,'api',side_effect=api),patch.object(worker,'pin'),patch.object(r,'release_candidate') as release:
            worker.tick();release.assert_called_once_with('candidate')
    def test_restart_finishes_lease_even_after_candidate_was_released(self):
        import uuid
        identity=str(uuid.uuid4());d=dict(id=identity,phase='cancelled')
        worker=object.__new__(w.Worker)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            path=worker.publication_path(d);path.parent.mkdir();path.write_text('{}')
            with patch.object(worker,'api',return_value=[d]) as api,patch.object(worker,'own_candidate',return_value={'id':'candidate','status':'released'}),patch.object(r,'request_path',return_value=Path(folder)/'missing'),patch.object(r,'read',return_value={}),patch.object(r,'check_process_ownership'),patch.object(worker,'finish_publication') as finish:
                worker.recover_finished_publications([])
                api.assert_called_once_with('delivery-queue?current='+identity)
                finish.assert_called_once_with(d)
    def test_restart_keeps_lease_while_publication_is_still_running(self):
        import uuid
        identity=str(uuid.uuid4());d=dict(id=identity,phase='published')
        worker=object.__new__(w.Worker)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            path=worker.publication_path(d);path.parent.mkdir();path.write_text('{}')
            request=Path(folder)/'request';request.write_text('{}')
            with patch.object(worker,'own_candidate',return_value={'id':'candidate','status':'succeeded'}),patch.object(r,'request_path',return_value=request),patch.object(r,'read',return_value={'status':'publishing'}),patch.object(worker,'finish_publication') as finish:
                with self.assertRaises(r.RecoveryRequired):worker.recover_finished_publications([d])
                finish.assert_not_called()
    def test_release_response_loss_only_removes_already_released_local_lease(self):
        import uuid
        d=dict(id=str(uuid.uuid4()))
        worker=object.__new__(w.Worker)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)):
            path=worker.publication_path(d);path.parent.mkdir();path.write_text(json.dumps({'generation':3}))
            with patch.object(worker,'api',return_value={'state':'idle','generation':3}),patch.object(worker,'publication') as publication:
                worker.finish_publication(d)
                self.assertFalse(path.exists());publication.assert_not_called()
    def test_claim_response_loss_reuses_current_generation_with_original_credentials(self):
        import uuid,urllib.error
        d=dict(id=str(uuid.uuid4()),worker_token=str(uuid.uuid4()),source_commit='a'*40)
        worker=object.__new__(w.Worker)
        current=dict(delivery_id=d['id'],generation=7,expired=False)
        receipt=dict(delivery_id=d['id'],worker_token=d['worker_token'],generation=7)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(worker,'api',side_effect=[urllib.error.HTTPError('fixture',409,'conflict',{},None),current,receipt]) as api:
            self.assertEqual(worker.publication(d),receipt)
            self.assertEqual(d['publication_generation'],7)
            check=api.call_args.args[1]
            self.assertEqual((check['action'],check['worker_token'],check['generation']),('check',d['worker_token'],7))
    def test_verifying_candidate_is_not_remerged_on_resume(self):
        worker=object.__new__(w.Worker);worker.env='prod';d={'id':'one','source_commit':'a'*40}
        candidate={'id':'candidate','status':'verifying','commit':'a'*40,'source':'/fixture'}
        with patch.object(worker,'busy',return_value=False),patch.object(worker,'own_candidate',return_value=candidate),patch.object(worker,'source') as source,patch.object(worker,'advance'),patch.object(r,'validate_candidate') as validate,patch.object(r,'read',return_value={'frontend_url':'http://int'}),patch.object(worker,'publication',return_value={'generation':1}):
            worker.prepare(d);source.assert_not_called();validate.assert_called_once_with(candidate)

    def test_dead_startup_process_fails_without_waiting_for_http_timeout(self):
        with patch.object(r,'exists',return_value=False),patch.object(r,'http') as http:
            with self.assertRaisesRegex(RuntimeError,'启动进程已退出'):r.ready('http://fixture',pid=123)
            http.assert_not_called()

    def test_coordinator_exit_keeps_application_processes_alive(self):
        files={}
        original=Path.write_text
        def write(path,text,*args,**kwargs):
            if str(path).startswith('/etc/systemd/system/'):
                files[path.name]=text;return len(text)
            return original(path,text,*args,**kwargs)
        with tempfile.TemporaryDirectory() as folder,patch.object(r,'RUN',Path(folder)),patch.object(r,'require_control_directory'),patch.object(r,'run') as command,patch.object(Path,'write_text',write):
            w.install('int')
            self.assertIn('KillMode=process',files['multica-delivery-int.service'])
            self.assertEqual(files['multica-delivery-int.service'].count('Environment=HOME='),1)
            self.assertIn('OnBootSec=30s',files['multica-delivery-int.timer'])
            self.assertIn('OnUnitInactiveSec=15s',files['multica-delivery-int.timer'])
            self.assertIn('--now',command.call_args.args[0])

if __name__=='__main__':unittest.main()
