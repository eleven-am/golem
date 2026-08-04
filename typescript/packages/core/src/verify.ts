import {
  AuthorizationProvider,
  GolemAction,
  isConditionalConstraint,
} from './authorization';
import { runPolicyChecks } from './concurrency';
import { DatamodelField, DatamodelModel } from './datamodel';
import { GolemForbiddenError } from './errors';
import { buildModelMetadata, ModelMetadataIndex } from './model-meta';
import { NestedRelationWritePlan, nestedPlanTargetsRow, planNestedWrites } from './nested-writes';
import {
  constraintFieldNames,
  constraintHydrationSelect,
  dependencyHydrationSelect,
  mergeSelect,
} from './readtree';
import { PrismaSelect } from './select';

export interface VerifyContext {
  modelsByName: Map<string, DatamodelModel>;
  metadata?: ModelMetadataIndex;
  provider: AuthorizationProvider;
  context: unknown;
}

function metadataFor(ctx: Pick<VerifyContext, 'modelsByName' | 'metadata'>): ModelMetadataIndex {
  return ctx.metadata ?? buildModelMetadata([...ctx.modelsByName.values()]);
}

export function modelScalarSelect(model: DatamodelModel): PrismaSelect {
  const select: PrismaSelect = {};
  for (const field of model.fields) {
    if (field.kind !== 'object') {
      select[field.name] = true;
    }
  }
  return select;
}

export function touchedRelations(
  modelsByName: Map<string, DatamodelModel>,
  model: DatamodelModel,
  data: Record<string, unknown>,
): readonly NestedRelationWritePlan[] {
  return planNestedWrites(buildModelMetadata([...modelsByName.values()]), model, data);
}

export function wideSelect(
  modelsByName: Map<string, DatamodelModel>,
  model: DatamodelModel,
  data: Record<string, unknown>,
): PrismaSelect {
  const metadata = buildModelMetadata([...modelsByName.values()]);
  return wideSelectWithMetadata(metadata, model, data);
}

function wideSelectWithMetadata(
  metadata: ModelMetadataIndex,
  model: DatamodelModel,
  data: Record<string, unknown>,
): PrismaSelect {
  const select = modelScalarSelect(model);
  for (const relation of planNestedWrites(metadata, model, data)) {
    const nestedSelect = modelScalarSelect(relation.target);
    for (const payload of [...relation.createPayloads, ...relation.updatePayloads]) {
      mergeSelect(nestedSelect, wideSelectWithMetadata(metadata, relation.target, payload));
    }
    select[relation.field.name] = { select: nestedSelect };
  }
  return select;
}

function payloadFieldNames(model: DatamodelModel, data: Record<string, unknown>): string[] {
  const names = new Set<string>();
  for (const field of model.fields) {
    if (data[field.name] === undefined) {
      continue;
    }
    if (field.kind === 'object') {
      if (field.relationFromFields?.length === 1) {
        names.add(field.relationFromFields[0]);
      }
      continue;
    }
    names.add(field.name);
  }
  return [...names];
}

async function checkRow(
  ctx: VerifyContext,
  action: GolemAction,
  model: string,
  row: Record<string, unknown>,
): Promise<void> {
  const allowed = await ctx.provider.check!(action, model, row, ctx.context);
  if (!allowed) {
    throw new GolemForbiddenError(`Cannot ${action} ${model} with the provided values`);
  }
}

async function checkFields(
  ctx: VerifyContext,
  action: GolemAction,
  model: string,
  entity: Record<string, unknown>,
  fields: readonly string[],
): Promise<void> {
  await runPolicyChecks(fields.map((field) => async () => {
    const allowed = await ctx.provider.checkField!(action, model, entity, field, ctx.context);
    if (!allowed) throw new GolemForbiddenError(`Cannot ${action} field "${field}" on ${model}`);
  }));
}

async function verifyAppearedRow(
  ctx: VerifyContext,
  relation: NestedRelationWritePlan,
  row: Record<string, unknown>,
): Promise<void> {
  await runPolicyChecks([...relation.addedActions].map((action) => () =>
    checkRow(ctx, action, relation.target.name, row)));
  if (relation.addedActions.has('create')) {
    const fieldUnion = new Set<string>();
    for (const payload of relation.createPayloads) {
      for (const name of payloadFieldNames(relation.target, payload)) {
        fieldUnion.add(name);
      }
    }
    await checkFields(ctx, 'create', relation.target.name, row, [...fieldUnion]);
    for (const payload of relation.createPayloads) {
      await verifyCreatedChildren(ctx, relation.target, row, payload);
    }
  }
}

async function verifyCreatedChildren(
  ctx: VerifyContext,
  model: DatamodelModel,
  row: Record<string, unknown>,
  data: Record<string, unknown>,
): Promise<void> {
  for (const relation of planNestedWrites(metadataFor(ctx), model, data)) {
    const value = row[relation.field.name];
    const children = Array.isArray(value) ? value : value ? [value] : [];
    await runPolicyChecks(children.map((child) => () =>
      verifyAppearedRow(ctx, relation, child as Record<string, unknown>)));
  }
}

export async function verifyCreatedTree(
  ctx: VerifyContext,
  model: DatamodelModel,
  row: Record<string, unknown>,
  data: Record<string, unknown>,
): Promise<void> {
  await checkRow(ctx, 'create', model.name, row);
  await checkFields(ctx, 'create', model.name, row, payloadFieldNames(model, data));
  await verifyCreatedChildren(ctx, model, row, data);
}

function scalarChanged(before: unknown, after: unknown): boolean {
  if (before instanceof Date || after instanceof Date) {
    const a = before instanceof Date ? before.getTime() : before;
    const b = after instanceof Date ? after.getTime() : after;
    return a !== b;
  }
  return before !== after;
}

export async function verifyUpdatedRow(
  ctx: VerifyContext,
  model: DatamodelModel,
  before: Record<string, unknown>,
  after: Record<string, unknown>,
  data: Record<string, unknown>,
): Promise<void> {
  const metadata = metadataFor(ctx);
  await checkRow(ctx, 'update', model.name, after);
  const changed: string[] = [];
  for (const field of metadata.get(model.name)?.scalarFields ?? []) {
    if (scalarChanged(before[field.name], after[field.name])) {
      changed.push(field.name);
    }
  }
  await checkFields(ctx, 'update', model.name, before, changed);
  for (const relation of planNestedWrites(metadata, model, data)) {
    const targetMeta = metadata.get(relation.target.name);
    const pkFields = targetMeta?.primaryKeys ?? [];
    if (pkFields.length === 0) {
      throw new GolemForbiddenError(
        `Cannot verify nested writes to ${relation.target.name}: model has no primary key`,
      );
    }
    const identityOf = (row: Record<string, unknown>): string =>
      pkFields.map((pkField) => String(row[pkField.name])).join('\u0000');
    const beforeValue = before[relation.field.name];
    const afterValue = after[relation.field.name];
    const beforeRows = Array.isArray(beforeValue) ? beforeValue : beforeValue ? [beforeValue] : [];
    const afterRows = Array.isArray(afterValue) ? afterValue : afterValue ? [afterValue] : [];
    const beforeById = new Map(beforeRows.map((row) => {
      const record = row as Record<string, unknown>;
      return [identityOf(record), record] as const;
    }));
    const afterById = new Map(afterRows.map((row) => {
      const record = row as Record<string, unknown>;
      return [identityOf(record), record] as const;
    }));
    for (const child of afterRows) {
      const childRow = child as Record<string, unknown>;
      const beforeChild = beforeById.get(identityOf(childRow));
      if (!beforeChild) {
        await verifyAppearedRow(ctx, relation, childRow);
        continue;
      }
      const childChanged = targetMeta!.scalarFields
        .filter((field) => scalarChanged(beforeChild[field.name], childRow[field.name]))
        .map((field) => field.name);
      if (
        relation.retainedActions.has('update') &&
        (childChanged.length > 0 || nestedPlanTargetsRow(relation, beforeChild))
      ) {
        await checkRow(ctx, 'update', relation.target.name, childRow);
        await checkFields(ctx, 'update', relation.target.name, beforeChild, childChanged);
        for (const payload of relation.updatePayloads) {
          await verifyUpdatedChildren(ctx, relation.target, beforeChild, childRow, payload);
        }
      }
    }
    for (const child of beforeRows) {
      const childRow = child as Record<string, unknown>;
      if (afterById.has(identityOf(childRow))) continue;
      await runPolicyChecks([...relation.removedActions].map((action) => () =>
        checkRow(ctx, action, relation.target.name, childRow)));
    }
  }
}

async function verifyUpdatedChildren(
  ctx: VerifyContext,
  model: DatamodelModel,
  before: Record<string, unknown>,
  after: Record<string, unknown>,
  data: Record<string, unknown>,
): Promise<void> {
  for (const relation of planNestedWrites(metadataFor(ctx), model, data)) {
    const wrapperBefore = { ...before, [relation.field.name]: before[relation.field.name] };
    const wrapperAfter = { ...after, [relation.field.name]: after[relation.field.name] };
    await verifyRelationOnly(ctx, model, wrapperBefore, wrapperAfter, data, relation.field.name);
  }
}

async function verifyRelationOnly(
  ctx: VerifyContext,
  model: DatamodelModel,
  before: Record<string, unknown>,
  after: Record<string, unknown>,
  data: Record<string, unknown>,
  fieldName: string,
): Promise<void> {
  const rootBefore = { ...before };
  const rootAfter = { ...after };
  for (const field of metadataFor(ctx).get(model.name)?.scalarFields ?? []) {
    rootAfter[field.name] = rootBefore[field.name];
  }
  const narrowed = { [fieldName]: data[fieldName] };
  // Reuse the same diff verifier while suppressing root scalar differences.
  await verifyUpdatedRow(ctx, model, rootBefore, rootAfter, narrowed);
}

export interface VerificationPlan {
  select: PrismaSelect;
  fastPath: boolean;
}

export async function planVerification(
  ctx: VerifyContext,
  model: DatamodelModel,
  data: Record<string, unknown>,
  constraint: unknown,
  action: 'create' | 'update',
): Promise<VerificationPlan> {
  const metadata = metadataFor(ctx);
  const relations = planNestedWrites(metadata, model, data);
  if (!ctx.provider.classifyFields) {
    const wide = wideSelectWithMetadata(metadata, model, data);
    const hydrationState = { complete: true };
    mergeSelect(wide, constraintHydrationSelect(metadata, model, constraint, {}, hydrationState));
    if (!hydrationState.complete) {
      throw new GolemForbiddenError(
        `Cannot ${action} ${model.name}: its authorization constraint cannot be hydrated safely`,
      );
    }
    return { select: wide, fastPath: false };
  }
  const payload = payloadFieldNames(model, data);
  const classification =
    payload.length > 0
      ? await ctx.provider.classifyFields(action, model.name, payload, ctx.context)
      : {};
  const allAlways = payload.every((f) => classification[f]?.access === 'always');
  if (allAlways && !isConditionalConstraint(constraint) && relations.length === 0) {
    return { select: {}, fastPath: true };
  }
  const scalarNames = new Set(metadata.get(model.name)!.scalarFields.map((f) => f.name));
  const needed = new Set<string>();
  const fieldDependencies: PrismaSelect = {};
  for (const field of metadata.get(model.name)!.scalarFields) {
    if (field.isId || field.isUpdatedAt) {
      needed.add(field.name);
    }
  }
  for (const name of payload) {
    needed.add(name);
  }
  for (const name of constraintFieldNames(constraint)) {
    needed.add(name);
  }
  for (const name of payload) {
    const entry = classification[name];
    if (entry?.dependencies) {
      const hydration = dependencyHydrationSelect(metadata, model, entry.dependencies);
      if (!hydration.complete) {
        throw new GolemForbiddenError(
          `Cannot ${action} field "${name}" on ${model.name}: its authorization dependencies cannot be hydrated safely`,
        );
      }
      mergeSelect(fieldDependencies, hydration.select);
    }
    for (const required of entry?.requires ?? []) {
      const requiredField = metadata.get(model.name)?.fieldsByName.get(required);
      if (!requiredField) {
        throw new GolemForbiddenError(
          `Cannot ${action} field "${name}" on ${model.name}: authorization dependency "${required}" is not a model field`,
        );
      }
      if (requiredField.kind === 'object') {
        if (!entry?.dependencies?.[required]) {
          throw new GolemForbiddenError(
            `Cannot ${action} field "${name}" on ${model.name}: relation dependency "${required}" has no exact hydration tree`,
          );
        }
        continue;
      }
      needed.add(required);
    }
  }
  const select: PrismaSelect = {};
  for (const name of needed) {
    if (scalarNames.has(name)) {
      select[name] = true;
    }
  }
  mergeSelect(select, fieldDependencies);
  for (const relation of relations) {
    const nestedSelect = modelScalarSelect(relation.target);
    for (const payload of [...relation.createPayloads, ...relation.updatePayloads]) {
      mergeSelect(nestedSelect, wideSelectWithMetadata(metadata, relation.target, payload));
    }
    mergeSelect(select, {
      [relation.field.name]: { select: nestedSelect },
    });
  }
  const hydrationState = { complete: true };
  mergeSelect(select, constraintHydrationSelect(metadata, model, constraint, {}, hydrationState));
  if (!hydrationState.complete) {
    throw new GolemForbiddenError(
      `Cannot ${action} ${model.name}: its authorization constraint cannot be hydrated safely`,
    );
  }
  return { select, fastPath: false };
}
