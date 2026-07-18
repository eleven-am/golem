import { graphql, printSchema } from 'graphql';
import { DatamodelDocument } from './datamodel';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument<{
  Play: 'id' | 'userId' | 'trackId' | 'msPlayed' | 'bytesPlayed' | 'cost';
}> = {
  models: [
    {
      name: 'Play',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'userId', type: 'String' }),
        field({ name: 'trackId', type: 'String' }),
        field({ name: 'msPlayed', type: 'Int' }),
        field({ name: 'bytesPlayed', type: 'BigInt' }),
        field({ name: 'cost', type: 'Decimal' }),
        field({ name: 'skipped', type: 'Boolean', hasDefaultValue: true }),
      ],
    },
  ],
  enums: [],
};

function fakeClient(groups: unknown[] = [], aggregate: unknown = {}) {
  return {
    play: {
      findMany: jest.fn().mockResolvedValue([]),
      findUnique: jest.fn().mockResolvedValue(null),
      create: jest.fn().mockResolvedValue({ id: '1' }),
      update: jest.fn().mockResolvedValue({ id: '1' }),
      updateMany: jest.fn().mockResolvedValue({ count: 0 }),
      delete: jest.fn().mockResolvedValue({ id: '1' }),
      deleteMany: jest.fn().mockResolvedValue({ count: 0 }),
      upsert: jest.fn().mockResolvedValue({ id: '1' }),
      count: jest.fn().mockResolvedValue(0),
      aggregate: jest.fn().mockResolvedValue(aggregate),
      groupBy: jest.fn().mockResolvedValue(groups),
    },
  };
}

const enabled = { Play: { aggregations: true as const } };

describe('aggregation schema surface', () => {
  it('is absent unless a model opts in', () => {
    const sdl = printSchema(
      buildGolemSchema({ datamodel, client: fakeClient() }),
    );
    expect(sdl).not.toContain('playsGrouped');
    expect(sdl).not.toContain('playsAggregate');
  });

  it('generates aggregate and grouped queries when enabled', () => {
    const sdl = printSchema(
      buildGolemSchema({ datamodel, client: fakeClient(), models: enabled }),
    );
    expect(sdl).toContain('playsAggregate');
    expect(sdl).toContain('playsGrouped');
    expect(sdl).toContain('enum PlayGroupField');
    expect(sdl).toContain('input PlayMeasuresInput');
    expect(sdl).toMatch(/type PlaySumValues[\s\S]*bytesPlayed: BigInt/);
    expect(sdl).toMatch(/type PlayAvgValues[\s\S]*bytesPlayed: Float/);
    expect(sdl).toMatch(/type PlayMinValues[\s\S]*bytesPlayed: BigInt/);
    expect(sdl).toMatch(/type PlayMaxValues[\s\S]*bytesPlayed: BigInt/);
    expect(sdl).toMatch(/type PlaySumValues[\s\S]*cost: Decimal/);
    expect(sdl).toContain('scalar Decimal');
  });

  it('serializes BigInt and Decimal measures without converting them to numbers', async () => {
    const exactBigInt = 9007199254740993n;
    const exactDecimal = { toString: () => '1234567890.1234567890123456789' };
    const client = fakeClient([], {
      _sum: { bytesPlayed: exactBigInt, cost: exactDecimal },
      _avg: { bytesPlayed: 4.5, cost: exactDecimal },
      _min: { bytesPlayed: exactBigInt, cost: exactDecimal },
      _max: { bytesPlayed: exactBigInt, cost: exactDecimal },
    });
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsAggregate(measures: {
          sum: [bytesPlayed, cost]
          avg: [bytesPlayed, cost]
          min: [bytesPlayed, cost]
          max: [bytesPlayed, cost]
        }) {
          sum { bytesPlayed cost }
          avg { bytesPlayed cost }
          min { bytesPlayed cost }
          max { bytesPlayed cost }
        }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.playsAggregate).toEqual({
      sum: { bytesPlayed: exactBigInt.toString(), cost: exactDecimal.toString() },
      avg: { bytesPlayed: 4.5, cost: exactDecimal.toString() },
      min: { bytesPlayed: exactBigInt.toString(), cost: exactDecimal.toString() },
      max: { bytesPlayed: exactBigInt.toString(), cost: exactDecimal.toString() },
    });
  });

  it('serializes exact grouped measures and preserves nullable empty measures', async () => {
    const client = fakeClient([
      {
        trackId: 't1',
        _sum: { bytesPlayed: 9007199254740993n, cost: { toString: () => '0.1000000000000000001' } },
        _min: { bytesPlayed: null, cost: null },
      },
    ]);
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsGrouped(by: [trackId], measures: { sum: [bytesPlayed, cost], min: [bytesPlayed, cost] }) {
          key { trackId }
          sum { bytesPlayed cost }
          min { bytesPlayed cost }
        }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.playsGrouped).toEqual([
      {
        key: { trackId: 't1' },
        sum: { bytesPlayed: '9007199254740993', cost: '0.1000000000000000001' },
        min: { bytesPlayed: null, cost: null },
      },
    ]);
  });

  it('limits grouping keys to the configured dimensions', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { Play: { aggregations: { dimensions: ['trackId'] } } },
      }),
    );
    const groupFields = /enum PlayGroupField \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    expect(groupFields).toContain('trackId');
    expect(groupFields).not.toContain('userId');
  });

  it('runs a top-N grouped query and maps it onto the delegate', async () => {
    const client = fakeClient([
      { trackId: 't1', _count: 12, _sum: { msPlayed: 4200 } },
    ]);
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsGrouped(
          by: [trackId]
          measures: { count: true, sum: [msPlayed] }
          orderBy: { sum: { msPlayed: desc } }
          take: 10
        ) { key { trackId } count sum { msPlayed } }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.playsGrouped).toEqual([
      { key: { trackId: 't1' }, count: 12, sum: { msPlayed: 4200 } },
    ]);
    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({
        by: ['trackId'],
        take: 10,
        _count: true,
        _sum: { msPlayed: true },
        orderBy: [{ _sum: { msPlayed: 'desc' } }],
      }),
    );
  });

  it('translates a measure filter into a prisma having clause', async () => {
    const client = fakeClient([]);
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsGrouped(
          by: [trackId]
          measures: { count: true }
          having: { sum: { msPlayed: { gt: 1000 } } }
          take: 5
        ) { count }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({
        having: { msPlayed: { _sum: { gt: 1000 } } },
      }),
    );
  });

  it('translates a count ordering onto the first grouping key', async () => {
    const client = fakeClient([]);
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    await graphql({
      schema,
      source: `{
        playsGrouped(by: [trackId], measures: { count: true }, orderBy: { count: desc }, take: 5) { count }
      }`,
    });

    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({ orderBy: [{ _count: { trackId: 'desc' } }] }),
    );
  });

  it('rejects a grouped query beyond the configured cap', async () => {
    const client = fakeClient([]);
    const schema = buildGolemSchema({
      datamodel,
      client,
      models: { Play: { aggregations: { maxGroups: 50 } } },
    });

    const result = await graphql({
      schema,
      source: '{ playsGrouped(by: [trackId], measures: { count: true }, take: 500) { count } }',
    });

    expect(result.errors?.[0].message).toMatch(/explicit take of at most 50/);
    expect(client.play.groupBy).not.toHaveBeenCalled();
  });

  it('bounds an uncapped-take query by the cap without demanding one', async () => {
    const client = fakeClient([{ trackId: 't1', _count: 1 }]);
    const schema = buildGolemSchema({
      datamodel,
      client,
      models: { Play: { aggregations: { maxGroups: 50 } } },
    });

    const result = await graphql({
      schema,
      source: '{ playsGrouped(by: [trackId], measures: { count: true }) { count } }',
    });

    expect(result.errors).toBeUndefined();
    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({ take: 51, orderBy: [{ trackId: 'asc' }] }),
    );
  });

  it('refuses rather than truncates when a query exceeds the cap', async () => {
    const rows = Array.from({ length: 51 }, (_, index) => ({
      trackId: `t${index}`,
      _count: 1,
    }));
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(rows),
      models: { Play: { aggregations: { maxGroups: 50 } } },
    });

    const result = await graphql({
      schema,
      source: '{ playsGrouped(by: [trackId], measures: { count: true }) { count } }',
    });

    expect(result.errors?.[0].message).toMatch(/matched more than 50 groups/);
    expect(result.data).toBeNull();
  });

  it('leaves an uncapped model unbounded and unordered', async () => {
    const client = fakeClient([]);
    const schema = buildGolemSchema({
      datamodel,
      client,
      models: { Play: { aggregations: { dimensions: ['trackId'] } } },
    });

    await graphql({
      schema,
      source: '{ playsGrouped(by: [trackId], measures: { count: true }) { count } }',
    });

    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.not.objectContaining({ take: expect.anything() }),
    );
    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.not.objectContaining({ orderBy: expect.anything() }),
    );
  });

  it('runs a filtered scalar aggregate', async () => {
    const client = fakeClient([], { _count: 7, _sum: { msPlayed: 990 } });
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsAggregate(where: { skipped: { equals: false } }, measures: { count: true, sum: [msPlayed] }) {
          count sum { msPlayed }
        }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.playsAggregate).toEqual({
      count: 7,
      sum: { msPlayed: 990 },
    });
    expect(client.play.aggregate).toHaveBeenCalledWith(
      expect.objectContaining({
        where: { skipped: { equals: false } },
        _count: true,
        _sum: { msPlayed: true },
      }),
    );
  });

  it('does not expose hidden fields as dimensions or measures', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { Play: { aggregations: true, hidden: ['userId'] } },
      }),
    );
    const groupFields = /enum PlayGroupField \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    expect(groupFields).not.toContain('userId');
  });
});
