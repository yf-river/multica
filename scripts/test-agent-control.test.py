import importlib.util
import json
from pathlib import Path
import tempfile
from datetime import datetime, timezone
import unittest
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('control',Path(__file__).with_name('test-agent-control.py'))
c=importlib.util.module_from_spec(spec);spec.loader.exec_module(c)
class Worker:
 def __init__(self,rows):self.rows=rows;self.reports=[]
 def api(self,path,body=None):
  if body is None:
   if path=='delivery-queue/slots':return [dict(name='verify-1',enabled=True)]
   return self.rows
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
 def test_preserves_reported_catalog_and_matches_actual_runtime(self):
  with tempfile.TemporaryDirectory() as directory,patch.dict(c.os.environ,{'MULTICA_RELEASE_ROOT':directory}):
   (Path(directory)/'.run').mkdir()
   policy=dict(provider='codebuddy',runtime='dsh',model='codebuddy/flash',thinking_level='',service_tier='')
   catalog=[dict(id='r',provider='dsh',catalog_observed_at=datetime.now(timezone.utc).isoformat(),models=[dict(id='codebuddy/flash',label='Flash')])]
   worker=Worker([dict(name='int',revision=3,desired=dict(policy=policy,overrides={}),observed=dict(revision=3,runtimes=catalog))])
   audit=dict(after=[dict(id='a',name='A',runtime_id='r',model='codebuddy/flash',thinking_level='',service_tier='')],runtimes=[dict(id='r',provider='dsh')])
   with patch.object(c.subprocess,'run',return_value=c.subprocess.CompletedProcess([],0,json.dumps(audit),'')) as run:c.tick(worker)
   self.assertEqual(run.call_count,1)
   self.assertEqual(worker.reports[-1]['observed']['runtimes'],catalog)
   self.assertEqual(worker.reports[-1]['observed']['status'],'applied')
 def test_current_but_unsupported_model_is_not_reported_as_applied(self):
  with tempfile.TemporaryDirectory() as directory,patch.dict(c.os.environ,{'MULTICA_RELEASE_ROOT':directory}):
   (Path(directory)/'.run').mkdir()
   catalog=[dict(id='r',provider='codebuddy',catalog_observed_at=datetime.now(timezone.utc).isoformat(),models=[dict(id='different',label='Different')])]
   worker=Worker([dict(name='int',revision=3,desired=dict(policy=c.FLASH,overrides={}),observed=dict(revision=3,runtimes=catalog))])
   audit=dict(after=[dict(id='a',name='A',runtime_id='r',model=c.FLASH['model'],thinking_level='',service_tier='')],runtimes=[dict(id='r',provider='codebuddy')])
   with patch.object(c.subprocess,'run',return_value=c.subprocess.CompletedProcess([],0,json.dumps(audit),'')) as run:c.tick(worker)
   self.assertEqual(run.call_count,1)
   self.assertEqual(worker.reports[-1]['observed']['status'],'error')
   self.assertEqual(worker.reports[-1]['observed']['error'],'所选模型不在本环境运行时目录中')
 def test_disabled_slot_is_not_touched(self):
  with tempfile.TemporaryDirectory() as directory,patch.dict(c.os.environ,{'MULTICA_RELEASE_ROOT':directory}):
   root=Path(directory);(root/'.run').mkdir();(root/'.run/test-agent-control-poll.json').write_text(json.dumps(dict(at=0,index=0)))
   worker=Worker([dict(name='verify-1',revision=3,desired=dict(policy=c.FLASH,overrides={}),observed=dict(agents=[dict(id='a')]))])
   original=worker.api
   worker.api=lambda path,body=None: [dict(name='verify-1',enabled=False)] if path=='delivery-queue/slots' else original(path,body)
   with patch.object(c.subprocess,'run') as run:c.tick(worker)
   run.assert_not_called();report=worker.reports[-1]['observed']
   self.assertEqual(report['status'],'pending');self.assertEqual(report['agents'],[dict(id='a')])
   self.assertEqual(report['error'],'验收槽位未启用，启用后自动应用')
 def test_missing_process_reports_recovery_required(self):
  with tempfile.TemporaryDirectory() as directory,patch.dict(c.os.environ,{'MULTICA_RELEASE_ROOT':directory}):
   (Path(directory)/'.run').mkdir();worker=Worker([])
   with patch.object(c.subprocess,'run',return_value=c.subprocess.CompletedProcess([],1,'','verify-1 server PID 工作目录不属于记录的版本')):c.tick(worker)
   self.assertEqual(worker.reports[-1]['observed']['error'],'环境进程与部署记录不一致，请检查环境状态')
 def test_unavailable_does_not_switch_provider(self):
  with tempfile.TemporaryDirectory() as directory,patch.dict(c.os.environ,{'MULTICA_RELEASE_ROOT':directory}):
   (Path(directory)/'.run').mkdir();worker=Worker([])
   with patch.object(c.subprocess,'run',return_value=c.subprocess.CompletedProcess([],1,'','HTTP 401')) as run:c.tick(worker)
   self.assertEqual(run.call_count,1);self.assertEqual(worker.reports[-1]['observed']['status'],'error')
if __name__=='__main__':unittest.main()
