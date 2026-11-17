import { useCallback, useMemo, useSyncExternalStore } from 'react'
import {
  hashKey,
  useInfiniteQuery,
  MutationObserver,
  notifyManager,
  useQuery,
  type InfiniteData,
  type MutateOptions,
  type UseInfiniteQueryOptions,
  type UseInfiniteQueryResult,
  type UseMutateAsyncFunction,
  type UseMutateFunction,
  type UseMutationOptions,
  type UseMutationResult,
  type UseQueryOptions,
  type UseQueryResult,
} from '@tanstack/react-query'
import type { ContinuationPageParam, Cursor } from '@eleven-am/golem-client'
import { useGolemClient } from './context.js'
import type { GolemDocument, ResultFor, VariablesFor } from './types.js'

type Variables = Readonly<Record<string, unknown>>

export type GolemQueryOptions<TData> = Omit<UseQueryOptions<TData, Error, TData, readonly unknown[]>, 'queryKey' | 'queryFn' | 'meta' | 'structuralSharing'>

export type GolemInfiniteQueryOptions<TPage> = Omit<
  UseInfiniteQueryOptions<TPage, Error, InfiniteData<TPage, Cursor | null>, readonly unknown[], Cursor | null>,
  'queryKey' | 'queryFn' | 'meta' | 'structuralSharing' | 'initialPageParam' | 'getNextPageParam'
>

export type GolemContinuationQueryOptions<TPage> = Omit<
  UseInfiniteQueryOptions<TPage, Error, InfiniteData<TPage, ContinuationPageParam>, readonly unknown[], ContinuationPageParam>,
  'queryKey' | 'queryFn' | 'meta' | 'structuralSharing' | 'initialPageParam' | 'getNextPageParam'
>

export type GolemMutationOptions<TData, TVariables, TContext = unknown> = Omit<UseMutationOptions<TData, Error, TVariables, TContext>, 'mutationFn'> & { readonly keepalive?: boolean }

export function useGolemQuery<TDocument extends GolemDocument>(
  document: TDocument,
  variables?: VariablesFor<TDocument>,
  options?: GolemQueryOptions<ResultFor<TDocument>>,
): UseQueryResult<ResultFor<TDocument>, Error> {
  const client = useGolemClient()
  const variablesHash = hashKey([variables ?? {}])
  const base = useMemo(() => client.queryOptions(document, (variables ?? {}) as Variables), [client, document, variablesHash])
  return useQuery({ ...options, ...base } as UseQueryOptions<ResultFor<TDocument>, Error, ResultFor<TDocument>, readonly unknown[]>)
}

export function useGolemStaticQuery<TDocument extends GolemDocument>(
  document: TDocument,
  variables?: VariablesFor<TDocument>,
  options?: GolemQueryOptions<ResultFor<TDocument>>,
): UseQueryResult<ResultFor<TDocument>, Error> {
  const client = useGolemClient()
  const variablesHash = hashKey([variables ?? {}])
  const base = useMemo(() => client.staticQueryOptions(document, (variables ?? {}) as Variables), [client, document, variablesHash])
  return useQuery({ ...options, ...base } as UseQueryOptions<ResultFor<TDocument>, Error, ResultFor<TDocument>, readonly unknown[]>)
}

export function useGolemInfiniteQuery<TDocument extends GolemDocument>(
  document: TDocument,
  variables?: VariablesFor<TDocument>,
  options?: GolemInfiniteQueryOptions<ResultFor<TDocument>>,
): UseInfiniteQueryResult<InfiniteData<ResultFor<TDocument>, Cursor | null>, Error> {
  const client = useGolemClient()
  const variablesHash = hashKey([variables ?? {}])
  const base = useMemo(() => client.infiniteQueryOptions(document, (variables ?? {}) as Variables), [client, document, variablesHash])
  return useInfiniteQuery({ ...options, ...base } as unknown as UseInfiniteQueryOptions<ResultFor<TDocument>, Error, InfiniteData<ResultFor<TDocument>, Cursor | null>, readonly unknown[], Cursor | null>)
}

export function useGolemStaticInfiniteQuery<TDocument extends GolemDocument>(
  document: TDocument,
  variables?: VariablesFor<TDocument>,
  options?: GolemInfiniteQueryOptions<ResultFor<TDocument>>,
): UseInfiniteQueryResult<InfiniteData<ResultFor<TDocument>, Cursor | null>, Error> {
  const client = useGolemClient()
  const variablesHash = hashKey([variables ?? {}])
  const base = useMemo(() => client.staticInfiniteQueryOptions(document, (variables ?? {}) as Variables), [client, document, variablesHash])
  return useInfiniteQuery({ ...options, ...base } as unknown as UseInfiniteQueryOptions<ResultFor<TDocument>, Error, InfiniteData<ResultFor<TDocument>, Cursor | null>, readonly unknown[], Cursor | null>)
}

export function useGolemContinuationQuery<TDocument extends GolemDocument>(
  document: TDocument,
  variables?: VariablesFor<TDocument>,
  options?: GolemContinuationQueryOptions<ResultFor<TDocument>>,
): UseInfiniteQueryResult<InfiniteData<ResultFor<TDocument>, ContinuationPageParam>, Error> {
  const client = useGolemClient()
  const variablesHash = hashKey([variables ?? {}])
  const base = useMemo(() => client.continuationQueryOptions(document, (variables ?? {}) as Variables), [client, document, variablesHash])
  return useInfiniteQuery({ ...options, ...base } as unknown as UseInfiniteQueryOptions<ResultFor<TDocument>, Error, InfiniteData<ResultFor<TDocument>, ContinuationPageParam>, readonly unknown[], ContinuationPageParam>)
}

export function useGolemStaticContinuationQuery<TDocument extends GolemDocument>(
  document: TDocument,
  variables?: VariablesFor<TDocument>,
  options?: GolemContinuationQueryOptions<ResultFor<TDocument>>,
): UseInfiniteQueryResult<InfiniteData<ResultFor<TDocument>, ContinuationPageParam>, Error> {
  const client = useGolemClient()
  const variablesHash = hashKey([variables ?? {}])
  const base = useMemo(() => client.staticContinuationQueryOptions(document, (variables ?? {}) as Variables), [client, document, variablesHash])
  return useInfiniteQuery({ ...options, ...base } as unknown as UseInfiniteQueryOptions<ResultFor<TDocument>, Error, InfiniteData<ResultFor<TDocument>, ContinuationPageParam>, readonly unknown[], ContinuationPageParam>)
}

export function useGolemMutation<TDocument extends GolemDocument, TContext = unknown>(
  document: TDocument,
  options?: GolemMutationOptions<ResultFor<TDocument>, VariablesFor<TDocument>, TContext>,
): UseMutationResult<ResultFor<TDocument>, Error, VariablesFor<TDocument>, TContext> {
  const client = useGolemClient()
  type Data = ResultFor<TDocument>
  type Vars = VariablesFor<TDocument>
  type Observer = MutationObserver<Data, Error, Vars, TContext>
  const { keepalive, ...supplied } = options ?? {}
  // Default before forcing retry:false: application defaults cannot replay a
  // mutation whose commit may already have happened.
  const mutation = client.queryClient.defaultMutationOptions(supplied)
  const binding = useMemo(() => {
    let observer: Observer = new MutationObserver<Data, Error, Vars, TContext>(client.queryClient, { retry: false })
    let stop: (() => void) | undefined
    let stopReset: (() => void) | undefined
    const listeners = new Set<() => void>()
    const notify = () => { for (const listener of listeners) listener() }
    return {
      current: () => observer,
      snapshot: () => observer.getCurrentResult(),
      subscribe(listener: () => void) {
        listeners.add(listener)
        if (stop === undefined) stop = observer.subscribe(notifyManager.batchCalls(notify))
        if (stopReset === undefined) stopReset = client.onSessionReset(() => { observer.reset(); notify() })
        return () => {
          listeners.delete(listener)
          if (listeners.size === 0) { stop?.(); stop = undefined; stopReset?.(); stopReset = undefined }
        }
      },
      invoke(next: Observer, variables: Vars, callbacks?: MutateOptions<Data, Error, Vars, TContext>) {
        stop?.()
        observer = next
        stop = listeners.size === 0 ? undefined : observer.subscribe(notifyManager.batchCalls(notify))
        const pending = observer.mutate(variables, callbacks)
        notify()
        return pending
      },
    }
  }, [client])
  const observer = binding.current()
  observer.setOptions({ ...mutation, meta: { ...mutation.meta, golem: { kind: 'mutation' } }, mutationFn: observer.options.mutationFn, retry: false })
  const result = useSyncExternalStore(binding.subscribe, binding.snapshot, binding.snapshot)
  const mutateAsync = useCallback<UseMutateAsyncFunction<Data, Error, Vars, TContext>>((...args: Parameters<UseMutateAsyncFunction<Data, Error, Vars, TContext>>) => {
    // TanStack makes the first tuple entry optional exactly when undefined is
    // an allowed variable value. Keep that public tuple, including zero args.
    const variables = args[0] as Vars
    const callbacks = args[1]
    let execute: () => Promise<Data>
    try {
      execute = client.mutations.prepare<Data>(document, variables as Variables, keepalive === undefined ? undefined : { keepalive })
    } catch (error) {
      execute = () => Promise.reject(error)
    }
    // Each TanStack mutation owns its prepared closure. Variables, including
    // undefined, stay untouched for cache callbacks, user callbacks and state.
    // An async onMutate or a same-scope queue cannot rebind another intent.
    const observer = new MutationObserver<Data, Error, Vars, TContext>(client.queryClient, {
      ...mutation, meta: { ...mutation.meta, golem: { kind: 'mutation' } }, mutationFn: execute, retry: false,
    })
    return binding.invoke(observer, variables, callbacks)
  }, [binding, client, document, keepalive, mutation])
  const mutate = useCallback<UseMutateFunction<Data, Error, Vars, TContext>>((...args: Parameters<UseMutateFunction<Data, Error, Vars, TContext>>) => {
    void mutateAsync(...args).catch(() => undefined)
  }, [mutateAsync])
  const throwOnError = mutation.throwOnError
  if (result.error !== null && (typeof throwOnError === 'function' ? throwOnError(result.error) : throwOnError === true)) throw result.error
  return { ...result, mutate, mutateAsync }
}
