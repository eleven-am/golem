import { createHash } from 'node:crypto';
import { canonicalToken } from './canonical';
import { GolemConflictError, GolemValidationError } from './errors';

export const GOLEM_UPSERT_GUARD_MODEL = 'GolemUpsertGuard';
export const GOLEM_UPSERT_GUARD_DELEGATE = 'golemUpsertGuard';
export const DEFAULT_UPSERT_GUARD_STRIPES = 4_096;

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
  client: Record<string, unknown>,
  model: string,
  where: unknown,
  stripes: number,
  provider?: string,
): Promise<void> {
  const stripe = upsertGuardStripe(model, where, stripes);
  try {
    await upsertGuardDelegate(client).upsert({
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
