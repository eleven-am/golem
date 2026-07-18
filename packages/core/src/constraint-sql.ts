import type {
  DatamodelField,
  DatamodelModel,
  GolemDialect,
} from './datamodel';

export class UnsupportedConstraintError extends Error {
  constructor(reason: string) {
    super(`Cannot compile the read constraint to SQL: ${reason}`);
    this.name = 'UnsupportedConstraintError';
  }
}

export interface JoinClause {
  table: string;
  alias: string;
  on: string;
}

export interface CompiledConstraint {
  sql: string;
  params: unknown[];
  joins: JoinClause[];
  alias: string;
}

export interface RelationHop {
  field: DatamodelField;
  relatedModel: DatamodelModel;
  fromColumns: string[];
  toColumns: string[];
}

export function resolveToOneRelation(
  model: DatamodelModel,
  fieldName: string,
  modelsByName: ReadonlyMap<string, DatamodelModel>,
): RelationHop {
  const field = model.fields.find((candidate) => candidate.name === fieldName);
  if (!field || field.kind !== 'object') {
    throw new UnsupportedConstraintError(
      `${model.name}.${fieldName} is not a relation`,
    );
  }
  if (field.isList) {
    throw new UnsupportedConstraintError(
      `${model.name}.${fieldName} is a to-many relation; hopping it would multiply rows and change every measure`,
    );
  }
  const fromFields = field.relationFromFields ?? [];
  const toFields = field.relationToFields ?? [];
  if (fromFields.length === 0 || fromFields.length !== toFields.length) {
    throw new UnsupportedConstraintError(
      `${model.name}.${fieldName} does not hold the foreign key; hop from the side that owns it`,
    );
  }
  const relatedModel = modelsByName.get(field.type);
  if (!relatedModel) {
    throw new UnsupportedConstraintError(`unknown related model ${field.type}`);
  }
  requirePhysicalNames(relatedModel);
  const columnOf = (
    owner: DatamodelModel,
    name: string,
  ): string => {
    const target = owner.fields.find((candidate) => candidate.name === name);
    if (!target || target.dbName === undefined) {
      throw new UnsupportedConstraintError(
        `datamodel for ${owner.name}.${name} predates physical name capture; regenerate the Golem client`,
      );
    }
    return physicalColumn(target);
  };
  return {
    field,
    relatedModel,
    fromColumns: fromFields.map((name) => columnOf(model, name)),
    toColumns: toFields.map((name) => columnOf(relatedModel, name)),
  };
}

const EQUALITY_KEYS = new Set(['equals', 'in']);

export function quoteIdentifier(name: string, dialect: GolemDialect): string {
  if (dialect === 'mysql') {
    return `\`${name.replace(/`/g, '``')}\``;
  }
  return `"${name.replace(/"/g, '""')}"`;
}

export function physicalTable(model: DatamodelModel): string {
  return model.dbName ?? model.name;
}

export function physicalColumn(field: DatamodelField): string {
  return field.dbName ?? field.name;
}

export function requirePhysicalNames(model: DatamodelModel): void {
  if (model.dbName === undefined) {
    throw new UnsupportedConstraintError(
      `datamodel for ${model.name} predates physical name capture; regenerate the Golem client`,
    );
  }
}

function scalarFieldOrThrow(
  model: DatamodelModel,
  name: string,
): DatamodelField {
  const field = model.fields.find((candidate) => candidate.name === name);
  if (!field) {
    throw new UnsupportedConstraintError(`unknown field ${model.name}.${name}`);
  }
  if (field.kind === 'object' || field.isList) {
    throw new UnsupportedConstraintError(
      `${model.name}.${name} is a relation; only own-model scalars are supported`,
    );
  }
  if (field.dbName === undefined) {
    throw new UnsupportedConstraintError(
      `datamodel for ${model.name}.${name} predates physical name capture; regenerate the Golem client`,
    );
  }
  return field;
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value);
}

function isScalarValue(value: unknown): boolean {
  return (
    value === null ||
    typeof value === 'string' ||
    typeof value === 'number' ||
    typeof value === 'boolean' ||
    typeof value === 'bigint' ||
    value instanceof Date
  );
}

function compilePredicate(
  column: string,
  operand: unknown,
  params: unknown[],
): string {
  if (isScalarValue(operand)) {
    if (operand === null) {
      return `${column} IS NULL`;
    }
    params.push(operand);
    return `${column} = ?`;
  }
  if (!isPlainObject(operand)) {
    throw new UnsupportedConstraintError('unsupported filter value');
  }
  const keys = Object.keys(operand);
  if (keys.length !== 1 || !EQUALITY_KEYS.has(keys[0])) {
    throw new UnsupportedConstraintError(
      `only equals and in are supported, received ${keys.join(', ') || 'an empty filter'}`,
    );
  }
  const [operator] = keys;
  const value = operand[operator];
  if (operator === 'equals') {
    if (!isScalarValue(value)) {
      throw new UnsupportedConstraintError('equals requires a scalar value');
    }
    if (value === null) {
      return `${column} IS NULL`;
    }
    params.push(value);
    return `${column} = ?`;
  }
  if (!Array.isArray(value) || !value.every(isScalarValue)) {
    throw new UnsupportedConstraintError('in requires a list of scalar values');
  }
  if (value.length === 0) {
    return '1 = 0';
  }
  for (const entry of value) {
    params.push(entry);
  }
  return `${column} IN (${value.map(() => '?').join(', ')})`;
}

export interface CompileConstraintOptions {
  model: DatamodelModel;
  dialect: GolemDialect;
  modelsByName?: ReadonlyMap<string, DatamodelModel>;
  baseAlias?: string;
}

interface CompileState {
  dialect: GolemDialect;
  modelsByName: ReadonlyMap<string, DatamodelModel>;
  params: unknown[];
  joins: JoinClause[];
  nextAlias: number;
}

export function compileConstraint(
  constraint: unknown,
  options: CompileConstraintOptions,
): CompiledConstraint {
  const alias = options.baseAlias ?? 't0';
  const state: CompileState = {
    dialect: options.dialect,
    modelsByName:
      options.modelsByName ?? new Map([[options.model.name, options.model]]),
    params: [],
    joins: [],
    nextAlias: 1,
  };
  const sql = compileNode(constraint, options.model, alias, state);
  return { sql, params: state.params, joins: state.joins, alias };
}

function aliasRef(alias: string, dialect: GolemDialect): string {
  return quoteIdentifier(alias, dialect);
}

function compileNode(
  constraint: unknown,
  model: DatamodelModel,
  alias: string,
  state: CompileState,
): string {
  if (constraint === undefined || constraint === null) {
    return '1 = 1';
  }
  if (!isPlainObject(constraint)) {
    throw new UnsupportedConstraintError('constraint must be an object');
  }
  requirePhysicalNames(model);
  const clauses: string[] = [];
  for (const [key, value] of Object.entries(constraint)) {
    if (key === 'AND') {
      const nodes = Array.isArray(value) ? value : [value];
      for (const node of nodes) {
        clauses.push(compileNode(node, model, alias, state));
      }
      continue;
    }
    if (key === 'OR' || key === 'NOT') {
      throw new UnsupportedConstraintError(
        `${key} is not supported; the aggregation is refused rather than approximated`,
      );
    }
    const relation = model.fields.find(
      (candidate) => candidate.name === key && candidate.kind === 'object',
    );
    if (relation) {
      clauses.push(compileRelationNode(model, alias, key, value, state));
      continue;
    }
    const field = scalarFieldOrThrow(model, key);
    const column = `${aliasRef(alias, state.dialect)}.${quoteIdentifier(physicalColumn(field), state.dialect)}`;
    clauses.push(compilePredicate(column, value, state.params));
  }
  if (clauses.length === 0) {
    return '1 = 1';
  }
  return clauses.length === 1 ? clauses[0] : `(${clauses.join(' AND ')})`;
}

function compileRelationNode(
  model: DatamodelModel,
  alias: string,
  fieldName: string,
  value: unknown,
  state: CompileState,
): string {
  if (!isPlainObject(value)) {
    throw new UnsupportedConstraintError(
      `${model.name}.${fieldName} requires an is filter`,
    );
  }
  const keys = Object.keys(value);
  if (keys.length !== 1 || keys[0] !== 'is') {
    throw new UnsupportedConstraintError(
      `only an is filter is supported on ${model.name}.${fieldName}, received ${keys.join(', ') || 'an empty filter'}`,
    );
  }
  const hop = resolveToOneRelation(model, fieldName, state.modelsByName);
  const joined = joinFor(hop, alias, state);
  return compileNode(value.is, hop.relatedModel, joined, state);
}

function joinFor(
  hop: RelationHop,
  parentAlias: string,
  state: CompileState,
): string {
  const alias = `t${state.nextAlias}`;
  state.nextAlias += 1;
  const parent = aliasRef(parentAlias, state.dialect);
  const child = aliasRef(alias, state.dialect);
  const on = hop.fromColumns
    .map(
      (from, index) =>
        `${parent}.${quoteIdentifier(from, state.dialect)} = ${child}.${quoteIdentifier(hop.toColumns[index], state.dialect)}`,
    )
    .join(' AND ');
  state.joins.push({
    table: quoteIdentifier(physicalTable(hop.relatedModel), state.dialect),
    alias: child,
    on,
  });
  return alias;
}
