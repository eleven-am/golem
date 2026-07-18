import { AuthorizationProvider, FieldClassification } from './authorization';
import { GolemValidationError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const models = [
  {
    name: 'Play',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'userId', type: 'String' }),
      field({ name: 'trackId', type: 'String' }),
      field({ name: 'msPlayed', type: 'Int' }),
      field({ name: 'secret', type: 'String', isRequired: false }),
    ],
  },
];

const ctx = { req: {} };

function fakeClient(rows: unknown[] = []) {
  return {
    play: {
      groupBy: jest.fn().mockResolvedValue(rows),
      aggregate: jest.fn().mockResolvedValue({}),
      count: jest.fn().mockResolvedValue(0),
    },
  };
}

function constrainProvider(constraint: unknown): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => constraint),
  };
}

function classifyProvider(
  classification: Record<string, FieldClassification>,
): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => undefined),
    checkField: jest.fn(async () => true),
    classifyFields: jest.fn(async (_action, _model, fields: readonly string[]) =>
      Object.fromEntries(
        fields.map((name) => [name, classification[name] ?? { access: 'never' }]),
      ),
    ),
  };
}

const ALWAYS: FieldClassification = { access: 'always' };

function engineWith(
  client: ReturnType<typeof fakeClient>,
  options: Partial<{
    authorization: AuthorizationProvider;
    checkReadFields: boolean;
    groupLimits: Map<string, number>;
  }> = {},
): GolemEngine {
  return new GolemEngine(client, models, {
    authorization: options.authorization,
    checkReadFields: options.checkReadFields ?? false,
    checkWriteResults: false,
    groupLimits: options.groupLimits,
  });
}

describe('engine groupBy', () => {
  it('merges the read constraint into the grouped query', async () => {
    const client = fakeClient();
    const engine = engineWith(client, {
      authorization: constrainProvider({ userId: 'user-1' }),
    });

    await engine.groupBy({
      model: 'Play',
      by: ['trackId'],
      where: { msPlayed: { gt: 0 } },
      _count: true,
      context: ctx,
    });

    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({
        by: ['trackId'],
        where: { AND: [{ msPlayed: { gt: 0 } }, { userId: 'user-1' }] },
        _count: true,
      }),
    );
  });

  it('rejects a grouping key that is not always readable', async () => {
    const engine = engineWith(fakeClient(), {
      checkReadFields: true,
      authorization: classifyProvider({ trackId: ALWAYS }),
    });

    await expect(
      engine.groupBy({
        model: 'Play',
        by: ['trackId', 'secret'],
        _count: true,
        context: ctx,
      }),
    ).rejects.toThrow(GolemValidationError);
  });

  it('rejects a conditionally readable grouping key', async () => {
    const engine = engineWith(fakeClient(), {
      checkReadFields: true,
      authorization: classifyProvider({ trackId: { access: 'conditional' } }),
    });

    await expect(
      engine.groupBy({
        model: 'Play',
        by: ['trackId'],
        _count: true,
        context: ctx,
      }),
    ).rejects.toThrow(/Cannot group or aggregate field "trackId"/);
  });

  it('rejects a measure that is not always readable', async () => {
    const engine = engineWith(fakeClient(), {
      checkReadFields: true,
      authorization: classifyProvider({ trackId: ALWAYS }),
    });

    await expect(
      engine.groupBy({
        model: 'Play',
        by: ['trackId'],
        _sum: { msPlayed: true },
        context: ctx,
      }),
    ).rejects.toThrow(/msPlayed/);
  });

  it('rejects a having filter over a field that is not always readable', async () => {
    const engine = engineWith(fakeClient(), {
      checkReadFields: true,
      authorization: classifyProvider({ trackId: ALWAYS }),
    });

    await expect(
      engine.groupBy({
        model: 'Play',
        by: ['trackId'],
        having: { msPlayed: { _sum: { gt: 10 } } },
        context: ctx,
      }),
    ).rejects.toThrow(/msPlayed/);
  });

  it('rejects ordering by a field that is not always readable', async () => {
    const engine = engineWith(fakeClient(), {
      checkReadFields: true,
      authorization: classifyProvider({ trackId: ALWAYS }),
    });

    await expect(
      engine.groupBy({
        model: 'Play',
        by: ['trackId'],
        orderBy: [{ _sum: { msPlayed: 'desc' } }],
        context: ctx,
      }),
    ).rejects.toThrow(/msPlayed/);
  });

  it('allows grouping and measures over always readable fields', async () => {
    const client = fakeClient([{ trackId: 't1', _count: 3 }]);
    const engine = engineWith(client, {
      checkReadFields: true,
      authorization: classifyProvider({ trackId: ALWAYS, msPlayed: ALWAYS }),
    });

    await expect(
      engine.groupBy({
        model: 'Play',
        by: ['trackId'],
        _sum: { msPlayed: true },
        having: { msPlayed: { _sum: { gt: 10 } } },
        orderBy: [{ _sum: { msPlayed: 'desc' } }],
        context: ctx,
      }),
    ).resolves.toEqual([{ trackId: 't1', _count: 3 }]);
  });

  it('requires at least one grouping key', async () => {
    const engine = engineWith(fakeClient());

    await expect(
      engine.groupBy({ model: 'Play', by: [], context: ctx }),
    ).rejects.toThrow(/requires at least one grouping key/);
  });

  it('rejects an unbounded group query when a group cap is configured', async () => {
    const engine = engineWith(fakeClient(), {
      groupLimits: new Map([['Play', 100]]),
    });

    await expect(
      engine.groupBy({ model: 'Play', by: ['trackId'], context: ctx }),
    ).rejects.toThrow(/requires take of at most 100/);
  });

  it('rejects a group query that exceeds the cap rather than truncating', async () => {
    const client = fakeClient();
    const engine = engineWith(client, {
      groupLimits: new Map([['Play', 100]]),
    });

    await expect(
      engine.groupBy({
        model: 'Play',
        by: ['trackId'],
        take: 500,
        context: ctx,
      }),
    ).rejects.toThrow(/requires take of at most 100/);
    expect(client.play.groupBy).not.toHaveBeenCalled();
  });

  it('accepts a group query within the cap', async () => {
    const client = fakeClient();
    const engine = engineWith(client, {
      groupLimits: new Map([['Play', 100]]),
    });

    await engine.groupBy({
      model: 'Play',
      by: ['trackId'],
      take: 10,
      context: ctx,
    });

    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({ take: 10 }),
    );
  });

  it('exposes groupBy on the scoped transaction view', async () => {
    const client = fakeClient();
    const engine = new GolemEngine(
      {
        ...client,
        $transaction: (run: (tx: unknown) => unknown) => run(client),
      },
      models,
      {},
    );

    await engine.transaction(ctx, (tx) =>
      tx.groupBy({ model: 'Play', by: ['trackId'], _count: true }),
    );

    expect(client.play.groupBy).toHaveBeenCalledWith(
      expect.objectContaining({ by: ['trackId'] }),
    );
  });
});
