import { GolemProtocolError } from './errors.js'
import type { ClientManifest } from './manifest.js'
import { parseVersion, type Version } from './merge.js'

export type RowKey = string

type Row = Record<string, unknown>

const REMOVED: unique symbol = Symbol('removed')

type Rewritten = unknown | typeof REMOVED

interface Slots {
  readonly nullableRoots: ReadonlySet<string>
  readonly held: Set<RowKey>
}

function isPlainObject(value: unknown): value is Row {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export type EventIdentity = string | Readonly<Record<string, unknown>>

export interface Eviction {
  readonly data: unknown
  readonly held: ReadonlySet<RowKey>
}

export class Patcher {
  private readonly manifest: ClientManifest

  constructor(manifest: ClientManifest) {
    this.manifest = manifest
  }

  keyOf(typename: string, source: Readonly<Record<string, unknown>>): RowKey | undefined {
    const model = this.manifest.models[typename]
    if (model === undefined) {
      return undefined
    }
    const parts: string[] = []
    for (const field of model.primaryKey) {
      const value = source[field]
      if (typeof value !== 'string' && typeof value !== 'number') {
        return undefined
      }
      parts.push(String(value))
    }
    return `${typename}:${parts.join('\u0001')}`
  }

  eventKey(model: string, id: EventIdentity, entity: Readonly<Record<string, unknown>> | null): RowKey {
    const described = this.manifest.models[model]
    if (described === undefined) {
      throw new GolemProtocolError(`an event named the unknown model ${model}`)
    }
    if (entity !== null) {
      const key = this.keyOf(model, entity)
      if (key !== undefined) {
        return key
      }
    }
    const [field] = described.primaryKey
    const source = typeof id === 'string' ? (described.primaryKey.length === 1 && field !== undefined ? { [field]: id } : undefined) : id
    const key = source === undefined ? undefined : this.keyOf(model, source)
    if (key === undefined) {
      throw new GolemProtocolError(`an event for ${model} carries an identity that does not hold ${described.primaryKey.join(', ')}`)
    }
    return key
  }

  versions(data: unknown): Map<RowKey, Version> {
    const found = new Map<RowKey, Version>()
    this.each(data, (key, row) => {
      const raw = row._version
      if (typeof raw !== 'string') {
        return
      }
      const version = parseVersion(raw)
      const known = found.get(key)
      if (known === undefined || version > known) {
        found.set(key, version)
      }
    })
    return found
  }

  rows(data: unknown): Map<RowKey, Row> {
    const found = new Map<RowKey, Row>()
    this.each(data, (key, row) => {
      found.set(key, row)
    })
    return found
  }

  patch(data: unknown, key: RowKey, entity: Readonly<Record<string, unknown>>): unknown {
    return this.rewrite(data, (candidate, candidateKey) => {
      if (candidateKey !== key) {
        return candidate
      }
      return this.overlay(candidate, entity)
    })
  }

  differs(data: unknown, key: RowKey, entity: Readonly<Record<string, unknown>>, names: ReadonlyArray<string>): boolean {
    let found = false
    this.each(data, (candidateKey, row) => {
      if (candidateKey === key && names.some((name) => name in entity && entity[name] !== row[name])) {
        found = true
      }
    })
    return found
  }

  remove(data: unknown, keys: ReadonlySet<RowKey>, nullableRoots: ReadonlySet<string>): Eviction {
    const held = new Set<RowKey>()
    const next = this.rewrite(data, (candidate, candidateKey) => (keys.has(candidateKey) ? REMOVED : candidate), { nullableRoots, held })
    return { data: next, held }
  }

  adopt(fresh: unknown, cached: ReadonlyMap<RowKey, Row>, keys: ReadonlySet<RowKey>): unknown {
    return this.rewrite(fresh, (candidate, candidateKey) => {
      const source = cached.get(candidateKey)
      return keys.has(candidateKey) && source !== undefined ? this.overlay(candidate, source) : candidate
    })
  }

  private overlay(row: Row, source: Readonly<Record<string, unknown>>): Row {
    const typename = row.__typename
    const relations = typeof typename === 'string' ? this.manifest.models[typename]?.relations ?? {} : {}
    let next: Row | undefined
    for (const [name, value] of Object.entries(source)) {
      if (name === '__typename' || !(name in row) || name in relations || row[name] === value) {
        continue
      }
      if (this.holdsRows(value) || this.holdsRows(row[name])) {
        continue
      }
      next ??= { ...row }
      next[name] = value
    }
    return next ?? row
  }

  private holdsRows(value: unknown): boolean {
    if (Array.isArray(value)) {
      return value.some((item) => this.identify(item) !== undefined)
    }
    return this.identify(value) !== undefined
  }

  private identify(value: unknown): RowKey | undefined {
    if (!isPlainObject(value) || typeof value.__typename !== 'string') {
      return undefined
    }
    return this.keyOf(value.__typename, value)
  }

  private each(value: unknown, visit: (key: RowKey, row: Row) => void): void {
    if (Array.isArray(value)) {
      for (const item of value) {
        this.each(item, visit)
      }
      return
    }
    if (!isPlainObject(value)) {
      return
    }
    const key = this.identify(value)
    if (key !== undefined) {
      visit(key, value)
    }
    for (const child of Object.values(value)) {
      this.each(child, visit)
    }
  }

  private vacate(child: unknown, name: string, depth: number, slots: Slots | undefined): unknown {
    if (slots !== undefined && depth === 0 && slots.nullableRoots.has(name)) {
      return null
    }
    const key = this.identify(child)
    if (slots !== undefined && key !== undefined) {
      slots.held.add(key)
    }
    return child
  }

  private rewrite(value: unknown, transform: (row: Row, key: RowKey) => Rewritten, slots?: Slots, depth = 0): unknown {
    if (Array.isArray(value)) {
      let changed = false
      const next: unknown[] = []
      for (const item of value) {
        const rewritten = this.rewrite(item, transform, slots, depth + 1)
        if (rewritten === REMOVED) {
          changed = true
          continue
        }
        if (rewritten !== item) {
          changed = true
        }
        next.push(rewritten)
      }
      return changed ? next : value
    }
    if (!isPlainObject(value)) {
      return value
    }
    let copy: Row | undefined
    for (const [name, child] of Object.entries(value)) {
      const rewritten = this.rewrite(child, transform, slots, depth + 1)
      const replacement = rewritten === REMOVED ? this.vacate(child, name, depth, slots) : rewritten
      if (replacement !== child) {
        copy ??= { ...value }
        copy[name] = replacement
      }
    }
    const current = copy ?? value
    const key = this.identify(current)
    if (key === undefined) {
      return current
    }
    return transform(current, key)
  }
}
