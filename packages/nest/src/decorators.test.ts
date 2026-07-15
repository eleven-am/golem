import 'reflect-metadata';
import {
  AfterFindFirst,
  BeforeFindFirst,
  GOLEM_HOOK,
  GolemHookMetadata,
} from './decorators';

describe('findFirst hook decorators', () => {
  it.each([
    ['before', BeforeFindFirst],
    ['after', AfterFindFirst],
  ] as const)('registers %s metadata', (phase, decorator) => {
    class Hooks {
      method(): void {}
    }
    const descriptor = Object.getOwnPropertyDescriptor(Hooks.prototype, 'method')!;
    decorator()(Hooks.prototype, 'method', descriptor);

    expect(
      Reflect.getMetadata(GOLEM_HOOK, Hooks.prototype.method) as GolemHookMetadata,
    ).toEqual({ phase, operation: 'findFirst' });
  });
});
