import { AsyncLocalStorage } from 'node:async_hooks';
import { CanonicalValueError, canonicalToken } from './canonical';

interface EngineWrite {
  readonly model: string;
  readonly operation: string;
  readonly fingerprint: string;
  offered: boolean;
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

async function issue(model: string, operation: string, args: unknown, write: () => Promise<any>): Promise<any> {
  const fingerprint = fingerprintOf(args);
  if (fingerprint === undefined) {
    return issued.exit(write);
  }
  const record: EngineWrite = { model, operation, fingerprint, offered: false, claimed: false };
  const result = await issued.run(record, write);
  if (record.offered && !record.claimed) {
    throw new Error(
      `The client the engine wrote ${operation} on ${model} through did not issue the write it guarded`,
    );
  }
  return result;
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
  write.offered = true;
  if (fingerprintOf(args) !== write.fingerprint) {
    return false;
  }
  write.claimed = true;
  return true;
}
