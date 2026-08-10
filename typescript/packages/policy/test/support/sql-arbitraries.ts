import fc from 'fast-check';
import { PolicyDatamodel, PolicyDatamodelModel } from '../../src/index';
import { WhereCase } from './matrix';
import { BELOW, BEYOND, D0, D1, D2, SAFE } from './rows';

export const SQL_SEED = Number(process.env.GOLEM_POLICY_SQL_SEED ?? 20260729);

export const SQL_NUM_RUNS = Number(process.env.GOLEM_POLICY_SQL_RUNS ?? 400);

export const SQL_NUM_ROWS = Number(process.env.GOLEM_POLICY_SQL_ROWS ?? 24);

export const SQL_RELATION_HOPS = Number(process.env.GOLEM_POLICY_SQL_HOPS ?? 3);

export const SQL_LIST_HOPS = Number(process.env.GOLEM_POLICY_SQL_LIST_HOPS ?? 2);

const TEXTS = ['', '0', 'a', 'alpha', 'Zulu', 'zulu', 'beta', 'Ångström', 'a b'];

const INTS = [-100, -1, 0, 1, 7, 100];

const BIGS = [BELOW, -1n, 0n, 1n, SAFE, BEYOND];

const DATES = [D0, D1, D2];

type Kind = 'text' | 'int' | 'big' | 'date' | 'bool';

interface ColumnArb {
  readonly name: string;
  readonly kind: Kind;
  readonly nullable: boolean;
}

const PARENT_COLUMNS: readonly ColumnArb[] = [
  { name: 'name', kind: 'text', nullable: false },
  { name: 'nameNull', kind: 'text', nullable: true },
  { name: 'count', kind: 'int', nullable: false },
  { name: 'countNull', kind: 'int', nullable: true },
  { name: 'big', kind: 'big', nullable: false },
  { name: 'bigNull', kind: 'big', nullable: true },
  { name: 'at', kind: 'date', nullable: false },
  { name: 'atNull', kind: 'date', nullable: true },
];

const RELATED_COLUMNS: readonly ColumnArb[] = [
  { name: 'tag', kind: 'text', nullable: false },
  { name: 'tagNull', kind: 'text', nullable: true },
  { name: 'num', kind: 'int', nullable: false },
  { name: 'numNull', kind: 'int', nullable: true },
  { name: 'big', kind: 'big', nullable: false },
  { name: 'at', kind: 'date', nullable: false },
  { name: 'nextId', kind: 'int', nullable: true },
];

function operandOf(kind: Kind): fc.Arbitrary<unknown> {
  switch (kind) {
    case 'text':
      return fc.constantFrom(...TEXTS);
    case 'int':
      return fc.constantFrom(...INTS);
    case 'big':
      return fc.constantFrom(...BIGS);
    case 'date':
      return fc.constantFrom(...DATES);
    case 'bool':
      return fc.boolean();
  }
}

function orderedOperators(kind: Kind): readonly string[] {
  return kind === 'bool' ? [] : ['lt', 'lte', 'gt', 'gte'];
}

function filterOf(column: ColumnArb): fc.Arbitrary<unknown> {
  const operand = operandOf(column.kind);
  const nullable = column.nullable
    ? [{ weight: 2, arbitrary: fc.constantFrom({ equals: null }, { not: null }, null) }]
    : [];
  const ordered = orderedOperators(column.kind);
  const comparisons =
    ordered.length === 0
      ? []
      : [
          {
            weight: 4,
            arbitrary: fc
              .constantFrom(...ordered)
              .chain((operator) => operand.map((value) => ({ [operator]: value }))),
          },
          {
            weight: 1,
            arbitrary: fc
              .tuple(operand, operand)
              .map(([low, high]) => ({ gte: low, lte: high })),
          },
        ];
  return fc.oneof(
    { weight: 3, arbitrary: operand },
    { weight: 3, arbitrary: operand.map((value) => ({ equals: value })) },
    { weight: 3, arbitrary: operand.map((value) => ({ not: value })) },
    { weight: 2, arbitrary: fc.array(operand, { maxLength: 3 }).map((list) => ({ in: list })) },
    { weight: 2, arbitrary: fc.array(operand, { maxLength: 3 }).map((list) => ({ notIn: list })) },
    { weight: 1, arbitrary: fc.constant({}) },
    ...comparisons,
    ...nullable,
  );
}

function fieldConditionOf(columns: readonly ColumnArb[]): fc.Arbitrary<Record<string, unknown>> {
  return fc
    .constantFrom(...columns)
    .chain((column) => filterOf(column).map((filter) => ({ [column.name]: filter })));
}

const QUANTIFIERS: readonly string[] = ['some', 'every', 'none'];

const TO_ONE_OPERATORS: readonly string[] = ['is', 'isNot'];

function relationBodyOf(
  columns: readonly ColumnArb[],
  chain: string,
  listChain: string,
  hops: number,
  listHops: number,
): fc.Arbitrary<Record<string, unknown>> {
  const flat = fc
    .array(fieldConditionOf(columns), { minLength: 0, maxLength: 2 })
    .chain((branches) =>
      fc.constantFrom<Record<string, unknown>>(
        Object.assign({}, ...branches) as Record<string, unknown>,
        { AND: branches },
        { OR: branches },
        { NOT: branches },
      ),
    );
  if (hops <= 1) {
    return flat;
  }
  const deeper = fc
    .constantFrom(...TO_ONE_OPERATORS)
    .chain((operator) =>
      relationBodyOf(columns, chain, listChain, hops - 1, listHops).map((nested) => ({
        [chain]: { [operator]: nested },
      })),
    );
  const branches: { weight: number; arbitrary: fc.Arbitrary<Record<string, unknown>> }[] = [
    { weight: 3, arbitrary: deeper },
    { weight: 2, arbitrary: deeper.map((branch) => ({ NOT: branch })) },
    {
      weight: 2,
      arbitrary: fc.tuple(flat, deeper).map(([left, right]) => ({ AND: [left, right] })),
    },
    {
      weight: 1,
      arbitrary: fc.tuple(flat, deeper).map(([left, right]) => ({ OR: [left, right] })),
    },
  ];
  if (listHops > 0) {
    const inner = relationBodyOf(columns, chain, listChain, hops - 1, listHops - 1);
    const quantified = fc
      .constantFrom(...QUANTIFIERS)
      .chain((quantifier) => inner.map((nested) => ({ [listChain]: { [quantifier]: nested } })));
    branches.push(
      { weight: 5, arbitrary: quantified },
      { weight: 1, arbitrary: quantified.map((branch) => ({ NOT: branch })) },
      {
        weight: 1,
        arbitrary: fc.tuple(flat, quantified).map(([left, right]) => ({ OR: [left, right] })),
      },
    );
  }
  return fc.oneof({ weight: 4, arbitrary: flat }, ...branches);
}

function relationConditionOf(
  field: string,
  columns: readonly ColumnArb[],
  chain: string,
  listChain: string,
  hops: number,
  listHops: number,
): fc.Arbitrary<Record<string, unknown>> {
  return fc
    .constantFrom(...TO_ONE_OPERATORS)
    .chain((operator) =>
      relationBodyOf(columns, chain, listChain, hops, listHops).map((nested) => ({
        [field]: { [operator]: nested },
      })),
    );
}

function conditionOf(
  columns: readonly ColumnArb[],
  relation: fc.Arbitrary<Record<string, unknown>>,
): fc.Arbitrary<Record<string, unknown>> {
  return fc.letrec<{ node: Record<string, unknown> }>((tie) => ({
    node: fc.oneof(
      { depthSize: 'small', maxDepth: 3 },
      fieldConditionOf(columns),
      fieldConditionOf(columns),
      fieldConditionOf(columns),
      relation,
      relation,
      fc.array(tie('node'), { minLength: 0, maxLength: 3 }).map((branches) => ({ AND: branches })),
      fc.array(tie('node'), { minLength: 0, maxLength: 3 }).map((branches) => ({ OR: branches })),
      fc.array(tie('node'), { minLength: 0, maxLength: 2 }).map((branches) => ({ NOT: branches })),
      tie('node').map((branch) => ({ NOT: branch })),
      fc
        .tuple(tie('node'), tie('node'))
        .map(([left, right]) => ({ AND: [left, { NOT: right }] })),
    ),
  })).node;
}

export function parentConditionArbitrary(): fc.Arbitrary<Record<string, unknown>> {
  return conditionOf(
    PARENT_COLUMNS,
    relationConditionOf(
      'related',
      RELATED_COLUMNS,
      'next',
      'previous',
      SQL_RELATION_HOPS,
      SQL_LIST_HOPS,
    ),
  );
}

function relatedInclude(remaining: number): unknown {
  if (remaining <= 1) {
    return true;
  }
  return {
    include: { next: relatedInclude(remaining - 1), previous: relatedInclude(remaining - 1) },
  };
}

export function demoInclude(hops: number): Record<string, unknown> {
  return { related: relatedInclude(hops) };
}

export function parentRowArbitrary(id: number): fc.Arbitrary<Record<string, unknown>> {
  return fc.record({
    id: fc.constant(id),
    name: fc.constantFrom(...TEXTS),
    nameNull: fc.option(fc.constantFrom(...TEXTS), { nil: null, freq: 3 }),
    count: fc.constantFrom(...INTS),
    countNull: fc.option(fc.constantFrom(...INTS), { nil: null, freq: 3 }),
    big: fc.constantFrom(...BIGS),
    bigNull: fc.option(fc.constantFrom(...BIGS), { nil: null, freq: 3 }),
    at: fc.constantFrom(...DATES),
    atNull: fc.option(fc.constantFrom(...DATES), { nil: null, freq: 3 }),
    relatedId: fc.option(fc.constantFrom(1, 2, 3), { nil: null, freq: 3 }),
  });
}

export function relatedRowArbitrary(id: number): fc.Arbitrary<Record<string, unknown>> {
  return fc.record({
    id: fc.constant(id),
    tag: fc.constantFrom(...TEXTS),
    tagNull: fc.option(fc.constantFrom(...TEXTS), { nil: null, freq: 3 }),
    num: fc.constantFrom(...INTS),
    numNull: fc.option(fc.constantFrom(...INTS), { nil: null, freq: 3 }),
    big: fc.constantFrom(...BIGS),
    at: fc.constantFrom(...DATES),
    nextId: fc.constant(id === 1 ? null : id - 1),
  });
}

export function hopDepthOf(node: unknown): number {
  if (Array.isArray(node)) {
    return node.reduce<number>((deepest, entry) => Math.max(deepest, hopDepthOf(entry)), 0);
  }
  if (typeof node !== 'object' || node === null || node instanceof Date) {
    return 0;
  }
  let deepest = 0;
  for (const [key, value] of Object.entries(node)) {
    const nested =
      key !== 'AND' && key !== 'OR' && key !== 'NOT' && typeof value === 'object' && value !== null
        ? (value as Record<string, unknown>)
        : undefined;
    const operand =
      nested === undefined || Array.isArray(value)
        ? undefined
        : TO_ONE_OPERATORS.find((name) => name in nested);
    const depth =
      operand === undefined ? hopDepthOf(value) : 1 + hopDepthOf(nested![operand]);
    deepest = Math.max(deepest, depth);
  }
  return deepest;
}

function modelNamed(datamodel: PolicyDatamodel, name: string): PolicyDatamodelModel {
  const found = datamodel.models.find((model) => model.name === name);
  if (found === undefined) {
    throw new Error(`model ${name} is missing from the datamodel`);
  }
  return found;
}

export function relationDepthOf(
  datamodel: PolicyDatamodel,
  model: string,
  node: unknown,
): number {
  if (Array.isArray(node)) {
    return node.reduce<number>((deepest, entry) => Math.max(deepest, relationDepthOf(datamodel, model, entry)), 0);
  }
  if (typeof node !== 'object' || node === null || node instanceof Date) {
    return 0;
  }
  let deepest = 0;
  for (const [key, value] of Object.entries(node)) {
    if (key === 'AND' || key === 'OR' || key === 'NOT') {
      deepest = Math.max(deepest, relationDepthOf(datamodel, model, value));
      continue;
    }
    const field = modelNamed(datamodel, model).fields.find((entry) => entry.name === key);
    if (field === undefined || field.kind !== 'object') {
      continue;
    }
    const nested =
      typeof value === 'object' && value !== null && !Array.isArray(value)
        ? (value as Record<string, unknown>)
        : {};
    const operands = ['is', 'isNot', 'some', 'every', 'none'].filter((name) => name in nested);
    const bodies = operands.length === 0 ? [nested] : operands.map((name) => nested[name]);
    for (const body of bodies) {
      deepest = Math.max(deepest, 1 + relationDepthOf(datamodel, field.type, body));
    }
  }
  return deepest;
}

export function relationDepthCounts(
  datamodel: PolicyDatamodel,
  model: string,
  cases: readonly WhereCase[],
): Record<number, number> {
  const counts: Record<number, number> = {};
  for (const entry of cases) {
    const depth = relationDepthOf(datamodel, model, entry.where);
    counts[depth] = (counts[depth] ?? 0) + 1;
  }
  return counts;
}

export function quantifierDepthOf(node: unknown): number {
  if (Array.isArray(node)) {
    return node.reduce<number>((deepest, entry) => Math.max(deepest, quantifierDepthOf(entry)), 0);
  }
  if (typeof node !== 'object' || node === null || node instanceof Date) {
    return 0;
  }
  return Object.entries(node).reduce((deepest, [key, value]) => {
    const nested = quantifierDepthOf(value);
    return Math.max(deepest, QUANTIFIERS.includes(key) ? 1 + nested : nested);
  }, 0);
}

export function quantifierCount(node: unknown): number {
  if (Array.isArray(node)) {
    return node.reduce<number>((total, entry) => total + quantifierCount(entry), 0);
  }
  if (typeof node !== 'object' || node === null || node instanceof Date) {
    return 0;
  }
  return Object.entries(node).reduce(
    (total, [key, value]) => total + (QUANTIFIERS.includes(key) ? 1 : 0) + quantifierCount(value),
    0,
  );
}

export function hopDepthCounts(cases: readonly WhereCase[]): Record<number, number> {
  const counts: Record<number, number> = {};
  for (const entry of cases) {
    const depth = hopDepthOf(entry.where);
    counts[depth] = (counts[depth] ?? 0) + 1;
  }
  return counts;
}

export function surfaceOf(node: unknown, found: Set<string> = new Set()): Set<string> {
  if (Array.isArray(node)) {
    if (node.length === 0) {
      found.add('empty-branch');
    }
    node.forEach((entry) => surfaceOf(entry, found));
    return found;
  }
  if (typeof node !== 'object' || node === null || node instanceof Date) {
    return found;
  }
  const entries = Object.entries(node);
  if (entries.length === 0) {
    found.add('empty-object');
  }
  for (const [key, value] of entries) {
    found.add(key);
    surfaceOf(value, found);
  }
  return found;
}

export function relationOperandsAt(
  node: unknown,
  field: string,
  found: Set<string> = new Set(),
): Set<string> {
  if (Array.isArray(node)) {
    node.forEach((entry) => relationOperandsAt(entry, field, found));
    return found;
  }
  if (typeof node !== 'object' || node === null || node instanceof Date) {
    return found;
  }
  for (const [key, value] of Object.entries(node)) {
    if (key === field && typeof value === 'object' && value !== null && !Array.isArray(value)) {
      for (const name of TO_ONE_OPERATORS) {
        if (name in (value as Record<string, unknown>)) {
          found.add(name);
        }
      }
    }
    relationOperandsAt(value, field, found);
  }
  return found;
}

export function corpusSurface(cases: readonly WhereCase[]): Set<string> {
  const found = new Set<string>();
  cases.forEach((entry) => surfaceOf(entry.where, found));
  return found;
}

export function sampleCases(count: number, seed: number): WhereCase[] {
  return fc
    .sample(parentConditionArbitrary(), { numRuns: count, seed })
    .map((where, index) => ({ id: `generated/${index}`, where }));
}
