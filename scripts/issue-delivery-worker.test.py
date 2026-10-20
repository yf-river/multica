import importlib.util,json,subprocess,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('delivery_worker',Path(__file__).with_name('issue-delivery-worker.py'))
w=importlib.util.module_from_spec(spec);spec.loader.exec_module(w);r=w.r

class DeliveryTests(unittest.TestCase):
    def git(self,root,*args):return subprocess.check_output(['git','-C',str(root),*args],stderr=subprocess.DEVNULL,text=True).strip()
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

if __name__=='__main__':unittest.main()
