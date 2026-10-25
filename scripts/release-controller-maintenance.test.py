import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock,patch
spec=importlib.util.spec_from_file_location('maintenance',Path(__file__).with_name('release-controller-maintenance.py'))
m=importlib.util.module_from_spec(spec);spec.loader.exec_module(m)

class MaintenanceTests(unittest.TestCase):
 # 1. Maintenance cannot carry application migrations or files outside its reviewed control-plane boundary.
 def test_source_rejects_business_changes_and_schema_drift(self):
  with tempfile.TemporaryDirectory() as directory:
   root=Path(directory);source=root/'source';old=root/'old'
   for folder in (source,old):(folder/'server/migrations').mkdir(parents=True)
   (old/'server/migrations/001.up.sql').write_text('original')
   (source/'server/migrations/001.up.sql').write_text('original')
   r=Mock();r.RUN=root;r.checked_source.return_value=(source,'commit')
   r.read.side_effect=lambda path:{'commit':'base'} if str(path).endswith('published.json') else {'release_dir':str(old)}
   r.output.return_value='server/internal/supervision/business.go'
   with self.assertRaisesRegex(RuntimeError,'授权范围'):m.validate_source(r,source,'base')
   r.output.return_value='server/internal/handler/issue_delivery_repair.go'
   self.assertEqual(m.validate_source(r,source,'base')[1],'commit')
   (source/'server/migrations/001.up.sql').write_text('changed')
   with self.assertRaisesRegex(RuntimeError,'业务迁移'):m.validate_source(r,source,'base')

 def test_new_user_comment_blocks_previous_maintenance_authorization(self):
  import hashlib
  comment=dict(id='comment',author_type='member',revision=1,content='authorized maintenance')
  record=dict(maintenance=dict(issue_id='issue',comment_id='comment',comment_revision=1,comment_sha256=hashlib.sha256(comment['content'].encode()).hexdigest(),user_comments_version='reviewed',scope='maintenance only',base_commit='base'),source='/source')
  with patch.object(m,'api',side_effect=[[comment],{'user_comments_version':'changed'}]),patch.object(m,'validate_source') as validate:
   with self.assertRaisesRegex(RuntimeError,'新的用户决定'):m.authorize(Mock(),record)
   validate.assert_not_called()

 def test_maintenance_refuses_other_deliveries(self):
  with patch.object(m,'api',return_value=[dict(phase='publishing')]):
   with self.assertRaisesRegex(RuntimeError,'其他交付'):m.idle(Mock(),production=True)

 def test_pending_queue_yields_to_maintenance_but_live_publication_does_not(self):
  with tempfile.TemporaryDirectory() as directory:
   r=Mock();r.candidate_path.return_value=Path(directory)/'missing'
   r.http.return_value=dict(active_tasks=0,claims_in_flight=0)
   def query(_,path):
    return [{'phase':'waiting_int'},{'phase':'approved'}] if path=='delivery-queue' else {'state':'idle'}
   with patch.object(m,'api',side_effect=query):m.idle(r,production=True)
   with patch.object(m,'api',side_effect=lambda _,p:[] if p=='delivery-queue' else {'state':'active'}):
    with self.assertRaisesRegex(RuntimeError,'占用尚未释放'):m.idle(r,production=True)

 def test_authorized_maintenance_reserves_before_waiting_for_active_tasks(self):
  import contextlib
  with tempfile.TemporaryDirectory() as directory:
   r=Mock();r.RUN=Path(directory);r.lock.side_effect=lambda *args:contextlib.nullcontext()
   r.candidate_path.return_value=Path(directory)/'missing'
   with patch.object(m,'validate_source',return_value=(Path(directory),'commit')),patch.object(m,'authorize'),patch.object(m,'idle',side_effect=RuntimeError('active tasks')):
    with self.assertRaisesRegex(RuntimeError,'active tasks'):m.prepare(r,Path(directory),{'base_commit':'base'})
   self.assertEqual(r.save_candidate.call_args[0][0]['status'],'building')

 def test_fenced_record_requires_no_owner_slot_or_process(self):
  import json
  with tempfile.TemporaryDirectory() as directory:
   root=Path(directory);(root/'verification-jobs').mkdir()
   path=root/'verification-jobs/delivery.json'
   r=Mock();r.RUN=root;r.read.side_effect=lambda p:json.loads(p.read_text())
   r.subprocess.run.return_value.stdout=''
   delivery=dict(id='delivery',phase='testing',candidate_id='')
   path.write_text(json.dumps(dict(stage='fenced',lease=None,candidate_id='')))
   with patch.object(m,'api',return_value=[]):
    self.assertTrue(m.fenced_without_work(r,delivery))
    for field,value in [('candidate_id','candidate'),('verification_owner',{'generation':1}),('verification_run',{'status':'running'})]:
     self.assertFalse(m.fenced_without_work(r,dict(delivery,**{field:value})))
    for changed in [dict(stage='building'),dict(stage='fenced',lease={'generation':1}),dict(stage='fenced',candidate_id='candidate')]:
     path.write_text(json.dumps(changed));self.assertFalse(m.fenced_without_work(r,delivery))
    path.write_text(json.dumps(dict(stage='fenced',lease=None,candidate_id='')))
    r.subprocess.run.return_value.stdout='active verification worker'
    self.assertFalse(m.fenced_without_work(r,delivery))
    r.subprocess.run.return_value.stdout=''
   with patch.object(m,'api',return_value=[{'delivery_id':'delivery'}]):
    self.assertFalse(m.fenced_without_work(r,delivery))

# Modification 1. Real Git ancestry permits an included maintenance mainline and rejects concurrent unrelated updates.
class MainlineTests(unittest.TestCase):
 def test_actual_git_mainline_ancestry(self):
  import subprocess
  with tempfile.TemporaryDirectory() as directory:
   def git(*args):return subprocess.check_output(['git',*args],cwd=directory,text=True).strip()
   git('init','-q');git('config','user.name','Test');git('config','user.email','test@example.invalid')
   git('commit','--allow-empty','-qm','base');base=git('rev-parse','HEAD')
   git('commit','--allow-empty','-qm','maintenance mainline');main=git('rev-parse','HEAD')
   git('update-ref','refs/remotes/origin/personal-studio-upstream',main)
   git('commit','--allow-empty','-qm','reviewed candidate');candidate=git('rev-parse','HEAD')
   r=Mock();r.subprocess=subprocess;r.output.side_effect=lambda args,cwd:subprocess.check_output(args,cwd=cwd,text=True).strip()
   record=dict(source=directory,commit=candidate,maintenance=dict(base_commit=base))
   m.check_mainline(r,record)
   git('checkout','-q','--detach',base);git('commit','--allow-empty','-qm','concurrent update')
   git('update-ref','refs/remotes/origin/personal-studio-upstream',git('rev-parse','HEAD'))
   with self.assertRaisesRegex(RuntimeError,'主分支已变化'):m.check_mainline(r,record)

if __name__=='__main__':unittest.main()
