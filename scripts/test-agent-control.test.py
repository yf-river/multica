import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('control',Path(__file__).with_name('test-agent-control.py'))
c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
class Worker:
 def __init__(self,rows):self.rows=rows;self.reports=[]
 def api(self,path,body=None):
  if body is None:return self.rows
  self.reports.append(body)
class TestControl(unittest.TestCase):
 def test_busy_reports_pending_and_does_not_claim_success(self):
  with tempfile.TemporaryDirectory() as directory,patch.dict(c.os.environ,{'MULTICA_RELEASE_ROOT':directory}):
   root=Path(directory);(root/'.run').mkdir()
   worker=Worker([dict(name='int',revision=3,desired=dict(policy=c.FLASH,overrides={}),observed={})])
   audit=dict(after=[dict(id='a',name='A',runtime_id='r',model='old',custom_args=['private'])],runtimes=[dict(id='r',provider='codebuddy')])
   with patch.object(c.subprocess,'run',side_effect=[c.subprocess.CompletedProcess([],0,json.dumps(audit),''),c.subprocess.CompletedProcess([],1,'','测试环境仍有运行')]) as run:
    c.tick(worker)
   self.assertEqual(run.call_count,2)
   report=worker.reports[-1]['observed'];self.assertEqual(report['status'],'pending');self.assertNotIn('custom_args',report['agents'][0])
   self.assertEqual(report['revision'],3)
 def test_unavailable_does_not_switch_provider(self):
  with tempfile.TemporaryDirectory() as directory,patch.dict(c.os.environ,{'MULTICA_RELEASE_ROOT':directory}):
   (Path(directory)/'.run').mkdir();worker=Worker([])
   with patch.object(c.subprocess,'run',return_value=c.subprocess.CompletedProcess([],1,'','HTTP 401')) as run:c.tick(worker)
   self.assertEqual(run.call_count,1);self.assertEqual(worker.reports[-1]['observed']['status'],'error')
if __name__=='__main__':unittest.main()
