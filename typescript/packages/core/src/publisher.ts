import { DatamodelDocument } from './datamodel';
import { canonicalToken } from './canonical';
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
  delete: 'DELETED',
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
  updateManyAndReturn?(args: unknown): Promise<Record<string, unknown>[]>;
  deleteMany(args: unknown): Promise<{ count: number }>;
}

export interface GolemBatchRuntime {
  /** True only for auxiliary operations issued by the publisher itself. */
  suppressed: boolean;
  run<T>(work: (delegate: GolemBatchDelegate) => Promise<T>): Promise<T>;
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
    const keys = model.primaryKey?.fields ?? model.fields
      .filter((field) => field.isId)
      .map((field) => field.name);
    if (keys.length > 0) {
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

  return async ({ model, operation, args, query, findExisting, batch }) => {
    if (batch?.suppressed) return query(args);
    if (
      model &&
      options.models.has(model) &&
      (operation === 'updateMany' || operation === 'deleteMany')
    ) {
      const pks = pkByModel.get(model);
      if (!pks || !batch) return query(args);
      if (
        operation === 'updateMany' &&
        pks.some((pk) => Object.prototype.hasOwnProperty.call(args?.data ?? {}, pk))
      ) {
        throw new GolemValidationError(
          `Eventful updateMany cannot modify primary key fields on ${model}`,
        );
      }
      const select = Object.fromEntries(pks.map((pk) => [pk, true]));
      return batch.run(async (delegate) => {
        const rows = await delegate.findMany({
          where: args?.where,
          ...(operation === 'updateMany' ? { select } : {}),
          orderBy: stableOrder(pks),
          take: maxBatchRows + 1,
        });
        if (rows.length > maxBatchRows) {
          throw new GolemValidationError(
            `Eventful ${operation} on ${model} exceeds the maximum of ${maxBatchRows} rows`,
          );
        }
        if (rows.length === 0) return { count: 0 };
        const eventType: GolemEventType = operation === 'updateMany' ? 'UPDATED' : 'DELETED';
        const events: GolemEventPayload[] = rows.map((row) => ({
          type: eventType,
          model,
          id: identityOf(row, pks),
          ...(eventType === 'DELETED' ? { entity: row } : {}),
        }));
        const payloadBytes = encodedGolemEventBytes({ kind: 'batch', events });
        if (payloadBytes > maxBatchPayloadBytes) {
          throw new GolemValidationError(
            `Eventful ${operation} on ${model} produces ${payloadBytes} event bytes, exceeding the maximum of ${maxBatchPayloadBytes}`,
          );
        }
        const where = exactRowsWhere(rows, pks);
        let updatedRows: readonly Record<string, unknown>[] | undefined;
        if (operation === 'updateMany') {
          if (!delegate.updateManyAndReturn) {
            throw new GolemValidationError(
              `Eventful updateMany on ${model} requires transaction-bound updateManyAndReturn`,
            );
          }
          const updated = await delegate.updateManyAndReturn({
            where,
            data: args.data,
            select: scalarSelectByModel.get(model) ?? select,
          });
          updatedRows = updated;
          if (updated.length !== rows.length) {
            throw new GolemConflictError(
              `Eventful updateMany on ${model} changed ${updated.length} rows after selecting ${rows.length}`,
            );
          }
          const updatedByIdentity = new Map(
            updated.map((row) => [canonicalToken(identityOf(row, pks)), row]),
          );
          if (rows.some((row) => !updatedByIdentity.has(canonicalToken(identityOf(row, pks))))) {
            throw new GolemConflictError(
              `Eventful updateMany on ${model} returned a different identity set`,
            );
          }
        } else {
          const deleted = await delegate.deleteMany({ where });
          if (deleted.count !== rows.length) {
            throw new GolemConflictError(
              `Eventful deleteMany on ${model} deleted ${deleted.count} rows after selecting ${rows.length}`,
            );
          }
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
      const pks = pkByModel.get(model);
      if (!pks || !findExisting) return query(args);
      const existing = await findExisting(
        args?.where,
        Object.fromEntries(pks.map((pk) => [pk, true])),
      );
      type = existing ? 'UPDATED' : 'CREATED';
    }
    if (!type || !model || !options.models.has(model)) {
      return query(args);
    }
    const pks = pkByModel.get(model);
    if (!pks) {
      return query(args);
    }
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
    const result = await query(finalArgs);
    const eventEntity =
      result && typeof result === 'object' && !Array.isArray(result)
        ? { ...(result as Record<string, unknown>) }
        : undefined;
    const identity: GolemEventIdentity = pks.length === 1
      ? (result as Record<string, GolemEventIdentityScalar>)[pks[0]]
      : Object.fromEntries(
          pks.map((pk) => [pk, (result as Record<string, GolemEventIdentityScalar>)[pk]]),
        );
    const payload = {
      type,
      model,
      id: identity,
      ...(type === 'DELETED' && eventEntity
        ? { entity: eventEntity }
        : {}),
    };
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
}
