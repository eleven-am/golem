import type { DatamodelModel } from '../../src/datamodel';
import { field } from '../../src/testing';

export const upsertGuardModel: DatamodelModel = {
  name: 'GolemUpsertGuard',
  dbName: '_golem_upsert_guard',
  fields: [
    field({ name: 'stripe', type: 'Int', isId: true }),
    field({ name: 'seq', type: 'BigInt', hasDefaultValue: true }),
  ],
};
