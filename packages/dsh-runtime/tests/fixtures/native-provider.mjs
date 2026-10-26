import { appendFileSync } from 'node:fs'

if (process.env.MULTICA_RUN_REAL_AGENT_SMOKE !== '1') throw new Error('Native DSH fixture requires the explicit smoke gate')
const { LlmAdapter } = await import(process.env.MULTICA_DSH_HOST_LLM_MODULE)
export const name = 'multica-native-interaction-fixture'
export const inject = ['llm']
let requests = 0

class FixtureAdapter extends LlmAdapter {
  async listModels() { return [{provider: 'multica-fixture', id: 'native-interaction', name: 'Native interaction fixture'}] }
  async resolveModel(provider, model) { return {provider, id: model, name: 'Native interaction fixture', context: {contextWindow: 1048576}, inputModalities: ['text']} }
  async *stream(options) {
    const phase = process.env.MULTICA_DSH_FIXTURE_PHASE
    appendFileSync(process.env.MULTICA_DSH_FIXTURE_REQUESTS, JSON.stringify({phase, messages: options.messages, tools: options.tools}) + '\n', {mode: 0o600})
    requests += 1
    if (phase === 'provider-error') throw new Error('Deterministic fixture provider failure')
    if (options.provider !== 'multica-fixture' || options.model !== 'native-interaction') throw new Error('Fixture model identity changed')
    let name, args
    if (requests === 1 && (phase === 'plan' || phase === 'keep-planning')) {
      name = 'exit_plan_mode'
      args = {plan: '# Native fixture plan\n\n1. Read the task.\n2. Write approved.txt only after approval.\n'}
    } else if (requests === 1 && phase === 'question') {
      name = 'ask_user_question'
      args = {questions: [{id: 'destination', header: 'Destination', question: 'Which folder should contain the report?', options: [{label: 'docs', description: 'Project documents'}], multi_select: false}]}
    } else if (requests === 1 && phase === 'approved') {
      name = 'bash'
      args = {command: "printf 'DSH_APPROVED_WRITE' > approved.txt", description: 'Write the approved fixture result'}
    }
    if (name) {
      if (!options.tools?.some(tool => tool.name === name)) throw new Error(`Native tool ${name} is unavailable`)
      const id = `fixture-${phase}-call`
      const argumentsText = JSON.stringify(args)
      yield {type: 'block-start', index: 0, blockType: 'tool-call'}
      yield {type: 'tool-call-delta', index: 0, id, name, argumentsDelta: argumentsText}
      yield {type: 'block-end', index: 0, block: {type: 'tool-call', id, name, arguments: argumentsText}}
      yield {type: 'usage', usage: {inputTokens: 11, cacheReadTokens: 7, outputTokens: 5}}
      yield {type: 'finish', reason: {kind: 'tool-calls'}}
      return
    }
    if (phase === 'plan' || phase === 'keep-planning' || phase === 'question') throw new Error('Native human interaction unexpectedly continued calling the model')
    const text = phase === 'canary' ? 'MULTICA_DSH_CANARY_OK' : phase === 'answer' ? 'DSH_ANSWER_RESUMED' : 'DSH_APPROVED_DONE'
    yield {type: 'block-start', index: 0, blockType: 'text'}
    yield {type: 'text-delta', index: 0, text}
    yield {type: 'block-end', index: 0, block: {type: 'text', text}}
    yield {type: 'usage', usage: {inputTokens: 13, cacheReadTokens: 9, outputTokens: 3}}
    yield {type: 'finish', reason: {kind: 'stop'}}
  }
}

export function apply(ctx) { ctx.llm.registerAdapter(['multica-fixture'], new FixtureAdapter()) }
