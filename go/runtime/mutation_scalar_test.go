package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	mutationsql "github.com/eleven-am/golem/go/internal/mutation/sql"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

func TestScalarMutationOwnedTransactionCommitsAndPriorResultDeleteDataflow(t *testing.T) {
	ctx := context.Background()
	fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL DEFAULT 'database-default', "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	id, author := scalarMutationUUID(1), scalarMutationUUID(2)

	created, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, scalarMutationCreatePlan(t, fixture, id, author)))
	if err != nil {
		t.Fatal(err)
	}
	if len(created.statements) != 2 { // apply plus persisted row postcondition
		t.Fatalf("create statement count=%d want=2", len(created.statements))
	}
	var decodedDefault bool
	for _, cell := range created.statements[0].cells {
		if cell.FieldID() != policyir.FieldID(fixture.PostTitle) {
			continue
		}
		value, ok := cell.PolicyValue()
		text, textOK := value.Text()
		decodedDefault = ok && textOK && text == "database-default"
	}
	if !decodedDefault {
		t.Fatalf("persisted default was not exact-decoded through P3: %#v", created.statements[0].cells)
	}
	var title string
	if err := database.GetContext(ctx, &title, `SELECT "title" FROM "posts" WHERE "id" = ?`, scalarMutationUUIDText(1)); err != nil || title != "database-default" {
		t.Fatalf("committed default title=%q err=%v", title, err)
	}

	deleted, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, scalarMutationDeletePlan(t, fixture, id)))
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.statements) != 2 || deleted.statements[1].role != mutationsql.ApplyDelete {
		t.Fatalf("delete result=%#v", deleted.statements)
	}
	var count int
	if err := database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts"`); err != nil || count != 0 {
		t.Fatalf("remaining rows=%d err=%v", count, err)
	}
}

func TestUpdateAfterImageMustRemainInsideUpdatePolicy(t *testing.T) {
	ctx := context.Background()
	fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL, "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	id := scalarMutationUUID(3)
	scalarMutationInsert(t, database, 3, 4, "before")
	program := scalarMutationRender(t, fixture, proof, scalarMutationUpdatePlan(t, fixture, id, fixture.PostTitle, scalarMutationString("after"), false, true, mutationir.IdentityUnchanged))

	_, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), program)
	var failure *scalarMutationFailure
	if !errors.As(err, &failure) || failure.kind != scalarMutationForbidden || failure.role != mutationsql.VerifyPostcondition {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
	var title string
	if err := database.GetContext(ctx, &title, `SELECT "title" FROM "posts" WHERE "id" = ?`, scalarMutationUUIDText(3)); err != nil || title != "before" {
		t.Fatalf("rolled-back title=%q err=%v", title, err)
	}
}

func TestNoOpUpdateDoesNotRequireUnchangedFieldPermission(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, fixture schematest.Fixture, database *sqlx.DB, provider policyir.Provider, proof policysql.CapabilityProof, posts string, placeholder func(int) string) {
		t.Helper()
		id := scalarMutationUUID(20)
		if _, err := database.ExecContext(ctx, `INSERT INTO `+posts+` ("id","author_id","title","big_int","decimal_value") VALUES (`+placeholder(1)+`,`+placeholder(2)+`,`+placeholder(3)+`,`+placeholder(4)+`,`+placeholder(5)+`)`, scalarMutationUUIDText(20), scalarMutationUUIDText(21), "before", int64(0), int64(0)); err != nil {
			t.Fatal(err)
		}
		render := func(plan mutationir.Plan) mutationsql.Program {
			program, err := mutationsql.Render(plan, fixture.Registry, provider, proof)
			if err != nil {
				t.Fatal(err)
			}
			return program
		}
		changed := scalarMutationUpdatePlan(t, fixture, id, fixture.PostTitle, scalarMutationString("after"), true, false, mutationir.IdentityUnchanged)
		_, err := executeScalarMutationProgram(ctx, database, provider, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), render(changed))
		var failure *scalarMutationFailure
		if !errors.As(err, &failure) || failure.kind != scalarMutationForbidden || failure.role != mutationsql.ApplyUpdate {
			t.Fatalf("changed-field failure=%#v err=%v", failure, err)
		}
		noOp := scalarMutationUpdatePlan(t, fixture, id, fixture.PostTitle, scalarMutationString("before"), true, false, mutationir.IdentityUnchanged)
		if _, err := executeScalarMutationProgram(ctx, database, provider, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), render(noOp)); err != nil {
			t.Fatalf("exact no-op was denied: %v", err)
		}
		var title string
		if err := database.GetContext(ctx, &title, `SELECT "title" FROM `+posts+` WHERE "id" = `+placeholder(1), scalarMutationUUIDText(20)); err != nil || title != "before" {
			t.Fatalf("title=%q err=%v", title, err)
		}
	}

	t.Run("sqlite", func(t *testing.T) {
		fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL, "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
		run(t, fixture, database, policyir.ProviderSQLite, proof, `"posts"`, func(int) string { return "?" })
	})
	for _, profile := range []struct{ name, env string }{{"postgresql-c", "GOLEM_TEST_POSTGRES_DSN"}, {"postgresql-linguistic", "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := strings.TrimSpace(os.Getenv(profile.env))
			if dsn == "" {
				t.Skipf("%s is required for exact no-op authorization evidence", profile.env)
			}
			fixture := schematest.NewIndexedExact(t)
			sequence := mutationOutboxNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_noop_%d", sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_p4_noop_system_%d", sequence))
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
			t.Cleanup(func() {
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(applicationNamespace)) + ` CASCADE`)
				_, _ = database.Exec(`DROP SCHEMA IF EXISTS ` + quoteAcceptanceIdentifier(string(systemNamespace)) + ` CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schema); err != nil {
				t.Fatal(err)
			}
			proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(registry.ModelFingerprint()))
			if err != nil {
				t.Fatal(err)
			}
			posts := quoteAcceptanceIdentifier(string(applicationNamespace)) + `."posts"`
			run(t, fixture, database, policyir.ProviderPostgreSQL, proof, posts, func(index int) string { return fmt.Sprintf("$%d", index) })
		})
	}
}

func TestScalarMutationZeroAndMultipleTargetRowsFailClosedBeforeWrite(t *testing.T) {
	ctx := context.Background()
	fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT NOT NULL, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL, "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	missing := scalarMutationUpdatePlan(t, fixture, scalarMutationUUID(5), fixture.PostTitle, scalarMutationString("after"), false, false, mutationir.IdentityUnchanged)
	_, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, missing))
	var failure *scalarMutationFailure
	if !errors.As(err, &failure) || failure.kind != scalarMutationNotFound || failure.role != mutationsql.SelectPreImage {
		t.Fatalf("missing failure=%#v err=%v", failure, err)
	}

	scalarMutationInsert(t, database, 6, 7, "first")
	scalarMutationInsert(t, database, 6, 8, "second")
	multiple := scalarMutationUpdatePlan(t, fixture, scalarMutationUUID(6), fixture.PostTitle, scalarMutationString("changed"), false, false, mutationir.IdentityUnchanged)
	_, err = executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, multiple))
	failure = nil
	if !errors.As(err, &failure) || failure.kind != scalarMutationInvariant || failure.role != mutationsql.SelectPreImage {
		t.Fatalf("multiple failure=%#v err=%v", failure, err)
	}
	var changed int
	if err := database.GetContext(ctx, &changed, `SELECT COUNT(*) FROM "posts" WHERE "title" = 'changed'`); err != nil || changed != 0 {
		t.Fatalf("changed rows=%d err=%v", changed, err)
	}
}

func TestScalarMutationIdentityMismatchRollsBack(t *testing.T) {
	ctx := context.Background()
	fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL, "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	scalarMutationInsert(t, database, 9, 10, "before")
	plan := scalarMutationUpdatePlan(t, fixture, scalarMutationUUID(9), fixture.PostID, scalarMutationUUID(11), false, false, mutationir.IdentityUnchanged)
	_, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), scalarMutationRender(t, fixture, proof, plan))
	var failure *scalarMutationFailure
	if !errors.As(err, &failure) || failure.kind != scalarMutationInvariant {
		t.Fatalf("failure=%#v err=%v", failure, err)
	}
	var count int
	if err := database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, scalarMutationUUIDText(9)); err != nil || count != 1 {
		t.Fatalf("original identity rows=%d err=%v", count, err)
	}
	if err := database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts" WHERE "id" = ?`, scalarMutationUUIDText(11)); err != nil || count != 0 {
		t.Fatalf("new identity rows=%d err=%v", count, err)
	}
}

func TestScalarMutationJoinsOuterTransactionWithoutCommittingIt(t *testing.T) {
	ctx := context.Background()
	fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL DEFAULT 'database-default', "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	transaction, err := database.BeginTxx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding := transactionExecution(database, transaction)
	program := scalarMutationRender(t, fixture, proof, scalarMutationCreatePlan(t, fixture, scalarMutationUUID(12), scalarMutationUUID(13)))
	if _, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), binding, program); err != nil {
		t.Fatal(err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatal(err)
	}
	binding.close()
	var count int
	if err := database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts"`); err != nil || count != 0 {
		t.Fatalf("joined mutation escaped outer rollback: count=%d err=%v", count, err)
	}
}

func TestScalarMutationPublicErrorDoesNotLeakProviderCause(t *testing.T) {
	cause := scalarMutationError(mutationir.Update, scalarMutationForbidden, mutationsql.ApplyUpdate, 1, "private physical detail", errors.New(`driver: table "secret"`))
	err := publicScalarMutationError([16]byte{1}, cause)
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != golem.CodeForbidden || failure.Error() != "FORBIDDEN: mutation is not authorized" {
		t.Fatalf("public failure=%#v err=%v", failure, err)
	}
}

func TestScalarMutationSQLiteUniqueFailureIsStableConflict(t *testing.T) {
	ctx := context.Background()
	fixture, database, proof := scalarMutationSQLite(t, `CREATE TABLE "posts" ("id" TEXT PRIMARY KEY, "author_id" TEXT NOT NULL, "title" TEXT NOT NULL DEFAULT 'database-default', "big_int" INTEGER NOT NULL DEFAULT 0, "decimal_value" INTEGER)`)
	program := scalarMutationRender(t, fixture, proof, scalarMutationCreatePlan(t, fixture, scalarMutationUUID(40), scalarMutationUUID(41)))
	if _, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), program); err != nil {
		t.Fatal(err)
	}
	_, err := executeScalarMutationProgram(ctx, database, policyir.ProviderSQLite, fixture.Registry, policyir.ModelID(fixture.Post), databaseExecution(database), program)
	var internal *scalarMutationFailure
	if !errors.As(err, &internal) || internal.kind != scalarMutationConflict {
		t.Fatalf("internal=%#v err=%v", internal, err)
	}
	public := publicScalarMutationError(fixture.Post, err)
	var failure *golem.Error
	if !errors.As(public, &failure) || failure.Code != golem.CodeConflict || failure.Error() != "CONFLICT: mutation conflicted" {
		t.Fatalf("public=%#v err=%v", failure, public)
	}
}

type scalarPreparedPost struct{}

func TestSystemScalarMutationPreparationBindsPlansRendersBeforeExecution(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewSubscribedIndexed(t)
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "prepared-scalar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	limits, err := normalizeMutationLimits(MutationLimits{})
	if err != nil {
		t.Fatal(err)
	}
	app := &App[struct{}, struct{}]{database: database, provider: policyir.ProviderSQLite, registry: fixture.Registry, capabilities: proof, mutationLimits: limits}
	system := System[struct{}, struct{}]{app: app, executor: databaseExecution(database)}
	beforeEpoch := system.executor.invalidationEpoch()
	idColumn := golem.GeneratedEqualField[scalarPreparedPost, golem.UUID](fixture.PostID)
	authorColumn := golem.GeneratedEqualField[scalarPreparedPost, golem.UUID](fixture.AuthorID)
	titleColumn := golem.GeneratedTextField[scalarPreparedPost, string](fixture.PostTitle)
	input := golem.GeneratedCreateInput[scalarPreparedPost](fixture.Post,
		golem.GeneratedCreateFieldValue(fixture.Post, idColumn, golem.UUID{15: 30}),
		golem.GeneratedCreateFieldValue(fixture.Post, authorColumn, golem.UUID{15: 31}),
		golem.GeneratedCreateFieldValue(fixture.Post, titleColumn, "prepared"),
	)
	frozen, err := golem.RuntimeFreezeCreateInput(input)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := mutationir.NewImageRequirements(policyir.ModelID(fixture.Post), []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle)}, nil)
	program, err := prepareSystemScalarProgram(system, scalarMutationPrepareRequest{operation: mutationir.Create, model: policyir.ModelID(fixture.Post), input: &frozen, result: result})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if _, err := executeSystemScalarProgram(ctx, system, policyir.ModelID(fixture.Post), program); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if system.executor.invalidationEpoch() != beforeEpoch+1 {
		t.Fatalf("scalar commit invalidation epoch=%d want=%d", system.executor.invalidationEpoch(), beforeEpoch+1)
	}
	var title string
	if err := database.GetContext(ctx, &title, `SELECT "title" FROM "posts" WHERE "id" = ?`, scalarMutationUUIDText(30)); err != nil || title != "prepared" {
		t.Fatalf("title=%q err=%v", title, err)
	}
	var stored struct {
		Action   string `db:"action"`
		Ordinal  int64  `db:"transaction_ordinal"`
		Metadata []byte `db:"metadata"`
	}
	if err := database.GetContext(ctx, &stored, `SELECT "action", "transaction_ordinal", "metadata" FROM "_golem_outbox"`); err != nil {
		t.Fatal(err)
	}
	envelope, err := decodeCurrentMutationFactMetadata(fixture.Registry, policyir.ModelID(fixture.Post), stored.Metadata)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip, err := mutationfact.Encode(envelope)
	if err != nil || !bytes.Equal(roundTrip, stored.Metadata) || stored.Action != "created" || stored.Ordinal != 1 || envelope.Action() != mutationir.FactCreated {
		t.Fatalf("persisted fact action=%q ordinal=%d envelope=%d exact=%t err=%v", stored.Action, stored.Ordinal, envelope.Action(), bytes.Equal(roundTrip, stored.Metadata), err)
	}
}

func scalarMutationSQLite(t *testing.T, ddl string) (schematest.Fixture, *sqlx.DB, policysql.CapabilityProof) {
	t.Helper()
	fixture := schematest.NewIndexedExact(t)
	ctx := context.Background()
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "scalar-mutation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	if _, err := database.ExecContext(ctx, ddl); err != nil {
		t.Fatal(err)
	}
	proof, err := provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint()))
	if err != nil {
		t.Fatal(err)
	}
	return fixture, database, proof
}

func scalarMutationRender(t *testing.T, fixture schematest.Fixture, proof policysql.CapabilityProof, plan mutationir.Plan) mutationsql.Program {
	t.Helper()
	program, err := mutationsql.Render(plan, fixture.Registry, policyir.ProviderSQLite, proof)
	if err != nil {
		t.Fatal(err)
	}
	return program
}

func scalarMutationCreatePlan(t *testing.T, fixture schematest.Fixture, id, author policyir.Value) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	idOperation, _ := mutationir.NewSet(policyir.FieldID(fixture.PostID), scalarMutationType(t, fixture, fixture.PostID), id)
	authorOperation, _ := mutationir.NewSet(policyir.FieldID(fixture.AuthorID), scalarMutationType(t, fixture, fixture.AuthorID), author)
	after, _ := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle)}, nil)
	truth, _ := policyir.NewConstant(model, true)
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Create, Model: model, ScalarOperations: []mutationir.ScalarOperation{idOperation, authorOperation}, After: after, RowPostcondition: &truth, Identity: mutationir.IdentityProduced})
	if err != nil {
		t.Fatal(err)
	}
	return scalarMutationPlan(t, graph, after)
}

func scalarMutationUpdatePlan(t *testing.T, fixture schematest.Fixture, id policyir.Value, fieldID [16]byte, value policyir.Value, denyField, denyPostcondition bool, identity mutationir.IdentityBehavior) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	operation, err := mutationir.NewSet(policyir.FieldID(fieldID), scalarMutationType(t, fixture, fieldID), value)
	if err != nil {
		t.Fatal(err)
	}
	target := scalarMutationTarget(t, fixture, id)
	truth, _ := policyir.NewConstant(model, true)
	postcondition := truth
	if denyPostcondition {
		postcondition, _ = policyir.NewConstant(model, false)
	}
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionUpdate, truth)
	fields := []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fieldID)}
	before, _ := mutationir.NewImageRequirements(model, fields, nil)
	after, _ := mutationir.NewImageRequirements(model, fields, nil)
	var authorizations []mutationir.FieldAuthorization
	if denyField {
		denied, _ := policyir.NewConstant(model, false)
		authorization, _ := mutationir.NewFieldAuthorization(policyir.FieldID(fieldID), denied)
		authorizations = []mutationir.FieldAuthorization{authorization}
	}
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Update, Model: model, Target: &target, ScalarOperations: []mutationir.ScalarOperation{operation}, Before: before, After: after, Selection: &selection, RowPostcondition: &postcondition, FieldConditions: authorizations, Identity: identity})
	if err != nil {
		t.Fatal(err)
	}
	return scalarMutationPlan(t, graph, after)
}

func scalarMutationDeletePlan(t *testing.T, fixture schematest.Fixture, id policyir.Value) mutationir.Plan {
	t.Helper()
	model := policyir.ModelID(fixture.Post)
	target := scalarMutationTarget(t, fixture, id)
	truth, _ := policyir.NewConstant(model, true)
	selection, _ := mutationir.NewSelectionRequirement(policyir.ActionDelete, truth)
	before, _ := mutationir.NewImageRequirements(model, []policyir.FieldID{policyir.FieldID(fixture.PostID), policyir.FieldID(fixture.PostTitle)}, nil)
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Delete, Model: model, Target: &target, Before: before, Selection: &selection, Identity: mutationir.IdentityUnchanged})
	if err != nil {
		t.Fatal(err)
	}
	return scalarMutationPlan(t, graph, before)
}

func scalarMutationPlan(t *testing.T, graph mutationir.Graph, result mutationir.ImageRequirements) mutationir.Plan {
	t.Helper()
	requirement, _ := mutationir.NewProviderRequirement(policyir.PortableProviders(), mutationir.CapabilityTransaction)
	bounds, _ := mutationir.NewStatementBounds(999, 1000)
	plan, err := mutationir.NewPlan(mutationir.PlanInput{Stance: mutationir.Caller, Graph: graph, Result: result, Providers: []mutationir.ProviderRequirement{requirement}, Retry: mutationir.NoRetry, Bounds: bounds})
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func scalarMutationTarget(t *testing.T, fixture schematest.Fixture, id policyir.Value) mutationir.Target {
	t.Helper()
	selector, _ := mutationir.NewSelectorValue(policyir.FieldID(fixture.PostID), id)
	target, err := mutationir.NewTarget(policyir.ModelID(fixture.Post), fixture.PostKey, []mutationir.SelectorValue{selector}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func scalarMutationType(t *testing.T, fixture schematest.Fixture, field [16]byte) policyir.TypeRef {
	t.Helper()
	resolved, ok := policysql.SchemaResolver(fixture.Registry).Field(policyir.ProviderSQLite, policyir.ModelID(fixture.Post), policyir.FieldID(field))
	if !ok {
		t.Fatalf("field %x has no physical type", field)
	}
	return resolved.Type
}

func scalarMutationString(value string) policyir.Value {
	result, _ := policyir.StringValue(value)
	return result
}

func scalarMutationUUID(last byte) policyir.Value {
	var value [16]byte
	value[15] = last
	return policyir.UUIDValue(value)
}

func scalarMutationUUIDText(last byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012x", last)
}

func scalarMutationInsert(t *testing.T, database *sqlx.DB, id, author byte, title string) {
	t.Helper()
	if _, err := database.Exec(`INSERT INTO "posts" ("id", "author_id", "title") VALUES (?, ?, ?)`, scalarMutationUUIDText(id), scalarMutationUUIDText(author), title); err != nil {
		t.Fatal(err)
	}
}
