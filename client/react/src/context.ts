import { createContext, useContext } from 'react'
import { GolemConfigurationError, type GolemClient } from '@eleven-am/golem-client'

export const GolemContext = createContext<GolemClient | null>(null)

export function useGolemClient(): GolemClient {
  const client = useContext(GolemContext)
  if (client === null) {
    throw new GolemConfigurationError('golem hooks must render inside a GolemProvider')
  }
  return client
}
