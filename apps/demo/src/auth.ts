import { AbilityBuilder } from '@casl/ability';
import { createPrismaAbility } from '@casl/prisma';
import {
  Authenticator,
  AuthorizationContext,
  Authorizer,
  ResolvedAbility,
  ResolvedUser,
  WillAuthorize,
} from '@eleven-am/authorizer';
import { GolemPrismaService } from './generated/golem/client';

interface DemoUser {
  id: string;
  email: string;
  name: string | null;
}

function tokenFrom(requestLike: unknown): string | null {
  const source = requestLike as {
    headers?: Record<string, string>;
    connectionParams?: Record<string, string>;
  } | null;
  return source?.headers?.authorization ?? source?.connectionParams?.authorization ?? null;
}

export class DemoAuthenticator implements Authenticator {
  constructor(private readonly prisma: GolemPrismaService) {}

  async retrieveUser(context: AuthorizationContext): Promise<ResolvedUser | null> {
    const token = tokenFrom(context.getRequestLike());
    if (!token?.startsWith('token-')) {
      return null;
    }
    const email = token.slice('token-'.length);
    return this.prisma.user.findUnique({ where: { email } });
  }

  abilityFactory(): AbilityBuilder<ResolvedAbility> {
    return new AbilityBuilder(createPrismaAbility) as unknown as AbilityBuilder<ResolvedAbility>;
  }
}

@Authorizer()
export class DemoRules implements WillAuthorize {
  forUser(user: ResolvedUser, builder: AbilityBuilder<ResolvedAbility>): void {
    const demoUser = user as DemoUser;
    const { can, cannot } = builder;
    if (demoUser.email === 'roy@example.com') {
      can('manage', 'all');
      return;
    }
    if (demoUser.name === 'REVOKED') {
      return;
    }
    if (demoUser.email === 'guest@example.com') {
      can('read', 'User', ['id', 'email', 'name']);
      can('read', 'Post', { published: true });
      can('create', 'Post');
    } else {
      can('read', 'User');
      cannot('read', 'User', ['phone']);
      can('read', 'User', ['phone'], { id: demoUser.id });
      if (demoUser.email === 'mod@example.com') {
        can('read', 'Post');
        can('update', 'Post', ['published']);
      } else {
        can('read', 'Post');
        can('update', 'User', { id: demoUser.id });
        can('create', 'Post', { type: 'PERSONAL', authorId: demoUser.id });
      }
    }
    can(['update', 'delete'], 'Post', { authorId: demoUser.id, type: 'PERSONAL' });
    can('read', 'Profile', { userId: demoUser.id });
  }
}
