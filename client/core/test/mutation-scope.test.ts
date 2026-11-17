import { QueryClient, QueryObserver } from '@tanstack/query-core'
import { getOperationAST, Kind, parse } from 'graphql'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  analyzeMutation, assertClientManifest, DocumentAnalyzer, GolemCommitUnknownError,
  GolemConfigurationError, GolemError, GolemPartialCommitError, LiveSync, Mutations,
  type ClientManifest, type ExecuteRequest, type LiveTransport, type StreamHandlers, type SubscriptionRequest,
} from '../src/index.js'

const model = { primaryKey: ['id'], cursorSelector: 'ID', uniqueInput: null, events: null, relations: {}, computed: {} } as const
const manifest = {
  contract: 'mutation-scope',
  models: { Invite: model, Team: model, Other: model },
  roots: {
    teams: { kind: 'generated', shape: 'rows', nullable: false, models: ['Team'] },
    invites: { kind: 'generated', shape: 'rows', nullable: false, models: ['Invite'] },
    others: { kind: 'generated', shape: 'rows', nullable: false, models: ['Other'] },
    submitNested: { kind: 'opaque', shape: 'row', nullable: false, models: ['Invite'], mutationScope: { kind: 'unscoped' } },
    createInvite: { kind: 'generated', shape: 'row', nullable: false, models: ['Invite'], mutationScope: { kind: 'models', models: ['Invite', 'Team'] } },
    oldCreateInvite: { kind: 'generated', shape: 'row', nullable: false, models: ['Invite'] },
  },
} as const satisfies ClientManifest

describe('canonical mutation scope metadata', () => {
  it('uses server write reach independently of the response and accepts the additive hint without a capability or format bump', () => {
    expect(() => assertClientManifest(manifest)).not.toThrow()
    expect(analyzeMutation('mutation{a:createInvite{id} b:submitNested{id} oldCreateInvite{id}}', manifest).roots).toEqual([
      { name: 'createInvite', mutationScope: { kind: 'models', models: ['Invite', 'Team'] } },
      { name: 'submitNested', mutationScope: { kind: 'unscoped' } },
      { name: 'oldCreateInvite', mutationScope: { kind: 'unscoped' } },
    ])
    expect(manifest.roots.createInvite.models).toEqual(['Invite'])
    expect(new DocumentAnalyzer(manifest).analyzeStatic('{teams{name}}').dependencies).toEqual({ models: ['Team'], opaque: false })
  })

  it.each([
    null, [], 'models', {}, { kind: 'future' },
    { kind: 'models' }, { kind: 'models', models: [] },
    { kind: 'models', models: ['Team', 'Team'] },
    { kind: 'models', models: ['team'] }, { kind: 'models', models: ['PrivateAudit'] },
    { kind: 'models', models: ['toString'] }, { kind: 'models', models: [null] },
    { kind: 'models', models: 'Team' }, { kind: 'models', models: ['Team'], extra: true },
    { kind: 'unscoped', models: [] }, { kind: 'unscoped', extra: true },
  ].map((scope) => [scope] as const))('rejects malformed or noncanonical scope %j before query or mutation analysis', (mutationScope) => {
    const invalid = { ...manifest, roots: { ...manifest.roots, createInvite: { ...manifest.roots.createInvite, mutationScope } } } as unknown as ClientManifest
    expect(() => new DocumentAnalyzer(invalid)).toThrow(GolemConfigurationError)
    expect(() => analyzeMutation('mutation{createInvite{id}}', invalid)).toThrow(GolemConfigurationError)
  })
})

// The fake owns only transport I/O. Actual SDK read owners, QueryClient and
// QueryObserver decide dependency selection, cancellation and refetching.
class ScopeTransport implements LiveTransport {
  revision = 0
  failure: GolemPartialCommitError | GolemCommitUnknownError | undefined
  readonly streams: SubscriptionRequest[] = []
  async execute<T>(request: ExecuteRequest): Promise<T> {
    const operation = getOperationAST(parse(request.query))!
    const selected = operation.selectionSet.selections[0]!
    if (selected.kind !== Kind.FIELD) throw new Error('fixture requires a field root')
    const name = selected.name.value
    if (operation.operation === 'mutation') {
      this.revision++
      if (this.failure !== undefined) throw this.failure
      return { [name]: { id: 'invite' } } as T
    }
    const typename = name === 'teams' ? 'Team' : name === 'invites' ? 'Invite' : 'Other'
    return { [name]: [{ id: name, name: `${name}-${this.revision}`, __typename: typename, _version: String(this.revision + 1) }] } as T
  }
  subscribe(request: SubscriptionRequest, handlers: StreamHandlers): () => void {
    this.streams.push(request)
    let stopped = false
    queueMicrotask(() => { if (!stopped) handlers.onAck() })
    return () => { stopped = true }
  }
  renew(): void {}
}

const cleanup: Array<() => void> = []
afterEach(() => { for (const close of cleanup.splice(0).reverse()) close() })

function setup(mode: 'static' | 'live') {
  const described: ClientManifest = mode === 'static' ? manifest : { ...manifest, models: { ...manifest.models, Team: { ...model, uniqueInput: 'TeamWhereUniqueInput', events: 'teamEvents' } } }
  const analyzer = new DocumentAnalyzer(described)
  const transport = new ScopeTransport()
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } })
  const live = new LiveSync({ queryClient, transport, manifest: described })
  if (mode === 'live') live.start()
  cleanup.push(() => { live.dispose(); queryClient.clear() })
  const mutations = new Mutations({ queryClient, transport, manifest: described })
  const activeOptions = mode === 'static' ? live.staticOptions(analyzer.analyzeStatic('{teams{name}}')) : live.options(analyzer.analyze('{teams{name}}'))
  const observer = new QueryObserver(queryClient, activeOptions)
  cleanup.push(observer.subscribe(() => undefined))
  return { queryClient, transport, live, mutations, observer, analyzer }
}

describe.each(['static', 'live'] as const)('mutation scope invalidation through the %s read owner', (mode) => {
  it.each([
    ['custom write returning Invite while also changing Team', 'submitNested', true],
    ['generated nested write returning Invite while also changing Team', 'createInvite', false],
    ['old manifest with response models but no write scope', 'oldCreateInvite', true],
  ] as const)('refreshes Team for a %s', async (_scenario, root, unscoped) => {
    for (const outcome of ['success', 'PARTIAL', 'UNKNOWN'] as const) {
      const view = setup(mode)
      await view.observer.refetch()
      const inactive = view.live.staticOptions(view.analyzer.analyzeStatic('query Inactive{teams{name}}'))
      const returned = view.live.staticOptions(view.analyzer.analyzeStatic('{invites{name}}'))
      const unrelated = view.live.staticOptions(view.analyzer.analyzeStatic('{others{name}}'))
      await view.queryClient.fetchQuery(inactive)
      await view.queryClient.fetchQuery(returned)
      await view.queryClient.fetchQuery(unrelated)
      view.queryClient.setQueryData(['application'], { kept: true })
      const evidence = new GolemError({ message: outcome })
      view.transport.failure = outcome === 'success' ? undefined : outcome === 'PARTIAL'
        ? new GolemPartialCommitError(evidence, [evidence], { [root]: { id: 'invite' } })
        : new GolemCommitUnknownError(evidence, [evidence], undefined)
      const document = root === 'createInvite'
        ? 'mutation{createInvite(data:{team:{create:{name:"changed"}}}){id}}'
        : `mutation{${root}{id}}`
      const pending = view.mutations.execute(document)
      if (outcome === 'success') await expect(pending).resolves.toEqual({ [root]: { id: 'invite' } })
      else await expect(pending).rejects.toBe(view.transport.failure)
      await vi.waitFor(() => expect(view.observer.getCurrentResult().data).toMatchObject({ teams: [{ name: 'teams-1' }] }))
      expect(view.queryClient.getQueryState(inactive.queryKey)?.isInvalidated).toBe(true)
      expect(view.queryClient.getQueryState(returned.queryKey)?.isInvalidated).toBe(true)
      expect(view.queryClient.getQueryState(unrelated.queryKey)?.isInvalidated).toBe(unscoped)
      expect(view.queryClient.getQueryState(['application'])?.isInvalidated).toBe(false)
      expect(view.transport.streams).toHaveLength(mode === 'static' ? 0 : 1)
    }
  })
})
