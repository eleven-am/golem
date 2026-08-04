import type { Kysely, KyselyPlugin, Sql } from 'kysely';
import { GolemValidationError } from './errors';
import { scopedContext, scopedEngine, scopedFieldQuery } from '../test/support/fixture';
import { CompiledScopedQuery, ScopedSelectBuilder } from './scoped';

type MustBeRemoved =
  | 'modifyFront'
  | 'modifyEnd'
  | 'innerJoin'
  | 'leftJoin'
  | 'rightJoin'
  | 'fullJoin'
  | 'crossJoin'
  | 'innerJoinLateral'
  | 'leftJoinLateral'
  | 'crossJoinLateral'
  | 'union'
  | 'unionAll'
  | 'withPlugin';

type StillReachable = Extract<keyof ScopedSelectBuilder, MustBeRemoved>;

const escapesRemovedFromTheType: [StillReachable] extends [never] ? true : false = true;

let foreign: Kysely<any>;
let sql: Sql;

beforeAll(async () => {
  const kysely = await import('kysely');
  sql = kysely.sql;
  foreign = new kysely.Kysely<any>({
    dialect: {
      createAdapter: () => new kysely.SqliteAdapter(),
      createDriver: () => new kysely.DummyDriver(),
      createIntrospector: (db) => new kysely.SqliteIntrospector(db),
      createQueryCompiler: () => new kysely.SqliteQueryCompiler(),
    },
  });
});

const tableNode = (name: string) => ({
  kind: 'TableNode',
  table: { kind: 'SchemableIdentifierNode', identifier: { kind: 'IdentifierNode', name } },
});

function scoped() {
  return scopedEngine({ constraints: { Post: { authorId: 7 }, User: { tenantId: 3 } } }).scoped({
    model: 'Post',
    context: scopedContext,
  });
}

function attempt(build: (builder: any) => unknown): Promise<CompiledScopedQuery> {
  return scoped()
    .query((builder) => build(builder) as never)
    .compile();
}

function compose(build: (builder: any, creator: any) => unknown): Promise<CompiledScopedQuery> {
  return scoped()
    .query((builder, creator) => build(builder, creator) as never)
    .compile();
}

describe('the narrowed builder type', () => {
  it('removes every documented escape from the type golem hands out', () => {
    expect(escapesRemovedFromTheType).toBe(true);
  });

  it('leaves those escapes callable at runtime, so the type is not the boundary', async () => {
    const builder = await new Promise<any>((resolve) => {
      void scoped()
        .query((qb) => {
          resolve(qb);
          return qb.select('Post.id' as never) as never;
        })
        .compile();
    });
    for (const escape of [
      'modifyFront',
      'modifyEnd',
      'innerJoin',
      'leftJoin',
      'rightJoin',
      'fullJoin',
      'crossJoin',
      'innerJoinLateral',
      'leftJoinLateral',
      'crossJoinLateral',
      'union',
      'unionAll',
      'withPlugin',
    ]) {
      expect(typeof builder[escape]).toBe('function');
    }
  });
});

describe('the thirteen escapes, each cast past the type', () => {
  it('refuses innerJoin onto an unscoped table', async () => {
    await expect(attempt((qb) => qb.innerJoin('secrets', 'secrets.id', 'Post.id'))).rejects.toThrow(
      'may only join scoped roots golem created',
    );
  });

  it('refuses leftJoin onto an unscoped table', async () => {
    await expect(attempt((qb) => qb.leftJoin('secrets', 'secrets.id', 'Post.id'))).rejects.toThrow(
      'may only join scoped roots golem created',
    );
  });

  it('refuses rightJoin onto an unscoped table', async () => {
    await expect(attempt((qb) => qb.rightJoin('secrets', 'secrets.id', 'Post.id'))).rejects.toThrow(
      'may only join scoped roots golem created',
    );
  });

  it('refuses fullJoin onto an unscoped table', async () => {
    await expect(attempt((qb) => qb.fullJoin('secrets', 'secrets.id', 'Post.id'))).rejects.toThrow(
      'may only join scoped roots golem created',
    );
  });

  it('refuses crossJoin onto an unscoped table', async () => {
    await expect(attempt((qb) => qb.crossJoin('secrets'))).rejects.toThrow(
      'may only join scoped roots golem created',
    );
  });

  it('refuses innerJoinLateral onto an unscoped subquery', async () => {
    await expect(
      attempt((qb) =>
        qb.innerJoinLateral(
          (eb: any) => eb.selectFrom('secrets').select('secrets.value').as('leak'),
          (join: any) => join.onRef('leak.value', '=', 'Post.title'),
        ),
      ),
    ).rejects.toThrow('may only join scoped roots golem created');
  });

  it('refuses leftJoinLateral onto an unscoped subquery', async () => {
    await expect(
      attempt((qb) =>
        qb.leftJoinLateral(
          (eb: any) => eb.selectFrom('secrets').select('secrets.value').as('leak'),
          (join: any) => join.onRef('leak.value', '=', 'Post.title'),
        ),
      ),
    ).rejects.toThrow('may only join scoped roots golem created');
  });

  it('refuses crossJoinLateral onto an unscoped subquery', async () => {
    await expect(
      attempt((qb) =>
        qb.crossJoinLateral((eb: any) =>
          eb.selectFrom('secrets').select('secrets.value').as('leak'),
        ),
      ),
    ).rejects.toThrow('may only join scoped roots golem created');
  });

  it('refuses modifyFront, which injects arbitrary leading SQL', async () => {
    await expect(attempt((qb) => qb.select('Post.id').modifyFront(sql`distinct`))).rejects.toThrow(
      'may not carry raw SQL golem did not create',
    );
  });

  it('refuses modifyEnd, which appends an unscoped read of the base table', async () => {
    await expect(
      attempt((qb) => qb.select('Post.id').modifyEnd(sql`union all select post_id from posts`)),
    ).rejects.toThrow('may not carry raw SQL golem did not create');
  });

  it('refuses union with an unscoped query', async () => {
    await expect(
      attempt((qb) => qb.select('Post.id').union(foreign.selectFrom('posts').select('post_id'))),
    ).rejects.toThrow('carries a set operation');
  });

  it('refuses unionAll with an unscoped query', async () => {
    await expect(
      attempt((qb) => qb.select('Post.id').unionAll(foreign.selectFrom('posts').select('post_id'))),
    ).rejects.toThrow('carries a set operation');
  });

  it('refuses unionAll even when the operand is itself scoped', async () => {
    await expect(attempt((qb) => qb.select('Post.id').unionAll(qb.select('Post.id')))).rejects.toThrow(
      'carries a set operation',
    );
  });

  it('refuses intersect and except, which the same clause carries', async () => {
    await expect(
      attempt((qb) => qb.select('Post.id').intersect(foreign.selectFrom('posts').select('post_id'))),
    ).rejects.toThrow('carries a set operation');
    await expect(
      attempt((qb) => qb.select('Post.id').except(foreign.selectFrom('posts').select('post_id'))),
    ).rejects.toThrow('carries a set operation');
  });

  it('refuses withPlugin when the plugin replaces the scoped root with the base table', async () => {
    const stripper: KyselyPlugin = {
      transformQuery: ({ node }) =>
        node.kind === 'SelectQueryNode'
          ? ({ ...node, from: { kind: 'FromNode', froms: [tableNode('posts')] } } as typeof node)
          : node,
      transformResult: async ({ result }) => result,
    };
    await expect(attempt((qb) => qb.select('Post.id').withPlugin(stripper))).rejects.toThrow(
      'the FROM clause of a scoped query must be a scoped root golem created',
    );
  });

  it('refuses withPlugin when the plugin rebuilds the scoped root without its predicate', async () => {
    const swapper: KyselyPlugin = {
      transformQuery: ({ node }) => {
        if (node.kind !== 'SelectQueryNode') {
          return node;
        }
        const from = (node as any).from;
        return {
          ...node,
          from: {
            ...from,
            froms: from.froms.map((entry: any) => ({
              ...entry,
              node: {
                kind: 'RawNode',
                sqlFragments: ['(select post_id as id from posts)'],
                parameters: [],
              },
            })),
          },
        } as typeof node;
      },
      transformResult: async ({ result }) => result,
    };
    await expect(attempt((qb) => qb.select('Post.id').withPlugin(swapper))).rejects.toThrow(
      'the FROM clause of a scoped query must be a scoped root golem created',
    );
  });

  it('refuses withPlugin when the plugin adds a common table expression over a raw table', async () => {
    const smuggler: KyselyPlugin = {
      transformQuery: ({ node }) =>
        node.kind === 'SelectQueryNode'
          ? ({
              ...node,
              with: {
                kind: 'WithNode',
                expressions: [
                  {
                    kind: 'CommonTableExpressionNode',
                    name: { kind: 'CommonTableExpressionNameNode', table: tableNode('leak') },
                    expression: foreign.selectFrom('secrets').select('value').toOperationNode(),
                  },
                ],
              },
            } as typeof node)
          : node,
      transformResult: async ({ result }) => result,
    };
    await expect(attempt((qb) => qb.select('Post.id').withPlugin(smuggler))).rejects.toThrow(
      'may not read the table "secrets" in a FROM clause',
    );
  });
});

describe('escapes the thirteen do not cover', () => {
  it('refuses a raw sql fragment anywhere in the projection', async () => {
    await expect(
      attempt((qb) => qb.select(sql`(select value from secrets limit 1)`.as('leak'))),
    ).rejects.toThrow('may not carry raw SQL golem did not create');
  });

  it('refuses a raw sql fragment in the predicate', async () => {
    await expect(
      attempt((qb) => qb.select('Post.id').where(sql`1 = 1 or exists (select 1 from secrets)`)),
    ).rejects.toThrow('may not carry raw SQL golem did not create');
  });

  it('refuses an unscoped correlated subquery, which is an existence oracle', async () => {
    await expect(
      attempt((qb) =>
        qb
          .select('Post.id')
          .where((eb: any) =>
            eb.exists(
              eb.selectFrom('secrets').select('secrets.id').whereRef('secrets.id', '=', 'Post.id'),
            ),
          ),
      ),
    ).rejects.toThrow('may not read the table "secrets" in a FROM clause');
  });

  it('refuses reading a scoped alias as though it were a table', async () => {
    await expect(
      attempt((qb) =>
        qb.select('Post.id').where((eb: any) => eb.exists(eb.selectFrom('Post').select('Post.id'))),
      ),
    ).rejects.toThrow('may not read the table "Post" in a FROM clause');
  });

  it('refuses an unscoped table reached through a schema', async () => {
    await expect(
      attempt((qb) =>
        qb
          .select('Post.id')
          .where((eb: any) => eb.exists(eb.selectFrom('public.secrets').select('secrets.id'))),
      ),
    ).rejects.toThrow('may not read the table "secrets" in a FROM clause');
  });

  it('refuses a column qualified by a table golem never declared', async () => {
    await expect(attempt((qb) => qb.select('secrets.value'))).rejects.toThrow(
      'references the table "secrets"',
    );
  });

  it('refuses a column qualified by a schema', async () => {
    await expect(attempt((qb) => qb.select('public.secrets.value'))).rejects.toThrow(
      'may not name a schema',
    );
  });

  it('refuses a data-modifying statement smuggled into the query', async () => {
    await expect(
      attempt((qb) => qb.select('Post.id').modifyEnd(foreign.deleteFrom('posts'))),
    ).rejects.toThrow('may only read, but it carries a DeleteQueryNode');
  });

  it('refuses a data-modifying common table expression, which on postgres is still a SELECT', async () => {
    const smuggler: KyselyPlugin = {
      transformQuery: ({ node }) =>
        node.kind === 'SelectQueryNode'
          ? ({
              ...node,
              with: {
                kind: 'WithNode',
                expressions: [
                  {
                    kind: 'CommonTableExpressionNode',
                    name: { kind: 'CommonTableExpressionNameNode', table: tableNode('gone') },
                    expression: foreign.deleteFrom('posts').toOperationNode(),
                  },
                ],
              },
            } as typeof node)
          : node,
      transformResult: async ({ result }) => result,
    };
    await expect(attempt((qb) => qb.select('Post.id').withPlugin(smuggler))).rejects.toThrow(
      'must be a SELECT, but "gone" is a DeleteQueryNode',
    );
  });

  it('refuses a builder that came from somewhere other than golem', async () => {
    await expect(
      scoped()
        .query(() => foreign.selectFrom('posts').select('post_id') as never)
        .compile(),
    ).rejects.toThrow('the FROM clause of a scoped query must be a scoped root golem created');
  });

  it('refuses a statement that is not a SELECT at all', async () => {
    await expect(
      scoped()
        .query(() => foreign.insertInto('posts').values({ post_id: 1 }) as never)
        .compile(),
    ).rejects.toThrow('must compile to a single SELECT, but the builder produced a InsertQueryNode');
  });

  it('refuses a callback that returns something that is not a query builder', async () => {
    await expect(
      scoped()
        .query(() => ({ sql: 'select 1' }) as never)
        .compile(),
    ).rejects.toThrow('not a Kysely query builder');
  });

  it('refuses a set operation golem cannot recognise as a node', async () => {
    const smuggler: KyselyPlugin = {
      transformQuery: ({ node }) =>
        node.kind === 'SelectQueryNode'
          ? ({
              ...node,
              setOperations: [{ expression: tableNode('posts') }],
            } as unknown as typeof node)
          : node,
      transformResult: async ({ result }) => result,
    };
    await expect(attempt((qb) => qb.select('Post.id').withPlugin(smuggler))).rejects.toThrow(
      'must be a single SELECT, but it carries a set operation',
    );
  });

  it('refuses a query whose FROM clause has been emptied', async () => {
    const emptier: KyselyPlugin = {
      transformQuery: ({ node }) =>
        node.kind === 'SelectQueryNode'
          ? ({ ...node, from: { kind: 'FromNode', froms: [] } } as typeof node)
          : node,
      transformResult: async ({ result }) => result,
    };
    await expect(attempt((qb) => qb.select('Post.id').withPlugin(emptier))).rejects.toThrow(
      'its FROM clause is empty',
    );
  });

  it('refuses every escape with a GolemValidationError, not a bare Error', async () => {
    await expect(
      attempt((qb) => qb.innerJoin('secrets', 'secrets.id', 'Post.id')),
    ).rejects.toBeInstanceOf(GolemValidationError);
  });
});

describe('the composition a multi-pass query needs, turned against the root', () => {
  it('refuses a common table expression whose body reads a raw table', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('leak', (c: any) => c.selectFrom('secrets').select('secrets.value as value'))
          .selectFrom('leak')
          .select('leak.value'),
      ),
    ).rejects.toThrow('may not read the table "secrets" in a FROM clause');
  });

  it('refuses a common table expression named after a physical table', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('posts', () => qb.select('Post.id'))
          .selectFrom('posts')
          .select('posts.id'),
      ),
    ).rejects.toThrow(
      'may not name a common table expression "posts", which is the physical table of model "Post"',
    );
  });

  it('refuses a common table expression named after a physical table in any letter case', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('POSTS', () => qb.select('Post.id'))
          .selectFrom('POSTS')
          .select('POSTS.id'),
      ),
    ).rejects.toThrow('which is the physical table of model "Post"');
  });

  it('refuses a common table expression named after a physical table no root reads', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('metrics', () => qb.select('Post.id'))
          .selectFrom('metrics')
          .select('metrics.id'),
      ),
    ).rejects.toThrow('which is the physical table of model "Metric"');
  });

  it('refuses a common table expression named after a scoped root', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('Post', () => qb.select('Post.id'))
          .selectFrom('Post')
          .select('Post.id'),
      ),
    ).rejects.toThrow('which is already the alias of the scoped root "Post"');
  });

  it('refuses a subquery in FROM that reaches a raw table at depth three', async () => {
    await expect(
      compose((qb, db) =>
        db
          .selectFrom(
            db
              .selectFrom(
                db.selectFrom('secrets').select('secrets.value as value').as('third'),
              )
              .select('third.value as value')
              .as('second'),
          )
          .select('second.value'),
      ),
    ).rejects.toThrow('may not read the table "secrets" in a FROM clause');
  });

  it('refuses a data-modifying common table expression, which on postgres is still a SELECT', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('gone', () => foreign.deleteFrom('posts'))
          .selectFrom(qb.select('Post.id').as('base'))
          .select('base.id'),
      ),
    ).rejects.toThrow('must be a SELECT, but "gone" is a DeleteQueryNode');
  });

  it('refuses a join inside a common table expression body onto a raw table', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('mixed', (c: any) =>
            c
              .selectFrom(qb.select('Post.id').as('base'))
              .innerJoin('secrets', 'secrets.id', 'base.id')
              .select('base.id as id'),
          )
          .selectFrom('mixed')
          .select('mixed.id'),
      ),
    ).rejects.toThrow('may not read the table "secrets" in a JOIN clause');
  });

  it('refuses a read of a common table expression declared out of scope', async () => {
    await expect(
      compose((qb, db) =>
        db
          .selectFrom(qb.select('Post.id').as('base'))
          .select('base.id')
          .where((eb: any) => eb.exists(eb.selectFrom('hidden').select('hidden.id')))
          .where((eb: any) =>
            eb.exists(
              db
                .with('hidden', () => qb.select('Post.id'))
                .selectFrom('hidden')
                .select('hidden.id'),
            ),
          ),
      ),
    ).rejects.toThrow('may not read the table "hidden" in a FROM clause');
  });

  it('refuses a common table expression that reads a sibling declared after it', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('first', (c: any) => c.selectFrom('second').select('second.id as id'))
          .with('second', () => qb.select('Post.id'))
          .selectFrom('first')
          .select('first.id'),
      ),
    ).rejects.toThrow('may not read the table "second" in a FROM clause');
  });

  it('refuses a common table expression that reads the physical table it is named for', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('shadow', (c: any) => c.selectFrom('shadow').select('shadow.id as id'))
          .selectFrom('shadow')
          .select('shadow.id'),
      ),
    ).rejects.toThrow('may not read the table "shadow" in a FROM clause');
  });

  it('still refuses a join onto a raw table at the top level of a composed query', async () => {
    await expect(
      compose((qb, db) =>
        db
          .with('base', () => qb.select('Post.id'))
          .selectFrom('base')
          .innerJoin('secrets', 'secrets.id', 'base.id')
          .select('base.id'),
      ),
    ).rejects.toThrow('may only join scoped roots golem created');
  });
});

describe('builder methods that do not defeat the scoped root', () => {
  it('survives clearWhere, which only clears the outer predicate', async () => {
    const compiled = await attempt((qb) =>
      qb.select('Post.id').where('Post.views', '>', 10).clearWhere(),
    );
    expect(compiled.sql).toContain('where ("g0"."author_id" IS ?)');
    expect(compiled.sql).not.toContain('"views" > ');
    expect(compiled.parameters).toEqual([7]);
  });

  it('survives clearSelect, clearLimit and clearOrderBy', async () => {
    const compiled = await attempt((qb) =>
      qb
        .select('Post.id')
        .limit(1)
        .orderBy('Post.id')
        .clearSelect()
        .clearLimit()
        .clearOrderBy()
        .selectAll(),
    );
    expect(compiled.sql).toContain('where ("g0"."author_id" IS ?)');
  });

  it('survives $call, which hands back the same builder', async () => {
    const compiled = await attempt((qb) => qb.$call((inner: any) => inner.select('Post.id')));
    expect(compiled.sql).toContain('where ("g0"."author_id" IS ?)');
    expect(compiled.sql).toContain('select "Post"."id"');
  });

  it('allows a plugin that adds nothing golem would refuse', async () => {
    const noop: KyselyPlugin = {
      transformQuery: ({ node }) => node,
      transformResult: async ({ result }) => result,
    };
    const compiled = await attempt((qb) => qb.select('Post.id').withPlugin(noop));
    expect(compiled.sql).toContain('where ("g0"."author_id" IS ?)');
  });
});

const WITHHELD = [{ model: 'Post', field: 'secretNote', access: 'never' as const }];

function withheld(provider = 'sqlite') {
  return scopedFieldQuery(
    {
      provider,
      constraints: { Post: { authorId: 7 }, User: { tenantId: 3 } },
      fields: WITHHELD,
    },
    { model: 'Post', context: scopedContext },
  );
}

function reach(build: (builder: any, creator: any) => unknown): Promise<CompiledScopedQuery> {
  return withheld()
    .query((builder, creator) => build(builder, creator) as never)
    .compile();
}

describe('a field the caller may not read, reached through every clause the builder offers', () => {
  it('refuses to select it', async () => {
    await expect(reach((qb) => qb.select(['Post.id', 'Post.secretNote']))).rejects.toThrow(
      'may not reference "Post"."secretNote"',
    );
  });

  it('refuses to filter by it', async () => {
    await expect(
      reach((qb) => qb.select('Post.id').where('Post.secretNote', '=', 'n3')),
    ).rejects.toThrow('may not reference "Post"."secretNote"');
  });

  it('refuses to order by it', async () => {
    await expect(reach((qb) => qb.select('Post.id').orderBy('Post.secretNote'))).rejects.toThrow(
      'may not reference "Post"."secretNote"',
    );
  });

  it('refuses to group by it', async () => {
    await expect(reach((qb) => qb.select('Post.id').groupBy('Post.secretNote'))).rejects.toThrow(
      'may not reference "Post"."secretNote"',
    );
  });

  it('refuses it inside the body of a common table expression', async () => {
    await expect(
      reach((qb, db) =>
        db
          .with('leak', () => qb.select(['Post.id', 'Post.secretNote']))
          .selectFrom('leak')
          .select('leak.id'),
      ),
    ).rejects.toThrow('may not reference "Post"."secretNote"');
  });

  it('refuses it inside a subquery in the FROM clause', async () => {
    await expect(
      reach((qb, db) =>
        db.selectFrom(qb.select(['Post.id', 'Post.secretNote']).as('inner')).select('inner.id'),
      ),
    ).rejects.toThrow('may not reference "Post"."secretNote"');
  });

  it('refuses it inside a correlated subquery', async () => {
    await expect(
      reach((qb, db) =>
        db
          .with('peers', () => qb.select(['Post.id', 'Post.title']))
          .selectFrom(
            qb
              .select(['Post.id'])
              .where((eb: any) =>
                eb.exists(
                  eb
                    .selectFrom('peers')
                    .select('peers.id')
                    .whereRef('peers.title', '=', 'Post.secretNote'),
                ),
              )
              .as('p'),
          )
          .select('p.id'),
      ),
    ).rejects.toThrow('may not reference "Post"."secretNote"');
  });

  it('refuses it in the condition joining two scoped roots', async () => {
    await expect(
      withheld()
        .join('inner', 'User', 'Author', (join: any) =>
          join.onRef('Author.name', '=', 'Post.secretNote'),
        )
        .query((qb: any) => qb.select('Post.id'))
        .compile(),
    ).rejects.toThrow('may not reference "Post"."secretNote"');
  });

  it('refuses a withheld field of the joined root, not only of the first', async () => {
    await expect(
      scopedFieldQuery(
        {
          constraints: { Post: { authorId: 7 }, User: { tenantId: 3 } },
          fields: [{ model: 'User', field: 'name', access: 'never' }],
        },
        { model: 'Post', context: scopedContext },
      )
        .join('inner', 'User', 'Author', (join: any) => join.onRef('Author.id', '=', 'Post.authorId'))
        .query((qb: any) => qb.select(['Post.id', 'Author.name']))
        .compile(),
    ).rejects.toThrow('may not reference "Author"."name"');
  });

  it('refuses it on postgres as readily as on sqlite', async () => {
    await expect(
      withheld('postgresql')
        .query((qb: any) => qb.select('Post.id').where('Post.secretNote', '=', 'n3'))
        .compile(),
    ).rejects.toThrow('may not reference "Post"."secretNote"');
  });

  it('refuses a hidden field reached the same way', async () => {
    await expect(
      scopedFieldQuery(
        {
          constraints: { Post: { authorId: 7 } },
          hiddenFields: new Map([['Post', new Set(['secretNote'])]]),
        },
        { model: 'Post', context: scopedContext },
      )
        .query((qb: any) => qb.select('Post.id').orderBy('Post.secretNote'))
        .compile(),
    ).rejects.toThrow('may not reference "Post"."secretNote"');
  });

  it('leaves a field the caller may read reachable in every one of those clauses', async () => {
    const compiled = await reach((qb, db) =>
      db
        .with('leak', () => qb.select(['Post.id', 'Post.title']).where('Post.title', '=', 'a1'))
        .selectFrom('leak')
        .select('leak.id')
        .orderBy('leak.title')
        .groupBy(['leak.id', 'leak.title']),
    );
    expect(compiled.sql).toContain('where "Post"."title" = ?');
    expect(compiled.sql).toContain('order by "leak"."title"');
  });
});

describe('a conditionally readable field, whose mask the whole query reads through', () => {
  function masked(provider = 'sqlite') {
    return scopedFieldQuery(
      {
        provider,
        constraints: { Post: { authorId: 7 } },
        fields: [{ model: 'Post', field: 'secretNote', condition: { published: true } }],
      },
      { model: 'Post', context: scopedContext },
    );
  }

  it('filters through the mask, never through the column', async () => {
    const compiled = await masked()
      .query((qb: any) => qb.select('Post.id').where('Post.secretNote', '=', 'n3'))
      .compile();

    expect(compiled.sql).toContain('where "Post"."secretNote" = ?');
    expect(compiled.sql).toContain('case when (("g0"."published" IS ?)) then "g0"."secret_note"');
    expect(compiled.sql).not.toContain('"g0"."secret_note" as "secretNote"');
  });

  it('orders through the mask, never through the column', async () => {
    const compiled = await masked()
      .query((qb: any) => qb.select('Post.id').orderBy('Post.secretNote'))
      .compile();

    expect(compiled.sql).toContain('order by "Post"."secretNote"');
    expect(compiled.sql.match(/"g0"\."secret_note"/g)).toHaveLength(1);
    expect(compiled.sql).toContain('case when');
  });

  it('carries the mask into a common table expression that reads the field', async () => {
    const compiled = await masked()
      .query((qb: any, db: any) =>
        db
          .with('notes', () => qb.select(['Post.id', 'Post.secretNote']))
          .selectFrom('notes')
          .select(['notes.id', 'notes.secretNote'])
          .where('notes.secretNote', 'is not', null),
      )
      .compile();

    expect(compiled.sql.match(/"g0"\."secret_note"/g)).toHaveLength(1);
    expect(compiled.sql).toContain('case when');
  });

  it('carries the mask on postgres too', async () => {
    const compiled = await masked('postgresql')
      .query((qb: any) => qb.select('Post.secretNote').where('Post.secretNote', '=', 'n3'))
      .compile();

    expect(compiled.sql).toContain(
      'case when (("g0"."published" IS NOT DISTINCT FROM $1)) then "g0"."secret_note" else null end',
    );
    expect(compiled.sql).toContain('where "Post"."secretNote" = $3');
  });
});
