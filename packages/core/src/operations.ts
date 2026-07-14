import {
  AuthorizationProvider,
  GolemAction,
  mergeConstraint,
} from './authorization';
import { DatamodelModel } from './datamodel';
import { GolemConflictError, GolemNotFoundError, GolemValidationError } from './errors';
import { runPolicyChecks } from './concurrency';
import { withBufferedEvents } from './event-buffer';
import { GolemOperation, HookRegistry } from './hooks';
import { lcFirst } from './naming';
import { buildModelMetadata, ModelMetadataIndex } from './model-meta';
import { planNestedWrites } from './nested-writes';
import {
  PreparedReadTree,
  applyFieldMasks,
  applyToOneChecks,
  prepareReadTree,
  stripInjectedFields,
} from './readtree';
import { PrismaSelect } from './select';
import {
  VerifyContext,
  planVerification,
  verifyCreatedTree,
  verifyUpdatedRow,
} from './verify';

export interface FindOneRequest {
  context?: unknown;
  model: string;
  where: unknown;
  select?: PrismaSelect;
  include?: unknown;
}

export interface FindManyRequest {
  context?: unknown;
  model: string;
  where?: unknown;
  orderBy?: unknown;
  take?: number;
  skip?: number;
  cursor?: unknown;
  distinct?: unknown;
  select?: PrismaSelect;
  include?: unknown;
}

export interface CreateRequest {
  context?: unknown;
  model: string;
  data: unknown;
  select?: PrismaSelect;
  include?: unknown;
}

export interface UpdateRequest {
  context?: unknown;
  model: string;
  where: unknown;
  data: unknown;
  select?: PrismaSelect;
  include?: unknown;
}

export interface UpdateManyRequest {
  context?: unknown;
  model: string;
  where?: unknown;
  data: unknown;
}

export interface DeleteRequest {
  context?: unknown;
  model: string;
  where: unknown;
  select?: PrismaSelect;
  include?: unknown;
}

export interface DeleteManyRequest {
  context?: unknown;
  model: string;
  where?: unknown;
}

export interface FindFirstRequest {
  context?: unknown;
  model: string;
  where?: unknown;
  select?: PrismaSelect;
  include?: unknown;
}

export interface UpsertRequest {
  context?: unknown;
  model: string;
  where: unknown;
  create: unknown;
  update: unknown;
  select?: PrismaSelect;
  include?: unknown;
}

export interface GolemEngineOptions {
  hooks?: HookRegistry;
  takeLimits?: ReadonlyMap<string, number>;
  authorization?: AuthorizationProvider;
  maxDepth?: number;
  checkWriteResults?: boolean;
  checkReadFields?: boolean;
}

export interface GolemEngineRef {
  current?: GolemEngine;
}

export interface BatchResult {
  count: number;
}

const PRISMA_CODE_PATTERN = /^P\d+$/;

function translateError(error: unknown, model: string): never {
  if (error instanceof Error) {
    if (error.name === 'PrismaClientValidationError') {
      throw new GolemValidationError(error.message);
    }
    const code = (error as { code?: unknown }).code;
    if (typeof code === 'string' && PRISMA_CODE_PATTERN.test(code)) {
      if (code === 'P2025') {
        throw new GolemNotFoundError(`${model} not found`);
      }
      if (code === 'P2002') {
        throw new GolemConflictError(`Unique constraint violation on ${model}`);
      }
      if (code === 'P2003' || code === 'P2014') {
        throw new GolemValidationError(`The requested change violates a relation constraint on ${model}`);
      }
    }
  }
  throw error;
}

export class GolemEngine {
  private readonly modelsByName: Map<string, DatamodelModel>;
  private readonly metadata: ModelMetadataIndex;
  private readonly hooks?: HookRegistry;
  private readonly takeLimits: ReadonlyMap<string, number>;
  private readonly authorization?: AuthorizationProvider;
  private readonly maxDepth: number;
  private readonly checkWriteResults: boolean;
  private readonly checkReadFields: boolean;
  private readonly authorizationSessions = new WeakMap<object, AuthorizationProvider>();

  constructor(
    private readonly client: Record<string, any>,
    models: readonly DatamodelModel[],
    options: GolemEngineOptions = {},
  ) {
    this.modelsByName = new Map(models.map((m) => [m.name, m]));
    this.metadata = buildModelMetadata(models);
    this.hooks = options.hooks;
    this.takeLimits = options.takeLimits ?? new Map();
    this.authorization = options.authorization;
    this.maxDepth = options.maxDepth ?? 5;
    this.checkWriteResults = options.checkWriteResults ?? (this.authorization !== undefined);
    this.checkReadFields = options.checkReadFields ?? (this.authorization !== undefined);
    if (this.checkReadFields && this.authorization) {
      const missing = [
        !this.authorization.classifyFields ? 'classifyFields' : null,
        !this.authorization.checkField ? 'checkField' : null,
      ].filter(Boolean);
      if (missing.length > 0) {
        throw new Error(
          `checkReadFields is enabled but the authorization provider does not implement ${missing.join(' and ')}`,
        );
      }
    }
    if (this.checkWriteResults && this.authorization) {
      const missing = [
        !this.authorization.check ? 'check' : null,
        !this.authorization.checkField ? 'checkField' : null,
      ].filter(Boolean);
      if (missing.length > 0) {
        throw new Error(
          `checkWriteResults is enabled but the authorization provider does not implement ${missing.join(' and ')}`,
        );
      }
    }
  }

  private verifyContext(context: unknown): VerifyContext {
    return {
      modelsByName: this.modelsByName,
      metadata: this.metadata,
      provider: this.enforced(context)!,
      context,
    };
  }

  private pkOf(model: string) {
    const pk = this.metadata.get(model)?.primaryKey;
    if (!pk) {
      throw new GolemValidationError(`Model ${model} has no primary key field`);
    }
    return pk;
  }

  private async prepareRead(request: {
    model: string;
    select?: PrismaSelect;
    include?: unknown;
    context?: unknown;
  }): Promise<PreparedReadTree> {
    return prepareReadTree({
      model: this.modelsByName.get(request.model)!,
      modelsByName: this.modelsByName,
      select: request.select as Record<string, unknown> | undefined,
      include: request.include as Record<string, unknown> | undefined,
      provider: this.enforced(request.context),
      context: request.context,
      maxDepth: this.maxDepth,
      readFieldChecks: this.checkReadFields,
      metadata: this.metadata,
    });
  }

  private async finishRead<T>(
    result: T,
    prepared: PreparedReadTree,
    context: unknown,
  ): Promise<T> {
    if (!result) {
      return result;
    }
    if (prepared.toOneChecks.length > 0) {
      await applyToOneChecks(result, prepared.toOneChecks, this.authorization!, context);
    }
    if (prepared.maskChecks.length > 0) {
      await applyFieldMasks(result, prepared.maskChecks, this.authorization!, context);
    }
    if (prepared.injected.length > 0) {
      await stripInjectedFields(result, prepared.injected);
    }
    return result;
  }

  private enforced(context: unknown): AuthorizationProvider | undefined {
    if (!this.authorization || context === undefined) {
      return undefined;
    }
    if (!context || typeof context !== 'object') return this.authorization;
    const cached = this.authorizationSessions.get(context);
    if (cached) return cached;
    const source = this.authorization;
    const values = new Map<string, Promise<unknown>>();
    const memo = <T>(key: string, load: () => Promise<T>): Promise<T> => {
      const existing = values.get(key) as Promise<T> | undefined;
      if (existing) return existing;
      const pending = load();
      values.set(key, pending);
      return pending;
    };
    const session: AuthorizationProvider = {
      authorize: (action, model, ctx) =>
        memo(`authorize:${action}:${model}`, () => source.authorize(action, model, ctx)),
      constrain: (action, model, ctx) =>
        memo(`constrain:${action}:${model}`, () => source.constrain(action, model, ctx)),
      check: source.check?.bind(source),
      checkField: source.checkField?.bind(source),
      classifyFields: source.classifyFields
        ? (action, model, fields, ctx) =>
            memo(
              `classify:${action}:${model}:${[...fields].sort().join('\u0000')}`,
              () => source.classifyFields!(action, model, fields, ctx),
            )
        : undefined,
      freshContext: source.freshContext?.bind(source),
    };
    this.authorizationSessions.set(context, session);
    return session;
  }

  private async constraintFor(
    action: GolemAction,
    model: string,
    context: unknown,
  ): Promise<unknown> {
    const provider = this.enforced(context);
    if (!provider) {
      return undefined;
    }
    return provider.constrain(action, model, context);
  }

  private async authorizeNestedWrites(
    model: string,
    data: unknown,
    context: unknown,
  ): Promise<void> {
    const provider = this.enforced(context);
    if (!provider) {
      return;
    }
    await this.walkNestedWrites(provider, model, data, context);
  }

  private async walkNestedWrites(
    provider: AuthorizationProvider,
    model: string,
    data: unknown,
    context: unknown,
  ): Promise<void> {
    if (!data || typeof data !== 'object') {
      return;
    }
    const definition = this.modelsByName.get(model);
    if (!definition) {
      return;
    }
    for (const relation of planNestedWrites(this.metadata, definition, data as Record<string, unknown>)) {
      for (const action of relation.modelActions) {
        await provider.authorize(action, relation.target.name, context);
      }
      for (const nested of [...relation.createPayloads, ...relation.updatePayloads]) {
        await this.walkNestedWrites(provider, relation.target.name, nested, context);
      }
    }
  }

  private async resolveConstrainedTarget(
    action: GolemAction,
    request: { model: string; where: unknown; context?: unknown },
  ): Promise<{ where: unknown; row?: Record<string, unknown> }> {
    const constraint = await this.constraintFor(action, request.model, request.context);
    if (constraint === undefined) {
      return { where: request.where };
    }
    const pkField = this.metadata.get(request.model)?.primaryKey;
    if (!pkField) {
      throw new GolemValidationError(`Model ${request.model} has no primary key field`);
    }
    const delegate = this.delegate(request.model);
    const found = (await this.run(request.model, () =>
      delegate.findFirst({
        where: mergeConstraint(request.where, constraint),
        select: { [pkField.name]: true },
      }),
    )) as Record<string, unknown> | null;
    if (!found) {
      throw new GolemNotFoundError(`${request.model} not found`);
    }
    return { where: { [pkField.name]: found[pkField.name] }, row: found };
  }

  private async runBefore<T extends { model: string; context?: unknown }>(
    operation: GolemOperation,
    request: T,
  ): Promise<T> {
    if (!this.hooks) {
      return request;
    }
    let current = request;
    for (const hook of this.hooks.beforeFor(request.model, operation)) {
      const result = await hook(current, {
        model: request.model,
        operation,
        context: request.context,
      });
      if (result !== undefined) {
        current = result as T;
      }
    }
    return current;
  }

  private async runAfter(
    operation: GolemOperation,
    model: string,
    result: unknown,
    context: unknown,
  ): Promise<void> {
    if (!this.hooks) {
      return;
    }
    for (const hook of this.hooks.afterFor(model, operation)) {
      await hook(result, { model, operation, context });
    }
  }

  private enforceTakeLimit(request: FindManyRequest): void {
    const limit = this.takeLimits.get(request.model);
    if (limit !== undefined && request.take !== undefined && Math.abs(request.take) > limit) {
      throw new GolemValidationError(
        `take ${request.take} exceeds the maximum of ${limit} for ${request.model}`,
      );
    }
  }

  private delegate(model: string): Record<string, (args: unknown) => Promise<any>> {
    if (!this.modelsByName.has(model)) {
      throw new GolemValidationError(`Unknown model ${model}`);
    }
    const delegate = this.client[lcFirst(model)];
    if (!delegate) {
      throw new GolemValidationError(`Prisma client has no delegate for model ${model}`);
    }
    return delegate;
  }

  private async run<T>(model: string, action: () => Promise<T>): Promise<T> {
    try {
      return await action();
    } catch (error) {
      translateError(error, model);
    }
  }

  async findOne(request: FindOneRequest): Promise<unknown> {
    const delegate = this.delegate(request.model);
    const req = await this.runBefore('findOne', request);
    const prepared = await this.prepareRead(req);
    const constraint = await this.constraintFor('read', req.model, req.context);
    const found = await this.run(req.model, () =>
      constraint === undefined
        ? delegate.findUnique({ where: req.where, select: prepared.select, include: prepared.include })
        : delegate.findFirst({
            where: mergeConstraint(req.where, constraint),
            select: prepared.select,
            include: prepared.include,
          }),
    );
    await this.finishRead(found, prepared, req.context);
    await this.runAfter('findOne', req.model, found, req.context);
    return found;
  }

  async findMany(request: FindManyRequest): Promise<unknown[]> {
    const delegate = this.delegate(request.model);
    const req = await this.runBefore('findMany', request);
    this.enforceTakeLimit(req);
    const prepared = await this.prepareRead(req);
    const constraint = await this.constraintFor('read', req.model, req.context);
    const found = (await this.run(req.model, () =>
      delegate.findMany({
        where: mergeConstraint(req.where, constraint),
        orderBy: req.orderBy,
        take: req.take,
        skip: req.skip,
        cursor: req.cursor,
        distinct: req.distinct,
        select: prepared.select,
        include: prepared.include,
      }),
    )) as unknown[];
    await this.finishRead(found, prepared, req.context);
    await this.runAfter('findMany', req.model, found, req.context);
    return found;
  }

  async findFirst(request: FindFirstRequest): Promise<unknown> {
    const delegate = this.delegate(request.model);
    const prepared = await this.prepareRead(request);
    const constraint = await this.constraintFor('read', request.model, request.context);
    const found = await this.run(request.model, () =>
      delegate.findFirst({
        where: mergeConstraint(request.where, constraint),
        select: prepared.select,
        include: prepared.include,
      }),
    );
    return this.finishRead(found, prepared, request.context);
  }

  async create(request: CreateRequest): Promise<unknown> {
    const delegate = this.delegate(request.model);
    const req = await this.runBefore('create', request);
    const provider = this.enforced(req.context);
    if (provider) {
      await provider.authorize('create', req.model, req.context);
      await this.authorizeNestedWrites(req.model, req.data, req.context);
    }
    const prepared = await this.prepareRead(req);
    let created: unknown;
    let createPlanFast = false;
    let createPlan: { select: PrismaSelect; fastPath: boolean } | undefined;
    if (provider && this.checkWriteResults) {
      const model = this.modelsByName.get(req.model)!;
      const vctx = this.verifyContext(req.context);
      const constraint = await this.constraintFor('create', req.model, req.context);
      createPlan = await planVerification(
        vctx,
        model,
        req.data as Record<string, unknown>,
        constraint,
        'create',
      );
      createPlanFast = createPlan.fastPath;
    }
    if (provider && this.checkWriteResults && !createPlanFast) {
      const model = this.modelsByName.get(req.model)!;
      const pk = this.pkOf(req.model);
      const vctx = this.verifyContext(req.context);
      created = await withBufferedEvents(() =>
        this.run(req.model, () =>
          (this.client as { $transaction: (fn: (tx: any) => Promise<unknown>) => Promise<unknown> }).$transaction(
            async (tx) => {
              const txDelegate = tx[lcFirst(req.model)];
              const wide = await txDelegate.create({
                data: req.data,
                select: { ...createPlan!.select, [pk.name]: true },
              });
              await verifyCreatedTree(vctx, model, wide, req.data as Record<string, unknown>);
              return txDelegate.findUnique({
                where: { [pk.name]: wide[pk.name] },
                select: prepared.select,
                include: prepared.include,
              });
            },
          ),
        ),
      );
    } else {
      created = await this.run(req.model, () =>
        delegate.create({ data: req.data, select: prepared.select, include: prepared.include }),
      );
    }
    await this.finishRead(created, prepared, req.context);
    await this.runAfter('create', req.model, created, req.context);
    return created;
  }

  async update(request: UpdateRequest): Promise<unknown> {
    const delegate = this.delegate(request.model);
    const req = await this.runBefore('update', request);
    await this.authorizeNestedWrites(req.model, req.data, req.context);
    const provider = this.enforced(req.context);
    const prepared = await this.prepareRead(req);
    let updated: unknown;
    let updatePlan: { select: PrismaSelect; fastPath: boolean } | undefined;
    let updateConstraint: unknown;
    if (provider && this.checkWriteResults) {
      const model = this.modelsByName.get(req.model)!;
      const vctx = this.verifyContext(req.context);
      updateConstraint = await this.constraintFor('update', req.model, req.context);
      updatePlan = await planVerification(
        vctx,
        model,
        req.data as Record<string, unknown>,
        updateConstraint,
        'update',
      );
    }
    if (provider && this.checkWriteResults && updatePlan && !updatePlan.fastPath) {
      const model = this.modelsByName.get(req.model)!;
      const pk = this.pkOf(req.model);
      const vctx = this.verifyContext(req.context);
      const constraint = updateConstraint;
      const wide = { ...updatePlan!.select, [pk.name]: true };
      updated = await withBufferedEvents(() =>
        this.run(req.model, () =>
          (this.client as { $transaction: (fn: (tx: any) => Promise<unknown>) => Promise<unknown> }).$transaction(
            async (tx) => {
              const txDelegate = tx[lcFirst(req.model)];
              const before = await txDelegate.findFirst({
                where: mergeConstraint(req.where, constraint),
                select: wide,
              });
              if (!before) {
                throw new GolemNotFoundError(`${req.model} not found`);
              }
              const after = await txDelegate.update({
                where: { [pk.name]: before[pk.name] },
                data: req.data,
                select: wide,
              });
              await verifyUpdatedRow(vctx, model, before, after, req.data as Record<string, unknown>);
              return txDelegate.findUnique({
                where: { [pk.name]: before[pk.name] },
                select: prepared.select,
                include: prepared.include,
              });
            },
          ),
        ),
      );
    } else {
      const target = await this.resolveConstrainedTarget('update', req);
      updated = await this.run(req.model, () =>
        delegate.update({
          where: target.where,
          data: req.data,
          select: prepared.select,
          include: prepared.include,
        }),
      );
    }
    await this.finishRead(updated, prepared, req.context);
    await this.runAfter('update', req.model, updated, req.context);
    return updated;
  }

  async updateMany(request: UpdateManyRequest): Promise<BatchResult> {
    const delegate = this.delegate(request.model);
    const req = await this.runBefore('updateMany', request);
    await this.authorizeNestedWrites(req.model, req.data, req.context);
    const constraint = await this.constraintFor('update', req.model, req.context);
    const provider = this.enforced(req.context);
    let result: BatchResult;
    let manyPlan: { select: PrismaSelect; fastPath: boolean } | undefined;
    if (provider && this.checkWriteResults) {
      const vctx = this.verifyContext(req.context);
      manyPlan = await planVerification(
        vctx,
        this.modelsByName.get(req.model)!,
        req.data as Record<string, unknown>,
        constraint,
        'update',
      );
    }
    if (provider && this.checkWriteResults && manyPlan && !manyPlan.fastPath) {
      const model = this.modelsByName.get(req.model)!;
      const pk = this.pkOf(req.model);
      const vctx = this.verifyContext(req.context);
      const scalars = { ...manyPlan.select, [pk.name]: true };
      result = (await withBufferedEvents(() =>
        this.run(req.model, () =>
          (this.client as { $transaction: (fn: (tx: any) => Promise<unknown>) => Promise<unknown> }).$transaction(
            async (tx) => {
              const txDelegate = tx[lcFirst(req.model)];
              const beforeRows = (await txDelegate.findMany({
                where: mergeConstraint(req.where, constraint),
                select: scalars,
              })) as Record<string, unknown>[];
              const ids = beforeRows.map((row) => row[pk.name]);
              await txDelegate.updateMany({ where: { [pk.name]: { in: ids } }, data: req.data });
              const afterRows = (await txDelegate.findMany({
                where: { [pk.name]: { in: ids } },
                select: scalars,
              })) as Record<string, unknown>[];
              const afterById = new Map(afterRows.map((row) => [row[pk.name], row]));
              await runPolicyChecks(beforeRows.map((before) => async () => {
                const after = afterById.get(before[pk.name]);
                if (after) {
                  await verifyUpdatedRow(vctx, model, before, after, req.data as Record<string, unknown>);
                }
              }));
              return { count: ids.length };
            },
          ),
        ),
      )) as BatchResult;
    } else {
      result = (await this.run(req.model, () =>
        delegate.updateMany({ where: mergeConstraint(req.where, constraint), data: req.data }),
      )) as BatchResult;
    }
    await this.runAfter('updateMany', req.model, result, req.context);
    return result;
  }

  async delete(request: DeleteRequest): Promise<unknown> {
    const delegate = this.delegate(request.model);
    const req = await this.runBefore('delete', request);
    const { where } = await this.resolveConstrainedTarget('delete', req);
    const prepared = await this.prepareRead(req);
    const deleted = await this.run(req.model, () =>
      delegate.delete({ where, select: prepared.select, include: prepared.include }),
    );
    await this.finishRead(deleted, prepared, req.context);
    await this.runAfter('delete', req.model, deleted, req.context);
    return deleted;
  }

  async upsert(request: UpsertRequest): Promise<unknown> {
    const delegate = this.delegate(request.model);
    const pkField = this.metadata.get(request.model)?.primaryKey;
    if (!pkField) {
      throw new GolemValidationError(`Model ${request.model} has no primary key field`);
    }
    const existing = await this.run(request.model, () =>
      delegate.findFirst({ where: request.where, select: { [pkField.name]: true } }),
    );
    if (existing) {
      return this.update({
        model: request.model,
        where: request.where,
        data: request.update,
        select: request.select,
        include: request.include,
        context: request.context,
      });
    }
    return this.create({
      model: request.model,
      data: request.create,
      select: request.select,
      include: request.include,
      context: request.context,
    });
  }

  async deleteMany(request: DeleteManyRequest): Promise<BatchResult> {
    const delegate = this.delegate(request.model);
    const req = await this.runBefore('deleteMany', request);
    const constraint = await this.constraintFor('delete', req.model, req.context);
    const result = (await this.run(req.model, () =>
      delegate.deleteMany({ where: mergeConstraint(req.where, constraint) }),
    )) as BatchResult;
    await this.runAfter('deleteMany', req.model, result, req.context);
    return result;
  }
}
