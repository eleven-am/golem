import { PolicyDatamodel, PolicyDatamodelField, PolicyDatamodelModel } from './datamodel';
import { compileJsonFilter, isJsonField, renderJsonFilter, validateJsonFilter } from './json';
import {
  ConditionIssue,
  ConditionIssueReason,
  ConditionValidationResult,
  SqlRenderError,
  UnsupportedConditionError,
} from './errors';
import {
  COMBINATORS,
  OPERATORS,
  OperatorHooks,
  OperatorName,
  SCALAR_LIST_OPERATORS,
  SUPPORTED_OPERATORS,
  SUPPORTED_SCALAR_LIST_OPERATORS,
  ScalarListOperatorName,
  compileRelationFilter,
  isCombinatorName,
  isListRelationOperatorName,
  isOperatorName,
  isRelationOperatorName,
  isScalarListOnlyOperatorName,
  isScalarListOperatorName,
  isStringOperatorName,
  renderRelationFilter,
  scalarFilterEntries,
  validateRelationFilter,
  validateStringMode,
} from './operators';
import { SQL_TRUE, SqlNode, SqlScope, sqlGroup, sqlJoin } from './sql';
import { ObjectPredicate, PolicyConditions } from './types';
import { describeValue, isPlainObject } from './values';

export interface ConditionContext {
  readonly datamodel?: PolicyDatamodel;
  readonly model?: string;
}

export interface CompiledCondition {
  readonly conditions: PolicyConditions;
  test(object: unknown): boolean;
  toSql(scope: SqlScope): SqlNode;
}

const NO_CONTEXT: ConditionContext = {};

export const RESERVED_PRISMA_FILTER_KEYS: readonly string[] = [
  'equals',
  'not',
  'in',
  'notIn',
  'lt',
  'lte',
  'gt',
  'gte',
  'contains',
  'startsWith',
  'endsWith',
  'search',
  'mode',
  'is',
  'isNot',
  'some',
  'every',
  'none',
  'has',
  'hasEvery',
  'hasSome',
  'isEmpty',
  'isSet',
  'path',
  'string_contains',
  'string_starts_with',
  'string_ends_with',
  'array_contains',
  'array_starts_with',
  'array_ends_with',
];

const RESERVED_FILTER_KEYS: ReadonlySet<string> = new Set(RESERVED_PRISMA_FILTER_KEYS);

type FieldPosition = 'unknown' | 'scalar' | 'relation' | 'missing' | 'to-many' | 'scalar-list';

type FieldForm =
  | 'scalar'
  | 'scalar-list'
  | 'json'
  | 'relation-filter'
  | 'relation-shorthand'
  | 'relation-null'
  | 'list-relation-filter'
  | 'invalid';

interface PlanIssue {
  readonly reason: ConditionIssueReason;
  readonly operator: string | null;
  readonly message: string;
}

interface FieldPlan {
  readonly form: FieldForm;
  readonly issue?: PlanIssue;
}

function findModel(datamodel: PolicyDatamodel, name: string): PolicyDatamodelModel | undefined {
  return datamodel.models.find((candidate) => candidate.name === name);
}

function contextModel(context: ConditionContext): PolicyDatamodelModel | undefined {
  if (context.datamodel === undefined || context.model === undefined) {
    return undefined;
  }
  return findModel(context.datamodel, context.model);
}

function contextField(context: ConditionContext, field: string): PolicyDatamodelField | undefined {
  return contextModel(context)?.fields.find((candidate) => candidate.name === field);
}

function positionOf(context: ConditionContext, field: string): FieldPosition {
  const model = contextModel(context);
  if (model === undefined) {
    return 'unknown';
  }
  const definition = model.fields.find((candidate) => candidate.name === field);
  if (definition === undefined) {
    return 'missing';
  }
  if (definition.kind === 'object') {
    return definition.isList ? 'to-many' : 'relation';
  }
  return definition.isList ? 'scalar-list' : 'scalar';
}

function relationContext(context: ConditionContext, field: string): ConditionContext {
  const definition = contextField(context, field);
  if (definition === undefined || definition.kind !== 'object') {
    return NO_CONTEXT;
  }
  return { datamodel: context.datamodel, model: definition.type };
}

function qualify(context: ConditionContext, field: string): string {
  return context.model === undefined ? `"${field}"` : `"${context.model}.${field}"`;
}

function shapeIssue(message: string): FieldPlan {
  return { form: 'invalid', issue: { reason: 'unsupported-shape', operator: null, message } };
}

function operatorIssue(operator: string, message: string): FieldPlan {
  return { form: 'invalid', issue: { reason: 'unsupported-operator', operator, message } };
}

function strayKeyPlan(
  context: ConditionContext,
  field: string,
  stray: readonly string[],
): FieldPlan | undefined {
  if (stray.length === 0) {
    return undefined;
  }
  return shapeIssue(
    `the filter on ${qualify(context, field)} mixes the to-many operators some, every and none with the keys ${stray.join(', ')}; a to-many relation filter carries only some, every and none, never a condition object on the related model`,
  );
}

function describePosition(position: FieldPosition): string {
  if (position === 'to-many') {
    return 'a to-many relation';
  }
  if (position === 'relation') {
    return 'a to-one relation';
  }
  return 'not a list column';
}

function scalarListPlan(context: ConditionContext, field: string, filter: unknown): FieldPlan {
  if (!isPlainObject(filter)) {
    return shapeIssue(
      `list column ${qualify(context, field)} cannot be compared to ${describeValue(filter)}; use { has: … }, { hasEvery: … }, { hasSome: … }, { isEmpty: … } or { equals: … }`,
    );
  }
  const keys = Object.keys(filter);
  const foreign = keys.filter((key) => !isScalarListOperatorName(key));
  if (foreign.length > 0) {
    return operatorIssue(
      foreign[0]!,
      `operator "${foreign[0]!}" does not filter a list column, and ${qualify(context, field)} is a list column; a scalar list carries ${SUPPORTED_SCALAR_LIST_OPERATORS.join(', ')}`,
    );
  }
  if (keys.length !== 1) {
    return shapeIssue(
      `the filter on list column ${qualify(context, field)} carries ${keys.length === 0 ? 'no operator' : keys.join(', ')}; a scalar list filter carries exactly one of ${SUPPORTED_SCALAR_LIST_OPERATORS.join(', ')}, and Prisma refuses both an empty filter and two operators at once`,
    );
  }
  return { form: 'scalar-list' };
}

function classifyFieldFilter(context: ConditionContext, field: string, filter: unknown): FieldPlan {
  const position = positionOf(context, field);
  if (position === 'missing') {
    return shapeIssue(`field ${qualify(context, field)} is not present in the datamodel`);
  }
  if (position === 'scalar-list') {
    return scalarListPlan(context, field, filter);
  }
  if (position === 'scalar' && isJsonField(contextField(context, field)?.type ?? '')) {
    return { form: 'json' };
  }
  if (!isPlainObject(filter)) {
    if (position === 'to-many') {
      return shapeIssue(
        `to-many relation ${qualify(context, field)} cannot be compared to ${describeValue(filter)}; use { some: … }, { every: … } or { none: … }`,
      );
    }
    if (position !== 'relation') {
      return { form: 'scalar' };
    }
    if (filter === null || filter === undefined) {
      return { form: 'relation-null' };
    }
    return shapeIssue(
      `relation ${qualify(context, field)} cannot be compared to ${describeValue(filter)}; use a condition object, { is: … }, { isNot: … } or null`,
    );
  }
  const keys = Object.keys(filter);
  const relational = keys.filter(isRelationOperatorName);
  const listed = keys.filter(isListRelationOperatorName);
  const columnListed = keys.filter(isScalarListOnlyOperatorName);
  if (columnListed.length > 0 && position !== 'unknown') {
    return operatorIssue(
      columnListed[0]!,
      `operator "${columnListed[0]!}" filters a scalar list column, and ${qualify(context, field)} is ${describePosition(position)}`,
    );
  }
  if (
    position === 'unknown' &&
    (columnListed.length > 0 || (keys.length === 1 && keys[0] === 'equals' && Array.isArray(filter.equals)))
  ) {
    return scalarListPlan(context, field, filter);
  }
  if (position === 'to-many') {
    if (relational.length > 0) {
      return operatorIssue(
        relational[0]!,
        `operator "${relational[0]!}" filters a to-one relation, and ${qualify(context, field)} is a to-many relation; quantify it with some, every or none`,
      );
    }
    return (
      strayKeyPlan(
        context,
        field,
        keys.filter((key) => !isListRelationOperatorName(key)),
      ) ?? { form: 'list-relation-filter' }
    );
  }
  if (listed.length > 0) {
    if (relational.length > 0) {
      return shapeIssue(
        `the filter on ${qualify(context, field)} mixes the to-one operators is and isNot with the to-many operators ${listed.join(', ')}; a relation is either to-one or to-many, never both`,
      );
    }
    const stray = strayKeyPlan(
      context,
      field,
      keys.filter((key) => !isListRelationOperatorName(key)),
    );
    if (stray !== undefined) {
      return stray;
    }
    if (position === 'scalar') {
      return operatorIssue(
        listed[0]!,
        `operator "${listed[0]!}" filters a to-many relation, and ${qualify(context, field)} is a scalar field`,
      );
    }
    if (position === 'relation') {
      return operatorIssue(
        listed[0]!,
        `operator "${listed[0]!}" filters a to-many relation, and ${qualify(context, field)} is a to-one relation; filter it with is, isNot or a condition object`,
      );
    }
    return { form: 'list-relation-filter' };
  }
  if (relational.length > 0) {
    if (relational.length !== keys.length) {
      return shapeIssue(
        `the filter on ${qualify(context, field)} mixes the relation operators is and isNot with the keys ${keys.filter((key) => !isRelationOperatorName(key)).join(', ')}; a to-one relation filter is either { is, isNot } or a condition object on the related model, never both`,
      );
    }
    if (position === 'scalar') {
      return operatorIssue(
        keys[0]!,
        `operator "${keys[0]!}" filters a to-one relation, and ${qualify(context, field)} is a scalar field`,
      );
    }
    return { form: 'relation-filter' };
  }
  if (position === 'relation') {
    return { form: 'relation-shorthand' };
  }
  if (keys.every((key) => RESERVED_FILTER_KEYS.has(key))) {
    return { form: 'scalar' };
  }
  return { form: position === 'scalar' ? 'scalar' : 'relation-shorthand' };
}

function hooksFor(context: ConditionContext): OperatorHooks {
  return {
    validateConditions: (conditions, path, issues) =>
      validateConditionsObject(context, conditions, path, issues),
    compileConditions: (conditions) => compileConditionsObject(context, conditions),
    renderConditions: (conditions, scope, path) => renderConditionsObject(context, conditions, scope, path),
  };
}

function validateConditionsObject(
  context: ConditionContext,
  conditions: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
): void {
  if (!isPlainObject(conditions)) {
    issues.push({
      reason: 'unsupported-shape',
      operator: null,
      path,
      message: `conditions must be a plain object, received ${describeValue(conditions)}`,
    });
    return;
  }
  for (const [key, value] of Object.entries(conditions)) {
    if (isCombinatorName(key)) {
      COMBINATORS[key].validate(value, path, issues, hooksFor(context));
      continue;
    }
    validateFieldFilter(context, key, value, [...path, key], issues);
  }
}

function textOperatorIssue(
  context: ConditionContext,
  field: string,
  operator: string,
): PlanIssue | undefined {
  if (!isStringOperatorName(operator)) {
    return undefined;
  }
  const definition = contextField(context, field);
  if (definition === undefined) {
    return undefined;
  }
  if (definition.kind === 'enum' || (definition.kind === 'scalar' && definition.type === 'String')) {
    return undefined;
  }
  return {
    reason: 'unsupported-operator',
    operator,
    message: `operator "${operator}" matches text, and ${qualify(context, field)} is a ${definition.type} column; Prisma generates contains, startsWith and endsWith on a String field only, and SQLite and MySQL would answer this by coercing the column to text where the evaluator matches nothing at all`,
  };
}

function validateScalarFilter(
  context: ConditionContext,
  field: string,
  filter: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
  hooks: OperatorHooks,
): void {
  if (!isPlainObject(filter)) {
    OPERATORS.equals.validate(filter, path, issues, hooks);
    return;
  }
  validateStringMode(filter, path, issues);
  for (const [key, operand] of scalarFilterEntries(filter)) {
    if (isOperatorName(key) && !isRelationOperatorName(key) && !isListRelationOperatorName(key)) {
      const refusal = textOperatorIssue(context, field, key);
      if (refusal !== undefined) {
        issues.push({ ...refusal, path });
        continue;
      }
      OPERATORS[key].validate(operand, path, issues, hooks);
      continue;
    }
    issues.push({
      reason: 'unsupported-operator',
      operator: key,
      path,
      message: `operator "${key}" is not supported; supported operators are ${SUPPORTED_OPERATORS.join(', ')}`,
    });
  }
}

function validateFieldFilter(
  context: ConditionContext,
  field: string,
  filter: unknown,
  path: readonly string[],
  issues: ConditionIssue[],
): void {
  const plan = classifyFieldFilter(context, field, filter);
  if (plan.issue !== undefined) {
    issues.push({ ...plan.issue, path });
  }
  const hooks = hooksFor(relationContext(context, field));
  switch (plan.form) {
    case 'invalid':
    case 'relation-null':
      return;
    case 'relation-shorthand':
      validateRelationFilter(field, filter, path, issues, hooks);
      return;
    case 'scalar-list':
      for (const [key, operand] of Object.entries(filter as Record<string, unknown>)) {
        SCALAR_LIST_OPERATORS[key as ScalarListOperatorName].validate(operand, path, issues, hooks);
      }
      return;
    case 'json':
      validateJsonFilter(field, filter, path, issues);
      return;
    case 'relation-filter':
    case 'list-relation-filter':
      for (const [key, operand] of Object.entries(filter as Record<string, unknown>)) {
        OPERATORS[key as OperatorName].validate(operand, path, issues, hooks);
      }
      return;
    default:
      validateScalarFilter(context, field, filter, path, issues, hooks);
  }
}

function readField(object: unknown, field: string): unknown {
  if (object === null || object === undefined || typeof object !== 'object') {
    return undefined;
  }
  return (object as Record<string, unknown>)[field];
}

function onField(field: string, predicate: (value: unknown) => boolean): ObjectPredicate {
  return (object) => predicate(readField(object, field));
}

function compileFieldFilter(context: ConditionContext, field: string, filter: unknown): ObjectPredicate {
  const plan = classifyFieldFilter(context, field, filter);
  const hooks = hooksFor(relationContext(context, field));
  switch (plan.form) {
    case 'invalid':
      return () => false;
    case 'relation-null':
      return onField(field, compileRelationFilter(null, hooks));
    case 'relation-shorthand':
      return onField(field, compileRelationFilter(filter, hooks));
    case 'scalar-list': {
      const predicates = Object.entries(filter as Record<string, unknown>).map(([key, operand]) =>
        SCALAR_LIST_OPERATORS[key as ScalarListOperatorName].compile(operand, hooks),
      );
      return onField(field, (value) => predicates.every((predicate) => predicate(value)));
    }
    case 'json':
      return onField(field, compileJsonFilter(filter));
    case 'relation-filter':
    case 'list-relation-filter': {
      const predicates = Object.entries(filter as Record<string, unknown>).map(([key, operand]) =>
        OPERATORS[key as OperatorName].compile(operand, hooks),
      );
      return onField(field, (value) => predicates.every((predicate) => predicate(value)));
    }
    default: {
      if (!isPlainObject(filter)) {
        return onField(field, OPERATORS.equals.compile(filter, hooks));
      }
      const predicates = scalarFilterEntries(filter).map(([key, operand]) =>
        OPERATORS[key as OperatorName].compile(operand, hooks),
      );
      return onField(field, (value) => predicates.every((predicate) => predicate(value)));
    }
  }
}

function compileConditionsObject(context: ConditionContext, conditions: unknown): ObjectPredicate {
  if (!isPlainObject(conditions)) {
    return () => false;
  }
  const predicates: ObjectPredicate[] = [];
  for (const [key, value] of Object.entries(conditions)) {
    if (isCombinatorName(key)) {
      predicates.push(COMBINATORS[key].compile(value, hooksFor(context)));
      continue;
    }
    predicates.push(compileFieldFilter(context, key, value));
  }
  if (predicates.length === 0) {
    return () => true;
  }
  if (predicates.length === 1) {
    return predicates[0]!;
  }
  return (object) => predicates.every((predicate) => predicate(object));
}

function conjunction(nodes: readonly SqlNode[]): SqlNode {
  if (nodes.length === 0) {
    return SQL_TRUE;
  }
  return nodes.length === 1 ? nodes[0]! : sqlGroup(sqlJoin(nodes, ' AND '));
}

function renderFieldFilter(
  context: ConditionContext,
  field: string,
  filter: unknown,
  scope: SqlScope,
  path: readonly string[],
): SqlNode {
  const plan = classifyFieldFilter(context, field, filter);
  const hooks = hooksFor(relationContext(context, field));
  const target = { field, scope, path };
  switch (plan.form) {
    case 'invalid':
      throw new SqlRenderError(plan.issue?.message ?? `the filter on "${field}" is not supported`, path);
    case 'relation-null':
      return renderRelationFilter(null, target, path, hooks);
    case 'relation-shorthand':
      return renderRelationFilter(filter, target, path, hooks);
    case 'scalar-list':
      return conjunction(
        Object.entries(filter as Record<string, unknown>).map(([key, operand]) =>
          SCALAR_LIST_OPERATORS[key as ScalarListOperatorName].renderSql(operand, target, hooks),
        ),
      );
    case 'json':
      return renderJsonFilter(filter, target);
    case 'relation-filter':
    case 'list-relation-filter':
      return conjunction(
        Object.entries(filter as Record<string, unknown>).map(([key, operand]) =>
          OPERATORS[key as OperatorName].renderSql(operand, target, hooks),
        ),
      );
    default: {
      if (!isPlainObject(filter)) {
        return OPERATORS.equals.renderSql(filter, target, hooks);
      }
      return conjunction(
        scalarFilterEntries(filter).map(([key, operand]) =>
          OPERATORS[key as OperatorName].renderSql(operand, target, hooks),
        ),
      );
    }
  }
}

function renderConditionsObject(
  context: ConditionContext,
  conditions: unknown,
  scope: SqlScope,
  path: readonly string[],
): SqlNode {
  if (!isPlainObject(conditions)) {
    return SQL_TRUE;
  }
  const nodes: SqlNode[] = [];
  for (const [key, value] of Object.entries(conditions)) {
    if (isCombinatorName(key)) {
      nodes.push(COMBINATORS[key].renderSql(value, scope, path, hooksFor(context)));
      continue;
    }
    nodes.push(renderFieldFilter(context, key, value, scope, [...path, key]));
  }
  return conjunction(nodes);
}

function rootIssues(context: ConditionContext): readonly ConditionIssue[] {
  if (context.datamodel === undefined && context.model === undefined) {
    return [];
  }
  if (context.datamodel === undefined || context.model === undefined) {
    return [
      {
        reason: 'unsupported-shape',
        operator: null,
        path: [],
        message:
          'a condition context needs both a datamodel and a model name; supplying one without the other would silently disable schema-aware disambiguation',
      },
    ];
  }
  if (findModel(context.datamodel, context.model) === undefined) {
    return [
      {
        reason: 'unsupported-shape',
        operator: null,
        path: [],
        message: `model "${context.model}" is not present in the datamodel`,
      },
    ];
  }
  return [];
}

export function validateConditions(
  conditions: unknown,
  context: ConditionContext = NO_CONTEXT,
): ConditionValidationResult {
  const issues: ConditionIssue[] = [...rootIssues(context)];
  if (issues.length === 0) {
    validateConditionsObject(context, conditions, [], issues);
  }
  if (issues.length === 0) {
    return { supported: true, issues: [] };
  }
  return { supported: false, issues: issues as [ConditionIssue, ...ConditionIssue[]] };
}

export function assertSupportedConditions(
  conditions: unknown,
  context: ConditionContext = NO_CONTEXT,
): asserts conditions is PolicyConditions {
  const result = validateConditions(conditions, context);
  if (!result.supported) {
    throw new UnsupportedConditionError(result.issues, conditions);
  }
}

export function compileConditions(
  conditions: unknown,
  context: ConditionContext = NO_CONTEXT,
): CompiledCondition {
  assertSupportedConditions(conditions, context);
  const predicate = compileConditionsObject(context, conditions);
  return {
    conditions,
    test: (object) => predicate(object),
    toSql: (scope) => renderConditionsObject(context, conditions, scope, []),
  };
}

export function evaluateConditions(
  conditions: unknown,
  object: unknown,
  context: ConditionContext = NO_CONTEXT,
): boolean {
  return compileConditions(conditions, context).test(object);
}
