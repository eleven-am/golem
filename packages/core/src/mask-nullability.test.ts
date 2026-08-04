import { graphql, parse, printSchema, subscribe } from 'graphql';
import { AuthorizationProvider, FieldClassification } from './authorization';
import { DatamodelDocument } from './datamodel';
import { GolemEventBus, GolemEventPayload } from './events';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument<{ User: 'id' | 'secret' | 'posts'; Post: 'id' | 'secret' | 'author' }> = {
  models: [
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'secret', type: 'String' }),
        field({ name: 'posts', type: 'Post', kind: 'object', isList: true, relationName: 'PostAuthor' }),
      ],
    },
    {
      name: 'Post',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'secret', type: 'String' }),
        field({
          name: 'author',
          type: 'User',
          kind: 'object',
          relationName: 'PostAuthor',
          relationFromFields: ['authorId'],
          relationToFields: ['id'],
        }),
        field({ name: 'authorId', type: 'String', isReadOnly: true }),
      ],
    },
  ],
  enums: [],
};

const authorization: AuthorizationProvider = {
  authorize: jest.fn(async () => undefined),
  constrain: jest.fn(async () => ({})),
  constrainField: jest.fn(async () => undefined),
  check: jest.fn(async () => true),
  checkField: jest.fn(async (_action, _model, entity, fieldName) =>
    fieldName !== 'secret' || (entity as { id?: string }).id !== 'hidden'
  ),
  classifyFields: jest.fn(async (_action, _model, fields) =>
    Object.fromEntries(fields.map((name) => [
      name,
      name === 'secret'
        ? {
            access: 'conditional',
            requires: ['id'],
            dependencies: { id: true },
          } satisfies FieldClassification
        : { access: 'always' } satisfies FieldClassification,
    ]))),
};

function delegate() {
  return {
    findMany: jest.fn().mockResolvedValue([]),
    findUnique: jest.fn().mockResolvedValue(null),
    findFirst: jest.fn().mockResolvedValue(null),
    create: jest.fn().mockResolvedValue({ id: 'allowed', secret: 'visible' }),
    update: jest.fn().mockResolvedValue({ id: 'allowed', secret: 'visible' }),
    updateMany: jest.fn().mockResolvedValue({ count: 0 }),
    delete: jest.fn().mockResolvedValue({ id: 'allowed', secret: 'visible' }),
    deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
  };
}

function client() {
  return { user: delegate(), post: delegate() };
}

class EventBus implements GolemEventBus {
  private values: GolemEventPayload[] = [];
  private wake: (() => void) | undefined;

  async publish(): Promise<void> {}

  push(value: GolemEventPayload): void {
    this.values.push(value);
    this.wake?.();
    this.wake = undefined;
  }

  async *iterate(): AsyncIterableIterator<GolemEventPayload> {
    while (true) {
      while (this.values.length > 0) yield this.values.shift()!;
      await new Promise<void>((resolve) => {
        this.wake = resolve;
      });
    }
  }
}

describe('authorization-aware GraphQL output nullability', () => {
  it('makes all visible scalar outputs nullable while leaving inputs and list structure intact', () => {
    const schema = buildGolemSchema({ datamodel, client: client(), authorization });
    const sdl = printSchema(schema);

    expect(sdl).toContain('type User {\n  id: String\n  secret: String\n  posts: [Post!]!');
    expect(sdl).toContain('input UserCreateInput {\n  id: String!\n  secret: String!');
  });

  it('keeps Prisma output requiredness when field checks are explicitly disabled', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: client(),
      authorization,
      defaults: { checkReadFields: false },
    });
    const sdl = printSchema(schema);

    expect(sdl).toContain('type User {\n  id: String!\n  secret: String!\n  posts: [Post!]!');
  });

  it('returns masked required values without bubbling top-level, list, nested or aliased data', async () => {
    const db = client();
    db.user.findMany.mockResolvedValue([
      { id: 'allowed', secret: 'visible', posts: [{ id: 'hidden', secret: 'nested-hidden' }] },
      { id: 'hidden', secret: 'root-hidden', posts: [] },
    ]);
    db.user.findUnique.mockResolvedValue({ id: 'hidden', secret: 'top-hidden' });
    db.user.findFirst.mockResolvedValue({ id: 'hidden', secret: 'top-hidden' });
    const schema = buildGolemSchema({ datamodel, client: db, authorization });

    const result = await graphql({
      schema,
      contextValue: {},
      source: `{
        user(where: { id: "hidden" }) { secret }
        users { masked: secret posts { secret } }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({
      user: { secret: null },
      users: [
        { masked: 'visible', posts: [{ secret: null }] },
        { masked: null, posts: [] },
      ],
    });
  });

  it('keeps a masked required subscription entity field local to that field', async () => {
    const db = client();
    db.user.findFirst.mockResolvedValue({ id: 'hidden', secret: 'event-hidden' });
    const eventBus = new EventBus();
    const schema = buildGolemSchema({
      datamodel,
      client: db,
      authorization,
      eventBus,
      models: { User: { subscriptions: true } },
    });
    const iterator = (await subscribe({
      schema,
      contextValue: {},
      document: parse('subscription { userEvents { entity { secret } } }'),
    })) as AsyncIterableIterator<any>;

    const next = iterator.next();
    eventBus.push({ type: 'UPDATED', model: 'User', id: 'hidden' });
    const result = (await next).value;

    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({ userEvents: { entity: { secret: null } } });
    await iterator.return?.();
  });
});
