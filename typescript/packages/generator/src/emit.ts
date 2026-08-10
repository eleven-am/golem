import type { DMMF } from '@prisma/generator-helper';

const SUPPORTED_KINDS = new Set(['scalar', 'object', 'enum']);
const INTERNAL_MODELS = new Set(['GolemUpsertGuard']);

export function emitDatamodelModule(datamodel: DMMF.Datamodel, provider?: string): string {
  const indexesByModel = new Map<string, Array<{ kind: string; name?: string; dbName?: string; fields: string[] }>>();
  for (const index of datamodel.indexes ?? []) {
    const entry = {
      kind: index.type,
      ...(index.name ? { name: index.name } : {}),
      ...(index.dbName ? { dbName: index.dbName } : {}),
      fields: index.fields.map((field) => field.name),
    };
    const existing = indexesByModel.get(index.model);
    if (existing) {
      existing.push(entry);
    } else {
      indexesByModel.set(index.model, [entry]);
    }
  }

  const models = datamodel.models
    .filter((model) => !INTERNAL_MODELS.has(model.name))
    .map((model) => ({
    name: model.name,
    dbName: model.dbName ?? model.name,
    fields: model.fields
      .filter((field) => SUPPORTED_KINDS.has(field.kind))
      .map((field) => ({
        name: field.name,
        dbName: field.dbName ?? field.name,
        kind: field.kind as 'scalar' | 'object' | 'enum',
        type: field.type,
        isList: field.isList,
        isRequired: field.isRequired,
        isUnique: field.isUnique,
        isId: field.isId,
        hasDefaultValue: field.hasDefaultValue,
        isReadOnly: field.isReadOnly,
        isUpdatedAt: field.isUpdatedAt ?? false,
        ...(field.nativeType
          ? { nativeType: [field.nativeType[0], [...field.nativeType[1]]] }
          : {}),
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
    ...(() => {
      const indexes = indexesByModel.get(model.name) ?? [];
      return indexes.length > 0 ? { indexes } : {};
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

declare global {
  interface GolemRegister {
    models: GolemModels;
    types: GolemTypes;
  }
}

export const datamodel = ${JSON.stringify(
    provider === undefined ? { models, enums } : { models, enums, provider },
    null,
    2,
  )} as const;

export function getDatamodel(): DatamodelDocument<GolemModels> {
  return datamodel as unknown as DatamodelDocument<GolemModels>;
}
`;
}
