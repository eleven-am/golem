import { DatamodelDocument, rowIdentityFields, supportedProvider } from './datamodel';
import { canonicalToken } from './canonical';
import { LinkGuard, LinkGuardPort } from './link-guard';
import { flattenUniqueSelectors } from './model-meta';
import {
  CascadePlan,
  CascadeRow,
  CascadeTouched,
  CascadeTransaction,
  enumerateNestedDeletes,
  transactionRowLocks,
  lockCascade,
  rowLocker,
  hasNestedDeletes,
  tooManyTouchedRows,
} from './cascade';
import { bufferEvent } from './event-buffer';
import { encodedGolemEventBytes } from './event-codec';
import { GolemConflictError, GolemValidationError } from './errors';
import {
  GolemEventBus,
  GolemEventIdentity,
  GolemEventIdentityScalar,
  GolemEventPayload,
  GolemEventType,
  eventTopic,
} from './events';

export interface GolemQueryParams {
  model?: string;
  operation: string;
  args: any;
  query: (args: any) => Promise<any>;
  findExisting?: (where: unknown, select: Record<string, true>) => Promise<unknown>;
  batch?: GolemBatchRuntime;
}

export type GolemQueryInterceptor = (params: GolemQueryParams) => Promise<any>;

const OPERATION_EVENTS: Record<string, GolemEventType> = {
  create: 'CREATED',
  update: 'UPDATED',
};

export interface CreateEventPublisherOptions {
  datamodel: DatamodelDocument<any>;
  eventBus: GolemEventBus;
  models: ReadonlySet<string>;
  batch?: GolemBatchEventOptions;
}

export const DEFAULT_BATCH_EVENT_MAX_ROWS = 1_000;
export const DEFAULT_BATCH_EVENT_MAX_PAYLOAD_BYTES = 1_048_576;

export interface GolemBatchEventOptions {
  maxRows?: number;
  maxPayloadBytes?: number;
}

export interface GolemBatchDelegate {
  findMany(args: unknown): Promise<Record<string, unknown>[]>;
  findUnique(args: unknown): Promise<Record<string, unknown> | null>;
  create(args: unknown): Promise<unknown>;
  createMany(args: unknown): Promise<{ count: number }>;
  createManyAndReturn(args: unknown): Promise<Record<string, unknown>[]>;
  updateMany(args: unknown): Promise<{ count: number }>;
  updateManyAndReturn?(args: unknown): Promise<Record<string, unknown>[]>;
  update(args: unknown): Promise<unknown>;
  upsert(args: unknown): Promise<unknown>;
  delete(args: unknown): Promise<unknown>;
  deleteMany(args: unknown): Promise<{ count: number }>;
}

export interface GolemBatchTransaction {
  readonly scope: object;
  delegate(model: string): GolemBatchDelegate;
  queryRaw(sql: string, ...values: unknown[]): Promise<unknown>;
}

export interface GolemBatchRuntime {
  /** True only for auxiliary operations issued by the publisher itself. */
  suppressed: boolean;
  run<T>(work: (delegate: GolemBatchDelegate, transaction: GolemBatchTransaction) => Promise<T>): Promise<T>;
}

export const GOLEM_BATCH_RESULT_ROWS = Symbol.for('@eleven-am/golem.batch-result-rows');

type BatchResultWithRows = { count: number; [GOLEM_BATCH_RESULT_ROWS]?: readonly Record<string, unknown>[] };

function batchResult(
  count: number,
  rows?: readonly Record<string, unknown>[],
): BatchResultWithRows {
  const result: BatchResultWithRows = { count };
  if (rows) {
    Object.defineProperty(result, GOLEM_BATCH_RESULT_ROWS, {
      value: rows,
      enumerable: false,
      configurable: false,
      writable: false,
    });
  }
  return result;
}

export function batchEventRows(result: unknown): readonly Record<string, unknown>[] | undefined {
  return result && typeof result === 'object'
    ? (result as BatchResultWithRows)[GOLEM_BATCH_RESULT_ROWS]
    : undefined;
}

function positiveLimit(value: number, name: string): number {
  if (!Number.isSafeInteger(value) || value < 1) {
    throw new Error(`${name} must be a positive safe integer`);
  }
  return value;
}

function identityOf(
  row: Record<string, unknown>,
  pks: readonly string[],
): GolemEventIdentity {
  return pks.length === 1
    ? row[pks[0]] as GolemEventIdentityScalar
    : Object.fromEntries(pks.map((pk) => [pk, row[pk] as GolemEventIdentityScalar]));
}

function identityWhere(row: Record<string, unknown>, pks: readonly string[]): Record<string, unknown> {
  return Object.fromEntries(pks.map((pk) => [pk, row[pk]]));
}

function exactRowsWhere(rows: readonly Record<string, unknown>[], pks: readonly string[]): unknown {
  return { OR: rows.map((row) => identityWhere(row, pks)) };
}

function stableOrder(pks: readonly string[]): readonly Record<string, 'asc'>[] {
  return pks.map((pk) => ({ [pk]: 'asc' }));
}

async function publishEvents(
  bus: GolemEventBus,
  topic: string,
  events: readonly GolemEventPayload[],
): Promise<void> {
  if (events.length === 0) return;
  if (bus.publishMany) {
    await bus.publishMany(topic, events);
    return;
  }
  for (const event of events) await bus.publish(topic, event);
}

const DELETE_ARGUMENTS: Record<'delete' | 'deleteMany', ReadonlySet<string>> = {
  delete: new Set(['where', 'select', 'include', 'omit']),
  deleteMany: new Set(['where', 'limit']),
};

function deleteSelection(
  model: string,
  operation: 'delete' | 'deleteMany',
  args: unknown,
  pks: readonly string[],
  select: Record<string, true>,
  maxRows: number,
): Record<string, unknown> {
  const given = (args ?? {}) as Record<string, unknown>;
  for (const [name, value] of Object.entries(given)) {
    if (value !== undefined && !DELETE_ARGUMENTS[operation].has(name)) {
      throw new GolemValidationError(`${operation} on ${model} does not support the argument ${name}`);
    }
  }
  if (operation === 'delete') {
    return { where: given.where, select };
  }
  const limit = given.limit;
  if (limit !== undefined && (typeof limit !== 'number' || !Number.isSafeInteger(limit) || limit < 0)) {
    throw new GolemValidationError(`deleteMany on ${model} needs a non-negative integer limit`);
  }
  return {
    where: given.where,
    select,
    orderBy: stableOrder(pks),
    take: limit === undefined ? maxRows + 1 : Math.min(limit, maxRows + 1),
  };
}

const GUARDED_WRITES = new Set([
  'create',
  'createMany',
  'createManyAndReturn',
  'update',
  'upsert',
  'updateMany',
  'updateManyAndReturn',
]);

export function createEventPublisher(options: CreateEventPublisherOptions): GolemQueryInterceptor {
  const provider = supportedProvider(options.datamodel.provider);
  const maxBatchRows = positiveLimit(
    options.batch?.maxRows ?? DEFAULT_BATCH_EVENT_MAX_ROWS,
    'batch.maxRows',
  );
  const maxBatchPayloadBytes = positiveLimit(
    options.batch?.maxPayloadBytes ?? DEFAULT_BATCH_EVENT_MAX_PAYLOAD_BYTES,
    'batch.maxPayloadBytes',
  );
  const pkByModel = new Map<string, readonly string[]>();
  const scalarSelectByModel = new Map<string, Readonly<Record<string, true>>>();
  for (const model of options.datamodel.models) {
    const keys = rowIdentityFields(model);
    if (!keys && options.models.has(model.name)) {
      throw new Error(`Model ${model.name} has no primary key or required unique field and cannot publish events`);
    }
    if (keys) {
      pkByModel.set(model.name, keys);
      scalarSelectByModel.set(
        model.name,
        Object.fromEntries(
          model.fields
            .filter((field) => field.kind !== 'object' && !field.isList)
            .map((field) => [field.name, true]),
        ),
      );
    }
  }

  const cascades = new CascadePlan(options.datamodel.models);
  for (const model of options.datamodel.models) {
    for (const dependency of cascades.dependentsOf(model.name)) {
      if (dependency.action === 'Cascade' || !options.models.has(dependency.dependent)) continue;
      const identity = pkByModel.get(dependency.dependent)!;
      const moved = dependency.from.filter((name) => identity.includes(name));
      if (moved.length > 0) {
        throw new Error(
          `Model ${dependency.dependent} cannot publish events: deleting a ${model.name} would ${dependency.action} ${moved.join(', ')}, which identify its rows, so the change event could not name the row`,
        );
      }
    }
  }
  const cascadeTransaction = (transaction: GolemBatchTransaction): CascadeTransaction => ({
    findMany: (model, args) => transaction.delegate(model).findMany(args),
  });
  const lockerFor = (transaction: GolemBatchTransaction) =>
    rowLocker(provider, cascades, (sql, values) => transaction.queryRaw(sql, ...values));
  const touchedEvents = (
    operation: string,
    model: string,
    touched: CascadeTouched,
  ): Map<string, GolemEventPayload[]> => {
    const topics = new Map<string, GolemEventPayload[]>();
    for (const [type, entries] of [['DELETED', touched.deleted], ['UPDATED', touched.updated]] as const) {
      for (const { model: touchedModel, row } of entries) {
        if (!options.models.has(touchedModel)) continue;
        const events = topics.get(touchedModel) ?? [];
        events.push({
          type,
          model: touchedModel,
          id: identityOf(row, pkByModel.get(touchedModel)!),
          ...(type === 'DELETED' ? { entity: row } : {}),
        });
        topics.set(touchedModel, events);
      }
    }
    for (const [touchedModel, events] of topics) {
      const payloadBytes = encodedGolemEventBytes({ kind: 'batch', events });
      if (payloadBytes > maxBatchPayloadBytes) {
        throw new GolemValidationError(
          `Eventful ${operation} on ${model} produces ${payloadBytes} event bytes for ${touchedModel}, exceeding the maximum of ${maxBatchPayloadBytes}`,
        );
      }
    }
    return topics;
  };
  const publishTopics = async (topics: Map<string, GolemEventPayload[]>): Promise<void> => {
    const publishAll = async () => {
      for (const [touchedModel, events] of topics) {
        await publishEvents(options.eventBus, eventTopic(touchedModel), events);
      }
    };
    if (!bufferEvent({ publish: publishAll })) await publishAll();
  };
  const writeRow = async (
    type: GolemEventType,
    model: string,
    args: any,
    run: (args: any) => Promise<any>,
  ): Promise<any> => {
    if (!options.models.has(model)) {
      return run(args);
    }
    const pks = pkByModel.get(model)!;
    const injectedSelect = new Set(
      args?.select ? pks.filter((pk) => args.select[pk] !== true) : [],
    );
    const injectedOmit = new Set(pks.filter((pk) => args?.omit?.[pk] === true));
    const finalArgs = args?.select
      ? {
          ...args,
          select: {
            ...args.select,
            ...Object.fromEntries(pks.map((pk) => [pk, true])),
          },
        }
      : injectedOmit.size > 0
        ? {
            ...args,
            omit: {
              ...args.omit,
              ...Object.fromEntries([...injectedOmit].map((pk) => [pk, false])),
            },
          }
        : args;
    const result = await run(finalArgs);
    const eventEntity =
      result && typeof result === 'object' && !Array.isArray(result)
        ? { ...(result as Record<string, unknown>) }
        : undefined;
    const identity: GolemEventIdentity = pks.length === 1
      ? (result as Record<string, GolemEventIdentityScalar>)[pks[0]]
      : Object.fromEntries(
          pks.map((pk) => [pk, (result as Record<string, GolemEventIdentityScalar>)[pk]]),
        );
    const payload = { type, model, id: identity };
    const deferred = bufferEvent({
      publish: () => options.eventBus.publish(eventTopic(model), payload),
    });
    if (!deferred) {
      await options.eventBus.publish(eventTopic(model), payload);
    }
    if ((injectedSelect.size > 0 || injectedOmit.size > 0) && eventEntity) {
      const publicResult = { ...eventEntity };
      for (const pk of [...injectedSelect, ...injectedOmit]) {
        delete publicResult[pk];
      }
      return publicResult;
    }
    return result;
  };
  const single = (read: Promise<Record<string, unknown> | null>) =>
    read.then((row) => (row ? [row] : []));
  const guardPort = (transaction: GolemBatchTransaction): LinkGuardPort => ({
    metadata: cascades.metadata,
    unwrap: (target, where) => flattenUniqueSelectors(cascades.metadata.get(target), where).where,
    findFirst: async (target, where) => {
      const [row] = await transaction.delegate(target).findMany({
        where,
        select: Object.fromEntries(cascades.identity(target).map((name) => [name, true])),
        take: 1,
      });
      return row ?? null;
    },
    findMany: (target, where) => transaction.delegate(target).findMany({
      where,
      select: Object.fromEntries(cascades.identity(target).map((name) => [name, true])),
    }),
    readable: async () => [],
    readableRow: async () => null,
    locks: () => transactionRowLocks(transaction.scope, cascades),
    locker: () => lockerFor(transaction),
    locksRows: provider === 'postgresql',
  });

  return async ({ model, operation, args, query, findExisting, batch }) => {
    if (batch?.suppressed) return query(args);
    const golemModel = model !== undefined && cascades.metadata.has(model);
    if (golemModel && (operation === 'delete' || operation === 'deleteMany')) {
      if (!batch) {
        throw new Error(`${operation} on ${model} requires the transaction-bound batch runtime`);
      }
      const pks = cascades.identity(model);
      return batch.run(async (delegate, transaction) => {
        const selection = deleteSelection(model, operation, args, pks, cascades.scalarSelect(model), maxBatchRows);
        const readScope = async () => {
          const found = operation === 'delete'
            ? await single(delegate.findUnique(selection))
            : await delegate.findMany(selection);
          if (found.length > maxBatchRows) {
            throw tooManyTouchedRows(model, maxBatchRows);
          }
          return { roots: found.map((row) => ({ model, row })), parents: [] };
        };
        const touched = await lockCascade(
          cascades,
          cascadeTransaction(transaction),
          transactionRowLocks(transaction.scope, cascades),
          lockerFor(transaction),
          model,
          readScope,
          maxBatchRows,
        );
        const rows = touched.roots.map(({ row }) => row);
        if (rows.length === 0) {
          return operation === 'delete' ? delegate.delete(args) : { count: 0 };
        }
        const topics = touchedEvents(operation, model, touched);
        let result: unknown;
        if (operation === 'delete') {
          result = await delegate.delete(args);
        } else {
          const deleted = await delegate.deleteMany({ where: exactRowsWhere(rows, pks) });
          if (deleted.count !== rows.length) {
            throw new GolemConflictError(
              `Eventful deleteMany on ${model} deleted ${deleted.count} rows after selecting ${rows.length}`,
            );
          }
          result = batchResult(rows.length);
        }
        await publishTopics(topics);
        return result;
      });
    }
    if (!golemModel || !GUARDED_WRITES.has(operation)) {
      return query(args);
    }
    if (!batch) {
      throw new Error(`${operation} on ${model} requires the transaction-bound batch runtime`);
    }
    const pks = cascades.identity(model);
    const identitySelect = Object.fromEntries(pks.map((pk) => [pk, true]));
    const eventful = options.models.has(model);
    const batchUpdate = operation === 'updateMany' || operation === 'updateManyAndReturn';
    if (batchUpdate && eventful && pks.some((pk) => Object.prototype.hasOwnProperty.call(args?.data ?? {}, pk))) {
      throw new GolemValidationError(`Eventful ${operation} cannot modify primary key fields on ${model}`);
    }
    return batch.run(async (delegate, transaction) => {
      const lockWrite = (data: unknown, roots: readonly Record<string, unknown>[]) =>
        LinkGuard.of(guardPort(transaction), model, data, false).before(transaction.scope, roots);
      if (operation === 'create') {
        await lockWrite(args?.data, []);
        return writeRow('CREATED', model, args, (finalArgs) => delegate.create(finalArgs));
      }
      if (operation === 'createMany' || operation === 'createManyAndReturn') {
        for (const item of Array.isArray(args?.data) ? args.data : [args?.data]) {
          await lockWrite(item, []);
        }
        return operation === 'createMany' ? delegate.createMany(args) : delegate.createManyAndReturn(args);
      }
      if (operation === 'update' || operation === 'upsert') {
        const root = await delegate.findUnique({ where: args?.where, select: identitySelect });
        const data = operation === 'upsert' ? (root ? args?.update : args?.create) : args?.data;
        await lockWrite(data, root ? [root] : []);
        const tx = cascadeTransaction(transaction);
        const nestedData = root ? (operation === 'upsert' ? args?.update : args?.data) : undefined;
        const touched = !hasNestedDeletes(cascades, model, nestedData) ? { deleted: [], updated: [] } : await lockCascade(
          cascades,
          tx,
          transactionRowLocks(transaction.scope, cascades),
          lockerFor(transaction),
          model,
          async () => {
            const parents = await single(delegate.findUnique({ where: args?.where, select: cascades.scalarSelect(model) }));
            const removed: CascadeRow[] = [];
            await enumerateNestedDeletes(cascades, tx, model, parents, nestedData, maxBatchRows, removed);
            return { roots: removed, parents: parents.map((row) => ({ model, row })) };
          },
          maxBatchRows,
        );
        const topics = touchedEvents(operation, model, touched);
        const result = await writeRow(
          operation === 'upsert' && !root ? 'CREATED' : 'UPDATED',
          model,
          args,
          (finalArgs) => (operation === 'upsert' ? delegate.upsert(finalArgs) : delegate.update(finalArgs)),
        );
        await publishTopics(topics);
        return result;
      }
      const rows = await delegate.findMany({
        where: args?.where,
        select: identitySelect,
        orderBy: stableOrder(pks),
        take: args?.limit === undefined ? maxBatchRows + 1 : Math.min(args.limit, maxBatchRows + 1),
      });
      if (rows.length > maxBatchRows) {
        throw new GolemValidationError(`${operation} on ${model} exceeds the maximum of ${maxBatchRows} rows`);
      }
      await lockWrite(args?.data, rows);
      const where = exactRowsWhere(rows, pks);
      if (operation === 'updateManyAndReturn') {
        return rows.length === 0 ? [] : delegate.updateManyAndReturn!({ ...args, where, limit: undefined });
      }
      if (rows.length === 0) return { count: 0 };
      if (!eventful) {
        return delegate.updateMany({ where, data: args.data });
      }
      const events: GolemEventPayload[] = rows.map((row) => ({ type: 'UPDATED', model, id: identityOf(row, pks) }));
      const payloadBytes = encodedGolemEventBytes({ kind: 'batch', events });
      if (payloadBytes > maxBatchPayloadBytes) {
        throw new GolemValidationError(
          `Eventful ${operation} on ${model} produces ${payloadBytes} event bytes, exceeding the maximum of ${maxBatchPayloadBytes}`,
        );
      }
      if (!delegate.updateManyAndReturn) {
        throw new GolemValidationError(`Eventful updateMany on ${model} requires transaction-bound updateManyAndReturn`);
      }
      const updatedRows = await delegate.updateManyAndReturn({
        where,
        data: args.data,
        select: scalarSelectByModel.get(model) ?? identitySelect,
      });
      if (updatedRows.length !== rows.length) {
        throw new GolemConflictError(
          `Eventful updateMany on ${model} changed ${updatedRows.length} rows after selecting ${rows.length}`,
        );
      }
      const updatedByIdentity = new Map(updatedRows.map((row) => [canonicalToken(identityOf(row, pks)), row]));
      if (rows.some((row) => !updatedByIdentity.has(canonicalToken(identityOf(row, pks))))) {
        throw new GolemConflictError(`Eventful updateMany on ${model} returned a different identity set`);
      }
      const deferred = bufferEvent({
        publish: () => publishEvents(options.eventBus, eventTopic(model), events),
      });
      if (!deferred) await publishEvents(options.eventBus, eventTopic(model), events);
      return batchResult(rows.length, updatedRows);
    });
  };
}
