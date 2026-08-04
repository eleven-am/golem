import { TransportAdapter, TransportContext, registerTransportAdapter } from '@eleven-am/authorizer';

const FRESH_MARKER = '__golemAuthFresh';

export interface FreshContextWrapper {
  [FRESH_MARKER]: true;
  store: Record<string, unknown>;
  inner: unknown;
}

export function isFreshWrapper(context: unknown): context is FreshContextWrapper {
  return !!context && typeof context === 'object' && (context as Record<string, unknown>)[FRESH_MARKER] === true;
}

export function wrapFresh(context: unknown): FreshContextWrapper {
  return {
    [FRESH_MARKER]: true,
    store: {},
    inner: isFreshWrapper(context) ? context.inner : context,
  };
}

export function golemContextStore(context: unknown): Record<string, unknown> | undefined {
  if (!context || typeof context !== 'object') return undefined;
  if (isFreshWrapper(context)) return context.store;
  const base = context as Record<string, unknown>;
  const request = (base.req ?? base.request) as Record<string, unknown> | undefined;
  return request ?? base;
}

class GolemTransportContext implements TransportContext {
  readonly type = 'golem';
  private readonly store: Record<string, unknown>;
  private readonly requestLike: unknown;

  constructor(context: unknown) {
    const base = (isFreshWrapper(context) ? context.inner : context) as Record<string, unknown> | null;
    const request = (base?.req ?? base?.request ?? null) as Record<string, unknown> | null;
    this.requestLike = request ?? base;
    this.store = golemContextStore(context) ?? {};
  }

  getClass(): unknown {
    return undefined;
  }

  getHandler(): unknown {
    return undefined;
  }

  getData<T>(key: string): T | null {
    return key in this.store ? (this.store[key] as T) : null;
  }

  setData<T>(key: string, value: T): void {
    this.store[key] = value;
  }

  getRequestLike(): unknown {
    return this.requestLike;
  }
}

const golemTransportAdapter: TransportAdapter = {
  type: 'golem',
  matches: (context) =>
    !!context &&
    typeof context === 'object' &&
    typeof (context as { getType?: unknown }).getType !== 'function',
  create: (context) => new GolemTransportContext(context),
};

let registered = false;

export function ensureGolemTransportRegistered(): void {
  if (registered) {
    return;
  }
  registered = true;
  registerTransportAdapter(golemTransportAdapter);
}
