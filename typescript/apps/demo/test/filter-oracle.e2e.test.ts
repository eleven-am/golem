import { INestApplication } from '@nestjs/common';
import { Test } from '@nestjs/testing';
import request from 'supertest';
import { ApolloDriver, ApolloDriverConfig } from '@nestjs/apollo';
import { GraphQLModule } from '@nestjs/graphql';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { AuthorizationModule } from '@eleven-am/authorizer';
import { GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';
import { GOLEM_GRAPHQL, GolemModule } from '@eleven-am/golem';
import type { GolemGraphQLArtifacts } from '@eleven-am/golem';
import { DemoAuthenticator, DemoRules } from '../src/auth';
import { getDatamodel } from '../src/generated/golem';
import { GolemPrismaService } from '../src/generated/golem/client';
import { seed } from '../src/seed';
import { databaseFileFor, provisionDatabase, removeDatabaseFiles } from './harness';

describe('filtering and ordering by a field the caller may not read (e2e)', () => {
  let app: INestApplication;

  beforeAll(async () => {
    const databaseFile = provisionDatabase(__filename);
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
          models: { User: { aggregations: true }, PostTag: false },
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
    removeDatabaseFiles(databaseFileFor(__filename));
  });

  function query(email: string, source: string) {
    return request(app.getHttpServer())
      .post('/graphql')
      .set('authorization', `token-${email}`)
      .send({ query: source });
  }

  it('refuses a prefix search over a field this caller reads only on their own row', async () => {
    const response = await query(
      'ada@example.com',
      '{ users(where: { phone: { startsWith: "+1" } }) { email phone } }',
    );

    expect(response.body.data).toBeFalsy();
    expect(response.body.errors[0].message).toMatch(/phone/);
    expect(response.body.errors[0].message).toContain(
      'Cannot filter or order by field "phone" on User',
    );
  });

  it('refuses the same search buried in an OR branch', async () => {
    const response = await query(
      'ada@example.com',
      '{ users(where: { OR: [{ email: { startsWith: "z" } }, { phone: { startsWith: "+1" } }] }) { email } }',
    );

    expect(response.body.data).toBeFalsy();
    expect(response.body.errors[0].message).toContain(
      'Cannot filter or order by field "phone" on User',
    );
  });

  it('refuses a search over a field this caller can never read', async () => {
    const response = await query(
      'guest@example.com',
      '{ users(where: { phone: { startsWith: "+1" } }) { email } }',
    );

    expect(response.body.data).toBeFalsy();
    expect(response.body.errors[0].message).toContain(
      'Cannot filter or order by field "phone" on User',
    );
  });

  it('refuses to order rows by a field the caller may not read on every row', async () => {
    const response = await query(
      'ada@example.com',
      '{ users(orderBy: [{ phone: asc }]) { email } }',
    );

    expect(response.body.data).toBeFalsy();
    expect(response.body.errors[0].message).toContain(
      'Cannot filter or order by field "phone" on User',
    );
  });

  it('refuses to count rows behind a filter on that field', async () => {
    const response = await query(
      'ada@example.com',
      '{ usersAggregate(where: { phone: { startsWith: "+1" } }, measures: { count: true }) { count } }',
    );

    expect(response.body.data).toBeFalsy();
    expect(response.body.errors[0].message).toMatch(/phone/);
  });

  it('still answers a filter and an order over fields the caller may read', async () => {
    const response = await query(
      'ada@example.com',
      '{ users(where: { email: { startsWith: "ada" } }, orderBy: [{ email: asc }]) { email phone } }',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', phone: '+44-555-0200' },
    ]);
  });

  it('leaves a caller with no field restriction able to search the field', async () => {
    const response = await query(
      'roy@example.com',
      '{ users(where: { phone: { startsWith: "+44" } }) { email phone } }',
    );

    expect(response.body.errors).toBeUndefined();
    expect(response.body.data.users).toEqual([
      { email: 'ada@example.com', phone: '+44-555-0200' },
    ]);
  });
});
