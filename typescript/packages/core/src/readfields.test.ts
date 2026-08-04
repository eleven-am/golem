import { AuthorizationProvider, FieldClassification } from './authorization';
import { DatamodelModel } from './datamodel';
import { GolemForbiddenError } from './errors';
import { GolemEngine } from './operations';
import { field } from './testing';

const user: DatamodelModel = {
  name: 'User',
  fields: [
    field({ name: 'id', type: 'String', isId: true }),
    field({ name: 'email', type: 'String', isUnique: true }),
    field({ name: 'phone', type: 'String', isRequired: false }),
    field({ name: 'profile', type: 'Profile', kind: 'object', isRequired: false }),
  ],
};

const profile: DatamodelModel = {
  name: 'Profile',
  fields: [
    field({ name: 'id', type: 'String', isId: true }),
    field({ name: 'secret', type: 'String', isRequired: false }),
  ],
};

const models = [user, profile];
const ctx = { req: {} };

function provider(
  classification: Record<string, FieldClassification>,
  phoneAllowedFor: (row: Record<string, unknown>) => boolean,
): AuthorizationProvider & { classifyFields: jest.Mock; checkField: jest.Mock } {
  return {
    authorize: jest.fn(async () => undefined),
    constrain: jest.fn(async () => ({})),
    check: jest.fn(async () => true),
    checkField: jest.fn(async (_a, _m, row: Record<string, unknown>, f: string) =>
      f === 'phone' || f === 'secret' ? phoneAllowedFor(row) : true,
    ),
    classifyFields: jest.fn(async (_a, _m, fields: readonly string[]) =>
      Object.fromEntries(fields.map((f) => [f, classification[f] ?? { access: 'always' }])),
    ),
  } as never;
}

describe('read field checks', () => {
  it('rejects never-readable fields at request time', async () => {
    const findMany = jest.fn();
    const engine = new GolemEngine({ user: { findMany } }, models, {
      authorization: provider({ phone: { access: 'never' } }, () => false),
    });
    await expect(
      engine.findMany({ model: 'User', select: { email: true, phone: true }, context: ctx }),
    ).rejects.toThrow('Cannot read field "phone" on User');
    expect(findMany).not.toHaveBeenCalled();
  });

  it('masks conditional fields per row and strips injected requires', async () => {
    const rows = [
      { email: 'a@b.c', phone: '111', id: 'me' },
      { email: 'x@y.z', phone: '222', id: 'other' },
    ];
    const findMany = jest.fn().mockResolvedValue(rows);
    const engine = new GolemEngine({ user: { findMany } }, models, {
      authorization: provider(
        { phone: { access: 'conditional', requires: ['id'] } },
        (row) => row.id === 'me',
      ),
      checkReadFields: true,
    });
    const result = (await engine.findMany({
      model: 'User',
      select: { email: true, phone: true },
      context: ctx,
    })) as Record<string, unknown>[];

    expect(findMany).toHaveBeenCalledWith(
      expect.objectContaining({ select: { email: true, phone: true, id: true } }),
    );
    expect(result[0].phone).toBe('111');
    expect(result[1].phone).toBeNull();
    expect('id' in result[0]).toBe(false);
    expect('id' in result[1]).toBe(false);
  });

  it('masks full-scalar reads when no select is given', async () => {
    const rows = [{ id: 'other', email: 'x@y.z', phone: '222' }];
    const findMany = jest.fn().mockResolvedValue(rows);
    const engine = new GolemEngine({ user: { findMany } }, models, {
      authorization: provider({ phone: { access: 'conditional' } }, () => false),
      checkReadFields: true,
    });
    const result = (await engine.findMany({ model: 'User', context: ctx })) as Record<
      string,
      unknown
    >[];
    expect(result[0].phone).toBeNull();
    expect(result[0].id).toBe('other');
  });

  it('classifies and masks relation shorthand selections', async () => {
    const findMany = jest.fn().mockResolvedValue([
      { id: 'u1', profile: { id: 'p1', secret: 'hidden' } },
    ]);
    const authz = provider({ secret: { access: 'conditional' } }, () => false);
    const engine = new GolemEngine({ user: { findMany } }, models, {
      authorization: authz,
      checkReadFields: true,
    });
    const result = (await engine.findMany({
      model: 'User',
      select: { id: true, profile: true },
      context: ctx,
    })) as Array<{ profile: { secret: string | null } }>;

    expect(findMany).toHaveBeenCalledWith(expect.objectContaining({
      select: {
        id: true,
        profile: { select: { id: true, secret: true } },
      },
    }));
    expect(authz.classifyFields).toHaveBeenCalledWith(
      'read', 'Profile', ['id', 'secret'], ctx,
    );
    expect(result[0].profile.secret).toBeNull();
  });

  it('does nothing when the flag is off', async () => {
    const findMany = jest.fn().mockResolvedValue([]);
    const authz = provider({ phone: { access: 'never' } }, () => false);
    const engine = new GolemEngine({ user: { findMany } }, models, {
      authorization: authz,
      checkReadFields: false,
    });
    await engine.findMany({ model: 'User', select: { phone: true }, context: ctx });
    expect(authz.classifyFields).not.toHaveBeenCalled();
    expect(findMany).toHaveBeenCalled();
  });

  it('fails at construction without provider support', () => {
    expect(
      () =>
        new GolemEngine({}, models, {
          authorization: { authorize: jest.fn(), constrain: jest.fn() },
          checkWriteResults: false,
          checkReadFields: true,
        }),
    ).toThrow('does not implement classifyFields and checkField');
  });
});
