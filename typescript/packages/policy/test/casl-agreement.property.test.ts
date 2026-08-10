import { prismaQuery } from '@casl/prisma';
import fc from 'fast-check';
import { evaluateConditions, validateConditions } from '../src/index';
import { NUM_RUNS, SEED, conditionArbitrary, objectArbitrary } from './support/arbitraries';
import { GENERATED_DIVERGENCE_IDS, classifyCase } from './support/casl-divergences';

jest.setTimeout(300000);

type Outcome =
  | { readonly kind: 'value'; readonly value: unknown }
  | { readonly kind: 'throw'; readonly name: string; readonly message: string };

function runCasl(conditions: unknown, object: unknown): Outcome {
  try {
    return { kind: 'value', value: (prismaQuery as (c: unknown) => (o: unknown) => unknown)(conditions)(object) };
  } catch (error) {
    return {
      kind: 'throw',
      name: (error as Error).constructor.name,
      message: (error as Error).message,
    };
  }
}

function runGolem(conditions: unknown, object: unknown): Outcome {
  try {
    return { kind: 'value', value: evaluateConditions(conditions, object) };
  } catch (error) {
    return { kind: 'throw', name: (error as Error).name, message: (error as Error).message };
  }
}

function show(value: unknown): string {
  return JSON.stringify(
    value,
    (_key, entry) => {
      if (typeof entry === 'bigint') {
        return `${entry}n`;
      }
      if (entry === undefined) {
        return '<undefined>';
      }
      if (typeof entry === 'number' && !Number.isFinite(entry)) {
        return String(entry);
      }
      return entry;
    },
  );
}

interface Agreement {
  readonly coverage: Map<string, { conditions: unknown; object: unknown }>;
  readonly witnesses: { conditions: unknown; object: unknown; casl: Outcome; golem: Outcome }[];
  readonly tally: { total: number; excused: number };
}

function runAgreement(): Agreement {
  const coverage = new Map<string, { conditions: unknown; object: unknown }>();
  const witnesses: { conditions: unknown; object: unknown; casl: Outcome; golem: Outcome }[] = [];
  const tally = { total: 0, excused: 0 };
  fc.assert(
    fc.property(conditionArbitrary(), objectArbitrary(), (conditions, object) => {
      const golem = runGolem(conditions, object);
      const casl = runCasl(conditions, object);
      const classes = classifyCase(conditions, object);
      tally.total += 1;
      tally.excused += classes.size > 0 ? 1 : 0;
      for (const id of classes) {
        if (!coverage.has(id)) {
          coverage.set(id, { conditions, object });
        }
      }
      const golemValue = golem.kind === 'value' ? golem.value : golem;
      if (classes.size > 0) {
        if (casl.kind === 'throw' || typeof casl.value !== 'boolean' || casl.value !== golemValue) {
          witnesses.push({ conditions, object, casl, golem });
        }
        return;
      }
      if (casl.kind === 'throw') {
        throw new Error(
          `unrecorded divergence: @casl/prisma threw ${casl.name} on ${show(conditions)} / ${show(object)}: ${casl.message}`,
        );
      }
      if (typeof casl.value !== 'boolean') {
        throw new Error(
          `unrecorded divergence: @casl/prisma returned ${String(casl.value)} on ${show(conditions)} / ${show(object)}`,
        );
      }
      if (casl.value !== golemValue) {
        throw new Error(
          `unrecorded divergence on ${show(conditions)} / ${show(object)}: casl=${String(casl.value)} golem=${String(golemValue)}`,
        );
      }
    }),
    { seed: SEED, numRuns: NUM_RUNS },
  );
  return { coverage, witnesses, tally };
}

let outcome: { readonly agreement: Agreement } | { readonly failure: unknown } | undefined;

function agreement(): Agreement {
  if (outcome === undefined) {
    try {
      outcome = { agreement: runAgreement() };
    } catch (failure) {
      outcome = { failure };
    }
  }
  if ('failure' in outcome) {
    throw outcome.failure;
  }
  return outcome.agreement;
}

describe('golem agrees with @casl/prisma except on the recorded divergences', () => {
  it('never throws and never returns a non-boolean for a supported condition', () => {
    fc.assert(
      fc.property(conditionArbitrary(), objectArbitrary(), (conditions, object) => {
        expect(validateConditions(conditions).supported).toBe(true);
        const golem = runGolem(conditions, object);
        if (golem.kind !== 'value') {
          throw new Error(`golem threw on ${show(conditions)} / ${show(object)}: ${golem.message}`);
        }
        expect(typeof golem.value).toBe('boolean');
      }),
      { seed: SEED, numRuns: NUM_RUNS },
    );
  });

  it('agrees with @casl/prisma on every case no recorded divergence explains', () => {
    expect(agreement().tally.total).toBe(NUM_RUNS);
  });

  it('exercises every divergence the generators are meant to reach', () => {
    const { coverage } = agreement();
    const missing = GENERATED_DIVERGENCE_IDS.filter((id) => !coverage.has(id));
    expect(missing).toEqual([]);
  });

  it('finds real disagreements behind the excused classes rather than excusing agreements', () => {
    const { witnesses, tally } = agreement();
    expect(witnesses.length).toBeGreaterThan(tally.excused / 10);
  });

  it('still asserts exact agreement on a quarter of everything it generates', () => {
    const { tally } = agreement();
    expect(tally.total).toBe(NUM_RUNS);
    expect(tally.total - tally.excused).toBeGreaterThan(tally.total / 4);
  });
});
