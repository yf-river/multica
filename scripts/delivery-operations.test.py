import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('releases',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
class TestOperations(unittest.TestCase):
 def fixture(self,root):
  scripts=root/'scripts';scripts.mkdir();names=['issue-delivery-worker.py','release-environments.py','new-operation.py']
  for name in names:(scripts/name).write_text('# fixture')
  (scripts/'delivery-operations.json').write_text(json.dumps(dict(version=1,files=names)));return scripts,names
 def test_source_manifest_includes_an_operation_unknown_to_the_installed_controller(self):
  with tempfile.TemporaryDirectory() as folder:
   scripts,names=self.fixture(Path(folder));self.assertEqual(r.operation_files(scripts),('delivery-operations.json',*names))
 def test_missing_file_stops_before_compilation(self):
  with tempfile.TemporaryDirectory() as folder:
   root=Path(folder);scripts,_=self.fixture(root);(scripts/'new-operation.py').unlink()
   with patch.object(r,'checked_source',return_value=(root,'a'*40)),patch.object(r,'executor_preflight') as preflight,patch.object(r,'run') as run:
    with self.assertRaisesRegex(RuntimeError,'缺少文件'):r.build(root,root/'output')
    preflight.assert_not_called();run.assert_not_called();self.assertFalse((root/'output').exists())
 def test_manifest_rejects_escape_and_missing_entry(self):
  with tempfile.TemporaryDirectory() as folder:
   root=Path(folder);scripts,names=self.fixture(root)
   for files in [names+['../private'],['new-operation.py'],names+['new-operation.py']]:
    (scripts/'delivery-operations.json').write_text(json.dumps(dict(version=1,files=files)))
    with self.assertRaises(RuntimeError):r.operation_files(scripts)
if __name__=='__main__':unittest.main()
