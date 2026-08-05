# Phase 0 traceability

Every compatibility claim below points to the relocated TypeScript source. The
Go spike uses a Go-native authoring API while preserving TypeScript's
priority-ordered allow/deny outcomes.

| Phase 0 claim | TypeScript evidence | Go evidence |
|---|---|---|
| Authorization has four actions: read, create, update, delete | `typescript/packages/core/src/authorization.ts:1-26` | `rules.go`; `TestOperationMethodsExpressTheSocialWritePolicy` |
| Request filters and policy constraints combine with `AND` | `typescript/packages/core/src/authorization.ts:63-70`; `authorization.test.ts:77-94` | `TestFindManyContractFixtures/request-filter-*` |
| Authorization is part of the database request before pagination | `typescript/packages/core/src/operations.ts:1009-1048` forwards the merged `where` together with `take` and `skip` | `TestFindManyContractFixtures/authorization-precedes-pagination` |
| A system call is distinct from a caller-authorized call | `typescript/packages/core/src/authorization.test.ts:161-170`; `operations.ts:637-646` | `TestFindManyContractFixtures/system-bypasses-caller-policy` |
| Authorization state is scoped by request context | `typescript/packages/core/src/operations.ts:600-634`; `authorization.test.ts:173-184` | `TestPoliciesAreBuiltPerActor` |
| Fields need always/conditional/never classification | `typescript/packages/core/src/authorization.ts:29-38` | `Classify`; `TestFieldRulesClassifyAndEvaluatePerRow` |
| One condition vocabulary is evaluated in memory and rendered to SQL | `typescript/packages/policy/src/matcher.ts:5-9`; `operators.ts:86-100` | `Predicate`; `Evaluate`; `TestNormalizationIsStable` |
| SQLite executes the shared operator/relation matrix and agrees with the evaluator | `typescript/packages/policy/test/sql-agreement.sqlite.test.ts:46-88,106-147` | `TestEveryPhase0OperatorRequiresSQLiteAndPostgreSQLParity` records the future Go gate; no Go SQL agreement is claimed yet |
| PostgreSQL executes that shared matrix using its own dialect | `typescript/packages/policy/test/sql-agreement.postgres.test.ts:90-170,220-278` | Same capability gate; no Go SQL agreement is claimed yet |
| Scalar and relation operator names are explicit, validated definitions | `typescript/packages/policy/src/operators.ts:34-100` | `Operator`, typed fields, typed relation handles |
| The newest applicable rule has priority and an unconditional rule stops the older chain | `typescript/node_modules/@casl/ability/dist/esm/extra.mjs:79-101`; `typescript/packages/authorizer/src/field-constraint.ts:23-40` | `effectiveRuleChain`; 518 generated row/field chains in `TestGeneratedRuleChainsMatchTheTypeScriptCASLOracle`; reproducible with `testdata/generate_rule_oracle.mjs` |
| Model and field rules share one priority chain; a model grant applies to fields | `typescript/packages/authorizer/src/field-constraint.test.ts:59-169,220-264` | `EffectiveField`; `TestFieldRulesShareTheOrderedModelRuleChain` |
| A positive field rule contributes to the model action, while a field denial does not hide the row | CASL `Rule.matchesField` and `rulesFor` in `typescript/node_modules/@casl/ability/dist/esm/index.mjs`; adapter row constraints in `typescript/node_modules/@eleven-am/authorizer/dist/prisma.js` | `Effective`; `TestFieldGrantAlsoGrantsTheModelAction` |

## New Phase 0 proposals

These do not claim to reproduce an existing TypeScript API:

- separate `PostPolicy.Define` objects;
- generated `Field[M, V]` and relation handles;
- typed Go method names instead of CASL's string subjects and fields;
- default deny for an action with no reachable grant;
- unmentioned fields seeing only the model-wide rule chain;
- the canonical normalized JSON representation.

Each proposal has an executable test. It must be reviewed before it becomes a
production Go contract.
