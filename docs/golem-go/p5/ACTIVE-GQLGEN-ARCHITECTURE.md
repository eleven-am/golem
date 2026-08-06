# Active gqlgen execution architecture

Status: **controlling P5-B architecture**

The pinned gqlgen executable schema is part of the live request path. It is not
an auxiliary validation artifact and it may not duplicate authorization,
planning, SQL, hooks, or transactions.

## Operation path

For both `GraphQLServer.Handler()` and the direct execution API, one operation
uses this order:

1. Golem's bounded transport parses the request, applies the public request,
   document, selection, input, paging, and complexity limits, and validates the
   document against the generated schema.
2. The Golem operation compiler binds the complete operation before opening a
   caller or invoking P3/P4. A refusal therefore performs no engine work.
3. Exactly one principal creates exactly one caller and one computed-loader
   scope. Query roots execute only through P3; mutation roots execute serially
   only through P4 and stop after the first failed non-null mutation root.
4. The prepared dynamic result and sanitized Golem errors are installed in an
   operation-local context. No result or caller is stored on the server.
5. The pinned executable returned by the generated `NewExecutableSchema`
   performs the live GraphQL root/field dispatch, selection serialization,
   null propagation, and response construction. Generated resolvers only read
   the prepared operation-local result by response name. They never authorize,
   open a database, start a transaction, or call P3/P4.
6. Golem merges its already-sanitized execution errors into the gqlgen response
   and closes the operation-local computed scope before returning.

This split keeps gqlgen active without turning per-field resolvers into a second
backend. Golem remains the single policy and data-execution authority, while
gqlgen remains the generated GraphQL executable and serializer.

## Generated artifacts

`golem generate` emits and atomically publishes all of the following without an
application-owned `gqlgen.yml` or resolver file:

- canonical SDL;
- pinned gqlgen executable-schema code;
- gqlgen input and enum models;
- Golem-generated resolver-root implementations for every generated root and
  output field;
- the opaque `App.GraphQL` adapter and typed computed-binding registry; and
- manifest ownership plus SDL, ContractIR, generator, template, and gqlgen
  identities.

All output object types bind to generated dynamic result objects. Resolver
lookups use gqlgen's current field response name, preserving aliases and
independent repeated occurrences. Container conversion may change only Go
slice/map wrappers; it must not JSON-round-trip exact scalar or JSON values.

## Forbidden shortcuts

- A generated executable that is never passed to the public server is dead code
  and does not satisfy P5-B.
- An operation interceptor that returns before `ExecutableSchema.Exec` does not
  satisfy P5-B.
- Generated resolvers may not receive `System`, `DB`, `Tx`, raw SQL, a policy
  evaluator, or an unrestricted runtime.
- The executable path may not create a second caller or computed scope.
- HTTP and direct execution may not use different compilation or execution
  semantics.
