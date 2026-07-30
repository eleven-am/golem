import type { Kysely, KyselyPlugin, Sql } from 'kysely';
import { GolemValidationError } from './errors';
import { scopedContext, scopedEngine } from '../test/support/fixture';
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

  it('refuses withPlugin when the plugin adds a common table expression', async () => {
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
      'cannot carry a common table expression',
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
