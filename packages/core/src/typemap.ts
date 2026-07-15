import { GolemHookOperation } from './hooks';
import { BatchResult } from './operations';
import { PrismaSelect } from './select';

export interface GolemTypeShape {
  entity: unknown;
  create: unknown;
  update: unknown;
  updateMany: unknown;
  where: unknown;
  whereUnique: unknown;
  orderBy: unknown;
}

export type GolemTypesMap = Record<string, GolemTypeShape>;

type ScalarFieldFor<T extends GolemTypeShape> = Extract<keyof T['entity'], string>;

export type HookRequestFor<
  TTypes extends GolemTypesMap,
  M extends keyof TTypes & string,
  O extends GolemHookOperation,
> = O extends 'create'
  ? { model: M; data: TTypes[M]['create']; select?: PrismaSelect; context?: unknown }
  : O extends 'update'
    ? {
        model: M;
        where: TTypes[M]['whereUnique'];
        data: TTypes[M]['update'];
        select?: PrismaSelect;
        context?: unknown;
      }
    : O extends 'updateMany'
      ? { model: M; where?: TTypes[M]['where']; data: TTypes[M]['updateMany']; context?: unknown }
      : O extends 'delete'
        ? { model: M; where: TTypes[M]['whereUnique']; select?: PrismaSelect; context?: unknown }
        : O extends 'deleteMany'
          ? { model: M; where?: TTypes[M]['where']; context?: unknown }
          : O extends 'findOne'
            ? { model: M; where: TTypes[M]['whereUnique']; select?: PrismaSelect; context?: unknown }
            : O extends 'findFirst'
              ? {
                  model: M;
                  where?: TTypes[M]['where'];
                  orderBy?: TTypes[M]['orderBy'] | TTypes[M]['orderBy'][];
                  take?: number;
                  skip?: number;
                  cursor?: TTypes[M]['whereUnique'];
                  distinct?: ScalarFieldFor<TTypes[M]> | ScalarFieldFor<TTypes[M]>[];
                  select?: PrismaSelect;
                  context?: unknown;
                }
            : O extends 'findMany'
              ? {
                  model: M;
                  where?: TTypes[M]['where'];
                  orderBy?: TTypes[M]['orderBy'] | TTypes[M]['orderBy'][];
                  take?: number;
                  skip?: number;
                  cursor?: TTypes[M]['whereUnique'];
                  distinct?: ScalarFieldFor<TTypes[M]> | ScalarFieldFor<TTypes[M]>[];
                  select?: PrismaSelect;
                  context?: unknown;
                }
              : never;

export type HookResultFor<
  TTypes extends GolemTypesMap,
  M extends keyof TTypes & string,
  O extends GolemHookOperation,
> = O extends 'create' | 'update' | 'delete'
  ? Partial<TTypes[M]['entity']>
  : O extends 'findOne' | 'findFirst'
    ? Partial<TTypes[M]['entity']> | null
    : O extends 'findMany'
      ? Partial<TTypes[M]['entity']>[]
      : BatchResult;
