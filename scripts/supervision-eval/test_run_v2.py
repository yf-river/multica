"""回放器只验证状态转移，模型是否找对问题由独立事实评分。"""
import unittest

from run_v2 import advance_ledger, advance_pending, merge_pending, select_batch


class ReplayTests(unittest.TestCase):
    def test_failed_batch_preserves_pending_and_new_generation(self):
        old=dict(kind='comment',id='a',generation=1,offset=0,text='旧评论')
        new=dict(old,generation=2,text='编辑后的评论')
        late=dict(kind='trace',id='late',generation=1,offset=0,text='迟到消息')
        rows=merge_pending([old],[new,late])
        self.assertEqual(rows,[new,late])
        self.assertEqual(merge_pending([old],[dict(old,offset=8000)]),[old])

    def test_rechecking_uses_host_id_and_resolution_preserves_history(self):
        finding=dict(id='',code='mapping',title='字段错误',evidence=[dict(kind='trace',id='a')])
        ledger=advance_ledger({},dict(findings=[finding],resolutions=[]),'story')
        key=next(iter(ledger))
        update=dict(finding,id=key,title='相同问题的新证据',evidence=[dict(kind='trace',id='b')])
        ledger=advance_ledger(ledger,dict(findings=[update],resolutions=[]),'story')
        self.assertEqual(len(ledger),1)
        closed=advance_ledger(ledger,dict(findings=[],resolutions=[dict(id=key)]),'story')
        self.assertEqual(closed[key]['status'],'resolved')
        self.assertEqual(ledger[key]['status'],'open')

    def test_new_id_does_not_silently_merge_semantically_similar_findings(self):
        a=dict(id='',code='mapping',evidence=[dict(kind='trace',id='a')])
        b=dict(a,evidence=[dict(kind='trace',id='b')])
        ledger=advance_ledger({},dict(findings=[a],resolutions=[]),'story')
        ledger=advance_ledger(ledger,dict(findings=[b],resolutions=[]),'story')
        self.assertEqual(len(ledger),2)

    def test_valid_first_page_keeps_unread_tail(self):
        full=dict(kind='trace',id='long',generation=1,offset=0,end=9001,length=9001,text='租'*9001)
        first=select_batch([full])
        self.assertEqual(first[0]['end'],8000)
        tail=advance_pending([full],first,{('trace','long'):full})
        self.assertEqual(tail[0]['offset'],8000)
        self.assertEqual(tail[0]['text'],'租'*1001)
        self.assertEqual(advance_pending(tail,select_batch(tail),{('trace','long'):full}),[])

    def test_failed_or_undelivered_material_is_not_consumed(self):
        rows=[dict(kind='trace',id=str(i),generation=1,offset=0,end=8000,length=8000,text='x'*8000) for i in range(9)]
        first=select_batch(rows)
        self.assertEqual(len(first),6)
        self.assertEqual(len(advance_pending(rows,first,{})),3)
        self.assertEqual(len(select_batch([dict(r,text='',end=0,length=0) for r in rows]*4)),24)
        with self.assertRaisesRegex(ValueError,'缺少未读原文'):
            advance_pending([rows[0]],[dict(rows[0],end=1000)],{})


if __name__=='__main__':
    unittest.main()
