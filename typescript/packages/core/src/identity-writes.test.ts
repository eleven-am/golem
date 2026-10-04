import { GolemValidationError } from './errors';
import { refuseIdentityChanges } from './identity-writes';
import { buildModelMetadata } from './model-meta';
import { field } from './testing';

const metadata = buildModelMetadata([
  {
    name: 'User',
    fields: [
      field({ name: 'id', type: 'String', isId: true }),
      field({ name: 'name', type: 'String' }),
      field({ name: 'profile', type: 'Profile', kind: 'object', isRequired: false, relationName: 'ProfileToUser' }),
    ],
  },
  {
    name: 'Profile',
    fields: [
      field({ name: 'userId', type: 'String', isId: true }),
      field({ name: 'bio', type: 'String' }),
      field({
        name: 'user', type: 'User', kind: 'object', relationName: 'ProfileToUser',
        relationFromFields: ['userId'], relationToFields: ['id'], relationOnDelete: 'Cascade',
      }),
    ],
  },
]);

const refused = new GolemValidationError('Profile.user identifies the row and cannot be changed by an update');

describe('identity changes through a relation that holds the identity', () => {
  it('accepts an update of the related row that leaves its referenced key alone', () => {
    expect(() => refuseIdentityChanges(metadata, 'Profile', { user: { update: { name: 'New' } } })).not.toThrow();
    expect(() => refuseIdentityChanges(metadata, 'Profile', { user: { update: { data: { name: 'New' } } } })).not.toThrow();
  });

  it('refuses an update of the related row that rewrites the key the identity references', () => {
    expect(() => refuseIdentityChanges(metadata, 'Profile', { user: { update: { id: 'x' } } })).toThrow(refused);
  });

  it.each([
    ['connect', { connect: { id: 'u2' } }],
    ['disconnect', { disconnect: true }],
    ['create', { create: { id: 'u2', name: 'n' } }],
    ['connectOrCreate', { connectOrCreate: { where: { id: 'u2' }, create: { id: 'u2', name: 'n' } } }],
    ['delete', { delete: true }],
    ['upsert', { upsert: { create: { id: 'u2', name: 'n' }, update: { name: 'n' } } }],
  ])('refuses %s, which can reassign or clear the relation', (_kind, envelope) => {
    expect(() => refuseIdentityChanges(metadata, 'Profile', { user: envelope })).toThrow(refused);
  });

  it('still refuses writing the identity scalar directly', () => {
    expect(() => refuseIdentityChanges(metadata, 'Profile', { userId: 'u2' }))
      .toThrow(new GolemValidationError('Profile.userId identifies the row and cannot be changed by an update'));
  });
});

describe('identity changes cascading through referenced keys', () => {
  const chain = buildModelMetadata([
    {
      name: 'Org',
      fields: [
        field({ name: 'code', type: 'String', isId: true }),
        field({ name: 'accounts', type: 'Account', kind: 'object', isList: true, relationName: 'AccountToOrg' }),
      ],
    },
    {
      name: 'Account',
      fields: [
        field({ name: 'code', type: 'String', isId: true }),
        field({ name: 'label', type: 'String' }),
        field({ name: 'org', type: 'Org', kind: 'object', relationName: 'AccountToOrg', relationFromFields: ['code'], relationToFields: ['code'] }),
        field({ name: 'users', type: 'User', kind: 'object', isList: true, relationName: 'AccountToUser' }),
      ],
    },
    {
      name: 'User',
      fields: [
        field({ name: 'id', type: 'String', isId: true }),
        field({ name: 'name', type: 'String' }),
        field({ name: 'code', type: 'String', isUnique: true }),
        field({ name: 'account', type: 'Account', kind: 'object', relationName: 'AccountToUser', relationFromFields: ['code'], relationToFields: ['code'] }),
        field({ name: 'profile', type: 'Profile', kind: 'object', isRequired: false, relationName: 'ProfileToUser' }),
      ],
    },
    {
      name: 'Profile',
      fields: [
        field({ name: 'userCode', type: 'String', isId: true }),
        field({ name: 'bio', type: 'String' }),
        field({ name: 'user', type: 'User', kind: 'object', relationName: 'ProfileToUser', relationFromFields: ['userCode'], relationToFields: ['code'] }),
      ],
    },
  ]);
  const through = new GolemValidationError('Profile.user identifies the row and cannot be changed by an update');

  it('refuses a related row whose referenced key a connect two hops away rewrites', () => {
    expect(() => refuseIdentityChanges(chain, 'Profile', { user: { update: { account: { connect: { code: 'new' } } } } }))
      .toThrow(through);
  });

  it('refuses a rewrite three hops away', () => {
    expect(() => refuseIdentityChanges(chain, 'Profile', {
      user: { update: { account: { update: { org: { connect: { code: 'other' } } } } } },
    })).toThrow(through);
  });

  it('accepts updates of unrelated fields along the chain', () => {
    expect(() => refuseIdentityChanges(chain, 'Profile', {
      bio: 'b',
      user: { update: { name: 'n', account: { update: { label: 'l' } } } },
    })).not.toThrow();
  });

  it('terminates on a self-relation, refusing only a rewrite of the referenced key', () => {
    const nodes = buildModelMetadata([{
      name: 'Node',
      fields: [
        field({ name: 'parentCode', type: 'String' }),
        field({ name: 'slot', type: 'Int' }),
        field({ name: 'code', type: 'String', isUnique: true }),
        field({ name: 'name', type: 'String' }),
        field({ name: 'parent', type: 'Node', kind: 'object', relationName: 'Tree', relationFromFields: ['parentCode'], relationToFields: ['code'] }),
        field({ name: 'children', type: 'Node', kind: 'object', isList: true, relationName: 'Tree' }),
      ],
      primaryKey: { fields: ['parentCode', 'slot'] },
    }]);
    const deep = { parent: { update: { name: 'a', parent: { update: { name: 'b', parent: { update: { name: 'c' } } } } } } };
    expect(() => refuseIdentityChanges(nodes, 'Node', deep)).not.toThrow();
    expect(() => refuseIdentityChanges(nodes, 'Node', { parent: { update: { code: 'z' } } }))
      .toThrow(new GolemValidationError('Node.parent identifies the row and cannot be changed by an update'));
  });
});
