package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	"github.com/eleven-am/golem/go/internal/physical"
	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresqlprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

const runtimeFullTextIndexName = "content"

func TestFullTextReadOptionsCombinePredicatesAndSelect(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[semanticLimitUser](fixture.User, golem.GeneratedDescriptorShape([]golem.FieldID{fixture.UserID, fixture.UserName}, nil, nil, nil))
	id := golem.GeneratedEqualField[semanticLimitUser, golem.UUID](fixture.UserID)
	name := golem.GeneratedTextField[semanticLimitUser, string](fixture.UserName)
	options, err := fullTextReadOptions([]golem.Predicate[semanticLimitUser]{name.Contains("go"), id.Eq(golem.UUID{})}, "go", 7, golem.Select[semanticLimitUser](name))
	if err != nil {
		t.Fatal(err)
	}
	request, err := golem.FreezeFindMany(descriptor, options...)
	if err != nil {
		t.Fatal(err)
	}
	where, present := request.Where()
	if !present || len(where.View().Root().Children()) != 2 {
		t.Fatalf("combined where=%#v present=%t", where, present)
	}
	selection := request.Selection()
	if request.ProjectionMode() != golem.ProjectionSelect || len(selection) != 1 || selection[0].FieldID() != fixture.UserName {
		t.Fatalf("projection=%d selection=%#v", request.ProjectionMode(), selection)
	}
	if take, present := request.Take(); !present || take != 7 {
		t.Fatalf("take=%d present=%t", take, present)
	}
}

func TestTransactionTextSearchRefusesMissingCapability(t *testing.T) {
	fixture := schematest.New(t)
	descriptor := golem.GeneratedModelDescriptor[semanticLimitUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	if _, err := CallerTxTextSearch[string, testActor](context.Background(), nil, descriptor, "content", "go", 1); err == nil || !strings.Contains(err.Error(), "caller transaction is unavailable") {
		t.Fatalf("caller transaction error=%v", err)
	}
	if _, err := SystemTxTextSearch[string, testActor](context.Background(), nil, descriptor, "content", "go", 1); err == nil || !strings.Contains(err.Error(), "system transaction is unavailable") {
		t.Fatalf("system transaction error=%v", err)
	}
}

func TestCallerTextSearchSelectPreservesFieldAuthorization(t *testing.T) {
	ctx := context.Background()
	fixture := newFullTextMutationFixture(t)
	for _, post := range []struct {
		id     byte
		author byte
		title  string
	}{
		{id: 70, author: 1, title: "visible alpha"},
		{id: 71, author: 2, title: "hidden alpha"},
	} {
		if _, err := SystemCreate(ctx, fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: post.author}, post.title)); err != nil {
			t.Fatal(err)
		}
	}

	caller := fullTextCallerWithDeniedField(t, fixture, fixture.authorID)
	if _, err := CallerTextSearch(ctx, caller, fixture.postDescriptor, runtimeFullTextIndexName, "alpha", 10); err == nil {
		t.Fatal("unprojected full-text search ignored the unrelated denied field")
	} else {
		var failure *golem.Error
		if !errors.As(err, &failure) || failure.Code != golem.CodeForbidden {
			t.Fatalf("unprojected full-text error=%v failure=%#v", err, failure)
		}
	}
	rows, err := CallerTextSearchSelect(ctx, caller, fixture.postDescriptor, runtimeFullTextIndexName, "alpha", 10, golem.Select[mutationResultPost](fixture.postID, fixture.title))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("selected full-text rows=%d want=2", len(rows))
	}
	for _, result := range rows {
		row := result.Row()
		if !golem.Value(row, fixture.postID).IsSelected() || !golem.Value(row, fixture.title).IsSelected() {
			t.Fatalf("safe projection lost selected fields: %#v", row)
		}
		if golem.Value(row, fixture.authorID).IsSelected() {
			t.Fatal("safe projection disclosed the unrelated denied field")
		}
	}

	indexedDenied := fullTextCallerWithDeniedField(t, fixture, fixture.title)
	denied, err := CallerTextSearchSelect(ctx, indexedDenied, fixture.postDescriptor, runtimeFullTextIndexName, "alpha", 10, golem.Select[mutationResultPost](fixture.postID))
	if err != nil {
		t.Fatal(err)
	}
	if len(denied) != 0 {
		t.Fatalf("unreadable indexed content occupied %d ranked slots", len(denied))
	}
}

func TestTransactionTextSearchObservesUncommittedWritesAndRollback(t *testing.T) {
	ctx := context.Background()
	fixture := newFullTextMutationFixture(t)
	rollback := errors.New("rollback full-text transaction")

	t.Run("system", func(t *testing.T) {
		err := SystemTransaction(ctx, fixture.app.System(), func(transaction *SystemTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, createErr := SystemTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(72, golem.UUID{15: 1}, "system pending")); createErr != nil {
				return createErr
			}
			rows, searchErr := SystemTxTextSearchSelect(ctx, transaction, fixture.postDescriptor, runtimeFullTextIndexName, "pending", 10, golem.Select[mutationResultPost](fixture.postID, fixture.title))
			if searchErr != nil {
				return searchErr
			}
			assertFullTextPost(t, rows, fixture, 72, "system pending")
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("system rollback error=%v", err)
		}
		assertFullTextRollback(t, fixture, 72, "pending")
	})

	t.Run("caller", func(t *testing.T) {
		caller := mustMutationResultCaller(t, fixture)
		err := CallerTransaction(ctx, caller, func(transaction *CallerTx[mutationResultPrincipal, mutationResultActor]) error {
			if _, createErr := CallerTxCreate(ctx, transaction, fixture.postDescriptor, fixture.createPost(73, golem.UUID{15: 1}, "caller pending")); createErr != nil {
				return createErr
			}
			rows, searchErr := CallerTxTextSearchSelect(ctx, transaction, fixture.postDescriptor, runtimeFullTextIndexName, "pending", 10, golem.Select[mutationResultPost](fixture.postID, fixture.title))
			if searchErr != nil {
				return searchErr
			}
			assertFullTextPost(t, rows, fixture, 73, "caller pending")
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("caller rollback error=%v", err)
		}
		assertFullTextRollback(t, fixture, 73, "pending")
	})
}

func newFullTextMutationFixture(t *testing.T) mutationResultFixture {
	t.Helper()
	return openMutationResultFixture(t, fullTextMutationSchema(t), MutationLimits{}, nil, nil, nil, true)
}

func fullTextMutationSchema(t *testing.T) schematest.Fixture {
	t.Helper()
	fixture := schematest.NewSubscribedIndexed(t)
	modelDocument := fixture.Bundle.Model()
	var model compilerir.ModelIR
	if err := json.Unmarshal(modelDocument.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	payload, err := fulltextcontract.Encode(fulltextcontract.Index{
		Name: runtimeFullTextIndexName, Folding: fulltextcontract.FoldingNone,
		Fields: []fulltextcontract.Field{{ID: hex.EncodeToString(fixture.PostTitle[:]), Weight: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL} {
		model.Extensions = append(model.Extensions, compilerir.ProviderExtensionIR{
			ID: "74000000000000000000000000000000", Provider: provider, Version: fulltextcontract.Version,
			Owner: compilerir.ObjectID(hex.EncodeToString(fixture.Post[:])), Kind: fulltextcontract.IndexKind, Payload: payload,
		})
	}
	fixture.SQLite = lowerFullTextFixture(t, sqliteprovider.New(), model, fixture.SQLite)
	fixture.PostgreSQL = lowerFullTextFixture(t, postgresqlprovider.New(), model, fixture.PostgreSQL)
	canonical, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(raw) != len(golem.SchemaDigest{}) {
		t.Fatalf("model fingerprint=%q error=%v", fingerprint, err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], raw)
	modelDocument = golem.GeneratedSchemaDocument(modelDocument.FormatVersion(), modelDocument.CanonicalVersion(), digest, canonical)
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		modelDocument, fixture.Bundle.Contract(),
		schematest.ProviderDocument(t, golem.SQLite, fixture.SQLite), schematest.ProviderDocument(t, golem.PostgreSQL, fixture.PostgreSQL),
	)
	return fixture
}

func lowerFullTextFixture(t *testing.T, provider physical.Lowerer, model compilerir.ModelIR, previous physical.PhysicalSchema) physical.PhysicalSchema {
	t.Helper()
	lowered, err := provider.Lower(context.Background(), model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	lowered.Namespace.Name = previous.Namespace.Name
	lowered.System.Namespace.Name = previous.System.Namespace.Name
	return lowered
}

func fullTextCallerWithDeniedField(t *testing.T, fixture mutationResultFixture, denied golem.Field[mutationResultPost]) *Caller[mutationResultPrincipal, mutationResultActor] {
	t.Helper()
	caller := mustMutationResultCaller(t, fixture)
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](fixture.schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		return rules.Freeze(fixture.schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](fixture.schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CannotReadFields(golem.All[mutationResultPost](), denied)
		return rules.Freeze(fixture.schema.Post)
	})
	bindings, err := golem.GeneratedApplicationBindings(
		fixture.schema.Bundle.GenerationDigest(),
		golem.GeneratedStampedPackageBindings(fixture.schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	caller.policies, err = policyruntime.Build(policyruntime.BuildRequest[mutationResultActor]{
		Bindings: bindings, Actor: mutationResultActor{}, Registry: fixture.app.registry,
		Provider: fixture.app.provider, Capabilities: fixture.app.capabilities,
	})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func assertFullTextPost(t *testing.T, rows []golem.FullTextResult[mutationResultPost], fixture mutationResultFixture, wantID byte, wantTitle string) {
	t.Helper()
	if len(rows) != 1 {
		t.Fatalf("transaction-bound full-text rows=%d want=1", len(rows))
	}
	row := rows[0].Row()
	id, idPresent := golem.Value(row, fixture.postID).Get()
	title, titlePresent := golem.Value(row, fixture.title).Get()
	if !idPresent || id != (golem.UUID{15: wantID}) || !titlePresent || title != wantTitle {
		t.Fatalf("transaction-bound full-text id=%v present=%t title=%q present=%t", id, idPresent, title, titlePresent)
	}
}

func assertFullTextRollback(t *testing.T, fixture mutationResultFixture, id byte, query string) {
	t.Helper()
	ctx := context.Background()
	rows, err := SystemTextSearch(ctx, fixture.app.System(), fixture.postDescriptor, runtimeFullTextIndexName, query, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rolled-back full-text query returned %d rows", len(rows))
	}
	var count int
	if err := fixture.app.database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "posts" WHERE "id"=?`, mutationResultUUIDText(id)); err != nil || count != 0 {
		t.Fatalf("rolled-back owner rows=%d err=%v", count, err)
	}
}
