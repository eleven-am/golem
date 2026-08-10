export type PolicyObject = Record<string, unknown>;

export type PolicyConditions = Record<string, unknown>;

export type ObjectPredicate = (object: unknown) => boolean;

export type FieldPredicate = (value: unknown) => boolean;
