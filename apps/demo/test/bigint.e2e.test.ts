import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('BigInt scalar (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const moduleRef = await Test.createTestingModule({
      imports: [AppModule],
    }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    await app.listen(0);
    prisma = app.get(GolemPrismaService);
    await seed(prisma);
    await prisma.user.create({ data: { email: 'analyst@example.com', name: 'Analyst' } });
    await prisma.user.create({ data: { email: 'cliff@example.com', name: 'Cliff' } });
  });

  afterAll(async () => {
    await app.close();
  });

  function gql(query: string, token = 'token-roy@example.com', variables?: Record<string, unknown>) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', token)
      .send(variables ? { query, variables } : { query });
  }

  it('round-trips a value above 2^53 losslessly as a string', async () => {
    const response = await gql(`mutation {
      createPost(data: {
        title: "big-create"
        viewCount: "9223372036854775807"
        author: { connect: { email: "roy@example.com" } }
      }) { id viewCount }
    }`);
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.createPost.viewCount).toBe('9223372036854775807');

    const id = response.body.data.createPost.id;
    const read = await gql(`{ posts(where: { id: { equals: "${id}" } }) { viewCount } }`);
    expect(read.body.data.posts).toEqual([{ viewCount: '9223372036854775807' }]);
  });

  it('filters on the field with both string and int literal forms', async () => {
    const equalsString = await gql('{ posts(where: { viewCount: { equals: "100" } }) { title viewCount } }');
    expect(equalsString.body.errors).toBeUndefined();
    expect(equalsString.body.data.posts).toEqual([{ title: 'Memory systems', viewCount: '100' }]);

    const equalsInt = await gql('{ posts(where: { viewCount: { equals: 100 } }) { title } }');
    expect(equalsInt.body.data.posts).toEqual([{ title: 'Memory systems' }]);

    const gtString = await gql('{ posts(where: { viewCount: { gt: "9007199254740992" } }) { title } }');
    const gtStringTitles = gtString.body.data.posts.map((p: { title: string }) => p.title);
    expect(gtStringTitles).toContain('First post');
    expect(gtStringTitles).not.toContain('Draft post');
    expect(gtStringTitles).not.toContain('Memory systems');

    const gtInt = await gql('{ posts(where: { viewCount: { gt: 5 } }) { title } }');
    const gtIntTitles = gtInt.body.data.posts.map((p: { title: string }) => p.title);
    expect(gtIntTitles).toContain('Memory systems');
    expect(gtIntTitles).toContain('First post');
    expect(gtIntTitles).not.toContain('Draft post');
  });

  it('orders by the field with database-native BigInt comparison', async () => {
    const response = await gql('{ posts(orderBy: { viewCount: asc }) { viewCount } }');
    expect(response.body.errors).toBeUndefined();
    const values: bigint[] = response.body.data.posts.map((p: { viewCount: string }) => BigInt(p.viewCount));
    const sorted = [...values].sort((a, b) => (a < b ? -1 : a > b ? 1 : 0));
    expect(values).toEqual(sorted);
    expect(values[0]).toBe(5n);
  });

  it('enforces a CASL row policy conditioned on the BigInt column', async () => {
    const analystView = await gql('{ posts { title viewCount } }', 'token-analyst@example.com');
    expect(analystView.body.errors).toBeUndefined();
    const titles = analystView.body.data.posts.map((p: { title: string }) => p.title);
    expect(titles).toContain('Memory systems');
    expect(titles).toContain('First post');
    expect(titles).not.toContain('Draft post');
    for (const post of analystView.body.data.posts) {
      expect(BigInt(post.viewCount) >= 100n).toBe(true);
    }

    const belowPolicy = await gql(
      '{ posts(where: { title: { equals: "Draft post" } }) { title } }',
      'token-analyst@example.com',
    );
    expect(belowPolicy.body.data.posts).toEqual([]);

    const facadeView = await prisma
      .forContext(ctxFor('analyst@example.com'))
      .post.findMany({ select: { title: true, viewCount: true } });
    expect(facadeView.map((p) => p.title)).not.toContain('Draft post');
    for (const post of facadeView) {
      expect((post.viewCount as bigint) >= 100n).toBe(true);
    }
  });

  it('verifies a BigInt write policy exactly one ULP beyond 2^53', async () => {
    const above = await prisma.post.create({
      data: {
        title: 'cliff-above',
        viewCount: 9007199254740993n,
        author: { connect: { email: 'roy@example.com' } },
      },
    });
    const below = await prisma.post.create({
      data: {
        title: 'cliff-below',
        viewCount: 9007199254740992n,
        author: { connect: { email: 'roy@example.com' } },
      },
    });

    const allowed = await gql(
      `mutation { updatePost(where: { id: "${above.id}" }, data: { title: "cliff-above-edited" }) { title } }`,
      'token-cliff@example.com',
    );
    expect(allowed.body.errors).toBeUndefined();
    expect(allowed.body.data.updatePost.title).toBe('cliff-above-edited');

    const denied = await gql(
      `mutation { updatePost(where: { id: "${below.id}" }, data: { title: "cliff-below-edited" }) { id } }`,
      'token-cliff@example.com',
    );
    expect(denied.body.errors[0].extensions.code).toBe('NOT_FOUND');
    const untouched = await prisma.post.findUnique({ where: { id: below.id } });
    expect(untouched!.title).toBe('cliff-below');
  });

  it('verifies a write policy conditioned on the BigInt column', async () => {
    const above = await prisma.post.create({
      data: { title: 'write-above', viewCount: 500n, author: { connect: { email: 'roy@example.com' } } },
    });
    const below = await prisma.post.create({
      data: { title: 'write-below', viewCount: 50n, author: { connect: { email: 'roy@example.com' } } },
    });

    const allowed = await gql(
      `mutation { updatePost(where: { id: "${above.id}" }, data: { title: "write-above-edited" }) { title } }`,
      'token-analyst@example.com',
    );
    expect(allowed.body.errors).toBeUndefined();
    expect(allowed.body.data.updatePost.title).toBe('write-above-edited');

    const denied = await gql(
      `mutation { updatePost(where: { id: "${below.id}" }, data: { title: "write-below-edited" }) { id } }`,
      'token-analyst@example.com',
    );
    expect(denied.body.errors[0].extensions.code).toBe('NOT_FOUND');
    const untouched = await prisma.post.findUnique({ where: { id: below.id } });
    expect(untouched!.title).toBe('write-below');

    const facadeAllowed = await prisma.forContext(ctxFor('analyst@example.com')).post.update({
      where: { id: above.id },
      data: { title: 'write-above-facade' },
    });
    expect(facadeAllowed.title).toBe('write-above-facade');
  });

  it('updates the field through the generated mutation and forContext', async () => {
    const memory = await prisma.post.findFirst({ where: { title: 'Memory systems' } });
    const mutated = await gql(`mutation {
      updatePost(where: { id: "${memory!.id}" }, data: { viewCount: "250" }) { viewCount }
    }`);
    expect(mutated.body.errors).toBeUndefined();
    expect(mutated.body.data.updatePost.viewCount).toBe('250');

    const facade = await prisma.forContext(ctxFor('roy@example.com')).post.update({
      where: { id: memory!.id },
      data: { viewCount: 999n },
    });
    expect(facade.viewCount).toBe(999n);

    const persisted = await prisma.post.findUnique({ where: { id: memory!.id } });
    expect(persisted!.viewCount).toBe(999n);
  });

  it('rejects malformed BigInt inputs with a clean scalar error', async () => {
    const fractionalLiteral = await gql(`mutation {
      createPost(data: { title: "bad", viewCount: 1.5, author: { connect: { email: "roy@example.com" } } }) { id }
    }`);
    expect(fractionalLiteral.body.errors).toBeDefined();
    expect(JSON.stringify(fractionalLiteral.body.errors)).toContain('BigInt');

    const nonNumericString = await gql(`mutation {
      createPost(data: { title: "bad", viewCount: "not-a-number", author: { connect: { email: "roy@example.com" } } }) { id }
    }`);
    expect(nonNumericString.body.errors).toBeDefined();
    expect(JSON.stringify(nonNumericString.body.errors)).toContain('BigInt');

    const booleanLiteral = await gql(`mutation {
      createPost(data: { title: "bad", viewCount: true, author: { connect: { email: "roy@example.com" } } }) { id }
    }`);
    expect(booleanLiteral.body.errors).toBeDefined();
    expect(JSON.stringify(booleanLiteral.body.errors)).toContain('BigInt');

    const fractionalVariable = await gql(
      `mutation ($vc: BigInt!) {
        createPost(data: { title: "bad", viewCount: $vc, author: { connect: { email: "roy@example.com" } } }) { id }
      }`,
      'token-roy@example.com',
      { vc: 1.5 },
    );
    expect(fractionalVariable.body.errors).toBeDefined();
    expect(JSON.stringify(fractionalVariable.body.errors)).toContain('BigInt');

    const survivor = await prisma.post.findMany({ where: { title: 'bad' } });
    expect(survivor).toEqual([]);
  });

  it('serializes the field as a string in subscription events', async () => {
    const address = app.getHttpServer().address() as { port: number };
    const wsClient: Client = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-roy@example.com' },
    });
    const events: any[] = [];
    const errors: unknown[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { postEvents { type entity { title viewCount } } }' },
      { next: (v) => events.push(v), error: (e) => errors.push(e), complete: () => undefined },
    );
    await sleep(400);

    await gql(`mutation {
      createPost(data: {
        title: "streamed"
        viewCount: "70368744177664"
        author: { connect: { email: "roy@example.com" } }
      }) { id }
    }`);
    await sleep(400);
    unsubscribe();
    await wsClient.dispose();

    expect(errors).toEqual([]);
    expect(events).toHaveLength(1);
    expect(events[0].data.postEvents.entity).toEqual({ title: 'streamed', viewCount: '70368744177664' });
  });
});
