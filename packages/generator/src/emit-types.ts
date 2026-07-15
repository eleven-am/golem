export function emitTypesModule(modelNames: readonly string[], clientImport: string): string {
  const entityImports = modelNames.join(', ');
  const entries = modelNames
    .map(
      (name) => `  ${name}: {
    entity: ${name};
    create: Prisma.${name}CreateInput;
    update: Prisma.${name}UpdateInput;
    updateMany: Prisma.${name}UpdateManyMutationInput;
    where: Prisma.${name}WhereInput;
    whereUnique: Prisma.${name}WhereUniqueInput;
    orderBy: Prisma.${name}OrderByWithRelationInput;
  };`,
    )
    .join('\n');

  return `import type { Prisma, ${entityImports} } from '${clientImport}';
import type { GolemHookOperation, HookRequestFor, HookResultFor } from '@eleven-am/golem-core';

export type GolemTypes = {
${entries}
};

export type GolemModelName = keyof GolemTypes & string;

export type GolemRequest<M extends GolemModelName, O extends GolemHookOperation> = HookRequestFor<
  GolemTypes,
  M,
  O
>;

export type GolemResult<M extends GolemModelName, O extends GolemHookOperation> = HookResultFor<
  GolemTypes,
  M,
  O
>;

export type GolemEntity<M extends GolemModelName> = GolemTypes[M]['entity'];
`;
}
