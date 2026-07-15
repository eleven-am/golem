import { emitTypesModule } from './emit-types';

describe('generated Golem hook types', () => {
  it('allows hook-only operations without widening schema operation configuration', () => {
    const output = emitTypesModule(['User'], '@prisma/client');

    expect(output).toContain(
      "import type { GolemHookOperation, HookRequestFor, HookResultFor } from '@eleven-am/golem-core';",
    );
    expect(output).toContain(
      'export type GolemRequest<M extends GolemModelName, O extends GolemHookOperation>',
    );
    expect(output).toContain(
      'export type GolemResult<M extends GolemModelName, O extends GolemHookOperation>',
    );
  });
});
