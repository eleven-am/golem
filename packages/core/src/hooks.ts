export type GolemOperation =
  | 'findOne'
  | 'findMany'
  | 'create'
  | 'update'
  | 'delete'
  | 'updateMany'
  | 'deleteMany';

export const ALL_OPERATIONS: readonly GolemOperation[] = [
  'findOne',
  'findMany',
  'create',
  'update',
  'delete',
  'updateMany',
  'deleteMany',
];

export interface GolemHookContext {
  model: string;
  operation: GolemOperation;
  context?: unknown;
}

export type BeforeHook<TRequest = any> = (
  request: TRequest,
  ctx: GolemHookContext,
) => TRequest | void | Promise<TRequest | void>;

export type AfterHook = (result: unknown, ctx: GolemHookContext) => void | Promise<void>;

export class HookRegistry {
  private readonly beforeHooks = new Map<string, BeforeHook[]>();
  private readonly afterHooks = new Map<string, AfterHook[]>();

  private static key(model: string, operation: GolemOperation): string {
    return `${model}.${operation}`;
  }

  registerBefore(model: string, operation: GolemOperation, hook: BeforeHook): void {
    const key = HookRegistry.key(model, operation);
    const hooks = this.beforeHooks.get(key) ?? [];
    hooks.push(hook);
    this.beforeHooks.set(key, hooks);
  }

  registerAfter(model: string, operation: GolemOperation, hook: AfterHook): void {
    const key = HookRegistry.key(model, operation);
    const hooks = this.afterHooks.get(key) ?? [];
    hooks.push(hook);
    this.afterHooks.set(key, hooks);
  }

  beforeFor(model: string, operation: GolemOperation): readonly BeforeHook[] {
    return this.beforeHooks.get(HookRegistry.key(model, operation)) ?? [];
  }

  afterFor(model: string, operation: GolemOperation): readonly AfterHook[] {
    return this.afterHooks.get(HookRegistry.key(model, operation)) ?? [];
  }
}
