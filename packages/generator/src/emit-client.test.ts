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
    expect(output).toContain('$transaction: commitAwareTransaction');
  });

  it('exposes count and aggregate through the context-bound surface', () => {
    expect(output).toContain('count:');
    expect(output).toContain('aggregate:');
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

  it('routes context-bound $transaction through the engine transaction', () => {
    expect(output).toContain("if (prop === '$transaction')");
    expect(output).toContain('.transaction(');
    expect(output).toContain('$transaction<T>(');
    expect(output).toContain('fn: (tx: ContextBoundDelegates) => Promise<T>');
  });
});
