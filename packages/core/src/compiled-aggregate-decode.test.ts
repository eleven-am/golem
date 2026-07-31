import {
  CompiledMeasure,
  decodeAggregateRow,
  unsafeIntegerError,
} from './compiled-aggregate-decode';
import { prismaDecimal } from './compiled-read-decode';

const decimal = prismaDecimal();

function measure(overrides: Partial<CompiledMeasure> & Pick<CompiledMeasure, 'alias' | 'group' | 'decode'>): CompiledMeasure {
  return { field: undefined, ...overrides };
}

describe('decoding a compiled aggregate row', () => {
  it('nests a measure under the group Prisma nests it under, and leaves a scalar count flat', async () => {
    const decoded = decodeAggregateRow(
      { _count$_all: 5n, _sum$hits: '43', owner_id: 1, ownerId: 2 },
      [
        measure({ alias: '_count$_all', group: '_count', decode: 'safeInteger' }),
        measure({ alias: '_sum$hits', group: '_sum', field: 'hits', decode: 'bigint' }),
      ],
      ['ownerId'],
      decimal,
    );
    expect(decoded).toStrictEqual({ ownerId: 2, _count: 5, _sum: { hits: 43n } });
  });

  it('reads a bigint the engine typed as bigint and one it typed as text alike', async () => {
    const slot = measure({ alias: '_sum$hits', group: '_sum', field: 'hits', decode: 'bigint' });
    expect(
      decodeAggregateRow({ _sum$hits: '2767011611056432742143' }, [slot], [], decimal),
    ).toStrictEqual({ _sum: { hits: 2767011611056432742143n } });
    expect(decodeAggregateRow({ _sum$hits: 43n }, [slot], [], decimal)).toStrictEqual({
      _sum: { hits: 43n },
    });
  });

  it('reads an average the engine typed as numeric text back as a JavaScript number', async () => {
    const slot = measure({ alias: '_avg$rank_value', group: '_avg', field: 'rank', decode: 'float' });
    expect(
      decodeAggregateRow({ _avg$rank_value: '2.0000000000000000' }, [slot], [], decimal),
    ).toStrictEqual({ _avg: { rank: 2 } });
    expect(decodeAggregateRow({ _avg$rank_value: 8.6 }, [slot], [], decimal)).toStrictEqual({
      _avg: { rank: 8.6 },
    });
  });

  it('rebuilds a Decimal from the text the engine casts it to, keeping every digit', async () => {
    const slot = measure({ alias: '_sum$score', group: '_sum', field: 'score', decode: 'decimal' });
    const decoded = decodeAggregateRow(
      { _sum$score: '1234567900.623456789012300001000000000000' },
      [slot],
      [],
      decimal,
    ) as { _sum: { score: unknown } };
    expect(String(decoded._sum.score)).toBe('1234567900.623456789012300001');
    expect(decoded._sum.score?.constructor.name).toBe(decimal!.name);
  });

  it('reads a DateTime the engine typed as a naive string as the instant Prisma reads', async () => {
    const slot = measure({
      alias: '_min$recorded_at',
      group: '_min',
      field: 'recordedAt',
      decode: 'datetime',
    });
    const naive = decodeAggregateRow(
      { _min$recorded_at: '2024-05-05 05:05:05.005' },
      [slot],
      [],
      decimal,
    ) as { _min: { recordedAt: Date } };
    expect(naive._min.recordedAt.toISOString()).toBe('2024-05-05T05:05:05.005Z');
    const zoned = decodeAggregateRow(
      { _min$recorded_at: '2024-05-05T05:05:05.005+00:00' },
      [slot],
      [],
      decimal,
    ) as { _min: { recordedAt: Date } };
    expect(zoned._min.recordedAt.toISOString()).toBe('2024-05-05T05:05:05.005Z');
  });

  it('reads an aggregate over no rows as null, whatever it would otherwise decode into', async () => {
    const decoded = decodeAggregateRow(
      { _sum$hits: null, _sum$score: null, _min$label: null, _min$recorded_at: null },
      [
        measure({ alias: '_sum$hits', group: '_sum', field: 'hits', decode: 'bigint' }),
        measure({ alias: '_sum$score', group: '_sum', field: 'score', decode: 'decimal' }),
        measure({ alias: '_min$label', group: '_min', field: 'label', decode: 'string' }),
        measure({ alias: '_min$recorded_at', group: '_min', field: 'recordedAt', decode: 'datetime' }),
      ],
      [],
      decimal,
    );
    expect(decoded).toStrictEqual({
      _sum: { hits: null, score: null },
      _min: { label: null, recordedAt: null },
    });
  });

  it('refuses an integer sum that a JavaScript number cannot hold, the way Prisma refuses it', async () => {
    const slot = measure({
      alias: '_sum$rank_value',
      group: '_sum',
      field: 'rank',
      decode: 'safeInteger',
    });
    const thrown = (() => {
      try {
        decodeAggregateRow({ _sum$rank_value: 9007199254740996n }, [slot], [], decimal);
        return null;
      } catch (error) {
        return error as Error & { code?: string };
      }
    })();
    expect(thrown).not.toBeNull();
    expect(thrown!.name).toBe('PrismaClientKnownRequestError');
    expect(thrown!.code).toBe('P2023');
    expect(thrown!.message).toBe(
      `Integer value in column '_sum$rank_value' is too large to represent as a JavaScript number` +
        ` without loss of precision, got: 9007199254740996. Consider using BigInt type.`,
    );
  });

  it('refuses the same sum when the engine hands it back as numeric text', async () => {
    const slot = measure({
      alias: '_sum$rank_value',
      group: '_sum',
      field: 'rank',
      decode: 'safeInteger',
    });
    expect(() =>
      decodeAggregateRow({ _sum$rank_value: '9007199254740996' }, [slot], [], decimal),
    ).toThrow(/too large to represent as a JavaScript number/);
  });

  it('holds an integer sum right at the edge of what a JavaScript number represents', async () => {
    const slot = measure({ alias: '_sum$rank_value', group: '_sum', field: 'rank', decode: 'safeInteger' });
    expect(
      decodeAggregateRow({ _sum$rank_value: 9007199254740991n }, [slot], [], decimal),
    ).toStrictEqual({ _sum: { rank: 9007199254740991 } });
    expect(
      decodeAggregateRow({ _sum$rank_value: -9007199254740991n }, [slot], [], decimal),
    ).toStrictEqual({ _sum: { rank: -9007199254740991 } });
  });

  it('carries the error Prisma raises, so golem can refuse before it compiles without it', async () => {
    expect(unsafeIntegerError()).not.toBeNull();
    expect(unsafeIntegerError()!('_count$_all', '1').name).toBe('PrismaClientKnownRequestError');
  });
});
