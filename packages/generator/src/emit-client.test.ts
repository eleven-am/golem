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
});
