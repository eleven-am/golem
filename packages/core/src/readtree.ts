import {
  AuthorizationProvider,
  FieldDependencyTree,
  isConditionalConstraint,
  mergeConstraint,
} from './authorization';
import { DatamodelModel } from './datamodel';
import { GolemForbiddenError, GolemValidationError } from './errors';
import {
  FilterClauses,
  RELATION_FILTER_KEYS,
  collectFilterFields,
  refuseUnreadableReferences,
} from './field-references';
import { buildModelMetadata, ModelMetadataIndex } from './model-meta';
import { PrismaSelect, RELATION_COUNT_FIELD } from './select';

export interface ToOneCheck {
  path: readonly string[];
  model: string;
}

export interface FieldMaskCheck {
  path: readonly string[];
  model: string;
  field: string;
  constraint?: unknown;
}

export interface InjectedField {
  path: readonly string[];
  field: string;
  masks?: readonly string[];
}

export interface PreparedReadTree {
  select?: Record<string, unknown>;
  include?: Record<string, unknown>;
  omit?: Record<string, unknown>;
  toOneChecks: ToOneCheck[];
  maskChecks: FieldMaskCheck[];
  injected: InjectedField[];
}

interface PrepareOptions {
  model: DatamodelModel;
  modelsByName: Map<string, DatamodelModel>;
  select?: Record<string, unknown>;
  include?: Record<string, unknown>;
  omit?: Record<string, unknown>;
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
  omit?: Record<string, unknown>;
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

export function mergeSelect(into: PrismaSelect, source: PrismaSelect): void {
  for (const [name, value] of Object.entries(source)) {
    const current = into[name];
    if (
      current && current !== true && value !== true &&
      typeof current === 'object' && typeof value === 'object' &&
      current.select && value.select
    ) {
      mergeSelect(current.select, value.select);
    } else {
      into[name] = value;
    }
  }
}

interface DependencyHydration {
  select: PrismaSelect;
  complete: boolean;
}

export function dependencyHydrationSelect(
  metadata: ModelMetadataIndex,
  model: DatamodelModel,
  dependencies: FieldDependencyTree,
  relationFilterNode = false,
): DependencyHydration {
  const select: PrismaSelect = {};
  let complete = true;
  const meta = metadata.get(model.name);
  for (const [name, dependency] of Object.entries(dependencies)) {
    const field = meta?.fieldsByName.get(name);
    if (
      (relationFilterNode || !field) &&
      (RELATION_FILTER_KEYS as readonly string[]).includes(name)
    ) {
      if (dependency === true) {
        const identity = meta?.primaryKeys[0] ?? meta?.scalarFields[0];
        if (identity) {
          select[identity.name] = true;
        } else {
          complete = false;
        }
        continue;
      }
      const nested = dependencyHydrationSelect(metadata, model, dependency, false);
      complete &&= nested.complete;
      mergeSelect(select, nested.select);
      continue;
    }
    if (!field) {
      complete = false;
      continue;
    }
    if (field.kind !== 'object') {
      select[name] = true;
      continue;
    }
    const target = metadata.get(field.type)?.model;
    if (!target) {
      complete = false;
      continue;
    }
    if (dependency === true) {
      const identity = metadata.get(target.name)?.primaryKeys[0] ??
        metadata.get(target.name)?.scalarFields[0];
      if (!identity) {
        complete = false;
        continue;
      }
      select[name] = { select: { [identity.name]: true } };
      continue;
    }
    const nested = dependencyHydrationSelect(metadata, target, dependency, true);
    complete &&= nested.complete;
    if (Object.keys(nested.select).length === 0) {
      complete = false;
      continue;
    }
    select[name] = { select: nested.select };
  }
  return { select, complete };
}

export function constraintHydrationSelect(
  metadata: ModelMetadataIndex,
  model: DatamodelModel,
  constraint: unknown,
  into: PrismaSelect = {},
  state: { complete: boolean } = { complete: true },
): PrismaSelect {
  if (!constraint || typeof constraint !== 'object') {
    if (constraint !== undefined && constraint !== null) state.complete = false;
    return into;
  }
  const meta = metadata.get(model.name);
  for (const [key, value] of Object.entries(constraint as Record<string, unknown>)) {
    if (key === 'AND' || key === 'OR' || key === 'NOT') {
      const branches = Array.isArray(value) ? value : [value];
      for (const branch of branches) {
        constraintHydrationSelect(metadata, model, branch, into, state);
      }
      continue;
    }
    const fieldDef = meta?.fieldsByName.get(key);
    if (!fieldDef) {
      state.complete = false;
      continue;
    }
    if (fieldDef.kind !== 'object') {
      into[key] = true;
      continue;
    }
    const target = metadata.get(fieldDef.type)?.model;
    if (!target || !value || typeof value !== 'object') {
      if (!target) state.complete = false;
      if (target && (value === null || value === undefined)) {
        const identity = metadata.get(target.name)?.primaryKeys[0] ??
          metadata.get(target.name)?.scalarFields[0];
        if (identity) into[key] = { select: { [identity.name]: true } };
        else state.complete = false;
      } else if (target) {
        state.complete = false;
      }
      continue;
    }
    const relationValue = value as Record<string, unknown>;
    const presentOperators = RELATION_FILTER_KEYS.filter((operator) => operator in relationValue);
    const conditions = presentOperators.length > 0
      ? presentOperators.map((operator) => relationValue[operator])
      : [value];
    const nested: PrismaSelect = {};
    for (const condition of conditions) {
      if (condition === null) {
        const identity = metadata.get(target.name)?.primaryKeys[0] ??
          metadata.get(target.name)?.scalarFields[0];
        if (identity) nested[identity.name] = true;
        else state.complete = false;
      } else {
        constraintHydrationSelect(metadata, target, condition, nested, state);
      }
    }
    if (Object.keys(nested).length === 0) {
      continue;
    }
    const existing = into[key];
    if (existing && existing !== true && typeof existing === 'object' && existing.select) {
      mergeSelect(existing.select, nested);
    } else {
      into[key] = { select: nested };
    }
  }
  return into;
}

function cloneSelectShape(shape: unknown): unknown {
  if (shape === true || shape === false || !shape || typeof shape !== 'object') {
    return shape;
  }
  const result: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(shape as Record<string, unknown>)) {
    result[key] = cloneSelectShape(value);
  }
  return result;
}

function injectHydrationIntoProjection(
  metadata: ModelMetadataIndex,
  model: DatamodelModel,
  projection: RelationEntry,
  hydration: PrismaSelect,
  path: readonly string[],
  injected: InjectedField[],
): void {
  if (projection.select) {
    injectHydrationIntoSelect(
      metadata,
      model,
      projection.select,
      hydration,
      path,
      injected,
    );
    return;
  }
  const modelMeta = metadata.get(model.name);
  for (const [key, shape] of Object.entries(hydration)) {
    const field = modelMeta?.fieldsByName.get(key);
    if (!field) {
      continue;
    }
    if (field.kind !== 'object' || shape === true) {
      if (projection.omit?.[key] === true) {
        delete projection.omit[key];
        injected.push({ path, field: key });
      }
      continue;
    }
    const nested = (shape as { select?: PrismaSelect }).select;
    const target = metadata.get(field.type)?.model;
    if (!nested || !target) {
      continue;
    }
    projection.include ??= {};
    const existing = projection.include[key];
    if (existing === undefined || existing === false) {
      projection.include[key] = { select: cloneSelectShape(nested) };
      injected.push({ path, field: key });
      continue;
    }
    if (existing === true) {
      const entry: RelationEntry = {};
      injectHydrationIntoProjection(metadata, target, entry, nested, [...path, key], injected);
      projection.include[key] = Object.keys(entry).length === 0 ? true : entry;
      continue;
    }
    if (typeof existing === 'object') {
      injectHydrationIntoProjection(
        metadata,
        target,
        existing as RelationEntry,
        nested,
        [...path, key],
        injected,
      );
    }
  }
}

function injectHydrationIntoSelect(
  metadata: ModelMetadataIndex,
  model: DatamodelModel,
  select: Record<string, unknown>,
  hydration: PrismaSelect,
  path: readonly string[],
  injected: InjectedField[],
): void {
  const modelMeta = metadata.get(model.name);
  for (const [key, shape] of Object.entries(hydration)) {
    const existing = select[key];
    if (existing === undefined || existing === false) {
      select[key] = cloneSelectShape(shape);
      injected.push({ path, field: key });
      continue;
    }
    const field = modelMeta?.fieldsByName.get(key);
    if (!field || field.kind !== 'object' || shape === true) {
      continue;
    }
    const nested = (shape as { select?: PrismaSelect }).select;
    const target = metadata.get(field.type)?.model;
    if (!nested || !target) {
      continue;
    }
    if (existing === true) {
      const entry: RelationEntry = {};
      injectHydrationIntoProjection(metadata, target, entry, nested, [...path, key], injected);
      select[key] = Object.keys(entry).length === 0 ? true : entry;
      continue;
    }
    if (typeof existing === 'object') {
      injectHydrationIntoProjection(
        metadata,
        target,
        existing as RelationEntry,
        nested,
        [...path, key],
        injected,
      );
    }
  }
}

function retainHydration(
  metadata: ModelMetadataIndex,
  model: DatamodelModel,
  hydration: PrismaSelect,
  path: readonly string[],
  injected: InjectedField[],
): void {
  for (const [key, shape] of Object.entries(hydration)) {
    for (const [index, entry] of injected.entries()) {
      if (
        entry.masks !== undefined &&
        entry.field === key &&
        entry.path.length === path.length &&
        entry.path.every((segment, depth) => segment === path[depth])
      ) {
        injected[index] = { path: entry.path, field: entry.field };
      }
    }
    const field = metadata.get(model.name)?.fieldsByName.get(key);
    const target = field?.kind === 'object' ? metadata.get(field.type)?.model : undefined;
    const nested = shape === true ? undefined : (shape as { select?: PrismaSelect }).select;
    if (nested !== undefined && target !== undefined) {
      retainHydration(metadata, target, nested, [...path, key], injected);
    }
  }
}

export async function prepareReadTree(options: PrepareOptions): Promise<PreparedReadTree> {
  if (options.select !== undefined && options.omit !== undefined) {
    throw new GolemValidationError('select and omit cannot be used together');
  }
  const checks: ToOneCheck[] = [];
  const maskChecks: FieldMaskCheck[] = [];
  const injected: InjectedField[] = [];
  const metadata =
    options.metadata ?? buildModelMetadata([...options.modelsByName.values()]);

  const classifying =
    options.readFieldChecks === true &&
    options.provider?.classifyFields !== undefined &&
    options.provider?.checkField !== undefined;

  async function classifyFilterClauses(
    model: DatamodelModel,
    clauses: FilterClauses,
  ): Promise<void> {
    if (
      !classifying ||
      (clauses.where === undefined &&
        clauses.orderBy === undefined &&
        clauses.cursor === undefined)
    ) {
      return;
    }
    await refuseUnreadableReferences(
      options.provider!,
      options.context,
      collectFilterFields(clauses, model.name, metadata),
      'filter or order by',
      (message) => new GolemForbiddenError(message),
    );
  }

  async function classifyModelFields(
    model: DatamodelModel,
    projection: RelationEntry,
    path: readonly string[],
  ): Promise<void> {
    if (!classifying) {
      return;
    }
    const modelMeta = metadata.get(model.name);
    const scalarNames =
      modelMeta?.scalarFields.map((f) => f.name) ??
      model.fields.filter((f) => f.kind !== 'object').map((f) => f.name);
    const tree = projection.select;
    const omit = projection.omit;
    const requested = tree
      ? scalarNames.filter((name) => tree[name] === true)
      : scalarNames.filter((name) => omit?.[name] !== true);
    if (requested.length === 0) {
      return;
    }
    const classification = await options.provider!.classifyFields!(
      'read',
      model.name,
      requested,
      options.context,
    );
    const needed: PrismaSelect = {};
    const contributors = new Map<string, Set<string>>();
    const contributed = (name: string, field: string): void => {
      const existing = contributors.get(name);
      if (existing) {
        existing.add(field);
        return;
      }
      contributors.set(name, new Set([field]));
    };
    for (const field of requested) {
      const entry = classification[field];
      if (!entry || entry.access === 'always') {
        continue;
      }
      if (entry.access === 'never') {
        throw new GolemForbiddenError(`Cannot read field "${field}" on ${model.name}`);
      }
      const constraint =
        options.provider?.constrainField !== undefined
          ? await options.provider.constrainField('read', model.name, field, options.context)
          : undefined;
      maskChecks.push({ path, model: model.name, field, constraint });
      if (entry.dependencies) {
        const hydration = dependencyHydrationSelect(metadata, model, entry.dependencies);
        if (!hydration.complete) {
          throw new GolemForbiddenError(
            `Cannot read field "${field}" on ${model.name}: its authorization dependencies cannot be hydrated safely`,
          );
        }
        for (const name of Object.keys(hydration.select)) {
          contributed(name, field);
        }
        mergeSelect(needed, hydration.select);
      }
      for (const required of entry.requires ?? []) {
        const requiredField = modelMeta?.fieldsByName.get(required);
        if (!requiredField) {
          throw new GolemForbiddenError(
            `Cannot read field "${field}" on ${model.name}: authorization dependency "${required}" is not a model field`,
          );
        }
        if (requiredField?.kind === 'object') {
          if (!entry.dependencies?.[required]) {
            throw new GolemForbiddenError(
              `Cannot read field "${field}" on ${model.name}: relation dependency "${required}" has no exact hydration tree`,
            );
          }
          continue;
        }
        contributed(required, field);
        needed[required] = true;
      }
    }
    if (Object.keys(needed).length > 0) {
      const mark = injected.length;
      injectHydrationIntoProjection(metadata, model, projection, needed, path, injected);
      for (let index = mark; index < injected.length; index += 1) {
        const entry = injected[index]!;
        const owners = entry.path.length === path.length
          ? contributors.get(entry.field)
          : undefined;
        if (owners !== undefined) {
          injected[index] = { ...entry, masks: [...owners] };
        }
      }
    }
  }

  async function walkRelationCounts(
    model: DatamodelModel,
    entry: unknown,
    depth: number,
  ): Promise<unknown> {
    const requested = (entry as { select?: unknown } | null)?.select;
    if (!requested || typeof requested !== 'object' || Array.isArray(requested)) {
      return entry;
    }
    const counted: Record<string, unknown> = {};
    for (const [name, value] of Object.entries(requested as Record<string, unknown>)) {
      const field = metadata.get(model.name)?.fieldsByName.get(name);
      const target = field?.kind === 'object' ? options.modelsByName.get(field.type) : undefined;
      if (
        value === false || value === undefined || !field || field.kind !== 'object' ||
        !field.isList || !target
      ) {
        counted[name] = value;
        continue;
      }
      if (depth + 1 > options.maxDepth) {
        throw new GolemValidationError(
          `Query depth ${depth + 1} exceeds the maximum of ${options.maxDepth}`,
        );
      }
      if (value !== true && typeof value === 'object' && value !== null) {
        await classifyFilterClauses(target, value as FilterClauses);
      }
      const constraint = options.provider
        ? await options.provider.constrain('read', target.name, options.context)
        : undefined;
      if (!isConditionalConstraint(constraint)) {
        counted[name] = value;
        continue;
      }
      const narrowed: Record<string, unknown> =
        value === true ? {} : { ...(value as Record<string, unknown>) };
      narrowed.where = mergeConstraint(narrowed.where, constraint);
      counted[name] = narrowed;
    }
    return { ...(entry as Record<string, unknown>), select: counted };
  }

  async function walkTree(
    model: DatamodelModel,
    tree: Record<string, unknown> | undefined,
    mode: 'select' | 'include',
    omit: Record<string, unknown> | undefined,
    depth: number,
    path: readonly string[],
  ): Promise<Record<string, unknown> | undefined> {
    if (!tree) {
      return tree;
    }
    const rewritten: Record<string, unknown> = {};
    for (const [key, entry] of Object.entries(tree)) {
      if (key === RELATION_COUNT_FIELD && entry !== false && entry !== undefined) {
        rewritten[key] = await walkRelationCounts(model, entry, depth);
        continue;
      }
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
      if (entryObject.omit) {
        entryObject.omit = { ...entryObject.omit };
      }
      if (entryObject.select !== undefined && entryObject.omit !== undefined) {
        throw new GolemValidationError(
          `select and omit cannot be used together at ${relationPath.join('.')}`,
        );
      }
      await classifyFilterClauses(target, entryObject);
      if (entryObject.select !== undefined) {
        entryObject.select = await walkTree(
          target,
          entryObject.select as Record<string, unknown>,
          'select',
          undefined,
          depth + 1,
          relationPath,
        );
      }
      if (entryObject.include !== undefined) {
        entryObject.include = await walkTree(
          target,
          entryObject.include as Record<string, unknown>,
          'include',
          entryObject.omit,
          depth + 1,
          relationPath,
        );
      }
      if (entryObject.select === undefined && entryObject.include === undefined) {
        await classifyModelFields(target, entryObject, relationPath);
      }
      if (conditional) {
        if (field.isList) {
          entryObject.where = mergeConstraint(entryObject.where, constraint);
        } else if (options.provider?.check) {
          const hydrationState = { complete: true };
          const hydration = constraintHydrationSelect(
            metadata,
            target,
            constraint,
            {},
            hydrationState,
          );
          if (!hydrationState.complete) {
            throw new GolemForbiddenError(
              `Cannot traverse relation ${model.name}.${key}: its authorization constraint cannot be hydrated safely`,
            );
          }
          injectHydrationIntoProjection(
            metadata,
            target,
            entryObject,
            hydration,
            relationPath,
            injected,
          );
          retainHydration(metadata, target, hydration, relationPath, injected);
          checks.push({ path: relationPath, model: target.name });
        } else {
          throw new GolemForbiddenError(
            `Cannot traverse relation ${model.name}.${key}: row-level constraints apply and the authorization provider does not support instance checks`,
          );
        }
      }
      rewritten[key] = Object.keys(entryObject).length === 0 ? true : entryObject;
    }
    await classifyModelFields(
      model,
      mode === 'select' ? { select: rewritten } : { include: rewritten, omit },
      path,
    );
    return rewritten;
  }

  const omit = options.omit ? { ...options.omit } : undefined;
  const select = await walkTree(options.model, options.select, 'select', undefined, 1, []);
  let include = await walkTree(options.model, options.include, 'include', omit, 1, []);
  if (!options.select && !options.include) {
    const rootProjection: RelationEntry = { include: {}, omit };
    await classifyModelFields(options.model, rootProjection, []);
    if (rootProjection.include && Object.keys(rootProjection.include).length > 0) {
      include = rootProjection.include;
    }
  }
  return { select, include, omit, toOneChecks: checks, maskChecks, injected };
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
