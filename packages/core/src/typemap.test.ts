import { HookRequestFor } from './typemap';

type TestTypes = {
  User: {
    entity: { id: string; email: string };
    create: { email: string };
    update: { email?: string };
    updateMany: { email?: string };
    where: { email?: string };
    whereUnique: { id?: string; email?: string };
    orderBy: { email?: 'asc' | 'desc' };
    select: { id?: boolean; email?: boolean };
    include: { posts?: boolean };
    omit: { id?: boolean; email?: boolean };
  };
};

describe('hook request types', () => {
  it.each(['findFirst', 'findMany'] as const)(
    'types cursor and distinct for %s requests',
    (operation) => {
      const request = {
        model: 'User',
        cursor: { id: 'u1' },
        distinct: ['email'],
        include: { posts: true },
        omit: { email: true },
      } satisfies HookRequestFor<TestTypes, 'User', typeof operation>;

      expect(request).toEqual({
        model: 'User',
        cursor: { id: 'u1' },
        distinct: ['email'],
        include: { posts: true },
        omit: { email: true },
      });
    },
  );
});
