package p8mutation

import "time"

// docsReleaseCompatibilityMutations is intentionally isolated from provider,
// runtime, and observation records. Each mutation changes one reviewable
// documentation, compatibility, upgrade, or release boundary and names the
// exact semantic P8 gate that must kill it.
func docsReleaseCompatibilityMutations() []Mutation {
	gate := func(pkg, test string) Gate {
		return Gate{Directory: "go", Package: pkg, Test: test}
	}
	providerGate := func(pkg, test string) Gate {
		return Gate{
			Directory: "go",
			Package:   pkg,
			Test:      test,
			Required:  []string{"GOLEM_TEST_POSTGRES_DSN", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
		}
	}
	return []Mutation{
		{
			Label: "EXAMPLE_USES_LOCAL_REPLACE", Summary: "make the checked consumer example depend on an unpublished local checkout",
			Patches: []Patch{{
				Path:   "go/examples/social/go.mod",
				Before: "go 1.25.0\n\nrequire (",
				After:  "go 1.25.0\n\nreplace github.com/eleven-am/golem/go => ../../\n\nrequire (",
			}},
			Gate: gate("./cmd/golem", "TestP8ExampleContainsNoInternalImportOrOrdinaryResolverClone"), Timeout: 2 * time.Minute,
		},
		{
			Label: "EXAMPLE_HANDWRITES_CRUD_RESOLVER", Summary: "hide an ordinary CRUD resolver clone in the handwritten example extension",
			Patches: []Patch{{
				Path:   "go/examples/social/social/extensions.go",
				Before: "\treturn result, err\n}\n",
				After:  "\treturn result, err\n}\n\n// FindMany is an invalid handwritten clone of a generated ordinary root.\nfunc FindMany() {}\n",
			}},
			Gate: gate("./cmd/golem", "TestP8ExampleContainsNoInternalImportOrOrdinaryResolverClone"), Timeout: 2 * time.Minute,
		},
		{
			Label: "UPGRADE_REWRITES_EVENT_ID", Summary: "rewrite a frozen pending event identity while loading the upgrade corpus",
			Patches: []Patch{{
				Path:   "go/cmd/golem/p8_event_upgrade_test.go",
				Before: "func p8SeedEventUpgradeState(t *testing.T, database *sqlx.DB, providerName string, event mutationfact.OutboxRow) {\n\tt.Helper()\n",
				After:  "func p8SeedEventUpgradeState(t *testing.T, database *sqlx.DB, providerName string, event mutationfact.OutboxRow) {\n\tt.Helper()\n\tevent.EventID = \"71000000-0000-4000-8000-000000000099\"\n",
			}},
			Gate: providerGate("./cmd/golem", "TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents"), Timeout: 8 * time.Minute,
		},
		{
			Label: "UPGRADE_ADVANCES_LEDGER_BEFORE_VERIFY", Summary: "write a terminal SQLite ledger row before final catalog verification",
			Patches: []Patch{{
				Path:   "go/internal/provider/sqlite/migrate.go",
				Before: "\tfor stepIndex, step := range plan.steps {\n\t\tfor statementIndex, statement := range step.statements {\n\t\t\tif _, err := transaction.ExecContext(ctx, statement); err != nil {\n\t\t\t\treturn fmt.Errorf(\"sqlite migration step %d statement %d: %w\", stepIndex, statementIndex, err)\n\t\t\t}\n\t\t}\n\t}\n\tif err := verifyForeignKeys(ctx, transaction); err != nil {\n",
				After:  "\tfor stepIndex, step := range plan.steps {\n\t\tfor statementIndex, statement := range step.statements {\n\t\t\tif _, err := transaction.ExecContext(ctx, statement); err != nil {\n\t\t\t\treturn fmt.Errorf(\"sqlite migration step %d statement %d: %w\", stepIndex, statementIndex, err)\n\t\t\t}\n\t\t}\n\t}\n\tif err := writeTerminalLedger(ctx, transaction, manifest, len(ledger), entry); err != nil {\n\t\treturn err\n\t}\n\tif err := verifyForeignKeys(ctx, transaction); err != nil {\n",
			}},
			Gate: providerGate("./cmd/golem", "TestP8UpgradePreservesAuthorizationMigrationChainAndPendingEvents"), Timeout: 8 * time.Minute,
		},
		{
			Label: "UNKNOWN_CODEC_BEST_EFFORT_DECODE", Summary: "reinterpret an unknown persisted fact version as the oldest supported codec",
			Patches: []Patch{{
				Path:   "go/internal/mutation/fact/codec.go",
				Before: "\tversion, err := d.u16()\n\tif err != nil || version != FormatVersionV1 && version != FormatVersionV2 {\n\t\treturn Envelope{}, d.fail(\"unsupported fact version %d\", version)\n\t}\n",
				After:  "\tversion, err := d.u16()\n\tif err != nil {\n\t\treturn Envelope{}, d.fail(\"unsupported fact version %d\", version)\n\t}\n\tif version != FormatVersionV1 && version != FormatVersionV2 {\n\t\tversion = FormatVersionV1\n\t}\n",
			}},
			Gate: gate("./internal/p8oracle/rejection", "TestP8UnsupportedPersistedVersionNeverReinterpreted"), Timeout: 2 * time.Minute,
		},
		{
			Label: "PATCH_BREAKS_GENERATED_ABI", Summary: "add an exported symbol to the frozen generated Go surface in a patch candidate",
			Patches: []Patch{{
				Path:   "go/examples/social/social/zz_golem_models.gen.go",
				Before: "\ttime \"time\"\n)\n\nvar GolemGeneratedCommentDescriptor",
				After:  "\ttime \"time\"\n)\n\nvar P8GeneratedCompatibilityMutation string\n\nvar GolemGeneratedCommentDescriptor",
			}},
			Gate: gate("./internal/compatibility", "TestP8GeneratedAndGraphQLCompatibilityGate"), Timeout: 3 * time.Minute,
		},
		{
			Label: "PATCH_BREAKS_GRAPHQL_SCHEMA", Summary: "rename a frozen GraphQL response field in a patch candidate",
			Patches: []Patch{{
				Path:   "go/examples/social/social/zz_golem_graphql.schema.graphqls",
				Before: "type Post {\n  id: UUID\n  authorID: UUID\n  title: String\n",
				After:  "type Post {\n  id: UUID\n  authorID: UUID\n  renamedTitle: String\n",
			}},
			Gate: gate("./internal/compatibility", "TestP8GeneratedAndGraphQLCompatibilityGate"), Timeout: 3 * time.Minute,
		},
		{
			Label: "RELEASE_FROM_MOVING_BRANCH", Summary: "allow release inspection when HEAD has moved beyond the signed tag target",
			Patches: []Patch{{
				Path:   "go/internal/release/release.go",
				Before: "\thead, err := output(ctx, repository, nil, \"git\", \"rev-parse\", \"--verify\", \"HEAD^{commit}\")\n\tif err != nil || head != commit {\n",
				After:  "\thead, err := output(ctx, repository, nil, \"git\", \"rev-parse\", \"--verify\", \"HEAD^{commit}\")\n\tif err != nil || head == \"\" {\n",
			}},
			Gate: gate("./internal/release", "TestP8ReleaseTagAndVersionAgreement"), Timeout: 3 * time.Minute,
		},
		{
			Label: "RELEASE_TAG_MODULE_MISMATCH", Summary: "ignore disagreement between the nested-module identity and the signed release manifest",
			Patches: []Patch{{
				Path:   "go/internal/release/release.go",
				Before: "\tif err != nil || module != ModulePath || !commitPattern.MatchString(commit) || manifest.Module != module || manifest.Release.Development ||",
				After:  "\tif err != nil || !commitPattern.MatchString(commit) || manifest.Release.Development ||",
			}},
			Gate: gate("./internal/release", "TestP8ReleaseTagAndVersionAgreement"), Timeout: 3 * time.Minute,
		},
		{
			Label: "REPLACE_EXISTING_RELEASE_BYTES", Summary: "accept different staged bytes for an already published release version",
			Patches: []Patch{{
				Path:   "go/internal/release/release.go",
				Before: "\t\tif compareErr != nil || !equal {\n\t\t\treturn fail(CodeReplacement)\n\t\t}\n",
				After:  "\t\tif compareErr != nil || equal && false {\n\t\t\treturn fail(CodeReplacement)\n\t\t}\n",
			}},
			Gate: gate("./internal/release", "TestP8ExistingVersionArtifactReplacementRefused"), Timeout: 2 * time.Minute,
		},
		{
			Label: "DOCUMENT_UNSUPPORTED_FEATURE", Summary: "claim federation and MySQL support in the public Go quickstart",
			Patches: []Patch{{
				Path:   "docs/golem-go/QUICKSTART.md",
				Before: "The first release does not provide federation, schema stitching, MySQL,",
				After:  "The first release provides federation and supports MySQL, schema stitching,",
			}},
			Gate: gate("./cmd/golem", "TestP8IntentionalBoundaryDisclosureCorpus"), Timeout: 2 * time.Minute,
		},
		{
			Label: "DOC_SNIPPET_NOT_COMPILED", Summary: "publish a Go quickstart snippet with an unknown SQLite configuration field",
			Patches: []Patch{{
				Path:   "docs/golem-go/QUICKSTART.md",
				Before: "return providersqlite.Open(ctx, providersqlite.Config{DataSourceName: dsn})",
				After:  "return providersqlite.Open(ctx, providersqlite.Config{UnknownDataSourceName: dsn})",
			}},
			Gate: gate("./cmd/golem", "TestP8EveryPublicSnippetTypeChecks"), Timeout: 5 * time.Minute,
		},
	}
}
