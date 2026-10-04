import { isDecimalLike } from '@eleven-am/golem-policy';
import { GolemValidationError } from './errors';

function refuse(value: unknown, path: string, open: Set<object>): void {
  if (typeof value === 'string') {
    if (value.includes('\u0000')) {
      throw new GolemValidationError(
        `${path === '' ? 'A string input' : `The string at ${path}`} contains a NUL byte, which cannot be stored`,
      );
    }
    return;
  }
  if (
    value === null ||
    typeof value !== 'object' ||
    value instanceof Date ||
    ArrayBuffer.isView(value) ||
    isDecimalLike(value) ||
    open.has(value)
  ) {
    return;
  }
  open.add(value);
  for (const [key, child] of Object.entries(value)) {
    refuse(child, path === '' ? key : `${path}.${key}`, open);
  }
  open.delete(value);
}

/** Refuses a NUL byte in any string at any depth of an operation's arguments. */
export function refuseNulStrings(args: unknown): void {
  refuse(args, '', new Set());
}
