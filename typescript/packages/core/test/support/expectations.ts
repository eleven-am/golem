import { GolemEngine } from '../../src/operations';
import { context, engineFor, integers } from './analytics';

export interface AnalyticsSubject {
  readonly provider: string;
  readonly client: Record<string, any>;
}

export function analyticsSuite(subject: () => AnalyticsSubject): void {
  const publishedEngine = (): GolemEngine =>
    engineFor(subject().client, subject().provider, { Post: { published: true } });

  it('answers a window function over exactly the rows the policy permits', async () => {
    const rows = await publishedEngine()
      .scoped({ model: 'Post', context })
      .query((qb) =>
        qb
          .select(['Post.id', 'Post.authorId', 'Post.views'])
          .select((eb) =>
            eb.fn
              .min('Post.views')
              .over((over) => over.partitionBy('Post.authorId'))
              .as('lowestViews'),
          )
          .select((eb) =>
            eb.fn
              .countAll()
              .over((over) => over.partitionBy('Post.authorId'))
              .as('postsByAuthor'),
          )
          .orderBy('Post.id'),
      )
      .execute();

    expect(rows.map((row) => integers(row as Record<string, unknown>))).toEqual([
      { id: 1, authorId: 1, views: 10, lowestViews: 10, postsByAuthor: 2 },
      { id: 2, authorId: 1, views: 30, lowestViews: 10, postsByAuthor: 2 },
      { id: 4, authorId: 2, views: 20, lowestViews: 20, postsByAuthor: 2 },
      { id: 5, authorId: 2, views: 20, lowestViews: 20, postsByAuthor: 2 },
      { id: 6, authorId: 3, views: 99, lowestViews: 99, postsByAuthor: 1 },
    ]);
  });

  it('counts distinct values over the permitted rows only', async () => {
    const rows = await publishedEngine()
      .scoped({ model: 'Post', context })
      .query((qb) =>
        qb
          .select('Post.authorId')
          .select((eb) => eb.fn.count('Post.views').distinct().as('distinctViews'))
          .groupBy('Post.authorId')
          .orderBy('Post.authorId'),
      )
      .execute();

    expect(rows.map((row) => integers(row as Record<string, unknown>))).toEqual([
      { authorId: 1, distinctViews: 2 },
      { authorId: 2, distinctViews: 1 },
      { authorId: 3, distinctViews: 1 },
    ]);
  });

  it('groups by an expression the generated surface cannot express', async () => {
    const rows = await publishedEngine()
      .scoped({ model: 'Post', context })
      .query((qb) =>
        qb
          .select((eb) =>
            eb
              .case()
              .when('Post.views', '>=', 20)
              .then('high')
              .else('low')
              .end()
              .as('band'),
          )
          .select((eb) => eb.fn.countAll().as('posts'))
          .groupBy('band')
          .orderBy('band'),
      )
      .execute();

    expect(rows.map((row) => integers(row as Record<string, unknown>))).toEqual([
      { band: 'high', posts: 4 },
      { band: 'low', posts: 1 },
    ]);
  });

  it('applies both predicates when two scoped roots are joined', async () => {
    const rows = await engineFor(subject().client, subject().provider, {
      Post: { published: true },
      User: { tenantId: 1 },
    })
      .scoped({ model: 'Post', context })
      .join('inner', 'User', 'Author', (join) => join.onRef('Author.id', '=', 'Post.authorId'))
      .query((qb) => qb.select(['Post.id', 'Author.name']).orderBy('Post.id'))
      .execute();

    expect(rows).toEqual([
      { id: 1, name: 'Ada' },
      { id: 2, name: 'Ada' },
      { id: 4, name: 'Bob' },
      { id: 5, name: 'Bob' },
    ]);
  });

  it('gives a caller who may see a subset exactly that subset', async () => {
    const rows = await engineFor(subject().client, subject().provider, {
      Post: { authorId: 2 },
    })
      .scoped({ model: 'Post', context })
      .query((qb) => qb.select('Post.id').orderBy('Post.id'))
      .execute();

    expect(rows).toEqual([{ id: 4 }, { id: 5 }]);
  });

  it('returns nothing at all when the policy denies every row', async () => {
    const rows = await engineFor(subject().client, subject().provider, { Post: null })
      .scoped({ model: 'Post', context })
      .query((qb) => qb.select('Post.id'))
      .execute();

    expect(rows).toEqual([]);
  });

  it('keeps hidden columns out of a select-all over the scoped root', async () => {
    const engine = engineFor(
      subject().client,
      subject().provider,
      {},
      new Map([['Post', new Set(['secretNote'])]]),
    );

    const rows = (await engine
      .scoped({ model: 'Post', context })
      .query((qb) => qb.selectAll().orderBy('Post.id'))
      .execute()) as Record<string, unknown>[];

    expect(rows).toHaveLength(6);
    for (const row of rows) {
      expect(Object.keys(row)).not.toContain('secretNote');
      expect(Object.keys(row)).toContain('title');
    }
  });

  it('runs a scoped query on the transaction client when a transaction is open', async () => {
    const engine = publishedEngine();
    const rows = await engine.transaction(context, (tx) =>
      tx
        .scoped({ model: 'Post' })
        .query((qb) => qb.select('Post.id').orderBy('Post.id'))
        .execute(),
    );

    expect(rows).toEqual([{ id: 1 }, { id: 2 }, { id: 4 }, { id: 5 }, { id: 6 }]);
  });

  it('reaches the secrets table when a policy-free statement asks for it, so the fixture is real', async () => {
    const leak = (await subject().client.$queryRawUnsafe(
      'select "value" from "secrets" order by "id"',
    )) as { value: string }[];
    expect(leak.map((row) => row.value)).toEqual(['never', 'ever']);
  });
}
