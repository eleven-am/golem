import { readFileSync } from 'node:fs'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRoot } from 'react-dom/client'
import { expect, expectTypeOf, it } from 'vitest'
import { buildSchema, GraphQLInputObjectType, introspectionFromSchema, parse, validate, validateInputValue } from 'graphql'
import { initGraphQLTada, type ResultOf, type VariablesOf } from 'gql.tada'
import { outputIntrospectionFile } from 'gql.tada/internal'
import { GolemClient } from '@eleven-am/golem-client'
import { GolemProvider, useGolemStaticQuery, type ResultFor, type VariablesFor } from '../src/index.js'
import { golemClientManifest } from '../../core/test/fixtures/objects.manifest.js'
import type { introspection } from './fixtures/objects.introspection.js'
import { until } from './harness.js'

const sdl = readFileSync(new URL('../../core/test/fixtures/objects.schema.graphql', import.meta.url), 'utf8')
const schema = buildSchema(sdl)
const graphql = initGraphQLTada<{ introspection: introspection; scalars: { UUID: string } }>()
const HOME = graphql(`query Home($input: HomeInput!) {
  homeFeed(input: $input) { page totalPages totalResults exhausted results { kind index } }
}`)
const COMPUTED = graphql(`query Computed { users { metadata { identity: id items { kind } } } }`)
const CHOICE = graphql(`mutation Choice($target: PlaybackTarget!) { choosePlayback(target: $target) }`)
const validChoice: VariablesFor<typeof CHOICE> = { target: { videoId: '00000000-0000-0000-0000-000000000001' } }
// @ts-expect-error one-of inputs require a choice
const emptyChoice: VariablesFor<typeof CHOICE> = { target: {} }
// @ts-expect-error a selected one-of member cannot be null
const nullChoice: VariablesFor<typeof CHOICE> = { target: { videoId: null } }
// gql.tada infers a structural union: TypeScript permits combined branches.
// Exclusivity remains an authoritative schema check, covered below and over HTTP.
const multipleChoices: VariablesFor<typeof CHOICE> = { target: { videoId: 'one', mediaId: 'two' } }
void validChoice
void emptyChoice
void nullChoice
void multipleChoices

const variables: VariablesOf<typeof HOME> = { input: { range: { seed: 7 } } }
const data: ResultOf<typeof HOME> = {
  homeFeed: { page: 1, totalPages: 1, totalResults: 1, exhausted: true, results: [{ kind: 'BASIC_PICK', index: 1 }] },
}

// These errors must remain visible to applications using the public hook types.
// @ts-expect-error the nested input's Int scalar is not a string
const invalidInput: VariablesFor<typeof HOME> = { input: { range: { seed: '7' } } }
// @ts-expect-error the result's kind is the generated enum, not an arbitrary string
const invalidKind: ResultFor<typeof HOME>['homeFeed']['results'][number]['kind'] = 'not-a-segment'
void invalidInput
void invalidKind

function Feed() {
  const query = useGolemStaticQuery(HOME, variables)
  if (query.isError) return <p>{query.error.message}</p>
  return <main>{query.data?.homeFeed.results.map((segment) => <p key={segment.index}>{segment.kind}:{segment.index}</p>)}</main>
}

it('keeps gql.tada introspection tied to the generated Go SDL', () => {
  const expected = outputIntrospectionFile(introspectionFromSchema(schema), { fileType: '.d.ts', shouldPreprocess: true })
  expect(readFileSync(new URL('./fixtures/objects.introspection.d.ts', import.meta.url), 'utf8')).toBe(expected)
  expectTypeOf<ResultFor<typeof HOME>>().toEqualTypeOf<ResultOf<typeof HOME>>()
  expectTypeOf<VariablesFor<typeof HOME>>().toEqualTypeOf<VariablesOf<typeof HOME>>()
  expectTypeOf<VariablesFor<typeof CHOICE>>().toEqualTypeOf<VariablesOf<typeof CHOICE>>()
  const target = schema.getType('PlaybackTarget')
  expect(target).toBeInstanceOf(GraphQLInputObjectType)
  expect(() => validateInputValue(multipleChoices.target, target as GraphQLInputObjectType, (error) => { throw error })).toThrow('exactly one field must be specified')
  expectTypeOf<ResultFor<typeof HOME>['homeFeed']['results'][number]['kind']>().toEqualTypeOf<'BASIC_PICK' | 'EDITOR_PICK' | 'RECOMMENDATION'>()
  expectTypeOf<ResultFor<typeof COMPUTED>['users'][number]['metadata']['identity']>().toEqualTypeOf<string>()
  expectTypeOf<NonNullable<ResultFor<typeof COMPUTED>['users'][number]['metadata']['items'][number]>['kind']>().toEqualTypeOf<'BASIC_PICK' | 'EDITOR_PICK' | 'RECOMMENDATION'>()
})

it('renders the typed home-feed fixture with the static hook and no row stream', async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity } } })
  let requests = 0
  let sockets = 0
  // The Go pipeline acceptance separately executes this DTO shape over real
  // generated HTTP. This test isolates React rendering and client rewriting.
  const client = new GolemClient({
    queryClient, url: 'http://dto-fixture/graphql', manifest: golemClientManifest,
    fetch: async (_input, init) => {
      requests++
      const payload = JSON.parse(String(init?.body))
      expect(payload.extensions.golem).toEqual({ protocol: 1, contract: golemClientManifest.contract, manifestVersion: 2 })
      expect(validate(schema, parse(payload.query))).toEqual([])
      expect(payload.variables).toEqual(variables)
      expect(payload.query).not.toContain('_version')
      expect(payload.query).not.toContain('__typename')
      return new Response(JSON.stringify({ data }), { status: 200, headers: { 'content-type': 'application/json' } })
    },
    createSocket: () => { sockets++; throw new Error('a static DTO query opened a row stream') },
  })
  const container = document.createElement('div')
  document.body.appendChild(container)
  const root = createRoot(container)
  try {
    root.render(<QueryClientProvider client={queryClient}><GolemProvider client={client}><Feed /></GolemProvider></QueryClientProvider>)
    await until(() => container.textContent === 'BASIC_PICK:1', 5_000, 'typed home-feed rendering')
    expect(requests).toBe(1)
    expect(sockets).toBe(0)
  } finally {
    root.unmount()
    client.dispose()
    queryClient.clear()
    container.remove()
  }
})
