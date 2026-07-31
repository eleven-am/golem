import type { DMMF } from '@prisma/generator-helper';
import { emitDatamodelModule } from './emit';

describe('the emitted datasource provider', () => {
  const empty = { models: [], enums: [], types: [], indexes: [] } as unknown as DMMF.Datamodel;

  it('carries the provider so the scoped root knows which dialect to render', () => {
    expect(emitDatamodelModule(empty, 'postgresql')).toContain('"provider": "postgresql"');
  });

  it('omits the provider rather than guessing one when the generator did not supply it', () => {
    expect(emitDatamodelModule(empty)).not.toContain('"provider"');
  });
});

function scalar(name: string): DMMF.Field {
  return {
    name,
    kind: 'scalar',
    type: 'String',
    isList: false,
    isRequired: true,
    isUnique: false,
    isId: false,
    hasDefaultValue: false,
    isReadOnly: false,
    isGenerated: false,
    isUpdatedAt: false,
  } as DMMF.Field;
}

function datamodel(models: DMMF.Model[], indexes: DMMF.Index[] = []): DMMF.Datamodel {
  return { models, enums: [], types: [], indexes } as unknown as DMMF.Datamodel;
}

function index(
  model: string,
  type: DMMF.IndexType,
  fields: string[],
  extra: { name?: string; dbName?: string } = {},
): DMMF.Index {
  return {
    model,
    type,
    isDefinedOnField: fields.length === 1,
    ...extra,
    fields: fields.map((name) => ({ name })),
  } as unknown as DMMF.Index;
}

function model(
  name: string,
  fields: DMMF.Field[],
  primaryKey: DMMF.Model['primaryKey'],
  uniqueIndexes: Array<{ name: string | null; fields: string[] }> = [],
): DMMF.Model {
  return {
    name,
    dbName: null,
    fields,
    primaryKey,
    uniqueFields: [],
    uniqueIndexes,
  } as unknown as DMMF.Model;
}

function parseEmitted(output: string): {
  models: Array<{
    name: string;
    dbName?: string;
    fields: Array<{ name: string; dbName?: string }>;
    primaryKey?: { name?: string; fields: string[] };
    uniqueIndexes?: Array<{ name?: string; fields: string[] }>;
    indexes?: Array<{ kind: string; name?: string; dbName?: string; fields: string[] }>;
  }>;
} {
  const marker = 'export const datamodel = ';
  const start = output.indexOf(marker) + marker.length;
  const end = output.indexOf(' as const;', start);
  return JSON.parse(output.slice(start, end));
}

describe('emitDatamodelModule composite primary keys', () => {
  it('emits an unnamed composite primary key as its field list', () => {
    const output = emitDatamodelModule(
      datamodel([
        model('PostTag', [scalar('postId'), scalar('tagId')], { name: null, fields: ['postId', 'tagId'] }),
      ]),
    );
    const parsed = parseEmitted(output);
    expect(parsed.models[0].primaryKey).toEqual({ fields: ['postId', 'tagId'] });
  });

  it('preserves a named composite primary key', () => {
    const output = emitDatamodelModule(
      datamodel([
        model('Membership', [scalar('userId'), scalar('orgId')], { name: 'membership', fields: ['userId', 'orgId'] }),
      ]),
    );
    const parsed = parseEmitted(output);
    expect(parsed.models[0].primaryKey).toEqual({ name: 'membership', fields: ['userId', 'orgId'] });
  });

  it('omits primaryKey for single-field id models', () => {
    const idField = { ...scalar('id'), isId: true } as DMMF.Field;
    const output = emitDatamodelModule(datamodel([model('User', [idField, scalar('email')], null)]));
    const parsed = parseEmitted(output);
    expect(parsed.models[0].primaryKey).toBeUndefined();
  });
});

describe('emitDatamodelModule compound unique indexes', () => {
  const idField = { ...scalar('id'), isId: true } as DMMF.Field;

  it('emits an unnamed compound unique index as its field list', () => {
    const output = emitDatamodelModule(
      datamodel([
        model('Branch', [idField, scalar('authorId'), scalar('name')], null, [
          { name: null, fields: ['authorId', 'name'] },
        ]),
      ]),
    );
    const parsed = parseEmitted(output);
    expect(parsed.models[0].uniqueIndexes).toEqual([{ fields: ['authorId', 'name'] }]);
  });

  it('preserves a named compound unique index', () => {
    const output = emitDatamodelModule(
      datamodel([
        model('Branch', [idField, scalar('authorId'), scalar('name')], null, [
          { name: 'authorNameKey', fields: ['authorId', 'name'] },
        ]),
      ]),
    );
    const parsed = parseEmitted(output);
    expect(parsed.models[0].uniqueIndexes).toEqual([
      { name: 'authorNameKey', fields: ['authorId', 'name'] },
    ]);
  });

  it('omits single-field unique indexes and empty uniqueIndexes entirely', () => {
    const output = emitDatamodelModule(
      datamodel([
        model('User', [idField, scalar('email')], null, [{ name: null, fields: ['email'] }]),
      ]),
    );
    const parsed = parseEmitted(output);
    expect(parsed.models[0].uniqueIndexes).toBeUndefined();
  });
});

describe('emitDatamodelModule declared indexes', () => {
  const idField = { ...scalar('id'), isId: true } as DMMF.Field;

  function leads(
    emitted: { indexes?: Array<{ kind: string; fields: string[] }> },
    field: string,
  ): boolean {
    return (emitted.indexes ?? []).some(
      (entry) => entry.kind !== 'fulltext' && entry.fields[0] === field,
    );
  }

  it('emits a single-column @@index and answers the leading-column question for it', () => {
    const parsed = parseEmitted(
      emitDatamodelModule(
        datamodel(
          [model('Article', [idField, scalar('a')], null)],
          [index('Article', 'id', ['id']), index('Article', 'normal', ['a'])],
        ),
      ),
    );

    expect(parsed.models[0].indexes).toEqual([
      { kind: 'id', fields: ['id'] },
      { kind: 'normal', fields: ['a'] },
    ]);
    expect(leads(parsed.models[0], 'a')).toBe(true);
  });

  it('treats only the leading column of a composite @@index as indexed', () => {
    const parsed = parseEmitted(
      emitDatamodelModule(
        datamodel(
          [model('Article', [idField, scalar('b'), scalar('c')], null)],
          [index('Article', 'id', ['id']), index('Article', 'normal', ['b', 'c'])],
        ),
      ),
    );

    expect(parsed.models[0].indexes).toContainEqual({ kind: 'normal', fields: ['b', 'c'] });
    expect(leads(parsed.models[0], 'b')).toBe(true);
    expect(leads(parsed.models[0], 'c')).toBe(false);
  });

  it('emits the index a field-level @unique implies', () => {
    const parsed = parseEmitted(
      emitDatamodelModule(
        datamodel(
          [model('Article', [idField, { ...scalar('slug'), isUnique: true } as DMMF.Field], null)],
          [index('Article', 'id', ['id']), index('Article', 'unique', ['slug'])],
        ),
      ),
    );

    expect(parsed.models[0].indexes).toContainEqual({ kind: 'unique', fields: ['slug'] });
    expect(leads(parsed.models[0], 'slug')).toBe(true);
  });

  it('emits a compound @@unique with both its client name and its physical name', () => {
    const parsed = parseEmitted(
      emitDatamodelModule(
        datamodel(
          [model('Naming', [idField, scalar('c'), scalar('d')], null)],
          [
            index('Naming', 'id', ['id']),
            index('Naming', 'unique', ['c', 'd'], { name: 'clientName', dbName: 'db_name_here' }),
          ],
        ),
      ),
    );

    expect(parsed.models[0].indexes).toContainEqual({
      kind: 'unique',
      name: 'clientName',
      dbName: 'db_name_here',
      fields: ['c', 'd'],
    });
    expect(leads(parsed.models[0], 'c')).toBe(true);
    expect(leads(parsed.models[0], 'd')).toBe(false);
  });

  it('emits a compound primary key as an index led by its first column', () => {
    const parsed = parseEmitted(
      emitDatamodelModule(
        datamodel(
          [
            model('Membership', [scalar('userId'), scalar('orgId')], {
              name: null,
              fields: ['userId', 'orgId'],
            }),
          ],
          [index('Membership', 'id', ['userId', 'orgId'])],
        ),
      ),
    );

    expect(parsed.models[0].indexes).toEqual([{ kind: 'id', fields: ['userId', 'orgId'] }]);
    expect(leads(parsed.models[0], 'userId')).toBe(true);
    expect(leads(parsed.models[0], 'orgId')).toBe(false);
  });

  it('keeps each index against the model that declared it', () => {
    const parsed = parseEmitted(
      emitDatamodelModule(
        datamodel(
          [
            model('Article', [idField, scalar('a')], null),
            model('Rel', [idField, scalar('ownerId')], null),
          ],
          [
            index('Article', 'id', ['id']),
            index('Article', 'normal', ['a']),
            index('Rel', 'id', ['id']),
          ],
        ),
      ),
    );

    expect(parsed.models[0].indexes).toEqual([
      { kind: 'id', fields: ['id'] },
      { kind: 'normal', fields: ['a'] },
    ]);
    expect(parsed.models[1].indexes).toEqual([{ kind: 'id', fields: ['id'] }]);
    expect(leads(parsed.models[1], 'ownerId')).toBe(false);
  });

  it('omits indexes entirely for a model that declares none', () => {
    const parsed = parseEmitted(
      emitDatamodelModule(datamodel([model('Bare', [scalar('note')], null)])),
    );

    expect(parsed.models[0].indexes).toBeUndefined();
    expect(leads(parsed.models[0], 'note')).toBe(false);
  });
});

describe('emitDatamodelModule physical names', () => {
  it('emits the mapped physical name for a model and its fields', () => {
    const mapped = {
      name: 'User',
      dbName: 'users',
      fields: [
        { ...scalar('id'), isId: true, dbName: null },
        { ...scalar('createdAt'), dbName: 'created_at' },
      ],
      primaryKey: null,
      uniqueFields: [],
      uniqueIndexes: [],
    } as unknown as DMMF.Model;

    const parsed = parseEmitted(emitDatamodelModule(datamodel([mapped])));

    expect(parsed.models[0].dbName).toBe('users');
    expect(parsed.models[0].fields).toEqual([
      expect.objectContaining({ name: 'id', dbName: 'id' }),
      expect.objectContaining({ name: 'createdAt', dbName: 'created_at' }),
    ]);
  });

  it('falls back to the Prisma name when no mapping is present', () => {
    const parsed = parseEmitted(
      emitDatamodelModule(datamodel([model('Post', [scalar('id'), scalar('title')], null)])),
    );

    expect(parsed.models[0].dbName).toBe('Post');
    expect(parsed.models[0].fields.map((field) => field.dbName)).toEqual(['id', 'title']);
  });
});

describe('emitDatamodelModule extension helpers', () => {
  it('registers the schema with the package so decorators and hook payloads are typed', () => {
    const output = emitDatamodelModule(
      datamodel([model('User', [scalar('id'), scalar('email')], null)]),
    );

    expect(output).toContain('declare global {');
    expect(output).toContain('interface GolemRegister {');
    expect(output).toContain('models: GolemModels;');
    expect(output).toContain('types: GolemTypes;');
    expect(output).toContain("import type { GolemTypes } from './types';");
  });

  it('no longer re-exports a generated decorator', () => {
    const output = emitDatamodelModule(
      datamodel([model('User', [scalar('id'), scalar('email')], null)]),
    );

    expect(output).not.toContain('createComputedFieldDecorator');
    expect(output).not.toContain('export const ComputedField');
  });
});
