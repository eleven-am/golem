package runtime

// GolemGraphQLCallerCapability marks only principal-bound Caller values as
// eligible for generated custom GraphQL resolver dispatch. System, CallerTx,
// SystemTx, DB, and raw execution bindings intentionally lack this method.
func (*Caller[P, A]) GolemGraphQLCallerCapability() {}
