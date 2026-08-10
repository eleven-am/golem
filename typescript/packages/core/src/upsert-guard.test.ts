import { GolemConflictError, GolemValidationError } from './errors';
import {
  acquireUpsertGuard,
  upsertGuardStripe,
  validateUpsertGuardInfrastructure,
  validateUpsertGuardStripes,
} from './upsert-guard';

describe('serialized upsert guard keys', () => {
  it('is stable, model-sensitive, type-sensitive, and bounded', () => {
    const where = { email: 'secret@example.com' };
    const first = upsertGuardStripe('User', where, 17);
    expect(upsertGuardStripe('User', { email: 'secret@example.com' }, 17)).toBe(first);
    expect(first).toBeGreaterThanOrEqual(0);
    expect(first).toBeLessThan(17);
    expect(upsertGuardStripe('Post', where, 17)).not.toBe(first);
    expect(upsertGuardStripe('User', { id: 7 }, 4096))
      .not.toBe(upsertGuardStripe('User', { id: '7' }, 4096));
  });

  it('rejects invalid stripe counts', () => {
    for (const value of [0, -1, 1.5, Number.MAX_SAFE_INTEGER + 1]) {
      expect(() => validateUpsertGuardStripes(value)).toThrow(
        'upsertGuardStripes must be a positive safe integer',
      );
    }
  });

  it('persists only a bounded stripe and sequence increment, never selector material', async () => {
    const upsert = jest.fn().mockResolvedValue({ stripe: 3 });
    await acquireUpsertGuard(
      { golemUpsertGuard: { upsert } },
      'User',
      { email: 'secret@example.com' },
      8,
      'postgresql',
    );

    expect(upsert).toHaveBeenCalledWith({
      where: { stripe: expect.any(Number) },
      create: { stripe: expect.any(Number), seq: 1n },
      update: { seq: { increment: 1n } },
      select: { stripe: true },
    });
    expect(JSON.stringify(upsert.mock.calls[0], (_key, value) =>
      typeof value === 'bigint' ? value.toString() : value,
    )).not.toContain('secret@example.com');
  });

  it('normalizes SQLite lock and snapshot failures to a stable conflict', async () => {
    const upsert = jest.fn().mockRejectedValue(new Error('database is locked: SQLITE_BUSY_SNAPSHOT'));
    await expect(acquireUpsertGuard(
      { golemUpsertGuard: { upsert } },
      'User',
      { email: 'x@example.com' },
      4096,
      'sqlite',
    )).rejects.toBeInstanceOf(GolemConflictError);
  });
});

describe('serialized upsert infrastructure validation', () => {
  it('fails clearly when the generated delegate is absent', async () => {
    await expect(validateUpsertGuardInfrastructure({}))
      .rejects.toBeInstanceOf(GolemValidationError);
  });

  it('checks the migrated table without creating a guard row', async () => {
    const findFirst = jest.fn().mockResolvedValue(null);
    await validateUpsertGuardInfrastructure({
      golemUpsertGuard: { upsert: jest.fn(), findFirst },
    });
    expect(findFirst).toHaveBeenCalledWith({ select: { stripe: true } });
  });

  it('turns a missing table into a stable startup validation error', async () => {
    await expect(validateUpsertGuardInfrastructure({
      golemUpsertGuard: {
        upsert: jest.fn(),
        findFirst: jest.fn().mockRejectedValue(new Error('table does not exist')),
      },
    })).rejects.toThrow('Serialized context-aware upsert guard validation failed: table does not exist');
  });
});
