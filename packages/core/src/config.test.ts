import { printSchema } from 'graphql';
import { DatamodelDocument } from './datamodel';
import { buildGolemSchema } from './schema';
import { field } from './testing';

const datamodel: DatamodelDocument<{ User: 'id' | 'email' | 'name' | 'apiKey' }> = {
  models: [
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true, hasDefaultValue: true }),
        field({ name: 'email', type: 'String', isUnique: true }),
        field({ name: 'name', type: 'String', isRequired: false }),
        field({ name: 'apiKey', type: 'String', isRequired: false }),
      ],
    },
  ],
  enums: [],
};

function fakeClient() {
  return {
    user: {
      findMany: jest.fn().mockResolvedValue([]),
      findUnique: jest.fn().mockResolvedValue(null),
      create: jest.fn(),
      update: jest.fn(),
      updateMany: jest.fn(),
      delete: jest.fn(),
      deleteMany: jest.fn(),
    },
  };
}

describe('model configuration', () => {
  it('removes disabled operations from the schema entirely', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { operations: ['findOne', 'findMany', 'create'] } },
    });
    const sdl = printSchema(schema);
    expect(sdl).toContain('createUser');
    expect(sdl).not.toContain('updateUser');
    expect(sdl).not.toContain('deleteUser');
    expect(sdl).not.toContain('updateManyUsers');
    expect(sdl).not.toContain('deleteManyUsers');
  });

  it('applies default operations when the model has no override', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      defaults: { operations: ['findOne', 'findMany'] },
    });
    const sdl = printSchema(schema);
    expect(sdl).not.toContain('type Mutation');
    expect(sdl).toContain('user(where');
  });

  it('hides hidden fields from every schema surface', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { hidden: ['apiKey'] } },
    });
    const sdl = printSchema(schema);
    expect(sdl).not.toContain('apiKey');
  });

  it('excludes immutable fields from update inputs but keeps them in create', () => {
    const schema = buildGolemSchema({
      datamodel,
      client: fakeClient(),
      models: { User: { immutable: ['email'] } },
    });
    const sdl = printSchema(schema);
    const createInput = sdl.slice(
      sdl.indexOf('input UserCreateInput'),
      sdl.indexOf('}', sdl.indexOf('input UserCreateInput')),
    );
    const updateInput = sdl.slice(
      sdl.indexOf('input UserUpdateInput'),
      sdl.indexOf('}', sdl.indexOf('input UserUpdateInput')),
    );
    const updateManyInput = sdl.slice(
      sdl.indexOf('input UserUpdateManyInput'),
      sdl.indexOf('}', sdl.indexOf('input UserUpdateManyInput')),
    );
    expect(createInput).toContain('email: String!');
    expect(updateInput).not.toContain('email');
    expect(updateManyInput).not.toContain('email');
  });

  it('rejects hiding the primary key', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { User: { hidden: ['id' as 'apiKey'] } },
      }),
    ).toThrow('Cannot hide primary key User.id');
  });

  it('rejects unknown field names in hidden and immutable', () => {
    expect(() =>
      buildGolemSchema({
        datamodel,
        client: fakeClient(),
        models: { User: { hidden: ['ghost' as 'apiKey'] } },
      }),
    ).toThrow('Unknown field ghost in configuration for model User');
  });
});
