import { AsyncLocalStorage } from 'node:async_hooks';

export const golemSharedContext: unique symbol = Symbol.for('@eleven-am/golem.shared-context');

const boundaries = new AsyncLocalStorage<object>();
const contextBindings = new WeakMap<object, object>();
const requestBindings = new WeakMap<object, object>();

export const SHARED_CONTEXT_MESSAGE =
  'The GraphQL context handed to Golem carries a request that is not the one being served. ' +
  'This happens when GraphQLModule is configured with a static context object: @nestjs/apollo reuses that object for every request, ' +
  'so ctx.req keeps the first caller forever, every later caller is served with the first caller\'s identity, ' +
  'and Golem\'s per-request caches lose their request scoping. ' +
  'Configure context as a function so each request builds its own object, e.g. context: ({ req }) => ({ req }). ' +
  'A context deliberately shared across requests must carry [golemSharedContext]: true.';

export function golemRequestBoundary(request: unknown, response: unknown, next: () => void): void {
  const boundary = {};
  if (typeof request === 'object' && request !== null) {
    requestBindings.set(request, boundary);
  }
  boundaries.run(boundary, next);
}

function boundaryCarriedBy(ctx: object): object | undefined {
  const req = (ctx as { req?: { raw?: unknown } }).req;
  if (typeof req !== 'object' || req === null) {
    return undefined;
  }
  const direct = requestBindings.get(req);
  if (direct) {
    return direct;
  }
  const raw = req.raw;
  return typeof raw === 'object' && raw !== null ? requestBindings.get(raw) : undefined;
}

export function assertContextWithinRequest(ctx: unknown): void {
  if (typeof ctx !== 'object' || ctx === null) {
    return;
  }
  if ((ctx as Record<PropertyKey, unknown>)[golemSharedContext] === true) {
    return;
  }
  const boundary = boundaries.getStore();
  if (!boundary) {
    return;
  }
  const carried = boundaryCarriedBy(ctx);
  if (carried && carried !== boundary) {
    throw new Error(SHARED_CONTEXT_MESSAGE);
  }
  const bound = contextBindings.get(ctx);
  if (!bound) {
    contextBindings.set(ctx, boundary);
    return;
  }
  if (bound !== boundary) {
    throw new Error(SHARED_CONTEXT_MESSAGE);
  }
}
