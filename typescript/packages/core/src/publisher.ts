import { DatamodelDocument, rowIdentityFields } from './datamodel';
import { canonicalToken } from './canonical';
import {
  CascadePlan,
  CascadeRow,
  CascadeTouched,
  CascadeTransaction,
  cascadeDialect,
  enumerateCascade,
  enumerateNestedDeletes,
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
  updateManyAndReturn?(args: unknown): Promise<Record<string, unknown>[]>;
  update(args: unknown): Promise<unknown>;
  upsert(args: unknown): Promise<unknown>;
  delete(args: unknown): Promise<unknown>;
  deleteMany(args: unknown): Promise<{ count: number }>;
}

export interface GolemBatchTransaction {
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

export function createEventPublisher(options: CreateEventPublisherOptions): GolemQueryInterceptor {
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
  const dialect = cascadeDialect(options.datamodel.provider, cascades);
  const cascadeTransaction = (transaction: GolemBatchTransaction): CascadeTransaction => ({
    findMany: (model, args) => transaction.delegate(model).findMany(args),
    queryRaw: (sql, ...values) => transaction.queryRaw(sql, ...values) as Promise<Record<string, unknown>[]>,
  });
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

  return async ({ model, operation, args, query, findExisting, batch }) => {
    if (batch?.suppressed) return query(args);
    const golemModel = model !== undefined && cascades.metadata.has(model);
    if (golemModel && (operation === 'delete' || operation === 'deleteMany')) {
      if (!batch) {
        throw new Error(`${operation} on ${model} requires the transaction-bound batch runtime`);
      }
      const pks = cascades.primaryKey(model);
      return batch.run(async (delegate, transaction) => {
        const tx = cascadeTransaction(transaction);
        const rows = await dialect.lockedRoots(tx, model, (select) =>
          operation === 'delete'
            ? single(delegate.findUnique({ where: args?.where, select }))
            : delegate.findMany({
                where: args?.where,
                select,
                orderBy: stableOrder(pks),
                take: maxBatchRows + 1,
              }));
        if (rows.length > maxBatchRows) {
          throw tooManyTouchedRows(model, maxBatchRows);
        }
        if (rows.length === 0) {
          return operation === 'delete' ? delegate.delete(args) : { count: 0 };
        }
        const touched = await enumerateCascade(
          cascades,
          dialect,
          tx,
          model,
          rows.map((row) => ({ model, row })),
          maxBatchRows,
        );
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
    const nestedData = operation === 'upsert' ? args?.update : args?.data;
    if (
      golemModel &&
      (operation === 'update' || operation === 'upsert') &&
      hasNestedDeletes(cascades, model, nestedData)
    ) {
      if (!batch) {
        throw new Error(`${operation} on ${model} requires the transaction-bound batch runtime`);
      }
      return batch.run(async (delegate, transaction) => {
        const tx = cascadeTransaction(transaction);
        const parents = await dialect.lockedRoots(tx, model, (select) =>
          single(delegate.findUnique({ where: args?.where, select })));
        const removed: CascadeRow[] = [];
        await enumerateNestedDeletes(cascades, dialect, tx, model, parents, nestedData, maxBatchRows, removed);
        const touched = await enumerateCascade(cascades, dialect, tx, model, removed, maxBatchRows);
        const topics = touchedEvents(operation, model, touched);
        const result = await writeRow(
          operation === 'upsert' && parents.length === 0 ? 'CREATED' : 'UPDATED',
          model,
          args,
          (finalArgs) => (operation === 'upsert' ? delegate.upsert(finalArgs) : delegate.update(finalArgs)),
        );
        await publishTopics(topics);
        return result;
      });
    }
    if (
      model &&
      options.models.has(model) &&
      operation === 'updateMany'
    ) {
      const pks = pkByModel.get(model)!;
      if (!batch) {
        throw new Error(`updateMany on ${model} requires the transaction-bound batch runtime`);
      }
      if (pks.some((pk) => Object.prototype.hasOwnProperty.call(args?.data ?? {}, pk))) {
        throw new GolemValidationError(
          `Eventful updateMany cannot modify primary key fields on ${model}`,
        );
      }
      const select = Object.fromEntries(pks.map((pk) => [pk, true]));
      return batch.run(async (delegate) => {
        const rows = await delegate.findMany({
          where: args?.where,
          select,
          orderBy: stableOrder(pks),
          take: maxBatchRows + 1,
        });
        if (rows.length > maxBatchRows) {
          throw new GolemValidationError(
            `Eventful ${operation} on ${model} exceeds the maximum of ${maxBatchRows} rows`,
          );
        }
        if (rows.length === 0) return { count: 0 };
        const events: GolemEventPayload[] = rows.map((row) => ({
          type: 'UPDATED',
          model,
          id: identityOf(row, pks),
        }));
        const payloadBytes = encodedGolemEventBytes({ kind: 'batch', events });
        if (payloadBytes > maxBatchPayloadBytes) {
          throw new GolemValidationError(
            `Eventful ${operation} on ${model} produces ${payloadBytes} event bytes, exceeding the maximum of ${maxBatchPayloadBytes}`,
          );
        }
        const where = exactRowsWhere(rows, pks);
        if (!delegate.updateManyAndReturn) {
          throw new GolemValidationError(
            `Eventful updateMany on ${model} requires transaction-bound updateManyAndReturn`,
          );
        }
        const updatedRows = await delegate.updateManyAndReturn({
          where,
          data: args.data,
          select: scalarSelectByModel.get(model) ?? select,
        });
        if (updatedRows.length !== rows.length) {
          throw new GolemConflictError(
            `Eventful updateMany on ${model} changed ${updatedRows.length} rows after selecting ${rows.length}`,
          );
        }
        const updatedByIdentity = new Map(
          updatedRows.map((row) => [canonicalToken(identityOf(row, pks)), row]),
        );
        if (rows.some((row) => !updatedByIdentity.has(canonicalToken(identityOf(row, pks))))) {
          throw new GolemConflictError(
            `Eventful updateMany on ${model} returned a different identity set`,
          );
        }
        const deferred = bufferEvent({
          publish: () => publishEvents(options.eventBus, eventTopic(model), events),
        });
        if (!deferred) await publishEvents(options.eventBus, eventTopic(model), events);
        return batchResult(rows.length, updatedRows);
      });
    }
    let type = OPERATION_EVENTS[operation];
    if (operation === 'upsert') {
      if (!model || !options.models.has(model)) return query(args);
      const pks = pkByModel.get(model)!;
      if (!findExisting) {
        throw new Error(`upsert on ${model} requires the branch probe of the generated client`);
      }
      const existing = await findExisting(
        args?.where,
        Object.fromEntries(pks.map((pk) => [pk, true])),
      );
      type = existing ? 'UPDATED' : 'CREATED';
    }
    if (!type || !model) {
      return query(args);
    }
    return writeRow(type, model, args, query);
  };
}
