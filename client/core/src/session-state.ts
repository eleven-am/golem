// One private lifetime owner per transport. This module is deliberately not
// re-exported: applications can prepare operations, never manufacture epochs.
import { GolemConfigurationError, GolemSessionError } from './errors.js'
import { DATA_INCARNATION_CAPABILITY, requiredCapabilities } from './manifest.js'

export interface SessionStamp { readonly epoch: object; readonly incarnation: string | undefined }
type Discovery = (nonce: string, entry: SessionStamp) => Promise<string>
const owners = new WeakMap<object, SessionState>()
export function sessionState(owner: object, manifest?: { readonly formatVersion?: number; readonly requiredCapabilities?: ReadonlyArray<string> }): SessionState {
  let state = owners.get(owner)
  if (state === undefined) {
    state = new SessionState(manifest !== undefined && requiredCapabilities(manifest).includes(DATA_INCARNATION_CAPABILITY))
    owners.set(owner, state)
  }
  return state
}
export function shareSession(owner: object, state: SessionState): void { owners.set(owner, state) }
export function canonicalIncarnation(value: unknown): value is string {
  return typeof value === 'string' && /^[0-9a-f]{32}$/.test(value) && value !== '0'.repeat(32)
}
function nonce(): string {
  for (let attempt = 0; attempt < 4; attempt++) {
    const bytes = new Uint8Array(16)
    globalThis.crypto.getRandomValues(bytes)
    const value = Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
    if (canonicalIncarnation(value)) return value
  }
  throw new GolemConfigurationError('could not create an incarnation discovery nonce')
}
export class SessionState {
  private epoch: object = {}
  private identity: string | undefined
  private closed = false
  private available = false
  private discover: Discovery | undefined
  private pending: { readonly entry: SessionStamp; readonly work: Promise<SessionStamp> } | undefined
  private readonly resets = new Set<(adopting: boolean, refusal?: GolemSessionError) => void>()
  private readonly readyListeners = new Set<() => void>()
  private readonly failureListeners = new Set<(error: Error) => void>()
  constructor(readonly opted: boolean) { this.available = !opted }
  get generation(): object { return this.epoch }
  get disposed(): boolean { return this.closed }
  get initialized(): boolean { return !this.closed && this.available }
  configure(discover: Discovery): void { this.discover = discover }
  onReset(listener: (adopting: boolean, refusal?: GolemSessionError) => void): () => void { this.resets.add(listener); return () => this.resets.delete(listener) }
  onReady(listener: () => void): () => void { this.readyListeners.add(listener); return () => this.readyListeners.delete(listener) }
  onFailure(listener: (error: Error) => void): () => void { this.failureListeners.add(listener); return () => this.failureListeners.delete(listener) }
  capture(): SessionStamp {
    if (this.closed) throw new GolemSessionError('disposed', false)
    return { epoch: this.epoch, incarnation: this.identity }
  }
  current(stamp: SessionStamp): boolean { return !this.closed && stamp.epoch === this.epoch && stamp.incarnation === this.identity }
  assert(stamp: SessionStamp, dispatched = false): void {
    if (!this.current(stamp)) throw new GolemSessionError(this.closed ? 'disposed' : 'changed', dispatched)
  }
  mutation(): SessionStamp {
    const stamp = this.capture()
    if (!this.initialized) throw new GolemSessionError('not-ready', false)
    return stamp
  }
  // Refusal never adopts a packet's identity. Fence synchronously, then run
  // exactly the ordinary authenticated discovery shared by all pending reads.
  refuse(stamp: SessionStamp): void {
    if (!this.current(stamp)) return
    const fenced = this.invalidate(new GolemSessionError('changed', false))
    if (fenced === undefined) return
    // Reset listeners enqueue raw terminal notifications first. A later
    // explicit reset/disposal supersedes both notification and rediscovery.
    queueMicrotask(() => {
      if (this.current(fenced)) void this.ready().catch(() => undefined)
    })
  }
  invalidate(refusal?: GolemSessionError): SessionStamp | undefined {
    if (this.closed) return
    this.epoch = {}
    this.identity = undefined
    this.available = !this.opted
    this.pending = undefined
    const fenced = this.capture()
    for (const listener of [...this.resets]) listener(false, refusal)
    if (!this.opted && this.current(fenced)) this.notifyReady()
    return this.current(fenced) ? fenced : undefined
  }
  close(): void {
    if (this.closed) return
    this.closed = true
    this.epoch = {}
    this.identity = undefined
    this.available = !this.opted
    this.pending = undefined
    for (const listener of [...this.resets]) listener(false)
  }
  private notifyReady(): void { for (const listener of [...this.readyListeners]) listener() }
  ready(): Promise<void> { return this.read(this.capture()).then(() => undefined) }
  read(entry: SessionStamp): Promise<SessionStamp> {
    this.assert(entry)
    if (this.initialized) return Promise.resolve(entry)
    if (this.pending !== undefined) return this.pending.work
    const discover = this.discover
    if (discover === undefined) return Promise.reject(new GolemConfigurationError('incarnation discovery requires a Golem transport'))
    const work = (async (): Promise<SessionStamp> => {
      const identity = await discover(nonce(), entry)
      this.assert(entry)
      if (!canonicalIncarnation(identity)) throw new GolemConfigurationError('discovery returned an invalid data incarnation')
      // First adoption also clears persisted/manual Golem cache entries. No
      // response or acknowledgement can enter this adoption path.
      this.epoch = {}
      this.identity = identity
      const adoption = this.epoch
      this.available = false
      for (const listener of [...this.resets]) listener(true)
      if (this.closed || this.epoch !== adoption) throw new GolemSessionError(this.closed ? 'disposed' : 'changed', false)
      this.available = true
      const adopted = this.capture()
      this.notifyReady()
      return adopted
    })().catch((failure: unknown) => {
      if (this.current(entry)) {
        const error = failure instanceof Error ? failure : new GolemConfigurationError('authenticated incarnation discovery failed')
        for (const listener of [...this.failureListeners]) {
          if (!this.current(entry)) break
          listener(error)
        }
      }
      throw failure
    })
    const pending = { entry, work }
    this.pending = pending
    void work.finally(() => { if (this.pending === pending) this.pending = undefined }).catch(() => undefined)
    return work
  }
}
