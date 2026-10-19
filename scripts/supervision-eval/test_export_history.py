import unittest

from export_history import sanitize, visible


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
        for channel in ['analysis','summary']:
            self.assertIsNone(visible(dict(type='message',role='assistant',channel=channel,content=[dict(text='private reasoning')])))
        call=visible(dict(type='custom_tool_call',name='functions.exec',call_id='a',input='await tools.exec_command({cmd:"git diff"})'))
        self.assertEqual(call['name'],'functions.exec')
        self.assertEqual(visible(dict(type='custom_tool_call_output',call_id='a',output='exit_code=1'))['output'],'exit_code=1')


if __name__=='__main__':unittest.main()
