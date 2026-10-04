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
