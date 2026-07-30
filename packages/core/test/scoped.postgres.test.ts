import { context, engineFor, seed } from './support/analytics';
import { analyticsSuite } from './support/expectations';
import {
  POSTGRES_OPTIONAL,
  POSTGRES_URL_ENV,
  POSTGRES_URL_HINT,
  PostgresHandle,
  openPostgres,
} from './support/postgres';

const url = process.env[POSTGRES_URL_ENV] ?? '';

describe('a scoped analytical query against a live postgres database', () => {
  let handle: PostgresHandle;

  beforeAll(async () => {
    if (url === '') {
      if (POSTGRES_OPTIONAL) {
        return;
      }
      throw new Error(POSTGRES_URL_HINT);
    }
    handle = await openPostgres(url);
    await seed(handle.prisma, (position) => `$${position}`);
  });

  afterAll(async () => {
    await handle?.close();
  });

  if (url === '' && POSTGRES_OPTIONAL) {
    it.skip(`skipped: ${POSTGRES_URL_HINT}`, () => undefined);
  } else {
    analyticsSuite(() => ({
      provider: 'postgresql',
      client: handle.prisma as unknown as Record<string, any>,
    }));

    it('numbers the policy parameter rather than inlining it', async () => {
      const compiled = await engineFor(
        handle.prisma as unknown as Record<string, any>,
        'postgresql',
        { Post: { authorId: 2 } },
      )
        .scoped({ model: 'Post', context })
        .query((qb) => qb.select('Post.id'))
        .compile();

      expect(compiled.sql).toContain('$1');
      expect(compiled.sql).not.toContain('= 2');
      expect(compiled.parameters).toEqual([2]);
    });
  }
});
