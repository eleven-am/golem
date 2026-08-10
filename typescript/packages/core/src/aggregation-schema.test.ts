import { graphql, parse, printSchema, validate } from 'graphql';
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
    expect(sdl).toMatch(/input PlaySumFilterInput[\s\S]*msPlayed: SafeIntFilter/);
    expect(sdl).toMatch(/input PlayAvgFilterInput[\s\S]*msPlayed: FloatFilter/);
    expect(sdl).toContain('scalar Decimal');
  });

  it('exposes list-valued in operators for every numeric aggregation filter', () => {
    const sdl = printSchema(
      buildGolemSchema({ datamodel, client: fakeClient(), models: enabled }),
    );
    const inputFields = (name: string): string =>
      new RegExp(`input ${name} \\{([^}]*)\\}`).exec(sdl)?.[1] ?? '';

    expect(inputFields('IntFilter')).toContain('in: [Int!]');
    expect(inputFields('SafeIntFilter')).toContain('in: [SafeInt!]');
    expect(inputFields('FloatFilter')).toContain('in: [Float!]');
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

  it('rejects fractional Int sum filters before calling Prisma', async () => {
    const client = fakeClient([]);
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsGrouped(
          by: [trackId]
          having: { sum: { msPlayed: { gt: 1.5 } } }
        ) { count }
      }`,
    });

    expect(result.errors?.[0].message).toContain('SafeInt cannot represent non-integer literal: 1.5');
    expect(client.play.groupBy).not.toHaveBeenCalled();
  });

  it('accepts Int sum filters above the GraphQL Int range', async () => {
    const client = fakeClient([]);
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsGrouped(
          by: [trackId]
          having: { sum: { msPlayed: { gt: 3000000000 } } }
        ) { count }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({
        having: { msPlayed: { _sum: { gt: 3000000000 } } },
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

  it('maps countFields onto a per-field prisma count', async () => {
    const client = fakeClient([], { _count: { trackId: 4, userId: 3 } });
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsAggregate(measures: { countFields: [trackId, userId] }) {
          count countBy { trackId userId }
        }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.playsAggregate).toEqual({
      count: null,
      countBy: { trackId: 4, userId: 3 },
    });
    expect(client.play.aggregate).toHaveBeenCalledWith(
      expect.objectContaining({ _count: { trackId: true, userId: true } }),
    );
  });

  it('asks for _all alongside per-field counts when both are requested', async () => {
    const client = fakeClient([], { _count: { _all: 9, trackId: 4 } });
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsAggregate(measures: { count: true, countFields: [trackId] }) {
          count countBy { trackId }
        }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.playsAggregate).toEqual({
      count: 9,
      countBy: { trackId: 4 },
    });
    expect(client.play.aggregate).toHaveBeenCalledWith(
      expect.objectContaining({ _count: { _all: true, trackId: true } }),
    );
  });

  it('keeps a bare count scalar when no fields are named', async () => {
    const client = fakeClient([], { _count: 7 });
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: '{ playsAggregate(measures: { count: true }) { count countBy { trackId } } }',
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.playsAggregate).toEqual({ count: 7, countBy: null });
    expect(client.play.aggregate).toHaveBeenCalledWith(
      expect.objectContaining({ _count: true }),
    );
  });

  it('asks prisma for no count at all when countFields is empty', async () => {
    const client = fakeClient([], {});
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: '{ playsAggregate(measures: { countFields: [] }) { count countBy { trackId } } }',
    });

    expect(result.errors).toBeUndefined();
    expect(client.play.aggregate).toHaveBeenCalledWith(
      expect.not.objectContaining({ _count: expect.anything() }),
    );
    expect(Object.keys(client.play.aggregate.mock.calls[0][0])).not.toContain('_count');
  });

  it('carries per-field counts through a grouped query', async () => {
    const client = fakeClient([
      { trackId: 't1', _count: { _all: 5, userId: 2 } },
    ]);
    const schema = buildGolemSchema({ datamodel, client, models: enabled });

    const result = await graphql({
      schema,
      source: `{
        playsGrouped(by: [trackId], measures: { count: true, countFields: [userId] }) {
          key { trackId } count countBy { userId }
        }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.playsGrouped).toEqual([
      { key: { trackId: 't1' }, count: 5, countBy: { userId: 2 } },
    ]);
    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({ _count: { _all: true, userId: true } }),
    );
  });

  it('counts fields the dimensions allowlist keeps out of grouping', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { Play: { aggregations: { dimensions: ['trackId'] } } },
      }),
    );
    const groupFields = /enum PlayGroupField \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    const countFields = /enum PlayCountField \{([^}]*)\}/.exec(sdl)?.[1] ?? '';

    expect(groupFields).not.toContain('userId');
    expect(countFields).toContain('userId');
    expect(countFields).toContain('trackId');
  });

  it('keeps hidden fields out of the countable set', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { Play: { aggregations: true, hidden: ['userId'] } },
      }),
    );
    const countValues = /type PlayCountValues \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    expect(countValues).not.toContain('userId');
    expect(countValues).toContain('trackId');
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

const sessionDatamodel: DatamodelDocument<{
  Session: 'id' | 'label' | 'startedAt' | 'duration';
}> = {
  models: [
    {
      name: 'Session',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'label', type: 'String' }),
        field({ name: 'startedAt', type: 'DateTime' }),
        field({ name: 'duration', type: 'Int' }),
        field({ name: 'active', type: 'Boolean' }),
      ],
    },
  ],
  enums: [],
};

const noteDatamodel: DatamodelDocument<{ Note: 'id' | 'body' | 'writtenAt' }> = {
  models: [
    {
      name: 'Note',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'body', type: 'String' }),
        field({ name: 'writtenAt', type: 'DateTime' }),
      ],
    },
  ],
  enums: [],
};

const counterDatamodel: DatamodelDocument<{ Counter: 'id' | 'value' }> = {
  models: [
    {
      name: 'Counter',
      fields: [
        field({ name: 'id', type: 'Int', isId: true, hasDefaultValue: true }),
        field({ name: 'value', type: 'Int' }),
      ],
    },
  ],
  enums: [],
};

function fakeDelegate(aggregate: unknown = {}) {
  return {
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
    groupBy: jest.fn().mockResolvedValue([]),
  };
}

describe('orderable measures', () => {
  const sessionEnabled = { Session: { aggregations: true as const } };

  it('admits DateTime and String to min and max but not to sum or avg', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel: sessionDatamodel,
        client: { session: fakeDelegate() },
        models: sessionEnabled,
      }),
    );

    const measureFields = /enum SessionMeasureField \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    const orderableFields = /enum SessionOrderableField \{([^}]*)\}/.exec(sdl)?.[1] ?? '';

    expect(measureFields).toContain('duration');
    expect(measureFields).not.toContain('startedAt');
    expect(measureFields).not.toContain('label');

    expect(orderableFields).toContain('duration');
    expect(orderableFields).toContain('startedAt');
    expect(orderableFields).toContain('label');
    expect(orderableFields).not.toContain('active');

    const measuresInput = /input SessionMeasuresInput \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    expect(measuresInput).toContain('sum: [SessionMeasureField!]');
    expect(measuresInput).toContain('avg: [SessionMeasureField!]');
    expect(measuresInput).toContain('min: [SessionOrderableField!]');
    expect(measuresInput).toContain('max: [SessionOrderableField!]');
  });

  it('types min and max outputs by the field, and keeps sum numeric', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel: sessionDatamodel,
        client: { session: fakeDelegate() },
        models: sessionEnabled,
      }),
    );

    expect(sdl).toMatch(/type SessionMinValues \{[^}]*startedAt: DateTime/);
    expect(sdl).toMatch(/type SessionMaxValues \{[^}]*label: String/);
    expect(sdl).not.toMatch(/type SessionSumValues \{[^}]*startedAt/);
    expect(sdl).not.toMatch(/type SessionAvgValues \{[^}]*label/);
  });

  it('resolves a DateTime max through the aggregate query', async () => {
    const startedAt = new Date('2026-07-18T16:44:09.000Z');
    const delegate = fakeDelegate({ _max: { startedAt, label: 'zulu' } });
    const schema = buildGolemSchema({
      datamodel: sessionDatamodel,
      client: { session: delegate },
      models: sessionEnabled,
    });

    const result = await graphql({
      schema,
      source: '{ sessionsAggregate(measures: { max: [startedAt, label] }) { max { startedAt label } } }',
    });

    expect(result.errors).toBeUndefined();
    expect(result.data?.sessionsAggregate).toEqual({
      max: { startedAt: startedAt.toISOString(), label: 'zulu' },
    });
    expect(delegate.aggregate).toHaveBeenCalledWith(
      expect.objectContaining({ _max: { startedAt: true, label: true } }),
    );
  });

  it('respects the measures allowlist for orderables', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel: sessionDatamodel,
        client: { session: fakeDelegate() },
        models: { Session: { aggregations: { measures: ['duration', 'startedAt'] } } },
      }),
    );
    const orderableFields = /enum SessionOrderableField \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    expect(orderableFields).toContain('startedAt');
    expect(orderableFields).not.toContain('label');
  });

  it('translates a String min filter into a prisma having clause', async () => {
    const delegate = fakeDelegate();
    const schema = buildGolemSchema({
      datamodel: sessionDatamodel,
      client: { session: delegate },
      models: sessionEnabled,
    });

    const result = await graphql({
      schema,
      source: `{
        sessionsGrouped(
          by: [active]
          measures: { min: [label] }
          having: { min: { label: { contains: "x" } } }
        ) { count }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(delegate.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({ having: { label: { _min: { contains: 'x' } } } }),
    );
  });

  it('translates a DateTime max filter into a prisma having clause', async () => {
    const delegate = fakeDelegate();
    const schema = buildGolemSchema({
      datamodel: sessionDatamodel,
      client: { session: delegate },
      models: sessionEnabled,
    });

    const result = await graphql({
      schema,
      source: `{
        sessionsGrouped(
          by: [active]
          measures: { max: [startedAt] }
          having: { max: { startedAt: { gte: "2026-07-18T16:44:09.000Z" } } }
        ) { count }
      }`,
    });

    expect(result.errors).toBeUndefined();
    expect(delegate.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({
        having: {
          startedAt: { _max: { gte: new Date('2026-07-18T16:44:09.000Z') } },
        },
      }),
    );
  });

  it('orders grouped rows by a DateTime min and a String max', async () => {
    const delegate = fakeDelegate();
    const schema = buildGolemSchema({
      datamodel: sessionDatamodel,
      client: { session: delegate },
      models: sessionEnabled,
    });

    const byDate = await graphql({
      schema,
      source: `{
        sessionsGrouped(by: [active], measures: { min: [startedAt] }, orderBy: { min: { startedAt: desc } }) { count }
      }`,
    });
    const byLabel = await graphql({
      schema,
      source: `{
        sessionsGrouped(by: [active], measures: { max: [label] }, orderBy: { max: { label: asc } }) { count }
      }`,
    });

    expect(byDate.errors).toBeUndefined();
    expect(byLabel.errors).toBeUndefined();
    expect(delegate.groupBy).toHaveBeenNthCalledWith(
      1,
      expect.objectContaining({ orderBy: [{ _min: { startedAt: 'desc' } }] }),
    );
    expect(delegate.groupBy).toHaveBeenNthCalledWith(
      2,
      expect.objectContaining({ orderBy: [{ _max: { label: 'asc' } }] }),
    );
  });

  it('offers min and max but no sum or avg to a model without numeric columns', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel: noteDatamodel,
        client: { note: fakeDelegate() },
        models: { Note: { aggregations: true } },
      }),
    );

    const measuresInput = /input NoteMeasuresInput \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    expect(measuresInput).toContain('min: [NoteOrderableField!]');
    expect(measuresInput).toContain('max: [NoteOrderableField!]');
    expect(measuresInput).not.toContain('sum:');
    expect(measuresInput).not.toContain('avg:');

    expect(sdl).not.toContain('NoteSumValues');
    expect(sdl).not.toContain('NoteAvgValues');
    expect(sdl).not.toContain('NoteMeasureField');
    expect(sdl).toMatch(/type NoteMinValues \{[^}]*writtenAt: DateTime/);
    expect(sdl).toMatch(/type NoteMaxValues \{[^}]*body: String/);
  });

  it('breaks a measure-typed variable in a min position while keeping sum and literals valid', () => {
    const schema = buildGolemSchema({
      datamodel: sessionDatamodel,
      client: { session: fakeDelegate() },
      models: sessionEnabled,
    });
    const errorsFor = (source: string) =>
      validate(schema, parse(source)).map((error) => error.message);

    expect(
      errorsFor(
        'query ($f: [SessionMeasureField!]) { sessionsAggregate(measures: { min: $f }) { count } }',
      ),
    ).toEqual([
      'Variable "$f" of type "[SessionMeasureField!]" used in position expecting type "[SessionOrderableField!]".',
    ]);
    expect(
      errorsFor(
        'query ($f: [SessionMeasureField!]) { sessionsAggregate(measures: { sum: $f }) { count } }',
      ),
    ).toEqual([]);
    expect(
      errorsFor('{ sessionsAggregate(measures: { min: [duration] }) { count } }'),
    ).toEqual([]);
    expect(
      errorsFor(
        'query ($f: [SessionOrderableField!]) { sessionsAggregate(measures: { min: $f }) { count } }',
      ),
    ).toEqual([]);
  });

  it('emits no orderable enum when it would duplicate the measure enum', () => {
    const sdl = printSchema(
      buildGolemSchema({
        datamodel: counterDatamodel,
        client: { counter: fakeDelegate() },
        models: { Counter: { aggregations: true } },
      }),
    );
    expect(sdl).toContain('enum CounterMeasureField');
    expect(sdl).not.toContain('CounterOrderableField');
    expect(sdl).not.toContain('CounterOrderableOrderInput');

    const measuresInput = /input CounterMeasuresInput \{([^}]*)\}/.exec(sdl)?.[1] ?? '';
    expect(measuresInput).toContain('min: [CounterMeasureField!]');
  });
});
