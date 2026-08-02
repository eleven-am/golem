import { AuthorizationProvider, FieldClassification } from './authorization';
import { CompiledReadEvent } from './compiled-read';
import { DatamodelModel } from './datamodel';
import { GolemEngine } from './operations';
import { field } from './testing';

const user: DatamodelModel = {
  name: 'User',
  dbName: 'users',
  fields: [
    field({ name: 'id', dbName: 'user_id', type: 'String', isId: true }),
    field({ name: 'email', dbName: 'email', type: 'String', isUnique: true }),
    field({ name: 'phone', dbName: 'phone', type: 'String', isRequired: false }),
  ],
};

const models = [user];
const ctx = { req: {} };

const classification: Record<string, FieldClassification> = {
  phone: { access: 'conditional', requires: ['id'] },
};

function baseProvider(): AuthorizationProvider {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => ({})),
    check: jest.fn(async () => true),
    checkField: jest.fn(async (_a, _m, row: unknown, f: string) =>
      f !== 'phone' || (row as Record<string, unknown>).id === 'me',
    ),
    classifyFields: jest.fn(async (_a, _m, fields: readonly string[]) =>
      Object.fromEntries(fields.map((name) => [name, classification[name] ?? { access: 'always' }])),
    ),
  } as never;
}

function rows(): Record<string, unknown>[] {
  return [
    { id: 'me', email: 'a@b.c', phone: '111' },
    { id: 'other', email: 'x@y.z', phone: '222' },
  ];
}

async function readWith(provider: AuthorizationProvider): Promise<unknown> {
  const findMany = jest.fn().mockResolvedValue(rows());
  const engine = new GolemEngine({ user: { findMany } }, models, {
    authorization: provider,
    checkReadFields: true,
  });
  const result = await engine.findMany({
    model: 'User',
    select: { email: true, phone: true },
    context: ctx,
  });
  return { result, select: findMany.mock.calls[0][0].select };
}

interface CompiledRead {
  result: unknown;
  sql: string;
  parameters: readonly unknown[];
  event: CompiledReadEvent;
}

async function readCompiled(
  provider: AuthorizationProvider,
  answer: Record<string, unknown>[],
): Promise<CompiledRead> {
  const queryRaw = jest.fn().mockResolvedValue(answer);
  const engine = new GolemEngine(
    { user: { findMany: jest.fn() }, $queryRawUnsafe: queryRaw },
    models,
    { authorization: provider, checkReadFields: true, provider: 'sqlite' },
  );
  const events: CompiledReadEvent[] = [];
  engine.observeCompiledRead((event) => events.push(event));
  const result = await engine.findMany({
    model: 'User',
    select: { email: true, phone: true },
    context: ctx,
    compiled: true,
  });
  const [sql, ...parameters] = queryRaw.mock.calls[0] as [string, ...unknown[]];
  return { result, sql, parameters, event: events[0]! };
}

describe('constrainField on AuthorizationProvider', () => {
  it('leaves a read Prisma answers byte-for-byte identical, and still masks it in memory', async () => {
    const constrainField = jest.fn(async () => ({ id: 'me' }));
    const withField: AuthorizationProvider = { ...baseProvider(), constrainField };
    const without = baseProvider();

    const answered = await readWith(withField);
    const baseline = await readWith(without);

    expect(answered).toEqual(baseline);
    expect(answered).toEqual({
      result: [
        { email: 'a@b.c', phone: '111' },
        { email: 'x@y.z', phone: null },
      ],
      select: { email: true, phone: true, id: true },
    });
    expect(constrainField).toHaveBeenCalledWith('read', 'User', 'phone', ctx);
    expect(withField.checkField).toHaveBeenCalledTimes(2);
  });

  it('masks the column in the statement and stops checking the field row by row', async () => {
    const constrainField = jest.fn(async () => ({ id: 'me' }));
    const provider: AuthorizationProvider = { ...baseProvider(), constrainField };
    const answered = await readCompiled(provider, [
      { email: 'a@b.c', phone: '111' },
      { email: 'x@y.z', phone: null },
    ]);

    expect(answered.result).toEqual([
      { email: 'a@b.c', phone: '111' },
      { email: 'x@y.z', phone: null },
    ]);
    expect(answered.sql).toBe(
      'select "t0"."email" as "email", ' +
        'case when (("t0"."user_id" COLLATE BINARY IS ?)) then "t0"."phone" else null end as "phone" ' +
        'from "users" as "t0" where ((1 = 1))',
    );
    expect(answered.parameters).toEqual(['me']);
    expect(provider.checkField).not.toHaveBeenCalled();
    expect(answered.event.masked).toEqual(['phone']);
    expect(answered.event.deferred).toEqual([]);
  });

  it('asks the database for no column the caller did not', async () => {
    const provider: AuthorizationProvider = {
      ...baseProvider(),
      constrainField: jest.fn(async () => ({ id: 'me' })),
    };
    const answered = await readCompiled(provider, []);

    expect(answered.sql).not.toContain('as "id"');
  });

  it('keeps checking a field row by row when it hands over no condition', async () => {
    const provider = baseProvider();
    const answered = await readCompiled(provider, [
      { email: 'a@b.c', phone: '111', id: 'me' },
      { email: 'x@y.z', phone: '222', id: 'other' },
    ]);

    expect(answered.result).toEqual([
      { email: 'a@b.c', phone: '111' },
      { email: 'x@y.z', phone: null },
    ]);
    expect(answered.sql).toContain('as "id"');
    expect(answered.sql).not.toContain('case when');
    expect(provider.checkField).toHaveBeenCalledTimes(2);
    expect(answered.event.masked).toEqual([]);
    expect(answered.event.deferred).toEqual([
      {
        path: 'the read',
        field: 'phone',
        reason: 'unconstrained',
        detail:
          'User.phone is masked by an authorization provider that hands golem no condition for the field, ' +
          'and an absent condition must not be read as a grant',
      },
    ]);
  });

  it('accepts a provider that omits constrainField', () => {
    const minimal: AuthorizationProvider = {
      authorize: async () => undefined,
      constrain: async () => ({}),
    };
    expect(minimal.constrainField).toBeUndefined();
  });
});
