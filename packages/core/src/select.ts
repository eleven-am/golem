import { GraphQLResolveInfo } from 'graphql';
import { parseResolveInfo, ResolveTree } from 'graphql-parse-resolve-info';
import { DatamodelModel } from './datamodel';
import { ComputedRequiresMap } from './extensions';

export interface PrismaSelectRelation {
  select?: PrismaSelect;
  include?: PrismaSelect;
  where?: unknown;
  [key: string]: unknown;
}

export type PrismaSelect = { [field: string]: boolean | PrismaSelectRelation };

export function primaryKeySelect(model: DatamodelModel): PrismaSelect {
  const select: PrismaSelect = {};
  for (const field of model.fields) {
    if (field.isId) {
      select[field.name] = true;
    }
  }
  if (Object.keys(select).length === 0) {
    throw new Error(`Model ${model.name} has no primary key field to select`);
  }
  return select;
}

function selectFromFields(
  fields: Record<string, ResolveTree>,
  model: DatamodelModel,
  modelsByName: Map<string, DatamodelModel>,
  computed?: ComputedRequiresMap,
): PrismaSelect {
  const select: PrismaSelect = {};
  const modelComputed = computed?.get(model.name);
  for (const key of Object.keys(fields)) {
    const tree = fields[key];
    const field = model.fields.find((f) => f.name === tree.name);
    if (!field) {
      const requires = modelComputed?.get(tree.name);
      if (requires) {
        for (const column of requires) {
          select[column] = true;
        }
      }
      continue;
    }
    if (field.kind === 'object') {
      const target = modelsByName.get(field.type);
      if (!target) {
        continue;
      }
      select[field.name] = {
        select: selectFromFields(tree.fieldsByTypeName[target.name] ?? {}, target, modelsByName, computed),
      };
    } else {
      select[field.name] = true;
    }
  }
  if (Object.keys(select).length === 0) {
    return primaryKeySelect(model);
  }
  return select;
}

export function buildSelect(
  info: GraphQLResolveInfo,
  model: DatamodelModel,
  modelsByName: Map<string, DatamodelModel>,
  computed?: ComputedRequiresMap,
): PrismaSelect {
  const tree = parseResolveInfo(info) as ResolveTree | null;
  if (!tree) {
    return primaryKeySelect(model);
  }
  return selectFromFields(tree.fieldsByTypeName[model.name] ?? {}, model, modelsByName, computed);
}

export function buildEventEntitySelect(
  info: GraphQLResolveInfo,
  eventTypeName: string,
  model: DatamodelModel,
  modelsByName: Map<string, DatamodelModel>,
  computed?: ComputedRequiresMap,
): PrismaSelect | null {
  const tree = parseResolveInfo(info) as ResolveTree | null;
  const eventFields = tree?.fieldsByTypeName[eventTypeName] ?? {};
  const entityTree = Object.values(eventFields).find((f) => f.name === 'entity');
  if (!entityTree) {
    return null;
  }
  return selectFromFields(entityTree.fieldsByTypeName[model.name] ?? {}, model, modelsByName, computed);
}
