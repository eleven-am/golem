import type { DMMF } from '@prisma/generator-helper';

const SUPPORTED_KINDS = new Set(['scalar', 'object', 'enum']);

export function emitDatamodelModule(datamodel: DMMF.Datamodel): string {
  const models = datamodel.models.map((model) => ({
    name: model.name,
    fields: model.fields
      .filter((field) => SUPPORTED_KINDS.has(field.kind))
      .map((field) => ({
        name: field.name,
        kind: field.kind as 'scalar' | 'object' | 'enum',
        type: field.type,
        isList: field.isList,
        isRequired: field.isRequired,
        isUnique: field.isUnique,
        isId: field.isId,
        hasDefaultValue: field.hasDefaultValue,
        isReadOnly: field.isReadOnly,
        isUpdatedAt: field.isUpdatedAt ?? false,
        ...(field.relationName ? { relationName: field.relationName } : {}),
        ...(field.relationFromFields?.length ? { relationFromFields: [...field.relationFromFields] } : {}),
        ...(field.relationToFields?.length ? { relationToFields: [...(field.relationToFields as string[])] } : {}),
      })),
    ...(model.primaryKey
      ? {
          primaryKey: {
            ...(model.primaryKey.name ? { name: model.primaryKey.name } : {}),
            fields: [...model.primaryKey.fields],
          },
        }
      : {}),
    ...(() => {
      const compound = (model.uniqueIndexes ?? [])
        .filter((index) => index.fields.length > 1)
        .map((index) => ({
          ...(index.name ? { name: index.name } : {}),
          fields: [...index.fields],
        }));
      return compound.length > 0 ? { uniqueIndexes: compound } : {};
    })(),
  }));
  const enums = datamodel.enums.map((e) => ({
    name: e.name,
    values: e.values.map((v) => v.name),
  }));

  const modelEntries = models
    .map((model) => `  ${model.name}: ${model.fields.map((f) => JSON.stringify(f.name)).join(' | ')};`)
    .join('\n');

  return `import type { DatamodelDocument } from '@eleven-am/golem-core';
import type { GolemTypes } from './types';

export interface GolemModels {
${modelEntries}
}

declare module '@eleven-am/golem' {
  interface Register {
    models: GolemModels;
    types: GolemTypes;
  }
}

export const datamodel = ${JSON.stringify({ models, enums }, null, 2)} as const;

export function getDatamodel(): DatamodelDocument<GolemModels> {
  return datamodel as unknown as DatamodelDocument<GolemModels>;
}
`;
}
