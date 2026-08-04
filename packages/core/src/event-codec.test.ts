import { isDecimalLike } from '@eleven-am/golem-policy';
import { prismaDecimal } from './compiled-read-decode';
import {
  decodeGolemEventMessage,
  encodeGolemEventMessage,
  encodedGolemEventBytes,
  GolemEventCodecError,
} from './event-codec';
import { GolemEventBatch, GolemEventPayload, isGolemEventBatch } from './events';

function jsonRoundTrip<T>(value: T): unknown {
  return JSON.parse(JSON.stringify(value)) as unknown;
}

describe('the versioned event wire protocol', () => {
  it('round-trips every supported typed value through JSON', () => {
    const Decimal = prismaDecimal();
    expect(Decimal).not.toBeNull();
    const sparse: unknown[] = new Array(2);
    sparse[1] = undefined;
    const payload: GolemEventPayload = {
      type: 'DELETED',
      model: 'LedgerEntry',
      id: {
        accountId: 900719925474099312345n,
        bookedAt: new Date('2026-08-04T18:15:00.000Z'),
      },
      entity: {
        amount: new Decimal!('12345678901234567890.123456789'),
        digest: Uint8Array.from([0, 1, 254, 255]),
        sparse,
      },
    };

    const encoded = encodeGolemEventMessage(payload);
    expect(() => JSON.stringify(encoded)).not.toThrow();
    const decoded = decodeGolemEventMessage(jsonRoundTrip(encoded));
    expect(isGolemEventBatch(decoded)).toBe(false);
    const event = decoded as GolemEventPayload;
    expect(event.type).toBe('DELETED');
    expect(event.id).toMatchObject({
      accountId: 900719925474099312345n,
      bookedAt: new Date('2026-08-04T18:15:00.000Z'),
    });
    expect(isDecimalLike(event.entity!.amount)).toBe(true);
    expect(String(event.entity!.amount)).toBe('12345678901234567890.123456789');
    expect(Array.from(event.entity!.digest as Uint8Array)).toEqual([0, 1, 254, 255]);
    const restoredSparse = event.entity!.sparse as unknown[];
    expect(0 in restoredSparse).toBe(false);
    expect(1 in restoredSparse).toBe(true);
    expect(restoredSparse[1]).toBeUndefined();
    expect(encodedGolemEventBytes(payload)).toBe(Buffer.byteLength(JSON.stringify(encoded), 'utf8'));
  });

  it('round-trips a deterministic batch envelope', () => {
    const batch: GolemEventBatch = {
      kind: 'batch',
      events: [
        { type: 'UPDATED', model: 'User', id: 'u1' },
        { type: 'UPDATED', model: 'User', id: 'u2' },
      ],
    };

    const decoded = decodeGolemEventMessage(jsonRoundTrip(encodeGolemEventMessage(batch)));
    expect(decoded).toEqual(batch);
  });

  it('refuses unknown protocol versions and malformed payloads', () => {
    const encoded = encodeGolemEventMessage({ type: 'CREATED', model: 'User', id: 'u1' });
    expect(() => decodeGolemEventMessage({ ...encoded, version: 2 })).toThrow(
      'unsupported Golem event protocol version 2',
    );
    expect(() =>
      decodeGolemEventMessage({ ...encoded, body: { tag: 'object', value: [] } }),
    ).toThrow(GolemEventCodecError);
  });
});
