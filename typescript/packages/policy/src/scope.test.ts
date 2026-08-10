import { PolicyDatamodel, PolicyDatamodelField } from './datamodel';
import { SqlRenderError, UnsupportedConditionError } from './errors';
import {
  ConstraintSql,
  createDatamodelSqlScope,
  renderConstraintNode,
  renderConstraintSql,
} from './scope';
import {
  SqlDialect,
  SqlScope,
  postgresDialect,
  renderSql,
  sqlIdentifier,
  sqliteDialect,
} from './sql';

function scalar(name: string, type: string, dbName: string): PolicyDatamodelField {
  return { name, dbName, kind: 'scalar', type, isList: false };
}

const DATAMODEL: PolicyDatamodel = {
  models: [
    {
      name: 'Post',
      dbName: 'post_rows',
      fields: [
        scalar('id', 'Int', 'post_pk'),
        scalar('title', 'String', 'title_text'),
        scalar('views', 'Int', 'view_count'),
        scalar('published', 'Boolean', 'is_published'),
        scalar('createdAt', 'DateTime', 'created_at'),
        { name: 'status', dbName: 'status_value', kind: 'enum', type: 'Status', isList: false },
        { name: 'tags', dbName: 'tag_list', kind: 'scalar', type: 'String', isList: true },
        scalar('authorId', 'String', 'author_ref'),
        {
          name: 'author',
          dbName: 'author',
          kind: 'object',
          type: 'User',
          isList: false,
          relationName: 'PostToUser',
          relationFromFields: ['authorId'],
          relationToFields: ['id'],
        },
        {
          name: 'reviewer',
          dbName: 'reviewer',
          kind: 'object',
          type: 'User',
          isList: false,
          relationName: 'PostReviewer',
          relationFromFields: ['reviewerId'],
          relationToFields: ['id'],
        },
        scalar('reviewerId', 'String', 'reviewer_ref'),
        {
          name: 'comments',
          dbName: 'comments',
          kind: 'object',
          type: 'Comment',
          isList: true,
          relationName: 'CommentToPost',
        },
        {
          name: 'ghost',
          dbName: 'ghost',
          kind: 'object',
          type: 'Ghost',
          isList: false,
          relationName: 'PostToGhost',
          relationFromFields: ['authorId'],
          relationToFields: ['id'],
        },
        {
          name: 'lopsided',
          dbName: 'lopsided',
          kind: 'object',
          type: 'User',
          isList: false,
          relationName: 'PostToUserLopsided',
          relationFromFields: ['authorId', 'reviewerId'],
          relationToFields: ['id'],
        },
      ],
    },
    {
      name: 'User',
      dbName: 'user_rows',
      fields: [
        scalar('id', 'String', 'user_pk'),
        scalar('name', 'String', 'name_text'),
        scalar('orgId', 'Int', 'org_ref'),
        {
          name: 'org',
          dbName: 'org',
          kind: 'object',
          type: 'Org',
          isList: false,
          relationName: 'UserToOrg',
          relationFromFields: ['orgId'],
          relationToFields: ['id'],
        },
        {
          name: 'profile',
          dbName: 'profile',
          kind: 'object',
          type: 'Profile',
          isList: false,
          relationName: 'UserToProfile',
        },
        { name: 'posts', dbName: 'posts', kind: 'object', type: 'Post', isList: true, relationName: 'PostToUser' },
      ],
    },
    {
      name: 'Org',
      dbName: 'org_rows',
      fields: [scalar('id', 'Int', 'org_pk'), scalar('tier', 'Int', 'tier_value'), scalar('slug', 'String', 'slug_text')],
    },
    {
      name: 'Profile',
      dbName: 'profile_rows',
      fields: [scalar('id', 'Int', 'profile_pk'), scalar('userId', 'String', 'user_ref')],
    },
    {
      name: 'Comment',
      dbName: 'comment_rows',
      fields: [
        scalar('id', 'Int', 'comment_pk'),
        scalar('body', 'String', 'body_text'),
        scalar('postId', 'Int', 'post_id'),
        {
          name: 'post',
          dbName: 'post',
          kind: 'object',
          type: 'Post',
          isList: false,
          relationName: 'CommentToPost',
          relationFromFields: ['postId'],
          relationToFields: ['id'],
        },
      ],
    },
  ],
};

const UNCORRELATABLE: PolicyDatamodel = {
  models: [
    {
      name: 'Course',
      dbName: 'course_rows',
      fields: [
        scalar('id', 'Int', 'course_pk'),
        { name: 'students', dbName: 'students', kind: 'object', type: 'Student', isList: true, relationName: 'CourseToStudent' },
        { name: 'seats', dbName: 'seats', kind: 'object', type: 'Seat', isList: true },
      ],
    },
    {
      name: 'Student',
      dbName: 'student_rows',
      fields: [
        scalar('id', 'Int', 'student_pk'),
        { name: 'courses', dbName: 'courses', kind: 'object', type: 'Course', isList: true, relationName: 'CourseToStudent' },
      ],
    },
    {
      name: 'Seat',
      dbName: 'seat_rows',
      fields: [
        scalar('id', 'Int', 'seat_pk'),
        scalar('bookedId', 'Int', 'booked_ref'),
        scalar('waitingId', 'Int', 'waiting_ref'),
        {
          name: 'booked',
          dbName: 'booked',
          kind: 'object',
          type: 'Course',
          isList: false,
          relationFromFields: ['bookedId'],
          relationToFields: ['id'],
        },
        {
          name: 'waiting',
          dbName: 'waiting',
          kind: 'object',
          type: 'Course',
          isList: false,
          relationFromFields: ['waitingId'],
          relationToFields: ['id'],
        },
      ],
    },
  ],
};

const COMPOSITE: PolicyDatamodel = {
  models: [
    {
      name: 'Doc',
      dbName: 'doc_rows',
      fields: [
        scalar('id', 'Int', 'doc_pk'),
        scalar('tenantId', 'String', 'tenant_ref'),
        scalar('ownerKey', 'Int', 'owner_ref'),
        {
          name: 'owner',
          dbName: 'owner',
          kind: 'object',
          type: 'Owner',
          isList: false,
          relationName: 'DocToOwner',
          relationFromFields: ['tenantId', 'ownerKey'],
          relationToFields: ['tenant', 'key'],
        },
      ],
    },
    {
      name: 'Owner',
      dbName: 'owner_rows',
      fields: [
        scalar('tenant', 'String', 'tenant_col'),
        scalar('key', 'Int', 'key_col'),
        scalar('level', 'Int', 'level_col'),
      ],
    },
  ],
};

const SELF_RELATION: PolicyDatamodel = {
  models: [
    {
      name: 'Node',
      dbName: 'node_rows',
      fields: [
        scalar('id', 'Int', 'node_pk'),
        scalar('label', 'String', 'label_text'),
        scalar('parentId', 'Int', 'parent_ref'),
        {
          name: 'parent',
          dbName: 'parent',
          kind: 'object',
          type: 'Node',
          isList: false,
          relationName: 'NodeSelf',
          relationFromFields: ['parentId'],
          relationToFields: ['id'],
        },
      ],
    },
  ],
};

function sql(
  constraint: unknown,
  dialect: SqlDialect = postgresDialect,
  datamodel: PolicyDatamodel = DATAMODEL,
  model = 'Post',
): ConstraintSql {
  return renderConstraintSql(constraint, { datamodel, model, dialect, absent: 'deny-all' });
}

function text(constraint: unknown, dialect: SqlDialect = postgresDialect): string {
  return sql(constraint, dialect).text;
}

describe('the datamodel-bound scope', () => {
  it('qualifies a column with the alias and the physical names', () => {
    expect(sql({ views: 3 })).toEqual({
      text: '("t0"."view_count" IS NOT DISTINCT FROM $1)',
      parameters: [3],
      table: 'post_rows',
      alias: 't0',
      from: '"post_rows" AS "t0"',
    });
  });

  it('honours a caller-supplied root alias', () => {
    const rendered = renderConstraintSql(
      { views: 3 },
      { datamodel: DATAMODEL, model: 'Post', dialect: postgresDialect, absent: 'deny-all', alias: 'p' },
    );
    expect(rendered.text).toBe('("p"."view_count" IS NOT DISTINCT FROM $1)');
    expect(rendered.from).toBe('"post_rows" AS "p"');
  });

  it('exposes the physical table and alias on the scope itself', () => {
    const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
    expect(scope.model).toBe('Post');
    expect(scope.table).toBe('post_rows');
    expect(scope.alias).toBe('t0');
  });

  it('quotes an identifier that would otherwise break out of the alias', () => {
    const evil: PolicyDatamodel = {
      models: [{ name: 'M', dbName: 'we"ird', fields: [scalar('f', 'Int', 'col"umn')] }],
    };
    const rendered = sql({ f: 1 }, postgresDialect, evil, 'M');
    expect(rendered.text).toBe('("t0"."col""umn" IS NOT DISTINCT FROM $1)');
    expect(rendered.from).toBe('"we""ird" AS "t0"');
  });
});

describe('every operator against the datamodel scope', () => {
  const cases: ReadonlyArray<readonly [string, unknown, Record<string, string>, readonly unknown[]]> = [
    [
      'implicit equals',
      { views: 3 },
      {
        postgres: '("t0"."view_count" IS NOT DISTINCT FROM $1)',
        sqlite: '("t0"."view_count" IS ?)',
      },
      [3],
    ],
    [
      'equals',
      { views: { equals: 3 } },
      {
        postgres: '("t0"."view_count" IS NOT DISTINCT FROM $1)',
        sqlite: '("t0"."view_count" IS ?)',
      },
      [3],
    ],
    [
      'equals null',
      { views: { equals: null } },
      {
        postgres: '("t0"."view_count" IS NULL)',
        sqlite: '("t0"."view_count" IS NULL)',
      },
      [],
    ],
    [
      'not',
      { views: { not: 3 } },
      {
        postgres: 'NOT ("t0"."view_count" IS NOT DISTINCT FROM $1)',
        sqlite: 'NOT ("t0"."view_count" IS ?)',
      },
      [3],
    ],
    [
      'not null',
      { views: { not: null } },
      {
        postgres: '("t0"."view_count" IS NOT NULL)',
        sqlite: '("t0"."view_count" IS NOT NULL)',
      },
      [],
    ],
    [
      'in',
      { views: { in: [1, 2] } },
      {
        postgres: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" IN ($1, $2))',
        sqlite: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" IN (?, ?))',
      },
      [1, 2],
    ],
    [
      'notIn',
      { views: { notIn: [1] } },
      {
        postgres: '("t0"."view_count" IS NULL OR "t0"."view_count" NOT IN ($1))',
        sqlite: '("t0"."view_count" IS NULL OR "t0"."view_count" NOT IN (?))',
      },
      [1],
    ],
    [
      'lt',
      { views: { lt: 5 } },
      {
        postgres: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" < $1)',
        sqlite: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" < ?)',
      },
      [5],
    ],
    [
      'lte',
      { views: { lte: 5 } },
      {
        postgres: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" <= $1)',
        sqlite: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" <= ?)',
      },
      [5],
    ],
    [
      'gt',
      { views: { gt: 5 } },
      {
        postgres: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" > $1)',
        sqlite: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" > ?)',
      },
      [5],
    ],
    [
      'gte',
      { views: { gte: 5 } },
      {
        postgres: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" >= $1)',
        sqlite: '("t0"."view_count" IS NOT NULL AND "t0"."view_count" >= ?)',
      },
      [5],
    ],
    [
      'is',
      { author: { is: { orgId: 4 } } },
      {
        postgres:
          '(EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE "C" = "t0"."author_ref" COLLATE "C" AND (("t0_1"."org_ref" IS NOT DISTINCT FROM $1))))',
        sqlite:
          '(EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE BINARY = "t0"."author_ref" COLLATE BINARY AND (("t0_1"."org_ref" IS ?))))',
      },
      [4],
    ],
  ];

  it.each(cases)('renders %s on every dialect', (_name, constraint, expected, parameters) => {
    for (const dialect of [postgresDialect, sqliteDialect]) {
      const rendered = sql(constraint, dialect);
      expect(rendered.text).toBe(expected[dialect.name]);
      expect(rendered.parameters).toEqual(parameters);
    }
  });

  it('binds identical parameters on every dialect', () => {
    const constraint = { OR: [{ views: 1 }, { title: { in: ['a', 'b'] } }] };
    const rendered = [postgresDialect, sqliteDialect].map((dialect) => sql(constraint, dialect).parameters);
    expect(rendered[0]).toEqual(rendered[1]);
  });

  it('never inlines a value into the text', () => {
    const rendered = sql({ title: "x'; DROP TABLE post_rows; --" });
    expect(rendered.text).not.toContain('DROP');
    expect(rendered.parameters).toEqual(["x'; DROP TABLE post_rows; --"]);
  });
});

describe('the correlated EXISTS', () => {
  it('emits a correlated subquery rather than a join', () => {
    const rendered = sql({ author: { is: { name: 'ada' } } });
    expect(rendered.text).toBe(
      '(EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE "C" = "t0"."author_ref" COLLATE "C" AND (("t0_1"."name_text" COLLATE "C" IS NOT DISTINCT FROM $1))))',
    );
    expect(rendered.text).not.toContain('JOIN');
    expect(rendered.from).toBe('"post_rows" AS "t0"');
  });

  it('keeps the base FROM clause free of the related table', () => {
    const rendered = sql({ author: { is: { org: { is: { tier: 1 } } } } });
    expect(rendered.from).toBe('"post_rows" AS "t0"');
    expect(rendered.table).toBe('post_rows');
  });

  it('correlates on every column of a composite foreign key', () => {
    const rendered = sql({ owner: { is: { level: 2 } } }, postgresDialect, COMPOSITE, 'Doc');
    expect(rendered.text).toBe(
      '(EXISTS (SELECT 1 FROM "owner_rows" AS "t0_1" WHERE "t0_1"."tenant_col" COLLATE "C" = "t0"."tenant_ref" COLLATE "C" AND "t0_1"."key_col" = "t0"."owner_ref" AND (("t0_1"."level_col" IS NOT DISTINCT FROM $1))))',
    );
    expect(rendered.parameters).toEqual([2]);
  });

  it('leaves a null foreign key rescuable by the other branch of a disjunction', () => {
    const rendered = sql({ OR: [{ author: { is: { name: 'ada' } } }, { published: true }] });
    expect(rendered.text).toBe(
      '((EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE "C" = "t0"."author_ref" COLLATE "C" AND (("t0_1"."name_text" COLLATE "C" IS NOT DISTINCT FROM $1)))) OR ("t0"."is_published" IS NOT DISTINCT FROM $2))',
    );
    expect(rendered.from).toBe('"post_rows" AS "t0"');
    expect(rendered.text).not.toContain('JOIN');
  });

  it('keeps the relation predicate inside the branch that mentions it', () => {
    const rendered = sql({ OR: [{ author: { is: { name: 'ada' } } }, { published: true }] });
    const branches = rendered.text.slice(1, -1).split(' OR ');
    expect(branches).toHaveLength(2);
    expect(branches[0]).toContain('EXISTS');
    expect(branches[1]).not.toContain('EXISTS');
  });

  it('negates the whole subquery under NOT', () => {
    expect(text({ NOT: [{ author: { is: { name: 'ada' } } }] })).toBe(
      'NOT ((EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE "C" = "t0"."author_ref" COLLATE "C" AND (("t0_1"."name_text" COLLATE "C" IS NOT DISTINCT FROM $1)))))',
    );
  });

  it('numbers parameters across the subquery boundary in order of appearance', () => {
    const rendered = sql({ AND: [{ views: 1 }, { author: { is: { name: 'ada' } } }, { views: 2 }] });
    expect(rendered.parameters).toEqual([1, 'ada', 2]);
    expect(rendered.text).toContain('$2');
  });
});

describe('the to-many quantifiers', () => {
  const COMMENT_HOP =
    'SELECT 1 FROM "comment_rows" AS "t0_1" WHERE "t0_1"."post_id" = "t0"."post_pk" AND';

  it('correlates through the foreign key the related model holds', () => {
    expect(text({ comments: { some: { body: 'x' } } })).toBe(
      `(EXISTS (${COMMENT_HOP} (("t0_1"."body_text" COLLATE "C" IS NOT DISTINCT FROM $1))))`,
    );
  });

  it('renders none as the negation of that subquery', () => {
    expect(text({ comments: { none: { body: 'x' } } })).toBe(
      `NOT (EXISTS (${COMMENT_HOP} (("t0_1"."body_text" COLLATE "C" IS NOT DISTINCT FROM $1))))`,
    );
  });

  it('renders every so that a row the condition leaves unknown still violates it', () => {
    expect(text({ comments: { every: { body: 'x' } } })).toBe(
      `NOT (EXISTS (${COMMENT_HOP} (((("t0_1"."body_text" COLLATE "C" IS NOT DISTINCT FROM $1)) IS NOT TRUE))))`,
    );
  });

  it.each([postgresDialect, sqliteDialect])(
    'spells the null-safe negation identically on %s',
    (dialect) => {
      const rendered = text({ comments: { every: { body: 'x' } } }, dialect);
      expect(rendered).toContain('IS NOT TRUE');
      expect(rendered).not.toContain('AND NOT');
    },
  );

  it('conjoins some, every and none as Prisma does', () => {
    const rendered = sql({ comments: { some: { body: 'a' }, none: { body: 'b' } } });
    expect(rendered.text).toBe(
      `((EXISTS (${COMMENT_HOP} (("t0_1"."body_text" COLLATE "C" IS NOT DISTINCT FROM $1)))) AND ` +
        `NOT (EXISTS (${COMMENT_HOP} (("t0_1"."body_text" COLLATE "C" IS NOT DISTINCT FROM $2)))))`,
    );
    expect(rendered.parameters).toEqual(['a', 'b']);
  });

  it('renders an empty condition object per quantifier, and never as an empty disjunction', () => {
    expect(text({ comments: { some: {} } })).toBe(`(EXISTS (${COMMENT_HOP} ((1 = 1))))`);
    expect(text({ comments: { none: {} } })).toBe(`NOT (EXISTS (${COMMENT_HOP} ((1 = 1))))`);
    expect(text({ comments: { every: {} } })).toBe(
      `NOT (EXISTS (${COMMENT_HOP} ((((1 = 1)) IS NOT TRUE))))`,
    );
    expect(text({ comments: {} })).toBe('(1 = 1)');
    expect(text({ comments: { some: { OR: [] } } })).toBe(`(EXISTS (${COMMENT_HOP} ((1 = 0))))`);
  });

  it('collates a string foreign key on both sides of a to-many correlation', () => {
    expect(sql({ posts: { some: { views: 1 } } }, postgresDialect, DATAMODEL, 'User').text).toBe(
      '(EXISTS (SELECT 1 FROM "post_rows" AS "t0_1" WHERE "t0_1"."author_ref" COLLATE "C" = "t0"."user_pk" COLLATE "C" ' +
        'AND (("t0_1"."view_count" IS NOT DISTINCT FROM $1))))',
    );
  });

  it('picks the back reference by relation name when several point at the same model', () => {
    expect(sql({ posts: { none: {} } }, postgresDialect, DATAMODEL, 'User').text).toContain(
      '"t0_1"."author_ref" COLLATE "C" = "t0"."user_pk" COLLATE "C"',
    );
  });

  it('refuses an implicit many-to-many rather than guessing a join table', () => {
    expect(() => sql({ students: { some: {} } }, postgresDialect, UNCORRELATABLE, 'Course')).toThrow(
      SqlRenderError,
    );
    expect(() => sql({ students: { some: {} } }, postgresDialect, UNCORRELATABLE, 'Course')).toThrow(
      'declares no field holding the foreign key back',
    );
  });

  it('refuses an ambiguous back reference rather than picking one of them', () => {
    expect(() => sql({ seats: { some: {} } }, postgresDialect, UNCORRELATABLE, 'Course')).toThrow(
      'declares 2 fields holding a foreign key back',
    );
  });

  it('refuses a to-many quantifier on a to-one relation', () => {
    expect(() => sql({ author: { some: { name: 'a' } } })).toThrow(
      'operator "some" filters a to-many relation, and "Post.author" is a to-one relation',
    );
  });
});

describe('hop aliasing', () => {
  it('allocates a distinct alias per level of nesting', () => {
    const rendered = text({ author: { is: { org: { is: { tier: 7 } } } } });
    expect(rendered).toBe(
      '(EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE "C" = "t0"."author_ref" COLLATE "C" AND ((EXISTS (SELECT 1 FROM "org_rows" AS "t0_2" WHERE "t0_2"."org_pk" = "t0_1"."org_ref" AND (("t0_2"."tier_value" IS NOT DISTINCT FROM $1)))))))',
    );
  });

  it('never reuses an enclosing alias for an enclosed hop', () => {
    const rendered = sql(
      { parent: { is: { parent: { is: { parent: { is: { label: 'x' } } } } } } },
      postgresDialect,
      SELF_RELATION,
      'Node',
    ).text;
    expect(rendered).toBe(
      '(EXISTS (SELECT 1 FROM "node_rows" AS "t0_1" WHERE "t0_1"."node_pk" = "t0"."parent_ref" AND ((EXISTS (SELECT 1 FROM "node_rows" AS "t0_2" WHERE "t0_2"."node_pk" = "t0_1"."parent_ref" AND ((EXISTS (SELECT 1 FROM "node_rows" AS "t0_3" WHERE "t0_3"."node_pk" = "t0_2"."parent_ref" AND (("t0_3"."label_text" COLLATE "C" IS NOT DISTINCT FROM $1))))))))))',
    );
  });

  it('never reuses an enclosing alias when the chain alternates cardinality', () => {
    expect(text({ comments: { some: { post: { author: { posts: { some: { views: 1 } } } } } } })).toBe(
      '(EXISTS (SELECT 1 FROM "comment_rows" AS "t0_1" WHERE "t0_1"."post_id" = "t0"."post_pk" AND ' +
        '((EXISTS (SELECT 1 FROM "post_rows" AS "t0_2" WHERE "t0_2"."post_pk" = "t0_1"."post_id" AND ' +
        '((EXISTS (SELECT 1 FROM "user_rows" AS "t0_3" WHERE "t0_3"."user_pk" COLLATE "C" = "t0_2"."author_ref" COLLATE "C" AND ' +
        '((EXISTS (SELECT 1 FROM "post_rows" AS "t0_4" WHERE "t0_4"."author_ref" COLLATE "C" = "t0_3"."user_pk" COLLATE "C" AND ' +
        '(("t0_4"."view_count" IS NOT DISTINCT FROM $1)))))))))))))',
    );
  });

  it('gives every hop of a six-level mixed chain an alias of its own', () => {
    const rendered = text({
      comments: {
        every: { post: { comments: { none: { post: { author: { org: { is: { tier: 1 } } } } } } } },
      },
    });
    for (const alias of ['t0_1', 't0_2', 't0_3', 't0_4', 't0_5', 't0_6']) {
      expect(rendered).toContain(`AS "${alias}"`);
    }
    expect(rendered).toContain('"t0_1"."post_id" = "t0"."post_pk"');
    expect(rendered).toContain('"t0_2"."post_pk" = "t0_1"."post_id"');
    expect(rendered).toContain('"t0_3"."post_id" = "t0_2"."post_pk"');
    expect(rendered).toContain('"t0_4"."post_pk" = "t0_3"."post_id"');
    expect(rendered).toContain('"t0_5"."user_pk" COLLATE "C" = "t0_4"."author_ref" COLLATE "C"');
    expect(rendered).toContain('"t0_6"."org_pk" = "t0_5"."org_ref"');
    expect(rendered).toContain('IS NOT TRUE');
  });

  it('keeps a hop inside a NOT inside a quantifier correlated to its own enclosing hop', () => {
    const rendered = text({ comments: { some: { NOT: { post: { author: { name: 'a' } } } } } });
    expect(rendered).toBe(
      '(EXISTS (SELECT 1 FROM "comment_rows" AS "t0_1" WHERE "t0_1"."post_id" = "t0"."post_pk" AND ' +
        '(NOT ((EXISTS (SELECT 1 FROM "post_rows" AS "t0_2" WHERE "t0_2"."post_pk" = "t0_1"."post_id" AND ' +
        '((EXISTS (SELECT 1 FROM "user_rows" AS "t0_3" WHERE "t0_3"."user_pk" COLLATE "C" = "t0_2"."author_ref" COLLATE "C" AND ' +
        '(("t0_3"."name_text" COLLATE "C" IS NOT DISTINCT FROM $1)))))))))))',
    );
  });

  it('gives sibling quantifiers at the same depth separate subqueries', () => {
    const rendered = text({
      AND: [{ comments: { some: { body: 'a' } } }, { comments: { none: { body: 'b' } } }],
    });
    expect(rendered.split('AS "t0_1"')).toHaveLength(3);
    expect(rendered).not.toContain('AS "t0_2"');
  });

  it('derives hop aliases from the root alias so they cannot collide with it', () => {
    const rendered = renderConstraintSql(
      { author: { is: { org: { is: { tier: 1 } } } } },
      { datamodel: DATAMODEL, model: 'Post', dialect: postgresDialect, absent: 'deny-all', alias: 'base' },
    );
    expect(rendered.text).toContain('AS "base_1"');
    expect(rendered.text).toContain('AS "base_2"');
    expect(rendered.text).toContain('"base"."author_ref"');
  });

  it('gives sibling hops in separate subqueries independent scopes at the same depth', () => {
    const rendered = text({ OR: [{ author: { is: { name: 'a' } } }, { reviewer: { is: { name: 'b' } } }] });
    expect(rendered).toBe(
      '((EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE "C" = "t0"."author_ref" COLLATE "C" AND (("t0_1"."name_text" COLLATE "C" IS NOT DISTINCT FROM $1)))) OR (EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE "C" = "t0"."reviewer_ref" COLLATE "C" AND (("t0_1"."name_text" COLLATE "C" IS NOT DISTINCT FROM $2)))))',
    );
  });

  it('renders the same constraint identically however many times the scope is used', () => {
    const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
    const constraint = { author: { is: { org: { is: { tier: 1 } } } } };
    const first = renderSql(renderConstraintNode(constraint, { scope, absent: 'deny-all' }), postgresDialect);
    const second = renderSql(renderConstraintNode(constraint, { scope, absent: 'deny-all' }), postgresDialect);
    expect(first).toEqual(second);
  });
});

describe('the string collation', () => {
  it('forces byte order on ordered comparisons of a string column', () => {
    expect(text({ title: { lt: 'Zulu' } }, postgresDialect)).toBe(
      '("t0"."title_text" COLLATE "C" IS NOT NULL AND "t0"."title_text" COLLATE "C" < $1)',
    );
    expect(text({ title: { lt: 'Zulu' } }, sqliteDialect)).toBe(
      '("t0"."title_text" COLLATE BINARY IS NOT NULL AND "t0"."title_text" COLLATE BINARY < ?)',
    );
  });

  it.each(['lt', 'lte', 'gt', 'gte'])('forces byte order on %s', (operator) => {
    expect(text({ title: { [operator]: 'a' } })).toContain('COLLATE "C"');
  });

  it.each(['equals', 'not'])('forces byte order on %s so a case-insensitive collation cannot widen it', (operator) => {
    expect(text({ title: { [operator]: 'a' } }, postgresDialect)).toContain('COLLATE "C"');
    expect(text({ title: { [operator]: 'a' } }, sqliteDialect)).toContain('COLLATE BINARY');
  });

  it.each(['in', 'notIn'])('forces byte order on %s', (operator) => {
    expect(text({ title: { [operator]: ['a'] } }, postgresDialect)).toBe(
      operator === 'in'
        ? '("t0"."title_text" COLLATE "C" IS NOT NULL AND "t0"."title_text" COLLATE "C" IN ($1))'
        : '("t0"."title_text" COLLATE "C" IS NULL OR "t0"."title_text" COLLATE "C" NOT IN ($1))',
    );
    expect(text({ title: { [operator]: ['a'] } }, sqliteDialect)).toBe(
      operator === 'in'
        ? '("t0"."title_text" COLLATE BINARY IS NOT NULL AND "t0"."title_text" COLLATE BINARY IN (?))'
        : '("t0"."title_text" COLLATE BINARY IS NULL OR "t0"."title_text" COLLATE BINARY NOT IN (?))',
    );
  });

  it.each(['contains', 'startsWith', 'endsWith'])(
    'forces byte order on %s wherever the engine matches through a collation',
    (operator) => {
      expect(text({ title: { [operator]: 'a' } }, postgresDialect)).toContain(
        '"t0"."title_text" COLLATE "C" LIKE $1',
      );
    },
  );

  it('forces byte order on insensitive matching too, so only ASCII case folds', () => {
    expect(text({ title: { contains: 'a', mode: 'insensitive' } }, postgresDialect)).toBe(
      `("t0"."title_text" COLLATE "C" IS NOT NULL AND "t0"."title_text" COLLATE "C" ILIKE $1 ESCAPE '\\')`,
    );
  });

  it('matches a string on SQLite through functions no collation can widen', () => {
    expect(text({ title: { contains: 'a' } }, sqliteDialect)).toBe(
      '("t0"."title_text" COLLATE BINARY IS NOT NULL AND instr("t0"."title_text" COLLATE BINARY, ?) > 0)',
    );
    expect(text({ title: { startsWith: 'a' } }, sqliteDialect)).toContain('instr(');
    expect(text({ title: { endsWith: 'a' } }, sqliteDialect)).toContain('substr(');
  });

  it('matches an enum column as text when a string operator reaches it', () => {
    expect(text({ status: { startsWith: 'DRA' } }, postgresDialect)).toBe(
      `(CAST("t0"."status_value" AS TEXT) COLLATE "C" IS NOT NULL AND CAST("t0"."status_value" AS TEXT) COLLATE "C" LIKE $1 ESCAPE '\\')`,
    );
  });

  it('forces byte order on the implicit equals shorthand', () => {
    expect(text({ title: 'a' })).toBe('("t0"."title_text" COLLATE "C" IS NOT DISTINCT FROM $1)');
  });

  it('forces byte order on a null check of a string column', () => {
    expect(text({ title: { equals: null } })).toBe('("t0"."title_text" COLLATE "C" IS NULL)');
    expect(text({ title: { not: null } })).toBe('("t0"."title_text" COLLATE "C" IS NOT NULL)');
  });

  it('leaves non-string columns uncollated, since a collation on them is a type error', () => {
    expect(text({ views: { lt: 1 } })).not.toContain('COLLATE');
    expect(text({ createdAt: { lt: new Date(0) } })).not.toContain('COLLATE');
    expect(text({ published: true })).not.toContain('COLLATE');
  });

  it('compares an enum by its label in byte order, not by its declaration order', () => {
    expect(text({ status: { gt: 'DRAFT' } }, postgresDialect)).toBe(
      '(CAST("t0"."status_value" AS TEXT) COLLATE "C" IS NOT NULL AND CAST("t0"."status_value" AS TEXT) COLLATE "C" > $1)',
    );
    expect(text({ status: { gt: 'DRAFT' } }, sqliteDialect)).toBe(
      '(CAST("t0"."status_value" AS TEXT) COLLATE BINARY IS NOT NULL AND CAST("t0"."status_value" AS TEXT) COLLATE BINARY > ?)',
    );
  });

  it('compares an enum by its label for equality too', () => {
    expect(text({ status: 'DRAFT' })).toBe(
      '(CAST("t0"."status_value" AS TEXT) COLLATE "C" IS NOT DISTINCT FROM $1)',
    );
    expect(text({ status: { in: ['DRAFT', 'LIVE'] } })).toBe(
      '(CAST("t0"."status_value" AS TEXT) COLLATE "C" IS NOT NULL AND CAST("t0"."status_value" AS TEXT) COLLATE "C" IN ($1, $2))',
    );
  });

  it('collates a string foreign key on both sides of the correlation', () => {
    expect(text({ author: { is: { name: 'a' } } })).toContain(
      '"t0_1"."user_pk" COLLATE "C" = "t0"."author_ref" COLLATE "C"',
    );
  });

  it('leaves a non-string foreign key uncollated', () => {
    const rendered = text({ author: { is: { org: { is: { tier: 1 } } } } });
    expect(rendered).toContain('"t0_2"."org_pk" = "t0_1"."org_ref"');
  });
});

describe('the empty disjunction', () => {
  it('renders as literal false at the top level', () => {
    expect(text({ OR: [] })).toBe('(1 = 0)');
  });

  it.each([
    [{ AND: [{ OR: [] }] }, '((1 = 0))'],
    [{ AND: [{ AND: [{ OR: [] }] }] }, '(((1 = 0)))'],
    [{ OR: [{ OR: [] }] }, '((1 = 0))'],
    [{ AND: [{ OR: [] }, { views: 1 }] }, '((1 = 0) AND ("t0"."view_count" IS NOT DISTINCT FROM $1))'],
  ])('stays false when nested (%#)', (constraint, expected) => {
    const rendered = text(constraint);
    expect(rendered).toBe(expected);
    expect(rendered).not.toContain('1 = 1');
  });

  it('stays false inside a relation subquery', () => {
    expect(text({ author: { is: { OR: [] } } })).toBe(
      '(EXISTS (SELECT 1 FROM "user_rows" AS "t0_1" WHERE "t0_1"."user_pk" COLLATE "C" = "t0"."author_ref" COLLATE "C" AND ((1 = 0))))',
    );
  });

  it('is a deny-all that NOT can invert, and nothing else silently can', () => {
    expect(text({ NOT: [{ OR: [] }] })).toBe('NOT ((1 = 0))');
  });

  it('does not confuse an empty conjunction with an empty disjunction', () => {
    expect(text({ AND: [] })).toBe('(1 = 1)');
    expect(text({ OR: [] })).toBe('(1 = 0)');
  });
});

describe('the absent constraint', () => {
  it('refuses to guess what a null constraint means', () => {
    expect(() =>
      renderConstraintSql(null, {
        datamodel: DATAMODEL,
        model: 'Post',
        dialect: postgresDialect,
        absent: undefined as never,
      }),
    ).toThrow(SqlRenderError);
    expect(() =>
      renderConstraintSql(null, {
        datamodel: DATAMODEL,
        model: 'Post',
        dialect: postgresDialect,
        absent: undefined as never,
      }),
    ).toThrow('grant-all');
  });

  it('refuses an absent meaning it does not recognise', () => {
    expect(() =>
      renderConstraintSql(null, {
        datamodel: DATAMODEL,
        model: 'Post',
        dialect: postgresDialect,
        absent: 'maybe' as never,
      }),
    ).toThrow(SqlRenderError);
  });

  it('renders a null constraint as the meaning the caller declared', () => {
    expect(sql(null).text).toBe('(1 = 0)');
    expect(
      renderConstraintSql(null, {
        datamodel: DATAMODEL,
        model: 'Post',
        dialect: postgresDialect,
        absent: 'grant-all',
      }).text,
    ).toBe('(1 = 1)');
  });

  it('treats undefined exactly as null', () => {
    expect(sql(undefined).text).toBe('(1 = 0)');
  });

  it('still renders an explicitly empty condition object as an unconditional grant', () => {
    expect(sql({}).text).toBe('(1 = 1)');
  });

  it('refuses a constraint that is not a condition object', () => {
    expect(() => sql(false)).toThrow('plain object');
    expect(() => sql('everything')).toThrow('plain object');
    expect(() => sql([{ views: 1 }])).toThrow('plain object');
  });
});

describe('refusals', () => {
  it('refuses a datamodel whose model carries no physical table name', () => {
    const stale: PolicyDatamodel = { models: [{ name: 'M', fields: [scalar('f', 'Int', 'f')] }] };
    expect(() => sql({ f: 1 }, postgresDialect, stale, 'M')).toThrow(SqlRenderError);
    expect(() => sql({ f: 1 }, postgresDialect, stale, 'M')).toThrow('regenerate the golem client');
  });

  it('refuses a datamodel whose column carries no physical name', () => {
    const stale: PolicyDatamodel = {
      models: [{ name: 'M', dbName: 'm', fields: [{ name: 'f', kind: 'scalar', type: 'Int', isList: false }] }],
    };
    expect(() => sql({ f: 1 }, postgresDialect, stale, 'M')).toThrow('regenerate the golem client');
  });

  it('refuses a stale model even when the constraint never mentions the unnamed column', () => {
    const stale: PolicyDatamodel = {
      models: [
        {
          name: 'M',
          dbName: 'm',
          fields: [scalar('named', 'Int', 'named_col'), { name: 'f', kind: 'scalar', type: 'Int', isList: false }],
        },
      ],
    };
    expect(() => sql({ named: 1 }, postgresDialect, stale, 'M')).toThrow('regenerate the golem client');
  });

  it('refuses a raw DMMF model whose physical table name is null rather than absent', () => {
    const raw: PolicyDatamodel = {
      models: [{ name: 'M', dbName: null as unknown as undefined, fields: [scalar('f', 'Int', 'f')] }],
    };
    expect(() => sql({ f: 1 }, postgresDialect, raw, 'M')).toThrow(SqlRenderError);
    expect(() => sql({ f: 1 }, postgresDialect, raw, 'M')).toThrow('regenerate the golem client');
  });

  it('refuses a raw DMMF column whose physical name is null rather than absent', () => {
    const raw: PolicyDatamodel = {
      models: [
        {
          name: 'M',
          dbName: 'm',
          fields: [{ name: 'f', dbName: null as unknown as undefined, kind: 'scalar', type: 'Int', isList: false }],
        },
      ],
    };
    expect(() => sql({ f: 1 }, postgresDialect, raw, 'M')).toThrow(SqlRenderError);
    expect(() => sql({ f: 1 }, postgresDialect, raw, 'M')).toThrow('regenerate the golem client');
  });

  it('refuses a raw DMMF relation target whose physical names are null', () => {
    const raw: PolicyDatamodel = {
      models: [
        {
          name: 'A',
          dbName: 'a',
          fields: [
            scalar('bId', 'Int', 'b_ref'),
            {
              name: 'b',
              kind: 'object',
              type: 'B',
              isList: false,
              relationFromFields: ['bId'],
              relationToFields: ['id'],
            },
          ],
        },
        { name: 'B', dbName: null as unknown as undefined, fields: [scalar('id', 'Int', 'id')] },
      ],
    };
    expect(() => sql({ b: { is: { id: 1 } } }, postgresDialect, raw, 'A')).toThrow(SqlRenderError);
    expect(() => sql({ b: { is: { id: 1 } } }, postgresDialect, raw, 'A')).toThrow(
      'regenerate the golem client',
    );
  });

  it('refuses a stale related model only reachable through a hop', () => {
    const stale: PolicyDatamodel = {
      models: [
        {
          name: 'A',
          dbName: 'a',
          fields: [
            scalar('bId', 'Int', 'b_ref'),
            {
              name: 'b',
              dbName: 'b',
              kind: 'object',
              type: 'B',
              isList: false,
              relationFromFields: ['bId'],
              relationToFields: ['id'],
            },
          ],
        },
        { name: 'B', fields: [scalar('id', 'Int', 'id')] },
      ],
    };
    expect(() => sql({ b: { is: { id: 1 } } }, postgresDialect, stale, 'A')).toThrow('regenerate the golem client');
  });

  it('never falls back to the Prisma name when the physical name is missing', () => {
    const stale: PolicyDatamodel = {
      models: [{ name: 'M', dbName: 'm', fields: [{ name: 'f', kind: 'scalar', type: 'Int', isList: false }] }],
    };
    let rendered: string | null = null;
    try {
      rendered = sql({ f: 1 }, postgresDialect, stale, 'M').text;
    } catch {
      rendered = null;
    }
    expect(rendered).toBeNull();
  });

  it('refuses an unknown model', () => {
    expect(() => createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Nope' })).toThrow(
      'model "Nope" is not present in the datamodel',
    );
  });

  it('refuses an unknown field', () => {
    expect(() => sql({ nope: 1 })).toThrow('field "Post.nope" is not present in the datamodel');
  });

  it('refuses a relation compared as a column', () => {
    expect(() => sql({ author: 'u1' })).toThrow(
      'relation "Post.author" cannot be compared to string',
    );
  });

  it('refuses a list column', () => {
    expect(() => sql({ tags: 'x' })).toThrow('list column "Post.tags" cannot be compared to string');
  });

  it('refuses "is" on a scalar column', () => {
    expect(() => sql({ views: { is: { x: 1 } } })).toThrow(
      'operator "is" filters a to-one relation, and "Post.views" is a scalar field',
    );
  });

  it('refuses "is" on a to-many relation', () => {
    expect(() => sql({ comments: { is: { body: 'x' } } })).toThrow(
      'operator "is" filters a to-one relation, and "Post.comments" is a to-many relation',
    );
  });

  it('refuses "is" on a relation that does not hold the foreign key', () => {
    expect(() => sql({ profile: { is: { id: 1 } } }, postgresDialect, DATAMODEL, 'User')).toThrow(
      'does not hold the foreign key on this side',
    );
  });

  it('refuses a relation whose target model is missing from the datamodel', () => {
    expect(() => sql({ ghost: { is: { id: 1 } } })).toThrow('model "Ghost" is not present in the datamodel');
  });

  it('refuses a relation whose foreign key and referenced columns do not line up', () => {
    expect(() => sql({ lopsided: { is: { name: 'a' } } })).toThrow('against 1 referenced column');
  });

  it('resolves a nested field against the related model, not the root model', () => {
    expect(() => sql({ author: { is: { views: 1 } } })).toThrow('field "User.views" is not present in the datamodel');
    expect(() => sql({ author: { is: { org: { is: { name: 'x' } } } } })).toThrow(
      'field "Org.name" is not present in the datamodel',
    );
  });

  it('refuses an empty root alias', () => {
    expect(() => createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post', alias: '' })).toThrow(
      'non-empty string',
    );
  });

  it('refuses every unsupported operator that reaches the scope', () => {
    expect(() => sql({ title: { search: 'x' } })).toThrow('not supported');
  });

  it('reports a shape the datamodel already contradicts as UnsupportedConditionError', () => {
    expect(() => sql({ comments: { is: { body: 'x' } } })).toThrow(UnsupportedConditionError);
    expect(() => sql({ nope: 1 })).toThrow(UnsupportedConditionError);
  });

  it('reports a shape only the physical layer can contradict as SqlRenderError', () => {
    expect(() => sql({ profile: { is: { id: 1 } } }, postgresDialect, DATAMODEL, 'User')).toThrow(
      SqlRenderError,
    );
    expect(() => sql({ ghost: { is: { id: 1 } } })).toThrow(SqlRenderError);
  });
});

describe('renderConstraintNode', () => {
  it('composes with a caller-built scope', () => {
    const scope = createDatamodelSqlScope({ datamodel: SELF_RELATION, model: 'Node', alias: 'n' });
    const node = renderConstraintNode({ label: 'x' }, { scope, absent: 'deny-all' });
    expect(renderSql(node, sqliteDialect)).toEqual({
      text: '("n"."label_text" COLLATE BINARY IS ?)',
      parameters: ['x'],
    });
  });

  it('refuses an absent constraint without a declared meaning', () => {
    const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
    expect(() => renderConstraintNode(null, { scope, absent: undefined as never })).toThrow(SqlRenderError);
  });

  it('refuses a scope that carries no datamodel', () => {
    const anonymous: SqlScope = {
      column: (field) => sqlIdentifier(['t0', field]),
      relation: () => undefined,
    };
    expect(() =>
      renderConstraintNode({ views: { contains: '2' } }, { scope: anonymous as never, absent: 'deny-all' }),
    ).toThrow(SqlRenderError);
    expect(() => renderConstraintNode(null, { scope: anonymous as never, absent: 'grant-all' })).toThrow(
      SqlRenderError,
    );
  });

  it('refuses a scope whose datamodel was replaced by a hand-built one', () => {
    const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
    const stripped = { ...scope, datamodel: undefined } as unknown as typeof scope;
    expect(() => renderConstraintNode({ title: 'x' }, { scope: stripped, absent: 'deny-all' })).toThrow(
      SqlRenderError,
    );
  });

  it('refuses a string operator on a non-String column through every render entry point', () => {
    const scope = createDatamodelSqlScope({ datamodel: DATAMODEL, model: 'Post' });
    expect(() => renderConstraintNode({ views: { contains: '2' } }, { scope, absent: 'deny-all' })).toThrow(
      UnsupportedConditionError,
    );
    expect(() =>
      renderConstraintSql(
        { views: { contains: '2' } },
        { datamodel: DATAMODEL, model: 'Post', dialect: sqliteDialect, absent: 'deny-all' },
      ),
    ).toThrow(UnsupportedConditionError);
  });
});
