import { describe, expect, it } from 'vitest'
import {
  EMPTY_MERGE_STATE,
  GolemProtocolError,
  applyRemoval,
  decideUpdate,
  noteChange,
  parseVersion,
  resolveResponse,
} from '../src/index.js'

const rows = (entries: Array<[string, bigint]>) => new Map(entries)

describe('version parsing', () => {
  it('keeps precision beyond 2^53', () => {
    expect(parseVersion('9007199254740993')).toBe(9007199254740993n)
    expect(parseVersion('9007199254740993') > parseVersion('9007199254740992')).toBe(true)
  })

  it('compares numerically, not lexically', () => {
    expect(parseVersion('10') > parseVersion('9')).toBe(true)
  })

  it.each(['', '-1', '1.5', '0x10', ' 1', 'abc'])('rejects %j', (raw) => {
    expect(() => parseVersion(raw)).toThrow(GolemProtocolError)
  })
})

describe('rule 2: UPDATED', () => {
  it('patches a higher version', () => {
    expect(decideUpdate(5n, 6n)).toBe('patch')
  })

  it('ignores an equal version', () => {
    expect(decideUpdate(5n, 5n)).toBe('ignore')
  })

  it('ignores an older version', () => {
    expect(decideUpdate(5n, 4n)).toBe('ignore')
  })

  it('invalidates a key the query does not hold', () => {
    expect(decideUpdate(undefined, 6n)).toBe('invalidate')
  })
})

describe('rule 3: DELETED and EXITED', () => {
  const removal = { key: 'k', version: 10n, receivedAt: 100 }

  it('removes a copy at or below the delete version', () => {
    expect(applyRemoval(EMPTY_MERGE_STATE, removal, 9n).remove).toBe(true)
    expect(applyRemoval(EMPTY_MERGE_STATE, removal, 10n).remove).toBe(true)
  })

  it('keeps a recreated copy above the delete version', () => {
    expect(applyRemoval(EMPTY_MERGE_STATE, removal, 11n).remove).toBe(false)
  })

  it('removes nothing from a query that does not hold the key', () => {
    expect(applyRemoval(EMPTY_MERGE_STATE, removal, undefined).remove).toBe(false)
  })

  it('records a marker even when nothing was held', () => {
    expect(applyRemoval(EMPTY_MERGE_STATE, removal, undefined).state.markers).toEqual([
      { key: 'k', version: 10n, receivedAt: 100 },
    ])
  })
})

describe('rule 4: a response lands', () => {
  it('keeps a cached copy with a higher version', () => {
    const state = noteChange(EMPTY_MERGE_STATE, 'k', 100)
    const result = resolveResponse(state, {
      issuedAt: 50,
      rows: rows([['k', 5n]]),
      cached: rows([['k', 6n]]),
    })
    expect(result.rows.get('k')).toBe('keep-cached')
  })

  it('applies a response row that is not older than the cache', () => {
    const result = resolveResponse(EMPTY_MERGE_STATE, {
      issuedAt: 50,
      rows: rows([['a', 6n], ['b', 5n]]),
      cached: rows([['a', 5n], ['b', 5n]]),
    })
    expect(result.rows.get('a')).toBe('apply')
    expect(result.rows.get('b')).toBe('apply')
  })

  it('drops a row deleted after the request was issued', () => {
    const { state } = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 100 }, 9n)
    const result = resolveResponse(state, {
      issuedAt: 50,
      rows: rows([['k', 9n]]),
      cached: rows([]),
    })
    expect(result.rows.get('k')).toBe('drop')
    expect(result.refetch).toBe(false)
  })

  it('drops a row exactly at the marker version', () => {
    const { state } = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 100 }, 10n)
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([['k', 10n]]), cached: rows([]) })
    expect(result.rows.get('k')).toBe('drop')
  })

  it('does not apply a marker received before the request was issued', () => {
    const { state } = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 40 }, 9n)
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([['k', 10n]]), cached: rows([]) })
    expect(result.rows.get('k')).toBe('apply')
  })

  it('applies a marker received in the same tick as the issue', () => {
    const { state } = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 50 }, 9n)
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([['k', 10n]]), cached: rows([]) })
    expect(result.rows.get('k')).toBe('drop')
  })

  it('refetches once when the response lacks a key an event changed after issue', () => {
    const state = noteChange(EMPTY_MERGE_STATE, 'k', 100)
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([['other', 1n]]), cached: rows([]) })
    expect(result.refetch).toBe(true)
  })

  it('does not refetch when the changed key is present', () => {
    const state = noteChange(EMPTY_MERGE_STATE, 'k', 100)
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([['k', 7n]]), cached: rows([]) })
    expect(result.refetch).toBe(false)
  })

  it('does not refetch for a change received before the request was issued', () => {
    const state = noteChange(EMPTY_MERGE_STATE, 'k', 40)
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([]), cached: rows([]) })
    expect(result.refetch).toBe(false)
  })

  it('does not refetch for an absent key whose last event was its removal', () => {
    const changed = noteChange(EMPTY_MERGE_STATE, 'k', 90)
    const state = applyRemoval(changed, { key: 'k', version: 10n, receivedAt: 100 }, 9n).state
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([]), cached: rows([]) })
    expect(result.refetch).toBe(false)
  })

  it('refetches when a key was removed and then changed again, and is absent', () => {
    const removed = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 100 }, 9n).state
    const state = noteChange(removed, 'k', 110)
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([]), cached: rows([]) })
    expect(result.refetch).toBe(true)
  })

  it('discards markers and changes received before the landed request was issued', () => {
    let state = applyRemoval(EMPTY_MERGE_STATE, { key: 'old', version: 1n, receivedAt: 10 }, 1n).state
    state = noteChange(state, 'oldchange', 20)
    state = applyRemoval(state, { key: 'new', version: 2n, receivedAt: 100 }, 2n).state
    state = noteChange(state, 'newchange', 110)
    const result = resolveResponse(state, {
      issuedAt: 50,
      rows: rows([['newchange', 1n]]),
      cached: rows([]),
    })
    expect(result.state.markers.map((m) => m.key)).toEqual(['new'])
    expect(result.state.changes.map((c) => c.key)).toEqual(['newchange'])
  })
})

describe('recreated key (plan 3.4)', () => {
  it('wins with its higher version against the old row removal', () => {
    const removal = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 100 }, 12n)
    expect(removal.remove).toBe(false)
    const result = resolveResponse(removal.state, {
      issuedAt: 50,
      rows: rows([['k', 12n]]),
      cached: rows([['k', 12n]]),
    })
    expect(result.rows.get('k')).toBe('apply')
  })

  it('never lets old copies win over the recreated row', () => {
    const { state } = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 100 }, 12n)
    const result = resolveResponse(state, {
      issuedAt: 50,
      rows: rows([['k', 8n]]),
      cached: rows([['k', 12n]]),
    })
    expect(result.rows.get('k')).toBe('drop')
  })

  it('applies a recreated row in a response issued before the delete marker', () => {
    const { state } = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 100 }, undefined)
    const result = resolveResponse(state, { issuedAt: 50, rows: rows([['k', 11n]]), cached: rows([]) })
    expect(result.rows.get('k')).toBe('apply')
  })

  it('exit then dependency re-entry: a response issued after the marker is not dropped', () => {
    const { state } = applyRemoval(EMPTY_MERGE_STATE, { key: 'k', version: 10n, receivedAt: 100 }, 10n)
    const result = resolveResponse(state, { issuedAt: 150, rows: rows([['k', 10n]]), cached: rows([]) })
    expect(result.rows.get('k')).toBe('apply')
    expect(result.state.markers).toEqual([])
  })
})

describe('gap (plan 3.4)', () => {
  it('deduplicates a commit present in both the fetch and an event by version', () => {
    expect(decideUpdate(7n, 7n)).toBe('ignore')
    const result = resolveResponse(EMPTY_MERGE_STATE, {
      issuedAt: 50,
      rows: rows([['k', 6n]]),
      cached: rows([['k', 7n]]),
    })
    expect(result.rows.get('k')).toBe('keep-cached')
  })
})
