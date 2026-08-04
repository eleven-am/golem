import { COMPILED_READ_BATCH_CHUNK } from '../../src/compiled-read-run';
import { FindManyRequest, GolemEngine } from '../../src/operations';
import { context, engineFor } from './analytics';
import { OracleRun, expectCompiled, expectIdentical, runBothMany, statements } from './oracle';

export const CHUNKED_OWNERS = 950;

export const SQLITE_CEILING_OWNERS = 33000;

export const POSTGRES_CEILING_OWNERS = 70000;

export const SPARSE_STRIDE = 500;

const METRICS_EACH = 3;

interface UserSeed {
  readonly id: number;
  readonly name: string;
  readonly tenantId: number;
}

interface MetricSeed {
  readonly id: number;
  readonly label: string;
  readonly ownerId: number;
  readonly hits: bigint;
  readonly ratio: number;
  readonly active: boolean;
  readonly recordedAt: Date;
}

export interface ChunkingClient {
  user: { createMany(args: { data: UserSeed[] }): Promise<unknown> };
  metric: { createMany(args: { data: MetricSeed[] }): Promise<unknown> };
}

export interface ChunkingSubject {
  readonly provider: string;
  readonly client: Record<string, any>;
  readonly owners: number;
}

export function sparseOwner(id: number): boolean {
  return id > CHUNKED_OWNERS && id % SPARSE_STRIDE === 0;
}

export function metricsOf(id: number): number[] {
  if (id <= CHUNKED_OWNERS) {
    const first = (id - 1) * METRICS_EACH + 1;
    return [first, first + 1, first + 2];
  }
  if (!sparseOwner(id)) {
    return [];
  }
  return [CHUNKED_OWNERS * METRICS_EACH + id / SPARSE_STRIDE];
}

async function inBatches<T>(
  rows: readonly T[],
  size: number,
  write: (slice: T[]) => Promise<unknown>,
): Promise<void> {
  for (let start = 0; start < rows.length; start += size) {
    await write(rows.slice(start, start + size));
  }
}

export async function seedChunking(client: ChunkingClient, owners: number): Promise<void> {
  const users: UserSeed[] = Array.from({ length: owners }, (_, index) => ({
    id: index + 1,
    name: `owner-${index + 1}`,
    tenantId: (index % 7) + 1,
  }));
  const metrics: MetricSeed[] = [];
  for (const user of users) {
    for (const id of metricsOf(user.id)) {
      metrics.push({
        id,
        label: `m${id}`,
        ownerId: user.id,
        hits: BigInt(id),
        ratio: 1,
        active: id % 2 === 0,
        recordedAt: new Date('2024-01-01T00:00:00.000Z'),
      });
    }
  }
  await inBatches(users, 4000, (slice) => client.user.createMany({ data: slice }));
  await inBatches(metrics, 2000, (slice) => client.metric.createMany({ data: slice }));
}

export function chunkingSuite(subject: () => ChunkingSubject): void {
  const engineOn = (): GolemEngine => engineFor(subject().client, subject().provider, {});

  const agree = async (
    request: Omit<FindManyRequest, 'context' | 'compiled'>,
  ): Promise<OracleRun> => {
    const run = await runBothMany(engineOn(), request);
    expectCompiled(run);
    expectIdentical(run);
    return run;
  };

  const chunks = (owners: number): number => Math.ceil(owners / COMPILED_READ_BATCH_CHUNK);

  it('answers a batched relation over more parents than one chunk exactly as Prisma answers it', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, metrics: { select: { id: true }, orderBy: { id: 'asc' } } },
      where: { id: { lte: CHUNKED_OWNERS } },
      orderBy: [{ id: 'asc' }],
    });

    expect(statements(run)).toHaveLength(1 + chunks(CHUNKED_OWNERS));
    const rows = run.compiled as { id: number; metrics: { id: number }[] }[];
    expect(rows).toHaveLength(CHUNKED_OWNERS);
    expect(rows.every((row) => row.metrics.length === METRICS_EACH)).toBe(true);
    expect(rows[COMPILED_READ_BATCH_CHUNK]!.metrics.map((metric) => metric.id)).toEqual(
      metricsOf(COMPILED_READ_BATCH_CHUNK + 1),
    );
    expect(rows[rows.length - 1]!.metrics.map((metric) => metric.id)).toEqual(
      metricsOf(CHUNKED_OWNERS),
    );
  });

  it('keeps a nested take per parent across a chunk boundary rather than across the batch', async () => {
    const run = await agree({
      model: 'User',
      select: { id: true, metrics: { select: { id: true }, take: 2, orderBy: { id: 'asc' } } },
      where: { id: { lte: CHUNKED_OWNERS } },
      orderBy: [{ id: 'asc' }],
    });

    expect(statements(run)).toHaveLength(1 + chunks(CHUNKED_OWNERS));
    const rows = run.compiled as { id: number; metrics: { id: number }[] }[];
    expect(rows.reduce((total, row) => total + row.metrics.length, 0)).toBe(CHUNKED_OWNERS * 2);
    for (const index of [
      0,
      COMPILED_READ_BATCH_CHUNK - 1,
      COMPILED_READ_BATCH_CHUNK,
      rows.length - 1,
    ]) {
      expect(rows[index]!.metrics.map((metric) => metric.id)).toEqual(
        metricsOf(index + 1).slice(0, 2),
      );
    }
  });

  it('skips and reverses a nested page per parent across a chunk boundary', async () => {
    const run = await agree({
      model: 'User',
      select: {
        id: true,
        metrics: { select: { id: true }, skip: 1, take: -2, orderBy: { id: 'asc' } },
      },
      where: { id: { lte: CHUNKED_OWNERS } },
      orderBy: [{ id: 'asc' }],
    });

    const rows = run.compiled as { id: number; metrics: { id: number }[] }[];
    const offsets = (index: number): number[] =>
      rows[index]!.metrics.map((metric) => metric.id - metricsOf(index + 1)[0]!);
    expect(rows.every((row) => row.metrics.length === 2)).toBe(true);
    expect(offsets(COMPILED_READ_BATCH_CHUNK)).toEqual(offsets(0));
    expect(offsets(rows.length - 1)).toEqual(offsets(0));
  });

  it('loads a batched relation over more parents than one statement could ever bind', async () => {
    const owners = subject().owners;
    const engine = engineOn();
    const counts: number[] = [];
    const release = engine.observeCompiledRead((event) => {
      counts.push((event.statements ?? []).length);
    });
    const rows = (await engine.findMany({
      model: 'User',
      select: { id: true, metrics: { select: { id: true }, orderBy: { id: 'asc' } } },
      orderBy: [{ id: 'asc' }],
      context,
      compiled: true,
    })) as { id: number; metrics: { id: number }[] }[];
    release();

    expect(counts).toEqual([1 + chunks(owners)]);
    expect(rows).toHaveLength(owners);
    for (const id of [1, COMPILED_READ_BATCH_CHUNK + 1, CHUNKED_OWNERS, CHUNKED_OWNERS + 1]) {
      expect(rows[id - 1]!.metrics.map((metric) => metric.id)).toEqual(metricsOf(id));
    }
    const sparse = rows.filter((row) => sparseOwner(row.id));
    expect(sparse.length).toBeGreaterThan(chunks(owners));
    expect(sparse.every((row) => row.metrics.length === 1)).toBe(true);
    expect(sparse[sparse.length - 1]!.metrics.map((metric) => metric.id)).toEqual(
      metricsOf(owners),
    );
    expect(rows.reduce((total, row) => total + row.metrics.length, 0)).toBe(
      CHUNKED_OWNERS * METRICS_EACH + sparse.length,
    );
  });
}
