import { subject } from '@casl/ability';
import {
  COMBINATORS,
  OPERATORS,
  OperatorDefinition,
  ScalarKind,
  evaluateConditions,
} from '@eleven-am/golem-policy';
import { AbilityBuilderFactory } from './casl';

export const BIGINT_EXACT_ABILITY_ERROR =
  'Golem authorization requires a BigInt-exact ability factory: the configured Authenticator.abilityFactory() does not match a rule condition { equals: 1n } against a numeric 1, so in-memory checks would be wrong for BigInt columns. Use createGolemAbility from @eleven-am/golem-authorizer, or omit abilityFactory to use the safe default.';

export const ABILITY_CONFORMANCE_ERROR =
  'Golem authorization requires an ability factory whose conditions matcher agrees with the golem operator table';

const BIGINT_PROBE_SUBJECT = 'GolemBigIntProbe';

const DATE_EARLY = new Date('2020-01-01T00:00:00.000Z');

const DATE_LATE = new Date('2021-01-01T00:00:00.000Z');

const OPERANDS: Readonly<Record<ScalarKind, readonly unknown[]>> = {
  null: [null],
  numeric: [1n, 2],
  string: ['b'],
  boolean: [true],
  date: [DATE_EARLY],
};

const VALUES: Readonly<Record<ScalarKind, readonly unknown[]>> = {
  null: [null, undefined, 1, 'a'],
  numeric: [1, 2n, 3],
  string: ['a', 'b'],
  boolean: [true, false],
  date: [DATE_EARLY, DATE_LATE],
};

const RELATION_CONDITIONS: readonly Record<string, unknown>[] = [
  { w: { equals: 1n } },
  { w: { is: { x: { equals: 'a' } } } },
];

const RELATION_VALUES: readonly unknown[] = [
  { w: 1 },
  { w: 2 },
  { w: { x: 'a' } },
  { w: { x: 'b' } },
  {},
  null,
  undefined,
  'a',
  [],
  [{ w: 1 }],
  [{ w: 1 }, { w: 2 }],
  [{ w: { x: 'a' } }],
  [{ w: { x: 'a' } }, { w: { x: 'b' } }],
];

const COMBINATOR_OBJECTS: readonly Record<string, unknown>[] = [
  { a: 1, b: 2 },
  { a: 1, b: 3 },
  { a: 9, b: 2 },
  { a: null, b: null },
];

const COMBINATOR_BRANCHES: readonly Record<string, unknown>[] = [{ a: { equals: 1 } }, { b: { equals: 2 } }];

interface ConformanceDraft {
  readonly operator: string;
  readonly conditions: Record<string, unknown>;
  readonly object: Record<string, unknown>;
  readonly subject?: string;
}

export interface ConformanceCase {
  readonly operator: string;
  readonly conditions: Record<string, unknown>;
  readonly object: Record<string, unknown>;
  readonly subject: string;
  readonly expected: boolean;
}

function describe(value: unknown): string {
  if (value === null) {
    return 'null';
  }
  if (value === undefined) {
    return 'undefined';
  }
  if (typeof value === 'bigint') {
    return `${value}n`;
  }
  if (typeof value === 'string') {
    return `'${value}'`;
  }
  if (value instanceof Date) {
    return value.toISOString();
  }
  if (Array.isArray(value)) {
    return `[${value.map(describe).join(', ')}]`;
  }
  if (typeof value === 'object') {
    return `{ ${Object.entries(value as Record<string, unknown>)
      .map(([key, entry]) => `${key}: ${describe(entry)}`)
      .join(', ')} }`;
  }
  return String(value);
}

function valuesFor(kind: ScalarKind): readonly unknown[] {
  return kind === 'null' ? VALUES.null : [...VALUES[kind], null, undefined];
}

function scalarDrafts(operator: OperatorDefinition): ConformanceDraft[] {
  const drafts: ConformanceDraft[] = [];
  for (const kind of operator.acceptedValueKinds) {
    for (const operand of OPERANDS[kind]) {
      for (const value of valuesFor(kind)) {
        drafts.push({
          operator: operator.name,
          conditions: { v: { [operator.name]: operand } },
          object: { v: value },
        });
      }
    }
  }
  return drafts;
}

function listDrafts(operator: OperatorDefinition): ConformanceDraft[] {
  const drafts: ConformanceDraft[] = [];
  for (const kind of operator.acceptedValueKinds) {
    for (const value of valuesFor(kind)) {
      drafts.push({
        operator: operator.name,
        conditions: { v: { [operator.name]: [...OPERANDS[kind]] } },
        object: { v: value },
      });
      drafts.push({
        operator: operator.name,
        conditions: { v: { [operator.name]: [] } },
        object: { v: value },
      });
    }
  }
  return drafts;
}

function relationDrafts(operator: OperatorDefinition): ConformanceDraft[] {
  const drafts: ConformanceDraft[] = [];
  for (const nested of RELATION_CONDITIONS) {
    for (const value of RELATION_VALUES) {
      drafts.push({
        operator: operator.name,
        conditions: { v: { [operator.name]: nested } },
        object: { v: value },
      });
    }
  }
  return drafts;
}

function operatorDrafts(operator: OperatorDefinition): ConformanceDraft[] {
  if (operator.operand === 'scalar') {
    return scalarDrafts(operator);
  }
  if (operator.operand === 'scalar-list') {
    return listDrafts(operator);
  }
  if (operator.operand === 'conditions') {
    return relationDrafts(operator);
  }
  throw new Error(
    `${ABILITY_CONFORMANCE_ERROR}: operator "${operator.name}" declares operand shape "${operator.operand}", which the conformance probe has no sample for. Extend the probe in @eleven-am/golem-authorizer before shipping the operator.`,
  );
}

function shorthandDrafts(): ConformanceDraft[] {
  const drafts: ConformanceDraft[] = [];
  for (const kind of OPERATORS.equals.acceptedValueKinds) {
    for (const operand of OPERANDS[kind]) {
      for (const value of valuesFor(kind)) {
        drafts.push({ operator: 'equals', conditions: { v: operand }, object: { v: value } });
      }
    }
  }
  return drafts;
}

function combinatorDrafts(): ConformanceDraft[] {
  const drafts: ConformanceDraft[] = [];
  const names = Object.keys(COMBINATORS);
  for (const name of names) {
    for (const object of COMBINATOR_OBJECTS) {
      drafts.push({ operator: name, conditions: { [name]: [...COMBINATOR_BRANCHES] }, object });
      drafts.push({ operator: name, conditions: { [name]: COMBINATOR_BRANCHES[0]! }, object });
      for (const inner of names) {
        drafts.push({
          operator: `${name}/${inner}`,
          conditions: { [name]: [{ [inner]: COMBINATOR_BRANCHES[0]! }, COMBINATOR_BRANCHES[1]!] },
          object,
        });
      }
    }
  }
  return drafts;
}

export function conformanceCases(): readonly ConformanceCase[] {
  const drafts: ConformanceDraft[] = [
    {
      operator: 'equals',
      conditions: { v: { equals: 1n } },
      object: { v: 1 },
      subject: BIGINT_PROBE_SUBJECT,
    },
    ...Object.values(OPERATORS).flatMap(operatorDrafts),
    ...shorthandDrafts(),
    ...combinatorDrafts(),
  ];
  return drafts.map((draft, index) => ({
    operator: draft.operator,
    conditions: draft.conditions,
    object: draft.object,
    subject: draft.subject ?? `GolemProbe${index}`,
    expected: evaluateConditions(draft.conditions, draft.object),
  }));
}

function divergenceMessage(probe: ConformanceCase, actual: boolean): string {
  return (
    `${ABILITY_CONFORMANCE_ERROR}: operator "${probe.operator}" diverged. ` +
    `Conditions ${describe(probe.conditions)} against ${describe(probe.object)} ` +
    `${actual ? 'matched' : 'did not match'}, golem requires ${probe.expected ? 'a match' : 'no match'}. ` +
    'Use createGolemAbility from @eleven-am/golem-authorizer, or omit abilityFactory to use the golem default.'
  );
}

export function assertAbilityConformance(factory: AbilityBuilderFactory): void {
  const probes = conformanceCases();
  let ability;
  try {
    const builder = factory();
    for (const probe of probes) {
      builder.can('read', probe.subject, probe.conditions);
    }
    ability = builder.build();
  } catch (error) {
    throw new Error(BIGINT_EXACT_ABILITY_ERROR, { cause: error });
  }
  for (const probe of probes) {
    const actual = Boolean(ability.can('read', subject(probe.subject, { ...probe.object })));
    if (actual === probe.expected) {
      continue;
    }
    throw new Error(
      probe.subject === BIGINT_PROBE_SUBJECT ? BIGINT_EXACT_ABILITY_ERROR : divergenceMessage(probe, actual),
    );
  }
}
