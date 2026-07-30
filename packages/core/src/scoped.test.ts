import { scopedContext, scopedEngine } from '../test/support/fixture';

const SCOPED_POST =
  '(select "g0"."post_id" as "id", "g0"."title" as "title", "g0"."author_id" as "authorId", ' +
  '"g0"."published" as "published", "g0"."views" as "views", "g0"."secret_note" as "secretNote" ' +
  'from "posts" as "g0" where ("g0"."author_id" IS ?)) as "Post"';

describe('the scoped root', () => {
  it('wraps the base table in a derived table carrying the policy predicate', async () => {
    const engine = scopedEngine({ constraints: { Post: { authorId: 7 } } });
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) => qb.select(['Post.id', 'Post.title']))
      .compile();

    expect(compiled.sql).toBe(`select "Post"."id", "Post"."title" from ${SCOPED_POST}`);
    expect(compiled.parameters).toEqual([7]);
  });

  it('renders the predicate for the postgres dialect too', async () => {
    const engine = scopedEngine({
      provider: 'postgresql',
      constraints: { Post: { authorId: 7 } },
    });
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) => qb.select('Post.id'))
      .compile();

    expect(compiled.sql).toContain('"g0"."author_id" IS NOT DISTINCT FROM $1');
    expect(compiled.sql).toContain('from "posts" as "g0"');
    expect(compiled.parameters).toEqual([7]);
  });

  it('grants every row when no authorization provider is configured', async () => {
    const engine = scopedEngine();
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) => qb.select('Post.id'))
      .compile();

    expect(compiled.sql).toContain('where (1 = 1)');
    expect(compiled.parameters).toEqual([]);
  });

  it('denies every row when the provider reduces the rule set to null', async () => {
    const engine = scopedEngine({ constraints: { Post: null } });
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) => qb.select('Post.id'))
      .compile();

    expect(compiled.sql).toContain('where (1 = 0)');
  });

  it('projects prisma field names over mapped physical columns', async () => {
    const engine = scopedEngine();
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) => qb.select('Post.secretNote'))
      .compile();

    expect(compiled.sql).toContain('"g0"."secret_note" as "secretNote"');
    expect(compiled.sql).toContain('select "Post"."secretNote" from');
  });

  it('keeps hidden fields out of the scoped projection', async () => {
    const engine = scopedEngine({
      hiddenFields: new Map([['Post', new Set(['secretNote'])]]),
    });
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) => qb.select('Post.id'))
      .compile();

    expect(compiled.sql).not.toContain('secret_note');
    expect(compiled.sql).not.toContain('secretNote');
  });

  it('never exposes a relation field as a column', async () => {
    const engine = scopedEngine();
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) => qb.select('Post.id'))
      .compile();

    expect(compiled.sql).not.toContain('"author"');
  });

  it('accepts a caller-supplied alias', async () => {
    const engine = scopedEngine({ constraints: { Post: { authorId: 7 } } });
    const compiled = await engine
      .scoped({ model: 'Post', alias: 'p', context: scopedContext })
      .query((qb) => qb.select('p.id'))
      .compile();

    expect(compiled.sql).toContain(') as "p"');
    expect(compiled.sql).toContain('select "p"."id" from');
  });

  it('compiles a window function over the scoped root', async () => {
    const engine = scopedEngine({ constraints: { Post: { published: true } } });
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) =>
        qb
          .select(['Post.id', 'Post.authorId'])
          .select((eb) =>
            eb.fn
              .min('Post.views')
              .over((over) => over.partitionBy('Post.authorId'))
              .as('lowestViews'),
          ),
      )
      .compile();

    expect(compiled.sql).toContain('min("Post"."views") over(partition by "Post"."authorId")');
    expect(compiled.sql).toContain('where ("g0"."published" IS ?)');
  });

  it('compiles a distinct count and an expression grouping over the scoped root', async () => {
    const engine = scopedEngine({ constraints: { Post: { published: true } } });
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) =>
        qb
          .select((eb) => eb.fn('abs', ['Post.views']).as('magnitude'))
          .select((eb) => eb.fn.count('Post.authorId').distinct().as('authors'))
          .groupBy('magnitude'),
      )
      .compile();

    expect(compiled.sql).toContain('abs("Post"."views") as "magnitude"');
    expect(compiled.sql).toContain('count(distinct "Post"."authorId") as "authors"');
    expect(compiled.sql).toContain('group by "magnitude"');
  });

  it('refuses to read a scoped alias as a table inside a subquery, where no predicate reaches', async () => {
    const engine = scopedEngine({ constraints: { Post: { published: true } } });
    await expect(
      engine
        .scoped({ model: 'Post', context: scopedContext })
        .query((qb) =>
          qb
            .select('Post.id')
            .where((eb) => eb.exists(eb.selectFrom('Post' as never).select('Post.id' as never))),
        )
        .compile(),
    ).rejects.toThrow('may not read the table "Post" in a FROM clause');
  });

  it('joins a second scoped root, carrying both predicates', async () => {
    const engine = scopedEngine({
      constraints: { Post: { published: true }, User: { tenantId: 3 } },
    });
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .join('inner', 'User', 'Author', (join) => join.onRef('Author.id', '=', 'Post.authorId'))
      .query((qb) => qb.select(['Post.id', 'Author.name']))
      .compile();

    expect(compiled.sql).toContain('from "posts" as "g0"');
    expect(compiled.sql).toContain('where ("g0"."published" IS ?)');
    expect(compiled.sql).toContain('from "users" as "g1"');
    expect(compiled.sql).toContain('where ("g1"."tenant_id" IS ?)');
    expect(compiled.sql).toContain('inner join (select "g1"."user_id" as "id"');
    expect(compiled.sql).toContain(') as "Author" on "Author"."id" = "Post"."authorId"');
    expect(compiled.parameters).toEqual([true, 3]);
  });

  it('renders a left join when the caller asks for one', async () => {
    const engine = scopedEngine({ constraints: { Post: {}, User: {} } });
    const compiled = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .join('left', 'User', 'Author', (join) => join.onRef('Author.id', '=', 'Post.authorId'))
      .query((qb) => qb.select('Post.id'))
      .compile();

    expect(compiled.sql).toContain('left join (select "g1"."user_id" as "id"');
  });

  it('refuses two roots sharing an alias', async () => {
    const engine = scopedEngine();
    await expect(
      engine
        .scoped({ model: 'Post', alias: 'x', context: scopedContext })
        .join('inner', 'User', 'x', (join) => join.onRef('x.id', '=', 'x.id'))
        .query((qb) => qb.select('x.id'))
        .compile(),
    ).rejects.toThrow('cannot declare "x" twice');
  });

  it('refuses a model that is not in the datamodel', async () => {
    const engine = scopedEngine();
    await expect(
      engine
        .scoped({ model: 'Ghost', context: scopedContext })
        .query((qb) => qb.select('Ghost.id' as never))
        .compile(),
    ).rejects.toThrow('not a model in the datamodel');
  });

  it('refuses to compile when the datamodel carries no datasource provider', async () => {
    const engine = scopedEngine({ provider: undefined });
    await expect(
      engine
        .scoped({ model: 'Post', context: scopedContext })
        .query((qb) => qb.select('Post.id'))
        .compile(),
    ).rejects.toThrow('regenerate the golem client');
  });

  it('refuses a datasource provider it cannot render a predicate for', async () => {
    const engine = scopedEngine({ provider: 'mysql' });
    await expect(
      engine
        .scoped({ model: 'Post', context: scopedContext })
        .query((qb) => qb.select('Post.id'))
        .compile(),
    ).rejects.toThrow('sqlite and postgresql only');
  });
});

describe('executing a scoped query', () => {
  it('runs the compiled statement through the prisma client', async () => {
    const queryRawUnsafe = jest.fn().mockResolvedValue([{ id: 1 }]);
    const engine = scopedEngine({
      client: { $queryRawUnsafe: queryRawUnsafe },
      constraints: { Post: { authorId: 7 } },
    });

    const rows = await engine
      .scoped({ model: 'Post', context: scopedContext })
      .query((qb) => qb.select('Post.id'))
      .execute();

    expect(rows).toEqual([{ id: 1 }]);
    expect(queryRawUnsafe).toHaveBeenCalledTimes(1);
    const [sql, ...parameters] = queryRawUnsafe.mock.calls[0]!;
    expect(sql).toContain('from "posts" as "g0"');
    expect(parameters).toEqual([7]);
  });

  it('refuses a client that cannot run raw statements', async () => {
    const engine = scopedEngine({ client: {} });
    await expect(
      engine
        .scoped({ model: 'Post', context: scopedContext })
        .query((qb) => qb.select('Post.id'))
        .execute(),
    ).rejects.toThrow('$queryRawUnsafe');
  });

  it('executes on the transaction client when one is open', async () => {
    const ambient = jest.fn().mockResolvedValue([{ id: 2 }]);
    const outer = jest.fn().mockResolvedValue([{ id: 99 }]);
    const client = {
      $queryRawUnsafe: outer,
      $transaction: (run: (tx: unknown) => Promise<unknown>) =>
        run({ $queryRawUnsafe: ambient }),
    };
    const engine = scopedEngine({ client, constraints: { Post: { authorId: 7 } } });

    const rows = await engine.transaction(scopedContext, (tx) =>
      tx
        .scoped({ model: 'Post' })
        .query((qb) => qb.select('Post.id'))
        .execute(),
    );

    expect(rows).toEqual([{ id: 2 }]);
    expect(ambient).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();
  });
});
