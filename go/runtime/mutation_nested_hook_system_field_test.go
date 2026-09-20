package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyschema "github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
	"github.com/jmoiron/sqlx"
)

var nestedSystemFieldNamespaceSequence atomic.Uint64

type nestedSystemFieldHooks func(schematest.Fixture, golem.TextField[mutationResultPost, string], golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor]

type nestedSystemFieldSpec struct {
	field   func(schematest.Fixture) (golem.ModelID, golem.FieldID)
	modes   []compilerir.FieldMode
	hooks   nestedSystemFieldHooks
	prepare func(testing.TB, schematest.Fixture) schematest.Fixture
}

type nestedSystemFieldFixture struct {
	mutationVocabularyFixture
	decimal golem.EqualField[mutationResultPost, golem.Decimal]
}

func systemOptionalInt(fixture schematest.Fixture) (golem.ModelID, golem.FieldID) {
	return fixture.Post, fixture.PostOptionalInt
}

func systemUserName(fixture schematest.Fixture) (golem.ModelID, golem.FieldID) {
	return fixture.User, fixture.UserName
}

func withContractFieldModes(t testing.TB, fixture schematest.Fixture, model golem.ModelID, field golem.FieldID, modes ...compilerir.FieldMode) schematest.Fixture {
	t.Helper()
	contractDocument := fixture.Bundle.Contract()
	var contract compilerir.ContractIR
	if err := json.Unmarshal(contractDocument.Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	found := false
	for modelIndex := range contract.Models {
		if contract.Models[modelIndex].ModelID != compilerir.ModelID(fmt.Sprintf("%x", model[:])) {
			continue
		}
		for fieldIndex := range contract.Models[modelIndex].Fields {
			if contract.Models[modelIndex].Fields[fieldIndex].FieldID == compilerir.FieldID(fmt.Sprintf("%x", field[:])) {
				contract.Models[modelIndex].Fields[fieldIndex].Modes = modes
				found = true
			}
		}
	}
	if !found {
		t.Fatal("contract field is absent")
	}
	payload, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(raw) != len(golem.SchemaDigest{}) {
		t.Fatalf("contract fingerprint=%q error=%v", fingerprint, err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], raw)
	contractDocument = golem.GeneratedSchemaDocument(contractDocument.FormatVersion(), contractDocument.CanonicalVersion(), digest, payload)
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		fixture.Bundle.Model(), contractDocument, fixture.Bundle.Providers()...,
	)
	fixture.Registry, err = policyschema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func withNullableUserName(t testing.TB, fixture schematest.Fixture) schematest.Fixture {
	t.Helper()
	return withNullableScalar(t, fixture, fixture.UserName)
}

func withNullableScalar(t testing.TB, fixture schematest.Fixture, target golem.FieldID) schematest.Fixture {
	t.Helper()
	field := compilerir.FieldID(fmt.Sprintf("%x", target[:]))
	var model compilerir.ModelIR
	if err := json.Unmarshal(fixture.Bundle.Model().Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	found := false
	for modelIndex := range model.Models {
		for fieldIndex := range model.Models[modelIndex].Fields {
			if model.Models[modelIndex].Fields[fieldIndex].ID == field && model.Models[modelIndex].Fields[fieldIndex].Scalar != nil {
				scalar := *model.Models[modelIndex].Fields[fieldIndex].Scalar
				scalar.Nullable = true
				model.Models[modelIndex].Fields[fieldIndex].Scalar = &scalar
				found = true
			}
		}
	}
	if !found {
		t.Fatal("model field is absent")
	}
	payload, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	modelDocument := golem.GeneratedSchemaDocument(fixture.Bundle.Model().FormatVersion(), fixture.Bundle.Model().CanonicalVersion(), nestedSchemaDigest(t, string(fingerprint)), payload)
	nullable := func(schema physical.PhysicalSchema) physical.PhysicalSchema {
		schema.Tables = append([]physical.PhysicalTable(nil), schema.Tables...)
		for tableIndex := range schema.Tables {
			schema.Tables[tableIndex].Columns = append([]physical.PhysicalColumn(nil), schema.Tables[tableIndex].Columns...)
			for columnIndex := range schema.Tables[tableIndex].Columns {
				if schema.Tables[tableIndex].Columns[columnIndex].ID == field {
					schema.Tables[tableIndex].Columns[columnIndex].Nullable = true
				}
			}
		}
		return schema
	}
	fixture.SQLite, fixture.PostgreSQL = nullable(fixture.SQLite), nullable(fixture.PostgreSQL)
	providers := []golem.ProviderSchemaDocument{nestedProviderDocument(t, golem.SQLite, fixture.SQLite), nestedProviderDocument(t, golem.PostgreSQL, fixture.PostgreSQL)}
	var contract compilerir.ContractIR
	if err := json.Unmarshal(fixture.Bundle.Contract().Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	for contractIndex := range contract.Models {
		event := contract.Models[contractIndex].Event
		if event == nil {
			continue
		}
		for _, logical := range model.Models {
			if logical.ID != contract.Models[contractIndex].ModelID {
				continue
			}
			snapshot := make([]compilerir.FieldID, len(event.Schema.SnapshotFields))
			for index, snapshotField := range event.Schema.SnapshotFields {
				snapshot[index] = snapshotField.FieldID
			}
			shape, shapeErr := compilerir.BuildEventSchemaShape(logical, model.Enums, snapshot)
			if shapeErr != nil {
				t.Fatal(shapeErr)
			}
			shapeFingerprint, shapeErr := compilerir.EventSchemaFingerprint(shape)
			if shapeErr != nil {
				t.Fatal(shapeErr)
			}
			patched := *event
			patched.Schema, patched.SchemaFingerprint = shape, shapeFingerprint
			contract.Models[contractIndex].Event = &patched
		}
	}
	contractPayload, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractDocument := golem.GeneratedSchemaDocument(fixture.Bundle.Contract().FormatVersion(), fixture.Bundle.Contract().CanonicalVersion(), nestedSchemaDigest(t, string(contractFingerprint)), contractPayload)
	fixture.Bundle = golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), modelDocument, contractDocument, providers...)
	fixture.Registry, err = policyschema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func nestedSchemaDigest(t testing.TB, fingerprint string) golem.SchemaDigest {
	t.Helper()
	raw, err := hex.DecodeString(fingerprint)
	if err != nil || len(raw) != len(golem.SchemaDigest{}) {
		t.Fatalf("fingerprint=%q error=%v", fingerprint, err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], raw)
	return digest
}

func nestedProviderDocument(t testing.TB, provider golem.Provider, value physical.PhysicalSchema) golem.ProviderSchemaDocument {
	t.Helper()
	payload, err := physical.CanonicalEncode(value)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := physical.PhysicalFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	system, err := physical.SystemFingerprint(value.Provider, value.System)
	if err != nil {
		t.Fatal(err)
	}
	return golem.GeneratedProviderSchemaDocument(provider, golem.SchemaDigest(system), golem.GeneratedSchemaDocument(value.Version, value.CanonicalVersion, golem.SchemaDigest(fingerprint), payload))
}

func forEachNestedSystemFieldProvider(t *testing.T, spec nestedSystemFieldSpec, run func(*testing.T, nestedSystemFieldFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, "file:"+filepath.Join(t.TempDir(), "nested-system-field.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = database.Close() })
		schema := schematest.NewMutationVocabulary(t)
		if spec.prepare != nil {
			schema = spec.prepare(t, schema)
		}
		model, field := spec.field(schema)
		schema = withContractFieldModes(t, schema, model, field, spec.modes...)
		if err := provider.ApplyInitial(ctx, database, schema.SQLite); err != nil {
			t.Fatal(err)
		}
		run(t, openNestedSystemFieldFixture(t, database, golem.SQLite, schema, spec.hooks))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			if profile.dsn == "" {
				testenv.SkipMissingPostgreSQL(t, profile.env+" is not configured")
			}
			ctx := context.Background()
			sequence := nestedSystemFieldNamespaceSequence.Add(1)
			applicationNamespace := physical.PhysicalName(fmt.Sprintf("golem_nested_system_%s_%d_%d", profile.name, os.Getpid(), sequence))
			systemNamespace := physical.PhysicalName(fmt.Sprintf("golem_nested_system_sys_%s_%d_%d", profile.name, os.Getpid(), sequence))
			schema := schematest.NewMutationVocabularyPostgreSQLNamespaces(t, applicationNamespace, systemNamespace)
			if spec.prepare != nil {
				schema = spec.prepare(t, schema)
			}
			model, field := spec.field(schema)
			schema = withContractFieldModes(t, schema, model, field, spec.modes...)
			provider := postgresprovider.New()
			database, _, err := provider.Open(ctx, profile.dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(applicationNamespace)+`" CASCADE`)
				_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+string(systemNamespace)+`" CASCADE`)
				_ = database.Close()
			})
			if err := provider.ApplyInitial(ctx, database, schema.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			run(t, openNestedSystemFieldFixture(t, database, golem.PostgreSQL, schema, spec.hooks))
		})
	}
}

func openNestedSystemFieldFixture(t *testing.T, database *sqlx.DB, provider golem.Provider, schema schematest.Fixture, hooks nestedSystemFieldHooks) nestedSystemFieldFixture {
	t.Helper()
	ctx := context.Background()
	userIdentity := golem.GeneratedIdentityMetadata(schema.User, schema.UserKey, golem.PrimaryIdentity, schema.UserID)
	postIdentity := golem.GeneratedIdentityMetadata(schema.Post, schema.PostKey, golem.PrimaryIdentity, schema.PostID)
	userRelation := golem.GeneratedRelationMetadata(schema.User, schema.Post, schema.UserPosts, schema.Authorship, golem.RelationInverse, golem.RelationToMany)
	postRelation := golem.GeneratedRelationMetadata(schema.Post, schema.User, schema.PostAuthor, schema.Authorship, golem.RelationSource, golem.RelationToOne)
	userDescriptor := golem.GeneratedModelDescriptor[mutationResultUser](schema.User, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.UserID, schema.UserName}, nil, []golem.IdentityMetadata{userIdentity}, []golem.RelationMetadata{userRelation},
	))
	postDescriptor := golem.GeneratedModelDescriptor[mutationResultPost](schema.Post, golem.GeneratedDescriptorShape(
		[]golem.FieldID{schema.PostID, schema.AuthorID, schema.PostTitle, schema.PostBigInt, schema.PostDecimal, schema.PostOptionalInt}, nil, []golem.IdentityMetadata{postIdentity}, []golem.RelationMetadata{postRelation},
	))
	descriptors, err := golem.GeneratedApplicationDescriptors(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageDescriptors(schema.Bundle.GenerationDigest(), userDescriptor.Metadata(), postDescriptor.Metadata()))
	if err != nil {
		t.Fatal(err)
	}
	title := golem.GeneratedTextField[mutationResultPost, string](schema.PostTitle)
	optionalInt := golem.GeneratedNullableOrderedField[mutationResultPost, int64](schema.PostOptionalInt)
	userPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultUser](schema.User, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultUser]()
		rules.CanRead(golem.All[mutationResultUser]())
		rules.CanCreate(golem.All[mutationResultUser]())
		rules.CanUpdate(golem.All[mutationResultUser]())
		rules.CanDelete(golem.All[mutationResultUser]())
		return rules.Freeze(schema.User)
	})
	postPolicy := golem.GeneratedPolicyBinding[mutationResultActor, mutationResultPost](schema.Post, func(mutationResultActor) (golem.FrozenPolicy, error) {
		rules := golem.NewRules[mutationResultPost]()
		rules.CanRead(golem.All[mutationResultPost]())
		rules.CanCreate(golem.All[mutationResultPost]())
		rules.CanUpdate(golem.All[mutationResultPost]())
		rules.CanDelete(golem.All[mutationResultPost]())
		return rules.Freeze(schema.Post)
	})
	var hookBindings []golem.HookBinding[mutationResultActor]
	if hooks != nil {
		hookBindings = hooks(schema, title, optionalInt)
	}
	bindings, err := golem.GeneratedApplicationBindings(schema.Bundle.GenerationDigest(), golem.GeneratedStampedPackageBindings(schema.Bundle.GenerationDigest(), []golem.PolicyBinding[mutationResultActor]{userPolicy, postPolicy}, hookBindings))
	if err != nil {
		t.Fatal(err)
	}
	app, err := Open(ctx, withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
		Database: p8RuntimeTestDatabase(database, provider), Bundle: schema.Bundle, Bindings: bindings, Descriptors: descriptors,
		ResolvePrincipal: func(context.Context, mutationResultPrincipal) (mutationResultActor, error) {
			return mutationResultActor{}, nil
		},
	}))
	if err != nil {
		t.Fatal(err)
	}
	users := nestedAcceptanceTable(app, schema.User)
	for _, user := range [][2]string{{mutationResultUUIDText(1), "alice"}, {mutationResultUUIDText(2), "bob"}} {
		if _, err := database.ExecContext(ctx, database.Rebind(`INSERT INTO `+users+`("id","name") VALUES (?,?)`), user[0], user[1]); err != nil {
			t.Fatal(err)
		}
	}
	base := mutationResultFixture{
		app: app, schema: schema, userDescriptor: userDescriptor, postDescriptor: postDescriptor,
		userID: golem.GeneratedEqualField[mutationResultUser, golem.UUID](schema.UserID), userName: golem.GeneratedTextField[mutationResultUser, string](schema.UserName),
		postID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID), authorID: golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID),
		title: title, author: golem.GeneratedToOne[mutationResultPost, mutationResultUser](schema.PostAuthor, schema.Authorship, schema.User),
	}
	return nestedSystemFieldFixture{
		mutationVocabularyFixture: mutationVocabularyFixture{mutationResultFixture: base, bigInt: golem.GeneratedOrderedField[mutationResultPost, int64](schema.PostBigInt), optionalInt: optionalInt},
		decimal:                   golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal),
	}
}

func (fixture nestedSystemFieldFixture) seedPost(t *testing.T, id byte, author byte, title string, optional int64) {
	t.Helper()
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	statement := fixture.app.database.Rebind(`INSERT INTO ` + posts + `("id","author_id","title","big_int","decimal_value","optional_int") VALUES (?,?,?,?,?,?)`)
	if _, err := fixture.app.database.ExecContext(context.Background(), statement, mutationResultUUIDText(id), mutationResultUUIDText(author), title, int64(10), 0, optional); err != nil {
		t.Fatal(err)
	}
}

func (fixture nestedSystemFieldFixture) readPost(t *testing.T, id byte) (string, string, *int64) {
	t.Helper()
	var title, author string
	var optional *int64
	posts := nestedAcceptanceTable(fixture.app, fixture.schema.Post)
	query := fixture.app.database.Rebind(`SELECT "title","author_id","optional_int" FROM ` + posts + ` WHERE "id" = ?`)
	if err := fixture.app.database.QueryRowxContext(context.Background(), query, mutationResultUUIDText(id)).Scan(&title, &author, &optional); err != nil {
		t.Fatal(err)
	}
	return title, author, optional
}

func (fixture nestedSystemFieldFixture) readUserName(t *testing.T, id byte) string {
	t.Helper()
	var name string
	users := nestedAcceptanceTable(fixture.app, fixture.schema.User)
	query := fixture.app.database.Rebind(`SELECT "name" FROM ` + users + ` WHERE "id" = ?`)
	if err := fixture.app.database.QueryRowxContext(context.Background(), query, mutationResultUUIDText(id)).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func (fixture nestedSystemFieldFixture) assertPost(t *testing.T, id byte, wantTitle string, wantAuthor byte, wantOptional int64) {
	t.Helper()
	title, author, optional := fixture.readPost(t, id)
	if title != wantTitle || !strings.EqualFold(author, mutationResultUUIDText(wantAuthor)) || optional == nil || *optional != wantOptional {
		t.Fatalf("persisted post %d title=%q author=%s optional=%v, want title=%q author=%s optional=%d", id, title, author, optional, wantTitle, mutationResultUUIDText(wantAuthor), wantOptional)
	}
}

func (fixture nestedSystemFieldFixture) alice() golem.MutationTarget[mutationResultUser] {
	return golem.GeneratedUniqueSelectorValue[mutationResultUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
}

func (fixture nestedSystemFieldFixture) nestedPostUpdate(id byte, fields ...golem.UpdateValue[mutationResultPost]) golem.UpdateInput[mutationResultUser] {
	return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedNestedUpdate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.target(id),
			golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post, fields...)),
	)
}

func (fixture nestedSystemFieldFixture) nestedPostUpdateMany(id byte, fields ...golem.UpdateManyValue[mutationResultPost]) golem.UpdateInput[mutationResultUser] {
	return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedNestedUpdateMany[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.postID.Eq(golem.UUID{15: id}),
			golem.GeneratedUpdateManyInput[mutationResultPost](fixture.schema.Post, fields...)),
	)
}

func (fixture nestedSystemFieldFixture) nestedPostConnect(id byte) golem.UpdateInput[mutationResultUser] {
	return golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
		golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.target(id)),
	)
}

func nestedPostUpdateHook(value int64) nestedSystemFieldHooks {
	return func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string], optional golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, title, "hook kept this"),
					golem.GeneratedSetFieldValue(schema.Post, optional, value),
				))
				return nil
			}),
		}
	}
}

func nestedPostUpdateManyHook(value int64) nestedSystemFieldHooks {
	return func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string], optional golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateManyHookRequest[mutationResultPost]](schema.Post, golem.HookUpdateMany, func(_ context.Context, request *golem.UpdateManyHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateManyInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, title, "hook kept this"),
					golem.GeneratedSetFieldValue(schema.Post, optional, value),
				))
				return nil
			}),
		}
	}
}

func nestedMembershipHook(value int64) nestedSystemFieldHooks {
	return func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string], optional golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
		author := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.AuthorID)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultPost](schema.Post,
					golem.GeneratedSetFieldValue(schema.Post, author, golem.UUID{15: 1}),
					golem.GeneratedSetFieldValue(schema.Post, optional, value),
				))
				return nil
			}),
		}
	}
}

func TestNestedChildBeforeHookWritesASystemFieldOnTheChild(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: nestedPostUpdateHook(7)}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 81, 1, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		input := fixture.nestedPostUpdate(81, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"))
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), input); err != nil {
			t.Fatalf("nested child hook-authored system write was refused: %v: %v", err, errorChain(err))
		}
		fixture.assertPost(t, 81, "hook kept this", 1, 7)
	})
}

func TestNestedCreatedChildBeforeHookWritesASystemFieldOnTheChild(t *testing.T) {
	decimal, err := golem.ParseDecimal("1.25")
	if err != nil {
		t.Fatal(err)
	}
	hooks := func(schema schematest.Fixture, title golem.TextField[mutationResultPost, string], optional golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
		postID := golem.GeneratedEqualField[mutationResultPost, golem.UUID](schema.PostID)
		bigInt := golem.GeneratedOrderedField[mutationResultPost, int64](schema.PostBigInt)
		decimalField := golem.GeneratedEqualField[mutationResultPost, golem.Decimal](schema.PostDecimal)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.CreateHookRequest[mutationResultPost]](schema.Post, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultPost]) error {
				request.ReplaceInput(golem.GeneratedCreateInput[mutationResultPost](schema.Post,
					golem.GeneratedCreateFieldValue(schema.Post, postID, golem.UUID{15: 82}),
					golem.GeneratedCreateFieldValue(schema.Post, title, "hook kept this"),
					golem.GeneratedCreateFieldValue(schema.Post, bigInt, int64(10)),
					golem.GeneratedCreateFieldValue(schema.Post, decimalField, decimal),
					golem.GeneratedCreateFieldValue(schema.Post, optional, int64(9)),
				))
				return nil
			}),
		}
	}
	forEachNestedSystemFieldProvider(t, nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: hooks}, func(t *testing.T, fixture nestedSystemFieldFixture) {
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		create := golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 82}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.bigInt, int64(10)),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.decimal, decimal),
		)
		input := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedNestedCreate[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, create),
		)
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), input); err != nil {
			t.Fatalf("nested created child hook-authored system write was refused: %v: %v", err, errorChain(err))
		}
		fixture.assertPost(t, 82, "hook kept this", 1, 9)
	})
}

func TestNestedChildSystemFieldTheCallerNamedStaysRefusedWhenTheHookRewritesIt(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: nestedPostUpdateHook(7)}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 83, 1, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		forged := fixture.nestedPostUpdate(83,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.optionalInt, int64(6)),
		)
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), forged); err == nil || !strings.Contains(errorChain(err), "system field is not caller writable") {
			t.Fatalf("a nested child hook rewriting the caller's own system-field value laundered it into a write: %v", err)
		}
		fixture.assertPost(t, 83, "seeded", 1, 5)
	})
}

func TestNestedChildHookCannotWriteASystemImmutableFieldOnUpdate(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem, compilerir.ModeImmutable}, hooks: nestedPostUpdateHook(7)}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 84, 1, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		input := fixture.nestedPostUpdate(84, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"))
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), input); err == nil || !strings.Contains(errorChain(err), "immutable field is writable only during create") {
			t.Fatalf("a nested child hook updated a system;immutable field: %v", err)
		}
		fixture.assertPost(t, 84, "seeded", 1, 5)
	})
}

func TestNestedChildCallerWriteToASystemFieldIsRefusedWithNoHookInPlay(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem}}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 85, 1, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		forged := fixture.nestedPostUpdate(85, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.optionalInt, int64(6)))
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), forged); err == nil || !strings.Contains(errorChain(err), "system field is not caller writable") {
			t.Fatalf("caller wrote a system field through a nested child: %v", err)
		}
		fixture.assertPost(t, 85, "seeded", 1, 5)
	})
}

func TestNestedCurrentToOneChildBeforeHookWritesASystemField(t *testing.T) {
	hooks := func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string], _ golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
		name := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.UpdateHookRequest[mutationResultUser]](schema.User, golem.HookUpdate, func(_ context.Context, request *golem.UpdateHookRequest[mutationResultUser]) error {
				request.ReplaceInput(golem.GeneratedUpdateInput[mutationResultUser](schema.User,
					golem.GeneratedSetFieldValue(schema.User, name, "renamed by the hook"),
				))
				return nil
			}),
		}
	}
	forEachNestedSystemFieldProvider(t, nestedSystemFieldSpec{field: systemUserName, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: hooks}, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 86, 1, "before", 5)
		userInput := golem.GeneratedUpdateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedNestedConnect[mutationResultUser, mutationResultPost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, fixture.target(86)),
		)
		input := golem.GeneratedUpdateInput[mutationResultPost](fixture.schema.Post,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
			golem.GeneratedNestedUpdate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, nil, userInput),
		)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdate(context.Background(), caller, fixture.postDescriptor, fixture.target(86), input); err != nil {
			t.Fatalf("current to-one child hook-authored system write was refused: %v: %v", err, errorChain(err))
		}
		if name := fixture.readUserName(t, 1); name != "renamed by the hook" {
			t.Fatalf("persisted name=%q, want the value the child hook authored", name)
		}
	})
}

func TestNestedUpdateManyChildBeforeHookWritesASystemField(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: nestedPostUpdateManyHook(7)}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 91, 1, "seeded", 5)
		fixture.seedPost(t, 92, 1, "untouched", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		input := fixture.nestedPostUpdateMany(91, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"))
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), input); err != nil {
			t.Fatalf("nested update-many hook-authored system write was refused: %v: %v", err, errorChain(err))
		}
		fixture.assertPost(t, 91, "hook kept this", 1, 7)
		fixture.assertPost(t, 92, "untouched", 1, 5)
	})
}

func TestNestedUpdateManySystemFieldTheCallerNamedStaysRefusedWhenTheHookRewritesIt(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: nestedPostUpdateManyHook(7)}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 93, 1, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		forged := fixture.nestedPostUpdateMany(93,
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
			golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.optionalInt, int64(6)),
		)
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), forged); err == nil || !strings.Contains(errorChain(err), "system field is not caller writable") {
			t.Fatalf("a nested update-many hook rewriting the caller's own system-field value laundered it into a write: %v", err)
		}
		fixture.assertPost(t, 93, "seeded", 1, 5)
	})
}

func TestNestedUpdateManyHookCannotWriteASystemImmutableField(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem, compilerir.ModeImmutable}, hooks: nestedPostUpdateManyHook(7)}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 94, 1, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		input := fixture.nestedPostUpdateMany(94, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"))
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), input); err == nil || !strings.Contains(errorChain(err), "immutable field is writable only during create") {
			t.Fatalf("a nested update-many hook updated a system;immutable field: %v", err)
		}
		fixture.assertPost(t, 94, "seeded", 1, 5)
	})
}

func TestNestedMembershipBeforeHookWritesASystemField(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: nestedMembershipHook(7)}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 95, 2, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), fixture.nestedPostConnect(95)); err != nil {
			t.Fatalf("membership hook-authored system write was refused: %v: %v", err, errorChain(err))
		}
		fixture.assertPost(t, 95, "seeded", 1, 7)
	})
}

func TestNestedMembershipHookCannotWriteASystemImmutableField(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemOptionalInt, modes: []compilerir.FieldMode{compilerir.ModeSystem, compilerir.ModeImmutable}, hooks: nestedMembershipHook(7)}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 96, 2, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), fixture.nestedPostConnect(96)); err == nil || !strings.Contains(errorChain(err), "immutable field is writable only during create") {
			t.Fatalf("a membership hook updated a system;immutable field: %v", err)
		}
		fixture.assertPost(t, 96, "seeded", 2, 5)
	})
}

func TestNestedMembershipHookCannotAuthorTheSystemForeignKeyTheCallerConnectAssigned(t *testing.T) {
	systemAuthor := func(fixture schematest.Fixture) (golem.ModelID, golem.FieldID) { return fixture.Post, fixture.AuthorID }
	passthrough := func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string], _ golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultPost, golem.UpdateHookRequest[mutationResultPost]](schema.Post, golem.HookUpdate, func(context.Context, *golem.UpdateHookRequest[mutationResultPost]) error {
				return nil
			}),
		}
	}
	forEachNestedSystemFieldProvider(t, nestedSystemFieldSpec{field: systemAuthor, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: passthrough}, func(t *testing.T, fixture nestedSystemFieldFixture) {
		fixture.seedPost(t, 97, 2, "seeded", 5)
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		if _, err := CallerUpdate(context.Background(), caller, fixture.userDescriptor, fixture.alice(), fixture.nestedPostConnect(97)); err == nil || !strings.Contains(errorChain(err), "system field is not caller writable") {
			t.Fatalf("a membership hook turned the caller's connect assignment of a system foreign key into a hook-authored write: %v", err)
		}
		fixture.assertPost(t, 97, "seeded", 2, 5)
	})
}

func nestedBeforeParentAuthorCreate(fixture nestedSystemFieldFixture, id byte, author golem.CreateInput[mutationResultUser]) golem.CreateInput[mutationResultPost] {
	decimal, _ := golem.ParseDecimal("1.25")
	return golem.GeneratedCreateInput[mutationResultPost](fixture.schema.Post,
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "caller wrote this"),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.bigInt, int64(10)),
		golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.decimal, decimal),
		golem.GeneratedNestedCreate[mutationResultPost, mutationResultUser](fixture.schema.Post, fixture.schema.PostAuthor, fixture.schema.Authorship, fixture.schema.User, author),
	)
}

func nestedBeforeParentUserCreateHook(name string) nestedSystemFieldHooks {
	return func(schema schematest.Fixture, _ golem.TextField[mutationResultPost, string], _ golem.NullableOrderedField[mutationResultPost, int64]) []golem.HookBinding[mutationResultActor] {
		userID := golem.GeneratedEqualField[mutationResultUser, golem.UUID](schema.UserID)
		userName := golem.GeneratedTextField[mutationResultUser, string](schema.UserName)
		return []golem.HookBinding[mutationResultActor]{
			golem.GeneratedBeforeHookBinding[mutationResultActor, mutationResultUser, golem.CreateHookRequest[mutationResultUser]](schema.User, golem.HookCreate, func(_ context.Context, request *golem.CreateHookRequest[mutationResultUser]) error {
				request.ReplaceInput(golem.GeneratedCreateInput[mutationResultUser](schema.User,
					golem.GeneratedCreateFieldValue(schema.User, userID, golem.UUID{15: 3}),
					golem.GeneratedCreateFieldValue(schema.User, userName, name),
				))
				return nil
			}),
		}
	}
}

func (fixture nestedSystemFieldFixture) countRows(t *testing.T, model golem.ModelID, id byte) int {
	t.Helper()
	var count int
	query := fixture.app.database.Rebind(`SELECT COUNT(*) FROM ` + nestedAcceptanceTable(fixture.app, model) + ` WHERE "id" = ?`)
	if err := fixture.app.database.GetContext(context.Background(), &count, query, mutationResultUUIDText(id)); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestNestedBeforeParentCreatedChildHookWritesASystemField(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemUserName, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: nestedBeforeParentUserCreateHook("hook named"), prepare: withNullableUserName}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		author := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 3}),
		)
		if _, err := CallerCreate(context.Background(), caller, fixture.postDescriptor, nestedBeforeParentAuthorCreate(fixture, 101, author)); err != nil {
			t.Fatalf("before-parent child hook-authored system write was refused: %v: %v", err, errorChain(err))
		}
		if name := fixture.readUserName(t, 3); name != "hook named" {
			t.Fatalf("persisted name=%q, want the value the before-parent child hook authored", name)
		}
		if title, author, _ := fixture.readPost(t, 101); title != "caller wrote this" || !strings.EqualFold(author, mutationResultUUIDText(3)) {
			t.Fatalf("persisted post title=%q author=%s, want the parent owned by the before-parent child", title, author)
		}
	})
}

func TestNestedBeforeParentCreatedChildSystemFieldTheCallerNamedStaysRefusedWhenTheHookRewritesIt(t *testing.T) {
	spec := nestedSystemFieldSpec{field: systemUserName, modes: []compilerir.FieldMode{compilerir.ModeSystem}, hooks: nestedBeforeParentUserCreateHook("hook rewrote this"), prepare: withNullableUserName}
	forEachNestedSystemFieldProvider(t, spec, func(t *testing.T, fixture nestedSystemFieldFixture) {
		caller := mustMutationResultCaller(t, fixture.mutationResultFixture)
		author := golem.GeneratedCreateInput[mutationResultUser](fixture.schema.User,
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userID, golem.UUID{15: 3}),
			golem.GeneratedCreateFieldValue(fixture.schema.User, fixture.userName, "caller wrote this"),
		)
		if _, err := CallerCreate(context.Background(), caller, fixture.postDescriptor, nestedBeforeParentAuthorCreate(fixture, 102, author)); err == nil || !strings.Contains(errorChain(err), "system field is not caller writable") {
			t.Fatalf("a before-parent child hook rewriting the caller's own system-field value laundered it into a write: %v", err)
		}
		if users, posts := fixture.countRows(t, fixture.schema.User, 3), fixture.countRows(t, fixture.schema.Post, 102); users != 0 || posts != 0 {
			t.Fatalf("refused before-parent create left users=%d posts=%d", users, posts)
		}
	})
}
