import { AuthorizationProvider, FieldClassification } from './authorization';
import { CompiledReadEvent } from './compiled-read';
import { DatamodelModel } from './datamodel';
import { GolemValidationError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const models: readonly DatamodelModel[] = [
  {
    name: 'Metric',
    dbName: 'metrics',
    fields: [
      field({ name: 'id', dbName: 'metric_id', type: 'Int', isId: true }),
      field({ name: 'label', dbName: 'label', type: 'String' }),
      field({ name: 'ownerId', dbName: 'owner_id', type: 'Int' }),
      field({ name: 'secretNote', dbName: 'secret_note', type: 'String' }),
      field({ name: 'rank', dbName: 'rank_value', type: 'Int', isRequired: false }),
    ],
  },
];

const ctx = { caller: 'analyst' };

function client(rows: Record<string, unknown>[]): Record<string, any> {
  return {
    $queryRawUnsafe: jest.fn(async () => rows),
    metric: {
      aggregate: jest.fn(async () => ({ _count: { _all: 0 } })),
      groupBy: jest.fn(async () => []),
    },
  };
}

function provider(
  classification: Record<string, FieldClassification>,
  constraint?: unknown,
): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => constraint),
    checkField: jest.fn(async () => true),
    classifyFields: jest.fn(async (_action, _model, fields: readonly string[]) =>
      Object.fromEntries(
        fields.map((name) => [name, classification[name] ?? { access: 'never' }]),
      ),
    ),
  };
}

function engineWith(
  db: Record<string, any>,
  classification: Record<string, FieldClassification>,
  constraint?: unknown,
): GolemEngine {
  return new GolemEngine(db, models, {
    provider: 'sqlite',
    authorization: provider(classification, constraint),
    checkWriteResults: false,
    checkReadFields: true,
  });
}

describe('the compiled aggregate path under field classification', () => {
  it('refuses a per-field count over a field the caller can never read, before it queries anything', async () => {
    const db = client([]);
    const engine = engineWith(db, { label: { access: 'always' } });
    const events: CompiledReadEvent[] = [];
    engine.observeCompiledRead((event) => events.push(event));

    await expect(
      engine.aggregate({
        model: 'Metric',
        _count: { _all: true, secretNote: true },
        context: ctx,
        compiled: true,
      }),
    ).rejects.toThrow(GolemValidationError);
    await expect(
      engine.aggregate({
        model: 'Metric',
        _count: { _all: true, secretNote: true },
        context: ctx,
        compiled: true,
      }),
    ).rejects.toThrow(/Cannot aggregate field "secretNote" on Metric/);

    expect(db.$queryRawUnsafe).not.toHaveBeenCalled();
    expect(db.metric.aggregate).not.toHaveBeenCalled();
    expect(events).toEqual([]);
  });

  it('refuses a grouping over a field the caller can never read, before it queries anything', async () => {
    const db = client([]);
    const engine = engineWith(db, { ownerId: { access: 'always' } });

    await expect(
      engine.groupBy({
        model: 'Metric',
        by: ['ownerId'],
        _min: { secretNote: true },
        context: ctx,
        compiled: true,
      }),
    ).rejects.toThrow(/Cannot group or aggregate field "secretNote" on Metric/);

    expect(db.$queryRawUnsafe).not.toHaveBeenCalled();
    expect(db.metric.groupBy).not.toHaveBeenCalled();
  });

  it('refuses a field whose readability the query constraint does not discharge', async () => {
    const db = client([]);
    const engine = engineWith(
      db,
      { rank: { access: 'conditional', requires: ['ownerId'], dischargedByConstraint: false } },
      { ownerId: 1 },
    );

    await expect(
      engine.aggregate({ model: 'Metric', _sum: { rank: true }, context: ctx, compiled: true }),
    ).rejects.toThrow(/readability depends on ownerId/);
    expect(db.$queryRawUnsafe).not.toHaveBeenCalled();
  });

  it('compiles a field whose readability the query constraint discharges', async () => {
    const db = client([{ '_sum$rank_value': 6n }]);
    const engine = engineWith(
      db,
      { rank: { access: 'conditional', requires: ['ownerId'], dischargedByConstraint: true } },
      { ownerId: 1 },
    );
    const events: CompiledReadEvent[] = [];
    engine.observeCompiledRead((event) => events.push(event));

    const result = await engine.aggregate({
      model: 'Metric',
      _sum: { rank: true },
      context: ctx,
      compiled: true,
    });

    expect(result).toStrictEqual({ _sum: { rank: 6 } });
    expect(db.$queryRawUnsafe).toHaveBeenCalledTimes(1);
    expect(db.metric.aggregate).not.toHaveBeenCalled();
    expect(events.map((event) => event.outcome)).toEqual(['compiled']);
    expect(events[0]!.sql).toContain('owner_id');
  });
});
