import { ConditionContext, compileConditions } from './compile';
import { PolicyDatamodel, PolicyDatamodelField, PolicyDatamodelModel } from './datamodel';
import { SqlRenderError } from './errors';
import {
  SQL_FALSE,
  SQL_TRUE,
  SqlDialect,
  SqlNode,
  SqlParameter,
  SqlRelationHop,
  SqlScope,
  renderSql,
  sqlBinaryCollate,
  sqlCastToText,
  sqlGroup,
  sqlIdentifier,
  sqlJoin,
  sqlSequence,
  sqlText,
} from './sql';

export interface DatamodelSqlScope extends SqlScope {
  readonly datamodel: PolicyDatamodel;
  readonly model: string;
  readonly table: string;
  readonly alias: string;
}

export interface DatamodelSqlScopeOptions {
  readonly datamodel: PolicyDatamodel;
  readonly model: string;
  readonly alias?: string;
}

export type AbsentConstraintMeaning = 'grant-all' | 'deny-all';

export interface ConstraintNodeOptions {
  readonly scope: DatamodelSqlScope;
  readonly absent: AbsentConstraintMeaning;
}

export interface ConstraintSqlOptions extends DatamodelSqlScopeOptions {
  readonly dialect: SqlDialect;
  readonly absent: AbsentConstraintMeaning;
}

export interface ConstraintSql {
  readonly text: string;
  readonly parameters: readonly SqlParameter[];
  readonly table: string;
  readonly alias: string;
  readonly from: string;
}

const DEFAULT_ALIAS = 't0';

const REGENERATE_HINT =
  'regenerate the golem client so the datamodel carries physical names; falling back to the Prisma name would silently target the wrong table when the schema uses @map';

function refuse(message: string): never {
  throw new SqlRenderError(message, []);
}

function resolveModel(datamodel: PolicyDatamodel, name: string): PolicyDatamodelModel {
  const model = datamodel.models.find((candidate) => candidate.name === name);
  if (model === undefined) {
    refuse(`model "${name}" is not present in the datamodel`);
  }
  return model;
}

function resolveField(model: PolicyDatamodelModel, name: string): PolicyDatamodelField {
  const field = model.fields.find((candidate) => candidate.name === name);
  if (field === undefined) {
    refuse(`field "${model.name}.${name}" is not present in the datamodel`);
  }
  return field;
}

function isColumnField(field: PolicyDatamodelField): boolean {
  return field.kind === 'scalar' || field.kind === 'enum';
}

function physicalTable(model: PolicyDatamodelModel): string {
  if (model.dbName == null) {
    refuse(`model "${model.name}" carries no physical table name: ${REGENERATE_HINT}`);
  }
  return model.dbName;
}

function physicalColumn(model: PolicyDatamodelModel, field: PolicyDatamodelField): string {
  if (field.dbName == null) {
    refuse(`field "${model.name}.${field.name}" carries no physical column name: ${REGENERATE_HINT}`);
  }
  return field.dbName;
}

function assertPhysicalNames(model: PolicyDatamodelModel): void {
  physicalTable(model);
  for (const field of model.fields) {
    if (isColumnField(field)) {
      physicalColumn(model, field);
    }
  }
}

function columnReference(model: PolicyDatamodelModel, alias: string, field: PolicyDatamodelField): SqlNode {
  const identifier = sqlIdentifier([alias, physicalColumn(model, field)]);
  if (field.kind === 'enum') {
    return sqlBinaryCollate(sqlCastToText(identifier));
  }
  if (field.kind === 'scalar' && field.type === 'String') {
    return sqlBinaryCollate(identifier);
  }
  return identifier;
}

function hopAlias(rootAlias: string, depth: number): string {
  return `${rootAlias}_${depth}`;
}

function aliasAtDepth(rootAlias: string, depth: number): string {
  return depth === 0 ? rootAlias : hopAlias(rootAlias, depth);
}

function columnNode(model: PolicyDatamodelModel, alias: string, fieldName: string): SqlNode {
  const field = resolveField(model, fieldName);
  if (field.kind === 'object') {
    refuse(
      `field "${model.name}.${fieldName}" is a relation, not a column; filter it with a relation filter instead of a scalar comparison`,
    );
  }
  if (field.isList) {
    refuse(
      `field "${model.name}.${fieldName}" is a list column; the supported operators compare single values and would not agree with the evaluator on a list`,
    );
  }
  return columnReference(model, alias, field);
}

function listColumnNode(model: PolicyDatamodelModel, alias: string, fieldName: string): SqlNode {
  const field = resolveField(model, fieldName);
  if (field.kind === 'object') {
    refuse(
      `field "${model.name}.${fieldName}" is a relation, not a list column; filter it with a relation filter instead of a list filter`,
    );
  }
  if (!field.isList) {
    refuse(
      `field "${model.name}.${fieldName}" is not a list column; has, hasEvery, hasSome, isEmpty and a list equals filter a scalar list, and applying them to a single value would compare a value to an array`,
    );
  }
  return sqlIdentifier([alias, physicalColumn(model, field)]);
}

interface KeyPair {
  readonly local: string;
  readonly remote: string;
}

function holdsForeignKey(field: PolicyDatamodelField): boolean {
  return field.kind === 'object' && (field.relationFromFields?.length ?? 0) > 0;
}

function backReference(
  model: PolicyDatamodelModel,
  field: PolicyDatamodelField,
  related: PolicyDatamodelModel,
): PolicyDatamodelField {
  const candidates = related.fields.filter(
    (candidate) =>
      holdsForeignKey(candidate) &&
      candidate.type === model.name &&
      (field.relationName === undefined ||
        candidate.relationName === undefined ||
        candidate.relationName === field.relationName),
  );
  if (candidates.length === 0) {
    refuse(
      `relation "${model.name}.${field.name}" is to-many and "${related.name}" declares no field holding the foreign key back to "${model.name}"; an implicit many-to-many keeps its keys in a join table golem cannot see, and golem will not guess one`,
    );
  }
  if (candidates.length > 1) {
    refuse(
      `relation "${model.name}.${field.name}" is to-many and "${related.name}" declares ${candidates.length} fields holding a foreign key back to "${model.name}"; regenerate the golem client so the datamodel carries relation names, because choosing between them would silently correlate the wrong relation`,
    );
  }
  return candidates[0]!;
}

function keyPairs(
  model: PolicyDatamodelModel,
  field: PolicyDatamodelField,
  related: PolicyDatamodelModel,
): readonly KeyPair[] {
  if (!field.isList) {
    const fromFields = field.relationFromFields ?? [];
    const toFields = field.relationToFields ?? [];
    if (fromFields.length === 0) {
      refuse(
        `relation "${model.name}.${field.name}" does not hold the foreign key on this side, so it cannot be correlated from "${model.name}"; scope from the side that owns the foreign key`,
      );
    }
    if (fromFields.length !== toFields.length) {
      refuse(
        `relation "${model.name}.${field.name}" declares ${fromFields.length} foreign key column(s) against ${toFields.length} referenced column(s); the relation cannot be correlated`,
      );
    }
    return fromFields.map((local, index) => ({ local, remote: toFields[index]! }));
  }
  const back = backReference(model, field, related);
  const fromFields = back.relationFromFields ?? [];
  const toFields = back.relationToFields ?? [];
  if (fromFields.length !== toFields.length) {
    refuse(
      `relation "${related.name}.${back.name}" declares ${fromFields.length} foreign key column(s) against ${toFields.length} referenced column(s); the relation cannot be correlated`,
    );
  }
  return fromFields.map((remote, index) => ({ local: toFields[index]!, remote }));
}

function relationHop(
  datamodel: PolicyDatamodel,
  model: PolicyDatamodelModel,
  rootAlias: string,
  depth: number,
  fieldName: string,
): SqlRelationHop {
  const field = resolveField(model, fieldName);
  if (field.kind !== 'object') {
    refuse(
      `field "${model.name}.${fieldName}" is a ${field.kind} column, not a relation; a relation filter requires a relation field`,
    );
  }
  const related = resolveModel(datamodel, field.type);
  const alias = aliasAtDepth(rootAlias, depth);
  const childDepth = depth + 1;
  const childAlias = hopAlias(rootAlias, childDepth);
  const childScope = buildScope(datamodel, related, rootAlias, childDepth);
  const correlation = (): readonly SqlNode[] =>
    keyPairs(model, field, related).map((pair) => {
      const localField = resolveField(model, pair.local);
      const remoteField = resolveField(related, pair.remote);
      if (!isColumnField(localField) || !isColumnField(remoteField)) {
        refuse(
          `relation "${model.name}.${fieldName}" is declared over "${localField.name}" and "${remoteField.name}", which are not columns`,
        );
      }
      return sqlSequence([
        columnReference(related, childAlias, remoteField),
        sqlText(' = '),
        columnReference(model, alias, localField),
      ]);
    });
  return {
    scope: childScope,
    toMany: field.isList,
    wrap: (predicate) =>
      sqlSequence([
        sqlText('EXISTS (SELECT 1 FROM '),
        sqlIdentifier([childScope.table]),
        sqlText(' AS '),
        sqlIdentifier([childAlias]),
        sqlText(' WHERE '),
        sqlJoin(correlation(), ' AND '),
        sqlText(' AND '),
        sqlGroup(predicate),
        sqlText(')'),
      ]),
  };
}

function buildScope(
  datamodel: PolicyDatamodel,
  model: PolicyDatamodelModel,
  rootAlias: string,
  depth: number,
): DatamodelSqlScope {
  assertPhysicalNames(model);
  const alias = aliasAtDepth(rootAlias, depth);
  return {
    datamodel,
    model: model.name,
    table: physicalTable(model),
    alias,
    column: (field) => columnNode(model, alias, field),
    listColumn: (field) => listColumnNode(model, alias, field),
    relation: (field) => relationHop(datamodel, model, rootAlias, depth, field),
  };
}

export function createDatamodelSqlScope(options: DatamodelSqlScopeOptions): DatamodelSqlScope {
  const alias = options.alias ?? DEFAULT_ALIAS;
  if (typeof alias !== 'string' || alias.length === 0) {
    refuse('the root alias must be a non-empty string');
  }
  return buildScope(options.datamodel, resolveModel(options.datamodel, options.model), alias, 0);
}

function scopeContext(scope: DatamodelSqlScope): ConditionContext {
  if (scope == null || scope.datamodel == null || typeof scope.model !== 'string') {
    refuse(
      'rendering a constraint requires a scope from createDatamodelSqlScope, which carries the datamodel the constraint is read against: without it golem cannot tell a String column from an Int one, and would render contains, startsWith and endsWith as a text match on a column the evaluator refuses to match at all, granting rows in SQL that are denied in memory',
    );
  }
  return { datamodel: scope.datamodel, model: scope.model };
}

export function renderConstraintNode(constraint: unknown, options: ConstraintNodeOptions): SqlNode {
  if (options.absent !== 'grant-all' && options.absent !== 'deny-all') {
    refuse(
      'rendering a constraint requires "absent" to be "grant-all" or "deny-all": an absent constraint must not be guessed, because guessing an unconditional grant turns a deny-all into an allow-all',
    );
  }
  const context = scopeContext(options.scope);
  if (constraint === null || constraint === undefined) {
    return options.absent === 'grant-all' ? SQL_TRUE : SQL_FALSE;
  }
  return compileConditions(constraint, context).toSql(options.scope);
}

export function renderConstraintSql(constraint: unknown, options: ConstraintSqlOptions): ConstraintSql {
  const scope = createDatamodelSqlScope(options);
  const node = renderConstraintNode(constraint, { scope, absent: options.absent });
  const fragment = renderSql(node, options.dialect);
  return {
    text: fragment.text,
    parameters: fragment.parameters,
    table: scope.table,
    alias: scope.alias,
    from: `${options.dialect.quoteIdentifier(scope.table)} AS ${options.dialect.quoteIdentifier(scope.alias)}`,
  };
}
