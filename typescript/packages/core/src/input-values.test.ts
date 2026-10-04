import { GolemValidationError } from './errors';
import { refuseNulStrings } from './input-values';

describe('refuseNulStrings', () => {
  it.each([
    ['a top-level string', 'a\u0000b', 'A string input contains a NUL byte'],
    ['a write payload', { data: { title: 'a\u0000b' } }, 'The string at data.title'],
    ['a filter operator', { where: { title: { in: ['ok', '\u0000'] } } }, 'The string at where.title.in.1'],
    ['a JSON value', { data: { meta: { tags: [{ label: 'x\u0000' }] } } }, 'The string at data.meta.tags.0.label'],
    ['a JSON string value', { data: { meta: '\u0000' } }, 'The string at data.meta'],
    ['a raw query parameter', ['SELECT ?', 'a\u0000'], 'The string at 1'],
  ])('refuses a NUL byte in %s', (_label, args, message) => {
    expect(() => refuseNulStrings(args)).toThrow(GolemValidationError);
    expect(() => refuseNulStrings(args)).toThrow(message);
  });

  it('accepts ordinary values of every kind', () => {
    const shared = { label: 'shared' };
    expect(() =>
      refuseNulStrings({
        data: {
          title: 'plain',
          count: 3,
          big: 9n,
          when: new Date(0),
          bytes: new Uint8Array([0, 1, 0]),
          nothing: null,
          left: shared,
          right: shared,
        },
      }),
    ).not.toThrow();
  });

  it('terminates on a cyclic argument', () => {
    const cyclic: Record<string, unknown> = { title: 'ok' };
    cyclic.self = cyclic;
    expect(() => refuseNulStrings(cyclic)).not.toThrow();
  });
});
