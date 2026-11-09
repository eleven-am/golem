import { golemClientManifest as social } from '../../../../go/examples/social/social/zz_golem_client.gen.js'
import { golemClientManifest as teams } from '../../../../go/runtime/testdata/p10operations/zz_golem_client.gen.js'
import type { ClientManifest } from '../../src/index.js'

export const socialManifest: ClientManifest = social

export const teamsManifest: ClientManifest = teams

export const socialWithRenamedPostUniqueInput: ClientManifest = {
  ...social,
  models: {
    ...social.models,
    Post: { ...social.models.Post, uniqueInput: 'PostKey' },
  },
}

export const socialWithoutPostUniqueInput: ClientManifest = {
  ...social,
  models: {
    ...social.models,
    Post: { ...social.models.Post, uniqueInput: null },
  },
}

export const socialWithEventfulUsers: ClientManifest = {
  ...social,
  models: {
    ...social.models,
    User: { ...social.models.User, events: 'userEvents' },
  },
}

export const socialHiddenCommentInverse: ClientManifest = {
  ...social,
  models: {
    ...social.models,
    Post: {
      ...social.models.Post,
      relations: {
        ...social.models.Post.relations,
        comments: { target: 'Comment', list: true, nullable: false, inverse: null },
      },
    },
  },
}

export const socialStrictCommentPost: ClientManifest = {
  ...social,
  models: {
    ...social.models,
    Comment: {
      ...social.models.Comment,
      relations: { ...social.models.Comment.relations, post: { ...social.models.Comment.relations.post, nullable: false } },
    },
  },
}

export const socialPostNotNullable: ClientManifest = {
  ...social,
  roots: { ...social.roots, post: { ...social.roots.post, nullable: false } },
}

export const socialWithRoot = (name: string, root: ClientManifest['roots'][string]): ClientManifest => ({ ...social, roots: { ...social.roots, [name]: root } })
