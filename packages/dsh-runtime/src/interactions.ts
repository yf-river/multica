import type { AskUserQuestionRequest } from '@deepseek-ai/dsh-user-questions'

export interface PendingQuestion {
  id: string
  header?: string
  question: string
  detail?: string
  options: Array<{ label: string; description?: string }>
  multi_select: boolean
}

export interface PendingInteraction {
  kind: 'plan' | 'question'
  call_id: string
  title: string
  plan_markdown?: string
  questions: PendingQuestion[]
}

// 调用身份取自原生请求或唯一在途调用，不能把并行问题配给错误的工具结果。
export function pendingInteraction(
  request: AskUserQuestionRequest,
  inFlight: ReadonlyArray<{ id: string; name: string }>,
): PendingInteraction {
  const plan = request.questions.find(question => question.intent?.kind === 'plan-review')
  const tool = plan === undefined ? 'ask_user_question' : 'exit_plan_mode'
  const declared = plan?.intent?.callId ?? request.wait?.callId
  const candidates = inFlight.filter(call => call.name === tool && (declared === undefined || call.id === String(declared)))
  if (candidates.length !== 1) throw new Error('cannot identify the native human-interaction tool call')
  const callId = candidates[0]!.id
  if (plan !== undefined) {
    const markdown = plan.detail ?? ''
    if (!/^#\s+\S/.test(markdown.trim()) || Buffer.byteLength(markdown) > 256 * 1024) {
      throw new Error('plan review requires complete Markdown within 256 KiB')
    }
    return {
      kind: 'plan', call_id: callId,
      title: markdown.trim().split('\n', 1)[0]!.replace(/^#\s+/, '').trim(),
      plan_markdown: markdown, questions: [],
    }
  }
  if (request.questions.length === 0 || request.questions.length > 10) throw new Error('human interaction requires 1 to 10 questions')
  const seen = new Set<string>()
  const questions = request.questions.map(question => {
    if (!question.id || seen.has(question.id) || !question.question.trim()) throw new Error('question identity or text is invalid')
    seen.add(question.id)
    return {
      id: question.id, question: question.question,
      ...(question.header === undefined ? {} : { header: question.header }),
      ...(question.detail === undefined ? {} : { detail: question.detail }),
      options: question.options?.map(option => ({ ...option })) ?? [],
      multi_select: question.multiSelect === true,
    }
  })
  const result: PendingInteraction = {
    kind: 'question', call_id: callId,
    title: questions[0]?.header || '需要你的回答', questions,
  }
  if (Buffer.byteLength(JSON.stringify(result)) > 256 * 1024) throw new Error('human interaction exceeds 256 KiB')
  return result
}
