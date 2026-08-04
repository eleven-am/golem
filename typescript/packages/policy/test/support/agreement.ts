import {
  PolicyDatamodel,
  SqlDialect,
  SqlParameter,
  evaluateConditions,
  renderConstraintSql,
} from '../../src/index';
import { idColumnOf } from './agreement-scope';
import { WhereCase } from './matrix';
import { Row, renderIds } from './measure';

export interface RawExecutor {
  $queryRawUnsafe(sql: string, ...parameters: unknown[]): Promise<unknown>;
}

export interface CountingExecutor extends RawExecutor {
  readonly executed: number;
  readonly answered: number;
}

export function counting(db: RawExecutor): CountingExecutor {
  let executed = 0;
  let answered = 0;
  return {
    get executed() {
      return executed;
    },
    get answered() {
      return answered;
    },
    $queryRawUnsafe: async (sql: string, ...parameters: unknown[]) => {
      executed += 1;
      const found = await db.$queryRawUnsafe(sql, ...parameters);
      answered += 1;
      return found;
    },
  };
}

export interface Rendered {
  readonly statement: string;
  readonly parameters: readonly SqlParameter[];
}

export interface Agreement {
  readonly id: string;
  readonly sql: string;
  readonly js: string;
  readonly statement: string;
  readonly parameters: readonly SqlParameter[];
}

function renderError(error: unknown): string {
  return `error:${(error as { name?: string }).name ?? 'Error'}`;
}

export function render(
  datamodel: PolicyDatamodel,
  modelName: string,
  dialect: SqlDialect,
  where: unknown,
): Rendered {
  const constraint = renderConstraintSql(where, {
    datamodel,
    model: modelName,
    dialect,
    absent: 'deny-all',
  });
  const quote = (identifier: string): string => dialect.quoteIdentifier(identifier);
  const statement =
    `SELECT ${quote(constraint.alias)}.${quote(idColumnOf(datamodel, modelName))} AS ${quote('id')} ` +
    `FROM ${constraint.from} ` +
    `WHERE ${constraint.text}`;
  return { statement, parameters: constraint.parameters };
}

async function sqlAnswer(
  db: RawExecutor,
  rendered: Rendered,
): Promise<string> {
  const found = (await db.$queryRawUnsafe(
    rendered.statement,
    ...(rendered.parameters as unknown[]),
  )) as { id: number | bigint }[];
  return renderIds(found.map((entry) => Number(entry.id)));
}

function jsAnswer(where: unknown, rows: readonly Row[]): string {
  return renderIds(rows.filter((row) => evaluateConditions(where, row)).map((row) => row.id));
}

export interface AgreementOptions {
  readonly datamodel: PolicyDatamodel;
  readonly model: string;
  readonly dialect: SqlDialect;
  readonly db: RawExecutor;
  readonly rows: readonly Row[];
}

export async function agreementFor(
  options: AgreementOptions,
  entry: WhereCase,
): Promise<Agreement> {
  let rendered: Rendered;
  try {
    rendered = render(options.datamodel, options.model, options.dialect, entry.where);
  } catch (error) {
    return {
      id: entry.id,
      sql: renderError(error),
      js: jsAnswer(entry.where, options.rows),
      statement: '',
      parameters: [],
    };
  }
  let sql: string;
  try {
    sql = await sqlAnswer(options.db, rendered);
  } catch (error) {
    sql = renderError(error);
  }
  let js: string;
  try {
    js = jsAnswer(entry.where, options.rows);
  } catch (error) {
    js = renderError(error);
  }
  return { id: entry.id, sql, js, statement: rendered.statement, parameters: rendered.parameters };
}

export async function agree(
  options: AgreementOptions,
  cases: readonly WhereCase[],
): Promise<Agreement[]> {
  const agreements: Agreement[] = [];
  for (const entry of cases) {
    agreements.push(await agreementFor(options, entry));
  }
  return agreements;
}

export function label(value: SqlParameter): string {
  if (typeof value === 'bigint') {
    return `${value}n`;
  }
  if (value instanceof Date) {
    return value.toISOString();
  }
  if (typeof value === 'string') {
    return `'${value}'`;
  }
  return String(value);
}

export function disagreementRecord(agreements: readonly Agreement[]): Record<string, string> {
  const record: Record<string, string> = {};
  for (const entry of agreements) {
    if (entry.sql !== entry.js) {
      record[entry.id] = `sql=${entry.sql} js=${entry.js}`;
    }
  }
  return record;
}

export function answerRecord(agreements: readonly Agreement[]): Record<string, string> {
  const record: Record<string, string> = {};
  for (const entry of agreements) {
    record[entry.id] = entry.sql;
  }
  return record;
}

export function discriminating(agreements: readonly Agreement[], rowCount: number): number {
  return agreements.filter(
    (entry) => entry.sql !== '<none>' && entry.sql.split(',').length < rowCount,
  ).length;
}

export function explain(entry: Agreement): string {
  return [
    `case      ${entry.id}`,
    `statement ${entry.statement}`,
    `params    [${entry.parameters.map(label).join(', ')}]`,
    `sql       ${entry.sql}`,
    `js        ${entry.js}`,
  ].join('\n');
}

export function explainAll(agreements: readonly Agreement[]): string {
  return agreements
    .filter((entry) => entry.sql !== entry.js)
    .map(explain)
    .join('\n\n');
}
