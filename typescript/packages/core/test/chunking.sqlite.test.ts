import { DatamodelModel } from '../src/datamodel';
import { GolemEngine } from '../src/operations';
import { context } from './support/analytics';
import { SQLITE_CEILING_OWNERS, chunkingSuite, seedChunking } from './support/chunking';
import { scopedModels } from './support/fixture';
import { SqliteHandle, openSqlite } from './support/sqlite';

jest.setTimeout(600000);

const TENANTS = 7;

const OWNERS = 40;

const sharedKeyModels: readonly DatamodelModel[] = scopedModels.map((model) =>
  model.name !== 'Metric'
    ? model
    : {
        ...model,
        fields: model.fields.map((field) =>
          field.name !== 'owner' ? field : { ...field, relationToFields: ['tenantId'] },
        ),
      },
);

describe('a batched relation over more parents than one statement can bind, on sqlite', () => {
  let handle: SqliteHandle;

  beforeAll(async () => {
    handle = await openSqlite();
    await seedChunking(handle.prisma as never, SQLITE_CEILING_OWNERS);
  });

  afterAll(async () => {
    await handle.close();
  });

  chunkingSuite(() => ({
    provider: 'sqlite',
    client: handle.prisma as unknown as Record<string, any>,
    owners: SQLITE_CEILING_OWNERS,
  }));
});

describe('a batched relation whose parents share the value it correlates on', () => {
  let handle: SqliteHandle;

  beforeAll(async () => {
    handle = await openSqlite();
    await handle.prisma.user.createMany({
      data: Array.from({ length: OWNERS }, (_, index) => ({
        id: index + 1,
        name: `owner-${index + 1}`,
        tenantId: (index % TENANTS) + 1,
      })),
    });
    await handle.prisma.metric.createMany({
      data: Array.from({ length: TENANTS * 2 }, (_, index) => ({
        id: index + 1,
        label: `m${index + 1}`,
        ownerId: (index % TENANTS) + 1,
        hits: BigInt(index + 1),
        ratio: 1,
        active: true,
        recordedAt: new Date('2024-01-01T00:00:00.000Z'),
      })),
    });
  });

  afterAll(async () => {
    await handle.close();
  });

  it('binds each distinct value once and still answers every parent that holds it', async () => {
    const engine = new GolemEngine(
      handle.prisma as unknown as Record<string, any>,
      sharedKeyModels,
      { provider: 'sqlite', checkWriteResults: false, checkReadFields: false },
    );
    const statements: string[] = [];
    const release = engine.observeCompiledRead((event) => {
      statements.push(...(event.statements ?? []));
    });
    const rows = (await engine.findMany({
      model: 'User',
      select: { id: true, tenantId: true, metrics: { select: { id: true }, orderBy: { id: 'asc' } } },
      orderBy: [{ id: 'asc' }],
      context,
      compiled: true,
    })) as { id: number; tenantId: number; metrics: { id: number }[] }[];
    release();

    expect(rows).toHaveLength(OWNERS);
    for (const row of rows) {
      expect(row.metrics.map((metric) => metric.id)).toEqual([row.tenantId, row.tenantId + TENANTS]);
    }

    expect(statements).toHaveLength(2);
    const bound = statements[1]!.split('?').length - 1;
    expect(bound).toBe(TENANTS);
    expect(bound).toBeLessThan(OWNERS);
  });
});
