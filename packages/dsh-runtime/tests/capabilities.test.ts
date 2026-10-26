import { describe, expect, it } from 'vitest'
import { detectServices, enforceReadOnly } from '../src/capabilities.js'

describe('DSH service capabilities', () => {
  it('reports missing or broken services without crashing optional features', () => {
    const caps = detectServices(name => { if (name === 'approval') throw new Error('missing'); return undefined })
    expect(Object.values(caps).every(value => value === false)).toBe(true)
    expect(detectServices(name => name === 'commands' ? { execute() {} } : {}).commands).toBe(true)
  })

  it('rejects required read-only policy when the sandbox is unavailable', () => {
    expect(() => enforceReadOnly(() => ({ setPolicy() {} }), { session: {} })).toThrow('cannot enforce')
  })

  it('checks the effective sandbox policy before allowing execution', () => {
    let mode = 'workspace-write'
    let policy = ''
    const agent = { session: { append(_event: string, value: { mode: string }) { mode = value.mode } } }
    const get = (name: string) => name === 'approval'
      ? { setPolicy(_agent: unknown, value: string) { policy = value } }
      : name === 'sandboxPolicy' ? { resolve: () => ({ mode }) } : undefined
    enforceReadOnly(get, agent)
    expect(mode).toBe('read-only')
    expect(policy).toBe('never')
    expect(() => enforceReadOnly(get, { session: { append() { mode = 'workspace-write' } } })).toThrow('did not apply')
  })
})
