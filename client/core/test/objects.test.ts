import { describe, expect, it } from 'vitest'
import { DocumentAnalyzer, GolemConfigurationError, GolemDocumentError, golemBlock, type ClientManifest } from '../src/index.js'

import { golemClientManifest as manifest } from './fixtures/objects.manifest.js'
import { readFileSync } from 'node:fs'
import { buildSchema, parse, validate } from 'graphql'

describe('registered transport objects', () => {
  it('publishes one-of input metadata from the Go declaration', () => {
    expect(manifest.types.PlaybackTarget.oneOf).toBe(true)
    expect(Object.keys(manifest.types.PlaybackTarget.fields).sort()).toEqual(['mediaId', 'playQueueItemId', 'playlistId', 'playlistVideoId', 'videoId'])
  })
  it('traverses computed DTOs with their registered shape and keeps them static', () => {
    const analyzer = new DocumentAnalyzer(manifest)
    const analysis = analyzer.analyze('{ users { data: metadata { identity: id version: _version items { kind } } } }')
    const schema = buildSchema(readFileSync(new URL('./fixtures/objects.schema.graphql', import.meta.url), 'utf8'))
    expect(validate(schema, parse(analysis.query))).toEqual([])
    expect(analysis.notLive.map((error) => [error.reason, error.subject])).toContainEqual(['non-row-root', 'User.metadata'])
    expect(analysis.streams).toEqual([])
    expect(analysis.query).toContain('identity: id')
    expect(analysis.query).toContain('version: _version')
    expect(() => analyzer.analyze('{ users { metadata { missing } } }')).toThrow(GolemDocumentError)
  })

  it('preserves DTO metadata names, aliases, fragments and directives without injecting row metadata', () => {
    const analysis = new DocumentAnalyzer(manifest).analyze(`query Home($show: Boolean!) {
      feed: homeFeed(input: {}) { results { ...Segment id: kind index @include(if: $show) } }
      transportMetadata { identity: id version: _version }
    } fragment Segment on HomeSegment { label: kind }`)
    expect(analysis.query).toContain('id: kind')
    expect(analysis.query).toContain('version: _version')
    expect(analysis.query).toContain('@include(if: $show)')
    expect(analysis.query).not.toContain('__typename')
    expect(analysis.query).not.toMatch(/^\s+id\s*$/m)
    expect(analysis.query).not.toMatch(/^\s+_version\s*$/m)
    expect(analysis.live).toBe(false)
    expect(analysis.streams).toEqual([])
  })

  it('rejects unknown object fields and cyclic fragments before a request is sent', () => {
    const analyzer = new DocumentAnalyzer(manifest)
    expect(() => analyzer.analyze('{ homeFeed(input: {}) { results { missing } } }')).toThrow(GolemDocumentError)
    expect(() => analyzer.analyze('{ homeFeed(input: {}) { results { constructor } } }')).toThrow(GolemDocumentError)
    expect(() => analyzer.analyze('{ homeFeed(input: {}) { ...A } } fragment A on HomeFeedPage { ...A }')).toThrow(GolemDocumentError)
  })

  it('declares format 2 in the shared HTTP/socket handshake', () => {
    expect(golemBlock(manifest)).toEqual({ protocol: 1, contract: manifest.contract, manifestVersion: 2 })
    expect(golemBlock({ contract: 'existing-contract' })).toEqual({ protocol: 1, contract: 'existing-contract' })
  })

  it('fails on unsupported or unversioned object manifests', () => {
    expect(() => new DocumentAnalyzer({ ...manifest, formatVersion: 4 } as unknown as ClientManifest)).toThrow(GolemConfigurationError)
    expect(() => new DocumentAnalyzer({ ...manifest, formatVersion: undefined } as unknown as ClientManifest)).toThrow(GolemConfigurationError)
    expect(() => golemBlock({ ...manifest, formatVersion: 4 })).toThrow(GolemConfigurationError)
    expect(() => golemBlock({ ...manifest, formatVersion: null } as unknown as ClientManifest)).toThrow(GolemConfigurationError)
  })
})
