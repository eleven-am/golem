import { Module } from '@nestjs/common';
import { ApolloDriver, ApolloDriverConfig } from '@nestjs/apollo';
import { GraphQLModule } from '@nestjs/graphql';
import { PrismaBetterSqlite3 } from '@prisma/adapter-better-sqlite3';
import { AuthorizationModule } from '@eleven-am/authorizer';
import { GolemAuthorizationAdapter } from '@eleven-am/golem-authorizer';
import { GOLEM_GRAPHQL, GolemModule } from '@eleven-am/golem';
import type { GolemGraphQLArtifacts } from '@eleven-am/golem';
import { DemoAuthenticator, DemoRules } from './auth';
import { getDatamodel } from './generated/golem';
import { GolemPrismaService } from './generated/golem/client';
import { UserExtension } from './user.extension';
import { UserHooks } from './user.hooks';
import { SearchPostsAccessModule } from './search-posts.guard';

@Module({
  providers: [UserHooks, DemoRules],
  imports: [
    AuthorizationModule.forRootAsync({
      inject: [GolemPrismaService],
      useFactory: (prisma: GolemPrismaService) => new DemoAuthenticator(prisma),
    }),
    GolemModule.forRoot({
      imports: [SearchPostsAccessModule],
      client: GolemPrismaService,
      prismaOptions: { adapter: new PrismaBetterSqlite3({ url: 'file:./prisma/dev.db' }) },
      datamodel: getDatamodel(),
      defaults: {
        maxTake: 100,
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
      inject: [GOLEM_GRAPHQL],
      useFactory: (golem: GolemGraphQLArtifacts) => ({
        typeDefs: golem.typeDefs,
        transformResolvers: golem.transformResolvers,
        subscriptions: { 'graphql-ws': true },
      }),
    }),
  ],
})
export class AppModule {}
