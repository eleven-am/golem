import { Injectable, OnModuleInit } from '@nestjs/common';
import { subject } from '@casl/ability';
import {
  AuthorizationProvider,
  GolemAction,
  GolemForbiddenError,
  GolemUnauthorizedError,
  FieldClassification,
} from '@eleven-am/golem-core';
import { AuthorizationService } from '@eleven-am/authorizer';
import { PrismaAuthorizationService } from '@eleven-am/authorizer/prisma';
import { golemContextStore, ensureGolemTransportRegistered, wrapFresh } from './transport';

export { ensureGolemTransportRegistered } from './transport';

export const BIGINT_EXACT_ABILITY_ERROR =
  'Golem authorization requires a BigInt-exact ability factory: the configured Authenticator.abilityFactory() does not match a rule condition { equals: 1n } against a numeric 1, so in-memory checks would be wrong for BigInt columns. Use createAbility from @eleven-am/authorizer/prisma, or omit abilityFactory to use the safe default.';

function translate(error: unknown): never {
  const status =
    typeof (error as { getStatus?: () => number })?.getStatus === 'function'
      ? (error as { getStatus: () => number }).getStatus()
      : undefined;
  const message = error instanceof Error ? error.message : 'Authorization failed';
  if (status === 401) {
    throw new GolemUnauthorizedError(message);
  }
  if (status === 403) {
    throw new GolemForbiddenError(message);
  }
  throw error;
}

@Injectable()
export class GolemAuthorizationAdapter implements AuthorizationProvider, OnModuleInit {
  private readonly port: PrismaAuthorizationService;

  constructor(private readonly authorizationService: AuthorizationService) {
    ensureGolemTransportRegistered();
    this.port = new PrismaAuthorizationService(authorizationService);
  }

  onModuleInit(): void {
    const factory = this.authorizationService.resolvedAbilityFactory();
    let ability: ResolvedAbilityLike;
    try {
      const builder = factory();
      builder.can('read', 'GolemBigIntProbe', { v: { equals: 1n } });
      ability = builder.build() as unknown as ResolvedAbilityLike;
    } catch (error) {
      throw new Error(BIGINT_EXACT_ABILITY_ERROR, { cause: error });
    }
    if (!ability.can('read', subject('GolemBigIntProbe', { v: 1 }))) {
      throw new Error(BIGINT_EXACT_ABILITY_ERROR);
    }
  }

  private ability(context: unknown): Promise<ResolvedAbilityLike> {
    const store = golemContextStore(context);
    const key = '__golemResolvedAbility';
    if (!store) {
      return this.authorizationService.getAbility(context as never) as unknown as Promise<ResolvedAbilityLike>;
    }
    const existing = store[key] as Promise<ResolvedAbilityLike> | undefined;
    if (existing) return existing;
    const pending = this.authorizationService.getAbility(context as never) as unknown as Promise<ResolvedAbilityLike>;
    store[key] = pending;
    return pending;
  }

  async authorize(action: GolemAction, model: string, context: unknown): Promise<void> {
    try {
      await this.port.authorize(action, model, context as never);
    } catch (error) {
      translate(error);
    }
  }

  async constrain(action: GolemAction, model: string, context: unknown): Promise<unknown> {
    try {
      return await this.port.constrain(action, model, context as never);
    } catch (error) {
      translate(error);
    }
  }

  async check(action: GolemAction, model: string, entity: unknown, context: unknown): Promise<boolean> {
    try {
      const ability = await this.ability(context);
      return ability.can(action, subject(model, entity as object));
    } catch (error) {
      translate(error);
    }
  }

  async checkField(
    action: GolemAction,
    model: string,
    entity: unknown,
    field: string,
    context: unknown,
  ): Promise<boolean> {
    try {
      const ability = await this.ability(context);
      return ability.can(action, subject(model, entity as object), field);
    } catch (error) {
      translate(error);
    }
  }

  async classifyFields(
    action: GolemAction,
    model: string,
    fields: readonly string[],
    context: unknown,
  ): Promise<Record<string, FieldClassification>> {
    try {
      const ability = await this.ability(context);
      const rulesFor = (
        ability as unknown as { rulesFor(action: string, subject: string, field: string): CaslRule[] }
      ).rulesFor.bind(ability);
      const result: Record<string, FieldClassification> = {};
      for (const field of fields) {
        const chain = rulesFor(action, model, field);
        let classified: FieldClassification | undefined;
        const requires = new Set<string>();
        for (const rule of chain) {
          if (rule.conditions) {
            collectConditionKeys(rule.conditions, requires);
            continue;
          }
          if (requires.size === 0) {
            classified = { access: rule.inverted ? 'never' : 'always' };
          } else {
            for (const later of chain.slice(chain.indexOf(rule))) {
              collectConditionKeys(later.conditions, requires);
            }
            classified = { access: 'conditional', requires: [...requires] };
          }
          break;
        }
        result[field] =
          classified ??
          (requires.size > 0
            ? { access: 'conditional', requires: [...requires] }
            : { access: 'never' });
      }
      return result;
    } catch (error) {
      translate(error);
    }
  }

  freshContext(context: unknown): unknown {
    return wrapFresh(context);
  }
}

interface CaslRule {
  fields?: string[];
  conditions?: unknown;
  inverted: boolean;
}

interface ResolvedAbilityLike {
  can(action: string, subject: unknown, field?: string): boolean;
  rulesFor(action: string, subject: string, field: string): readonly CaslRule[];
}

function collectConditionKeys(conditions: unknown, into: Set<string>): void {
  if (!conditions || typeof conditions !== 'object') {
    return;
  }
  for (const [key, value] of Object.entries(conditions)) {
    if (key === 'AND' || key === 'OR' || key === 'NOT') {
      const branches = Array.isArray(value) ? value : [value];
      for (const branch of branches) {
        collectConditionKeys(branch, into);
      }
    } else {
      into.add(key);
    }
  }
}
