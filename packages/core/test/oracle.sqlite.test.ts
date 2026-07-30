import { CompiledReadEvent } from '../src/compiled-read';
import { HookRegistry } from '../src/hooks';
import { GolemEngine } from '../src/operations';
import { scopedModels } from './support/fixture';
import { context, seed, seedMetrics } from './support/analytics';
import { oracleSuite } from './support/oracle';
import { SqliteHandle, openSqlite } from './support/sqlite';

describe('the compiled read path against a live sqlite database', () => {
  let handle: SqliteHandle;

  beforeAll(async () => {
    handle = await openSqlite();
    await seed(handle.prisma, () => '?');
    await seedMetrics(handle.prisma);
  });

  afterAll(async () => {
    await handle.close();
  });

  oracleSuite(() => ({
    provider: 'sqlite',
    client: handle.prisma as unknown as Record<string, any>,
  }));

  it('still compiles when a before hook hands back a request it rebuilt from scratch', async () => {
    const hooks = new HookRegistry();
    hooks.registerBefore('Metric', 'findMany', (request) => ({
      model: (request as { model: string }).model,
      select: { id: true },
      orderBy: [{ id: 'desc' }],
      context: (request as { context?: unknown }).context,
    }));
    const engine = new GolemEngine(
      handle.prisma as unknown as Record<string, any>,
      scopedModels,
      { provider: 'sqlite', hooks, checkWriteResults: false, checkReadFields: false },
    );
    const events: CompiledReadEvent[] = [];
    engine.observeCompiledRead((event) => events.push(event));

    const rows = await engine.findMany({
      model: 'Metric',
      select: { id: true, label: true },
      orderBy: [{ id: 'asc' }],
      context,
      compiled: true,
    });

    expect(events.map((event) => event.outcome)).toEqual(['compiled']);
    expect(rows).toEqual([{ id: 5 }, { id: 4 }, { id: 3 }, { id: 2 }, { id: 1 }]);
  });
});
