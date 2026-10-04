import type { DatamodelField, DatamodelModel, GolemProvider } from './datamodel';
import { createHash } from 'node:crypto';
import { canonicalToken } from './canonical';
import { GolemConflictError, GolemValidationError } from './errors';

export const GOLEM_UPSERT_GUARD_MODEL = 'GolemUpsertGuard';
export const GOLEM_UPSERT_GUARD_DELEGATE = 'golemUpsertGuard';
export const DEFAULT_UPSERT_GUARD_STRIPES = 4_096;

const guardColumn = (name: string, type: string): DatamodelField => ({
  name,
  kind: 'scalar',
  type,
  isList: false,
  isRequired: true,
  isUnique: false,
  isId: name === 'stripe',
  hasDefaultValue: name === 'seq',
  isReadOnly: false,
  isUpdatedAt: false,
});

export const GOLEM_UPSERT_GUARD_TABLE: DatamodelModel = Object.freeze({
  name: GOLEM_UPSERT_GUARD_MODEL,
  dbName: '_golem_upsert_guard',
  fields: [guardColumn('stripe', 'Int'), guardColumn('seq', 'BigInt')],
});

export function validateUpsertGuardStripes(value: number): number {
  if (!Number.isSafeInteger(value) || value < 1) {
    throw new Error('upsertGuardStripes must be a positive safe integer');
  }
  return value;
}

export function upsertGuardStripe(
  model: string,
  where: unknown,
  stripes = DEFAULT_UPSERT_GUARD_STRIPES,
): number {
  const count = validateUpsertGuardStripes(stripes);
  const key = canonicalToken({ model, where });
  const digest = createHash('sha256').update(key, 'utf8').digest();
  return Number(digest.readBigUInt64BE(0) % BigInt(count));
}

export interface UpsertGuardDelegate {
  upsert(args: {
    where: { stripe: number };
    create: { stripe: number; seq: bigint };
    update: { seq: { increment: bigint } };
    select: { stripe: true };
  }): Promise<unknown>;
  createMany(args: { data: { stripe: number; seq: bigint }[]; skipDuplicates: true }): Promise<unknown>;
  findFirst?(args: { select: { stripe: true } }): Promise<unknown>;
}

export function upsertGuardDelegate(client: Record<string, unknown>): UpsertGuardDelegate {
  const delegate = client[GOLEM_UPSERT_GUARD_DELEGATE] as UpsertGuardDelegate | undefined;
  if (!delegate || typeof delegate.upsert !== 'function') {
    throw new GolemValidationError(
      'Serialized context-aware upsert requires the GolemUpsertGuard model and migration',
    );
  }
  return delegate;
}

export async function acquireUpsertGuard(
  guard: UpsertGuardDelegate,
  model: string,
  where: unknown,
  stripes: number,
  provider: GolemProvider | undefined,
  lock: (row: { stripe: number }) => Promise<void>,
): Promise<void> {
  const stripe = upsertGuardStripe(model, where, stripes);
  switch (provider) {
    case 'postgresql':
      await guard.createMany({ data: [{ stripe, seq: 0n }], skipDuplicates: true });
      await lock({ stripe });
      return;
    case 'sqlite':
    case undefined:
      return bumpUpsertGuard(guard, stripe, provider);
  }
}

async function bumpUpsertGuard(
  guard: UpsertGuardDelegate,
  stripe: number,
  provider: GolemProvider | undefined,
): Promise<void> {
  try {
    await guard.upsert({
      where: { stripe },
      create: { stripe, seq: 1n },
      update: { seq: { increment: 1n } },
      select: { stripe: true },
    });
  } catch (error) {
    if (error instanceof GolemValidationError) throw error;
    const candidate = error as { code?: unknown; message?: unknown };
    const message = typeof candidate?.message === 'string' ? candidate.message : '';
    if (
      provider === 'sqlite' &&
      (candidate?.code === 'P1008' ||
        candidate?.code === 'P2028' ||
        /SQLITE_(?:BUSY|LOCKED|BUSY_SNAPSHOT)|database is locked/i.test(message))
    ) {
      throw new GolemConflictError(
        'Serialized context-aware upsert could not acquire its SQLite guard in the current transaction',
      );
    }
    throw error;
  }
}

export async function validateUpsertGuardInfrastructure(
  client: Record<string, unknown>,
): Promise<void> {
  const delegate = upsertGuardDelegate(client);
  if (typeof delegate.findFirst !== 'function') {
    throw new GolemValidationError(
      'Serialized context-aware upsert requires a queryable GolemUpsertGuard delegate',
    );
  }
  try {
    await delegate.findFirst({ select: { stripe: true } });
  } catch (error) {
    throw new GolemValidationError(
      `Serialized context-aware upsert guard validation failed: ${(error as Error).message}`,
    );
  }
}
