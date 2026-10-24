import importlib.util
from pathlib import Path
import unittest
spec=importlib.util.spec_from_file_location('policy',Path(__file__).with_name('test-agent-policy.py'))
p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)

class TestPolicy(unittest.TestCase):
    def test_manual_initialization_defaults_to_flash(self):
        rt=[dict(id='local',provider='codebuddy',status='online',daemon_id='test')]
        self.assertEqual(p.agent_fields(rt,'test',environ={})['model'],p.FLASH)
    def test_inherits_complete_actual_codex_configuration(self):
        context=dict(task_id='run',resolved=True,provider='codex',model='gpt-6.1-sol',thinking_level='low',service_tier='default')
        rt=[dict(id='foreign',provider='codex',status='online',daemon_id='prod'),dict(id='local',provider='codex',status='online',daemon_id='test')]
        self.assertEqual(p.agent_fields(rt,'test',context,{'MULTICA_TASK_ID':'run'}),dict(runtime_id='local',model='gpt-6.1-sol',thinking_level='low',service_tier='default'))
    def test_unknown_or_overridden_context_does_not_default_to_flash(self):
        for values in [dict(resolved=False),dict(task_id='other'),dict(model=''),dict(service_tier=''),dict(thinking_level='')]:
            context=dict(task_id='run',resolved=True,provider='codex',model='gpt-6.1-sol',thinking_level='low',service_tier='default');context.update(values)
            with self.subTest(values=values),self.assertRaises(RuntimeError):p.selection(context,{'MULTICA_TASK_ID':'run'})
    def test_missing_context_does_not_read_cli_user_default(self):
        with self.assertRaises(RuntimeError):p.selection(environ={'MULTICA_TASK_ID':'run','TMPDIR':'/nonexistent'})
    def test_provider_unavailable_never_selects_another_provider(self):
        for rt in [[],[dict(id='claude',provider='claude',status='online',daemon_id='test')],[dict(id='a',provider='codebuddy',status='online',daemon_id='test'),dict(id='b',provider='codebuddy',status='online',daemon_id='test')]]:
            with self.subTest(rt=rt),self.assertRaises(RuntimeError):p.agent_fields(rt,'test',environ={})

if __name__=='__main__':unittest.main()
