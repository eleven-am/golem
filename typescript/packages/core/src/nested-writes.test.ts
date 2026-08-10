import { buildModelMetadata } from './model-meta';
import { planNestedWrites } from './nested-writes';
import { field } from './testing';

const child = {
  name: 'Child',
  fields: [field({ name: 'id', type: 'String', isId: true }), field({ name: 'value', type: 'String' })],
};
const parent = {
  name: 'Parent',
  fields: [
    field({ name: 'id', type: 'String', isId: true }),
    field({ name: 'children', type: 'Child', kind: 'object', isList: true }),
  ],
};
const metadata = buildModelMetadata([parent, child]);

describe('normalized nested write plans', () => {
  it.each([
    ['create', { create: [{ value: 'x' }] }, ['create'], ['create'], [], []],
    ['createMany', { createMany: { data: [{ value: 'x' }] } }, ['create'], ['create'], [], []],
    ['connectOrCreate', { connectOrCreate: { where: { id: 'c1' }, create: { value: 'x' } } }, ['create', 'update'], ['create', 'update'], [], []],
    ['connect', { connect: [{ id: 'c1' }] }, ['update'], ['update'], [], []],
    ['disconnect', { disconnect: [{ id: 'c1' }] }, ['update'], [], ['update'], []],
    ['set', { set: [{ id: 'c1' }] }, ['update'], ['update'], ['update'], []],
    ['update', { update: { where: { id: 'c1' }, data: { value: 'y' } } }, ['update'], [], [], ['update']],
    ['updateMany', { updateMany: { where: { id: 'c1' }, data: { value: 'y' } } }, ['update'], [], [], ['update']],
    ['upsert', { upsert: { where: { id: 'c1' }, create: { value: 'x' }, update: { value: 'y' } } }, ['create', 'update'], ['create'], [], ['update']],
    ['delete', { delete: [{ id: 'c1' }] }, ['delete'], [], ['delete'], []],
    ['deleteMany', { deleteMany: { id: 'c1' } }, ['delete'], [], ['delete'], []],
  ])(
    'normalizes %s action semantics',
    (_name, envelope, modelActions, added, removed, retained) => {
      const [plan] = planNestedWrites(metadata, parent, { children: envelope });
      expect([...plan.modelActions]).toEqual(modelActions);
      expect([...plan.addedActions]).toEqual(added);
      expect([...plan.removedActions]).toEqual(removed);
      expect([...plan.retainedActions]).toEqual(retained);
    },
  );

  it('extracts create and update payloads once for recursive planning', () => {
    const [plan] = planNestedWrites(metadata, parent, {
      children: {
        createMany: { data: [{ value: 'a' }, { value: 'b' }] },
        update: { where: { id: 'c1' }, data: { value: 'c' } },
      },
    });
    expect(plan.createPayloads).toEqual([{ value: 'a' }, { value: 'b' }]);
    expect(plan.updatePayloads).toEqual([{ value: 'c' }]);
  });
});

