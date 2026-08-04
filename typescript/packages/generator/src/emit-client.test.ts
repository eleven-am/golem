import { emitClientModule } from './emit-client';

describe('generated Golem client', () => {
  const output = emitClientModule(['User', 'Post'], '../prisma/client');

  it('routes the complete policy operation surface through GolemEngine', () => {
    for (const operation of [
      'findUnique', 'findFirst', 'findMany', 'create', 'update', 'updateMany',
      'delete', 'deleteMany', 'upsert',
    ]) {
      expect(output).toContain(`${operation}:`);
    }
    expect(output).toContain("user: 'User'");
    expect(output).toContain("post: 'Post'");
  });

  it('provides a branch probe for truthful raw upsert events without replacing the native query', () => {
    expect(output).toContain('findExisting: model');
    expect(output).toContain('.findUnique({ where, select })');
    expect(output).toContain('query: query as');
  });

  it('buffers intercepted writes until the native transaction commits', () => {
    expect(output).toContain("import { withBufferedEvents } from '@eleven-am/golem-core'");
    expect(output).toContain('const transaction = instrumented.$transaction.bind(instrumented)');
    expect(output).toContain('withBufferedEvents(() =>');
    expect(output).toContain('withBufferedEvents(() =>\n                      raw.$transaction');
    expect(output).toContain('$transaction: commitAwareTransaction');
    expect(output).toContain('transactionContext.run(');
    expect(output).toContain('{ client: tx, suppressBatchEvents: false }');
  });

  it('runs batch-event helpers on the ambient interactive transaction', () => {
    expect(output).toContain("import { AsyncLocalStorage } from 'node:async_hooks'");
    expect(output).toContain('const current = transactionContext.getStore()');
    expect(output).toContain('if (current) return execute(current.client)');
    expect(output).toContain('raw.$transaction((tx) =>');
    expect(output).toContain('{ client, suppressBatchEvents: true }');
    expect(output).toContain('batch: model');
  });

  it('exposes count, aggregate, and separately typed relation aggregation through the context-bound surface', () => {
    expect(output).toContain('count:');
    expect(output).toContain('aggregate:');
    expect(output).toContain("relationGroupBy: 'relationGroupBy'");
    expect(output).toContain("Omit<RelationGroupByRequest<TDimension, TMeasure>, 'model' | 'context'>");
  });

  it('generates an explicit policy-aware argument whitelist instead of copying Prisma delegates', () => {
    expect(output).toContain('type ContextBoundDelegate<TDelegate> = {');
    expect(output).toContain("'findMany', 'where' | 'orderBy' | 'take' | 'skip' | 'cursor' | 'distinct' | 'select' | 'include' | 'omit'");
    expect(output).toContain("'aggregate', 'where' | 'orderBy' | 'cursor' | 'take' | 'skip' | '_count' | '_avg' | '_sum' | '_min' | '_max'");
    expect(output).toContain("'groupBy', 'where' | 'orderBy' | 'by' | 'having' | 'take' | 'skip' | '_count' | '_avg' | '_sum' | '_min' | '_max'");
    expect(output).toContain("'updateMany', 'where' | 'data'");
    expect(output).toContain("'deleteMany', 'where'");
    expect(output).toContain("'count', 'where'");
    expect(output).not.toContain('Pick<TDelegate');
  });

  it('uses Prisma payload inference for projection-sensitive results', () => {
    expect(output).toContain("Promise<Prisma.Result<TDelegate, TArgs, 'findUnique'>>");
    expect(output).toContain("Promise<Prisma.Result<TDelegate, TArgs, 'findMany'>>");
    expect(output).toContain("Promise<Prisma.Result<TDelegate, TArgs, 'aggregate'>>");
    expect(output).toContain("Promise<Prisma.Result<TDelegate, TArgs, 'groupBy'>>");
    expect(output).toContain('): Promise<number>;');
  });

  it('exposes the scoped query root on the context-bound surface', () => {
    expect(output).toContain('GolemEngineRef, GolemQueryInterceptor, RelationGroupByRequest, RelationGroupByRow, ScopedQuery');
    expect(output).toContain('$scoped(model: GolemModelName, alias?: string): ScopedQuery;');
    expect(output).toContain("if (delegateName === '$scoped')");
    expect(output).toContain('.scoped({ model, alias, context })');
  });

  it('binds the scoped query root to the open transaction inside $transaction', () => {
    expect(output).toContain('.scoped({ model, alias })');
    expect(output).toContain('txView as unknown as {');
  });

  it('routes context-bound $transaction through the engine transaction', () => {
    expect(output).toContain("if (prop === '$transaction')");
    expect(output).toContain('.transaction(');
    expect(output).toContain('$transaction<T>(');
    expect(output).toContain('fn: (tx: ContextBoundDelegates) => Promise<T>');
  });
});
