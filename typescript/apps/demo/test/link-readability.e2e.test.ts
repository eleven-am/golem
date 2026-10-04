import { INestApplication } from '@nestjs/common';
import request from 'supertest';
import { GolemNotFoundError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

const LINKER = 'linker@example.com';
const MISSING = 'missing-target-id';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

interface Outcome {
  error: string;
  code?: string;
  message: string;
}

async function outcome(run: () => Promise<unknown>): Promise<Outcome> {
  try {
    await run();
    return { error: 'none', message: 'succeeded' };
  } catch (error) {
    return {
      error: (error as Error).constructor.name,
      code: (error as { code?: string }).code,
      message: (error as Error).message,
    };
  }
}

describe('a caller links only to rows it can read (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let linkerId: string;
  let ownPostId: string;
  let ownSessionId: string;
  let draftId: string;
  let publishedId: string;
  let foreignSessionId: string;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    const ada = await prisma.user.findUniqueOrThrow({ where: { email: 'ada@example.com' } });
    const linker = await prisma.user.create({ data: { email: LINKER, name: 'Linker' } });
    linkerId = linker.id;
    const ownPost = await prisma.post.create({
      data: { title: 'Linker post', published: true, authorId: linkerId },
    });
    ownPostId = ownPost.id;
    ownSessionId = (await prisma.readingSession.create({ data: { postId: ownPostId } })).id;
    draftId = (await prisma.post.create({
      data: { title: 'Ada draft', published: false, authorId: ada.id },
    })).id;
    publishedId = (await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } })).id;
    foreignSessionId = (await prisma.readingSession.create({ data: { postId: publishedId } })).id;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  const asLinker = () => prisma.forContext(ctxFor(LINKER));

  async function sessionsOn(postId: string): Promise<number> {
    return prisma.readingSession.count({ where: { postId } });
  }

  async function expectSameAsMissing(
    attempt: (target: string) => Promise<unknown>,
    unreadable: string,
  ): Promise<Outcome> {
    const hidden = await outcome(() => attempt(unreadable));
    const missing = await outcome(() => attempt(MISSING));
    expect(hidden).toEqual(missing);
    expect(hidden.error).not.toBe('none');
    return hidden;
  }

  it('refuses a foreign-key scalar on create exactly as a missing target', async () => {
    const result = await expectSameAsMissing(
      (target) => asLinker().readingSession.create({ data: { postId: target } as never }),
      draftId,
    );
    expect(result).toEqual({ error: 'GolemNotFoundError', code: 'NOT_FOUND', message: 'Post not found' });
    await expect(sessionsOn(draftId)).resolves.toBe(0);
  });

  it('refuses a foreign-key scalar on update exactly as a missing target', async () => {
    await expectSameAsMissing(
      (target) => asLinker().readingSession.update({
        where: { id: ownSessionId },
        data: { postId: target } as never,
      }),
      draftId,
    );
    await expect(prisma.readingSession.findUnique({ where: { id: ownSessionId } }))
      .resolves.toMatchObject({ postId: ownPostId });
  });

  it('refuses a foreign-key scalar on updateMany exactly as a missing target', async () => {
    await expectSameAsMissing(
      (target) => asLinker().readingSession.updateMany({
        where: { id: ownSessionId },
        data: { postId: target } as never,
      }),
      draftId,
    );
    await expect(sessionsOn(draftId)).resolves.toBe(0);
  });

  it('refuses a to-one connect exactly as a missing target', async () => {
    await expectSameAsMissing(
      (target) => asLinker().readingSession.create({ data: { post: { connect: { id: target } } } }),
      draftId,
    );
    await expect(sessionsOn(draftId)).resolves.toBe(0);
  });

  it('refuses a to-many connect, set and disconnect exactly as a missing target', async () => {
    for (const kind of ['connect', 'set', 'disconnect'] as const) {
      const result = await expectSameAsMissing(
        (target) => asLinker().user.update({
          where: { id: linkerId },
          data: { posts: { [kind]: [{ id: target }] } },
        }),
        draftId,
      );
      expect(result).toEqual({ error: 'GolemNotFoundError', code: 'NOT_FOUND', message: 'Post not found' });
    }
    await expect(prisma.post.findUnique({ where: { id: draftId } }))
      .resolves.toMatchObject({ authorId: (await prisma.user.findUniqueOrThrow({ where: { email: 'ada@example.com' } })).id });
    await expect(prisma.post.count({ where: { authorId: linkerId } })).resolves.toBe(1);
  });

  it('reports an unreadable connectOrCreate target as not found instead of creating', async () => {
    const before = await prisma.post.count();
    await expect(
      asLinker().readingSession.create({
        data: {
          post: {
            connectOrCreate: {
              where: { id: draftId },
              create: { title: 'Duplicate', author: { connect: { id: linkerId } } },
            },
          },
        },
      }),
    ).rejects.toBeInstanceOf(GolemNotFoundError);
    await expect(prisma.post.count()).resolves.toBe(before);
    await expect(sessionsOn(draftId)).resolves.toBe(0);
  });

  it('creates through connectOrCreate when the target is missing', async () => {
    await asLinker().readingSession.create({
      data: {
        post: {
          connectOrCreate: {
            where: { id: 'linker-created-post' },
            create: { id: 'linker-created-post', title: 'Created', author: { connect: { id: linkerId } } },
          },
        },
      },
    });
    await expect(sessionsOn('linker-created-post')).resolves.toBe(1);
  });

  it('links to a row it can read but neither update nor create', async () => {
    await asLinker().readingSession.create({ data: { post: { connect: { id: publishedId } } } });
    await asLinker().readingSession.create({ data: { postId: publishedId } as never });
    await asLinker().readingSession.create({
      data: {
        post: {
          connectOrCreate: {
            where: { id: publishedId },
            create: { title: 'Never created', author: { connect: { id: linkerId } } },
          },
        },
      },
    });
    await expect(sessionsOn(publishedId)).resolves.toBe(4);
    await expect(prisma.post.count({ where: { title: 'Never created' } })).resolves.toBe(0);
  });

  it('refuses an unreadable link through GraphQL exactly as a missing one', async () => {
    const attempt = async (target: string) => {
      const response = await request(app.getHttpServer())
        .post('/graphql')
        .set('authorization', `token-${LINKER}`)
        .send({
          query: 'mutation ($post: String!) { createReadingSession(data: { post: { connect: { id: $post } } }) { id } }',
          variables: { post: target },
        });
      return response.body.errors.map((error: { message: string; extensions: { code: string } }) => ({
        message: error.message,
        code: error.extensions.code,
      }));
    };
    const hidden = await attempt(draftId);
    expect(hidden).toEqual(await attempt(MISSING));
    expect(hidden).toEqual([{ message: 'Post not found', code: 'NOT_FOUND' }]);
    await expect(sessionsOn(draftId)).resolves.toBe(0);
  });

  it('leaves the unscoped client free to link to any row', async () => {
    await prisma.readingSession.create({ data: { postId: draftId } });
    await expect(sessionsOn(draftId)).resolves.toBe(1);
  });
});
