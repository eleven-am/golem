import { INestApplication } from '@nestjs/common';
import { Test, TestingModule } from '@nestjs/testing';
import request from 'supertest';
import { ApolloDriver, ApolloDriverConfig } from '@nestjs/apollo';
import { GraphQLModule } from '@nestjs/graphql';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { AuthorizationModule } from '@eleven-am/authorizer';
import { GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';
import { GOLEM_GRAPHQL, GolemModule } from '@eleven-am/golem';
import type { GolemGraphQLArtifacts } from '@eleven-am/golem';
import { CompiledReadEvent, GolemEngine } from '@eleven-am/golem-core';
import { DemoAuthenticator, DemoRules } from '../src/auth';
import { getDatamodel } from '../src/generated/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import {
  bootDemoApp,
  databaseFileFor,
  provisionDatabase,
  removeDatabaseFiles,
  shutdownDemoApp,
} from './harness';

describe('the compiled aggregate path (e2e)', () => {
  let app: INestApplication;
  let moduleRef: TestingModule;
  let engine: GolemEngine;
  let events: CompiledReadEvent[];
  let release: () => void;

  beforeAll(async () => {
    const context = await bootDemoApp(__filename);
    app = context.app;
    moduleRef = context.moduleRef;
    engine = moduleRef.get<GolemEngine>('GOLEM_ENGINE');
  });

  afterAll(async () => {
    await shutdownDemoApp(app, __filename);
  });

  beforeEach(() => {
    events = [];
    release = engine.observeCompiledRead((event) => events.push(event));
  });

  afterEach(() => {
    release();
  });

  function gql(query: string, email = 'roy@example.com') {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', `token-${email}`)
      .send({ query })
      .expect(200);
  }

  it('compiles the aggregate the GraphQL surface asks for, rather than handing it to Prisma', async () => {
    const response = await gql(`{
      postsAggregate(measures: {
        count: true
        countFields: [rating]
        sum: [viewCount]
        avg: [viewCount]
        min: [title, createdAt]
        max: [rating]
      }) {
        count
        countBy { rating }
        sum { viewCount }
        avg { viewCount }
        min { title }
        max { rating }
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsAggregate).toEqual({
      count: 3,
      countBy: { rating: 2 },
      sum: { viewCount: '9007199254741098' },
      avg: { viewCount: 3002399751580366 },
      min: { title: 'Draft post' },
      max: { rating: '0.2' },
    });
    expect(events.map((event) => `${event.operation}:${event.outcome}`)).toEqual([
      'aggregate:compiled',
    ]);
    expect(events[0].sql).toContain('from "Post" as "t0"');
  });

  it('carries the policy predicate into the compiled aggregate', async () => {
    const open = await gql('{ postsAggregate(measures: { count: true }) { count } }');
    expect(open.body.data.postsAggregate.count).toBe(3);

    events.length = 0;
    const scoped = await gql(
      '{ postsAggregate(measures: { count: true }) { count } }',
      'guest@example.com',
    );
    expect(scoped.body.data.postsAggregate.count).toBe(2);
    expect(events[0].outcome).toBe('compiled');
    expect(events[0].sql).toContain('"published"');
  });

  it('compiles the grouping the GraphQL surface asks for, having and order included', async () => {
    const response = await gql(`{
      postsGrouped(
        by: [published]
        measures: { count: true, sum: [viewCount] }
        having: { count: { gte: 1 } }
        orderBy: { sum: { viewCount: desc } }
      ) {
        key { published }
        count
        sum { viewCount }
      }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsGrouped).toEqual([
      { key: { published: true }, count: 2, sum: { viewCount: '9007199254741093' } },
      { key: { published: false }, count: 1, sum: { viewCount: '5' } },
    ]);
    expect(events.map((event) => `${event.operation}:${event.outcome}`)).toEqual([
      'groupBy:compiled',
    ]);
    expect(events[0].sql).toContain('group by');
  });

  it('groups by an enum dimension on the compiled path', async () => {
    const response = await gql(`{
      postsGrouped(by: [type], measures: { count: true }) { key { type } count }
    }`);

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsGrouped).toEqual([
      { key: { type: 'PERSONAL' }, count: 3 },
    ]);
    expect(events[0].outcome).toBe('compiled');
  });
});

describe('the group cap over the compiled path (e2e)', () => {
  let app: INestApplication;

  beforeAll(async () => {
    const databaseFile = provisionDatabase(__filename.replace('.e2e', '.cap.e2e'));
    const moduleRef = await Test.createTestingModule({
      providers: [DemoRules],
      imports: [
        AuthorizationModule.forRootAsync({
          inject: [GolemPrismaService],
          useFactory: (prisma: GolemPrismaService) => new DemoAuthenticator(prisma),
        }),
        GolemModule.forRoot({
          client: GolemPrismaService,
          prismaOptions: { adapter: new PrismaBetterSqlite3({ url: `file:${databaseFile}` }) },
          datamodel: getDatamodel(),
          models: {
            Post: { aggregations: { dimensions: ['title'], maxGroups: 2 } },
            PostTag: false,
          },
          authorization: GolemAuthorizationAdapter,
        }),
        GraphQLModule.forRootAsync<ApolloDriverConfig>({
          driver: ApolloDriver,
          inject: [GOLEM_GRAPHQL],
          useFactory: (golem: GolemGraphQLArtifacts) => ({
            typeDefs: golem.typeDefs,
            transformResolvers: golem.transformResolvers,
            fieldResolverEnhancers: golem.fieldResolverEnhancers,
          }),
        }),
      ],
    }).compile();
    app = moduleRef.createNestApplication();
    await app.init();
    await seed(app.get(GolemPrismaService));
  });

  afterAll(async () => {
    await app.close();
    removeDatabaseFiles(databaseFileFor(__filename.replace('.e2e', '.cap.e2e')));
  });

  function gql(query: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', 'token-roy@example.com')
      .send({ query });
  }

  it('refuses a grouping wider than the cap rather than handing back a truncated one', async () => {
    const response = await gql(
      '{ postsGrouped(by: [title], measures: { count: true }) { key { title } count } }',
    );

    expect(response.body.data).toBeFalsy();
    expect(response.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
    expect(response.body.errors[0].message).toMatch(/matched more than 2 groups/);
  });

  it('answers exactly the take it was given, which is how the cap sees the overflow', async () => {
    const response = await gql(
      '{ postsGrouped(by: [title], measures: { count: true }, take: 2) { key { title } } }',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.postsGrouped).toHaveLength(2);
  });
});
