import fc from 'fast-check';

export const SEED = Number(process.env.GOLEM_POLICY_PROPERTY_SEED ?? 20260729);

export const NUM_RUNS = Number(process.env.GOLEM_POLICY_PROPERTY_RUNS ?? 4000);

export const SAFE = 9007199254740992n;
export const BEYOND = 9007199254740993n;

const DATES = [
  new Date(0),
  new Date('2020-01-01T00:00:00.000Z'),
  new Date('2021-06-15T12:30:00.000Z'),
  new Date('invalid'),
];

const NUMERIC = fc.oneof(
  fc.constantFrom(0, -0, 1, -1, 1.5, 9007199254740992, 9007199254740993),
  fc.constantFrom(Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY),
  fc.integer({ min: -3, max: 3 }),
  fc.constantFrom(0n, 1n, -1n, SAFE, BEYOND, -BEYOND),
);

const TEXT = fc.constantFrom('', 'a', 'b', 'Z', '0', '2020-01-01T00:00:00.000Z');

const ORDERED = fc.oneof(NUMERIC, TEXT, fc.constantFrom(...DATES));

export const SCALAR = fc.oneof(
  { weight: 2, arbitrary: fc.constantFrom(null, undefined) },
  { weight: 1, arbitrary: fc.constantFrom(true, false) },
  { weight: 4, arbitrary: ORDERED },
);

const LIST_ELEMENT = fc.oneof(NUMERIC, TEXT, fc.constantFrom(true, false), fc.constantFrom(...DATES));

const SCALAR_FIELDS = ['a', 'b'] as const;

const RELATION_FIELD = 'rel';

function scalarFilter(): fc.Arbitrary<unknown> {
  return fc.oneof(
    { weight: 3, arbitrary: SCALAR },
    { weight: 2, arbitrary: SCALAR.map((operand) => ({ equals: operand })) },
    { weight: 2, arbitrary: SCALAR.map((operand) => ({ not: operand })) },
    {
      weight: 3,
      arbitrary: fc.constantFrom('lt', 'lte', 'gt', 'gte').chain((operator) =>
        ORDERED.map((operand) => ({ [operator]: operand })),
      ),
    },
    {
      weight: 2,
      arbitrary: fc
        .array(LIST_ELEMENT, { maxLength: 3 })
        .map((list) => ({ in: list })),
    },
    {
      weight: 2,
      arbitrary: fc
        .array(LIST_ELEMENT, { maxLength: 3 })
        .map((list) => ({ notIn: list })),
    },
    {
      weight: 1,
      arbitrary: fc
        .tuple(fc.constantFrom('lt', 'gt'), ORDERED, fc.constantFrom('lte', 'gte'), ORDERED)
        .map(([first, firstOperand, second, secondOperand]) => ({
          [first]: firstOperand,
          [second]: secondOperand,
        })),
    },
    { weight: 1, arbitrary: fc.constant({}) },
  );
}

function fieldCondition(): fc.Arbitrary<Record<string, unknown>> {
  return fc
    .tuple(fc.constantFrom(...SCALAR_FIELDS), scalarFilter())
    .map(([field, filter]) => ({ [field]: filter }));
}

function relationCondition(): fc.Arbitrary<Record<string, unknown>> {
  return fieldCondition().map((nested) => ({ [RELATION_FIELD]: { is: nested } }));
}

export function conditionArbitrary(): fc.Arbitrary<Record<string, unknown>> {
  return fc.letrec<{ node: Record<string, unknown> }>((tie) => ({
    node: fc.oneof(
      { depthSize: 'small', maxDepth: 3 },
      fieldCondition(),
      fieldCondition(),
      relationCondition(),
      fc.array(tie('node'), { minLength: 0, maxLength: 3 }).map((branches) => ({ AND: branches })),
      fc.array(tie('node'), { minLength: 0, maxLength: 3 }).map((branches) => ({ OR: branches })),
      fc.array(tie('node'), { minLength: 0, maxLength: 2 }).map((branches) => ({ NOT: branches })),
      tie('node').map((branch) => ({ NOT: branch })),
    ),
  })).node;
}

export function objectArbitrary(): fc.Arbitrary<Record<string, unknown>> {
  const scalars = fc.record(
    { a: SCALAR, b: SCALAR },
    { requiredKeys: [] },
  );
  return fc.record(
    {
      a: SCALAR,
      b: SCALAR,
      [RELATION_FIELD]: fc.oneof(fc.constantFrom(null, undefined), scalars),
    },
    { requiredKeys: [] },
  ) as fc.Arbitrary<Record<string, unknown>>;
}
