import { graphql, printSchema } from 'graphql';
import type { AuthorizationProvider, FieldClassification } from './authorization';
import type { DatamodelModel } from './datamodel';
import { GolemValidationError } from './errors';
import { GolemEngine } from './operations';
import {
  buildRelationAggregationPlan,
  DEFAULT_MAX_INTERMEDIATE_GROUPS,
} from './relation-aggregation';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const models: DatamodelModel[] = [
  {
    name: 'Play',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'albumId', type: 'String' }),
      field({ name: 'trackId', type: 'String' }),
      field({ name: 'msPlayed', type: 'Int', isRequired: false }),
      field({
        name: 'track',
        type: 'Track',
        kind: 'object',
        relationFromFields: ['trackId'],
        relationToFields: ['id'],
      }),
    ],
  },
  {
    name: 'Track',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'primaryArtistId', type: 'String' }),
      field({
        name: 'primaryArtist',
        type: 'Artist',
        kind: 'object',
        relationFromFields: ['primaryArtistId'],
        relationToFields: ['id'],
      }),
    ],
  },
  {
    name: 'Artist',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'country', type: 'String', isRequired: false }),
    ],
  },
];

const config = {
  dimensions: ['albumId'],
  relationDimensions: {
    artistCountry: { path: ['track', 'primaryArtist'], field: 'country' },
  },
  measures: ['msPlayed'],
  maxIntermediateGroups: 10,
  maxGroups: 3,
} as const;

function plan() {
  return buildRelationAggregationPlan(
    models[0]!,
    config,
    new Map(models.map((model) => [model.name, model])),
  )!;
}

function client(rootRows?: Record<string, unknown>[]) {
  return {
    play: {
      groupBy: jest.fn().mockResolvedValue(rootRows ?? [
        {
          albumId: 'album-1',
          trackId: 'track-1',
          _count: { _all: 2, msPlayed: 2 },
          _sum: { msPlayed: 30 },
          _min: { msPlayed: 10 },
          _max: { msPlayed: 20 },
        },
        {
          albumId: 'album-1',
          trackId: 'track-2',
          _count: { _all: 1, msPlayed: 1 },
          _sum: { msPlayed: 90 },
          _min: { msPlayed: 90 },
          _max: { msPlayed: 90 },
        },
        {
          albumId: 'album-2',
          trackId: 'missing',
          _count: { _all: 5, msPlayed: 5 },
          _sum: { msPlayed: 500 },
          _min: { msPlayed: 100 },
          _max: { msPlayed: 100 },
        },
      ]),
    },
    track: {
      findMany: jest.fn().mockResolvedValue([
        { id: 'track-1', primaryArtistId: 'artist-1' },
        { id: 'track-2', primaryArtistId: 'artist-1' },
      ]),
    },
    artist: {
      findMany: jest.fn().mockResolvedValue([
        { id: 'artist-1', country: 'FR' },
      ]),
    },
  };
}

function provider(
  classify: FieldClassification = { access: 'always' },
): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async (_action, model) => ({ visibleOn: model })),
    checkField: jest.fn(async () => true),
    classifyFields: jest.fn(async (_action, _model, fields) =>
      Object.fromEntries(fields.map((name) => [name, classify])),
    ),
  };
}

function engine(
  fake = client(),
  authorization?: AuthorizationProvider,
): GolemEngine {
  return new GolemEngine(fake, models, {
    relationAggregations: new Map([['Play', plan()]]),
    authorization,
    checkReadFields: authorization !== undefined,
    checkWriteResults: false,
  });
}

describe('relation aggregation configuration', () => {
  it('resolves an explicit multi-hop to-one path and bounded defaults', () => {
    const resolved = buildRelationAggregationPlan(
      models[0]!,
      { relationDimensions: config.relationDimensions },
      new Map(models.map((model) => [model.name, model])),
    )!;

    expect(resolved.path.map((step) => step.relation)).toEqual([
      'track',
      'primaryArtist',
    ]);
    expect(resolved.maxIntermediateGroups).toBe(DEFAULT_MAX_INTERMEDIATE_GROUPS);
    expect(resolved.maxGroups).toBe(100);
  });

  it('rejects to-many, reverse-only, and multiple paths', () => {
    const map = new Map(models.map((model) => [model.name, model]));
    const toMany: DatamodelModel = {
      name: 'Root',
      fields: [field({ name: 'tracks', type: 'Track', kind: 'object', isList: true })],
    };
    expect(() => buildRelationAggregationPlan(toMany, {
      relationDimensions: { bad: { path: ['tracks'], field: 'id' } },
    }, new Map([...map, ['Root', toMany]]))).toThrow(/to-many/);

    const reverse: DatamodelModel = {
      name: 'Root',
      fields: [field({ name: 'track', type: 'Track', kind: 'object' })],
    };
    expect(() => buildRelationAggregationPlan(reverse, {
      relationDimensions: { bad: { path: ['track'], field: 'id' } },
    }, new Map([...map, ['Root', reverse]]))).toThrow(/explicit forward relation keys/);

    expect(() => buildRelationAggregationPlan(models[0]!, {
      relationDimensions: {
        country: config.relationDimensions.artistCountry,
        trackCode: { path: ['track'], field: 'id' },
      },
    }, map)).toThrow(/multiple relation paths/);
  });
});

describe('engine relationGroupBy', () => {
  it('merges terminal groups, reconstructs weighted averages, and drops unreachable roots', async () => {
    const fake = client();
    const rows = await engine(fake).relationGroupBy({
      model: 'Play',
      by: ['albumId', 'artistCountry'],
      _count: true,
      _sum: { msPlayed: true },
      _avg: { msPlayed: true },
      _min: { msPlayed: true },
      _max: { msPlayed: true },
    });

    expect(rows).toEqual([{
      albumId: 'album-1',
      artistCountry: 'FR',
      _count: 3,
      _sum: { msPlayed: 120 },
      _avg: { msPlayed: 40 },
      _min: { msPlayed: 10 },
      _max: { msPlayed: 90 },
    }]);
    expect(fake.play.groupBy).toHaveBeenCalledWith(expect.objectContaining({
      by: ['albumId', 'trackId'],
      take: 11,
    }));
    expect(fake.play.groupBy).toHaveBeenCalledWith(
      expect.not.objectContaining({ having: expect.anything(), skip: expect.anything() }),
    );
  });

  it('enforces row policy at every model and applies having/order/pagination after merge', async () => {
    const fake = client();
    const auth = provider();
    const rows = await engine(fake, auth).relationGroupBy({
      model: 'Play',
      by: ['artistCountry'],
      having: { avg: { msPlayed: { gt: 20 } } },
      orderBy: { avg: { msPlayed: 'desc' } },
      take: 1,
      _count: true,
      context: {},
    }, undefined);

    expect(rows).toEqual([{ artistCountry: 'FR', _count: 3 }]);
    expect(auth.constrain).toHaveBeenCalledWith('read', 'Play', expect.anything());
    expect(auth.constrain).toHaveBeenCalledWith('read', 'Track', expect.anything());
    expect(auth.constrain).toHaveBeenCalledWith('read', 'Artist', expect.anything());
    expect(fake.track.findMany).toHaveBeenCalledWith(expect.objectContaining({
      where: expect.objectContaining({ AND: expect.any(Array) }),
    }));
  });

  it.each([
    ['write-only/never', { access: 'never' } as const],
    ['conditional or inverted', { access: 'conditional', dischargedByConstraint: false } as const],
  ])('fails closed on a %s path field before querying facts', async (_kind, classification) => {
    const fake = client();
    await expect(engine(fake, provider(classification)).relationGroupBy({
      model: 'Play',
      by: ['artistCountry'],
      _count: true,
      context: {},
    })).rejects.toBeInstanceOf(GolemValidationError);
    expect(fake.play.groupBy).not.toHaveBeenCalled();
  });

  it('rejects the complete intermediate set before relation fetch or final pagination', async () => {
    const fake = client(Array.from({ length: 11 }, (_, index) => ({
      trackId: `track-${index}`,
      _count: { _all: 1 },
    })));
    await expect(engine(fake).relationGroupBy({
      model: 'Play',
      by: ['artistCountry'],
      take: 1,
    })).rejects.toThrow(/more than 10 intermediate groups/);
    expect(fake.track.findMany).not.toHaveBeenCalled();
  });

  it('does not alter the existing programmatic groupBy operation', async () => {
    const fake = client([]);
    await engine(fake).groupBy({ model: 'Play', by: ['albumId'], take: 50 });
    expect(fake.play.groupBy).toHaveBeenCalledWith(expect.objectContaining({ take: 50 }));
  });

  it('rejects final ordering by a key that is not part of the grouped result', async () => {
    const fake = client();
    await expect(engine(fake).relationGroupBy({
      model: 'Play',
      by: ['artistCountry'],
      orderBy: { key: { albumId: 'asc' } },
    })).rejects.toThrow(/albumId because it is not in by/);
    expect(fake.play.groupBy).not.toHaveBeenCalled();
  });

  it('rejects an unconfigured measure before querying facts', async () => {
    const fake = client();
    await expect(engine(fake).relationGroupBy({
      model: 'Play',
      by: ['artistCountry'],
      _sum: { secret: true },
    })).rejects.toThrow(/unconfigured measure Play.secret/);
    expect(fake.play.groupBy).not.toHaveBeenCalled();
  });
});

describe('relation aggregation GraphQL surface', () => {
  it('adds a separately named operation with configured relation dimensions', async () => {
    const fake = client([]);
    const schema = buildGolemSchema({
      datamodel: { models, enums: [] },
      client: fake,
      models: { Play: { aggregations: config } },
      defaults: { checkReadFields: false, operations: ['findMany'] },
    });
    const printed = printSchema(schema);
    expect(printed).toContain('playsRelationGrouped');
    expect(printed).toContain('artistCountry');
    expect(printed).toContain('playsGrouped');

    const result = await graphql({
      schema,
      source: `query {
        playsRelationGrouped(by: [artistCountry], measures: { count: true }) {
          key { artistCountry }
          count
        }
      }`,
    });
    expect(result.errors).toBeUndefined();
    expect(result.data).toEqual({ playsRelationGrouped: [] });
  });

  it.each(['hidden', 'writeOnly'] as const)(
    'rejects a %s field required by the relation plan during construction',
    (mode) => {
    expect(() => buildGolemSchema({
      datamodel: { models, enums: [] },
      client: client([]),
      models: {
        Play: { aggregations: config },
        Track: { [mode]: ['primaryArtistId'] },
      },
    })).toThrow(/requires unreadable field Track.primaryArtistId/);
    },
  );
});
