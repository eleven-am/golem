import { AsyncLocalStorage } from 'node:async_hooks';

interface EngineWrite {
  readonly model: string;
  readonly operation: string;
  claimed: boolean;
}

const GUARDED_OPERATIONS: ReadonlySet<string> = new Set(['create', 'update', 'updateMany']);

const issued = new AsyncLocalStorage<EngineWrite>();

export type EngineDelegate = Record<string, (args: unknown) => Promise<any>>;

export function engineDelegate(model: string, delegate: EngineDelegate): EngineDelegate {
  return new Proxy(delegate, {
    get: (target, name, receiver) => {
      const member = Reflect.get(target, name, receiver);
      if (typeof name !== 'string' || !GUARDED_OPERATIONS.has(name) || typeof member !== 'function') {
        return member;
      }
      return (args: unknown) =>
        issued.run({ model, operation: name, claimed: false }, async () => await member.call(target, args));
    },
  });
}

export function claimEngineWrite(model: string, operation: string): boolean {
  const write = issued.getStore();
  if (!write || write.claimed || write.model !== model || write.operation !== operation) {
    return false;
  }
  write.claimed = true;
  return true;
}
