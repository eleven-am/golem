import { readFileSync } from 'node:fs'
import { assertClientManifest, type ClientManifest } from '../../src/manifest.js'
import type { Credentials } from '../../src/session.js'

export const generatedURL = process.env.GOLEM_GENERATED_HTTP_URL
export const generatedFixture = process.env.GOLEM_GENERATED_CLIENT_FIXTURE

export function generatedSocket(url: string, protocols: string | string[], credentials: Credentials): WebSocket {
  const Socket = WebSocket as unknown as new (url: string, options: { protocols: string | string[]; headers: Record<string, string> }) => WebSocket
  return new Socket(url, { protocols, headers: { ...credentials.headers } })
}

// Read the actual owner-emitted JSON literal, never a copied client contract.
export function generatedManifest(): ClientManifest {
  const path = process.env.GOLEM_GENERATED_HTTP_MANIFEST
  if (path === undefined) throw new Error('generated client manifest path is required')
  const source = readFileSync(path, 'utf8')
  const marker = 'export const golemClientManifest = '
  const start = source.indexOf(marker)
  const end = source.indexOf(' as const;', start)
  if (start < 0 || end < 0) throw new Error('generated client manifest is missing')
  const manifest = JSON.parse(source.slice(start + marker.length, end)) as ClientManifest
  assertClientManifest(manifest)
  return manifest
}
