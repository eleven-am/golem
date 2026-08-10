import type { AuthorizationService } from '@eleven-am/authorizer';
import { createAbility } from '@eleven-am/authorizer/prisma';
import { accessibleBy } from '@casl/prisma';
import { evaluateConditions } from '@eleven-am/golem-policy';
import { AbilityLike, CaslRule } from './casl';
import { deriveFieldConstraint } from './field-constraint';
import { GolemAuthorizationAdapter } from './index';
import { createGolemAbility } from './policy';

type Row = Record<string, unknown>;

interface RawRule {
  action: string;
  subject: string;
  fields?: string[];
  conditions?: Record<string, unknown>;
  inverted?: boolean;
}

interface Scenario {
  name: string;
  field: string;
  rules: RawRule[];
  expected: unknown;
  discriminating: boolean;
}

const AUTHORS = [42, 7];
const STATUSES = ['PUBLISHED', 'DRAFT'];
const ORGS = ['acme', 'other'];
const COMMENTS: Row[][] = [[], [{ flagged: false }], [{ flagged: true }, { flagged: false }]];

const rows: Row[] = AUTHORS.flatMap((authorId) =>
  STATUSES.flatMap((status) =>
    ORGS.flatMap((organizationId) =>
      COMMENTS.map((comments, index) => ({
        id: `${authorId}-${status}-${organizationId}-${index}`,
        authorId,
        status,
        author: { organizationId },
        comments,
      })),
    ),
  ),
);

const can = (fields?: string[], conditions?: Record<string, unknown>): RawRule => ({
  action: 'read',
  subject: 'Post',
  ...(fields ? { fields } : {}),
  ...(conditions ? { conditions } : {}),
});

const cannot = (fields?: string[], conditions?: Record<string, unknown>): RawRule => ({
  ...can(fields, conditions),
  inverted: true,
});

const scenarios: Scenario[] = [
  {
    name: 'a field readable unconditionally',
    field: 'title',
    rules: [can()],
    expected: {},
    discriminating: false,
  },
  {
    name: 'a field never readable',
    field: 'title',
    rules: [can(), cannot(['title'])],
    expected: { OR: [] },
    discriminating: false,
  },
  {
    name: 'a field readable under a condition on the row own columns',
    field: 'title',
    rules: [can(['title'], { authorId: 42 })],
    expected: { authorId: 42 },
    discriminating: true,
  },
  {
    name: 'a field denied under a condition on the row own columns',
    field: 'title',
    rules: [can(), cannot(['title'], { status: 'DRAFT' })],
    expected: { NOT: { status: 'DRAFT' } },
    discriminating: true,
  },
  {
    name: 'a field readable under a condition that hops a to-one relation',
    field: 'title',
    rules: [can(), cannot(['title'], { author: { is: { organizationId: 'other' } } })],
    expected: { NOT: { author: { is: { organizationId: 'other' } } } },
    discriminating: true,
  },
  {
    name: 'a field readable under a condition that hops a to-many relation',
    field: 'secret',
    rules: [can(), cannot(['secret'], { comments: { some: { flagged: true } } })],
    expected: { NOT: { comments: { some: { flagged: true } } } },
    discriminating: true,
  },
  {
    name: 'interleaved can/cannot where the deny is declared last',
    field: 'title',
    rules: [can(), can(['title'], { authorId: 42 }), cannot(['title'], { status: 'DRAFT' })],
    expected: {
      OR: [
        { AND: [{ authorId: 42 }, { NOT: { status: 'DRAFT' } }] },
        { AND: [{ NOT: { status: 'DRAFT' } }, { NOT: { authorId: 42 } }] },
      ],
    },
    discriminating: true,
  },
  {
    name: 'interleaved can/cannot where the grant is declared last',
    field: 'title',
    rules: [can(), cannot(['title'], { status: 'DRAFT' }), can(['title'], { authorId: 42 })],
    expected: {
      OR: [{ authorId: 42 }, { AND: [{ NOT: { authorId: 42 } }, { NOT: { status: 'DRAFT' } }] }],
    },
    discriminating: true,
  },
  {
    name: 'a cannot on a field without conditions outranks a conditional grant',
    field: 'title',
    rules: [can(), can(['title'], { authorId: 42 }), cannot(['title'])],
    expected: { OR: [] },
    discriminating: false,
  },
  {
    name: 'a field-scoped rule alongside a model-scoped rule with a different condition',
    field: 'title',
    rules: [can(undefined, { status: 'PUBLISHED' }), can(['title'], { authorId: 42 })],
    expected: {
      OR: [{ authorId: 42 }, { AND: [{ status: 'PUBLISHED' }, { NOT: { authorId: 42 } }] }],
    },
    discriminating: true,
  },
  {
    name: 'two conditional grants on the same field',
    field: 'title',
    rules: [can(['title'], { authorId: 42 }), can(['title'], { status: 'PUBLISHED' })],
    expected: {
      OR: [{ status: 'PUBLISHED' }, { AND: [{ authorId: 42 }, { NOT: { status: 'PUBLISHED' } }] }],
    },
    discriminating: true,
  },
  {
    name: 'a deny written as a field pattern rather than a field name',
    field: 'title',
    rules: [can(), cannot(['t*'], { status: 'DRAFT' })],
    expected: { NOT: { status: 'DRAFT' } },
    discriminating: true,
  },
  {
    name: 'a model with no field rules at all',
    field: 'title',
    rules: [can(undefined, { status: 'PUBLISHED' })],
    expected: { status: 'PUBLISHED' },
    discriminating: true,
  },
  {
    name: 'a model with no rules at all',
    field: 'title',
    rules: [],
    expected: { OR: [] },
    discriminating: false,
  },
];

const factories: Record<string, (rules: RawRule[]) => AbilityLike> = {
  golem: (rules) => createGolemAbility(rules as never) as unknown as AbilityLike,
  prisma: (rules) => createAbility(rules as never) as unknown as AbilityLike,
};

function adapterFor(ability: AbilityLike): GolemAuthorizationAdapter {
  const authorizationService = {
    getAbility: jest.fn(async () => ability),
  } as unknown as AuthorizationService;
  return new GolemAuthorizationAdapter(authorizationService);
}

async function verdicts(adapter: GolemAuthorizationAdapter, field: string): Promise<boolean[]> {
  const answers: boolean[] = [];
  for (const row of rows) {
    answers.push(await adapter.checkField('read', 'Post', { ...row }, field, { request: {} }));
  }
  return answers;
}

function predict(condition: unknown): boolean[] {
  return rows.map((row) => evaluateConditions(condition, row));
}

function disagreements(condition: unknown, answers: readonly boolean[]): string[] {
  const predicted = predict(condition);
  return rows
    .map((row, index) => ({ row, predicted: predicted[index]!, answered: answers[index]! }))
    .filter((entry) => entry.predicted !== entry.answered)
    .map((entry) => `${entry.row.id}: condition ${entry.predicted}, checkField ${entry.answered}`);
}

type RowFilterLens = (ability: AbilityLike, action: string) => { ofType(model: string): unknown };

function rowFilterLens(ability: AbilityLike): unknown {
  return (accessibleBy as unknown as RowFilterLens)(ability, 'read').ofType('Post');
}

function unorderedLens(chain: readonly CaslRule[]): unknown {
  const granted = chain.filter((rule) => !rule.inverted);
  const denied = chain.filter((rule) => rule.inverted);
  const grants = granted.some((rule) => rule.conditions === undefined)
    ? []
    : [{ OR: granted.map((rule) => rule.conditions) }];
  const denials = denied.map((rule) => ({ NOT: rule.conditions ?? {} }));
  const branches = [...grants, ...denials];
  return branches.length === 0 ? {} : { AND: branches };
}

describe('deriveFieldConstraint', () => {
  it.each(scenarios)('derives the documented condition for $name', ({ rules, field, expected }) => {
    const chain = factories.golem!(rules).rulesFor('read', 'Post', field);
    expect(deriveFieldConstraint(chain)).toEqual(expected);
  });

  it('returns an always-false condition for an empty chain', () => {
    expect(deriveFieldConstraint([])).toEqual({ OR: [] });
    expect(evaluateConditions(deriveFieldConstraint([]), rows[0]!)).toBe(false);
  });

  it('returns an always-true condition for a lone unconditional grant', () => {
    expect(deriveFieldConstraint([{ inverted: false }])).toEqual({});
    expect(evaluateConditions(deriveFieldConstraint([{ inverted: false }]), rows[0]!)).toBe(true);
  });

  it('stops at the first unconditional rule because no later rule can be reached', () => {
    const chain: CaslRule[] = [
      { inverted: true, conditions: { status: 'DRAFT' } },
      { inverted: false },
      { inverted: false, conditions: { authorId: 42 } },
    ];
    expect(deriveFieldConstraint(chain)).toEqual({ NOT: { status: 'DRAFT' } });
  });
});

describe.each(Object.keys(factories))('field constraint oracle on the %s matcher', (name) => {
  const factory = factories[name]!;

  it.each(scenarios)(
    'the condition agrees with checkField on every row for $name',
    async ({ rules, field, expected, discriminating }) => {
      const ability = factory(rules);
      const adapter = adapterFor(ability);
      const condition = await adapter.constrainField('read', 'Post', field, { request: {} });
      const answers = await verdicts(adapter, field);

      expect(condition).toEqual(expected);
      expect(disagreements(condition, answers)).toEqual([]);
      if (discriminating) {
        expect(new Set(answers).size).toBe(2);
      }
    },
  );
});

describe('the condition reads the entity it is given', () => {
  it('agrees with checkField on a row whose relation was never hydrated', async () => {
    const rules = [can(), cannot(['title'], { author: { is: { organizationId: 'other' } } })];
    const adapter = adapterFor(factories.golem!(rules));
    const condition = await adapter.constrainField('read', 'Post', 'title', { request: {} });
    const bare = { id: 'no-author', authorId: 42, status: 'PUBLISHED' };

    const answered = await adapter.checkField('read', 'Post', { ...bare }, 'title', { request: {} });
    expect(evaluateConditions(condition, bare)).toBe(answered);
    expect(answered).toBe(true);
  });
});

const alphabet: RawRule[] = [
  can(),
  can(undefined, { status: 'PUBLISHED' }),
  cannot(),
  cannot(undefined, { status: 'DRAFT' }),
  can(['title']),
  can(['title'], { authorId: 42 }),
  can(['title'], { author: { is: { organizationId: 'acme' } } }),
  cannot(['title']),
  cannot(['title'], { status: 'DRAFT' }),
  cannot(['title'], { comments: { some: { flagged: true } } }),
];

function chainsUpTo(length: number): RawRule[][] {
  let chains: RawRule[][] = [[]];
  const all: RawRule[][] = [];
  for (let step = 0; step < length; step += 1) {
    chains = chains.flatMap((chain) => alphabet.map((rule) => [...chain, rule]));
    all.push(...chains);
  }
  return all;
}

describe('the derivation survives every rule chain the alphabet can build', () => {
  it.each([['title'], ['body']])('agrees with checkField on field %s', async (field) => {
    const failures: string[] = [];
    for (const rules of chainsUpTo(3)) {
      const ability = factories.golem!(rules);
      const adapter = adapterFor(ability);
      const condition = await adapter.constrainField('read', 'Post', field, { request: {} });
      const found = disagreements(condition, await verdicts(adapter, field));
      if (found.length > 0) {
        failures.push(`${JSON.stringify(rules)} -> ${JSON.stringify(condition)}: ${found[0]}`);
      }
    }
    expect(failures).toEqual([]);
  }, 120000);

  it('reaches conditions on both sides across the sweep', async () => {
    const shapes = new Set<string>();
    for (const rules of chainsUpTo(2)) {
      const adapter = adapterFor(factories.golem!(rules));
      const answers = await verdicts(adapter, 'title');
      shapes.add(new Set(answers).size === 2 ? 'mixed' : String(answers[0]));
    }
    expect([...shapes].sort()).toEqual(['false', 'mixed', 'true']);
  }, 60000);
});

describe('the oracle catches a wrong lens', () => {
  const rowFilterVictims = [
    'a field never readable',
    'a field denied under a condition on the row own columns',
    'a field readable under a condition that hops a to-one relation',
    'a field readable under a condition that hops a to-many relation',
    'interleaved can/cannot where the deny is declared last',
    'a cannot on a field without conditions outranks a conditional grant',
  ];

  it.each(rowFilterVictims)('flags the row-filter lens on %s', async (name) => {
    const scenario = scenarios.find((entry) => entry.name === name)!;
    const ability = factories.golem!(scenario.rules);
    const adapter = adapterFor(ability);
    const answers = await verdicts(adapter, scenario.field);

    expect(disagreements(rowFilterLens(ability), answers).length).toBeGreaterThan(0);
  });

  it('flags the row-filter lens across the generated sweep, not only the named cases', async () => {
    let flagged = 0;
    for (const rules of chainsUpTo(2)) {
      const ability = factories.golem!(rules);
      const adapter = adapterFor(ability);
      const answers = await verdicts(adapter, 'title');
      if (disagreements(rowFilterLens(ability), answers).length > 0) {
        flagged += 1;
      }
    }
    expect(flagged).toBeGreaterThan(0);
  }, 60000);

  it('flags a lens that ignores rule priority', async () => {
    const scenario = scenarios.find(
      (entry) => entry.name === 'interleaved can/cannot where the grant is declared last',
    )!;
    const ability = factories.golem!(scenario.rules);
    const adapter = adapterFor(ability);
    const answers = await verdicts(adapter, scenario.field);
    const chain = ability.rulesFor('read', 'Post', scenario.field);

    expect(disagreements(unorderedLens(chain), answers).length).toBeGreaterThan(0);
    expect(disagreements(deriveFieldConstraint(chain), answers)).toEqual([]);
  });

  it('flags a lens that drops the deny rules', async () => {
    const scenario = scenarios.find(
      (entry) => entry.name === 'a field readable under a condition that hops a to-one relation',
    )!;
    const ability = factories.golem!(scenario.rules);
    const adapter = adapterFor(ability);
    const answers = await verdicts(adapter, scenario.field);
    const chain = ability.rulesFor('read', 'Post', scenario.field);
    const grantsOnly = chain.filter((rule) => !rule.inverted);

    expect(disagreements(deriveFieldConstraint(grantsOnly), answers).length).toBeGreaterThan(0);
  });
});
