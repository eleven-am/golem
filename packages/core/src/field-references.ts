import { AuthorizationProvider } from './authorization';
import { GolemValidationError } from './errors';
import { ModelMetadata, ModelMetadataIndex } from './model-meta';

export type FieldReferences = Map<string, Set<string>>;

export const MEASURE_KEYS = ['_sum', '_avg', '_min', '_max', '_count'] as const;

export const RELATION_FILTER_KEYS = ['is', 'isNot', 'some', 'every', 'none'] as const;

const RELEVANCE_KEY = '_relevance';

export interface FilterClauses {
  where?: unknown;
  orderBy?: unknown;
  cursor?: unknown;
}

export function referenceField(into: FieldReferences, model: string, field: string): void {
  const existing = into.get(model);
  if (existing) {
    existing.add(field);
    return;
  }
  into.set(model, new Set([field]));
}

export function addTrueKeys(into: FieldReferences, model: string, source: unknown): void {
  if (source && typeof source === 'object') {
    for (const [key, value] of Object.entries(source as Record<string, unknown>)) {
      if (value === true) {
        referenceField(into, model, key);
      }
    }
  }
}

export function addObservedKeys(into: FieldReferences, model: string, source: unknown): void {
  if (!source || typeof source !== 'object') {
    return;
  }
  for (const key of Object.keys(source as Record<string, unknown>)) {
    if (key !== '_all') {
      referenceField(into, model, key);
    }
  }
}

function addRelevanceFields(into: FieldReferences, model: string, source: unknown): void {
  const fields = (source as { fields?: unknown } | null)?.fields;
  const named = Array.isArray(fields) ? fields : [fields];
  if (named.length === 0 || named.some((name) => typeof name !== 'string')) {
    throw new GolemValidationError(
      `Cannot order ${model} by relevance without naming the fields it searches`,
    );
  }
  for (const name of named) {
    referenceField(into, model, name as string);
  }
}

export function addNestedFields(
  into: FieldReferences,
  metadata: ModelMetadataIndex,
  model: string,
  source: unknown,
): void {
  if (!source || typeof source !== 'object') {
    return;
  }
  const meta = metadata.get(model);
  for (const entry of Array.isArray(source) ? source : [source]) {
    if (!entry || typeof entry !== 'object') {
      continue;
    }
    for (const [key, value] of Object.entries(entry as Record<string, unknown>)) {
      if ((MEASURE_KEYS as readonly string[]).includes(key)) {
        addObservedKeys(into, model, value);
        continue;
      }
      if (key === 'AND' || key === 'OR' || key === 'NOT') {
        addNestedFields(into, metadata, model, value);
        continue;
      }
      if (key === RELEVANCE_KEY) {
        addRelevanceFields(into, model, value);
        continue;
      }
      const field = meta?.fieldsByName.get(key);
      if (field === undefined) {
        const compoundFields = key === meta?.compoundKeyName
          ? meta.primaryKeys.map((primaryKey) => primaryKey.name)
          : meta?.compoundUniqueSelectors.get(key);
        if (compoundFields) {
          for (const name of compoundFields) referenceField(into, model, name);
          continue;
        }
        if (meta !== undefined) {
          throw new GolemValidationError(
            `Cannot filter or order by "${key}" on ${model}: no such field`,
          );
        }
        referenceField(into, model, key);
        continue;
      }
      referenceField(into, model, key);
      if (field.kind === 'object' && metadata.has(field.type)) {
        addRelationFilterFields(into, metadata, field.type, value);
      }
    }
  }
}

function addRelationFilterFields(
  into: FieldReferences,
  metadata: ModelMetadataIndex,
  model: string,
  source: unknown,
): void {
  if (!source || typeof source !== 'object') {
    return;
  }
  for (const entry of Array.isArray(source) ? source : [source]) {
    if (!entry || typeof entry !== 'object') {
      continue;
    }
    const record = entry as Record<string, unknown>;
    const operators = RELATION_FILTER_KEYS.filter((operator) => operator in record);
    if (operators.length === 0) {
      addNestedFields(into, metadata, model, record);
      continue;
    }
    for (const operator of operators) {
      addNestedFields(into, metadata, model, record[operator]);
    }
    const remaining = Object.fromEntries(
      Object.entries(record).filter(
        ([key]) => !(RELATION_FILTER_KEYS as readonly string[]).includes(key),
      ),
    );
    if (Object.keys(remaining).length > 0) {
      addNestedFields(into, metadata, model, remaining);
    }
  }
}

export function addCursorFields(
  into: FieldReferences,
  model: string,
  source: unknown,
  metadata: ModelMetadata,
): void {
  if (!source || typeof source !== 'object' || Array.isArray(source)) {
    return;
  }
  for (const key of Object.keys(source as Record<string, unknown>)) {
    if (metadata.fieldsByName.has(key)) {
      referenceField(into, model, key);
      continue;
    }
    const compoundFields = key === metadata.compoundKeyName
      ? metadata.primaryKeys.map((field) => field.name)
      : metadata.compoundUniqueSelectors.get(key);
    if (compoundFields) {
      for (const field of compoundFields) referenceField(into, model, field);
      continue;
    }
    throw new GolemValidationError(
      `Cannot page from "${key}" on ${model}: no such field`,
    );
  }
}

export function collectFilterFields(
  request: FilterClauses,
  model: string,
  index: ModelMetadataIndex,
): FieldReferences {
  const references: FieldReferences = new Map();
  addNestedFields(references, index, model, request.where);
  addNestedFields(references, index, model, request.orderBy);
  const metadata = index.get(model);
  if (metadata) {
    addCursorFields(references, model, request.cursor, metadata);
  }
  return references;
}

export async function refuseUnreadableReferences(
  provider: AuthorizationProvider,
  context: unknown,
  references: FieldReferences,
  verb: string,
  fail: (message: string) => Error,
): Promise<void> {
  for (const [model, names] of references) {
    const fields = [...names];
    if (fields.length === 0) {
      continue;
    }
    const classification = await provider.classifyFields!('read', model, fields, context);
    for (const field of fields) {
      const entry = classification[field];
      if (entry?.access === 'always') {
        continue;
      }
      if (entry?.access === 'conditional' && entry.dischargedByConstraint) {
        continue;
      }
      const undischarged = entry?.requires?.join(', ');
      throw fail(
        undischarged
          ? `Cannot ${verb} field "${field}" on ${model}: readability depends on ${undischarged}, which the query constraint does not discharge`
          : `Cannot ${verb} field "${field}" on ${model}`,
      );
    }
  }
}
