import { canonicalToken, CanonicalValueError, isScalarValue, sameValue } from './canonical';
import { prismaDecimal } from './compiled-read-decode';

describe('canonical typed values', () => {
  it('is independent of object key order while preserving scalar types', () => {
    expect(canonicalToken({ b: 2, a: '1' })).toBe(canonicalToken({ a: '1', b: 2 }));
    expect(canonicalToken('1')).not.toBe(canonicalToken(1));
    expect(canonicalToken(1)).not.toBe(canonicalToken(1n));
    expect(canonicalToken(-0)).not.toBe(canonicalToken(0));
  });

  it('covers the database values used by selectors and event payloads', () => {
    const Decimal = prismaDecimal();
    expect(Decimal).not.toBeNull();
    const value = {
      at: new Date('2026-08-04T18:00:00.000Z'),
      bytes: Uint8Array.from([0, 127, 255]),
      decimal: new Decimal!('1234567890.123456789'),
      invalid: new Date(Number.NaN),
    };

    expect(canonicalToken(value)).toContain('date:2026-08-04T18:00:00.000Z');
    expect(canonicalToken(value)).toContain('bytes:AH//');
    expect(canonicalToken(value)).toContain('decimal:"1234567890.123456789"');
    expect(canonicalToken(value)).toContain('date:invalid');
  });

  it('distinguishes sparse arrays from explicit undefined entries', () => {
    expect(canonicalToken(new Array(1))).not.toBe(canonicalToken([undefined]));
  });

  it('rejects cycles and accessors without executing them', () => {
    const cyclic: Record<string, unknown> = {};
    cyclic.self = cyclic;
    expect(() => canonicalToken(cyclic)).toThrow(CanonicalValueError);

    const getter = jest.fn(() => 'secret');
    const accessed = Object.defineProperty({}, 'secret', { get: getter });
    expect(() => canonicalToken(accessed)).toThrow('accessor property');
    expect(getter).not.toHaveBeenCalled();
  });
});

describe('scalar values and their equality', () => {
  const Decimal = prismaDecimal()!;

  it('recognises every Prisma scalar representation, objects included, and nothing else', () => {
    for (const value of [null, 'a', 1, true, 2n, new Date(0), new Uint8Array([1]), new Decimal('1.5')]) {
      expect(isScalarValue(value)).toBe(true);
    }
    for (const value of [undefined, { set: 1 }, [1], () => 1]) {
      expect(isScalarValue(value)).toBe(false);
    }
  });

  it('compares scalars by value, exactly and by type', () => {
    expect(sameValue(new Decimal('1.50'), new Decimal('1.50'))).toBe(true);
    expect(sameValue(new Decimal('1.50'), new Decimal('2.50'))).toBe(false);
    expect(sameValue(new Uint8Array([1, 2]), new Uint8Array([1, 2]))).toBe(true);
    expect(sameValue(new Uint8Array([1, 2]), new Uint8Array([2, 1]))).toBe(false);
    expect(sameValue(new Date(5), new Date(5))).toBe(true);
    expect(sameValue(5n, 5n)).toBe(true);
    expect(sameValue(5n, 5)).toBe(false);
  });
});
