import { CascadePlan, cascadeDialect, lockedReadStatement } from './cascade';
import { DatamodelDocument, DatamodelReferentialAction } from './datamodel';
import { GolemValidationError } from './errors';
import { GolemEventBus, GolemEventPayload } from './events';
import { createEventPublisher } from './publisher';
import { field } from './testing';
import { fakeBatch } from '../test/support/fake-batch';

function reference(
  name: string,
  type: string,
  from: string,
  relationName: string,
  onDelete: DatamodelReferentialAction,
) {
  return field({
    name,
    type,
    kind: 'object',
    relationName,
    relationFromFields: [from],
    relationToFields: ['id'],
    relationOnDelete: onDelete,
  });
}

function many(name: string, type: string, relationName: string) {
  return field({ name, type, kind: 'object', isList: true, relationName });
}

function datamodel(provider: string): DatamodelDocument {
  return {
    provider,
    enums: [],
    models: [
      {
        name: 'Post',
        dbName: 'posts',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          many('comments', 'Comment', 'PostComments'),
          many('bookmarks', 'Bookmark', 'PostBookmarks'),
          many('pinnedReactions', 'Reaction', 'PinnedReactions'),
          many('citations', 'Citation', 'PostCitations'),
        ],
      },
      {
        name: 'Comment',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'postId', type: 'String', dbName: 'post_id' }),
          reference('post', 'Post', 'postId', 'PostComments', 'Cascade'),
          many('reactions', 'Reaction', 'CommentReactions'),
        ],
      },
      {
        name: 'Reaction',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'commentId', type: 'String' }),
          reference('comment', 'Comment', 'commentId', 'CommentReactions', 'Cascade'),
          field({ name: 'pinnedPostId', type: 'String', isRequired: false }),
          reference('pinnedPost', 'Post', 'pinnedPostId', 'PinnedReactions', 'SetNull'),
        ],
      },
      {
        name: 'Bookmark',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'postId', type: 'String', isRequired: false }),
          reference('post', 'Post', 'postId', 'PostBookmarks', 'SetNull'),
        ],
      },
      {
        name: 'Citation',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'postId', type: 'String' }),
          reference('post', 'Post', 'postId', 'PostCitations', 'Restrict'),
        ],
      },
      {
        name: 'Channel',
        dbName: 'channels',
        fields: [
          field({ name: 'slug', type: 'String', isUnique: true }),
          field({ name: 'title', type: 'String' }),
          many('messages', 'Message', 'ChannelMessages'),
        ],
      },
      {
        name: 'Message',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'channelSlug', type: 'String', dbName: 'channel_slug' }),
          field({
            name: 'channel', type: 'Channel', kind: 'object', relationName: 'ChannelMessages',
            relationFromFields: ['channelSlug'], relationToFields: ['slug'], relationOnDelete: 'Cascade',
          }),
        ],
      },
      {
        name: 'Node',
        fields: [
          field({ name: 'id', type: 'String', isId: true }),
          field({ name: 'parentId', type: 'String', isRequired: false }),
          reference('parent', 'Node', 'parentId', 'NodeTree', 'Cascade'),
          many('children', 'Node', 'NodeTree'),
        ],
      },
    ],
  };
}

function tables() {
  return {
    Post: [{ id: 'p1' }, { id: 'p2' }],
    Comment: [
      { id: 'c1', postId: 'p1' },
      { id: 'c2', postId: 'p1' },
      { id: 'c3', postId: 'p2' },
    ],
    Reaction: [
      { id: 'r1', commentId: 'c1', pinnedPostId: null },
      { id: 'r2', commentId: 'c3', pinnedPostId: 'p1' },
    ],
    Bookmark: [{ id: 'b1', postId: 'p1' }, { id: 'b2', postId: 'p2' }],
    Citation: [{ id: 'x1', postId: 'p2' }],
    Channel: [{ slug: 'general', title: 'General' }, { slug: 'random', title: 'Random' }],
    Message: [
      { id: 'm1', channelSlug: 'general' },
      { id: 'm2', channelSlug: 'general' },
      { id: 'm3', channelSlug: 'random' },
    ],
    Node: [
      { id: 'n1', parentId: 'n3' },
      { id: 'n2', parentId: 'n1' },
      { id: 'n3', parentId: 'n2' },
    ],
  };
}

function bus() {
  const published: Array<{ topic: string; events: GolemEventPayload[] }> = [];
  const eventBus: GolemEventBus = {
    publish: async (topic, event) => { published.push({ topic, events: [event] }); },
    publishMany: async (topic, events) => { published.push({ topic, events: [...events] }); },
    iterate: (async function* () {})() as never,
  };
  return { eventBus, published };
}

function publisherFor(provider: string, models: readonly string[], maxRows?: number) {
  const { eventBus, published } = bus();
  const publisher = createEventPublisher({
    datamodel: datamodel(provider),
    eventBus,
    models: new Set(models),
    ...(maxRows === undefined ? {} : { batch: { maxRows } }),
  });
  return { publisher, published };
}

function batchFor(provider: string, model: string, rows = tables()) {
  return fakeBatch(rows, model, {}, datamodel(provider));
}

const P1_CASCADE = [
  { topic: 'golem.Post', events: [{ type: 'DELETED', model: 'Post', id: 'p1', entity: { id: 'p1' } }] },
  { topic: 'golem.Comment', events: [
    { type: 'DELETED', model: 'Comment', id: 'c1', entity: { id: 'c1', postId: 'p1' } },
    { type: 'DELETED', model: 'Comment', id: 'c2', entity: { id: 'c2', postId: 'p1' } },
  ] },
  { topic: 'golem.Reaction', events: [
    { type: 'DELETED', model: 'Reaction', id: 'r1', entity: { id: 'r1', commentId: 'c1', pinnedPostId: null } },
    { type: 'UPDATED', model: 'Reaction', id: 'r2' },
  ] },
  { topic: 'golem.Bookmark', events: [{ type: 'UPDATED', model: 'Bookmark', id: 'b1' }] },
];

describe.each(['sqlite', 'postgresql'])('cascaded delete events on %s', (provider) => {
  it('publishes a delete or update event for every row the database cascades to', async () => {
    const { publisher, published } = publisherFor(provider, ['Post', 'Comment', 'Reaction', 'Bookmark']);

    await publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batchFor(provider, 'Post').runtime,
    });

    expect(published).toEqual(P1_CASCADE);
  });

  it('enumerates dependents of models nobody subscribes to and publishes only subscribed ones', async () => {
    const { publisher, published } = publisherFor(provider, ['Reaction']);

    await publisher({
      model: 'Post', operation: 'deleteMany', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batchFor(provider, 'Post').runtime,
    });

    expect(published).toEqual([P1_CASCADE[2]]);
  });

  it.each([
    ['a subscribed model', ['Post', 'Comment']],
    ['no subscribed model', []],
  ])('refuses the whole delete when its cascade would touch more rows than the cap, with %s', async (_label, models) => {
    const { publisher, published } = publisherFor(provider, models, 4);
    const batch = batchFor(provider, 'Post');

    const attempt = publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batch.runtime,
    });

    await expect(attempt).rejects.toThrow(GolemValidationError);
    await expect(attempt).rejects.toThrow(
      'Deleting from Post would touch more than the maximum of 4 rows, counting cascaded dependents',
    );
    expect(batch.delegates.get('Post')!.delete).not.toHaveBeenCalled();
    expect(batch.tables.get('Post')).toHaveLength(2);
    expect(published).toEqual([]);
  });

  it('counts a row reached through several foreign keys once, as deleted when any path deletes it', async () => {
    const { publisher, published } = publisherFor(provider, ['Reaction']);
    const rows = tables();
    rows.Reaction = [{ id: 'r9', commentId: 'c1', pinnedPostId: 'p1' }];

    await publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batchFor(provider, 'Post', rows).runtime,
    });

    expect(published).toEqual([{ topic: 'golem.Reaction', events: [
      { type: 'DELETED', model: 'Reaction', id: 'r9', entity: { id: 'r9', commentId: 'c1', pinnedPostId: 'p1' } },
    ] }]);
  });

  it('terminates on a cascading cycle and reports each row once', async () => {
    const { publisher, published } = publisherFor(provider, ['Node']);

    await publisher({
      model: 'Node', operation: 'delete', args: { where: { id: 'n1' } }, query: jest.fn(),
      batch: batchFor(provider, 'Node').runtime,
    });

    expect(published[0].events.map((event) => event.id)).toEqual(['n1', 'n2', 'n3']);
  });

  it('identifies a unique-only root by its unique field and publishes its dependents', async () => {
    const { publisher, published } = publisherFor(provider, ['Message']);
    const batch = batchFor(provider, 'Channel');

    await publisher({
      model: 'Channel', operation: 'delete', args: { where: { slug: 'general' } }, query: jest.fn(),
      batch: batch.runtime,
    });

    expect(published).toEqual([{ topic: 'golem.Message', events: [
      { type: 'DELETED', model: 'Message', id: 'm1', entity: { id: 'm1', channelSlug: 'general' } },
      { type: 'DELETED', model: 'Message', id: 'm2', entity: { id: 'm2', channelSlug: 'general' } },
    ] }]);
    expect(batch.delegates.get('Channel')!.delete).toHaveBeenCalledTimes(1);
  });

  it('counts the dependents of a unique-only root against the cap', async () => {
    const { publisher, published } = publisherFor(provider, ['Message'], 2);
    const batch = batchFor(provider, 'Channel');

    await expect(publisher({
      model: 'Channel', operation: 'deleteMany', args: { where: { slug: 'general' } }, query: jest.fn(),
      batch: batch.runtime,
    })).rejects.toThrow('Deleting from Channel would touch more than the maximum of 2 rows');
    expect(batch.delegates.get('Channel')!.deleteMany).not.toHaveBeenCalled();
    expect(published).toEqual([]);
  });

  it('leaves Restrict dependents to the database', async () => {
    const { publisher } = publisherFor(provider, ['Citation']);
    const batch = batchFor(provider, 'Post');

    await publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p2' } }, query: jest.fn(),
      batch: batch.runtime,
    });

    expect(batch.delegates.get('Citation')!.findMany).not.toHaveBeenCalled();
    expect(batch.statements.some((sql) => sql.includes('"Citation"'))).toBe(false);
  });

  it('publishes events for rows a nested delete removes and for their cascades', async () => {
    const { publisher, published } = publisherFor(provider, ['Post', 'Comment', 'Reaction']);
    const batch = batchFor(provider, 'Post');

    await publisher({
      model: 'Post',
      operation: 'update',
      args: { where: { id: 'p1' }, data: { comments: { delete: [{ id: 'c1' }] } } },
      query: jest.fn(),
      batch: batch.runtime,
    });

    expect(batch.delegates.get('Post')!.update).toHaveBeenCalledTimes(1);
    expect(published).toEqual([
      { topic: 'golem.Post', events: [{ type: 'UPDATED', model: 'Post', id: 'p1' }] },
      { topic: 'golem.Comment', events: [
        { type: 'DELETED', model: 'Comment', id: 'c1', entity: { id: 'c1', postId: 'p1' } },
      ] },
      { topic: 'golem.Reaction', events: [
        { type: 'DELETED', model: 'Reaction', id: 'r1', entity: { id: 'r1', commentId: 'c1', pinnedPostId: null } },
      ] },
    ]);
  });

  it('scopes a nested deleteMany to its parent and follows nested updates to any depth', async () => {
    const { publisher, published } = publisherFor(provider, ['Comment', 'Reaction']);

    await publisher({
      model: 'Post',
      operation: 'update',
      args: {
        where: { id: 'p2' },
        data: {
          comments: {
            deleteMany: {},
            update: [{ where: { id: 'c3' }, data: { reactions: { deleteMany: { id: 'r2' } } } }],
          },
        },
      },
      query: jest.fn(),
      batch: batchFor(provider, 'Post').runtime,
    });

    expect(published).toEqual([
      { topic: 'golem.Comment', events: [
        { type: 'DELETED', model: 'Comment', id: 'c3', entity: { id: 'c3', postId: 'p2' } },
      ] },
      { topic: 'golem.Reaction', events: [
        { type: 'DELETED', model: 'Reaction', id: 'r2', entity: { id: 'r2', commentId: 'c3', pinnedPostId: 'p1' } },
      ] },
    ]);
  });

  it('publishes nested deletes on the update branch of an upsert', async () => {
    const { publisher, published } = publisherFor(provider, ['Comment']);

    await publisher({
      model: 'Post',
      operation: 'upsert',
      args: { where: { id: 'p1' }, create: { id: 'p1' }, update: { comments: { deleteMany: { id: 'c2' } } } },
      query: jest.fn(),
      batch: batchFor(provider, 'Post').runtime,
    });

    expect(published).toEqual([{ topic: 'golem.Comment', events: [
      { type: 'DELETED', model: 'Comment', id: 'c2', entity: { id: 'c2', postId: 'p1' } },
    ] }]);
  });

  it('refuses a nested delete whose rows and cascades exceed the cap', async () => {
    const { publisher, published } = publisherFor(provider, ['Comment'], 2);
    const batch = batchFor(provider, 'Post');

    await expect(publisher({
      model: 'Post',
      operation: 'update',
      args: { where: { id: 'p1' }, data: { comments: { deleteMany: {} } } },
      query: jest.fn(),
      batch: batch.runtime,
    })).rejects.toThrow('Deleting from Post would touch more than the maximum of 2 rows');
    expect(batch.delegates.get('Post')!.update).not.toHaveBeenCalled();
    expect(published).toEqual([]);
  });

  it('leaves an update without nested deletes on the ordinary path', async () => {
    const { publisher, published } = publisherFor(provider, ['Post']);
    const query = jest.fn().mockResolvedValue({ id: 'p1' });
    const batch = batchFor(provider, 'Post');

    await publisher({
      model: 'Post',
      operation: 'update',
      args: { where: { id: 'p1' }, data: { comments: { create: { id: 'c9' } } } },
      query,
      batch: batch.runtime,
    });

    expect(query).toHaveBeenCalledTimes(1);
    expect(batch.delegates.get('Post')!.findUnique).not.toHaveBeenCalled();
    expect(published).toEqual([{ topic: 'golem.Post', events: [{ type: 'UPDATED', model: 'Post', id: 'p1' }] }]);
  });
});

describe('one locked read per delete level', () => {
  it('reads the deleted row and each dependent level once on SQLite', async () => {
    const { publisher } = publisherFor('sqlite', ['Post']);
    const batch = batchFor('sqlite', 'Post');

    await publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batch.runtime,
    });

    const reads = (model: string) =>
      batch.delegates.get(model)!.findMany.mock.calls.length + batch.delegates.get(model)!.findUnique.mock.calls.length;
    expect([reads('Post'), reads('Comment'), reads('Bookmark'), reads('Reaction')]).toEqual([1, 1, 1, 2]);
    expect(batch.statements).toEqual([]);
  });

  it('locks the deleted rows and each dependent level in one statement each on PostgreSQL', async () => {
    const { publisher } = publisherFor('postgresql', ['Post']);
    const batch = batchFor('postgresql', 'Post');

    await publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batch.runtime,
    });

    expect(batch.statements).toEqual([
      'SELECT "id" AS "id" FROM "posts" WHERE ("id") IN (($1)) ORDER BY "id" LIMIT 1 FOR UPDATE',
      'SELECT "id" AS "id", "post_id" AS "postId" FROM "Comment" WHERE ("post_id") IN (($1)) ORDER BY "id" LIMIT 1001 FOR UPDATE',
      'SELECT "id" AS "id", "commentId" AS "commentId", "pinnedPostId" AS "pinnedPostId" FROM "Reaction" WHERE ("pinnedPostId") IN (($1)) ORDER BY "id" LIMIT 1001 FOR UPDATE',
      'SELECT "id" AS "id", "postId" AS "postId" FROM "Bookmark" WHERE ("postId") IN (($1)) ORDER BY "id" LIMIT 1001 FOR UPDATE',
      'SELECT "id" AS "id", "commentId" AS "commentId", "pinnedPostId" AS "pinnedPostId" FROM "Reaction" WHERE ("commentId") IN (($1), ($2)) ORDER BY "id" LIMIT 1001 FOR UPDATE',
    ]);
    expect(batch.delegates.get('Comment')!.findMany).not.toHaveBeenCalled();
    expect(batch.delegates.get('Post')!.findMany).not.toHaveBeenCalled();
    expect(batch.delegates.get('Post')!.findUnique).toHaveBeenCalledTimes(1);
  });
});

describe('lockedReadStatement', () => {
  it('binds every tuple and quotes identifiers', () => {
    const plan = new CascadePlan([{
      name: 'Odd',
      dbName: 'we"ird',
      fields: [
        field({ name: 'a', type: 'String', isId: true }),
        field({ name: 'b', type: 'Int', dbName: 'b"col' }),
      ],
    }]);
    expect(lockedReadStatement(plan, 'Odd', ['a', 'b'], [['1', 2], ['3', 4]], 7)).toEqual({
      sql: 'SELECT "a" AS "a", "b""col" AS "b" FROM "we""ird" WHERE ("a", "b""col") IN (($1, $2), ($3, $4)) ORDER BY "a" LIMIT 7 FOR UPDATE',
      values: ['1', 2, '3', 4],
    });
  });

  it('refuses a provider it cannot lock rows on', async () => {
    const dialect = cascadeDialect('mysql', new CascadePlan([]));
    expect(() => dialect.lockedRead({} as never, 'X', [], [], 1)).toThrow(
      'Deletes cannot lock the rows they touch on provider mysql',
    );
  });
});
