import { describe, expect, it } from 'vitest'
import { ToolCallId } from '@deepseek-ai/dsh-llm'
import { pendingInteraction } from '../src/interactions.js'

describe('native pending interactions', () => {
  it('preserves the complete native plan and declared call identity', () => {
    const markdown = '# 发布计划\n\n先完成验收，再执行。\n'
    const pending = pendingInteraction({ questions: [{
      id: 'review', question: 'Approve?', detail: markdown,
      options: [{ label: 'Approve' }, { label: 'Keep planning' }],
      intent: { kind: 'plan-review', approve: 'Approve', callId: ToolCallId('right') },
    }] }, [{ id: 'other', name: 'exit_plan_mode' }, { id: 'right', name: 'exit_plan_mode' }])
    expect(pending).toEqual({ kind: 'plan', call_id: 'right', title: '发布计划', plan_markdown: markdown, questions: [] })
  })

  it('preserves question options, detail and multiple-selection semantics', () => {
    const pending = pendingInteraction({ questions: [{ id: 'target', question: '选择环境', detail: '只选择验收环境', options: [{ label: 'int', description: '固定集成环境' }], multiSelect: true }] }, [{ id: 'ask', name: 'ask_user_question' }])
    expect(pending.questions).toEqual([{ id: 'target', question: '选择环境', detail: '只选择验收环境', options: [{ label: 'int', description: '固定集成环境' }], multi_select: true }])
  })

  it('rejects ambiguous simultaneous calls and duplicate question identities', () => {
    const request = { questions: [{ id: 'one', question: 'Which?' }] }
    expect(() => pendingInteraction(request, [{ id: 'a', name: 'ask_user_question' }, { id: 'b', name: 'ask_user_question' }])).toThrow('cannot identify')
    expect(() => pendingInteraction({ questions: [...request.questions, ...request.questions] }, [{ id: 'a', name: 'ask_user_question' }])).toThrow('identity')
  })

  it('rejects oversized plan data instead of silently truncating it', () => {
    expect(() => pendingInteraction({ questions: [{ id: 'review', question: 'Approve?', detail: '# Plan\n' + 'x'.repeat(256 * 1024), intent: { kind: 'plan-review', approve: 'Approve' } }] }, [{ id: 'a', name: 'exit_plan_mode' }])).toThrow('complete Markdown')
  })
})
