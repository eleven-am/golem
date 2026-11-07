import { useMemo } from 'react'
import {
  hashKey,
  useInfiniteQuery,
  useMutation,
  useQuery,
  type InfiniteData,
  type UseInfiniteQueryOptions,
  type UseInfiniteQueryResult,
  type UseMutationOptions,
  type UseMutationResult,
  type UseQueryOptions,
  type UseQueryResult,
} from '@tanstack/react-query'
import type { Cursor } from '@eleven-am/golem-client'
import { useGolemClient } from './context.js'
import type { GolemDocument, ResultFor, VariablesFor } from './types.js'

type Variables = Readonly<Record<string, unknown>>

export type GolemQueryOptions<TData> = Omit<UseQueryOptions<TData, Error, TData, readonly unknown[]>, 'queryKey' | 'queryFn' | 'meta'>

export type GolemInfiniteQueryOptions<TPage> = Omit<
  UseInfiniteQueryOptions<TPage, Error, InfiniteData<TPage, Cursor | null>, readonly unknown[], Cursor | null>,
  'queryKey' | 'queryFn' | 'meta' | 'initialPageParam' | 'getNextPageParam'
>

export type GolemMutationOptions<TData, TVariables> = Omit<UseMutationOptions<TData, Error, TVariables>, 'mutationFn'> & { readonly keepalive?: boolean }

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

export function useGolemMutation<TDocument extends GolemDocument>(
  document: TDocument,
  options?: GolemMutationOptions<ResultFor<TDocument>, VariablesFor<TDocument>>,
): UseMutationResult<ResultFor<TDocument>, Error, VariablesFor<TDocument>> {
  const client = useGolemClient()
  const { keepalive, ...mutation } = options ?? {}
  return useMutation<ResultFor<TDocument>, Error, VariablesFor<TDocument>>({
    ...mutation,
    mutationFn: (variables) => client.mutate<ResultFor<TDocument>>(document, variables as Variables, keepalive === undefined ? undefined : { keepalive }),
  })
}
