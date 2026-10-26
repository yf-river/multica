export type ServiceLookup = (name: string) => unknown

function hasMethod(value: unknown, method: string): boolean {
  return value !== null && (typeof value === 'object' || typeof value === 'function')
    && typeof (value as Record<string, unknown>)[method] === 'function'
}

export function detectServices(get: ServiceLookup) {
  const available = (name: string, method: string): boolean => {
    try { return hasMethod(get(name), method) } catch { return false }
  }
  return {
    commands: available('commands', 'execute'),
    user_questions: available('userQuestions', 'ask'),
    session_projections: available('sessionProjections', 'stateOf'),
    approval: available('approval', 'setPolicy'),
    sandbox_policy: available('sandboxPolicy', 'resolve'),
    compaction: available('compaction', 'compactIfNeeded'),
  }
}

// 只读是执行前置条件，不能用拒绝审批替代缺失的沙箱。
export function enforceReadOnly(get: ServiceLookup, agent: { session: unknown }): void {
  const services = detectServices(get)
  if (!services.approval || !services.sandbox_policy) throw new Error('DSH cannot enforce the required read-only policy')
  const approval = get('approval') as { setPolicy(agent: unknown, policy: string): void }
  const sandbox = get('sandboxPolicy') as { resolve(request: { session: unknown }): { mode: string } }
  const session = agent.session as { append(type: string, value: unknown): void }
  if (typeof session.append !== 'function') throw new Error('DSH session does not support sandbox policy events')
  approval.setPolicy(agent, 'never')
  session.append('sandbox/mode', { mode: 'read-only' })
  if (sandbox.resolve({ session }).mode !== 'read-only') throw new Error('DSH did not apply the required read-only policy')
}
