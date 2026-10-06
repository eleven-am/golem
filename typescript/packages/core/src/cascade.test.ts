import { upsertGuardModel } from '../test/support/upsert-guard-model';
import { CascadePlan, LOCK_STATEMENT_ROWS, lockStatement, rowLocker, transactionRowLocks } from './cascade';
import { DatamodelDocument, DatamodelReferentialAction } from './datamodel';
import { GolemConflictError, GolemValidationError } from './errors';
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
    upsertGuard: upsertGuardModel,
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

    expect(query).not.toHaveBeenCalled();
    expect(batch.delegates.get('Post')!.update).toHaveBeenCalledTimes(1);
    expect(batch.statements).toEqual(provider === 'postgresql' ? ['SELECT 1 FROM "posts" WHERE ("id") = ($1) FOR UPDATE'] : []);
    expect(published).toEqual([{ topic: 'golem.Post', events: [{ type: 'UPDATED', model: 'Post', id: 'p1' }] }]);
  });
});

describe('one lock order for every row a delete touches', () => {
  it('enumerates the closure, then re-reads it under the locks, on SQLite', async () => {
    const { publisher } = publisherFor('sqlite', ['Post']);
    const batch = batchFor('sqlite', 'Post');

    await publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batch.runtime,
    });

    const reads = (model: string) =>
      batch.delegates.get(model)!.findMany.mock.calls.length + batch.delegates.get(model)!.findUnique.mock.calls.length;
    expect([reads('Post'), reads('Comment'), reads('Bookmark'), reads('Reaction')]).toEqual([2, 2, 2, 4]);
    expect(batch.statements).toEqual([]);
  });

  it('locks every touched row in model then identity order on PostgreSQL', async () => {
    const { publisher } = publisherFor('postgresql', ['Post']);
    const batch = batchFor('postgresql', 'Post');

    await publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batch.runtime,
    });

    expect(batch.statements).toEqual([
      'SELECT 1 FROM "Bookmark" WHERE ("id") = ($1) FOR UPDATE',
      'SELECT 1 FROM "Comment" WHERE ("id") IN (($1), ($2)) ORDER BY CASE WHEN ("id") = ($1) THEN 0 WHEN ("id") = ($2) THEN 1 END FOR UPDATE',
      'SELECT 1 FROM "posts" WHERE ("id") = ($1) FOR UPDATE',
      'SELECT 1 FROM "Reaction" WHERE ("id") IN (($1), ($2)) ORDER BY CASE WHEN ("id") = ($1) THEN 0 WHEN ("id") = ($2) THEN 1 END FOR UPDATE',
    ]);
    expect(batch.values).toEqual([['b1'], ['c1', 'c2'], ['p1'], ['r1', 'r2']]);
  });

  it.each(['sqlite', 'postgresql'])('refuses a delete whose closure changes between enumeration and lock on %s', async (provider) => {
    const { publisher, published } = publisherFor(provider, ['Post', 'Comment']);
    const batch = batchFor(provider, 'Post');
    const comments = batch.delegates.get('Comment')!;
    const enumerate = comments.findMany.getMockImplementation()!;
    comments.findMany.mockImplementationOnce(async (args: unknown) => {
      const found = await enumerate(args);
      batch.tables.get('Comment')!.push({ id: 'c9', postId: 'p1' });
      return found;
    });

    await expect(publisher({
      model: 'Post', operation: 'delete', args: { where: { id: 'p1' } }, query: jest.fn(),
      batch: batch.runtime,
    })).rejects.toThrow(new GolemConflictError('The rows deleting from Post touches changed while they were being locked'));
    expect(batch.delegates.get('Post')!.delete).not.toHaveBeenCalled();
    expect(published).toEqual([]);
  });
});

describe('lockStatement', () => {
  it('binds the row identity and quotes identifiers', () => {
    const plan = new CascadePlan([{
      name: 'Odd',
      dbName: 'we"ird',
      fields: [
        field({ name: 'a', type: 'String' }),
        field({ name: 'b', type: 'Int', dbName: 'b"col' }),
      ],
      primaryKey: { fields: ['a', 'b'] },
    }]);
    expect(lockStatement(plan, 'Odd', [{ a: '1', b: 2 }], 'SHARE', true)).toEqual({
      sql: 'SELECT 1 FROM "we""ird" WHERE ("a", "b""col") = ($1, $2) FOR SHARE',
      values: ['1', 2],
    });
  });

  it('locks several rows of one table in one statement, in the order it names them', () => {
    const plan = new CascadePlan([{
      name: 'Odd',
      fields: [field({ name: 'a', type: 'String' }), field({ name: 'b', type: 'Int' })],
      primaryKey: { fields: ['a', 'b'] },
    }]);
    expect(lockStatement(plan, 'Odd', [{ a: 'x', b: 2 }, { a: 'x', b: 10 }, { a: 'y', b: 1 }], 'UPDATE', false)).toEqual({
      sql: 'SELECT 1 FROM "Odd" WHERE ("a", "b") IN (($1, $2), ($3, $4), ($5, $6)) '
        + 'ORDER BY CASE WHEN ("a", "b") = ($1, $2) THEN 0 WHEN ("a", "b") = ($3, $4) THEN 1 WHEN ("a", "b") = ($5, $6) THEN 2 END '
        + 'FOR UPDATE NOWAIT',
      values: ['x', 2, 'x', 10, 'y', 1],
    });
  });

  it('qualifies the table with the schema its model declares', () => {
    const plan = new CascadePlan([{
      name: 'Odd',
      dbName: 'items',
      schema: 'tenant"one',
      fields: [field({ name: 'a', type: 'String', isId: true })],
    }]);
    expect(lockStatement(plan, 'Odd', [{ a: '1' }], 'UPDATE', true).sql).toBe(
      'SELECT 1 FROM "tenant""one"."items" WHERE ("a") = ($1) FOR UPDATE',
    );
  });

  it('takes a lock without waiting when told not to', () => {
    const plan = new CascadePlan([{ name: 'Odd', fields: [field({ name: 'a', type: 'String', isId: true })] }]);
    expect(lockStatement(plan, 'Odd', [{ a: '1' }], 'SHARE', false).sql).toBe(
      'SELECT 1 FROM "Odd" WHERE ("a") = ($1) FOR SHARE NOWAIT',
    );
  });
});

function recording(taken: string[]) {
  return async (requests: readonly { model: string; row: Record<string, unknown> }[], wait: boolean) => {
    taken.push(`${requests.map((request) => `${request.model}:${String(request.row.id)}`).join(',')}:${wait ? 'wait' : 'nowait'}`);
    return true;
  };
}

describe('transaction row locks', () => {
  const plan = new CascadePlan([
    { name: 'A', fields: [field({ name: 'id', type: 'String', isId: true })] },
    { name: 'B', fields: [field({ name: 'id', type: 'String', isId: true })] },
  ]);

  it('is one instance per transaction', () => {
    const transaction = {};
    expect(transactionRowLocks(transaction, plan)).toBe(transactionRowLocks(transaction, plan));
    expect(transactionRowLocks({}, plan)).not.toBe(transactionRowLocks(transaction, plan));
  });

  it('waits for rows that sort after everything held and never for one that sorts before', async () => {
    const taken: string[] = [];
    const locks = transactionRowLocks({}, plan);
    await locks.acquire([
      { model: 'B', row: { id: '1' }, mode: 'UPDATE' },
      { model: 'A', row: { id: '2' }, mode: 'SHARE' },
    ], recording(taken));
    await locks.acquire([
      { model: 'A', row: { id: '1' }, mode: 'UPDATE' },
      { model: 'B', row: { id: '2' }, mode: 'UPDATE' },
      { model: 'B', row: { id: '1' }, mode: 'SHARE' },
    ], recording(taken));
    expect(taken).toEqual(['A:2:wait', 'B:1:wait', 'A:1:nowait', 'B:2:wait']);
  });

  it('takes neighbouring rows of one table and lock mode in one call, in order, split where waiting stops being safe', async () => {
    const taken: string[] = [];
    const locks = transactionRowLocks({}, plan);
    await locks.acquire([{ model: 'A', row: { id: '5' }, mode: 'UPDATE' }], recording(taken));
    await locks.acquire([
      { model: 'B', row: { id: '3' }, mode: 'SHARE' },
      { model: 'A', row: { id: '7' }, mode: 'UPDATE' },
      { model: 'A', row: { id: '2' }, mode: 'UPDATE' },
      { model: 'A', row: { id: '1' }, mode: 'UPDATE' },
      { model: 'B', row: { id: '1' }, mode: 'SHARE' },
      { model: 'A', row: { id: '6' }, mode: 'UPDATE' },
      { model: 'B', row: { id: '2' }, mode: 'UPDATE' },
      { model: 'A', row: { id: '8' }, mode: 'SHARE' },
    ], recording(taken));
    expect(taken).toEqual(['A:5:wait', 'A:1,A:2:nowait', 'A:6,A:7:wait', 'A:8:wait', 'B:1:wait', 'B:2:wait', 'B:3:wait']);
  });

  it('reports a batch present only when every row it names still exists', async () => {
    const lock = rowLocker('postgresql', plan, async (_sql, values) => values.slice(1));
    await expect(lock([{ model: 'A', row: { id: '1' }, mode: 'UPDATE' }], true)).resolves.toBe(false);
    await expect(lock([
      { model: 'A', row: { id: '1' }, mode: 'UPDATE' },
      { model: 'A', row: { id: '2' }, mode: 'UPDATE' },
    ], true)).resolves.toBe(false);
    const all = rowLocker('postgresql', plan, async (_sql, values) => values);
    await expect(all([
      { model: 'A', row: { id: '1' }, mode: 'UPDATE' },
      { model: 'A', row: { id: '2' }, mode: 'UPDATE' },
    ], true)).resolves.toBe(true);
  });

  it(`splits a run longer than ${LOCK_STATEMENT_ROWS} rows into statements that keep its order`, async () => {
    const statements: unknown[][] = [];
    const lock = rowLocker('postgresql', plan, async (_sql, values) => {
      statements.push(values);
      return values;
    });
    const rows = Array.from({ length: LOCK_STATEMENT_ROWS + 1 }, (_value, index) => ({
      model: 'A', row: { id: String(index).padStart(4, '0') }, mode: 'UPDATE' as const,
    }));
    await expect(lock(rows, true)).resolves.toBe(true);
    expect(statements.map((values) => values.length)).toEqual([LOCK_STATEMENT_ROWS, 1]);
    expect(statements.flat()).toEqual(rows.map(({ row }) => row.id));
  });

  it('reports a row another write holds as a conflict when it cannot wait for it', async () => {
    const lock = rowLocker('postgresql', plan, async () => {
      throw Object.assign(new Error('could not obtain lock on row in relation "A"'), { meta: { code: '55P03' } });
    });
    await expect(lock([{ model: 'A', row: { id: '1' }, mode: 'UPDATE' }], false))
      .rejects.toThrow(new GolemConflictError('A row of A this write needs is held by a concurrent write'));
  });

  it('issues no lock statement on SQLite, where the database serialises writers', async () => {
    const run = jest.fn();
    await rowLocker('sqlite', new CascadePlan([]), run)([{ model: 'X', row: {}, mode: 'UPDATE' }], true);
    expect(run).not.toHaveBeenCalled();
  });
});

describe('dependents whose identity a delete would rewrite', () => {
  function layout(onDelete: DatamodelReferentialAction, identityHoldsForeignKey: boolean): DatamodelDocument {
    return {
      provider: 'sqlite',
      enums: [],
      models: [
        { name: 'Post', fields: [field({ name: 'id', type: 'String', isId: true })] },
        {
          name: 'Label',
          fields: [
            field({ name: 'postId', type: 'String', hasDefaultValue: true }),
            field({ name: 'name', type: 'String' }),
            field({
              name: 'post', type: 'Post', kind: 'object', relationName: 'PostLabels',
              relationFromFields: ['postId'], relationToFields: ['id'], relationOnDelete: onDelete,
            }),
          ],
          primaryKey: { fields: identityHoldsForeignKey ? ['postId', 'name'] : ['name'] },
        },
      ],
    };
  }

  it.each(['SetDefault', 'SetNull'] as const)(
    'refuses to publish events for a model whose identity %s would rewrite',
    (onDelete) => {
      expect(() => createEventPublisher({
        datamodel: layout(onDelete, true),
        eventBus: bus().eventBus,
        models: new Set(['Label']),
      })).toThrow(
        `Model Label cannot publish events: deleting a Post would ${onDelete} postId, which identify its rows`,
      );
    },
  );

  it('accepts the same layout when nobody subscribes to the dependent', () => {
    expect(() => createEventPublisher({
      datamodel: layout('SetDefault', true),
      eventBus: bus().eventBus,
      models: new Set(['Post']),
    })).not.toThrow();
  });

  it('accepts a SetDefault on a foreign key outside the identity', () => {
    expect(() => createEventPublisher({
      datamodel: layout('SetDefault', false),
      eventBus: bus().eventBus,
      models: new Set(['Label']),
    })).not.toThrow();
  });
});
