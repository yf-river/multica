import { readFile } from 'node:fs/promises'
import { describe, expect, it } from 'vitest'
import { encodeFrame, parseInboundCommand, type OutboundFrame, type ExecuteCommand } from '../src/protocol.js'
import { BRIDGE_VERSION } from '../src/version.js'

describe('Go adapter and bridge wire contract', () => {
  it('accepts the complete command emitted by the Go adapter without dropping fields', async () => {
    const raw = await readFile(new URL('../fixtures/execute.json', import.meta.url), 'utf8')
    expect(parseInboundCommand(raw)).toEqual(JSON.parse(raw))
    const command: ExecuteCommand = parseInboundCommand(raw) as ExecuteCommand
    expect(command.approval_policy).toBe('never')
  })

  it('writes the golden frames consumed by Go without changing wire fields', async () => {
    const raw = await readFile(new URL('../fixtures/frames.jsonl', import.meta.url), 'utf8')
    for (const line of raw.trim().split('\n')) {
      expect(encodeFrame(JSON.parse(line) as OutboundFrame)).toBe(line + '\n')
    }
    const sample: OutboundFrame = {
      v: 1, type: 'context', request_id: 'contract-task', session_id: 'contract-session', seq: 5,
      at: '2026-10-26T00:00:00Z', provider: 'deepseek-official', model: 'deepseek-flash',
      duration_ms: 109, input_tokens: 101, output_tokens: 17, cache_read_tokens: 63,
      context_window: 1048576, pressure_tokens: 164, projected_tokens: 200,
      breakdown: { system_tokens: 20, tools_tokens: 30, message_tokens: 114 },
    }
    expect(raw.split('\n')).toContain(encodeFrame(sample).trimEnd())
  })

  it('packs one identity in package metadata, compatibility and ready frames', async () => {
    const pkg = JSON.parse(await readFile(new URL('../package.json', import.meta.url), 'utf8'))
    const compat = JSON.parse(await readFile(new URL('../compat.json', import.meta.url), 'utf8'))
    const lines = await readFile(new URL('../fixtures/frames.jsonl', import.meta.url), 'utf8')
    expect(pkg.version).toBe(BRIDGE_VERSION)
    expect(compat.bridgeVersion).toBe(BRIDGE_VERSION)
    expect(JSON.parse(lines.split('\n')[0]).plugin_version).toBe(BRIDGE_VERSION)
    expect(compat.protocolVersion).toBe(1)
  })
})
