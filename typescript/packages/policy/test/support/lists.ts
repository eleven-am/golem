import { PolicyDatamodel } from '../../src/index';
import { WhereCase } from './matrix';
import { Row } from './measure';
import { BEYOND, D0, D1, SAFE } from './rows';

function scalar(name: string, type: string) {
  return { name, dbName: name, kind: 'scalar' as const, type, isList: false };
}

function list(name: string, type: string) {
  return { name, dbName: name, kind: 'scalar' as const, type, isList: true };
}

export const LIST_DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'ListRow',
      dbName: 'ListRow',
      fields: [
        scalar('id', 'Int'),
        list('tags', 'String'),
        list('scores', 'Int'),
        list('bigs', 'BigInt'),
        list('moments', 'DateTime'),
        list('flags', 'Boolean'),
        scalar('optional', 'String'),
        scalar('ownerId', 'Int'),
        {
          name: 'owner',
          kind: 'object' as const,
          type: 'ListOwner',
          isList: false,
          relationName: 'OwnerToListRow',
          relationFromFields: ['ownerId'],
          relationToFields: ['id'],
        },
      ],
    },
    {
      name: 'ListOwner',
      dbName: 'ListOwner',
      fields: [
        scalar('id', 'Int'),
        scalar('label', 'String'),
        {
          name: 'rows',
          kind: 'object' as const,
          type: 'ListRow',
          isList: true,
          relationName: 'OwnerToListRow',
        },
      ],
    },
  ],
};

export const LIST_DDL: readonly string[] = [
  `DROP TABLE IF EXISTS "ListRow"`,
  `DROP TABLE IF EXISTS "ListOwner"`,
  `CREATE TABLE "ListOwner" (
     "id" INTEGER PRIMARY KEY,
     "label" TEXT
   )`,
  `CREATE TABLE "ListRow" (
     "id" INTEGER PRIMARY KEY,
     "tags" TEXT[],
     "scores" INTEGER[],
     "bigs" BIGINT[],
     "moments" TIMESTAMP(3)[],
     "flags" BOOLEAN[],
     "optional" TEXT,
     "ownerId" INTEGER
   )`,
];

export const LIST_OWNERS: readonly { id: number; label: string }[] = [
  { id: 1, label: 'mixed' },
  { id: 2, label: 'populated' },
  { id: 3, label: 'edges' },
  { id: 4, label: 'empty' },
];

export function listOwnerOf(rowId: number): number {
  if (rowId <= 4) {
    return 1;
  }
  return rowId <= 7 ? 2 : 3;
}

export const LIST_OWNER_INSERTS: readonly string[] = LIST_OWNERS.map(
  (owner) => `INSERT INTO "ListOwner" ("id","label") VALUES (${owner.id}, '${owner.label}')`,
);

const COLUMNS = `("id","tags","scores","bigs","moments","flags","optional","ownerId")`;

const D0_SQL = `'2020-01-01T00:00:00.000'::timestamp(3)`;

const D1_SQL = `'2021-06-15T12:30:00.000'::timestamp(3)`;

export const LIST_INSERTS: readonly string[] = [
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (1, '{}', '{}', '{}', '{}', '{}', 'empty', ${listOwnerOf(1)})`,
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (2, NULL, NULL, NULL, NULL, NULL, 'null column', ${listOwnerOf(2)})`,
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (3, '{a}', '{1}', ARRAY[1::bigint], ARRAY[${D0_SQL}], '{true}', 'single', ${listOwnerOf(3)})`,
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (4, '{a,a,b}', '{1,1,2}', ARRAY[${SAFE}::bigint, ${BEYOND}::bigint], ARRAY[${D0_SQL}, ${D1_SQL}], '{true,false}', 'duplicates', ${listOwnerOf(4)})`,
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (5, '{b,a}', '{2,1}', ARRAY[${BEYOND}::bigint], ARRAY[${D1_SQL}], '{false}', 'reversed', ${listOwnerOf(5)})`,
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (6, '{a,b}', '{1,2}', ARRAY[1::bigint, 2::bigint], ARRAY[${D1_SQL}, ${D0_SQL}], '{false,true}', 'ordered', ${listOwnerOf(6)})`,
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (7, ARRAY['a', NULL]::text[], ARRAY[1, NULL]::int[], ARRAY[1, NULL]::bigint[], ARRAY[${D0_SQL}, NULL], ARRAY[true, NULL]::boolean[], 'null element', ${listOwnerOf(7)})`,
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (8, '{""}', '{0}', ARRAY[0::bigint], '{}', '{}', 'empty string and zero', ${listOwnerOf(8)})`,
  `INSERT INTO "ListRow" ${COLUMNS} VALUES (9, '{Zulu,alpha}', '{-1,100}', ARRAY[-1::bigint], ARRAY[${D1_SQL}], '{true}', 'collation sensitive', ${listOwnerOf(9)})`,
];

export const LIST_ROWS: readonly Row[] = [
  { id: 1, tags: [], scores: [], bigs: [], moments: [], flags: [], optional: 'empty' },
  { id: 2, tags: null, scores: null, bigs: null, moments: null, flags: null, optional: 'null column' },
  { id: 3, tags: ['a'], scores: [1], bigs: [1n], moments: [D0], flags: [true], optional: 'single' },
  {
    id: 4,
    tags: ['a', 'a', 'b'],
    scores: [1, 1, 2],
    bigs: [SAFE, BEYOND],
    moments: [D0, D1],
    flags: [true, false],
    optional: 'duplicates',
  },
  { id: 5, tags: ['b', 'a'], scores: [2, 1], bigs: [BEYOND], moments: [D1], flags: [false], optional: 'reversed' },
  {
    id: 6,
    tags: ['a', 'b'],
    scores: [1, 2],
    bigs: [1n, 2n],
    moments: [D1, D0],
    flags: [false, true],
    optional: 'ordered',
  },
  {
    id: 7,
    tags: ['a', null],
    scores: [1, null],
    bigs: [1n, null],
    moments: [D0, null],
    flags: [true, null],
    optional: 'null element',
  },
  { id: 8, tags: [''], scores: [0], bigs: [0n], moments: [], flags: [], optional: 'empty string and zero' },
  { id: 9, tags: ['Zulu', 'alpha'], scores: [-1, 100], bigs: [-1n], moments: [D1], flags: [true], optional: 'collation sensitive' },
];

export const LIST_OWNER_ROWS: readonly Row[] = LIST_OWNERS.map((owner) => ({
  id: owner.id,
  label: owner.label,
  rows: LIST_ROWS.filter((row) => listOwnerOf(row.id) === owner.id),
}));

export const LIST_OWNER_CASES: readonly WhereCase[] = [
  { id: 'owner/some-has-a', where: { rows: { some: { tags: { has: 'a' } } } } },
  { id: 'owner/some-has-zulu', where: { rows: { some: { tags: { has: 'Zulu' } } } } },
  { id: 'owner/some-has-empty-string', where: { rows: { some: { tags: { has: '' } } } } },
  { id: 'owner/some-isEmpty-true', where: { rows: { some: { tags: { isEmpty: true } } } } },
  { id: 'owner/some-hasEvery-a-b', where: { rows: { some: { tags: { hasEvery: ['a', 'b'] } } } } },
  { id: 'owner/some-hasEvery-none', where: { rows: { some: { tags: { hasEvery: [] } } } } },
  { id: 'owner/some-hasSome-none', where: { rows: { some: { tags: { hasSome: [] } } } } },
  { id: 'owner/some-equals-a-b', where: { rows: { some: { tags: { equals: ['a', 'b'] } } } } },
  { id: 'owner/some-equals-null', where: { rows: { some: { tags: { equals: null } } } } },
  { id: 'owner/some-scores-has-0', where: { rows: { some: { scores: { has: 0 } } } } },
  { id: 'owner/some-bigs-has-beyond', where: { rows: { some: { bigs: { has: BEYOND } } } } },
  { id: 'owner/some-moments-hasEvery-d0-d1', where: { rows: { some: { moments: { hasEvery: [D0, D1] } } } } },
  { id: 'owner/every-isEmpty-false', where: { rows: { every: { tags: { isEmpty: false } } } } },
  { id: 'owner/every-has-a', where: { rows: { every: { tags: { has: 'a' } } } } },
  { id: 'owner/none-has-b', where: { rows: { none: { tags: { has: 'b' } } } } },
  { id: 'owner/none-hasSome-a-b', where: { rows: { none: { tags: { hasSome: ['a', 'b'] } } } } },
  {
    id: 'owner/label-and-some-has-a',
    where: { AND: [{ label: 'populated' }, { rows: { some: { tags: { has: 'a' } } } }] },
  },
  {
    id: 'owner/not-some-has-a',
    where: { NOT: { rows: { some: { tags: { has: 'a' } } } } },
  },
  {
    id: 'owner/some-has-a-and-flags-has-false',
    where: { rows: { some: { AND: [{ tags: { has: 'a' } }, { flags: { has: false } }] } } },
  },
];

const TAG_FILTERS: readonly [string, Record<string, unknown>][] = [
  ['has-a', { has: 'a' }],
  ['has-b', { has: 'b' }],
  ['has-missing', { has: 'z' }],
  ['has-empty-string', { has: '' }],
  ['has-upper', { has: 'Zulu' }],
  ['has-null', { has: null }],
  ['hasEvery-none', { hasEvery: [] }],
  ['hasEvery-a', { hasEvery: ['a'] }],
  ['hasEvery-a-b', { hasEvery: ['a', 'b'] }],
  ['hasEvery-a-a', { hasEvery: ['a', 'a'] }],
  ['hasEvery-a-missing', { hasEvery: ['a', 'z'] }],
  ['hasSome-none', { hasSome: [] }],
  ['hasSome-a', { hasSome: ['a'] }],
  ['hasSome-a-b', { hasSome: ['a', 'b'] }],
  ['hasSome-missing', { hasSome: ['z'] }],
  ['hasSome-missing-empty-string', { hasSome: ['z', ''] }],
  ['isEmpty-true', { isEmpty: true }],
  ['isEmpty-false', { isEmpty: false }],
  ['equals-none', { equals: [] }],
  ['equals-a', { equals: ['a'] }],
  ['equals-a-b', { equals: ['a', 'b'] }],
  ['equals-b-a', { equals: ['b', 'a'] }],
  ['equals-a-a-b', { equals: ['a', 'a', 'b'] }],
  ['equals-null', { equals: null }],
  ['equals-empty-string', { equals: [''] }],
];

const SCORE_FILTERS: readonly [string, Record<string, unknown>][] = [
  ['has-1', { has: 1 }],
  ['has-0', { has: 0 }],
  ['has-missing', { has: 42 }],
  ['hasEvery-none', { hasEvery: [] }],
  ['hasEvery-1-2', { hasEvery: [1, 2] }],
  ['hasSome-none', { hasSome: [] }],
  ['hasSome-1-42', { hasSome: [1, 42] }],
  ['isEmpty-true', { isEmpty: true }],
  ['isEmpty-false', { isEmpty: false }],
  ['equals-none', { equals: [] }],
  ['equals-1-2', { equals: [1, 2] }],
  ['equals-2-1', { equals: [2, 1] }],
  ['equals-null', { equals: null }],
];

const BIG_FILTERS: readonly [string, Record<string, unknown>][] = [
  ['has-1', { has: 1n }],
  ['has-beyond', { has: BEYOND }],
  ['has-safe', { has: SAFE }],
  ['hasSome-none', { hasSome: [] }],
  ['hasSome-safe-beyond', { hasSome: [SAFE, BEYOND] }],
  ['hasEvery-none', { hasEvery: [] }],
  ['hasEvery-safe-beyond', { hasEvery: [SAFE, BEYOND] }],
  ['isEmpty-true', { isEmpty: true }],
  ['equals-1-2', { equals: [1n, 2n] }],
  ['equals-null', { equals: null }],
];

const MOMENT_FILTERS: readonly [string, Record<string, unknown>][] = [
  ['has-d0', { has: D0 }],
  ['has-d1', { has: D1 }],
  ['hasSome-none', { hasSome: [] }],
  ['hasEvery-d0-d1', { hasEvery: [D0, D1] }],
  ['isEmpty-true', { isEmpty: true }],
  ['isEmpty-false', { isEmpty: false }],
  ['equals-d0-d1', { equals: [D0, D1] }],
  ['equals-null', { equals: null }],
];

const FLAG_FILTERS: readonly [string, Record<string, unknown>][] = [
  ['has-true', { has: true }],
  ['has-false', { has: false }],
  ['hasSome-none', { hasSome: [] }],
  ['hasEvery-true-false', { hasEvery: [true, false] }],
  ['isEmpty-true', { isEmpty: true }],
  ['equals-true-false', { equals: [true, false] }],
  ['equals-null', { equals: null }],
];

function leaves(): WhereCase[] {
  const cases: WhereCase[] = [];
  for (const [id, filter] of TAG_FILTERS) {
    cases.push({ id: `tags.${id}`, where: { tags: filter } });
  }
  for (const [id, filter] of SCORE_FILTERS) {
    cases.push({ id: `scores.${id}`, where: { scores: filter } });
  }
  for (const [id, filter] of BIG_FILTERS) {
    cases.push({ id: `bigs.${id}`, where: { bigs: filter } });
  }
  for (const [id, filter] of MOMENT_FILTERS) {
    cases.push({ id: `moments.${id}`, where: { moments: filter } });
  }
  for (const [id, filter] of FLAG_FILTERS) {
    cases.push({ id: `flags.${id}`, where: { flags: filter } });
  }
  return cases;
}

export const LIST_LEAF_CASES: readonly WhereCase[] = leaves();

export const LIST_NULL_COLUMN_CASES: readonly string[] = LIST_LEAF_CASES.filter((entry) =>
  entry.id.endsWith('.equals-null'),
).flatMap((entry) => [entry.id, `NOT.${entry.id}`]);

export const LIST_NULL_COLUMN_ANSWERS: Readonly<Record<string, string>> = Object.fromEntries(
  LIST_NULL_COLUMN_CASES.map((id) => [id, id.startsWith('NOT.') ? '1,3,4,5,6,7,8,9' : '2']),
);

export const LIST_CASES: readonly WhereCase[] = [
  ...LIST_LEAF_CASES,
  ...LIST_LEAF_CASES.map((entry) => ({ id: `NOT.${entry.id}`, where: { NOT: entry.where } })),
  {
    id: 'and.has-a.isEmpty-false',
    where: { AND: [{ tags: { has: 'a' } }, { tags: { isEmpty: false } }] },
  },
  {
    id: 'and.hasSome-none.has-a',
    where: { AND: [{ tags: { hasSome: [] } }, { tags: { has: 'a' } }] },
  },
  {
    id: 'or.hasSome-none.has-a',
    where: { OR: [{ tags: { hasSome: [] } }, { tags: { has: 'a' } }] },
  },
  {
    id: 'or.hasEvery-none.has-missing',
    where: { OR: [{ tags: { hasEvery: [] } }, { tags: { has: 'z' } }] },
  },
  {
    id: 'and.tags-has-a.scores-has-1',
    where: { AND: [{ tags: { has: 'a' } }, { scores: { has: 1 } }] },
  },
  {
    id: 'and.equals-a-b.scores-equals-1-2',
    where: { AND: [{ tags: { equals: ['a', 'b'] } }, { scores: { equals: [1, 2] } }] },
  },
  {
    id: 'mixed.isEmpty-true.optional',
    where: { AND: [{ tags: { isEmpty: true } }, { optional: 'empty' }] },
  },
  {
    id: 'mixed.has-a.optional-not-null',
    where: { AND: [{ tags: { has: 'a' } }, { optional: { not: null } }] },
  },
];
