import {
  CompiledReadNestedProjection,
  decodeCompiledRelations,
  decodeDateTime,
  prismaDecimal,
} from './compiled-read-decode';

const metrics: CompiledReadNestedProjection = {
  name: 'metrics',
  list: true,
  fields: [
    { name: 'id', kind: 'plain' },
    { name: 'hits', kind: 'bigint' },
    { name: 'score', kind: 'decimal' },
    { name: 'recordedAt', kind: 'datetime' },
    { name: 'active', kind: 'boolean' },
  ],
  relations: [],
};

const profile: CompiledReadNestedProjection = {
  name: 'profile',
  list: false,
  fields: [{ name: 'bio', kind: 'plain' }],
  relations: [],
};

const decimal = prismaDecimal();

describe('decoding what a JSON aggregate carries back', () => {
  it('parses the aggregate sqlite hands back as a string', () => {
    const rows = [
      {
        metrics:
          '[{"id":1,"hits":"9007199254740993","score":"10.500","recordedAt":"2024-01-01T00:00:00.000+00:00","active":1}]',
      },
    ] as Record<string, unknown>[];

    decodeCompiledRelations(rows, [metrics], decimal);

    const row = (rows[0].metrics as Record<string, unknown>[])[0];
    expect(row.id).toBe(1);
    expect(row.hits).toBe(9007199254740993n);
    expect(String(row.score)).toBe('10.5');
    expect(row.recordedAt).toEqual(new Date('2024-01-01T00:00:00.000Z'));
    expect(row.active).toBe(true);
  });

  it('takes the aggregate postgres hands back already parsed', () => {
    const rows = [
      {
        metrics: [
          {
            id: 2,
            hits: '-9007199254740993',
            score: null,
            recordedAt: '2024-02-02T12:30:45.123',
            active: false,
          },
        ],
      },
    ] as Record<string, unknown>[];

    decodeCompiledRelations(rows, [metrics], decimal);

    const row = (rows[0].metrics as Record<string, unknown>[])[0];
    expect(row.hits).toBe(-9007199254740993n);
    expect(row.score).toBeNull();
    expect((row.recordedAt as Date).toISOString()).toBe('2024-02-02T12:30:45.123Z');
    expect(row.active).toBe(false);
  });

  it('reads a to-one that matched nothing as null and a to-many as an empty array', () => {
    const rows = [{ metrics: '[]', profile: null }] as Record<string, unknown>[];

    decodeCompiledRelations(rows, [metrics, profile], decimal);

    expect(rows[0].metrics).toEqual([]);
    expect(rows[0].profile).toBeNull();
  });

  it('reads a timestamp carrying no zone as the UTC one Prisma stored', () => {
    expect((decodeDateTime('2024-03-03T23:59:59.999') as Date).toISOString()).toBe(
      '2024-03-03T23:59:59.999Z',
    );
    expect((decodeDateTime('2024-03-03 23:59:59.999') as Date).toISOString()).toBe(
      '2024-03-03T23:59:59.999Z',
    );
    expect((decodeDateTime('2024-03-03T23:59:59.999Z') as Date).toISOString()).toBe(
      '2024-03-03T23:59:59.999Z',
    );
    expect((decodeDateTime('2024-03-03T23:59:59.999+00:00') as Date).toISOString()).toBe(
      '2024-03-03T23:59:59.999Z',
    );
    expect((decodeDateTime(1709510399999) as Date).toISOString()).toBe(
      '2024-03-03T23:59:59.999Z',
    );
  });
});
