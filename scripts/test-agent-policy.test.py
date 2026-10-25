import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
spec=importlib.util.spec_from_file_location('policy',Path(__file__).with_name('test-agent-policy.py'))
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)
class TestPolicy(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup)
  root=Path(self.temp.name);(root/'.run').mkdir();self.file=root/'.run/test-agent-policy.json';self.env={'MULTICA_RELEASE_ROOT':str(root)}
 def save(self,provider='codebuddy'):
  policy=dict(provider=provider,model=p.FLASH if provider=='codebuddy' else 'gpt-6.1-sol',thinking_level='' if provider=='codebuddy' else 'low',service_tier='' if provider=='codebuddy' else 'default')
  self.file.write_text(json.dumps(dict(policy=policy)));return policy
 def test_latest_default_overrides_creator(self):
  chosen=self.save('codex');rt=[dict(id='local',provider='codex',status='online',daemon_id='test')]
  self.assertEqual(p.agent_fields(rt,'test',dict(provider='codebuddy'),self.env),dict(runtime_id='local',**{k:chosen[k] for k in ('model','thinking_level','service_tier')}))
 def test_missing_policy_stops(self):
  with self.assertRaises(RuntimeError):p.selection(environ=self.env)
 def test_provider_unavailable_never_falls_back(self):
  self.save()
  for rt in [[],[dict(id='foreign',provider='codebuddy',status='online',daemon_id='prod')],[dict(id='a',provider='codebuddy',status='online',daemon_id='test'),dict(id='b',provider='codebuddy',status='online',daemon_id='test')]]:
   with self.subTest(rt=rt),self.assertRaises(RuntimeError):p.agent_fields(rt,'test',environ=self.env)
if __name__=='__main__':unittest.main()
