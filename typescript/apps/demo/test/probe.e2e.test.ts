import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { AbilityBuilder, subject } from '@casl/ability';
import { createPrismaAbility } from '@casl/prisma';
import {
  Authenticator,
  AuthorizationModule,
  AuthorizationService,
  Authorizer,
  ResolvedAbility,
  ResolvedUser,
  WillAuthorize,
} from '@eleven-am/authorizer';
import { createAbility } from '@eleven-am/authorizer/prisma';
import {
  ABILITY_CONFORMANCE_ERROR,
  BIGINT_EXACT_ABILITY_ERROR,
  GolemAuthorizationAdapter,
  GolemPolicyRuleError,
  createGolemAbility,
} from '@eleven-am/golem-authorizer';

class NullAuthenticator implements Authenticator {
  async retrieveUser(): Promise<ResolvedUser | null> {
    return null;
  }
}

class SafeFactoryAuthenticator extends NullAuthenticator {
  abilityFactory(): AbilityBuilder<ResolvedAbility> {
    return new AbilityBuilder(createGolemAbility) as unknown as AbilityBuilder<ResolvedAbility>;
  }
}

class DivergentFactoryAuthenticator extends NullAuthenticator {
  abilityFactory(): AbilityBuilder<ResolvedAbility> {
    return new AbilityBuilder(createAbility) as unknown as AbilityBuilder<ResolvedAbility>;
  }
}

class UnsafeFactoryAuthenticator extends NullAuthenticator {
  abilityFactory(): AbilityBuilder<ResolvedAbility> {
    return new AbilityBuilder(createPrismaAbility) as unknown as AbilityBuilder<ResolvedAbility>;
  }
}

class ThrowingFactoryAuthenticator extends NullAuthenticator {
  abilityFactory(): AbilityBuilder<ResolvedAbility> {
    throw new Error('factory exploded');
  }
}

class MarkedFactoryAuthenticator extends NullAuthenticator {
  abilityFactory(): AbilityBuilder<ResolvedAbility> {
    const create = (
      rules?: Parameters<typeof createGolemAbility>[0],
      options?: Parameters<typeof createGolemAbility>[1],
    ) => Object.assign(createGolemAbility(rules, options), { marked: true });
    return new AbilityBuilder(create) as unknown as AbilityBuilder<ResolvedAbility>;
  }
}

class SignedInAuthenticator implements Authenticator {
  async retrieveUser(): Promise<ResolvedUser | null> {
    return { id: 'user-1' } as ResolvedUser;
  }
}

@Authorizer()
class UnsupportedRules implements WillAuthorize {
  forUser(user: ResolvedUser, builder: AbilityBuilder<ResolvedAbility>): void {
    const { can } = builder as unknown as AbilityBuilder<never>;
    can('read' as never, 'Post' as never);
    can('read' as never, 'Comment' as never, { body: { search: 'secret' } } as never);
  }
}

async function boot(
  authenticator: Authenticator,
  providers: unknown[] = [],
): Promise<INestApplication> {
  const moduleRef = await Test.createTestingModule({
    imports: [AuthorizationModule.forRoot(authenticator)],
    providers: [GolemAuthorizationAdapter, ...(providers as never[])],
  }).compile();
  const app = moduleRef.createNestApplication();
  await app.init();
  return app;
}

function abilityFor(app: INestApplication, conditions: unknown): ResolvedAbility {
  const service = app.get(AuthorizationService);
  const builder = service.resolvedAbilityFactory()() as unknown as AbilityBuilder<never>;
  builder.can('read' as never, 'Probe' as never, conditions as never);
  return builder.build() as unknown as ResolvedAbility;
}

describe('GolemAuthorizationAdapter startup conformance probe (e2e)', () => {
  it('boots when no abilityFactory is provided (safe default)', async () => {
    const app = await boot(new NullAuthenticator());
    expect(app.get(GolemAuthorizationAdapter)).toBeInstanceOf(GolemAuthorizationAdapter);
    await app.close();
  });

  it('boots with an explicit createGolemAbility factory', async () => {
    const app = await boot(new SafeFactoryAuthenticator());
    expect(app.get(GolemAuthorizationAdapter)).toBeInstanceOf(GolemAuthorizationAdapter);
    await app.close();
  });

  it('fails fast with plain createPrismaAbility from @casl/prisma', async () => {
    await expect(boot(new UnsafeFactoryAuthenticator())).rejects.toThrow(BIGINT_EXACT_ABILITY_ERROR);
  });

  it('fails fast with createAbility from @eleven-am/authorizer, whose matcher diverges from the golem table', async () => {
    await expect(boot(new DivergentFactoryAuthenticator())).rejects.toThrow(ABILITY_CONFORMANCE_ERROR);
    await expect(boot(new DivergentFactoryAuthenticator())).rejects.toThrow(/operator "[^"]+" diverged/);
  });

  it('installs the golem matcher on the real AuthorizationService', async () => {
    const app = await boot(new NullAuthenticator());

    const ability = abilityFor(app, { v: { lt: 5 } });

    expect(ability.can('read', subject('Probe', { v: 3 }))).toBe(true);
    expect(ability.can('read', subject('Probe', { v: null }))).toBe(false);
    await app.close();
  });

  it('keeps a conforming consumer factory instead of replacing it', async () => {
    const app = await boot(new MarkedFactoryAuthenticator());

    const ability = abilityFor(app, { v: { lt: 5 } });

    expect((ability as unknown as { marked?: boolean }).marked).toBe(true);
    expect(ability.can('read', subject('Probe', { v: null }))).toBe(false);
    await app.close();
  });

  it('refuses unsupported conditions from a consumer factory as well', async () => {
    const app = await boot(new MarkedFactoryAuthenticator());

    expect(() => abilityFor(app, { title: { search: 'a' } })).toThrow(GolemPolicyRuleError);
    await app.close();
  });

  it('refuses an unsupported condition when the per-user ability is built', async () => {
    const app = await boot(new SignedInAuthenticator(), [UnsupportedRules]);
    const service = app.get(AuthorizationService);

    await expect(service.getAbility({ req: {} } as never)).rejects.toThrow(GolemPolicyRuleError);
    await expect(service.getAbility({ req: {} } as never)).rejects.toThrow(/"search"/);
    await app.close();
  });

  it('rethrows with the original error as cause when the factory itself throws', async () => {
    let error: Error | undefined;
    try {
      const app = await boot(new ThrowingFactoryAuthenticator());
      await app.close();
    } catch (caught) {
      error = caught as Error;
    }
    expect(error).toBeInstanceOf(Error);
    expect(error?.message).toBe(BIGINT_EXACT_ABILITY_ERROR);
    expect((error?.cause as Error | undefined)?.message).toBe('factory exploded');
  });
});
