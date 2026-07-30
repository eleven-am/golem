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

  it('hands back a read that asks for a cursor or for distinct rows', async () => {
    expect(await refusal({ cursor: { id: 1 } })).toMatchObject({ reason: 'cursor' });
    expect(await refusal({ distinct: ['title'] })).toMatchObject({ reason: 'distinct' });
  });

  it('hands back a read whose selection set reaches a relation', async () => {
    expect(
      await refusal({ prepared: tree({ select: { id: true, author: { select: { id: true } } } }) }),
    ).toMatchObject({ reason: 'relation' });
    expect(await refusal({ prepared: tree({ include: { author: true } }) })).toMatchObject({
      reason: 'relation',
    });
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
    expect(await refusal({ orderBy: [{ id: { sort: 'asc', nulls: 'first' } }] })).toMatchObject({
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
