import { INestApplication } from '@nestjs/common';
import { GolemForbiddenError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

async function tagId(prisma: GolemPrismaService, label: string): Promise<string> {
  const tag = await prisma.tag.findUniqueOrThrow({ where: { label } });
  return tag.id;
}

async function joinRow(prisma: GolemPrismaService, postId: string, tagId: string) {
  return prisma.postTag.findUnique({ where: { postId_tagId: { postId, tagId } } });
}

describe('forContext composite-key join model (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let adaPostId: string;
  let royPostId: string;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
  });

  beforeEach(async () => {
    await seed(prisma);
    adaPostId = (await prisma.post.findFirstOrThrow({ where: { title: 'Memory systems' } })).id;
    royPostId = (await prisma.post.findFirstOrThrow({ where: { title: 'First post' } })).id;
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  it('creates a relation-scoped join row for the post owner', async () => {
    const alpha = await tagId(prisma, 'alpha');
    const created = await prisma.forContext(ctxFor('ada@example.com')).postTag.create({
      data: { postId: adaPostId, tagId: alpha },
    });
    expect(created).toMatchObject({ postId: adaPostId, tagId: alpha });
    expect(await joinRow(prisma, adaPostId, alpha)).not.toBeNull();
  });

  it('rolls back a cross-tenant join create as FORBIDDEN, leaving no row', async () => {
    const beta = await tagId(prisma, 'beta');
    await expect(
      prisma.forContext(ctxFor('ada@example.com')).postTag.create({
        data: { postId: royPostId, tagId: beta },
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(await joinRow(prisma, royPostId, beta)).toBeNull();
  });

  it('deletes a join row by compound identity for the owner', async () => {
    const gamma = await tagId(prisma, 'gamma');
    await prisma.forContext(ctxFor('ada@example.com')).postTag.create({
      data: { postId: adaPostId, tagId: gamma },
    });
    const deleted = await prisma.forContext(ctxFor('ada@example.com')).postTag.delete({
      where: { postId_tagId: { postId: adaPostId, tagId: gamma } },
    });
    expect(deleted).toMatchObject({ postId: adaPostId, tagId: gamma });
    expect(await joinRow(prisma, adaPostId, gamma)).toBeNull();
  });

  it('commits a readable-shape transaction of a counter update and a join create atomically', async () => {
    const delta = await tagId(prisma, 'delta');
    await prisma.forContext(ctxFor('ada@example.com')).$transaction(async (tx) => {
      await tx.post.update({ where: { id: adaPostId }, data: { viewCount: 250n } });
      await tx.postTag.create({ data: { postId: adaPostId, tagId: delta } });
    });
    const post = await prisma.post.findUniqueOrThrow({ where: { id: adaPostId } });
    expect(post.viewCount).toBe(250n);
    expect(await joinRow(prisma, adaPostId, delta)).not.toBeNull();
  });

  it('rolls back the counter update when the join create is denied', async () => {
    const epsilon = await tagId(prisma, 'epsilon');
    await expect(
      prisma.forContext(ctxFor('ada@example.com')).$transaction(async (tx) => {
        await tx.post.update({ where: { id: adaPostId }, data: { viewCount: 999n } });
        await tx.postTag.create({ data: { postId: royPostId, tagId: epsilon } });
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    const post = await prisma.post.findUniqueOrThrow({ where: { id: adaPostId } });
    expect(post.viewCount).toBe(100n);
    expect(await joinRow(prisma, royPostId, epsilon)).toBeNull();
  });

  it('verifies an appeared composite child nested under a parent update for the owner', async () => {
    const alpha = await tagId(prisma, 'alpha');
    await prisma.forContext(ctxFor('ada@example.com')).tag.update({
      where: { id: alpha },
      data: { postTags: { create: [{ post: { connect: { id: adaPostId } } }] } },
    });
    expect(await joinRow(prisma, adaPostId, alpha)).not.toBeNull();
  });

  it('rejects and rolls back a cross-tenant composite child nested under a parent update', async () => {
    const beta = await tagId(prisma, 'beta');
    await expect(
      prisma.forContext(ctxFor('ada@example.com')).tag.update({
        where: { id: beta },
        data: { postTags: { create: [{ post: { connect: { id: royPostId } } }] } },
      }),
    ).rejects.toBeInstanceOf(GolemForbiddenError);
    expect(await joinRow(prisma, royPostId, beta)).toBeNull();
  });

  it('updates many composite rows by scalar identity for the owner', async () => {
    const alpha = await tagId(prisma, 'alpha');
    const beta = await tagId(prisma, 'beta');
    const scoped = prisma.forContext(ctxFor('ada@example.com'));
    await scoped.postTag.create({ data: { postId: adaPostId, tagId: alpha } });
    await scoped.postTag.create({ data: { postId: adaPostId, tagId: beta } });

    const stamped = new Date('2020-01-01T00:00:00.000Z');
    const result = await scoped.postTag.updateMany({
      where: { postId: adaPostId },
      data: { addedAt: stamped },
    });
    expect(result.count).toBe(2);

    const rows = await prisma.postTag.findMany({ where: { postId: adaPostId } });
    expect(rows).toHaveLength(2);
    for (const row of rows) {
      expect(row.addedAt.toISOString()).toBe(stamped.toISOString());
    }
  });
});
