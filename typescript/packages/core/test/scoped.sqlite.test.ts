import { context, engineFor, seed, seedPlays } from './support/analytics';
import { analyticsSuite } from './support/expectations';
import { engineFieldScopeSuite, fieldScopeSuite } from './support/field-scope';
import { multiPassSuite } from './support/multipass';
import { SqliteHandle, openSqlite } from './support/sqlite';

describe('a scoped analytical query against a live sqlite database', () => {
  let handle: SqliteHandle;

  beforeAll(async () => {
    handle = await openSqlite();
    await seed(handle.prisma, () => '?');
    await seedPlays(handle.prisma, () => '?');
  });

  afterAll(async () => {
    await handle.close();
  });

  analyticsSuite(() => ({ provider: 'sqlite', client: handle.prisma as unknown as Record<string, any> }));

  multiPassSuite(() => ({
    provider: 'sqlite',
    client: handle.prisma as unknown as Record<string, any>,
  }));

  fieldScopeSuite(() => ({
    provider: 'sqlite',
    client: handle.prisma as unknown as Record<string, any>,
  }));

  engineFieldScopeSuite(() => ({
    provider: 'sqlite',
    client: handle.prisma as unknown as Record<string, any>,
  }));

  it('binds the policy parameter rather than inlining it', async () => {
    const compiled = await engineFor(
      handle.prisma as unknown as Record<string, any>,
      'sqlite',
      { Post: { authorId: 2 } },
    )
      .scoped({ model: 'Post', context })
      .query((qb) => qb.select('Post.id'))
      .compile();

    expect(compiled.sql).toContain('?');
    expect(compiled.sql).not.toContain('= 2');
    expect(compiled.parameters).toEqual([2]);
  });
});
