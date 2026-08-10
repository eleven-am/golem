import { WhereCase } from './matrix';
import { BELOW, BEYOND, D0, D1, D2, SAFE } from './rows';

export interface RegionSeed {
  code: string;
  name: string;
}

export interface OwnerSeed {
  id: number;
  kind: string;
  rank: number;
  rankNull: number | null;
  regionCode: string | null;
}

export interface TaggedSeed {
  id: number;
  label: string;
  labelNull: string | null;
  amount: number;
  amountNull: number | null;
  big: bigint;
  at: Date;
  flag: boolean;
  flagNull: boolean | null;
  state: string;
  stateNull: string | null;
  ownerId: number | null;
  parentId: number | null;
}

export const REGIONS: readonly RegionSeed[] = [
  { code: 'north', name: 'northern' },
  { code: 'North', name: 'Northern' },
  { code: 'nord', name: '' },
];

export const OWNERS: readonly OwnerSeed[] = [
  { id: 1, kind: 'red', rank: 1, rankNull: 10, regionCode: 'north' },
  { id: 2, kind: '', rank: 0, rankNull: null, regionCode: 'North' },
  { id: 3, kind: 'Zulu', rank: -5, rankNull: 0, regionCode: null },
];

export const TAGGED: readonly TaggedSeed[] = [
  {
    id: 1, label: 'alpha', labelNull: 'alpha', amount: 0, amountNull: 0,
    big: 0n, at: D0, flag: true, flagNull: true, state: 'OPEN', stateNull: 'OPEN', ownerId: 1, parentId: null,
  },
  {
    id: 2, label: '', labelNull: null, amount: -1, amountNull: null,
    big: 1n, at: D1, flag: false, flagNull: null, state: 'CLOSED', stateNull: null, ownerId: 2, parentId: 1,
  },
  {
    id: 3, label: 'beta', labelNull: '', amount: 7, amountNull: 7,
    big: SAFE, at: D2, flag: true, flagNull: false, state: 'ON_HOLD', stateNull: 'CLOSED', ownerId: 3, parentId: 1,
  },
  {
    id: 4, label: 'Zulu', labelNull: 'zulu', amount: 100, amountNull: -1,
    big: BEYOND, at: D0, flag: false, flagNull: null, state: 'OPEN', stateNull: null, ownerId: null, parentId: null,
  },
  {
    id: 5, label: '0', labelNull: null, amount: 0, amountNull: null,
    big: BELOW, at: D1, flag: true, flagNull: true, state: 'CLOSED', stateNull: 'ON_HOLD', ownerId: null, parentId: 2,
  },
  {
    id: 6, label: 'alpha', labelNull: 'omega', amount: 7, amountNull: 0,
    big: 1n, at: D2, flag: false, flagNull: false, state: 'ON_HOLD', stateNull: 'OPEN', ownerId: 1, parentId: 3,
  },
  {
    id: 7, label: 'Ångström', labelNull: null, amount: -1, amountNull: 100,
    big: BEYOND, at: D0, flag: true, flagNull: null, state: 'OPEN', stateNull: null, ownerId: 2, parentId: null,
  },
  {
    id: 8, label: 'delta', labelNull: 'delta', amount: 100, amountNull: 7,
    big: 2n, at: D1, flag: false, flagNull: true, state: 'CLOSED', stateNull: 'CLOSED', ownerId: null, parentId: 5,
  },
];

const OWNER_INCLUDE = { include: { region: true } } as const;

export const MAPPED_INCLUDE = {
  owner: OWNER_INCLUDE,
  parent: {
    include: {
      owner: OWNER_INCLUDE,
      parent: {
        include: {
          owner: OWNER_INCLUDE,
          parent: {
            include: {
              owner: OWNER_INCLUDE,
              parent: { include: { owner: OWNER_INCLUDE } },
            },
          },
        },
      },
    },
  },
} as const;

export const SQLITE_MAPPED_DDL: readonly string[] = [
  `DROP TABLE IF EXISTS "tagged_rows"`,
  `DROP TABLE IF EXISTS "owner_rows"`,
  `DROP TABLE IF EXISTS "region_rows"`,
  `CREATE TABLE "region_rows" (
     "region_code" TEXT PRIMARY KEY,
     "region_name" TEXT NOT NULL
   )`,
  `CREATE TABLE "owner_rows" (
     "owner_pk" INTEGER PRIMARY KEY,
     "kind_text" TEXT NOT NULL,
     "rank_value" INTEGER NOT NULL,
     "rank_null" INTEGER,
     "region_ref" TEXT REFERENCES "region_rows"("region_code")
   )`,
  `CREATE TABLE "tagged_rows" (
     "row_pk" INTEGER PRIMARY KEY,
     "label_text" TEXT NOT NULL,
     "label_null" TEXT,
     "amount_value" INTEGER NOT NULL,
     "amount_null" INTEGER,
     "big_value" BIGINT NOT NULL,
     "at_value" DATETIME NOT NULL,
     "flag_value" BOOLEAN NOT NULL,
     "flag_null" BOOLEAN,
     "state_value" TEXT NOT NULL,
     "state_null" TEXT,
     "owner_ref" INTEGER REFERENCES "owner_rows"("owner_pk"),
     "parent_ref" INTEGER REFERENCES "tagged_rows"("row_pk")
   )`,
];

export const POSTGRES_MAPPED_DDL: readonly string[] = [
  `DROP TABLE IF EXISTS "tagged_rows"`,
  `DROP TABLE IF EXISTS "owner_rows"`,
  `DROP TABLE IF EXISTS "region_rows"`,
  `DROP TYPE IF EXISTS "State"`,
  `CREATE TYPE "State" AS ENUM ('OPEN', 'CLOSED', 'ON_HOLD')`,
  `CREATE TABLE "region_rows" (
     "region_code" TEXT PRIMARY KEY,
     "region_name" TEXT NOT NULL
   )`,
  `CREATE TABLE "owner_rows" (
     "owner_pk" INTEGER PRIMARY KEY,
     "kind_text" TEXT NOT NULL,
     "rank_value" INTEGER NOT NULL,
     "rank_null" INTEGER,
     "region_ref" TEXT REFERENCES "region_rows"("region_code")
   )`,
  `CREATE TABLE "tagged_rows" (
     "row_pk" INTEGER PRIMARY KEY,
     "label_text" TEXT NOT NULL,
     "label_null" TEXT,
     "amount_value" INTEGER NOT NULL,
     "amount_null" INTEGER,
     "big_value" BIGINT NOT NULL,
     "at_value" TIMESTAMP(3) NOT NULL,
     "flag_value" BOOLEAN NOT NULL,
     "flag_null" BOOLEAN,
     "state_value" "State" NOT NULL,
     "state_null" "State",
     "owner_ref" INTEGER REFERENCES "owner_rows"("owner_pk"),
     "parent_ref" INTEGER REFERENCES "tagged_rows"("row_pk")
   )`,
];

const SCALAR_CASES: readonly WhereCase[] = [
  { id: 'label/equals', where: { label: 'alpha' } },
  { id: 'label/equals-empty', where: { label: '' } },
  { id: 'label/equals-zero-text', where: { label: '0' } },
  { id: 'label/not', where: { label: { not: 'alpha' } } },
  { id: 'label/lt-Zulu', where: { label: { lt: 'Zulu' } } },
  { id: 'label/gte-Zulu', where: { label: { gte: 'Zulu' } } },
  { id: 'label/lt-alpha', where: { label: { lt: 'alpha' } } },
  { id: 'label/equals-non-ascii', where: { label: 'Ångström' } },
  { id: 'label/lt-non-ascii', where: { label: { lt: 'Ångström' } } },
  { id: 'label/gte-non-ascii', where: { label: { gte: 'Ångström' } } },
  { id: 'label/in-non-ascii', where: { label: { in: ['Ångström', 'alpha'] } } },
  { id: 'label/in', where: { label: { in: ['alpha', ''] } } },
  { id: 'label/notIn', where: { label: { notIn: ['alpha', ''] } } },
  { id: 'label/in-empty', where: { label: { in: [] } } },
  { id: 'label/notIn-empty', where: { label: { notIn: [] } } },
  { id: 'labelNull/equals-null', where: { labelNull: null } },
  { id: 'labelNull/not-null', where: { labelNull: { not: null } } },
  { id: 'labelNull/equals-empty', where: { labelNull: '' } },
  { id: 'labelNull/not-empty', where: { labelNull: { not: '' } } },
  { id: 'labelNull/lt-zulu', where: { labelNull: { lt: 'zulu' } } },
  { id: 'labelNull/notIn', where: { labelNull: { notIn: ['alpha', 'delta'] } } },
  { id: 'amount/equals-zero', where: { amount: 0 } },
  { id: 'amount/not-zero', where: { amount: { not: 0 } } },
  { id: 'amount/gt-zero', where: { amount: { gt: 0 } } },
  { id: 'amount/lte-zero', where: { amount: { lte: 0 } } },
  { id: 'amount/multi', where: { amount: { gte: 0, lte: 7 } } },
  { id: 'amountNull/equals-null', where: { amountNull: null } },
  { id: 'amountNull/lt-1', where: { amountNull: { lt: 1 } } },
  { id: 'amountNull/notIn', where: { amountNull: { notIn: [0, 7] } } },
  { id: 'big/equals-beyond', where: { big: BEYOND } },
  { id: 'big/gt-safe', where: { big: { gt: SAFE } } },
  { id: 'big/gte-safe', where: { big: { gte: SAFE } } },
  { id: 'big/lt-zero', where: { big: { lt: 0n } } },
  { id: 'big/in-beyond', where: { big: { in: [BEYOND, BELOW] } } },
  { id: 'big/notIn-beyond', where: { big: { notIn: [BEYOND] } } },
  { id: 'at/equals-D0', where: { at: D0 } },
  { id: 'at/lt-D2', where: { at: { lt: D2 } } },
  { id: 'at/gte-D1', where: { at: { gte: D1 } } },
  { id: 'flag/true', where: { flag: true } },
  { id: 'flag/false', where: { flag: false } },
  { id: 'flag/not-true', where: { flag: { not: true } } },
  { id: 'flagNull/true', where: { flagNull: true } },
  { id: 'flagNull/false', where: { flagNull: false } },
  { id: 'flagNull/null', where: { flagNull: null } },
  { id: 'flagNull/not-false', where: { flagNull: { not: false } } },
  { id: 'flagNull/not-null', where: { flagNull: { not: null } } },
  { id: 'state/equals-open', where: { state: 'OPEN' } },
  { id: 'state/not-open', where: { state: { not: 'OPEN' } } },
  { id: 'state/in', where: { state: { in: ['OPEN', 'ON_HOLD'] } } },
  { id: 'state/notIn', where: { state: { notIn: ['OPEN'] } } },
  { id: 'state/lt-ON_HOLD', where: { state: { lt: 'ON_HOLD' } } },
  { id: 'stateNull/null', where: { stateNull: null } },
  { id: 'stateNull/not-null', where: { stateNull: { not: null } } },
  { id: 'stateNull/equals-closed', where: { stateNull: 'CLOSED' } },
  { id: 'ownerId/null', where: { ownerId: null } },
  { id: 'ownerId/not-null', where: { ownerId: { not: null } } },
];

const RELATION_CASES: readonly WhereCase[] = [
  { id: 'owner/is/kind-red', where: { owner: { is: { kind: 'red' } } } },
  { id: 'owner/is/kind-empty', where: { owner: { is: { kind: '' } } } },
  { id: 'owner/is/rank-gt-0', where: { owner: { is: { rank: { gt: 0 } } } } },
  { id: 'owner/is/rankNull-null', where: { owner: { is: { rankNull: null } } } },
  { id: 'owner/is/rankNull-lt-1', where: { owner: { is: { rankNull: { lt: 1 } } } } },
  { id: 'owner/is/empty', where: { owner: { is: {} } } },
  { id: 'owner/is/id-1', where: { owner: { is: { id: 1 } } } },
  { id: 'owner/is/nested-NOT', where: { owner: { is: { NOT: { kind: 'red' } } } } },
  { id: 'owner/is/kind-lt-Zulu', where: { owner: { is: { kind: { lt: 'Zulu' } } } } },
  { id: 'parent/is/label-alpha', where: { parent: { is: { label: 'alpha' } } } },
  { id: 'parent/is/label-empty', where: { parent: { is: { label: '' } } } },
  { id: 'parent/is/id-1', where: { parent: { is: { id: 1 } } } },
  { id: 'parent/is/big-gte-safe', where: { parent: { is: { big: { gte: SAFE } } } } },
  { id: 'parent/is/flag-true', where: { parent: { is: { flag: true } } } },
  { id: 'parent/is/state-open', where: { parent: { is: { state: 'OPEN' } } } },
  { id: 'parent/is/stateNull-null', where: { parent: { is: { stateNull: null } } } },
  { id: 'parent/is/labelNull-null', where: { parent: { is: { labelNull: null } } } },
  { id: 'parent/is/empty', where: { parent: { is: {} } } },
  { id: 'parent/is/parentId-null', where: { parent: { is: { parentId: null } } } },
  { id: 'parent/is/nested-NOT', where: { parent: { is: { NOT: { label: 'alpha' } } } } },
  {
    id: 'parent/is/two-hop-label',
    where: { parent: { is: { parent: { is: { label: 'alpha' } } } } },
  },
  { id: 'parent/is/two-hop-empty', where: { parent: { is: { parent: { is: {} } } } } },
  {
    id: 'parent/is/two-hop-under-NOT',
    where: { parent: { is: { NOT: { parent: { is: { label: 'alpha' } } } } } },
  },
  {
    id: 'parent/is/hop-then-owner',
    where: { parent: { is: { owner: { is: { kind: 'red' } } } } },
  },
  { id: 'owner/isNot/kind-red', where: { owner: { isNot: { kind: 'red' } } } },
  { id: 'owner/isNot/rankNull-null', where: { owner: { isNot: { rankNull: null } } } },
  { id: 'owner/isNot/empty', where: { owner: { isNot: {} } } },
  { id: 'owner/is-null', where: { owner: { is: null } } },
  { id: 'owner/isNot-null', where: { owner: { isNot: null } } },
  { id: 'owner/bare-null', where: { owner: null } },
  { id: 'parent/isNot/label-alpha', where: { parent: { isNot: { label: 'alpha' } } } },
  { id: 'parent/isNot/stateNull-null', where: { parent: { isNot: { stateNull: null } } } },
  { id: 'parent/is-null', where: { parent: { is: null } } },
  { id: 'parent/isNot-null', where: { parent: { isNot: null } } },
  { id: 'parent/bare-null', where: { parent: null } },
  {
    id: 'parent/isNot/two-hop-label',
    where: { parent: { isNot: { parent: { isNot: { label: 'alpha' } } } } },
  },
  {
    id: 'parent/isNot/hop-then-owner',
    where: { parent: { isNot: { owner: { isNot: { kind: 'red' } } } } },
  },
];

const STRING_KEY_CASES: readonly WhereCase[] = [
  { id: 'region/is/name', where: { owner: { is: { region: { is: { name: 'northern' } } } } } },
  { id: 'region/is/name-empty', where: { owner: { is: { region: { is: { name: '' } } } } } },
  { id: 'region/is/code-upper', where: { owner: { is: { region: { is: { code: 'North' } } } } } },
  {
    id: 'region/is/code-lt-north',
    where: { owner: { is: { region: { is: { code: { lt: 'north' } } } } } },
  },
  { id: 'region/is/empty', where: { owner: { is: { region: { is: {} } } } } },
  { id: 'region/regionCode-null', where: { owner: { is: { regionCode: null } } } },
];

const DEEP_CHAIN_CASES: readonly WhereCase[] = [
  {
    id: 'chain/three-hop-label',
    where: { parent: { is: { parent: { is: { parent: { is: { label: 'alpha' } } } } } } },
  },
  {
    id: 'chain/three-hop-empty',
    where: { parent: { is: { parent: { is: { parent: { is: {} } } } } } },
  },
  {
    id: 'chain/four-hop-owner-kind',
    where: {
      parent: { is: { parent: { is: { parent: { is: { owner: { is: { kind: 'red' } } } } } } } },
    },
  },
  {
    id: 'chain/five-hop-region-name',
    where: {
      parent: {
        is: {
          parent: {
            is: { parent: { is: { owner: { is: { region: { is: { name: 'northern' } } } } } } },
          },
        },
      },
    },
  },
  {
    id: 'chain/four-hop-empty',
    where: { parent: { is: { parent: { is: { parent: { is: { parent: { is: {} } } } } } } } },
  },
  {
    id: 'chain/three-hop-under-NOT',
    where: {
      parent: {
        is: { NOT: { parent: { is: { parent: { is: { owner: { is: { kind: 'red' } } } } } } } },
      },
    },
  },
  {
    id: 'chain/hop-then-owner-then-region',
    where: { parent: { is: { owner: { is: { region: { is: { code: 'north' } } } } } } },
  },
];

const RELATION_ALIAS_CASES: readonly WhereCase[] = [
  {
    id: 'alias/deep-chain-beside-shallow-hop',
    where: {
      AND: [
        { parent: { is: { parent: { is: { parent: { is: { label: 'alpha' } } } } } } },
        { parent: { is: { label: '0' } } },
      ],
    },
  },
  {
    id: 'alias/deep-chain-shadows-root-column',
    where: {
      AND: [
        { id: { gt: 7 } },
        { parent: { is: { parent: { is: { parent: { is: { id: { lt: 2 } } } } } } } },
      ],
    },
  },
  {
    id: 'alias/sibling-hops-same-table',
    where: { AND: [{ parent: { is: { label: 'alpha' } } }, { parent: { is: { amount: { gt: -1 } } } }] },
  },
  {
    id: 'alias/sibling-hops-different-tables',
    where: { AND: [{ parent: { is: { label: 'alpha' } } }, { owner: { is: { kind: 'red' } } }] },
  },
  {
    id: 'alias/hop-shadows-root-column',
    where: { AND: [{ id: { gt: 2 } }, { parent: { is: { id: { lt: 3 } } } }] },
  },
  {
    id: 'alias/hop-and-root-same-column-name',
    where: { AND: [{ big: { gte: SAFE } }, { parent: { is: { big: { lt: SAFE } } } }] },
  },
  {
    id: 'alias/three-hops-in-one-condition',
    where: {
      OR: [
        { parent: { is: { label: 'alpha' } } },
        { parent: { is: { label: '' } } },
        { owner: { is: { kind: 'red' } } },
      ],
    },
  },
];

const NULL_FK_CASES: readonly WhereCase[] = [
  {
    id: 'null-fk/OR-relation-then-scalar',
    where: { OR: [{ owner: { is: { kind: 'red' } } }, { amount: 100 }] },
  },
  {
    id: 'null-fk/OR-scalar-then-relation',
    where: { OR: [{ amount: 100 }, { owner: { is: { kind: 'red' } } }] },
  },
  {
    id: 'null-fk/OR-relation-then-null-scalar',
    where: { OR: [{ owner: { is: { kind: 'red' } } }, { labelNull: null }] },
  },
  {
    id: 'null-fk/OR-self-relation-then-scalar',
    where: { OR: [{ parent: { is: { label: 'alpha' } } }, { amount: 100 }] },
  },
  {
    id: 'null-fk/OR-two-relations',
    where: { OR: [{ owner: { is: { kind: 'red' } } }, { parent: { is: { label: '' } } }] },
  },
  {
    id: 'null-fk/NOT-relation',
    where: { NOT: { owner: { is: { kind: 'red' } } } },
  },
  {
    id: 'null-fk/NOT-nested-is',
    where: { NOT: { owner: { is: { NOT: { kind: 'red' } } } } },
  },
  {
    id: 'null-fk/NOT-of-OR-with-relation',
    where: { NOT: { OR: [{ owner: { is: { kind: 'red' } } }, { amount: 100 }] } },
  },
  {
    id: 'null-fk/relation-under-double-NOT',
    where: { NOT: { NOT: { owner: { is: { kind: 'red' } } } } },
  },
  {
    id: 'null-fk/AND-relation-with-null-scalar',
    where: { AND: [{ owner: { is: { kind: 'red' } } }, { labelNull: { not: null } }] },
  },
  {
    id: 'null-fk/OR-two-hop-then-scalar',
    where: {
      OR: [{ parent: { is: { parent: { is: { label: 'alpha' } } } } }, { amount: 100 }],
    },
  },
  {
    id: 'null-fk/OR-hop-with-null-column-then-relation',
    where: { OR: [{ amountNull: null }, { owner: { is: { kind: 'red' } } }] },
  },
  {
    id: 'null-fk/OR-four-hop-then-scalar',
    where: {
      OR: [
        {
          parent: {
            is: { parent: { is: { parent: { is: { owner: { is: { kind: 'red' } } } } } } },
          },
        },
        { amount: 100 },
      ],
    },
  },
  {
    id: 'null-fk/OR-region-hop-then-scalar',
    where: {
      OR: [{ owner: { is: { region: { is: { name: 'northern' } } } } }, { amount: 100 }],
    },
  },
];

const EMPTY_BRANCH_CASES: readonly WhereCase[] = [
  { id: 'empty/OR-at-depth', where: { AND: [{ OR: [] }] } },
  { id: 'empty/OR-at-depth-two', where: { AND: [{ AND: [{ OR: [] }] }] } },
  { id: 'empty/NOT-of-OR-at-depth', where: { NOT: { AND: [{ OR: [] }] } } },
  { id: 'empty/OR-beside-a-branch', where: { OR: [{ OR: [] }, { amount: 0 }] } },
  { id: 'empty/AND-of-empty-OR-and-true', where: { AND: [{ OR: [] }, { AND: [] }] } },
  { id: 'empty/OR-inside-relation', where: { owner: { is: { AND: [{ OR: [] }] } } } },
  { id: 'empty/AND-inside-relation', where: { owner: { is: { AND: [] } } } },
  { id: 'empty/NOT-empty-object', where: { NOT: {} } },
  { id: 'empty/NOT-empty-array', where: { NOT: [] } },
  { id: 'empty/AND-empty', where: { AND: [] } },
  { id: 'empty/OR-empty', where: { OR: [] } },
  { id: 'empty/object', where: {} },
];

export const MAPPED_BARE_CASES: readonly WhereCase[] = [
  ...SCALAR_CASES,
  ...RELATION_CASES,
  ...STRING_KEY_CASES,
  ...DEEP_CHAIN_CASES,
  ...RELATION_ALIAS_CASES,
  ...NULL_FK_CASES,
  ...EMPTY_BRANCH_CASES,
];

export const MAPPED_CASES: readonly WhereCase[] = [
  ...MAPPED_BARE_CASES,
  ...MAPPED_BARE_CASES.map((entry) => ({ id: `NOT(${entry.id})`, where: { NOT: entry.where } })),
];
