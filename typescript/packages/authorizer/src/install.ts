import type { AuthorizationService } from '@eleven-am/authorizer';
import { AbilityBuilderFactory } from './casl';
import { golemAbilityBuilderFactory, withRuleValidation } from './policy';

export type InstalledFactory = 'golem' | 'consumer';

interface AuthorizationServiceInternals {
  authenticator?: { abilityFactory?: AbilityBuilderFactory } | null;
  resolvedAbilityFactory?: () => AbilityBuilderFactory;
}

const installed = new WeakMap<object, InstalledFactory>();

export function installGolemAbilityFactory(service: AuthorizationService): InstalledFactory {
  const internals = service as unknown as AuthorizationServiceInternals;
  const already = installed.get(internals as object);
  if (already !== undefined) {
    return already;
  }
  const authenticator = internals.authenticator;
  const consumer = typeof authenticator?.abilityFactory === 'function' ? authenticator.abilityFactory : null;
  const base = consumer === null ? golemAbilityBuilderFactory : withRuleValidation(consumer.bind(authenticator));
  internals.resolvedAbilityFactory = () => base;
  const kind: InstalledFactory = consumer === null ? 'golem' : 'consumer';
  installed.set(internals as object, kind);
  return kind;
}
