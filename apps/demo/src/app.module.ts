import { Module } from '@nestjs/common';
import { ApolloDriver, ApolloDriverConfig } from '@nestjs/apollo';
import { GraphQLModule } from '@nestjs/graphql';
import { GraphQLSchema } from 'graphql';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { AuthorizationModule } from '@eleven-am/authorizer';
import { GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';
import { GOLEM_SCHEMA, GolemModule } from '@eleven-am/golem';
import { DemoAuthenticator, DemoRules } from './auth';
import { getDatamodel } from './generated/golem';
import { GolemPrismaService } from './generated/golem/client';
import { UserExtension } from './user.extension';
import { UserHooks } from './user.hooks';

@Module({
  providers: [UserHooks, DemoRules],
  imports: [
    AuthorizationModule.forRootAsync({
      inject: [GolemPrismaService],
      useFactory: (prisma: GolemPrismaService) => new DemoAuthenticator(prisma),
    }),
    GolemModule.forRoot({
      client: GolemPrismaService,
      prismaOptions: { adapter: new PrismaBetterSqlite3({ url: 'file:./prisma/dev.db' }) },
      datamodel: getDatamodel(),
      defaults: {
        maxTake: 100,
        checkWriteResults: true,
        checkReadFields: true,
      },
      models: {
        User: { subscriptions: true, hidden: ['apiKey'], immutable: ['email'] },
        Post: { subscriptions: true },
        Profile: { operations: ['findOne', 'findMany', 'create'] },
      },
      extensions: [UserExtension],
      authorization: GolemAuthorizationAdapter,
    }),
    GraphQLModule.forRootAsync<ApolloDriverConfig>({
      driver: ApolloDriver,
      inject: [GOLEM_SCHEMA],
      useFactory: (schema: GraphQLSchema) => ({
        schema,
        subscriptions: { 'graphql-ws': true },
      }),
    }),
  ],
})
export class AppModule {}
