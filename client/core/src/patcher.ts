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
  listed: boolean
}

function isPlainObject(value: unknown): value is Row {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export type Occurrences = ReadonlyArray<string>

const childPath = (path: string, name: string): string => (path === '' ? name : `${path}.${name}`)

export type EventIdentity = string | Readonly<Record<string, unknown>>

export interface Eviction {
  readonly data: unknown
  readonly held: ReadonlySet<RowKey>
  readonly listed: boolean
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

  versions(data: unknown, within?: Occurrences): Map<RowKey, Version> {
    const found = new Map<RowKey, Version>()
    this.each(data, within, (key, row) => {
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

  patch(data: unknown, key: RowKey, entity: Readonly<Record<string, unknown>>, within?: Occurrences): unknown {
    return this.rewrite(data, within, (candidate, candidateKey) => {
      if (candidateKey !== key) {
        return candidate
      }
      return this.overlay(candidate, entity)
    })
  }

  differs(data: unknown, key: RowKey, entity: Readonly<Record<string, unknown>>, names: ReadonlyArray<string>, within?: Occurrences): boolean {
    let found = false
    this.each(data, within, (candidateKey, row) => {
      if (candidateKey === key && names.some((name) => name in entity && entity[name] !== row[name])) {
        found = true
      }
    })
    return found
  }

  remove(data: unknown, keys: ReadonlySet<RowKey>, nullableRoots: ReadonlySet<string>, within?: Occurrences): Eviction {
    const slots: Slots = { nullableRoots, held: new Set<RowKey>(), listed: false }
    const next = this.rewrite(data, within, (candidate, candidateKey) => (keys.has(candidateKey) ? REMOVED : candidate), slots)
    return { data: next, held: slots.held, listed: slots.listed }
  }

  private overlay(row: Row, source: Readonly<Record<string, unknown>>): Row {
    const typename = row.__typename
    const relations = typeof typename === 'string' ? this.manifest.models[typename]?.relations ?? {} : {}
    const computed = typeof typename === 'string' ? this.manifest.models[typename]?.computed ?? {} : {}
    const complete = Object.keys(row).every(
      (name) => name in source || name === '__typename' || name in relations || (computed[name]?.length ?? 0) > 0 || this.holdsRows(row[name]),
    )
    let next: Row | undefined
    for (const [name, value] of Object.entries(source)) {
      if (name === '_version' && !complete) {
        continue
      }
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

  private each(value: unknown, within: Occurrences | undefined, visit: (key: RowKey, row: Row) => void, path = ''): void {
    if (Array.isArray(value)) {
      for (const item of value) {
        this.each(item, within, visit, path)
      }
      return
    }
    if (!isPlainObject(value)) {
      return
    }
    const key = this.identify(value)
    if (key !== undefined && (within === undefined || within.includes(path))) {
      visit(key, value)
    }
    for (const [name, child] of Object.entries(value)) {
      this.each(child, within, visit, childPath(path, name))
    }
  }

  private nullableSlot(parent: Row, name: string, depth: number, slots: Slots): boolean {
    if (depth === 0) {
      return slots.nullableRoots.has(name)
    }
    const typename = parent.__typename
    const relation = typeof typename === 'string' ? this.manifest.models[typename]?.relations[name] : undefined
    return relation !== undefined && !relation.list && relation.nullable
  }

  private vacate(child: unknown, name: string, depth: number, slots: Slots | undefined, parent: Row): unknown {
    if (slots !== undefined && this.nullableSlot(parent, name, depth, slots)) {
      return null
    }
    const key = this.identify(child)
    if (slots !== undefined && key !== undefined) {
      slots.held.add(key)
    }
    return child
  }

  private rewrite(value: unknown, within: Occurrences | undefined, transform: (row: Row, key: RowKey) => Rewritten, slots?: Slots, depth = 0, path = ''): unknown {
    if (Array.isArray(value)) {
      let changed = false
      const next: unknown[] = []
      for (const item of value) {
        const rewritten = this.rewrite(item, within, transform, slots, depth + 1, path)
        if (rewritten === REMOVED) {
          changed = true
          if (slots !== undefined) {
            slots.listed = true
          }
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
      const rewritten = this.rewrite(child, within, transform, slots, depth + 1, childPath(path, name))
      const replacement = rewritten === REMOVED ? this.vacate(child, name, depth, slots, value) : rewritten
      if (replacement !== child) {
        copy ??= { ...value }
        copy[name] = replacement
      }
    }
    const current = copy ?? value
    const key = this.identify(current)
    if (key === undefined || (within !== undefined && !within.includes(path))) {
      return current
    }
    return transform(current, key)
  }
}
