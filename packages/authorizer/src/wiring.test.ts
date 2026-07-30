import { AbilityBuilder } from '@casl/ability';
import { createPrismaAbility } from '@casl/prisma';
import type { AuthorizationService } from '@eleven-am/authorizer';
import { createAbility } from '@eleven-am/authorizer/prisma';
import { COMBINATORS, SUPPORTED_OPERATORS } from '@eleven-am/golem-policy';
import {
  ABILITY_CONFORMANCE_ERROR,
  BIGINT_EXACT_ABILITY_ERROR,
  GolemAuthorizationAdapter,
  GolemPolicyRuleError,
  UNSUPPORTED_POLICY_CONDITION_ERROR,
  conformanceCases,
  createGolemAbility,
} from './index';
import { installGolemAbilityFactory } from './install';

type Define = (builder: AbilityBuilder<never>) => void;

interface FakeOptions {
  readonly abilityFactory?: () => unknown;
  readonly stockDefault?: () => unknown;
}

class FakeAuthorizationService {
  readonly authenticator: { abilityFactory?: () => unknown };

  private readonly stockDefault: () => unknown;

  constructor(
    private readonly define: Define = () => undefined,
    options: FakeOptions = {},
  ) {
    this.authenticator = options.abilityFactory ? { abilityFactory: options.abilityFactory } : {};
    this.stockDefault = options.stockDefault ?? (() => new AbilityBuilder(createAbility));
  }

  resolvedAbilityFactory(): () => unknown {
    return this.stockDefault;
  }

  async getAbility(): Promise<unknown> {
    const builder = this.resolvedAbilityFactory()() as AbilityBuilder<never>;
    this.define(builder);
    return (builder as unknown as { build(): unknown }).build();
  }
}

function bootedAdapter(define: Define = () => undefined, options: FakeOptions = {}): GolemAuthorizationAdapter {
  const service = new FakeAuthorizationService(define, options) as unknown as AuthorizationService;
  const adapter = new GolemAuthorizationAdapter(service);
  adapter.onModuleInit();
  return adapter;
}

function context(): unknown {
  return { req: {} };
}

describe('golem policy matcher is the matcher backing ability.can', () => {
  it('refuses to match a comparison against a null field, where @casl/prisma matches', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, { viewCount: { lt: 5 } } as never);
    });

    await expect(adapter.check('read', 'Post', { viewCount: null }, context())).resolves.toBe(false);
    await expect(adapter.check('read', 'Post', { viewCount: 3 }, context())).resolves.toBe(true);
  });

  it('leaves the @casl/prisma interpreter matching that same null comparison', async () => {
    const ability = createAbility([{ action: 'read', subject: 'Post', conditions: { viewCount: { lt: 5 } } }]);
    const casl = ability.can('read', { __caslSubjectType__: 'Post', viewCount: null } as never);

    expect(casl).toBe(true);
  });

  it('keeps a comparison symmetric across null: neither lt nor gte matches', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, { OR: [{ v: { lt: 5 } }, { v: { gte: 5 } }] } as never);
    });

    await expect(adapter.check('read', 'Post', { v: null }, context())).resolves.toBe(false);
  });

  it('matches a BigInt condition against a numeric field', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, { v: { equals: 1n } } as never);
    });

    await expect(adapter.check('read', 'Post', { v: 1 }, context())).resolves.toBe(true);
  });

  it('backs checkField through the same matcher', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, ['secret'] as never, { v: { gte: 5 } } as never);
    });

    await expect(adapter.checkField('read', 'Post', { v: null }, 'secret', context())).resolves.toBe(false);
    await expect(adapter.checkField('read', 'Post', { v: 7 }, 'secret', context())).resolves.toBe(true);
  });

  it('treats not against a null field as a match', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, { status: { not: 'DRAFT' } } as never);
    });

    await expect(adapter.check('read', 'Post', { status: null }, context())).resolves.toBe(true);
    await expect(adapter.check('read', 'Post', { status: 'DRAFT' }, context())).resolves.toBe(false);
  });
});

describe('unsupported policy conditions are refused', () => {
  const unsupported: readonly [string, unknown][] = [
    ['search', { title: { search: 'a' } }],
    ['isSet', { title: { isSet: true } }],
  ];

  it.each(unsupported)('refuses %s and names the rule and the operator', async (operator, conditions) => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, conditions as never);
    });

    const error = await adapter
      .check('read', 'Post', { title: 'a' }, context())
      .then(() => null)
      .catch((caught: unknown) => caught as GolemPolicyRuleError);

    expect(error).toBeInstanceOf(GolemPolicyRuleError);
    expect(error?.message).toContain(UNSUPPORTED_POLICY_CONDITION_ERROR);
    expect(error?.message).toContain(`can('read', 'Post')`);
    expect(error?.message).toContain(`"${operator}"`);
    expect(error?.operators).toContain(operator);
  });

  it('names an inverted field rule precisely', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.cannot('read' as never, 'Post' as never, ['secret'] as never, {
        title: { search: 'a' },
      } as never);
    });

    const error = await adapter
      .check('read', 'Post', { title: 'a' }, context())
      .then(() => null)
      .catch((caught: unknown) => caught as GolemPolicyRuleError);

    expect(error?.message).toContain(`cannot('read', 'Post', ['secret'])`);
  });

  it('refuses an unsupported operator nested inside a combinator', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, {
        OR: [{ v: { equals: 1 } }, { title: { search: 'a' } }],
      } as never);
    });

    await expect(adapter.check('read', 'Post', { v: 1 }, context())).rejects.toThrow(/"search"/);
  });

  it.each([
    ['contains', { title: { contains: 'lph' } }, { title: 'alpha' }, { title: 'beta' }],
    ['startsWith', { title: { startsWith: 'al' } }, { title: 'alpha' }, { title: 'beta' }],
    ['endsWith', { title: { endsWith: 'ha' } }, { title: 'alpha' }, { title: 'beta' }],
    ['has', { tags: { has: 'a' } }, { tags: ['a', 'b'] }, { tags: ['b'] }],
    ['hasSome', { tags: { hasSome: ['a'] } }, { tags: ['a'] }, { tags: ['b'] }],
    ['isEmpty', { tags: { isEmpty: true } }, { tags: [] }, { tags: ['a'] }],
    [
      'insensitive contains',
      { title: { contains: 'LPH', mode: 'insensitive' } },
      { title: 'alpha' },
      { title: 'beta' },
    ],
    [
      'insensitive equals',
      { title: { equals: 'ALPHA', mode: 'insensitive' } },
      { title: 'alpha' },
      { title: 'beta' },
    ],
    [
      'insensitive in',
      { title: { in: ['ALPHA'], mode: 'insensitive' } },
      { title: 'alpha' },
      { title: 'beta' },
    ],
  ])('evaluates %s rather than refusing it', async (_name, conditions, matching, other) => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, conditions as never);
    });

    await expect(adapter.check('read', 'Post', matching, context())).resolves.toBe(true);
    await expect(adapter.check('read', 'Post', other, context())).resolves.toBe(false);
  });

  it('accepts a to-many hop under a supported relation operator', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, {
        author: { is: { posts: { some: { id: '1' } } } },
      } as never);
    });

    await expect(
      adapter.check('read', 'Post', { author: { posts: [{ id: '1' }] } }, context()),
    ).resolves.toBe(true);
    await expect(
      adapter.check('read', 'Post', { author: { posts: [{ id: '2' }] } }, context()),
    ).resolves.toBe(false);
  });

  it.each([
    ['some', { comments: { some: { id: '1' } } }, [{ id: '1' }], [{ id: '2' }]],
    ['every', { comments: { every: { id: '1' } } }, [{ id: '1' }], [{ id: '1' }, { id: '2' }]],
    ['none', { comments: { none: { id: '1' } } }, [{ id: '2' }], [{ id: '1' }]],
  ])('evaluates the %s quantifier rather than refusing it', async (_name, conditions, matching, other) => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, conditions as never);
    });

    await expect(adapter.check('read', 'Post', { comments: matching }, context())).resolves.toBe(true);
    await expect(adapter.check('read', 'Post', { comments: other }, context())).resolves.toBe(false);
  });
});

describe('validation timing', () => {
  it('does not see application rules at boot, because none exist yet', () => {
    expect(() =>
      bootedAdapter((builder) => {
        builder.can('read' as never, 'Post' as never, { title: { search: 'a' } } as never);
      }),
    ).not.toThrow();
  });

  it('validates every rule of an ability when that ability is built, not on demand', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never);
      builder.can('read' as never, 'Comment' as never, { body: { search: 'x' } } as never);
    });

    await expect(adapter.check('read', 'Post', { id: '1' }, context())).rejects.toThrow(GolemPolicyRuleError);
  });

  it('surfaces the refusal as a hard error, never as a denial', async () => {
    const adapter = bootedAdapter((builder) => {
      builder.can('read' as never, 'Post' as never, { title: { search: 'a' } } as never);
    });

    const outcome = await adapter
      .check('read', 'Post', { title: 'a' }, context())
      .then((value) => ({ value }))
      .catch((error: unknown) => ({ error }));

    expect(outcome).not.toEqual({ value: false });
    expect((outcome as { error: unknown }).error).toBeInstanceOf(GolemPolicyRuleError);
  });

  it('validates the rules of a consumer-supplied factory too', async () => {
    const adapter = bootedAdapter(
      (builder) => {
        builder.can('read' as never, 'Post' as never, { title: { search: 'a' } } as never);
      },
      { abilityFactory: () => new AbilityBuilder(createGolemAbility) },
    );

    await expect(adapter.check('read', 'Post', { title: 'a' }, context())).rejects.toThrow(
      GolemPolicyRuleError,
    );
  });
});

describe('conformance probe', () => {
  it('covers every operator and combinator in the table', () => {
    const covered = new Set(conformanceCases().flatMap((probe) => probe.operator.split('/')));

    for (const operator of SUPPORTED_OPERATORS) {
      expect(covered).toContain(operator);
    }
    for (const combinator of Object.keys(COMBINATORS)) {
      expect(covered).toContain(combinator);
    }
  });

  it('expects both matches and non-matches for every operator', () => {
    const outcomes = new Map<string, Set<boolean>>();
    for (const probe of conformanceCases()) {
      const seen = outcomes.get(probe.operator) ?? new Set<boolean>();
      seen.add(probe.expected);
      outcomes.set(probe.operator, seen);
    }

    for (const operator of SUPPORTED_OPERATORS) {
      expect([...(outcomes.get(operator) ?? [])].sort()).toEqual([false, true]);
    }
  });

  it('accepts the golem default factory', () => {
    expect(() => bootedAdapter()).not.toThrow();
  });

  it('accepts a consumer factory built on createGolemAbility', () => {
    expect(() => bootedAdapter(() => undefined, { abilityFactory: () => new AbilityBuilder(createGolemAbility) })).not.toThrow();
  });

  it('refuses a consumer factory that is not BigInt exact', () => {
    expect(() =>
      bootedAdapter(() => undefined, { abilityFactory: () => new AbilityBuilder(createPrismaAbility) }),
    ).toThrow(BIGINT_EXACT_ABILITY_ERROR);
  });

  it('refuses a BigInt-exact consumer factory whose matcher diverges elsewhere', () => {
    let message = '';
    try {
      bootedAdapter(() => undefined, { abilityFactory: () => new AbilityBuilder(createAbility) });
    } catch (error) {
      message = (error as Error).message;
    }

    expect(message).toContain(ABILITY_CONFORMANCE_ERROR);
    expect(message).toMatch(/operator "(lt|lte|gt|gte|equals|not|in|notIn|is|AND|OR|NOT)" diverged/);
  });

  it('names the operator that diverged for a matcher that always matches', () => {
    const always = () =>
      new AbilityBuilder((rules: unknown[]) => ({
        can: () => true,
        rulesFor: () => rules,
      })) as unknown as AbilityBuilder<never>;

    let message = '';
    try {
      bootedAdapter(() => undefined, { abilityFactory: always });
    } catch (error) {
      message = (error as Error).message;
    }

    expect(message).toContain(ABILITY_CONFORMANCE_ERROR);
    expect(message).toMatch(/operator "[^"]+" diverged/);
  });

  it('reports a factory that throws with the BigInt error and the original cause', () => {
    let error: Error | undefined;
    try {
      bootedAdapter(() => undefined, {
        abilityFactory: () => {
          throw new Error('factory exploded');
        },
      });
    } catch (caught) {
      error = caught as Error;
    }

    expect(error?.message).toBe(BIGINT_EXACT_ABILITY_ERROR);
    expect((error?.cause as Error | undefined)?.message).toBe('factory exploded');
  });
});

describe('factory installation', () => {
  it('installs the golem factory when the authenticator supplies none', () => {
    const service = new FakeAuthorizationService() as unknown as AuthorizationService;

    expect(installGolemAbilityFactory(service)).toBe('golem');
    expect(installGolemAbilityFactory(service)).toBe('golem');
  });

  it('keeps a consumer factory when the authenticator supplies one', () => {
    const service = new FakeAuthorizationService(() => undefined, {
      abilityFactory: () => new AbilityBuilder(createGolemAbility),
    }) as unknown as AuthorizationService;

    expect(installGolemAbilityFactory(service)).toBe('consumer');
  });
});
