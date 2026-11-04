import { golemClientManifest as social } from '../../../../go/examples/social/social/zz_golem_client.gen.js'
import { golemClientManifest as teams } from '../../../../go/runtime/testdata/p10operations/zz_golem_client.gen.js'
import type { ClientManifest } from '../../src/index.js'

export const socialManifest: ClientManifest = social

export const teamsManifest: ClientManifest = teams

export const socialWithPostTagEvents: ClientManifest = {
  ...social,
  models: {
    ...social.models,
    PostTag: { ...social.models.PostTag, events: 'postTagEvents' },
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
        comments: { target: 'Comment', list: true, inverse: null },
      },
    },
  },
}
