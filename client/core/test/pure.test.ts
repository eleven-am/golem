import { describe, expect, it } from 'vitest'
import {
  AckTracker,
  BACKOFF_BASE_MS,
  BACKOFF_CAP_MS,
  GolemCommitUnknownError,
  GolemContractMismatchError,
  GolemDeadlineRequiredError,
  GolemPartialCommitError,
  GolemProtocolError,
  GolemProtocolMismatchError,
  GolemError,
  PROTOCOL_VERSION,
  backoffCeiling,
  backoffDelay,
  classifyClose,
  failureOf,
  golemBlock,
  toGolemError,
} from '../src/index.js'

describe('negotiation block', () => {
  it('carries the protocol version and the manifest contract', () => {
    expect(golemBlock({ contract: 'abc' })).toEqual({ protocol: PROTOCOL_VERSION, contract: 'abc' })
    expect(PROTOCOL_VERSION).toBe(1)
  })
})

describe('backoff', () => {
  it('starts at 250 ms, doubles and caps at 30 s', () => {
    const ceilings = [0, 1, 2, 3, 6, 7, 8, 20, 1000].map(backoffCeiling)
    expect(ceilings).toEqual([250, 500, 1000, 2000, 16000, 30000, 30000, 30000, 30000])
    expect(BACKOFF_BASE_MS).toBe(250)
    expect(BACKOFF_CAP_MS).toBe(30000)
  })

  it('draws the full range from zero to just under the ceiling', () => {
    expect(backoffDelay(3, () => 0)).toBe(0)
    expect(backoffDelay(3, () => 0.999999)).toBe(1999)
    expect(backoffDelay(50, () => 0.999999)).toBe(29999)
  })
})

describe('close classification', () => {
  const fatal = (code: number, reason = '', acknowledged = false) => classifyClose(code, reason, acknowledged)

  it('treats 4426 reasons as typed fatal refusals', () => {
    const protocol = fatal(4426, 'golem:protocol')
    expect(protocol).toMatchObject({ action: 'fatal', status: 'protocol-mismatch' })
    expect(protocol.action === 'fatal' && protocol.error).toBeInstanceOf(GolemProtocolMismatchError)
    const contract = fatal(4426, 'golem:contract')
    expect(contract).toMatchObject({ action: 'fatal', status: 'contract-mismatch' })
    expect(contract.action === 'fatal' && contract.error).toBeInstanceOf(GolemContractMismatchError)
    const deadline = fatal(4426, 'golem:GOLEM_PRINCIPAL_DEADLINE_REQUIRED')
    expect(deadline).toMatchObject({ action: 'fatal', status: 'deadline-required' })
    expect(deadline.action === 'fatal' && deadline.error).toBeInstanceOf(GolemDeadlineRequiredError)
    const unknown = fatal(4426, 'golem:something-new')
    expect(unknown.action === 'fatal' && unknown.error).toBeInstanceOf(GolemProtocolError)
  })

  it('reauthenticates on 4401 after the acknowledgement and is unauthenticated before it', () => {
    expect(fatal(4401, 'credential expired', true)).toEqual({ action: 'reauthenticate' })
    expect(fatal(4401, 'authentication is required', false)).toMatchObject({ action: 'fatal', status: 'unauthenticated' })
  })

  it('treats 4400, 4403, 4409 and 4429 as fatal and everything else as retryable', () => {
    expect(fatal(4403)).toMatchObject({ action: 'fatal', status: 'forbidden' })
    for (const code of [4400, 4409, 4429]) {
      expect(fatal(code)).toMatchObject({ action: 'fatal', status: 'invalid' })
    }
    for (const code of [1000, 1001, 1006, 1012, 4408]) {
      expect(fatal(code)).toEqual({ action: 'retry' })
    }
  })
})

describe('per-operation acknowledgement bookkeeping', () => {
  const ack = { data: null, extensions: { golem: { live: true } } }

  it('accepts one acknowledgement and then events', () => {
    const tracker = new AckTracker()
    expect(tracker.accept(ack)).toEqual({ kind: 'ack' })
    expect(tracker.live).toBe(true)
    expect(tracker.accept({ data: { postEvents: {} } })).toMatchObject({ kind: 'event' })
  })

  it('rejects an event before the acknowledgement', () => {
    const outcome = new AckTracker().accept({ data: { postEvents: {} } })
    expect(outcome.kind === 'violation' && outcome.error).toBeInstanceOf(GolemProtocolError)
  })

  it('rejects a second acknowledgement', () => {
    const tracker = new AckTracker()
    tracker.accept(ack)
    expect(tracker.accept(ack).kind).toBe('violation')
  })

  it('accepts a second acknowledgement after a reset', () => {
    const tracker = new AckTracker()
    tracker.accept(ack)
    tracker.reset()
    expect(tracker.accept(ack)).toEqual({ kind: 'ack' })
  })

  it('rejects an acknowledgement that carries data', () => {
    expect(new AckTracker().accept({ data: {}, extensions: { golem: { live: true } } }).kind).toBe('violation')
  })

  it('reports a terminal error before or after the acknowledgement', () => {
    const errors = [{ message: 'gone', extensions: { code: 'RESYNC_REQUIRED' } }]
    const before = new AckTracker().accept({ errors })
    expect(before.kind === 'error' && before.errors[0]?.code).toBe('RESYNC_REQUIRED')
    const tracker = new AckTracker()
    tracker.accept(ack)
    expect(tracker.accept({ errors }).kind).toBe('error')
  })
})

describe('error decoding', () => {
  const failure = { message: 'boom', path: ['second'], extensions: { code: 'BAD_USER_INPUT' } }

  it('keeps code and path on a plain error', () => {
    const error = failureOf({ errors: [failure] })
    expect(error).toBeInstanceOf(GolemError)
    expect(error).toMatchObject({ code: 'BAD_USER_INPUT', path: ['second'], retryable: true })
  })

  it('gives an error without a code an explicit placeholder code', () => {
    expect(toGolemError({ message: 'x' }).code).toBe('GOLEM_UNCODED')
  })

  it('maps the commit report to typed errors that are never retryable', () => {
    const partial = failureOf({ errors: [failure], extensions: { golem: { commit: 'PARTIAL' } } })
    expect(partial).toBeInstanceOf(GolemPartialCommitError)
    expect(partial?.retryable).toBe(false)
    const unknown = failureOf({ errors: [failure], extensions: { golem: { commit: 'UNKNOWN' } } })
    expect(unknown).toBeInstanceOf(GolemCommitUnknownError)
    expect(unknown?.retryable).toBe(false)
  })

  it('refuses an unrecognised commit value', () => {
    expect(() => failureOf({ errors: [failure], extensions: { golem: { commit: 'MAYBE' } } })).toThrow(GolemProtocolError)
  })

  it('returns nothing for a clean response', () => {
    expect(failureOf({ data: {} })).toBeUndefined()
  })
})
