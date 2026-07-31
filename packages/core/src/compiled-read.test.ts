import {
  CompiledReadFallback,
  CompiledReadInput,
  CompiledReadStatement,
  planCompiledRead,
} from './compiled-read';
import { DatamodelModel } from './datamodel';
import { buildModelMetadata } from './model-meta';
import { PreparedReadTree } from './readtree';
import { field } from './testing';

const models: readonly DatamodelModel[] = [
  {
    name: 'Post',
    dbName: 'posts',
    indexes: [{ kind: 'normal', fields: ['authorId'] }],
    fields: [
      field({ name: 'id', dbName: 'post_id', type: 'Int', isId: true }),
      field({ name: 'title', dbName: 'title', type: 'String' }),
      field({ name: 'authorId', dbName: 'author_id', type: 'Int' }),
      field({ name: 'published', dbName: 'published', type: 'Boolean' }),
      field({ name: 'tags', dbName: 'tags', type: 'String', isList: true }),
      field({
        name: 'author',
        kind: 'object',
        type: 'User',
        relationName: 'PostToUser',
        relationFromFields: ['authorId'],
        relationToFields: ['id'],
      }),
    ],
  },
  {
    name: 'User',
    dbName: 'users',
    fields: [
      field({ name: 'id', dbName: 'user_id', type: 'Int', isId: true }),
      field({ name: 'name', dbName: 'name', type: 'String' }),
      field({
        name: 'assets',
        kind: 'object',
        type: 'Asset',
        isList: true,
        isRequired: false,
        relationName: 'AssetToUser',
      }),
      field({
        name: 'posts',
        kind: 'object',
        type: 'Post',
        isList: true,
        isRequired: false,
        relationName: 'PostToUser',
      }),
      field({
        name: 'groups',
        kind: 'object',
        type: 'Group',
        isList: true,
        isRequired: false,
        relationName: 'GroupToUser',
      }),
    ],
  },
  {
    name: 'Unmapped',
    fields: [field({ name: 'id', dbName: 'id', type: 'Int', isId: true })],
  },
  {
    name: 'Shadowed',
    dbName: 'shadowed',
    fields: [
      field({ name: 'id', dbName: 'id', type: 'Int', isId: true }),
      field({ name: 'shadow', type: 'String' }),
    ],
  },
  {
    name: 'Asset',
    dbName: 'assets',
    indexes: [{ kind: 'normal', fields: ['ownerId'] }],
    fields: [
      field({ name: 'id', dbName: 'asset_id', type: 'Int', isId: true }),
      field({ name: 'payload', dbName: 'payload', type: 'Bytes', isRequired: false }),
      field({ name: 'meta', dbName: 'meta', type: 'Json', isRequired: false }),
      field({ name: 'size', dbName: 'size', type: 'BigInt' }),
      field({ name: 'cost', dbName: 'cost', type: 'Decimal', isRequired: false }),
      field({ name: 'createdAt', dbName: 'created_at', type: 'DateTime' }),
      field({ name: 'ownerId', dbName: 'owner_id', type: 'Int' }),
      field({
        name: 'owner',
        kind: 'object',
        type: 'User',
        relationName: 'AssetToUser',
        relationFromFields: ['ownerId'],
        relationToFields: ['id'],
      }),
    ],
  },
  {
    name: 'Group',
    dbName: 'groups',
    fields: [
      field({ name: 'id', dbName: 'group_id', type: 'Int', isId: true }),
      field({ name: 'label', dbName: 'label', type: 'String' }),
      field({
        name: 'members',
        kind: 'object',
        type: 'User',
        isList: true,
        isRequired: false,
        relationName: 'GroupToUser',
      }),
    ],
  },
];

const metadata = buildModelMetadata(models);

function tree(overrides: Partial<PreparedReadTree> = {}): PreparedReadTree {
  return { toOneChecks: [], maskChecks: [], injected: [], ...overrides };
}

function plan(
  overrides: Partial<CompiledReadInput> = {},
): Promise<CompiledReadStatement | CompiledReadFallback> {
  return planCompiledRead({
    model: models[0],
    models,
    metadata,
    provider: 'sqlite',
    prepared: tree({ select: { id: true, title: true } }),
    ...overrides,
  });
}

async function statement(overrides: Partial<CompiledReadInput> = {}): Promise<CompiledReadStatement> {
  const result = await plan(overrides);
  if (result.kind !== 'compiled') {
    throw new Error(`expected a compiled statement, got a fallback: ${result.detail}`);
  }
  return result;
}

async function refusal(overrides: Partial<CompiledReadInput> = {}): Promise<CompiledReadFallback> {
  const result = await plan(overrides);
  if (result.kind !== 'fallback') {
    throw new Error(`expected a fallback, got ${result.sql}`);
  }
  return result;
}

describe('planning a compiled read', () => {
  it('projects the requested columns under their datamodel names', async () => {
    const compiled = await statement({
      prepared: tree({ select: { id: true, authorId: true } }),
    });

    expect(compiled.sql).toBe(
      'select "t0"."post_id" as "id", "t0"."author_id" as "authorId" from "posts" as "t0"',
    );
    expect(compiled.columns).toEqual([
      { name: 'id', dbName: 'post_id' },
      { name: 'authorId', dbName: 'author_id' },
    ]);
  });

  it('projects every scalar column when nothing narrows the read', async () => {
    const compiled = await statement({
      model: models[1],
      prepared: tree(),
    });

    expect(compiled.columns.map((column) => column.name)).toEqual(['id', 'name']);
  });

  it('drops the columns an omit removes', async () => {
    const compiled = await statement({
      model: models[1],
      prepared: tree({ omit: { name: true } }),
    });

    expect(compiled.columns.map((column) => column.name)).toEqual(['id']);
  });

  it('binds the policy predicate rather than inlining it', async () => {
    const compiled = await statement({ constraint: { authorId: 7 } });

    expect(compiled.sql).toContain('where');
    expect(compiled.sql).toContain('"t0"."author_id"');
    expect(compiled.sql).not.toContain('7');
    expect(compiled.parameters).toEqual([7]);
  });

  it('intersects the caller where with the policy predicate', async () => {
    const compiled = await statement({
      where: { published: true },
      constraint: { authorId: 7 },
    });

    expect(compiled.parameters).toEqual([true, 7]);
    expect(compiled.sql).toContain(' AND ');
  });

  it('emits no predicate at all when nothing constrains the read', async () => {
    const compiled = await statement();

    expect(compiled.sql).not.toContain('where');
    expect(compiled.parameters).toEqual([]);
  });

  it('orders by the physical column in the order the terms were given', async () => {
    const compiled = await statement({
      orderBy: [{ authorId: 'desc' }, { id: 'asc' }],
    });

    expect(compiled.sql).toContain('order by "t0"."author_id" desc, "t0"."post_id" asc');
    expect(compiled.reversed).toBe(false);
  });

  it('walks a negative take backwards and asks the caller to reverse the rows', async () => {
    const compiled = await statement({
      orderBy: [{ id: 'asc' }],
      take: -3,
      skip: 2,
    });

    expect(compiled.sql).toContain('order by "t0"."post_id" desc');
    expect(compiled.sql).toContain('limit ?');
    expect(compiled.parameters).toEqual([3, 2]);
    expect(compiled.reversed).toBe(true);
  });

  it('pages a positive take without reversing anything', async () => {
    const compiled = await statement({ orderBy: [{ id: 'asc' }], take: 3, skip: 1 });

    expect(compiled.parameters).toEqual([3, 1]);
    expect(compiled.reversed).toBe(false);
  });

  it('gives sqlite the open limit it needs to accept an offset alone', async () => {
    const compiled = await statement({ skip: 4 });

    expect(compiled.sql).toContain('limit ?');
    expect(compiled.parameters).toEqual([-1, 4]);
  });

  it('gives postgres an offset with no limit at all', async () => {
    const compiled = await statement({ provider: 'postgresql', skip: 4 });

    expect(compiled.sql).not.toContain('limit');
    expect(compiled.sql).toContain('offset $1');
    expect(compiled.parameters).toEqual([4]);
  });

  it('reads a single row when the read is a findOne', async () => {
    const compiled = await statement({ single: true, where: { id: 3 } });

    expect(compiled.sql).toContain('limit ?');
    expect(compiled.parameters).toEqual([3, 1]);
  });
});

describe('planning a compiled read that reaches a relation', () => {
  const user = models[1];

  it('aggregates a to-many into a correlated json array', async () => {
    const compiled = await statement({
      model: user,
      prepared: tree({ select: { id: true, posts: { select: { id: true, title: true } } } }),
    });

    expect(compiled.sql).toBe(
      'select "t0"."user_id" as "id", cast((select coalesce(json_group_array(json_object(' +
        `'id', "agg"."id", 'title', "agg"."title")), '[]') from (select "t1"."post_id" as "id", ` +
        '"t1"."title" as "title" from "posts" as "t1" where "t1"."author_id" = "t0"."user_id") ' +
        'as agg) as text) as "posts" from "users" as "t0"',
    );
    expect(compiled.relations).toEqual([
      {
        name: 'posts',
        list: true,
        fields: [
          { name: 'id', kind: 'plain' },
          { name: 'title', kind: 'plain' },
        ],
        relations: [],
      },
    ]);
  });

  it('aggregates a to-one into a correlated json object', async () => {
    const compiled = await statement({
      prepared: tree({ select: { id: true, author: { select: { name: true } } } }),
    });

    expect(compiled.sql).toBe(
      `select "t0"."post_id" as "id", cast((select json_object('name', "obj"."name") from ` +
        '(select "t1"."name" as "name" from "users" as "t1" where "t1"."user_id" = ' +
        '"t0"."author_id") as obj) as text) as "author" from "posts" as "t0"',
    );
    expect(compiled.relations[0].list).toBe(false);
  });

  it('aggregates a to-many on postgres with json_agg and a to-one with to_json', async () => {
    const many = await statement({
      model: user,
      provider: 'postgresql',
      prepared: tree({ select: { id: true, posts: { select: { id: true } } } }),
    });
    expect(many.sql).toBe(
      'select "t0"."user_id" as "id", cast((select coalesce(json_agg(agg), \'[]\') from ' +
        '(select "t1"."post_id" as "id" from "posts" as "t1" where "t1"."author_id" = ' +
        '"t0"."user_id") as agg) as text) as "posts" from "users" as "t0"',
    );

    const one = await statement({
      provider: 'postgresql',
      prepared: tree({ select: { id: true, author: { select: { name: true } } } }),
    });
    expect(one.sql).toBe(
      'select "t0"."post_id" as "id", cast((select to_json(obj) from (select "t1"."name" as ' +
        '"name" from "users" as "t1" where "t1"."user_id" = "t0"."author_id") as obj) as text) ' +
        'as "author" from "posts" as "t0"',
    );
  });

  it('puts the policy predicate of the nested model inside its own subquery', async () => {
    const compiled = await statement({
      model: user,
      prepared: tree({
        select: {
          id: true,
          posts: { select: { id: true }, where: { published: true } },
        },
      }),
      constraint: { name: 'Ada' },
    });

    const subquery = compiled.sql.slice(
      compiled.sql.indexOf('from (select'),
      compiled.sql.indexOf(') as agg'),
    );
    expect(subquery).toContain('"t1"."author_id" = "t0"."user_id"');
    expect(subquery).toContain('"t1"."published"');
    expect(compiled.sql.slice(compiled.sql.indexOf('from "users" as "t0"'))).toContain(
      '"t0"."name"',
    );
    expect(compiled.parameters).toEqual([true, 'Ada']);
  });

  it('reads every scalar column of a relation the projection asks for by name only', async () => {
    const compiled = await statement({
      prepared: tree({ select: { id: true, author: true } }),
    });

    expect(compiled.relations[0].fields.map((entry) => entry.name)).toEqual(['id', 'name']);
  });

  it('carries a BigInt and a Decimal out of a relation as text, and dates as they are', async () => {
    const compiled = await statement({
      model: user,
      prepared: tree({
        select: {
          id: true,
          assets: { select: { size: true, cost: true, createdAt: true } },
        },
      }),
    });

    expect(compiled.relations[0].fields).toEqual([
      { name: 'size', kind: 'bigint' },
      { name: 'cost', kind: 'decimal' },
      { name: 'createdAt', kind: 'datetime' },
    ]);
    expect(compiled.sql).toContain('cast("t1"."size" as text) as "size"');
    expect(compiled.sql).toContain('cast("t1"."cost" as text) as "cost"');
    expect(compiled.sql).toContain('"t1"."created_at" as "createdAt"');
  });

  it('nests a relation of a relation', async () => {
    const compiled = await statement({
      model: user,
      prepared: tree({
        select: { id: true, posts: { select: { id: true, author: { select: { name: true } } } } },
      }),
    });

    expect(compiled.relations[0].relations).toEqual([
      { name: 'author', list: false, fields: [{ name: 'name', kind: 'plain' }], relations: [] },
    ]);
    expect(compiled.sql).toContain('"t2"."user_id" = "t1"."author_id"');
  });

  it('adds the relations an include asks for to the columns of the model', async () => {
    const compiled = await statement({
      prepared: tree({
        include: { author: { select: { name: true } } },
        omit: { tags: true },
      }),
    });

    expect(compiled.columns.map((column) => column.name)).toEqual([
      'id',
      'title',
      'authorId',
      'published',
    ]);
    expect(compiled.relations.map((relation) => relation.name)).toEqual(['author']);
  });
});

describe('refusing to compile a read that reaches a relation', () => {
  it('hands back a relation golem cannot narrow to distinct rows or position a cursor in', async () => {
    const nested = (entry: Record<string, unknown>) =>
      refusal({
        model: models[1],
        prepared: tree({ select: { id: true, posts: { select: { id: true }, ...entry } } }),
      });

    expect(await nested({ distinct: ['id'] })).toMatchObject({ reason: 'distinct' });
    expect(await nested({ cursor: { id: 1 } })).toMatchObject({ reason: 'cursor' });
  });

  it('hands back a to-one asked to page, which Prisma rejects as an unknown argument', async () => {
    const nested = (entry: Record<string, unknown>) =>
      refusal({
        prepared: tree({ select: { id: true, author: { select: { id: true }, ...entry } } }),
      });

    expect(await nested({ take: 1 })).toMatchObject({ reason: 'take' });
    expect(await nested({ skip: 1 })).toMatchObject({ reason: 'take' });
    expect(await nested({ orderBy: { id: 'asc' } })).toMatchObject({ reason: 'orderBy' });
  });

  it('hands back a relation carrying a Bytes or a Json column', async () => {
    expect(
      await refusal({
        model: models[1],
        prepared: tree({ select: { id: true, assets: { select: { payload: true } } } }),
      }),
    ).toMatchObject({ reason: 'projection' });
    expect(
      await refusal({
        model: models[1],
        prepared: tree({ select: { id: true, assets: { select: { meta: true } } } }),
      }),
    ).toMatchObject({ reason: 'projection' });
    expect(
      await refusal({
        model: models[1],
        prepared: tree({ select: { id: true, assets: true } }),
      }),
    ).toMatchObject({ reason: 'projection' });
  });

  it('hands back a relation with no foreign key to correlate a subquery on', async () => {
    expect(
      await refusal({
        model: models[1],
        prepared: tree({ select: { id: true, groups: { select: { id: true } } } }),
      }),
    ).toMatchObject({ reason: 'relation' });
  });

  it('hands back a relation matching more than one foreign key on the model it points at', async () => {
    const ticket: DatamodelModel = {
      name: 'Ticket',
      dbName: 'tickets',
      fields: [
        field({ name: 'id', dbName: 'ticket_id', type: 'Int', isId: true }),
        field({ name: 'reporterId', dbName: 'reporter_id', type: 'Int' }),
        field({ name: 'assigneeId', dbName: 'assignee_id', type: 'Int' }),
        field({
          name: 'reporter',
          kind: 'object',
          type: 'Person',
          relationName: 'PersonToTicket',
          relationFromFields: ['reporterId'],
          relationToFields: ['id'],
        }),
        field({
          name: 'assignee',
          kind: 'object',
          type: 'Person',
          relationName: 'PersonToTicket',
          relationFromFields: ['assigneeId'],
          relationToFields: ['id'],
        }),
      ],
    };
    const person: DatamodelModel = {
      name: 'Person',
      dbName: 'people',
      fields: [
        field({ name: 'id', dbName: 'person_id', type: 'Int', isId: true }),
        field({
          name: 'tickets',
          kind: 'object',
          type: 'Ticket',
          isList: true,
          isRequired: false,
          relationName: 'PersonToTicket',
        }),
      ],
    };
    const ambiguous = [person, ticket];

    expect(
      await planCompiledRead({
        model: person,
        models: ambiguous,
        metadata: buildModelMetadata(ambiguous),
        provider: 'sqlite',
        prepared: tree({ select: { id: true, tickets: { select: { id: true } } } }),
      }),
    ).toMatchObject({
      kind: 'fallback',
      reason: 'relation',
      detail: 'Person.tickets matches 2 foreign keys on Ticket, and golem cannot tell which one holds it',
    });
  });

  it('hands back a relation whose where the policy condition language does not render', async () => {
    expect(
      await refusal({
        model: models[1],
        prepared: tree({
          select: {
            id: true,
            posts: { select: { id: true }, where: { title: { mode: 'insensitive', search: 'x' } } },
          },
        }),
      }),
    ).toMatchObject({ reason: 'where' });
  });

  it('hands back a to-one a where narrows, which Prisma rejects rather than filtering', async () => {
    expect(
      await refusal({
        prepared: tree({
          select: { id: true, author: { select: { name: true }, where: { name: 'Ada' } } },
        }),
      }),
    ).toMatchObject({ reason: 'relation' });
  });

  it('hands sqlite back a relation projecting more fields than json_object takes', async () => {
    const wide: DatamodelModel = {
      name: 'Wide',
      dbName: 'wide',
      indexes: [{ kind: 'normal', fields: ['ownerId'] }],
      fields: [
        field({ name: 'id', dbName: 'id', type: 'Int', isId: true }),
        field({ name: 'ownerId', dbName: 'owner_id', type: 'Int' }),
        ...Array.from({ length: 70 }, (_, index) =>
          field({ name: `c${index}`, dbName: `c${index}`, type: 'String' }),
        ),
        field({
          name: 'owner',
          kind: 'object',
          type: 'User',
          relationName: 'WideToUser',
          relationFromFields: ['ownerId'],
          relationToFields: ['id'],
        }),
      ],
    };
    const owner: DatamodelModel = {
      name: 'User',
      dbName: 'users',
      fields: [
        field({ name: 'id', dbName: 'user_id', type: 'Int', isId: true }),
        field({
          name: 'wides',
          kind: 'object',
          type: 'Wide',
          isList: true,
          isRequired: false,
          relationName: 'WideToUser',
        }),
      ],
    };
    const wideModels = [owner, wide];
    const prepared = {
      select: { id: true, wides: true },
      toOneChecks: [],
      maskChecks: [],
      injected: [],
    };

    expect(
      await planCompiledRead({
        model: owner,
        models: wideModels,
        metadata: buildModelMetadata(wideModels),
        provider: 'sqlite',
        prepared,
      }),
    ).toMatchObject({ kind: 'fallback', reason: 'projection' });
    expect(
      await planCompiledRead({
        model: owner,
        models: wideModels,
        metadata: buildModelMetadata(wideModels),
        provider: 'postgresql',
        prepared,
      }),
    ).toMatchObject({ kind: 'compiled' });
  });

  it('hands back a relation projecting nothing at all', async () => {
    expect(
      await refusal({
        model: models[1],
        prepared: tree({ select: { id: true, posts: { select: {} } } }),
      }),
    ).toMatchObject({ reason: 'projection' });
  });
});

describe('refusing to compile a read', () => {
  it('hands back a read whose provider golem does not render SQL for', async () => {
    expect(await refusal({ provider: 'mongodb' })).toMatchObject({ reason: 'provider' });
    expect(await refusal({ provider: undefined })).toMatchObject({ reason: 'provider' });
  });

  it('hands back a read on a model carrying no physical table name', async () => {
    expect(
      await refusal({ model: models[2], prepared: tree({ select: { id: true } }) }),
    ).toMatchObject({ reason: 'projection' });
  });

  it('hands back a cursor golem cannot position and a distinct golem cannot group by', async () => {
    expect(await refusal({ cursor: { nope: 1 } })).toMatchObject({ reason: 'cursor' });
    expect(await refusal({ cursor: { id: { in: [1] } } })).toMatchObject({ reason: 'cursor' });
    expect(await refusal({ cursor: {} })).toMatchObject({ reason: 'cursor' });
    expect(await refusal({ cursor: { id: 1 }, orderBy: [{ author: { name: 'asc' } }] })).toMatchObject(
      { reason: 'cursor' },
    );
    expect(await refusal({ distinct: ['author'] })).toMatchObject({ reason: 'distinct' });
    expect(await refusal({ distinct: ['tags'] })).toMatchObject({ reason: 'distinct' });
    expect(await refusal({ distinct: [] })).toMatchObject({ reason: 'distinct' });
  });

  it('hands back a read selecting something that is not a column of the model', async () => {
    expect(await refusal({ prepared: tree({ select: { nope: true } }) })).toMatchObject({
      reason: 'projection',
    });
    expect(await refusal({ prepared: tree({ select: { tags: true } }) })).toMatchObject({
      reason: 'projection',
    });
    expect(
      await refusal({ model: models[3], prepared: tree({ select: { shadow: true } }) }),
    ).toMatchObject({ reason: 'projection' });
    expect(await refusal({ prepared: tree({ select: {} }) })).toMatchObject({
      reason: 'projection',
    });
  });

  it('hands back a read ordered by something the compiled path cannot order by', async () => {
    expect(await refusal({ orderBy: [{ author: 'asc' }] })).toMatchObject({ reason: 'orderBy' });
    expect(await refusal({ orderBy: [{ id: { sort: 'sideways' } }] })).toMatchObject({
      reason: 'orderBy',
    });
    expect(await refusal({ orderBy: [{ id: { sort: 'asc', nulls: 'middle' } }] })).toMatchObject({
      reason: 'orderBy',
    });
    expect(await refusal({ orderBy: [{ id: { sort: 'asc', mode: 'insensitive' } }] })).toMatchObject({
      reason: 'orderBy',
    });
    expect(await refusal({ orderBy: [{ author: { _count: 'asc' } }] })).toMatchObject({
      reason: 'orderBy',
    });
    expect(await refusal({ orderBy: [{ _count: 'asc' }] })).toMatchObject({ reason: 'orderBy' });
    expect(await refusal({ orderBy: ['id'] })).toMatchObject({ reason: 'orderBy' });
  });

  it('hands back a read paged by something that is not a whole number of rows', async () => {
    expect(await refusal({ take: 1.5 })).toMatchObject({ reason: 'take' });
    expect(await refusal({ skip: -1 })).toMatchObject({ reason: 'take' });
  });

  it('hands back a negative take over rows nothing orders', async () => {
    expect(await refusal({ take: -2 })).toMatchObject({ reason: 'take' });
  });

  it('hands back a read whose policy is a flat denial, which Prisma rejects', async () => {
    expect(await refusal({ constraint: null })).toMatchObject({ reason: 'where' });
    expect(await refusal({ where: null })).toMatchObject({ reason: 'where' });
  });

  it('hands back a read whose where the policy condition language does not render', async () => {
    expect(await refusal({ where: { title: { mode: 'insensitive', search: 'x' } } })).toMatchObject({
      reason: 'where',
    });
    expect(await refusal({ where: { notAColumn: 1 } })).toMatchObject({ reason: 'where' });
  });
});
