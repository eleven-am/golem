export type GolemAction = 'read' | 'create' | 'update' | 'delete';

export interface AuthorizationProvider {
  authorize(action: GolemAction, model: string, context: unknown): Promise<void>;
  constrain(action: GolemAction, model: string, context: unknown): Promise<unknown>;
  constrainField?(
    action: GolemAction,
    model: string,
    field: string,
    context: unknown,
  ): Promise<unknown>;
  check?(action: GolemAction, model: string, entity: unknown, context: unknown): Promise<boolean>;
  checkField?(
    action: GolemAction,
    model: string,
    entity: unknown,
    field: string,
    context: unknown,
  ): Promise<boolean>;
  classifyFields?(
    action: GolemAction,
    model: string,
    fields: readonly string[],
    context: unknown,
  ): Promise<Record<string, FieldClassification>>;
  freshContext?(context: unknown): unknown;
}

export interface FieldClassification {
  access: 'always' | 'conditional' | 'never';
  requires?: readonly string[];
  dependencies?: FieldDependencyTree;
  dischargedByConstraint?: boolean;
}

export type FieldDependencyTree = Readonly<{
  [field: string]: true | FieldDependencyTree;
}>;

export function isConditionalConstraint(constraint: unknown): boolean {
  return (
    constraint !== undefined &&
    constraint !== null &&
    typeof constraint === 'object' &&
    Object.keys(constraint).length > 0
  );
}

export const NESTED_WRITE_ACTIONS: Record<string, GolemAction> = {
  create: 'create',
  createMany: 'create',
  connectOrCreate: 'create',
  connect: 'read',
  disconnect: 'read',
  set: 'read',
  update: 'update',
  updateMany: 'update',
  upsert: 'update',
  delete: 'delete',
  deleteMany: 'delete',
};

export function mergeConstraint(where: unknown, constraint: unknown): unknown {
  if (constraint === undefined) {
    return where;
  }
  if (where === undefined || where === null) {
    return constraint;
  }
  return { AND: [where, constraint] };
}

export function constrainUnique(where: unknown, constraint: unknown): unknown {
  if (constraint === undefined) {
    return where;
  }
  const unique = (where ?? {}) as Record<string, unknown>;
  const existing = unique.AND === undefined ? [] : Array.isArray(unique.AND) ? unique.AND : [unique.AND];
  return { ...unique, AND: [...existing, constraint] };
}
