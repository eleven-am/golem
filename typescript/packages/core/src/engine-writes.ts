import { AsyncLocalStorage } from 'node:async_hooks';
import { CanonicalValueError, canonicalToken } from './canonical';

interface EngineWrite {
  readonly model: string;
  readonly operation: string;
  readonly fingerprint: string;
  claimed: boolean;
}

const GUARDED_OPERATIONS: ReadonlySet<string> = new Set(['create', 'update', 'updateMany']);

const issued = new AsyncLocalStorage<EngineWrite>();

export type EngineDelegate = Record<string, (args: unknown) => Promise<any>>;

function fingerprintOf(args: unknown): string | undefined {
  try {
    return canonicalToken(args);
  } catch (error) {
    if (error instanceof CanonicalValueError) {
      return undefined;
    }
    throw error;
  }
}

function issue(model: string, operation: string, args: unknown, write: () => Promise<any>): Promise<any> {
  const fingerprint = fingerprintOf(args);
  if (fingerprint === undefined) {
    return write();
  }
  return issued.run({ model, operation, fingerprint, claimed: false }, write);
}

export function engineDelegate(model: string, delegate: EngineDelegate): EngineDelegate {
  return new Proxy(delegate, {
    get: (target, name, receiver) => {
      const member = Reflect.get(target, name, receiver);
      if (typeof name !== 'string' || !GUARDED_OPERATIONS.has(name) || typeof member !== 'function') {
        return member;
      }
      return (args: unknown) => issue(model, name, args, async () => await member.call(target, args));
    },
  });
}

export function claimEngineWrite(model: string, operation: string, args: unknown): boolean {
  const write = issued.getStore();
  if (!write || write.claimed || write.model !== model || write.operation !== operation) {
    return false;
  }
  if (fingerprintOf(args) !== write.fingerprint) {
    return false;
  }
  write.claimed = true;
  return true;
}
