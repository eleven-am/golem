import { useCallback, useMemo, useSyncExternalStore } from 'react'
import { hashKey, type QueryKey } from '@tanstack/react-query'
import type { GolemClient, LiveStatus } from '@eleven-am/golem-client'
import { useGolemClient } from './context.js'
import type { GolemDocument } from './types.js'

export function useLiveStatus(client: GolemClient, queryKey: QueryKey): LiveStatus | undefined {
  const hash = hashKey(queryKey)
  const subscribe = useCallback((listener: () => void) => client.live.subscribeStatus(listener), [client])
  const snapshot = useCallback(() => {
    const query = client.queryClient.getQueryCache().find({ queryKey, exact: true })
    return query === undefined ? undefined : client.live.statusOf(query)
  }, [client, hash])
  return useSyncExternalStore(subscribe, snapshot, snapshot)
}

export interface GolemStatusOptions {
  readonly infinite?: boolean
}

export function useGolemStatus(document: GolemDocument, variables: Readonly<Record<string, unknown>> = {}, options: GolemStatusOptions = {}): LiveStatus | undefined {
  const client = useGolemClient()
  const variablesHash = hashKey([variables])
  const queryKey = useMemo(
    () => (options.infinite === true ? client.infiniteQueryOptions(document, variables) : client.queryOptions(document, variables)).queryKey,
    [client, document, variablesHash, options.infinite],
  )
  return useLiveStatus(client, queryKey)
}
