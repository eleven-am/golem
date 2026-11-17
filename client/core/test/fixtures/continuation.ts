import type { ClientManifest } from '../../src/manifest.js'

// Isolated transport fixture. Go's page acceptance tests own backend execution.
export const continuationSDL = `
  type Query { homeFeed(seed: Int!, take: Int = 25, after: String): HomeFeedPage! }
  type HomeFeedPage { items: [HomeSegment!]!, next: String }
  type HomeSegment { kind: String!, index: Int! }
`

export const continuationManifest = {
  formatVersion: 3,
  contract: 'continuation-fixture',
  models: {},
  roots: {
    homeFeed: {
      kind: 'opaque', shape: 'object', nullable: false, models: [],
      result: { kind: 'object', name: 'HomeFeedPage', nullable: false },
      pagination: { kind: 'continuation', takeArgument: 'take', afterArgument: 'after', itemsField: 'items', nextField: 'next' },
    },
  },
  types: {
    HomeFeedPage: {
      kind: 'object', fields: {
        items: { kind: 'list', nullable: false, maxItems: 100, element: { kind: 'object', name: 'HomeSegment', nullable: false } },
        next: { kind: 'scalar', name: 'String', nullable: true },
      },
    },
    HomeSegment: {
      kind: 'object', fields: {
        kind: { kind: 'scalar', name: 'String', nullable: false },
        index: { kind: 'scalar', name: 'Int', nullable: false },
      },
    },
  },
} as const satisfies ClientManifest
