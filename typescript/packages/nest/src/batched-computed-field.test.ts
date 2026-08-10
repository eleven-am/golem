import 'reflect-metadata';
import { Args, Context, Info, Parent } from '@nestjs/graphql';
import { BatchedComputedField, ComputedField, GOLEM_COMPUTED_FIELD } from './decorators';
import type { ComputedFieldMetadata } from './decorators';
import { extractExtensionSpecs } from './extensions';
import { SHARED_CONTEXT_MESSAGE, golemRequestBoundary } from './request-boundary';

interface Row {
  id: string;
  teamId: string | null;
}

class Counter {
  calls: Array<{ keys: readonly string[]; ctx: unknown; args: Record<string, unknown> }> = [];

  @BatchedComputedField('User', { type: 'Int!', key: 'id' })
  postCount(
    keys: readonly string[],
    ctx: unknown,
    args: Record<string, unknown>,
  ): Promise<Map<string, number>> {
    this.calls.push({ keys, ctx, args });
    const scale = (ctx as { scale?: number }).scale ?? 1;
    return Promise.resolve(new Map(keys.map((key, index) => [key, (index + 1) * scale])));
  }

  @BatchedComputedField('User', { type: 'Int', key: 'teamId', requires: ['id'] })
  teamSize(keys: readonly string[]): Promise<Map<string, number>> {
    this.calls.push({ keys, ctx: undefined, args: {} });
    return Promise.resolve(new Map(keys.map((key) => [key, key.length])));
  }

  @ComputedField('User', { type: 'String!', requires: ['id'] })
  label(parent: Row): string {
    return parent.id.toUpperCase();
  }
}

function resolveField(
  instance: Counter,
  field: 'postCount' | 'teamSize' | 'label',
  parent: Row,
  ctx: unknown,
  args: Record<string, unknown> = {},
  info: { rootValue?: unknown } = {},
): unknown {
  return (instance[field] as unknown as (...callArgs: unknown[]) => unknown).call(
    instance,
    parent,
    args,
    ctx,
    info,
  );
}

const rows: Row[] = [
  { id: 'u1', teamId: 't1' },
  { id: 'u2', teamId: 't1' },
  { id: 'u3', teamId: null },
];

describe('BatchedComputedField', () => {
  it('declares the same computed field metadata a per-row field would', () => {
    const metadata = Reflect.getMetadata(
      GOLEM_COMPUTED_FIELD,
      Counter.prototype.postCount,
    ) as ComputedFieldMetadata;

    expect(metadata).toEqual({
      model: 'User',
      type: 'Int!',
      name: undefined,
      args: undefined,
      requires: ['id'],
    });
  });

  it('folds the key field into the columns the row query must select', () => {
    const specs = extractExtensionSpecs([Counter]);

    expect(specs.computedFields).toMatchObject([
      { model: 'User', name: 'postCount', requires: ['id'] },
      { model: 'User', name: 'teamSize', requires: ['id', 'teamId'] },
      { model: 'User', name: 'label', requires: ['id'] },
    ]);
  });

  it('loads once for all the parents of one request', async () => {
    const counter = new Counter();
    const ctx = {};

    const values = await Promise.all(
      rows.map((row) => resolveField(counter, 'postCount', row, ctx)),
    );

    expect(values).toEqual([1, 2, 3]);
    expect(counter.calls).toHaveLength(1);
    expect(counter.calls[0].keys).toEqual(['u1', 'u2', 'u3']);
    expect(counter.calls[0].ctx).toBe(ctx);
  });

  it('keeps two requests from sharing a batch', async () => {
    const counter = new Counter();
    const wideRequest = { scale: 10 };
    const narrowRequest = { scale: 1 };

    const [wide, narrow] = await Promise.all([
      Promise.all(rows.map((row) => resolveField(counter, 'postCount', row, wideRequest))),
      Promise.all(rows.map((row) => resolveField(counter, 'postCount', row, narrowRequest))),
    ]);

    expect(wide).toEqual([10, 20, 30]);
    expect(narrow).toEqual([1, 2, 3]);
    expect(counter.calls).toHaveLength(2);
  });

  it('reads again for each execution of one long-lived context', async () => {
    const counter = new Counter();
    const connection = { scale: 1 };

    await resolveField(counter, 'postCount', rows[0], connection, {}, { rootValue: { event: 1 } });
    await resolveField(counter, 'postCount', rows[0], connection, {}, { rootValue: { event: 2 } });

    expect(counter.calls).toHaveLength(2);
  });

  it('answers a parent whose key is null and still batches the rest', async () => {
    const counter = new Counter();
    const ctx = {};

    const values = await Promise.all(rows.map((row) => resolveField(counter, 'teamSize', row, ctx)));

    expect(values).toEqual([2, 2, null]);
    expect(counter.calls).toHaveLength(1);
    expect(counter.calls[0].keys).toEqual(['t1']);
  });

  it('hands the field arguments to the batch and batches each set apart', async () => {
    const counter = new Counter();
    const ctx = {};

    await Promise.all([
      resolveField(counter, 'postCount', rows[0], ctx, { published: true }),
      resolveField(counter, 'postCount', rows[1], ctx, { published: false }),
    ]);

    expect(counter.calls).toHaveLength(2);
    expect(counter.calls.map((call) => call.args)).toEqual([
      { published: true },
      { published: false },
    ]);
  });

  it('gives every waiting parent the failure when the batch fails', async () => {
    class Failing {
      @BatchedComputedField('User', { type: 'Int!', key: 'id' })
      postCount(): Promise<Map<string, number>> {
        return Promise.reject(new Error('post count unavailable'));
      }
    }
    const instance = new Failing();
    const ctx = {};

    const settled = await Promise.allSettled(
      rows.map((row) =>
        (instance.postCount as unknown as (...args: unknown[]) => Promise<unknown>).call(
          instance,
          row,
          {},
          ctx,
          {},
        ),
      ),
    );

    expect(settled.map((result) => result.status)).toEqual([
      'rejected',
      'rejected',
      'rejected',
    ]);
    for (const result of settled) {
      expect((result as PromiseRejectedResult).reason.message).toBe('post count unavailable');
    }
  });

  it('keeps two provider instances from sharing a batch', async () => {
    const first = new Counter();
    const second = new Counter();
    const ctx = {};

    await Promise.all([
      resolveField(first, 'postCount', rows[0], ctx),
      resolveField(second, 'postCount', rows[1], ctx),
    ]);

    expect(first.calls).toHaveLength(1);
    expect(second.calls).toHaveLength(1);
  });

  it('refuses to load with a context created for another request', async () => {
    const counter = new Counter();
    const ctx = {};
    let first!: Promise<unknown>;
    golemRequestBoundary({}, {}, () => {
      first = resolveField(counter, 'postCount', rows[0], ctx) as Promise<unknown>;
    });
    await first;

    let second!: Promise<unknown>;
    golemRequestBoundary({}, {}, () => {
      second = resolveField(counter, 'postCount', rows[1], ctx) as Promise<unknown>;
    });

    await expect(second).rejects.toThrow(SHARED_CONTEXT_MESSAGE);
    expect(counter.calls).toHaveLength(1);
  });

  it('loads normally when each request builds its own context', async () => {
    const counter = new Counter();
    let first!: Promise<unknown>;
    golemRequestBoundary({}, {}, () => {
      first = resolveField(counter, 'postCount', rows[0], {}) as Promise<unknown>;
    });
    let second!: Promise<unknown>;
    golemRequestBoundary({}, {}, () => {
      second = resolveField(counter, 'postCount', rows[1], {}) as Promise<unknown>;
    });

    await expect(first).resolves.toBe(1);
    await expect(second).resolves.toBe(1);
    expect(counter.calls).toHaveLength(2);
  });

  it('refuses a batched method that takes its parent through a parameter decorator', () => {
    expect(() => {
      class Broken {
        @BatchedComputedField('User', { type: 'Int!', key: 'id' })
        postCount(@Parent() _parent: Row): Promise<Map<string, number>> {
          return Promise.resolve(new Map());
        }
      }
      return Broken;
    }).toThrow(
      'Batched computed field User.postCount on Broken.postCount cannot use @Parent(): a batched method is called with (keys, ctx, args) and a Nest parameter decorator rewrites those positions',
    );
  });

  it('names every parameter decorator a batched method reached for', () => {
    expect(() => {
      class Broken {
        @BatchedComputedField('User', { type: 'Int!', key: 'id', name: 'tally' })
        postCount(
          @Context() _ctx: unknown,
          @Args() _args: Record<string, unknown>,
          @Info() _info: unknown,
        ): Promise<Map<string, number>> {
          return Promise.resolve(new Map());
        }
      }
      return Broken;
    }).toThrow(
      'Batched computed field User.tally on Broken.postCount cannot use @Args(), @Context(), @Info()',
    );
  });

  it('leaves a per-row computed field taking parameter decorators', () => {
    expect(() => {
      class Fine {
        @ComputedField('User', { type: 'String!', requires: ['id'] })
        label(@Parent() parent: Row, @Context() _ctx: unknown): string {
          return parent.id;
        }
      }
      return Fine;
    }).not.toThrow();
  });

  it('leaves a per-row computed field resolving per row', () => {
    const counter = new Counter();

    expect(resolveField(counter, 'label', rows[0], {})).toBe('U1');
    expect(counter.calls).toHaveLength(0);
  });
});
