import { CaslRule } from './casl';

function conjoin(branches: readonly unknown[]): unknown {
  if (branches.length === 0) {
    return {};
  }
  if (branches.length === 1) {
    return branches[0];
  }
  return { AND: [...branches] };
}

function disjoin(branches: readonly unknown[]): unknown {
  if (branches.length === 0) {
    return { OR: [] };
  }
  if (branches.length === 1) {
    return branches[0];
  }
  return { OR: [...branches] };
}

export function deriveFieldConstraint(chain: readonly CaslRule[]): unknown {
  const branches: unknown[] = [];
  const unmatchedEarlier: unknown[] = [];
  for (const rule of chain) {
    const conditions = rule.conditions;
    if (conditions === undefined || conditions === null) {
      if (!rule.inverted) {
        branches.push(conjoin(unmatchedEarlier));
      }
      return disjoin(branches);
    }
    if (!rule.inverted) {
      branches.push(conjoin([conditions, ...unmatchedEarlier]));
    }
    unmatchedEarlier.push({ NOT: conditions });
  }
  return disjoin(branches);
}
