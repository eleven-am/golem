import { DatamodelDocument } from './datamodel';
import { bufferEvent } from './event-buffer';
import { GolemEventBus, GolemEventType, eventTopic } from './events';

export interface GolemQueryParams {
  model?: string;
  operation: string;
  args: any;
  query: (args: any) => Promise<any>;
  findExisting?: (where: unknown, select: Record<string, true>) => Promise<unknown>;
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
}

export function createEventPublisher(options: CreateEventPublisherOptions): GolemQueryInterceptor {
  const pkByModel = new Map<string, string>();
  for (const model of options.datamodel.models) {
    const pk = model.fields.find((f) => f.isId);
    if (pk) {
      pkByModel.set(model.name, pk.name);
    }
  }

  return async ({ model, operation, args, query, findExisting }) => {
    let type = OPERATION_EVENTS[operation];
    if (operation === 'upsert') {
      if (!model || !options.models.has(model)) return query(args);
      const pk = pkByModel.get(model);
      if (!pk || !findExisting) return query(args);
      const existing = await findExisting(args?.where, { [pk]: true });
      type = existing ? 'UPDATED' : 'CREATED';
    }
    if (!type || !model || !options.models.has(model)) {
      return query(args);
    }
    const pk = pkByModel.get(model);
    if (!pk) {
      return query(args);
    }
    const omittedPk = args?.omit?.[pk] === true;
    const finalArgs = args?.select
      ? { ...args, select: { ...args.select, [pk]: true } }
      : omittedPk
        ? { ...args, omit: { ...args.omit, [pk]: false } }
        : args;
    const result = await query(finalArgs);
    const eventEntity =
      result && typeof result === 'object' && !Array.isArray(result)
        ? { ...(result as Record<string, unknown>) }
        : undefined;
    const payload = {
      type,
      model,
      id: (result as Record<string, string | number>)[pk],
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
    if (omittedPk && eventEntity) {
      const publicResult = { ...eventEntity };
      delete publicResult[pk];
      return publicResult;
    }
    return result;
  };
}
