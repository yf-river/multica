"""预检真实仓库与执行器；缺依赖/错误版本使用受控子进程结果，不执行构建或迁移。"""
import importlib.util,os,subprocess,tempfile,unittest,json
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('release',Path(__file__).with_name('release-environments.py'))
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
ROOT=Path(__file__).resolve().parent.parent
class PreflightTest(unittest.TestCase):
 def test_real_executor_and_source(self):
  result=r.executor_preflight(ROOT,integration=True)
  self.assertEqual(result['status'],'passed');self.assertTrue(result['git_identity_checked'])
  self.assertEqual(result['controller_matches_source'],True)
  self.assertNotIn('HOME',result);self.assertNotIn('token',json.dumps(result))
 def test_home_rejected_before_dependency_calls(self):
  with patch.object(r.subprocess,'run') as run,self.assertRaisesRegex(RuntimeError,'HOME'):
   r.executor_preflight(ROOT,env={**os.environ,'HOME':'/path/not/present/for-preflight'})
  run.assert_not_called()
 def test_missing_tool_before_install_or_build(self):
  original=r.shutil.which
  with patch.object(r.shutil,'which',side_effect=lambda name,**kw: None if name=='pnpm' else original(name,**kw)),self.assertRaisesRegex(RuntimeError,'缺少 pnpm'):
   r.executor_preflight(ROOT)
 def test_wrong_package_version_rejected(self):
  original=r.subprocess.run
  def execute(args,**kw):
   if args[0].endswith('/pnpm'):return subprocess.CompletedProcess(args,0,'0.0.1','')
   return original(args,**kw)
  with patch.object(r.subprocess,'run',side_effect=execute),self.assertRaisesRegex(RuntimeError,'packageManager'):
   r.executor_preflight(ROOT)
 def test_failure_does_not_expose_stderr_credentials(self):
  with patch.object(r.subprocess,'run',side_effect=subprocess.CalledProcessError(1,['git'],stderr='secret-token')):
   with self.assertRaisesRegex(RuntimeError,'git --version') as error:r.executor_preflight(ROOT)
  self.assertNotIn('secret-token',str(error.exception))
 def test_failed_preflight_precedes_build_side_effects(self):
  with tempfile.TemporaryDirectory() as directory:
   target=Path(directory)/'artifact'
   with patch.object(r,'checked_source',return_value=(ROOT,'abc')),patch.object(r,'executor_preflight',side_effect=RuntimeError('missing tool')),patch.object(r,'run') as commands:
    with self.assertRaisesRegex(RuntimeError,'missing tool'):r.build(ROOT,target)
   commands.assert_not_called();self.assertFalse(target.exists())
 def test_cli_not_ready_is_not_passed(self):
  import io
  stream=io.StringIO()
  with patch('sys.argv',['release','preflight','--source',str(ROOT)]),patch('sys.stdout',stream),patch.object(r,'require_control_directory'),patch.object(r,'executor_preflight',return_value={'status':'passed'}),patch.object(r,'read',return_value={'backend_url':'http://fixture'}),patch.object(r,'readiness_snapshot',return_value={'http_status':503,'body':{'status':'not_ready'}}):
   with self.assertRaises(SystemExit) as error:r.main()
  self.assertEqual(error.exception.code,1);self.assertEqual(json.loads(stream.getvalue())['status'],'blocked')
 def test_build_stage_records_failure_and_time(self):
  import io
  stream=io.StringIO()
  with patch('sys.stderr',stream),self.assertRaisesRegex(RuntimeError,'fixture'):
   with r.build_stage('compile'):raise RuntimeError('fixture')
  rows=[json.loads(x) for x in stream.getvalue().splitlines()]
  self.assertEqual([x['status'] for x in rows],['started','failed']);self.assertGreaterEqual(rows[1]['elapsed_ms'],0)
if __name__=='__main__':unittest.main()
