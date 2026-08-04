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

describe('per-field count authorization over GraphQL (e2e)', () => {
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

  it('refuses a per-field count over a field the caller can never read', async () => {
    const denied = await query('guest@example.com', `{
      usersAggregate(measures: { count: true, countFields: [phone] }) {
        count
        countBy { phone }
      }
    }`);

    expect(denied.body.errors[0].extensions.code).toBe('BAD_USER_INPUT');
    expect(denied.body.errors[0].message).toMatch(/phone/);
    expect(denied.body.data).toBeFalsy();

    const allowed = await query('guest@example.com', `{
      usersAggregate(measures: { count: true, countFields: [email] }) {
        count
        countBy { email }
      }
    }`);

    expect(allowed.body.errors).toBeUndefined();
    expect(allowed.body.data.usersAggregate.countBy.email).toBeGreaterThan(0);
  });
});
