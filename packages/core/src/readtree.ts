import {
  AuthorizationProvider,
  isConditionalConstraint,
  mergeConstraint,
} from './authorization';
import { DatamodelModel } from './datamodel';
import { GolemForbiddenError, GolemValidationError } from './errors';
import { ModelMetadataIndex } from './model-meta';

export interface ToOneCheck {
  path: readonly string[];
  model: string;
}

export interface FieldMaskCheck {
  path: readonly string[];
  model: string;
  field: string;
}

export interface InjectedField {
  path: readonly string[];
  field: string;
}

export interface PreparedReadTree {
  select?: Record<string, unknown>;
  include?: Record<string, unknown>;
  toOneChecks: ToOneCheck[];
  maskChecks: FieldMaskCheck[];
  injected: InjectedField[];
}

interface PrepareOptions {
  model: DatamodelModel;
  modelsByName: Map<string, DatamodelModel>;
  select?: Record<string, unknown>;
  include?: Record<string, unknown>;
  provider?: AuthorizationProvider;
  context?: unknown;
  maxDepth: number;
  readFieldChecks?: boolean;
  metadata?: ModelMetadataIndex;
}

interface RelationEntry {
  where?: unknown;
  select?: Record<string, unknown>;
  include?: Record<string, unknown>;
  [key: string]: unknown;
}

export function constraintFieldNames(constraint: unknown, into: Set<string> = new Set()): Set<string> {
  if (!constraint || typeof constraint !== 'object') {
    return into;
  }
  for (const [key, value] of Object.entries(constraint)) {
    if (key === 'AND' || key === 'OR' || key === 'NOT') {
      const branches = Array.isArray(value) ? value : [value];
      for (const branch of branches) {
        constraintFieldNames(branch, into);
      }
    } else {
      into.add(key);
    }
  }
  return into;
}

export async function prepareReadTree(options: PrepareOptions): Promise<PreparedReadTree> {
  const checks: ToOneCheck[] = [];
  const maskChecks: FieldMaskCheck[] = [];
  const injected: InjectedField[] = [];

  const classifying =
    options.readFieldChecks === true &&
    options.provider?.classifyFields !== undefined &&
    options.provider?.checkField !== undefined;

  async function classifyModelFields(
    model: DatamodelModel,
    tree: Record<string, unknown> | undefined,
    path: readonly string[],
  ): Promise<void> {
    if (!classifying) {
      return;
    }
    const scalarNames =
      options.metadata?.get(model.name)?.scalarFields.map((f) => f.name) ??
      model.fields.filter((f) => f.kind !== 'object').map((f) => f.name);
    const requested = tree
      ? scalarNames.filter((name) => tree[name] === true)
      : scalarNames;
    if (requested.length === 0) {
      return;
    }
    const classification = await options.provider!.classifyFields!(
      'read',
      model.name,
      requested,
      options.context,
    );
    for (const field of requested) {
      const entry = classification[field];
      if (!entry || entry.access === 'always') {
        continue;
      }
      if (entry.access === 'never') {
        throw new GolemForbiddenError(`Cannot read field "${field}" on ${model.name}`);
      }
      maskChecks.push({ path, model: model.name, field });
      if (tree) {
        for (const required of entry.requires ?? []) {
          if (tree[required] === undefined && scalarNames.includes(required)) {
            tree[required] = true;
            injected.push({ path, field: required });
          }
        }
      }
    }
  }

  async function walkTree(
    model: DatamodelModel,
    tree: Record<string, unknown> | undefined,
    depth: number,
    path: readonly string[],
  ): Promise<Record<string, unknown> | undefined> {
    if (!tree) {
      return tree;
    }
    const rewritten: Record<string, unknown> = {};
    for (const [key, entry] of Object.entries(tree)) {
      const field = options.metadata?.get(model.name)?.fieldsByName.get(key) ??
        model.fields.find((f) => f.name === key);
      if (!field || field.kind !== 'object' || entry === false || entry === undefined) {
        rewritten[key] = entry;
        continue;
      }
      const target = options.modelsByName.get(field.type);
      if (!target) {
        rewritten[key] = entry;
        continue;
      }
      if (depth + 1 > options.maxDepth) {
        throw new GolemValidationError(
          `Query depth ${depth + 1} exceeds the maximum of ${options.maxDepth}`,
        );
      }
      let constraint: unknown;
      if (options.provider) {
        constraint = await options.provider.constrain('read', target.name, options.context);
      }
      const conditional = isConditionalConstraint(constraint);
      const relationPath = [...path, key];
      const entryObject: RelationEntry = entry === true && classifying
        ? {
            select: Object.fromEntries(
              (options.metadata?.get(target.name)?.scalarFields ??
                target.fields.filter((candidate) => candidate.kind !== 'object'))
                .map((candidate) => [candidate.name, true]),
            ),
          }
        : entry === true ? {} : { ...(entry as RelationEntry) };
      if (entryObject.select !== undefined) {
        entryObject.select = await walkTree(
          target,
          entryObject.select as Record<string, unknown>,
          depth + 1,
          relationPath,
        );
      }
      if (entryObject.include !== undefined) {
        entryObject.include = await walkTree(
          target,
          entryObject.include as Record<string, unknown>,
          depth + 1,
          relationPath,
        );
      }
      if (conditional) {
        if (field.isList) {
          entryObject.where = mergeConstraint(entryObject.where, constraint);
        } else if (options.provider?.check) {
          if (entryObject.select) {
            for (const name of constraintFieldNames(constraint)) {
              const constraintField = options.metadata?.get(target.name)?.fieldsByName.get(name) ??
                target.fields.find((f) => f.name === name);
              if (constraintField && constraintField.kind !== 'object') {
                (entryObject.select as Record<string, unknown>)[name] ??= true;
              }
            }
          }
          checks.push({ path: relationPath, model: target.name });
        } else {
          throw new GolemForbiddenError(
            `Cannot traverse relation ${model.name}.${key}: row-level constraints apply and the authorization provider does not support instance checks`,
          );
        }
      }
      rewritten[key] = Object.keys(entryObject).length === 0 ? true : entryObject;
    }
    await classifyModelFields(model, rewritten, path);
    return rewritten;
  }

  const select = await walkTree(options.model, options.select, 1, []);
  const include = await walkTree(options.model, options.include, 1, []);
  if (!options.select && !options.include) {
    await classifyModelFields(options.model, undefined, []);
  }
  return { select, include, toOneChecks: checks, maskChecks, injected };
}

export async function applyToOneChecks(
  data: unknown,
  checks: readonly ToOneCheck[],
  provider: AuthorizationProvider,
  context: unknown,
): Promise<void> {
  for (const check of checks) {
    await applyCheck(data, check.path, check.model, provider, context);
  }
}

export async function applyFieldMasks(
  data: unknown,
  checks: readonly FieldMaskCheck[],
  provider: AuthorizationProvider,
  context: unknown,
): Promise<void> {
  for (const check of checks) {
    await walkAndTransform(data, check.path, async (row) => {
      if (row[check.field] === undefined || row[check.field] === null) {
        return;
      }
      const allowed = await provider.checkField!('read', check.model, row, check.field, context);
      if (!allowed) {
        row[check.field] = null;
      }
    });
  }
}

export async function stripInjectedFields(
  data: unknown,
  injected: readonly InjectedField[],
): Promise<void> {
  for (const entry of injected) {
    await walkAndTransform(data, entry.path, async (row) => {
      delete row[entry.field];
    });
  }
}

async function walkAndTransform(
  node: unknown,
  path: readonly string[],
  transform: (row: Record<string, unknown>) => Promise<void>,
): Promise<void> {
  if (!node || typeof node !== 'object') {
    return;
  }
  if (Array.isArray(node)) {
    for (const item of node) {
      await walkAndTransform(item, path, transform);
    }
    return;
  }
  if (path.length === 0) {
    await transform(node as Record<string, unknown>);
    return;
  }
  const [segment, ...rest] = path;
  await walkAndTransform((node as Record<string, unknown>)[segment], rest, transform);
}

async function applyCheck(
  node: unknown,
  path: readonly string[],
  model: string,
  provider: AuthorizationProvider,
  context: unknown,
): Promise<void> {
  if (!node || typeof node !== 'object') {
    return;
  }
  if (Array.isArray(node)) {
    for (const item of node) {
      await applyCheck(item, path, model, provider, context);
    }
    return;
  }
  const container = node as Record<string, unknown>;
  const [segment, ...rest] = path;
  const value = container[segment];
  if (value === undefined || value === null) {
    return;
  }
  if (rest.length > 0) {
    await applyCheck(value, rest, model, provider, context);
    return;
  }
  if (Array.isArray(value)) {
    return;
  }
  const allowed = await provider.check!('read', model, value, context);
  if (!allowed) {
    container[segment] = null;
  }
}
