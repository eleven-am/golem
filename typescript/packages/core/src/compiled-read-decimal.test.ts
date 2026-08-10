jest.mock('@prisma/client/runtime/client', () => ({}));

import { planCompiledRead } from './compiled-read';
import { prismaDecimal } from './compiled-read-decode';
import { DatamodelModel } from './datamodel';
import { buildModelMetadata } from './model-meta';
import { field } from './testing';

const models: readonly DatamodelModel[] = [
  {
    name: 'User',
    dbName: 'users',
    fields: [
      field({ name: 'id', dbName: 'id', type: 'Int', isId: true }),
      field({
        name: 'assets',
        kind: 'object',
        type: 'Asset',
        isList: true,
        isRequired: false,
        relationName: 'AssetToUser',
      }),
    ],
  },
  {
    name: 'Asset',
    dbName: 'assets',
    indexes: [{ kind: 'normal', fields: ['ownerId'] }],
    fields: [
      field({ name: 'id', dbName: 'id', type: 'Int', isId: true }),
      field({ name: 'cost', dbName: 'cost', type: 'Decimal', isRequired: false }),
      field({ name: 'ownerId', dbName: 'owner_id', type: 'Int' }),
      field({
        name: 'owner',
        kind: 'object',
        type: 'User',
        relationName: 'AssetToUser',
        relationFromFields: ['ownerId'],
        relationToFields: ['id'],
      }),
    ],
  },
];

const metadata = buildModelMetadata(models);

function plan(select: Record<string, unknown>) {
  return planCompiledRead({
    model: models[0],
    models,
    metadata,
    provider: 'sqlite',
    prepared: { select, toOneChecks: [], maskChecks: [], injected: [] },
  });
}

describe('a Prisma runtime that hands golem no Decimal class', () => {
  it('finds no Decimal class to decode one into', () => {
    expect(prismaDecimal()).toBeNull();
  });

  it('hands a read carrying a Decimal through a relation back to Prisma', async () => {
    expect(await plan({ id: true, assets: { select: { id: true, cost: true } } })).toMatchObject({
      kind: 'fallback',
      reason: 'decoder',
    });
  });

  it('still compiles a relation carrying no Decimal at all', async () => {
    expect(await plan({ id: true, assets: { select: { id: true } } })).toMatchObject({
      kind: 'compiled',
    });
  });
});
