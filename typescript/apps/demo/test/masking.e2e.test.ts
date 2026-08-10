import { INestApplication } from '@nestjs/common';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import { GolemPrismaService } from '../src/generated/golem/client';
import { bootDemoApp, shutdownDemoApp } from './harness';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function ctxFor(email: string) {
  return { req: { headers: { authorization: `token-${email}` } } };
}

describe('read-side field masking (e2e)', () => {
  let app: INestApplication;
  let prisma: GolemPrismaService;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    prisma = context.prisma;
    await app.listen(0);
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  function gql(query: string, token: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', token)
      .send({ query });
  }

  it('masks conditionally readable fields per row', async () => {
    const response = await gql(
      '{ users(orderBy: [{ email: asc }]) { email phone } }',
      'token-ada@example.com',
    );
    expect(response.body.errors).toBeUndefined();
    const byEmail = Object.fromEntries(
      response.body.data.users.map((u: { email: string; phone: string | null }) => [u.email, u.phone]),
    );
    expect(byEmail['ada@example.com']).toBe('+44-555-0200');
    expect(byEmail['roy@example.com']).toBeNull();
  });

  it('rejects never-readable fields at request time, naming the field', async () => {
    const response = await gql('{ users { phone } }', 'token-guest@example.com');
    expect(response.body.errors[0].extensions.code).toBe('FORBIDDEN');
    expect(response.body.errors[0].message).toContain('Cannot read field "phone" on User');
  });

  it('masks and strips injected columns through forContext', async () => {
    const rows = await prisma.forContext(ctxFor('ada@example.com')).user.findMany({
      where: { email: { in: ['ada@example.com', 'roy@example.com'] } },
      orderBy: { email: 'asc' },
      select: { email: true, phone: true },
    });
    expect(rows[0].phone).toBe('+44-555-0200');
    expect(rows[1].phone).toBeNull();
    expect('id' in rows[0]).toBe(false);
    expect('id' in rows[1]).toBe(false);
  });

  it('masks subscription event entities per subscriber', async () => {
    const address = app.getHttpServer().address();
    const wsClient: Client = createClient({
      url: `ws://127.0.0.1:${address.port}/graphql`,
      webSocketImpl: WebSocket,
      connectionParams: { authorization: 'token-ada@example.com' },
    });
    const events: any[] = [];
    const unsubscribe = wsClient.subscribe(
      { query: 'subscription { userEvents { type entity { email phone } } }' },
      { next: (v) => events.push(v), error: () => undefined, complete: () => undefined },
    );
    await sleep(400);

    await prisma.user.update({
      where: { email: 'roy@example.com' },
      data: { name: 'Roy R.' },
    });
    await sleep(400);
    unsubscribe();
    await wsClient.dispose();

    expect(events).toHaveLength(1);
    expect(events[0].data.userEvents.entity).toEqual({
      email: 'roy@example.com',
      phone: null,
    });
    await prisma.user.update({ where: { email: 'roy@example.com' }, data: { name: 'Roy' } });
  });

  it('leaves unrestricted callers untouched', async () => {
    const response = await gql(
      '{ user(where: { email: "ada@example.com" }) { phone } }',
      'token-roy@example.com',
    );
    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.user.phone).toBe('+44-555-0200');
  });
});
