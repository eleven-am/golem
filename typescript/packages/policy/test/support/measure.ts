import { evaluateConditions } from '../../src/index';
import { WhereCase } from './matrix';

export interface Row {
  readonly id: number;
  readonly [key: string]: unknown;
}

export interface ParentFinder {
  findMany(args: { where: unknown; select: { id: true } }): Promise<{ id: number }[]>;
}

export type DivergenceClass =
  | 'golem-superset'
  | 'prisma-superset'
  | 'prisma-rejects'
  | 'golem-rejects'
  | 'disjoint';

export interface Measurement {
  readonly id: string;
  readonly prisma: string;
  readonly golem: string;
}

export const NO_ROWS = '<none>';

export function renderIds(ids: readonly number[]): string {
  return ids.length === 0 ? NO_ROWS : [...ids].sort((left, right) => left - right).join(',');
}

function renderError(error: unknown): string {
  return `error:${(error as { name?: string }).name ?? 'Error'}`;
}

function parseAnswer(answer: string): Set<number> | null {
  if (answer.startsWith('error:')) {
    return null;
  }
  return answer === NO_ROWS ? new Set() : new Set(answer.split(',').map(Number));
}

function isSubset(left: Set<number>, right: Set<number>): boolean {
  return [...left].every((entry) => right.has(entry));
}

export function classify(measurement: Measurement): DivergenceClass {
  const prisma = parseAnswer(measurement.prisma);
  const golem = parseAnswer(measurement.golem);
  if (prisma === null) {
    return 'prisma-rejects';
  }
  if (golem === null) {
    return 'golem-rejects';
  }
  if (isSubset(prisma, golem)) {
    return 'golem-superset';
  }
  if (isSubset(golem, prisma)) {
    return 'prisma-superset';
  }
  return 'disjoint';
}

async function prismaAnswer(finder: ParentFinder, where: unknown): Promise<string> {
  try {
    const found = await finder.findMany({ where, select: { id: true } });
    return renderIds(found.map((entry) => entry.id));
  } catch (error) {
    return renderError(error);
  }
}

function golemAnswer(where: unknown, rows: readonly Row[]): string {
  try {
    return renderIds(rows.filter((row) => evaluateConditions(where, row)).map((row) => row.id));
  } catch (error) {
    return renderError(error);
  }
}

export async function measure(
  finder: ParentFinder,
  rows: readonly Row[],
  cases: readonly WhereCase[],
): Promise<Measurement[]> {
  const measurements: Measurement[] = [];
  for (const entry of cases) {
    measurements.push({
      id: entry.id,
      prisma: await prismaAnswer(finder, entry.where),
      golem: golemAnswer(entry.where, rows),
    });
  }
  return measurements;
}

export function divergenceRecord(measurements: readonly Measurement[]): Record<string, string> {
  const record: Record<string, string> = {};
  for (const entry of measurements) {
    if (entry.prisma !== entry.golem) {
      record[entry.id] = `prisma=${entry.prisma} golem=${entry.golem}`;
    }
  }
  return record;
}

export function classCounts(measurements: readonly Measurement[]): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const entry of measurements) {
    if (entry.prisma === entry.golem) {
      continue;
    }
    const name = classify(entry);
    counts[name] = (counts[name] ?? 0) + 1;
  }
  return counts;
}
