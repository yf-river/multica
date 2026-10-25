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

if __name__=='__main__':unittest.main()
