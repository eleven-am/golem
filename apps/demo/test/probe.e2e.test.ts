import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { AbilityBuilder } from '@casl/ability';
import { createPrismaAbility } from '@casl/prisma';
import { Authenticator, AuthorizationModule, ResolvedAbility, ResolvedUser } from '@eleven-am/authorizer';
import { createAbility } from '@eleven-am/authorizer/prisma';
import { BIGINT_EXACT_ABILITY_ERROR, GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';

class NullAuthenticator implements Authenticator {
  async retrieveUser(): Promise<ResolvedUser | null> {
    return null;
  }
}

class SafeFactoryAuthenticator extends NullAuthenticator {
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

async function boot(authenticator: Authenticator): Promise<INestApplication> {
  const moduleRef = await Test.createTestingModule({
    imports: [AuthorizationModule.forRoot(authenticator)],
    providers: [GolemAuthorizationAdapter],
  }).compile();
  const app = moduleRef.createNestApplication();
  await app.init();
  return app;
}

describe('GolemAuthorizationAdapter startup BigInt probe (e2e)', () => {
  it('boots when no abilityFactory is provided (safe default)', async () => {
    const app = await boot(new NullAuthenticator());
    expect(app.get(GolemAuthorizationAdapter)).toBeInstanceOf(GolemAuthorizationAdapter);
    await app.close();
  });

  it('boots with an explicit createAbility factory', async () => {
    const app = await boot(new SafeFactoryAuthenticator());
    expect(app.get(GolemAuthorizationAdapter)).toBeInstanceOf(GolemAuthorizationAdapter);
    await app.close();
  });

  it('fails fast with plain createPrismaAbility from @casl/prisma', async () => {
    await expect(boot(new UnsafeFactoryAuthenticator())).rejects.toThrow(BIGINT_EXACT_ABILITY_ERROR);
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
