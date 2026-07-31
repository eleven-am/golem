import {
  CompiledAggregateInput,
  CompiledAggregateStatement,
  planCompiledAggregate,
} from './compiled-aggregate';
import { CompiledReadFallback } from './compiled-read';
import { DatamodelModel } from './datamodel';
import { buildModelMetadata } from './model-meta';
import { field } from './testing';

const models: readonly DatamodelModel[] = [
  {
    name: 'Metric',
    dbName: 'metrics',
    fields: [
      field({ name: 'id', dbName: 'metric_id', type: 'Int', isId: true }),
      field({ name: 'label', dbName: 'label', type: 'String' }),
      field({ name: 'ownerId', dbName: 'owner_id', type: 'Int' }),
      field({ name: 'rank', dbName: 'rank_value', type: 'Int', isRequired: false }),
      field({ name: 'score', dbName: 'score', type: 'Decimal', isRequired: false }),
      field({ name: 'hits', dbName: 'hits', type: 'BigInt' }),
      field({ name: 'ratio', dbName: 'ratio', type: 'Float' }),
      field({ name: 'active', dbName: 'active', type: 'Boolean' }),
      field({ name: 'recordedAt', dbName: 'recorded_at', type: 'DateTime' }),
      field({ name: 'tags', dbName: 'tags', type: 'String', isList: true }),
      field({
        name: 'owner',
        kind: 'object',
        type: 'User',
        relationName: 'MetricToUser',
        relationFromFields: ['ownerId'],
        relationToFields: ['id'],
      }),
    ],
  },
  {
    name: 'User',
    dbName: 'users',
    fields: [field({ name: 'id', dbName: 'user_id', type: 'Int', isId: true })],
  },
  {
    name: 'Unmapped',
    fields: [field({ name: 'id', dbName: 'id', type: 'Int', isId: true })],
  },
];

const metadata = buildModelMetadata(models);

function plan(
  overrides: Partial<CompiledAggregateInput> = {},
): Promise<CompiledAggregateStatement | CompiledReadFallback> {
  return planCompiledAggregate({
    model: models[0]!,
    models,
    metadata,
    provider: 'postgresql',
    ...overrides,
  }) as Promise<CompiledAggregateStatement | CompiledReadFallback>;
}

async function compiled(
  overrides: Partial<CompiledAggregateInput> = {},
): Promise<CompiledAggregateStatement> {
  const result = await plan(overrides);
  expect(result.kind).toBe('compiled');
  return result as CompiledAggregateStatement;
}

async function refused(
  overrides: Partial<CompiledAggregateInput> = {},
): Promise<CompiledReadFallback> {
  const result = await plan(overrides);
  expect(result.kind).toBe('fallback');
  return result as CompiledReadFallback;
}

describe('planning a compiled aggregate', () => {
  it('names every measure after the column Prisma names it after', async () => {
    const statement = await compiled({
      _count: { _all: true, label: true },
      _sum: { hits: true },
      _avg: { rank: true },
      _min: { recordedAt: true },
      _max: { score: true },
    });
    expect(statement.measures.map((measure) => measure.alias)).toEqual([
      '_count$_all',
      '_count$label',
      '_sum$hits',
      '_avg$rank_value',
      '_min$recorded_at',
      '_max$score',
    ]);
  });

  it('decodes each measure by the aggregate and the column type together', async () => {
    const statement = await compiled({
      _count: { _all: true },
      _sum: { rank: true, hits: true, score: true, ratio: true },
      _avg: { rank: true, hits: true, score: true },
      _min: { rank: true, hits: true, score: true, label: true, recordedAt: true, ratio: true },
    });
    const kinds = Object.fromEntries(
      statement.measures.map((measure) => [`${measure.group}.${measure.field ?? ''}`, measure.decode]),
    );
    expect(kinds).toEqual({
      '_count._all': 'safeInteger',
      '_sum.rank': 'safeInteger',
      '_sum.hits': 'bigint',
      '_sum.score': 'decimal',
      '_sum.ratio': 'float',
      '_avg.rank': 'float',
      '_avg.hits': 'float',
      '_avg.score': 'decimal',
      '_min.rank': 'safeInteger',
      '_min.hits': 'bigint',
      '_min.score': 'decimal',
      '_min.label': 'string',
      '_min.recordedAt': 'datetime',
      '_min.ratio': 'float',
    });
  });

  it('carries a BigInt and a Decimal out as text, and leaves every other measure alone', async () => {
    const statement = await compiled({
      _count: { _all: true },
      _sum: { hits: true, score: true, rank: true },
      _min: { recordedAt: true },
    });
    expect(statement.sql).toContain('cast(sum("t0"."hits") as text)');
    expect(statement.sql).toContain('cast(sum("t0"."score") as text)');
    expect(statement.sql).toContain('sum("t0"."rank_value") as "_sum$rank_value"');
    expect(statement.sql).toContain('min("t0"."recorded_at") as "_min$recorded_at"');
    expect(statement.sql).not.toContain('cast(count');
  });

  it('reads a scalar count as a count of rows, not of a column', async () => {
    const statement = await compiled({ _count: true });
    expect(statement.sql).toContain('count(*)');
    expect(statement.measures).toEqual([
      { alias: '_count$_all', group: '_count', field: undefined, decode: 'safeInteger' },
    ]);
  });

  it('puts the policy predicate in the aggregate WHERE', async () => {
    const statement = await compiled({
      _count: { _all: true },
      constraint: { ownerId: 2 },
    });
    expect(statement.sql).toContain('"t0"."owner_id"');
    expect(statement.parameters).toEqual([2]);
  });

  it('groups, filters and pages a grouped statement in one SELECT', async () => {
    const statement = await compiled({
      by: ['ownerId'],
      _count: { _all: true },
      having: { ownerId: { _count: { gt: 1 } } },
      orderBy: [{ _count: { ownerId: 'desc' } }],
      take: 5,
      skip: 2,
    });
    expect(statement.keys).toEqual(['ownerId']);
    expect(statement.sql).toContain('group by "t0"."owner_id"');
    expect(statement.sql).toContain('having');
    expect(statement.sql).toContain('order by count("t0"."owner_id") desc');
    expect(statement.sql).toContain('limit');
    expect(statement.sql).toContain('offset');
  });

  it('bounds a skipped sqlite grouping, which refuses an offset without a limit', async () => {
    const statement = await compiled({
      provider: 'sqlite',
      by: ['ownerId'],
      _count: { _all: true },
      skip: 2,
    });
    expect(statement.sql).toContain('limit');
    expect(statement.sql).toContain('offset');
    expect(statement.parameters).toEqual([-1, 2]);
  });

  it('hands back an aggregate over a window Prisma fences with a subquery', async () => {
    for (const window of [{ take: 1 }, { skip: 1 }, { orderBy: { id: 'asc' } }]) {
      expect(await refused({ _count: { _all: true }, ...window })).toMatchObject({
        reason: 'take',
      });
    }
    expect(await refused({ _count: { _all: true }, cursor: { id: 1 } })).toMatchObject({
      reason: 'cursor',
    });
  });

  it('hands back a measure it cannot decode', async () => {
    expect(await refused({ _min: { active: true } })).toMatchObject({ reason: 'measure' });
    expect(await refused({ _sum: { label: true } })).toMatchObject({ reason: 'measure' });
    expect(await refused({ _avg: { recordedAt: true } })).toMatchObject({ reason: 'measure' });
    expect(await refused({ _count: { tags: true } })).toMatchObject({ reason: 'measure' });
    expect(await refused({ _count: { owner: true } })).toMatchObject({ reason: 'measure' });
    expect(await refused({ _sum: { missing: true } })).toMatchObject({ reason: 'measure' });
    expect(await refused({})).toMatchObject({ reason: 'measure' });
  });

  it('hands back a grouping it cannot key on', async () => {
    expect(await refused({ by: ['owner'], _count: { _all: true } })).toMatchObject({
      reason: 'group',
    });
    expect(await refused({ by: ['tags'], _count: { _all: true } })).toMatchObject({
      reason: 'group',
    });
    expect(await refused({ by: [], _count: { _all: true } })).toMatchObject({ reason: 'group' });
    expect(
      await refused({ by: ['ownerId', 'ownerId'], _count: { _all: true } }),
    ).toMatchObject({ reason: 'group' });
  });

  it('hands back a having it cannot render', async () => {
    expect(
      await refused({
        by: ['ownerId'],
        _count: { _all: true },
        having: { label: { _min: { contains: 'a' } } },
      }),
    ).toMatchObject({ reason: 'having' });
    expect(
      await refused({
        by: ['ownerId'],
        _count: { _all: true },
        having: { label: { _sum: { gt: 1 } } },
      }),
    ).toMatchObject({ reason: 'having' });
    expect(
      await refused({ _count: { _all: true }, having: { ownerId: { _count: { gt: 1 } } } }),
    ).toMatchObject({ reason: 'having' });
  });

  it('hands back an order it cannot resolve against the grouping', async () => {
    expect(
      await refused({ by: ['ownerId'], _count: { _all: true }, orderBy: { label: 'asc' } }),
    ).toMatchObject({ reason: 'orderBy' });
    expect(
      await refused({
        by: ['ownerId'],
        _count: { _all: true },
        orderBy: { _count: { ownerId: 'sideways' } },
      }),
    ).toMatchObject({ reason: 'orderBy' });
  });

  it('hands back a provider, a table and a policy it cannot compile', async () => {
    expect(await refused({ provider: 'mysql', _count: true })).toMatchObject({
      reason: 'provider',
    });
    expect(await refused({ provider: undefined, _count: true })).toMatchObject({
      reason: 'provider',
    });
    expect(await refused({ model: models[2]!, _count: true })).toMatchObject({
      reason: 'measure',
    });
    expect(await refused({ _count: true, constraint: null })).toMatchObject({ reason: 'where' });
    expect(await refused({ _count: true, where: null })).toMatchObject({ reason: 'where' });
  });

  it('hands back a where the policy renderer refuses', async () => {
    expect(
      await refused({ _count: true, where: { label: { mode: 'insensitive', search: 'x' } } }),
    ).toMatchObject({ reason: 'where' });
    expect(await refused({ _count: true, where: { notAColumn: 1 } })).toMatchObject({
      reason: 'where',
    });
  });
});
