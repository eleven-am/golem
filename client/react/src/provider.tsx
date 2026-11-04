import { useCallback, useSyncExternalStore, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { GolemConfigurationError, type GolemClient } from '@eleven-am/golem-client'
import { GolemContext } from './context.js'

export interface GolemProviderProps {
  readonly client: GolemClient
  readonly children?: ReactNode
}

const NEVER_STARTED = () => false

export function GolemProvider({ client, children }: GolemProviderProps) {
  const queryClient = useQueryClient()
  if (queryClient !== client.queryClient) {
    throw new GolemConfigurationError('GolemProvider needs the QueryClient that GolemClient was built with; wrap it in the QueryClientProvider of client.queryClient')
  }
  const subscribe = useCallback(
    (notify: () => void) => {
      const release = client.attach()
      notify()
      return release
    },
    [client],
  )
  const attached = useSyncExternalStore(subscribe, () => client.live.started, NEVER_STARTED)
  return attached ? <GolemContext.Provider value={client}>{children}</GolemContext.Provider> : null
}
