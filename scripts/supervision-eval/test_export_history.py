import unittest
import hashlib
import json
from pathlib import Path
import tempfile

from export_history import export, sanitize, visible


class HistoryExportTests(unittest.TestCase):
    def test_secret_equality_survives_without_revealing_values(self):
        salt=b'fixed-test-salt-value-with-32-bytes'
        a=sanitize({'password':'first-secret','text':'password=first-secret Bearer second-secret-value'},salt)
        self.assertNotIn('first-secret',str(a))
        self.assertNotIn('second-secret-value',str(a))
        self.assertIn(a['password'],a['text'])
        self.assertNotEqual(sanitize({'password':'different'},salt)['password'],a['password'])
        wrapped=sanitize({'superPassword':'first-secret','accessToken':'second-secret-value','captchaAnswer':'third-secret'},salt)
        self.assertNotIn('first-secret',str(wrapped))
        self.assertNotIn('second-secret-value',str(wrapped))
        self.assertNotIn('third-secret',str(wrapped))

    def test_only_visible_messages_and_actual_tools_enter_dataset(self):
        self.assertIsNone(visible(dict(type='message',role='developer',content=[dict(text='private instruction')])) )
        for channel in ['analysis','summary']:
            self.assertIsNone(visible(dict(type='message',role='assistant',channel=channel,content=[dict(text='private reasoning')])))
        call=visible(dict(type='custom_tool_call',name='functions.exec',call_id='a',input='await tools.exec_command({cmd:"git diff"})'))
        self.assertEqual(call['name'],'functions.exec')
        self.assertEqual(visible(dict(type='custom_tool_call_output',call_id='a',output='exit_code=1'))['output'],'exit_code=1')

    def test_latest_user_decision_survives_material_budget(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory);source=root/'source.jsonl';output=root/'export';output.mkdir()
            events=[dict(type='response_item',timestamp='2026-10-19T01:00:00Z',payload=dict(type='message',role='assistant',content=[dict(text='过程记录')])) for _ in range(24)]
            events.append(dict(type='response_item',timestamp='2026-10-19T02:00:00Z',payload=dict(type='message',role='user',content=[dict(text='/note 生产发布需要等我最终确认。')])) )
            raw=('\n'.join(json.dumps(e,ensure_ascii=False) for e in events)+'\n').encode();source.write_bytes(raw)
            spec=dict(id='history',family='task',title='task',requirement='核验执行是否遵守用户决定',windows=[dict(source=str(source),first=1,last=25,sha256=hashlib.sha256(raw).hexdigest())])
            export(dict(cases=[spec]),output,b'test-salt')
            case=json.loads((output/'cases.json').read_text())['cases'][0]
            self.assertEqual(len(case['materials']),24)
            self.assertNotIn('生产发布',str(case['materials']))
            self.assertEqual(case['current']['latest_user_comments'][0]['content'],'/note 生产发布需要等我最终确认。')


if __name__=='__main__':unittest.main()
