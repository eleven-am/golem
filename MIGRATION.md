# Migrating to 0.1.0

This release changes the Nest GraphQL integration, strengthens authorization defaults, and updates the generated Prisma client. Existing applications should make the following changes.

## Replace `GOLEM_SCHEMA` with `GOLEM_GRAPHQL`

Golem now lets Nest own custom resolver execution while merging Golem's generated resolvers into the same GraphQL module.

```typescript
import { GOLEM_GRAPHQL } from '@eleven-am/golem';
import type { GolemGraphQLArtifacts } from '@eleven-am/golem';

GraphQLModule.forRootAsync<ApolloDriverConfig>({
  driver: ApolloDriver,
  inject: [GOLEM_GRAPHQL],
  useFactory: (golem: GolemGraphQLArtifacts) => ({
    typeDefs: golem.typeDefs,
    transformResolvers: golem.transformResolvers,
    subscriptions: { 'graphql-ws': true },
  }),
})
```

Remove imports and injections of `GOLEM_SCHEMA` and `GraphQLSchema` from the old setup.

## Update custom operations

`@CustomQuery` and `@CustomMutation` methods are now real Nest GraphQL resolver methods. Use standard Nest parameter decorators and attach guards, pipes, interceptors, and filters normally.

```typescript
@UseGuards(SearchArticlesGuard)
@CustomQuery({ type: '[Article!]!', args: { term: 'String!' } })
searchArticles(@Args() args: { term: string }, @Context() context: unknown) {
  return this.prisma.forContext(context).article.findMany({
    where: { title: { contains: args.term } },
  });
}
```

Calls made through `forContext(context)` run Golem authorization and hooks. Plain generated-client calls remain intentional system-level Prisma access.

## Review authorization opt-outs

When an authorization provider is configured, `checkWriteResults` and `checkReadFields` now default to `true`. Applications using the Golem authorization adapter generally need no configuration change.

A deliberately row-policy-only provider can retain the previous behavior explicitly:

```typescript
GolemModule.forRoot({
  // ...
  authorization: RowPolicyProvider,
  defaults: {
    checkWriteResults: false,
    checkReadFields: false,
  },
})
```

Without those opt-outs, a custom authorization provider must implement the field classification and instance-check methods required by the enabled verification paths.

## Regenerate the Golem client

Run the generator after upgrading:

```bash
npx prisma generate
```

The regenerated client makes subscription events transaction-aware. Writes in interactive and batch `$transaction` calls publish only after commit and are discarded on rollback. Previously generated clients do not contain this transaction wrapper.

## Verify the upgrade

Run your build and test suite, paying particular attention to custom-operation guards and any application-owned Prisma transactions.
