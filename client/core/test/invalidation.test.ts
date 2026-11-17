import { InfiniteQueryObserver, QueryClient, QueryObserver, type InfiniteData } from '@tanstack/query-core'
import { buildSchema, Kind, parse, print, validate } from 'graphql'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  DocumentAnalyzer, GolemClient, GolemConfigurationError, GolemError, GolemNotLiveError,
  GolemProtocolError, LiveSync, Mutations, OPERATION_INVALIDATION_CAPABILITY,
  Transport, golemBlock, streamRequest,
  type ClientManifest, type ContinuationPageParam, type ExecuteRequest, type LiveTransport, type StreamHandlers, type SubscriptionRequest,
} from '../src/index.js'
import { continuationManifest } from './fixtures/continuation.js'

const manifest = {
  ...continuationManifest, requiredCapabilities: [OPERATION_INVALIDATION_CAPABILITY],
  models: { Movie: { primaryKey: ['id'], cursorSelector: 'ID', uniqueInput: 'MovieWhereUniqueInput', events: null, relations: {}, computed: {} } },
  roots: {
    homeFeed: { ...continuationManifest.roots.homeFeed, invalidation: { kind: 'operation', subscriptionField: 'homeFeedChanges' } },
    state: { kind: 'opaque', shape: 'object', models: [], nullable: false, result: { kind: 'object', name: 'HomeSegment', nullable: false }, invalidation: { kind: 'operation', subscriptionField: 'stateChanges' } },
    staticFeed: continuationManifest.roots.homeFeed,
    movies: { kind: 'generated', shape: 'rows', models: ['Movie'], nullable: false },
    updateMovie: { kind: 'generated', shape: 'row', models: ['Movie'], nullable: false, mutationScope: { kind: 'models', models: ['Movie'] } },
    changeState: { kind: 'opaque', shape: 'value', models: [], nullable: false },
  },
  types: { ...continuationManifest.types, HomeFeedPage: { kind: 'object', fields: {
    items: { kind: 'list', nullable: false, maxItems: 100, element: { kind: 'model', name: 'Movie', nullable: false } },
    next: { kind: 'scalar', name: 'String', nullable: true },
  } } },
} as const satisfies ClientManifest
const schema = buildSchema(`
 type Query { homeFeed(seed:Int!,take:Int,after:String):HomeFeedPage! staticFeed(seed:Int!,take:Int,after:String):HomeFeedPage! state(seed:Int!,take:Int,after:String):HomeSegment! movies:[Movie!]! }
 type Mutation { changeState:Boolean! updateMovie:Movie! }
 type Subscription { homeFeedChanges(seed:Int!):Boolean! stateChanges(seed:Int!,take:Int,after:String):Boolean! }
 type HomeFeedPage {items:[Movie!]!,next:String}
 type HomeSegment {kind:String!,index:Int!}
 type Movie {id:ID!,title:String!,_version:String!}
`)
const analyzer = new DocumentAnalyzer(manifest)
const STATE = 'query State($seed:Int!){ state(seed:$seed){index} }'
const PAGE = 'query Feed($seed:Int!){ homeFeed(seed:$seed,take:1){items{title}} }'
const terminated = () => ({ kind: 'error' as const, errors: [new GolemError({ message: 'invalidate', extensions: { code: 'GOLEM_SUBSCRIPTION_RESYNC', reason: 'DEPENDENCY_CHANGED' } })] })
const deferred = <T>() => { let resolve!: (value:T)=>void; const promise = new Promise<T>((done)=>{resolve=done}); return {promise,resolve} }
class OperationTransport implements LiveTransport {
  streams: Array<{handlers:StreamHandlers;request:SubscriptionRequest;stopped:boolean}> = []
  calls: ExecuteRequest[] = []
  pending: Array<ReturnType<typeof deferred<unknown>>> = []
  revision = 0
  hold = false
  response(request:ExecuteRequest): unknown {
    if (request.query.includes('mutation')) return request.query.includes('updateMovie') ? {updateMovie:{title:`revision-${this.revision}`}} : {changeState:true}
    if (!request.query.includes('homeFeed')) return {state:{index:this.revision}}
    const token = request.variables?.golemAfter
    const index = typeof token === 'string' ? Number(token.split(':')[1]) : 0
    return {homeFeed:{items:index===1?[]:[{title:`revision-${this.revision}-${index}`}],_golemNext:index<3?`${this.revision}:${index+1}`:null}}
  }
  async execute<T>(request:ExecuteRequest):Promise<T> {
    this.calls.push(request)
    if (!this.hold) return this.response(request) as T
    const pending=deferred<unknown>();this.pending.push(pending);return await pending.promise as T
  }
  subscribe(request:SubscriptionRequest,handlers:StreamHandlers):()=>void {
    const stream={request,handlers,stopped:false};this.streams.push(stream);return ()=>{stream.stopped=true}
  }
  renew():void {}
}
const cleanup:Array<()=>void>=[]
beforeEach(()=>{vi.useFakeTimers()})
afterEach(()=>{for(const close of cleanup.splice(0).reverse())close();vi.useRealTimers()})
const flush=()=>vi.advanceTimersByTimeAsync(0)
function setup(){
  const queryClient=new QueryClient({defaultOptions:{queries:{retry:false,staleTime:Infinity,gcTime:Infinity}}})
  const transport=new OperationTransport()
  const live=new LiveSync({queryClient,transport,manifest,clock:()=>Date.now(),random:()=>0})
  live.start();cleanup.push(()=>queryClient.clear(),()=>live.dispose())
  return {queryClient,transport,live}
}
function pages(){
  const base=setup();const options=base.live.continuationOptions(analyzer.analyze(PAGE),{seed:7})
  const observer=new InfiniteQueryObserver<unknown,Error,InfiniteData<unknown,ContinuationPageParam>,readonly unknown[],ContinuationPageParam>(base.queryClient,options)
  const stop=observer.subscribe(()=>undefined);cleanup.push(stop)
  const query=base.queryClient.getQueryCache().find({queryKey:options.queryKey,exact:true})!
  return {...base,options,observer,stop,query}
}
async function threePages(view:ReturnType<typeof pages>){view.transport.streams[0]!.handlers.onAck();await flush();await view.observer.fetchNextPage();await view.observer.fetchNextPage();expect(view.observer.getCurrentResult().data?.pages).toHaveLength(3)}

describe('declared operation invalidation plans and capability negotiation',()=>{
  it('globally refuses unknown/missing requirements before a row-stream fallback',()=>{
    expect(()=>new DocumentAnalyzer({...manifest,requiredCapabilities:['future-capability']})).toThrow(GolemConfigurationError)
    expect(()=>new DocumentAnalyzer({...manifest,requiredCapabilities:undefined})).toThrow(GolemConfigurationError)
    expect(()=>new DocumentAnalyzer({...manifest,formatVersion:2})).toThrow(GolemConfigurationError)
    expect(()=>new Transport({url:'http://unused.test/graphql',manifest:{contract:'future',formatVersion:3,requiredCapabilities:['future-capability']}})).toThrow(GolemConfigurationError)
    expect(golemBlock({contract:'old'})).toEqual({protocol:1,contract:'old'})
    expect(golemBlock({contract:'dto',formatVersion:2})).toEqual({protocol:1,contract:'dto',manifestVersion:2})
    expect(golemBlock({contract:'page',formatVersion:3})).toEqual({protocol:1,contract:'page',manifestVersion:3})
    expect(golemBlock(manifest)).toEqual({protocol:1,contract:manifest.contract,manifestVersion:3,capabilities:[OPERATION_INVALIDATION_CAPABILITY]})
  })
  it('observes the operation independently of returned models, preserving fragments/directives and private projections',()=>{
    const document='query Feed($seed:Int!,$show:Boolean!,$take:Int){ f:homeFeed(seed:$seed,take:$take){...Page} } fragment Page on HomeFeedPage {items {...MovieFields}} fragment MovieFields on Movie {title @include(if:$show)}'
    const analysis=analyzer.analyze(document)
    expect(analysis.live).toBe(true)
    expect(analysis.query).toBe(print(parse(document)))
    expect(analysis.streams).toHaveLength(1)
    const plan=analysis.streams[0]!
    expect(plan.kind).toBe('invalidation');expect('model' in plan).toBe(false)
    const request=streamRequest(plan,{seed:7,show:true,take:25})
    expect(validate(schema,parse(request.query))).toEqual([])
    expect(request.variables).toEqual({seed:7})
    expect(request.query).toContain('homeFeedChanges(seed: $seed)')
    for(const field of ['id','_version','__typename'])expect(analysis.query).not.toContain(field)
    const authored='{ homeFeed(seed:7){items{title id _version __typename}} }'
    expect(analyzer.analyze(authored).query).toBe(print(parse(authored)))
    const ordinary=analyzer.analyze('query($seed:Int!,$take:Int,$after:String){state(seed:$seed,take:$take,after:$after){index}}')
    expect(streamRequest(ordinary.streams[0]!,{seed:7,take:2,after:'ordinary'}).variables).toEqual({seed:7,take:2,after:'ordinary'})
    expect(ordinary.streams[0]!.query).toContain('after: $after')
  })
  it('keeps a shared authored fragment private while ordinary row roots retain their enriched clone',()=>{
    const mixed=new DocumentAnalyzer({...manifest,models:{Movie:{...manifest.models.Movie,events:'movieEvents'}}})
    const analysis=mixed.analyze('{ homeFeed(seed:7){items{...MovieFields}} movies{...MovieFields} } fragment MovieFields on Movie {title}')
    expect(validate(schema,parse(analysis.query))).toEqual([])
    const document=parse(analysis.query)
    const original=document.definitions.find((definition)=>definition.kind===Kind.FRAGMENT_DEFINITION&&definition.name.value==='MovieFields')
    expect(original?.kind).toBe(Kind.FRAGMENT_DEFINITION)
    if(original?.kind!==Kind.FRAGMENT_DEFINITION)throw new Error('missing authored fragment')
    expect(print(original.selectionSet)).toBe('{\n  title\n}')
    expect(analysis.query).toContain('golemLiveFragment')
    expect(analysis.streams.map((plan)=>plan.kind??'rows')).toEqual(['invalidation','rows'])
    expect(analysis.query).toContain('_version')
  })
  it('advertises the capability for static HTTP but opens no stream',async()=>{
    const queryClient=new QueryClient();const socket=vi.fn(()=>{throw new Error('static operation opened a socket')})
    const fetch=vi.fn<typeof globalThis.fetch>(async(_url,init)=>{
      expect(JSON.parse(String(init?.body)).extensions.golem.capabilities).toEqual([OPERATION_INVALIDATION_CAPABILITY])
      return new Response(JSON.stringify({data:{state:{index:1}}}))
    })
    const client=new GolemClient({queryClient,url:'http://static.test/graphql',manifest,fetch,createSocket:socket})
    cleanup.push(()=>queryClient.clear(),()=>client.dispose())
    await client.staticQueryOptions(STATE,{seed:7}).queryFn({signal:new AbortController().signal})
    expect(socket).not.toHaveBeenCalled()
    expect(()=>client.continuationQueryOptions('{staticFeed(seed:7){items{title}}}')).toThrow(GolemNotLiveError)
  })
})

describe('terminal-only invalidation lifecycle',()=>{
  it('waits for ack, then refetches a scalar-only result on terminal invalidation without row packets',async()=>{
    const view=setup();const options=view.live.options(analyzer.analyze(STATE),{seed:7})
    const observer=new QueryObserver(view.queryClient,options);cleanup.push(observer.subscribe(()=>undefined))
    const query=view.queryClient.getQueryCache().find({queryKey:options.queryKey,exact:true})!
    expect(view.transport.calls).toHaveLength(0);expect(view.transport.streams).toHaveLength(1)
    view.transport.streams[0]!.handlers.onAck();await flush();expect(query.state.data).toEqual({state:{index:0}})
    view.transport.revision=1;view.transport.streams[0]!.handlers.onEnd(terminated())
    expect(query.state.data).toBeUndefined();expect(view.live.statusOf(query)?.state).toBe('RECOVERING')
    await vi.advanceTimersByTimeAsync(250);expect(view.transport.calls).toHaveLength(1)
    view.transport.streams[1]!.handlers.onAck();await flush()
    expect(query.state.data).toEqual({state:{index:1}});expect(view.transport.calls).toHaveLength(2)
  })
  it('rejects any data packet before row parsing or patching',async()=>{
    const view=setup();const options=view.live.options(analyzer.analyze(STATE),{seed:7})
    const observer=new QueryObserver(view.queryClient,options);cleanup.push(observer.subscribe(()=>undefined))
    const query=view.queryClient.getQueryCache().find({queryKey:options.queryKey,exact:true})!
    view.transport.streams[0]!.handlers.onAck();await flush()
    view.transport.streams[0]!.handlers.onEvent({data:{stateChanges:true}})
    expect(query.state.data).toBeUndefined();expect(view.live.statusOf(query)?.state).toBe('FAILED')
    expect(view.live.statusOf(query)?.error).toBeInstanceOf(GolemProtocolError)
    expect(view.live.statusOf(query)?.error?.message).toContain('unexpected data packet')
  })
  it('restarts a live continuation at null and prevents an old in-flight page from appending',async()=>{
    const view=pages();await threePages(view)
    view.transport.hold=true;void view.observer.fetchNextPage();await flush()
    expect(view.transport.pending).toHaveLength(1)
    view.transport.streams[0]!.handlers.onEnd(terminated());expect(view.query.state.data).toBeUndefined()
    view.transport.revision=1;view.transport.hold=false
    await vi.advanceTimersByTimeAsync(250);view.transport.streams[1]!.handlers.onAck();await flush()
    expect(view.observer.getCurrentResult().data?.pageParams).toEqual([null])
    expect(view.transport.calls.at(-1)!.variables).toEqual({seed:7})
    view.transport.pending[0]!.resolve({homeFeed:{items:[{title:'stale-page'}],_golemNext:null}});await flush()
    expect(view.observer.getCurrentResult().data?.pages).toHaveLength(1)
    expect(JSON.stringify(view.query.state.data)).not.toContain('stale-page')
  })
  it('honors refetchType:none while clearing three pages, then resets automatic and own mutation invalidations',async()=>{
    const view=pages();await threePages(view)
    view.transport.hold=true;void view.observer.fetchNextPage();await flush()
    const before=view.transport.calls.length
    await view.queryClient.invalidateQueries({queryKey:view.query.queryKey,exact:true,refetchType:'none'})
    expect(view.query.state.data).toBeUndefined();expect(view.live.statusOf(view.query)?.state).toBe('RECOVERING')
    await vi.advanceTimersByTimeAsync(1000);expect(view.transport.calls).toHaveLength(before)
    view.transport.hold=false;view.transport.revision=1;await view.observer.refetch()
    expect(view.observer.getCurrentResult().data?.pageParams).toEqual([null])
    view.transport.pending[0]!.resolve({homeFeed:{items:[{title:'obsolete'}],_golemNext:null}});await flush()
    expect(JSON.stringify(view.query.state.data)).not.toContain('obsolete')
    await view.observer.fetchNextPage();await view.observer.fetchNextPage()
    view.transport.revision=2;await view.queryClient.invalidateQueries({queryKey:view.query.queryKey,exact:true})
    expect(view.observer.getCurrentResult().data?.pageParams).toEqual([null])
    await view.observer.fetchNextPage();await view.observer.fetchNextPage()
    const mutations=new Mutations({queryClient:view.queryClient,transport:view.transport,manifest})
    view.transport.revision=3;await mutations.execute('mutation{changeState}');await flush()
    expect(view.observer.getCurrentResult().data?.pageParams).toEqual([null])
    expect(view.transport.calls.at(-1)!.variables).toEqual({seed:7})
  })
  it('conservatively resets an opaque live page after an own scoped model write', async () => {
    const view = pages(); await threePages(view)
    const mutations = new Mutations({ queryClient: view.queryClient, transport: view.transport, manifest })
    view.transport.revision = 1
    await mutations.execute('mutation{updateMovie{title}}'); await flush()
    expect(view.observer.getCurrentResult().data?.pageParams).toEqual([null])
    expect(view.observer.getCurrentResult().data?.pages).toHaveLength(1)
    expect(view.transport.calls.at(-1)!.variables).toEqual({ seed: 7 })
  })
  it('fences an inactive cached continuation invalidate and starts a new population after a subscription gap',async()=>{
    const view=pages();await threePages(view)
    view.stop();await vi.advanceTimersByTimeAsync(1000)
    expect(view.live.statusOf(view.query)).toBeUndefined()
    view.transport.hold=true
    void view.queryClient.refetchQueries({queryKey:view.query.queryKey,exact:true,type:'all'});await flush()
    expect(view.transport.pending).toHaveLength(1)
    await view.queryClient.invalidateQueries({queryKey:view.query.queryKey,exact:true,refetchType:'none'})
    expect(view.query.state.data).toBeUndefined()
    view.transport.pending[0]!.resolve({homeFeed:{items:[{title:'inactive-obsolete'}],_golemNext:null}});await flush()
    expect(view.query.state.data).toBeUndefined()
    view.transport.hold=false;view.transport.revision=1
    cleanup.push(view.observer.subscribe(()=>undefined))
    expect(view.transport.streams).toHaveLength(2)
    view.transport.streams[1]!.handlers.onAck();await flush()
    expect(view.observer.getCurrentResult().data?.pageParams).toEqual([null])
    expect(view.transport.calls.at(-1)!.variables).toEqual({seed:7})
  })
  it('fences an inactive full-refetch result after retryer resolution but before cache commit', async () => {
    const view = pages()
    await threePages(view)
    view.stop()
    await vi.advanceTimersByTimeAsync(1000)
    expect(view.live.statusOf(view.query)).toBeUndefined()
    let invalidating: Promise<void> | undefined
    let cancellationWasTooLate = false
    view.query.setOptions({ ...view.query.options, structuralSharing: (previous, next) => {
      if (invalidating === undefined) {
        // This callback is inside real Query.setData after retryer settlement.
        // There is no active Entry; cancel alone can no longer stop the commit.
        invalidating = view.queryClient.invalidateQueries({ queryKey: view.query.queryKey, exact: true, refetchType: 'none' })
        cancellationWasTooLate = view.transport.calls.at(-1)!.signal?.aborted === false
      }
      return view.live.structuralSharing(previous, next)
    } })
    view.transport.revision = 1
    await view.queryClient.refetchQueries({ queryKey: view.query.queryKey, exact: true, type: 'all' })
    await invalidating
    expect(cancellationWasTooLate).toBe(true)
    expect(view.transport.calls).toHaveLength(6)
    expect(view.query.state.data).toBeUndefined()
    expect(view.query.state.fetchStatus).toBe('idle')
    expect(view.live.statusOf(view.query)).toBeUndefined()
    expect(view.transport.streams).toHaveLength(1)
  })

  it('retains a quick remount chain but discards cached pages when reopening after close grace',async()=>{
    const view=pages();await threePages(view)
    const cached=view.query.state.data
    view.stop();await vi.advanceTimersByTimeAsync(100)
    const quickStop=view.observer.subscribe(()=>undefined);cleanup.push(quickStop)
    expect(view.query.state.data).toBe(cached);expect(view.transport.streams).toHaveLength(1)
    quickStop();await vi.advanceTimersByTimeAsync(1000)
    view.transport.revision=1
    cleanup.push(view.observer.subscribe(()=>undefined))
    expect(view.query.state.data).toBeUndefined()
    expect(view.transport.streams).toHaveLength(2)
    view.transport.streams[1]!.handlers.onAck();await flush()
    expect(view.observer.getCurrentResult().data?.pageParams).toEqual([null])
    expect(view.transport.calls.at(-1)!.variables).toEqual({seed:7})
  })
})
