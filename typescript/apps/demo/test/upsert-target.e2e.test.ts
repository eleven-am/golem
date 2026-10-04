import { INestApplication } from '@nestjs/common';
import { GOLEM_ENGINE, GolemEngine, GolemValidationError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

const REFUSAL = 'upsert create input does not set the target selector id on Post';

describe('upsert target identity (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let engine: GolemEngine;
  let royId: string;
  let royPostId: string;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    engine = app.get(GOLEM_ENGINE);
    royId = (await prisma.user.findUniqueOrThrow({ where: { email: 'roy@example.com' } })).id;
    royPostId = (await prisma.post.findFirstOrThrow({ where: { title: 'First post' } })).id;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  const asRoy = () => prisma.forContext(ctxFor('roy@example.com'));

  it('refuses a root upsert whose create input names a different id, creating nothing', async () => {
    await expect(
      asRoy().post.upsert({
        where: { id: 'root-target' },
        create: { id: 'root-other', title: 'Wrong row', author: { connect: { id: royId } } },
        update: { title: 'Updated' },
      }),
    ).rejects.toThrow(REFUSAL);
    await expect(prisma.post.count({ where: { id: { in: ['root-target', 'root-other'] } } }))
      .resolves.toBe(0);
  });

  it('refuses a root upsert with a mismatched create input even when the target exists', async () => {
    await expect(
      asRoy().post.upsert({
        where: { id: royPostId },
        create: { id: 'root-other', title: 'Wrong row', author: { connect: { id: royId } } },
        update: { title: 'Hijacked' },
      }),
    ).rejects.toBeInstanceOf(GolemValidationError);
    await expect(prisma.post.findUnique({ where: { id: royPostId } }))
      .resolves.toMatchObject({ title: 'First post' });
  });

  it('creates the target when the create input names it', async () => {
    await asRoy().post.upsert({
      where: { id: 'root-target' },
      create: { id: 'root-target', title: 'Right row', author: { connect: { id: royId } } },
      update: { title: 'Updated' },
    });
    await expect(prisma.post.findUnique({ where: { id: 'root-target' } }))
      .resolves.toMatchObject({ title: 'Right row' });
  });

  it('refuses a mismatched nested upsert below a nested create', async () => {
    await expect(
      asRoy().user.create({
        data: {
          email: 'deep@example.com',
          posts: {
            create: [{
              title: 'Parent',
              readingSessions: {
                upsert: [{
                  where: { id: 'deep-target' },
                  create: { id: 'deep-other' },
                  update: { progress: 1 },
                }],
              },
            }],
          },
        },
      } as never),
    ).rejects.toThrow('upsert create input does not set the target selector id on ReadingSession');
    await expect(prisma.user.count({ where: { email: 'deep@example.com' } })).resolves.toBe(0);
  });

  describe.each([
    ['a context-bound caller', () => ctxFor('roy@example.com')],
    ['the unscoped engine', () => undefined],
  ])('nested upsert through %s', (label, context) => {
    const slug = label.replace(/\W+/g, '-');
    const updateRoy = (posts: unknown) =>
      engine.update({
        model: 'User',
        where: { id: royId },
        data: { posts },
        select: { id: true },
        context: context(),
      });

    it('refuses a mismatched create input for a missing target, writing nothing', async () => {
      await expect(
        updateRoy({
          upsert: [{
            where: { id: `${slug}-target` },
            create: { id: `${slug}-other`, title: 'Wrong row' },
            update: { title: 'Updated' },
          }],
        }),
      ).rejects.toThrow(REFUSAL);
      await expect(
        prisma.post.count({ where: { id: { in: [`${slug}-target`, `${slug}-other`] } } }),
      ).resolves.toBe(0);
    });

    it('refuses a mismatched create input for an existing target, changing nothing', async () => {
      await expect(
        updateRoy({
          upsert: [{
            where: { id: royPostId },
            create: { id: `${slug}-other`, title: 'Wrong row' },
            update: { title: 'Hijacked' },
          }],
        }),
      ).rejects.toThrow(REFUSAL);
      await expect(prisma.post.findUnique({ where: { id: royPostId } }))
        .resolves.toMatchObject({ title: 'First post' });
      await expect(prisma.post.count({ where: { id: `${slug}-other` } })).resolves.toBe(0);
    });

    it('creates the target when the create input names it', async () => {
      await updateRoy({
        upsert: [{
          where: { id: `${slug}-target` },
          create: { id: `${slug}-target`, title: 'Right row' },
          update: { title: 'Updated' },
        }],
      });
      await expect(prisma.post.findUnique({ where: { id: `${slug}-target` } }))
        .resolves.toMatchObject({ title: 'Right row', authorId: royId });
    });

    it('updates the target when it exists and the create input names it', async () => {
      await updateRoy({
        upsert: [{
          where: { id: `${slug}-target` },
          create: { id: `${slug}-target`, title: 'Ignored' },
          update: { title: 'Updated' },
        }],
      });
      await expect(prisma.post.findUnique({ where: { id: `${slug}-target` } }))
        .resolves.toMatchObject({ title: 'Updated' });
    });
  });
});
