import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import { Client, createClient } from 'graphql-ws';
import request from 'supertest';
import WebSocket from 'ws';
import type { ApolloDriverConfig } from '@nestjs/apollo';
import { golemSharedContext } from '@eleven-am/golem';
import { AppModule } from '../src/app.module';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { databaseFileFor, provisionDatabase, removeDatabaseFiles } from './harness';

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

async function bootApp(graphql: Partial<ApolloDriverConfig>): Promise<INestApplication> {
  const databaseFile = provisionDatabase(__filename);
  const moduleRef = await Test.createTestingModule({
    imports: [AppModule.forDatabase(`file:${databaseFile}`, graphql)],
  }).compile();
  const app = moduleRef.createNestApplication();
  await app.init();
  await seed(app.get(GolemPrismaService));
  return app;
}

function gql(app: INestApplication, token: string, query: string) {
  return request(app.getHttpServer())
    .post('/graphql')
    .set('authorization', `token-${token}`)
    .send({ query });
}

const USERS_QUERY = '{ users(take: 3) { id email postCount } }';

describe('context boundary (e2e)', () => {
  afterAll(() => {
    removeDatabaseFiles(databaseFileFor(__filename));
  });

  describe('a static context object', () => {
    let app: INestApplication;

    beforeAll(async () => {
      app = await bootApp({ context: {} });
    });

    afterAll(async () => {
      await app.close();
    });

    it('serves the first request and refuses every request after it', async () => {
      const first = await gql(app, 'roy@example.com', USERS_QUERY).expect(200);
      expect(first.body.errors).toBeUndefined();
      expect(first.body.data.users.length).toBeGreaterThan(0);

      const second = await gql(app, 'guest@example.com', USERS_QUERY).expect(200);
      expect(second.body.data ?? undefined).toBeUndefined();
      expect(second.body.errors[0].message).toContain('static context object');
      expect(second.body.errors[0].message).toContain('context: ({ req }) => ({ req })');

      const third = await gql(app, 'roy@example.com', USERS_QUERY).expect(200);
      expect(third.body.errors[0].message).toContain('static context object');
    });
  });

  describe('a context function building one object per request', () => {
    let app: INestApplication;
    let socket: Client;

    beforeAll(async () => {
      app = await bootApp({
        context: (raw: { req?: unknown }) => ({ req: raw?.req ?? raw }),
        allowBatchedHttpRequests: true,
      });
      await app.listen(0);
      const address = app.getHttpServer().address() as { port: number };
      socket = createClient({
        url: `ws://127.0.0.1:${address.port}/graphql`,
        webSocketImpl: WebSocket,
        connectionParams: { authorization: 'token-roy@example.com' },
      });
    });

    afterAll(async () => {
      await socket.dispose();
      await app.close();
    });

    function wsExecute(query: string): Promise<Array<Record<string, unknown>>> {
      return new Promise((resolve, reject) => {
        const results: Array<Record<string, unknown>> = [];
        socket.subscribe(
          { query },
          {
            next: (value) => results.push(value as Record<string, unknown>),
            error: reject,
            complete: () => resolve(results),
          },
        );
      });
    }

    it('serves every caller including batched computed fields', async () => {
      for (const token of ['roy@example.com', 'guest@example.com', 'roy@example.com']) {
        const response = await gql(app, token, USERS_QUERY).expect(200);
        expect(response.body.errors).toBeUndefined();
        expect(response.body.data.users.length).toBeGreaterThan(0);
      }
    });

    it('serves a batched HTTP request of several operations', async () => {
      const response = await request(app.getHttpServer())
        .post('/graphql')
        .set('authorization', 'token-roy@example.com')
        .send([
          { query: '{ users(take: 1) { id postCount } }' },
          { query: '{ users(take: 2) { email postCount } }' },
        ])
        .expect(200);

      expect(response.body).toHaveLength(2);
      for (const result of response.body) {
        expect(result.errors).toBeUndefined();
        expect(result.data.users.length).toBeGreaterThan(0);
      }
    });

    it('serves operations multiplexed over one graphql-ws connection', async () => {
      const first = await wsExecute('{ users(take: 2) { id postCount } }');
      const betweenOperations = await gql(app, 'guest@example.com', USERS_QUERY).expect(200);
      const second = await wsExecute('{ users(take: 2) { email postCount } }');

      expect((first[0] as { errors?: unknown }).errors).toBeUndefined();
      expect(betweenOperations.body.errors).toBeUndefined();
      expect((second[0] as { errors?: unknown }).errors).toBeUndefined();
    });

    it('keeps a long-lived subscription delivering across later HTTP requests', async () => {
      const events: unknown[] = [];
      const errors: unknown[] = [];
      const unsubscribe = socket.subscribe(
        { query: 'subscription { userEvents { type id } }' },
        {
          next: (value) => events.push(value),
          error: (error) => errors.push(error),
          complete: () => undefined,
        },
      );
      await sleep(300);

      await gql(
        app,
        'roy@example.com',
        'mutation { createUser(data: { email: "boundary-one@example.com" }) { id } }',
      ).expect(200);
      await gql(
        app,
        'roy@example.com',
        'mutation { createUser(data: { email: "boundary-two@example.com" }) { id } }',
      ).expect(200);
      await sleep(400);
      unsubscribe();

      expect(errors).toEqual([]);
      expect(events.length).toBeGreaterThanOrEqual(2);
    });
  });

  describe('a context deliberately shared across requests', () => {
    let app: INestApplication;

    beforeAll(async () => {
      const shared: Record<PropertyKey, unknown> = { [golemSharedContext]: true };
      app = await bootApp({ context: () => shared });
    });

    afterAll(async () => {
      await app.close();
    });

    it('is left alone', async () => {
      for (let attempt = 0; attempt < 3; attempt += 1) {
        const response = await gql(app, 'roy@example.com', USERS_QUERY).expect(200);
        expect(response.body.errors).toBeUndefined();
        expect(response.body.data.users.length).toBeGreaterThan(0);
      }
    });
  });
});
