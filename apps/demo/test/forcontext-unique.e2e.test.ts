import { INestApplication } from '@nestjs/common';
import { GolemNotFoundError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { bootDemoApp, shutdownDemoApp } from './harness';

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

async function userId(prisma: GolemPrismaService, email: string): Promise<string> {
  return (await prisma.user.findUniqueOrThrow({ where: { email } })).id;
}

describe('forContext compound-unique upsert (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let adaId: string;
  let royId: string;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
  });

  beforeEach(async () => {
    await seed(prisma);
    adaId = await userId(prisma, 'ada@example.com');
    royId = await userId(prisma, 'roy@example.com');
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  it('creates then updates a branch through a compound-unique upsert selector', async () => {
    const ada = prisma.forContext(ctxFor('ada@example.com'));

    const created = await ada.branch.upsert({
      where: { authorId_name: { authorId: adaId, name: 'main' } },
      create: { authorId: adaId, name: 'main' },
      update: { name: 'main' },
    });
    expect(created).toMatchObject({ authorId: adaId, name: 'main' });

    const updated = await ada.branch.upsert({
      where: { authorId_name: { authorId: adaId, name: 'main' } },
      create: { authorId: adaId, name: 'main' },
      update: { name: 'main-renamed' },
    });
    expect(updated.id).toBe(created.id);
    expect(updated.name).toBe('main-renamed');

    const rows = await prisma.branch.findMany({ where: { authorId: adaId } });
    expect(rows).toHaveLength(1);
    expect(rows[0].name).toBe('main-renamed');
  });

  it('updates a branch addressed by its compound-unique selector', async () => {
    const ada = prisma.forContext(ctxFor('ada@example.com'));
    await ada.branch.create({ data: { authorId: adaId, name: 'feature' } });

    const updated = await ada.branch.update({
      where: { authorId_name: { authorId: adaId, name: 'feature' } },
      data: { name: 'feature-2' },
    });
    expect(updated.name).toBe('feature-2');
  });

  it('rejects a cross-tenant upsert against another owner branch as NOT_FOUND', async () => {
    await prisma.branch.create({ data: { authorId: royId, name: 'main' } });

    await expect(
      prisma.forContext(ctxFor('ada@example.com')).branch.upsert({
        where: { authorId_name: { authorId: royId, name: 'main' } },
        create: { authorId: royId, name: 'main' },
        update: { name: 'stolen' },
      }),
    ).rejects.toBeInstanceOf(GolemNotFoundError);

    const roysBranch = await prisma.branch.findUniqueOrThrow({
      where: { authorId_name: { authorId: royId, name: 'main' } },
    });
    expect(roysBranch.name).toBe('main');
  });
});
