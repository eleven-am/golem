import { INestApplication } from '@nestjs/common';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import { GolemValidationError } from '@eleven-am/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

interface Fixture {
  postId: string;
  royComment: string;
  adaComment: string;
  royReaction: string;
  adaReaction: string;
  royBookmark: string;
  adaBookmark: string;
}

type Seen = Record<'comment' | 'reaction' | 'bookmark', string[]>;

describe('cascaded delete events (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;
  let royId: string;
  let adaId: string;
  const clients: Client[] = [];

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    await app.listen(0);
    royId = (await prisma.user.findUniqueOrThrow({ where: { email: 'roy@example.com' } })).id;
    adaId = (await prisma.user.findUniqueOrThrow({ where: { email: 'ada@example.com' } })).id;
  });

  afterAll(async () => {
    await Promise.all(clients.map((client) => client.dispose()));
    await shutdownDemoApp(app, __filename);
  });

  async function fixture(label: string): Promise<Fixture> {
    const post = await prisma.post.create({ data: { title: `cascade ${label}`, authorId: royId } });
    const royComment = await prisma.comment.create({ data: { body: 'mine', authorId: royId, postId: post.id } });
    const adaComment = await prisma.comment.create({ data: { body: 'hers', authorId: adaId, postId: post.id } });
    const royReaction = await prisma.reaction.create({ data: { emoji: '+', authorId: royId, commentId: adaComment.id } });
    const adaReaction = await prisma.reaction.create({ data: { emoji: '!', authorId: adaId, commentId: royComment.id } });
    const royBookmark = await prisma.bookmark.create({ data: { label: 'r', ownerId: royId, postId: post.id } });
    const adaBookmark = await prisma.bookmark.create({ data: { label: 'a', ownerId: adaId, postId: post.id } });
    return {
      postId: post.id,
      royComment: royComment.id,
      adaComment: adaComment.id,
      royReaction: royReaction.id,
      adaReaction: adaReaction.id,
      royBookmark: royBookmark.id,
      adaBookmark: adaBookmark.id,
    };
  }

  function subscriber(email: string): { seen: Seen; stop(): void } {
    const address = app.getHttpServer().address();
    const client = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: `token-${email}` },
    });
    clients.push(client);
    const seen: Seen = { comment: [], reaction: [], bookmark: [] };
    const stops = (['comment', 'reaction', 'bookmark'] as const).map((model) =>
      client.subscribe(
        { query: `subscription { ${model}Events { type id } }` },
        {
          next: (value: any) => {
            const event = value.data[`${model}Events`];
            seen[model].push(`${event.type}:${event.id}`);
          },
          error: () => undefined,
          complete: () => undefined,
        },
      ));
    return { seen, stop: () => stops.forEach((stop) => stop()) };
  }

  async function observe(run: () => Promise<unknown>): Promise<{ roy: Seen; ada: Seen }> {
    const roy = subscriber('roy@example.com');
    const ada = subscriber('ada@example.com');
    await sleep(400);
    await run();
    await sleep(400);
    roy.stop();
    ada.stop();
    const sorted = (seen: Seen): Seen => ({
      comment: [...seen.comment].sort(),
      reaction: [...seen.reaction].sort(),
      bookmark: [...seen.bookmark].sort(),
    });
    return { roy: sorted(roy.seen), ada: sorted(ada.seen) };
  }

  function expected(rows: Fixture): { roy: Seen; ada: Seen } {
    return {
      roy: {
        comment: [`DELETED:${rows.adaComment}`, `DELETED:${rows.royComment}`].sort(),
        reaction: [`DELETED:${rows.adaReaction}`, `DELETED:${rows.royReaction}`].sort(),
        bookmark: [`UPDATED:${rows.adaBookmark}`, `UPDATED:${rows.royBookmark}`].sort(),
      },
      ada: {
        comment: [`DELETED:${rows.adaComment}`],
        reaction: [`DELETED:${rows.adaReaction}`],
        bookmark: [`UPDATED:${rows.adaBookmark}`],
      },
    };
  }

  async function expectCascaded(rows: Fixture): Promise<void> {
    await expect(prisma.comment.count({ where: { postId: rows.postId } })).resolves.toBe(0);
    await expect(prisma.reaction.count({
      where: { id: { in: [rows.royReaction, rows.adaReaction] } },
    })).resolves.toBe(0);
    await expect(prisma.bookmark.findMany({
      where: { id: { in: [rows.royBookmark, rows.adaBookmark] } },
      select: { postId: true },
    })).resolves.toEqual([{ postId: null }, { postId: null }]);
  }

  it('emits an event per cascaded row for a GraphQL delete, filtered by each subscriber read policy', async () => {
    const rows = await fixture('graphql');
    const seen = await observe(() =>
      request(app.getHttpServer())
        .post('/graphql')
        .set('authorization', 'token-roy@example.com')
        .send({ query: `mutation { deletePost(where: { id: "${rows.postId}" }) { id } }` })
        .expect(200)
        .then((response) => expect(response.body.errors).toBeUndefined()));

    expect(seen).toEqual(expected(rows));
    await expectCascaded(rows);
  });

  it('emits cascaded events for a context-bound deleteMany', async () => {
    const rows = await fixture('many');
    const seen = await observe(() =>
      prisma.forContext(ctxFor('roy@example.com')).post.deleteMany({ where: { id: rows.postId } }));

    expect(seen).toEqual(expected(rows));
    await expectCascaded(rows);
  });

  it('emits cascaded events for a nested delete', async () => {
    const rows = await fixture('nested');
    const seen = await observe(() =>
      prisma.forContext(ctxFor('roy@example.com')).user.update({
        where: { id: royId },
        data: { posts: { delete: [{ id: rows.postId }] } },
      }));

    expect(seen).toEqual(expected(rows));
    await expectCascaded(rows);
  });

  it('emits cascaded events for a delete on the unscoped client', async () => {
    const rows = await fixture('unscoped');
    const seen = await observe(() => prisma.post.delete({ where: { id: rows.postId } }));

    expect(seen).toEqual(expected(rows));
    await expectCascaded(rows);
  });

  describe('with a touched-row cap', () => {
    let capped: INestApplication;
    let cappedPrisma: GolemPrismaService;

    beforeAll(async () => {
      const context = await bootDemoApp(`${__filename.replace('.e2e', '.capped.e2e')}`, {
        golem: { batchEvents: { maxRows: 5 } },
      });
      capped = context.app;
      cappedPrisma = context.prisma;
    });

    afterAll(async () => {
      await shutdownDemoApp(capped, `${__filename.replace('.e2e', '.capped.e2e')}`);
    });

    it('refuses the whole delete when the cascade would touch more rows than the cap', async () => {
      const author = await cappedPrisma.user.findUniqueOrThrow({ where: { email: 'roy@example.com' } });
      const post = await cappedPrisma.post.create({ data: { title: 'capped', authorId: author.id } });
      for (let index = 0; index < 5; index += 1) {
        await cappedPrisma.comment.create({ data: { body: `${index}`, authorId: author.id, postId: post.id } });
      }

      await expect(cappedPrisma.post.delete({ where: { id: post.id } }))
        .rejects.toBeInstanceOf(GolemValidationError);
      await expect(cappedPrisma.post.delete({ where: { id: post.id } }))
        .rejects.toThrow('Deleting from Post would touch more than the maximum of 5 rows, counting cascaded dependents');
      await expect(cappedPrisma.comment.count({ where: { postId: post.id } })).resolves.toBe(5);
      await expect(cappedPrisma.post.count({ where: { id: post.id } })).resolves.toBe(1);
    });
  });
});
