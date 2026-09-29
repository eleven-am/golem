package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

func countAcceptancePosts(t *testing.T, profile mutationProviderAcceptanceFixture, id byte) int {
	t.Helper()
	var count int
	if err := profile.fixture.app.database.GetContext(context.Background(), &count, `SELECT COUNT(*) FROM `+profile.posts+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(id)); err != nil {
		t.Fatal(err)
	}
	return count
}

func assertNULValueDomainRefusal(t *testing.T, err error) {
	t.Helper()
	assertPublicUpsertCode(t, err, golem.CodeBadUserInput)
	chain := make([]string, 0, 4)
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		chain = append(chain, cause.Error())
	}
	joined := strings.Join(chain, " <- ")
	if !strings.Contains(joined, "contains NUL, which is outside the portable provider value domain") || strings.Contains(joined, "22021") {
		t.Fatalf("NUL refusal chain=%q; want the binder's value-domain refusal before SQL", joined)
	}
}

func TestStringValuesContainingNULAreRefusedBeforeSQLAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		system := fixture.app.System()
		_, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(81, golem.UUID{15: 1}, "nul\x00byte"))
		assertNULValueDomainRefusal(t, err)
		if count := countAcceptancePosts(t, profile, 81); count != 0 {
			t.Fatalf("NUL create stored %d rows", count)
		}
		if _, err := SystemCreate(ctx, system, fixture.postDescriptor, fixture.createPost(82, golem.UUID{15: 1}, "plain")); err != nil {
			t.Fatal(err)
		}
		_, err = SystemUpdate(ctx, system, fixture.postDescriptor, fixture.target(82), fixture.updateTitle("nul\x00byte"))
		assertNULValueDomainRefusal(t, err)
		var title string
		if err := fixture.app.database.GetContext(ctx, &title, `SELECT "title" FROM `+profile.posts+` WHERE "id"=`+profile.placeholder(1), mutationResultUUIDText(82)); err != nil || title != "plain" {
			t.Fatalf("NUL update changed title=%q err=%v", title, err)
		}
	})
}

func TestUpsertRefusesCreateInputThatContradictsTargetAcrossProviders(t *testing.T) {
	runMutationProviderAcceptanceProfiles(t, func(t *testing.T, profile mutationProviderAcceptanceFixture) {
		ctx := context.Background()
		fixture := profile.fixture
		system := fixture.app.System()
		_, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(95), fixture.createPost(96, golem.UUID{15: 1}, "mismatch"), fixture.updateTitle("mismatch2"))
		if err == nil {
			t.Fatal("upsert created a row its target never named")
		}
		assertPublicUpsertCode(t, err, golem.CodeBadUserInput)
		if count := countAcceptancePosts(t, profile, 95) + countAcceptancePosts(t, profile, 96); count != 0 {
			t.Fatalf("contradicting upsert stored %d rows", count)
		}
		if _, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(97), fixture.createPost(97, golem.UUID{15: 1}, "agreeing"), fixture.updateTitle("unused")); err != nil {
			t.Fatal(err)
		}
		if count := countAcceptancePosts(t, profile, 97); count != 1 {
			t.Fatalf("agreeing upsert stored %d rows", count)
		}
		if _, err := SystemUpsert(ctx, system, fixture.postDescriptor, fixture.target(97), fixture.createPost(98, golem.UUID{15: 1}, "unused"), fixture.updateTitle("updated")); err != nil {
			t.Fatalf("update branch refused an unexecuted create input: %v", err)
		}
		if count := countAcceptancePosts(t, profile, 98); count != 0 {
			t.Fatalf("update branch created %d rows", count)
		}
	})
}

type exactValueProfile struct {
	fixture     schematest.Fixture
	database    *sqlx.DB
	provider    policyir.Provider
	proof       policysql.CapabilityProof
	prefix      string
	outbox      string
	placeholder func(int) string
}

func runExactValueProfiles(t *testing.T, operation func(*testing.T, exactValueProfile)) {
	t.Helper()
	runExactValueProfilesFor(t, schematest.NewMutationExactValues, operation)
}

func runExactValueProfilesFor(t *testing.T, newFixture func(testing.TB) schematest.Fixture, operation func(*testing.T, exactValueProfile)) {
	t.Helper()
	ctx := context.Background()
	t.Run("sqlite", func(t *testing.T) {
		fixture := newFixture(t)
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "value-domain.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
			t.Fatal(err)
		}
		proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
		if err != nil {
			t.Fatal(err)
		}
		operation(t, exactValueProfile{fixture: fixture, database: database, provider: policyir.ProviderSQLite, proof: proof, outbox: `"_golem_outbox"`, placeholder: func(int) string { return "?" }})
	})
	for _, profile := range []struct{ name, env string }{{"postgresql-c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := testenv.DisposablePostgreSQL(t, profile.env)
			fixture := newFixture(t)
			sequence := mutationOutboxNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_value_domain_%d", sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_value_domain_system_%d", sequence))
			schema := fixture.PostgreSQL
			schema.Namespace.Name, schema.System.Namespace.Name = applicationNamespace, systemNamespace
			bundle := postgresRuntimeBundle(t, fixture, schema)
			registry, err := policyschema.New(bundle)
			if err != nil {
				t.Fatal(err)
			}
			fixture.Bundle, fixture.Registry, fixture.PostgreSQL = bundle, registry, schema
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := provider.ApplyInitial(ctx, database, schema); err != nil {
				t.Fatal(err)
			}
			proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(registry.ModelFingerprint()))
			if err != nil {
				t.Fatal(err)
			}
			operation(t, exactValueProfile{fixture: fixture, database: database, provider: policyir.ProviderPostgreSQL, proof: proof,
				prefix: quoteAcceptanceIdentifier(string(applicationNamespace)) + ".", outbox: quoteAcceptanceIdentifier(string(systemNamespace)) + `."_golem_outbox"`, placeholder: func(index int) string { return fmt.Sprintf("$%d", index) }})
		})
	}
}

func (profile exactValueProfile) seed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := profile.database.ExecContext(ctx, `INSERT INTO `+profile.prefix+`"users" ("id","name") VALUES (`+profile.placeholder(1)+`,`+profile.placeholder(2)+`)`, mutationResultUUIDText(201), "exact-author"); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.database.ExecContext(ctx, `INSERT INTO `+profile.prefix+`"posts" ("id","author_id","title","big_int","decimal_value") VALUES (`+profile.placeholder(1)+`,`+profile.placeholder(2)+`,`+profile.placeholder(3)+`,`+profile.placeholder(4)+`,`+profile.placeholder(5)+`)`, mutationResultUUIDText(202), mutationResultUUIDText(201), "exact", int64(0), int64(0)); err != nil {
		t.Fatal(err)
	}
}

func (profile exactValueProfile) set(t *testing.T, field golem.FieldID, value policyir.Value) policyir.Value {
	t.Helper()
	ctx := context.Background()
	program, err := mutationsql.Render(exactMutationUpdatePlan(t, profile.fixture, field, value), profile.fixture.Registry, profile.provider, profile.proof)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := executeScalarMutationProgram(ctx, profile.database, profile.provider, profile.fixture.Registry, policyir.ModelID(profile.fixture.Post), databaseExecution(profile.database), program)
	if err != nil {
		t.Fatal(err)
	}
	for statementIndex := len(execution.statements) - 1; statementIndex >= 0; statementIndex-- {
		for _, cell := range execution.statements[statementIndex].cells {
			if cell.FieldID() != policyir.FieldID(field) {
				continue
			}
			if cell.IsNull() {
				t.Fatalf("field %x decoded as NULL", field)
			}
			decoded, ok := cell.PolicyValue()
			if !ok {
				t.Fatalf("field %x did not exact-decode", field)
			}
			return decoded
		}
	}
	t.Fatalf("field %x is absent from mutation result", field)
	return policyir.Value{}
}

func TestEmptyBytesPersistAsEmptyNotNullAcrossProviders(t *testing.T) {
	runExactValueProfiles(t, func(t *testing.T, profile exactValueProfile) {
		profile.seed(t)
		decoded := profile.set(t, profile.fixture.PostBytes, policyir.BytesValue([]byte{}))
		if got, ok := decoded.Bytes(); !ok || got == nil || len(got) != 0 {
			t.Fatalf("decoded empty bytes=%#v ok=%t", got, ok)
		}
		var stored struct {
			Null   bool  `db:"is_null"`
			Length int64 `db:"size"`
		}
		query := `SELECT "bytes_value" IS NULL AS "is_null", COALESCE(length("bytes_value"),-1) AS "size" FROM ` + profile.prefix + `"posts" WHERE "id"=` + profile.placeholder(1)
		if err := profile.database.GetContext(context.Background(), &stored, query, mutationResultUUIDText(202)); err != nil || stored.Null || stored.Length != 0 {
			t.Fatalf("stored empty bytes=%+v err=%v; want a non-NULL zero-length value", stored, err)
		}
		if got, present := profile.readPublicBytes(t, 202); !present || got == nil || len(got) != 0 {
			t.Fatalf("public row empty bytes=%#v present=%t; want a non-nil zero-length slice", got, present)
		}
	})
}

func (profile exactValueProfile) readPublicBytes(t *testing.T, id byte) ([]byte, bool) {
	t.Helper()
	schema := profile.fixture
	provider := golem.SQLite
	if profile.provider == policyir.ProviderPostgreSQL {
		provider = golem.PostgreSQL
	}
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postDescriptor := golem.GeneratedModelDescriptor[mutationResultPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.PostBytes}, nil, []golem.IdentityMetadata{postIdentity}, nil))
	userDescriptor := golem.GeneratedModelDescriptor[mutationResultUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID}, nil, []golem.IdentityMetadata{userIdentity}, nil))
	fixture := mutationResultFixtureForSchemaConfigured(t, profile.database, provider, schema, func(config *Config[mutationResultPrincipal, mutationResultActor]) {
		descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(),
			golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()))
		if err != nil {
			t.Fatal(err)
		}
		config.Descriptors = descriptors
	})
	field := golem.GeneratedBytesField[mutationResultPost](schema.PostBytes)
	selector := golem.GeneratedUniqueSelectorValue[mutationResultPost](schema.Post, schema.PostKey, golem.GeneratedSelectorComponent(schema.PostID, golem.UUID{15: id}))
	row, err := SystemFindUnique(context.Background(), fixture.app.System(), postDescriptor, selector, golem.RuntimeProjectionReadOption(golem.Select[mutationResultPost](field)))
	if err != nil {
		t.Fatal(err)
	}
	return golem.Value(row, field).Get()
}
