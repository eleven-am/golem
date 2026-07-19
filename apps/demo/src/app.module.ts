import { DynamicModule, Module } from '@nestjs/common';
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

const DEFAULT_DATABASE_URL = 'file:./prisma/dev.db';

@Module({})
export class AppModule {
  static forDatabase(databaseUrl: string = DEFAULT_DATABASE_URL): DynamicModule {
    return {
      module: AppModule,
      providers: [UserHooks, DemoRules],
      imports: [
        AuthorizationModule.forRootAsync({
          inject: [GolemPrismaService],
          useFactory: (prisma: GolemPrismaService) => new DemoAuthenticator(prisma),
        }),
        GolemModule.forRoot({
          imports: [SearchPostsAccessModule],
          client: GolemPrismaService,
          prismaOptions: { adapter: new PrismaBetterSqlite3({ url: databaseUrl }) },
          datamodel: getDatamodel(),
          defaults: {
            maxTake: 100,
          },
          models: {
            User: { subscriptions: true, hidden: ['apiKey'], immutable: ['email'] },
            Post: {
              subscriptions: true,
              aggregations: {
                dimensions: ['authorId', 'published', 'type'],
                maxGroups: 50,
              },
            },
            Profile: { operations: ['findOne', 'findMany', 'create'] },
            PostTag: false,
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
            fieldResolverEnhancers: golem.fieldResolverEnhancers,
            subscriptions: { 'graphql-ws': true },
          }),
        }),
      ],
    };
  }
}
