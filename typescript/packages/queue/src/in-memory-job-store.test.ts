import { InMemoryJobStore } from './in-memory-job-store';

const NOW = new Date('2026-01-01T00:00:00.000Z');
const LATER = new Date(NOW.getTime() + 60_000);

async function seed(
  store: InMemoryJobStore,
  id: string,
  scopeId: string | null,
): Promise<void> {
  await store.create({
    id,
    type: 'message.send',
    payload: '{}',
    scopeType: scopeId === null ? null : 'Account',
    scopeId,
    runAt: NOW,
    dedupeKey: null,
    maxAttempts: 3,
  });
}

function claim(store: InMemoryJobStore, id: string, owner: string) {
  return store.claim({
    id,
    fromStatus: 'PENDING',
    now: NOW,
    leaseOwner: owner,
    leaseExpiresAt: LATER,
    serializeScope: true,
  });
}

describe('InMemoryJobStore scope serialization', () => {
  it('refuses a second claim while the scope holds a live lease', async () => {
    const store = new InMemoryJobStore();
    await seed(store, 'a', 'account-1');
    await seed(store, 'b', 'account-1');

    expect(await claim(store, 'a', 'worker-1')).toBe(true);
    expect(await claim(store, 'b', 'worker-2')).toBe(false);
  });

  it('allows a different scope to run concurrently', async () => {
    const store = new InMemoryJobStore();
    await seed(store, 'a', 'account-1');
    await seed(store, 'b', 'account-2');

    expect(await claim(store, 'a', 'worker-1')).toBe(true);
    expect(await claim(store, 'b', 'worker-2')).toBe(true);
  });

  it('does not let a crashed worker block its scope forever', async () => {
    const store = new InMemoryJobStore();
    await seed(store, 'a', 'account-1');
    await seed(store, 'b', 'account-1');
    await claim(store, 'a', 'worker-1');

    const afterExpiry = new Date(LATER.getTime() + 1);
    const claimed = await store.claim({
      id: 'b',
      fromStatus: 'PENDING',
      now: afterExpiry,
      leaseOwner: 'worker-2',
      leaseExpiresAt: new Date(afterExpiry.getTime() + 60_000),
      serializeScope: true,
    });

    expect(claimed).toBe(true);
  });

  it('lets a job reclaim its own expired lease rather than blocking on itself', async () => {
    const store = new InMemoryJobStore();
    await seed(store, 'a', 'account-1');
    await claim(store, 'a', 'worker-1');

    const afterExpiry = new Date(LATER.getTime() + 1);
    const reclaimed = await store.claim({
      id: 'a',
      fromStatus: 'RUNNING',
      now: afterExpiry,
      leaseOwner: 'worker-2',
      leaseExpiresAt: new Date(afterExpiry.getTime() + 60_000),
      attempts: 1,
      lastError: 'The previous worker lease expired',
      serializeScope: true,
    });

    expect(reclaimed).toBe(true);
  });

  it('leaves unscoped jobs unserialized', async () => {
    const store = new InMemoryJobStore();
    await seed(store, 'a', null);
    await seed(store, 'b', null);

    expect(await claim(store, 'a', 'worker-1')).toBe(true);
    expect(await claim(store, 'b', 'worker-2')).toBe(true);
  });

  it('does not serialize when the flag is absent', async () => {
    const store = new InMemoryJobStore();
    await seed(store, 'a', 'account-1');
    await seed(store, 'b', 'account-1');

    const base = {
      fromStatus: 'PENDING' as const,
      now: NOW,
      leaseExpiresAt: LATER,
    };
    expect(await store.claim({ ...base, id: 'a', leaseOwner: 'worker-1' })).toBe(true);
    expect(await store.claim({ ...base, id: 'b', leaseOwner: 'worker-2' })).toBe(true);
  });
});

describe('InMemoryJobStore lease fence', () => {
  async function claimUntil(store: InMemoryJobStore, id: string, leaseExpiresAt: Date) {
    await seed(store, id, null);
    expect(
      await store.claim({ id, fromStatus: 'PENDING', now: new Date(), leaseOwner: 'worker-1', leaseExpiresAt }),
    ).toBe(true);
  }

  it('refuses every outcome from a holder whose lease has expired', async () => {
    const store = new InMemoryJobStore();
    await claimUntil(store, 'gone', new Date(Date.now() - 1));
    const owned = { id: 'gone', leaseOwner: 'worker-1' };

    expect(await store.complete(owned)).toBe(false);
    expect(await store.fail({ ...owned, attempts: 1, lastError: 'late' })).toBe(false);
    expect(await store.retry({ ...owned, attempts: 1, lastError: 'late', runAt: new Date() })).toBe(false);
    expect(await store.findOwned(owned)).toBeNull();
    expect(store.get('gone')?.status).toBe('RUNNING');
    expect(store.get('gone')?.lastError).toBeNull();
  });

  it('accepts the outcome from a holder whose lease is live', async () => {
    const store = new InMemoryJobStore();
    await claimUntil(store, 'live', new Date(Date.now() + 60_000));

    expect(await store.complete({ id: 'live', leaseOwner: 'worker-1' })).toBe(true);
    expect(store.get('live')?.status).toBe('SUCCEEDED');
  });
});
