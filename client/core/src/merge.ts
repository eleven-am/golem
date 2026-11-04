import { GolemProtocolError } from './errors.js'

export type Version = bigint

export interface Marker {
  readonly key: string
  readonly version: Version
  readonly receivedAt: number
}

export interface Change {
  readonly key: string
  readonly receivedAt: number
}

export interface MergeState {
  readonly markers: readonly Marker[]
  readonly changes: readonly Change[]
}

export const EMPTY_MERGE_STATE: MergeState = { markers: [], changes: [] }

export type UpdateDecision = 'patch' | 'ignore' | 'invalidate'

export type RowDecision = 'apply' | 'keep-cached' | 'drop'

export interface Removal {
  readonly key: string
  readonly version: Version
  readonly receivedAt: number
}

export interface RemovalOutcome {
  readonly remove: boolean
  readonly state: MergeState
}

export interface Response {
  readonly issuedAt: number
  readonly rows: ReadonlyMap<string, Version>
  readonly cached: ReadonlyMap<string, Version>
}

export interface Resolution {
  readonly rows: ReadonlyMap<string, RowDecision>
  readonly refetch: boolean
  readonly state: MergeState
}

const DIGITS = /^[0-9]+$/

export function parseVersion(raw: string): Version {
  if (!DIGITS.test(raw)) {
    throw new GolemProtocolError(`row version is not an unsigned integer: ${JSON.stringify(raw)}`)
  }
  return BigInt(raw)
}

export function decideUpdate(cached: Version | undefined, event: Version): UpdateDecision {
  if (cached === undefined) {
    return 'invalidate'
  }
  return event > cached ? 'patch' : 'ignore'
}

export function noteChange(state: MergeState, key: string, receivedAt: number): MergeState {
  return { markers: state.markers, changes: [...state.changes, { key, receivedAt }] }
}

export function applyRemoval(state: MergeState, removal: Removal, cached: Version | undefined): RemovalOutcome {
  const marker: Marker = { key: removal.key, version: removal.version, receivedAt: removal.receivedAt }
  return {
    remove: cached !== undefined && cached <= removal.version,
    state: { markers: [...state.markers, marker], changes: state.changes },
  }
}

export function resolveResponse(state: MergeState, response: Response): Resolution {
  const rows = new Map<string, RowDecision>()
  for (const [key, version] of response.rows) {
    rows.set(key, decideRow(state, response, key, version))
  }
  return {
    rows,
    refetch: droppedChangedKey(state, response),
    state: {
      markers: state.markers.filter((marker) => marker.receivedAt >= response.issuedAt),
      changes: state.changes.filter((change) => change.receivedAt >= response.issuedAt),
    },
  }
}

function decideRow(state: MergeState, response: Response, key: string, version: Version): RowDecision {
  for (const marker of state.markers) {
    if (marker.key === key && marker.receivedAt >= response.issuedAt && version <= marker.version) {
      return 'drop'
    }
  }
  const cached = response.cached.get(key)
  return cached !== undefined && cached > version ? 'keep-cached' : 'apply'
}

function droppedChangedKey(state: MergeState, response: Response): boolean {
  for (const change of state.changes) {
    if (change.receivedAt < response.issuedAt || response.rows.has(change.key)) {
      continue
    }
    if (!removalExplains(state, change)) {
      return true
    }
  }
  return false
}

function removalExplains(state: MergeState, change: Change): boolean {
  return state.markers.some((marker) => marker.key === change.key && marker.receivedAt >= change.receivedAt)
}
