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

export interface CompiledConstraint {
  sql: string;
  params: unknown[];
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
  alias?: string;
}

export function compileConstraint(
  constraint: unknown,
  options: CompileConstraintOptions,
): CompiledConstraint {
  const params: unknown[] = [];
  const sql = compileNode(constraint, options, params);
  return { sql, params };
}

function compileNode(
  constraint: unknown,
  options: CompileConstraintOptions,
  params: unknown[],
): string {
  if (constraint === undefined || constraint === null) {
    return '1 = 1';
  }
  if (!isPlainObject(constraint)) {
    throw new UnsupportedConstraintError('constraint must be an object');
  }
  requirePhysicalNames(options.model);
  const clauses: string[] = [];
  for (const [key, value] of Object.entries(constraint)) {
    if (key === 'AND') {
      const nodes = Array.isArray(value) ? value : [value];
      for (const node of nodes) {
        clauses.push(compileNode(node, options, params));
      }
      continue;
    }
    if (key === 'OR' || key === 'NOT') {
      throw new UnsupportedConstraintError(
        `${key} is not supported; the aggregation is refused rather than approximated`,
      );
    }
    const field = scalarFieldOrThrow(options.model, key);
    const table = options.alias ?? quoteIdentifier(
      physicalTable(options.model),
      options.dialect,
    );
    const column = `${table}.${quoteIdentifier(physicalColumn(field), options.dialect)}`;
    clauses.push(compilePredicate(column, value, params));
  }
  if (clauses.length === 0) {
    return '1 = 1';
  }
  return clauses.length === 1 ? clauses[0] : `(${clauses.join(' AND ')})`;
}
