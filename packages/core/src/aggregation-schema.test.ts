import { graphql, printSchema } from 'graphql';
import { DatamodelDocument } from './datamodel';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument<{
  Play: 'id' | 'userId' | 'trackId' | 'msPlayed';
}> = {
  models: [
    {
      name: 'Play',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'userId', type: 'String' }),
        field({ name: 'trackId', type: 'String' }),
        field({ name: 'msPlayed', type: 'Int' }),
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

    expect(result.errors?.[0].message).toMatch(/take of at most 50/);
    expect(client.play.groupBy).not.toHaveBeenCalled();
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
