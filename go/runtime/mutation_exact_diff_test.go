package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/physical"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type exactDiffPrincipal struct{}
type exactDiffActor struct{}
type exactDiffRecord struct{}

func TestUpdateFieldAuthorizationUsesExactPersistedDiff(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, fixture schematest.LogicalDiffFixture, database *sqlx.DB, provider golem.Provider, table string, placeholder func(int) string) {
		t.Helper()
		id := golem.UUID{15: 1}
		// JSON object order/whitespace and list whitespace are deliberately
		// noncanonical physical input. Both providers must decode the same logical
		// values authored below, regardless of their physical JSON representation.
		if _, err := database.ExecContext(ctx, `INSERT INTO `+table+`("id","document","tags","score") VALUES (`+placeholder(1)+`,`+placeholder(2)+`,`+placeholder(3)+`,`+placeholder(4)+`)`, "00000000-0000-0000-0000-000000000001", `{ "b" : 2, "a" : 1 }`, `[ "x", "y" ]`, 1.0); err != nil {
			t.Fatal(err)
		}

		documentField := golem.GeneratedJSONField[exactDiffRecord](fixture.Document)
		tagsField := golem.GeneratedListField[exactDiffRecord, string](fixture.Tags)
		scoreField := golem.GeneratedOrderedField[exactDiffRecord, float64](fixture.Score)
		identity := golem.GeneratedIdentityMetadata(fixture.Record, fixture.Primary, golem.PrimaryIdentity, fixture.ID)
		descriptor := golem.GeneratedModelDescriptor[exactDiffRecord](fixture.Record, golem.GeneratedDescriptorShape(
			[]golem.FieldID{fixture.ID, fixture.Document, fixture.Tags, fixture.Score}, nil, []golem.IdentityMetadata{identity}, nil,
		))
		descriptors, err := golem.GeneratedApplicationDescriptors(fixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(fixture.Bundle.GenerationDigest(), descriptor.Metadata()))
		if err != nil {
			t.Fatal(err)
		}
		binding := golem.GeneratedPolicyBinding[exactDiffActor, exactDiffRecord](fixture.Record, func(exactDiffActor) (golem.FrozenPolicy, error) {
			rules := golem.NewRules[exactDiffRecord]()
			rules.CanRead(golem.All[exactDiffRecord]())
			rules.CanUpdate(golem.All[exactDiffRecord]())
			rules.CannotUpdateFields(golem.All[exactDiffRecord](), documentField, tagsField, scoreField)
			return rules.Freeze(fixture.Record)
		})
		bindings, err := golem.GeneratedApplicationBindings(fixture.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(fixture.Bundle.GenerationDigest(), []golem.PolicyBinding[exactDiffActor]{binding}, nil))
		if err != nil {
			t.Fatal(err)
		}
		app, err := Open(ctx, Config[exactDiffPrincipal, exactDiffActor]{
			DB: database, Provider: provider, Bundle: fixture.Bundle, Bindings: bindings, Descriptors: descriptors,
			ResolvePrincipal: func(context.Context, exactDiffPrincipal) (exactDiffActor, error) { return exactDiffActor{}, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		caller, err := app.ForPrincipal(ctx, exactDiffPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		target := golem.GeneratedUniqueSelectorValue[exactDiffRecord](fixture.Record, fixture.Primary, golem.GeneratedSelectorComponent(fixture.ID, id))
		document, err := golem.NewJSONDocument[any]([]byte(`{"a":1,"b":2}`))
		if err != nil {
			t.Fatal(err)
		}
		for name, input := range map[string]golem.UpdateInput[exactDiffRecord]{
			"noncanonical json no-op": golem.GeneratedUpdateInput[exactDiffRecord](fixture.Record, golem.GeneratedSetFieldValue(fixture.Record, documentField, document)),
			"noncanonical list no-op": golem.GeneratedUpdateInput[exactDiffRecord](fixture.Record, golem.GeneratedSetFieldValue(fixture.Record, tagsField, golem.List[string]{"x", "y"})),
			"same float bits no-op":   golem.GeneratedUpdateInput[exactDiffRecord](fixture.Record, golem.GeneratedSetFieldValue(fixture.Record, scoreField, 1.0)),
		} {
			t.Run(name, func(t *testing.T) {
				if _, err := CallerUpdate(ctx, caller, descriptor, target, input); err != nil {
					t.Fatalf("exact logical no-op required a denied grant: %v: %v", err, errors.Unwrap(err))
				}
			})
		}

		changedDocument, _ := golem.NewJSONDocument[any]([]byte(`{"a":1,"b":3}`))
		_, err = CallerUpdate(ctx, caller, descriptor, target, golem.GeneratedUpdateInput[exactDiffRecord](fixture.Record, golem.GeneratedSetFieldValue(fixture.Record, documentField, changedDocument)))
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeForbidden {
			t.Fatalf("changed denied JSON field succeeded: %#v err=%v", failure, err)
		}
		var raw string
		if err := database.GetContext(ctx, &raw, `SELECT "document" FROM `+table+` WHERE "id" = `+placeholder(1), "00000000-0000-0000-0000-000000000001"); err != nil {
			t.Fatal(err)
		}
		var persisted map[string]float64
		if err := json.Unmarshal([]byte(raw), &persisted); err != nil || persisted["a"] != 1 || persisted["b"] != 2 {
			t.Fatalf("denied update was not rolled back: raw=%q decoded=%v err=%v", raw, persisted, err)
		}

		changedScore := math.Nextafter(1, 2)
		_, err = CallerUpdate(ctx, caller, descriptor, target, golem.GeneratedUpdateInput[exactDiffRecord](fixture.Record, golem.GeneratedSetFieldValue(fixture.Record, scoreField, changedScore)))
		if !errors.As(err, &failure) || failure.Code != golem.CodeForbidden {
			t.Fatalf("changed denied float bits succeeded: %#v err=%v", failure, err)
		}
		var score float64
		if err := database.GetContext(ctx, &score, `SELECT "score" FROM `+table+` WHERE "id" = `+placeholder(1), "00000000-0000-0000-0000-000000000001"); err != nil || math.Float64bits(score) != math.Float64bits(1.0) {
			t.Fatalf("denied float update was not rolled back: score=%v bits=%016x err=%v", score, math.Float64bits(score), err)
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		fixture := schematest.NewLogicalDiff(t)
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "exact-logical-diff.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
			t.Fatal(err)
		}
		run(t, fixture, database, golem.SQLite, `"logical_records"`, func(int) string { return "?" })
	})

	for _, profile := range []struct{ name, env string }{{"postgresql-c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for exact persisted-diff evidence", profile.env)
			}
			fixture := schematest.NewLogicalDiff(t)
			sequence := mutationOutboxNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_diff_%d", sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_diff_system_%d", sequence))
			schema := fixture.PostgreSQL
			schema.Namespace.Name, schema.System.Namespace.Name = applicationNamespace, systemNamespace
			bundle := postgresRuntimeBundleFrom(t, fixture.Bundle, schema)
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
			t.Cleanup(func() {
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(applicationNamespace)) + ` CASCADE`)
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(systemNamespace)) + ` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schema); err != nil {
				t.Fatal(err)
			}
			table := quoteAcceptanceIdentifier(string(applicationNamespace)) + `."logical_records"`
			run(t, fixture, database, golem.PostgreSQL, table, func(index int) string { return fmt.Sprintf("$%d", index) })
		})
	}
}
