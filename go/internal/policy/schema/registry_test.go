package schema

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestRegistryIndexesImmutableLogicalRelationAndPhysicalFacts(t *testing.T) {
	bundle, ids := testBundle(t)
	registry, err := New(bundle)
	if err != nil {
		t.Fatal(err)
	}

	if got := registry.Providers(); len(got) != 2 || got[0] != golem.PostgreSQL || got[1] != golem.SQLite {
		t.Fatalf("providers = %v", got)
	}
	gotProviders := registry.Providers()
	gotProviders[0] = "forged"
	if registry.Providers()[0] != golem.PostgreSQL {
		t.Fatal("provider accessor leaked mutable registry storage")
	}

	post, ok := registry.Model(ids.post)
	if !ok || post.ID() != ids.post {
		t.Fatal("post model was not indexed")
	}
	if !registry.HasModel(ids.post) || registry.HasModel(golem.ModelID{0xff}) {
		t.Fatal("model identity membership disagrees with the registry index")
	}
	if !post.EqualityIndexed(ids.postID) || post.EqualityIndexed(ids.author) {
		t.Fatal("model equality-index facts do not include the leading primary key exactly")
	}
	user, _ := registry.Model(ids.user)
	if !post.SubscriptionsEnabled() || user.SubscriptionsEnabled() {
		t.Fatal("model subscription fact did not preserve the exact contract setting")
	}
	eventFingerprint, eventSnapshot, eventEnabled := post.EventSchema()
	if !eventEnabled || eventFingerprint == "" || len(eventSnapshot) != 3 || eventSnapshot[0] != ids.postID || eventSnapshot[1] != ids.author || eventSnapshot[2] != ids.price {
		t.Fatalf("event schema fingerprint=%q snapshot=%x enabled=%v", eventFingerprint, eventSnapshot, eventEnabled)
	}
	eventSnapshot[0] = golem.FieldID{99}
	_, freshSnapshot, _ := post.EventSchema()
	if freshSnapshot[0] != ids.postID {
		t.Fatal("event snapshot accessor leaked registry ownership")
	}
	if _, _, enabled := user.EventSchema(); enabled {
		t.Fatal("unsubscribed model exposed an event schema")
	}
	author, ok := registry.Field(ids.post, ids.author)
	if !ok || author.Kind() != compilerir.FieldScalar || author.ModelID() != ids.post {
		t.Fatalf("author field = %#v, %v", author, ok)
	}
	if !registry.HasField(ids.post, ids.author) || registry.HasField(ids.user, ids.author) {
		t.Fatal("field identity membership accepted an unknown or wrong-owner field")
	}
	if _, ok := registry.Field(ids.user, ids.author); ok {
		t.Fatal("cross-model field identity was accepted")
	}
	defaultValue, ok := author.Default()
	if !ok || defaultValue.Kind != compilerir.DefaultUUID || defaultValue.Producer != compilerir.ProducerApplication {
		t.Fatalf("author default = %#v, %v", defaultValue, ok)
	}
	price, ok := registry.Field(ids.post, ids.price)
	if !ok || !price.Updated() || !price.DatabaseReadOnly() {
		t.Fatalf("price write ownership = %#v, %v", price, ok)
	}
	generation, ok := price.Generation()
	if !ok || generation.Storage != compilerir.GeneratedStored || generation.Expr.Kind != compilerir.SchemaExprLiteral {
		t.Fatalf("price generation = %#v, %v", generation, ok)
	}
	generation.Expr.ReferencedFields = append(generation.Expr.ReferencedFields, compilerir.FieldID("forged"))
	secondGeneration, _ := price.Generation()
	if len(secondGeneration.Expr.ReferencedFields) != 0 {
		t.Fatal("generation accessor leaked mutable registry storage")
	}

	source, ok := registry.RelationEndpoint(ids.post, ids.authorRelationField, ids.relation)
	if !ok || source.TargetModelID() != ids.user || source.Cardinality() != compilerir.RelationOne || source.Role() != compilerir.RelationSource {
		t.Fatalf("source endpoint = %#v, %v", source, ok)
	}
	pairs := source.Correlation()
	if len(pairs) != 1 || pairs[0].ParentFieldID() != ids.author || pairs[0].ChildFieldID() != ids.userID {
		t.Fatalf("source correlation = %#v", pairs)
	}
	pairs[0] = Correlation{}
	if registryEndpoint, _ := registry.RelationEndpoint(ids.post, ids.authorRelationField, ids.relation); registryEndpoint.Correlation()[0].ParentFieldID() != ids.author {
		t.Fatal("correlation accessor leaked mutable registry storage")
	}
	inverse, ok := registry.RelationEndpoint(ids.user, ids.postsRelationField, ids.relation)
	if !ok || inverse.TargetModelID() != ids.post || inverse.Cardinality() != compilerir.RelationMany || inverse.Role() != compilerir.RelationInverse {
		t.Fatalf("inverse endpoint = %#v, %v", inverse, ok)
	}
	if _, ok := registry.RelationEndpoint(ids.post, ids.postsRelationField, ids.relation); ok {
		t.Fatal("cross-model relation endpoint was accepted")
	}
	if _, ok := registry.RelationEndpoint(ids.post, ids.authorRelationField, golem.RelationID{0xff}); ok {
		t.Fatal("forged relation identity was accepted")
	}

	physicalField, ok := registry.PhysicalField(golem.SQLite, ids.post, ids.author)
	if !ok || physicalField.TableName() != "posts" || physicalField.ColumnName() != "author_id" || physicalField.Storage().Kind != physical.StorageSQLiteText {
		t.Fatalf("SQLite physical field = %#v, %v", physicalField, ok)
	}
	if _, ok := registry.PhysicalField(golem.SQLite, ids.user, ids.author); ok {
		t.Fatal("cross-model physical field was accepted")
	}
	if physicalModel, ok := registry.PhysicalModel(golem.PostgreSQL, ids.user); !ok || physicalModel.Name() != "users" {
		t.Fatalf("PostgreSQL physical model = %#v, %v", physicalModel, ok)
	}
}

func TestNilRegistryIdentityMembershipFailsClosed(t *testing.T) {
	var registry *Registry
	if registry.HasModel(golem.ModelID{1}) || registry.HasField(golem.ModelID{1}, golem.FieldID{1}) {
		t.Fatal("nil registry reported a known identity")
	}
}

func TestRegistryOptimisticConcurrencyRequiresExactThreeWayAgreement(t *testing.T) {
	bundle, ids, token := concurrencyTestBundle(t)
	registry, err := New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	model, ok := registry.Model(ids.post)
	got, enabled := model.OptimisticConcurrency()
	if !ok || !enabled || got != token {
		t.Fatalf("optimistic concurrency = %x enabled=%v model=%v", got, enabled, ok)
	}
	copyModel := model
	copyModel.optimisticConcurrency = nil
	got, enabled = registryModelConcurrency(t, registry, ids.post)
	if !enabled || got != token {
		t.Fatal("model accessor mutation escaped into registry")
	}

	var contract compilerir.ContractIR
	if err := decodeCanonicalJSON(bundle.Contract().Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	contract.Models[1].OptimisticConcurrency = nil
	contractBytes, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	forgedContract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(t, string(contractFingerprint)), contractBytes)
	if _, err := New(golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), forgedContract, bundle.Providers()...)); !isSchemaFailure(err, CodeContract, ".optimisticConcurrency", "ModelIR and ContractIR optimistic-concurrency identities disagree") {
		t.Fatalf("contract omission error = %v", err)
	}

	providers := bundle.Providers()
	decoded, err := physical.CanonicalDecode(providers[0].Schema().Bytes())
	if err != nil {
		t.Fatal(err)
	}
	for index := range decoded.Tables {
		if decoded.Tables[index].ID == compilerir.ModelID(testID(2)) {
			decoded.Tables[index].OptimisticConcurrency = nil
		}
	}
	providers[0] = providerDocument(t, providers[0].Provider(), decoded)
	if _, err := New(golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), providers...)); !isSchemaFailure(err, CodePhysical, ".optimisticConcurrency", "ModelIR and physical optimistic-concurrency identities disagree") {
		t.Fatalf("physical omission error = %v", err)
	}

	var invalidModes compilerir.ContractIR
	if err := decodeCanonicalJSON(bundle.Contract().Bytes(), &invalidModes); err != nil {
		t.Fatal(err)
	}
	for modelIndex := range invalidModes.Models {
		for fieldIndex := range invalidModes.Models[modelIndex].Fields {
			if invalidModes.Models[modelIndex].Fields[fieldIndex].FieldID == compilerir.FieldID(testID(25)) {
				invalidModes.Models[modelIndex].Fields[fieldIndex].Modes = []compilerir.FieldMode{compilerir.ModeReadOnly}
			}
		}
	}
	invalidModeBytes, err := compilerir.CanonicalContract(invalidModes)
	if err != nil {
		t.Fatal(err)
	}
	invalidModeFingerprint, err := compilerir.ContractFingerprint(invalidModes)
	if err != nil {
		t.Fatal(err)
	}
	invalidModeDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(t, string(invalidModeFingerprint)), invalidModeBytes)
	if _, err := New(golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), invalidModeDocument, bundle.Providers()...)); !isSchemaFailure(err, CodeContract, ".optimisticConcurrency", "optimistic-concurrency field requires the exact ordinary visible contract mode") {
		t.Fatalf("non-visible concurrency contract mode error = %v", err)
	}
}

func registryModelConcurrency(t *testing.T, registry *Registry, model golem.ModelID) (golem.FieldID, bool) {
	t.Helper()
	fact, ok := registry.Model(model)
	if !ok {
		t.Fatal("model absent")
	}
	return fact.OptimisticConcurrency()
}

func TestRegistryRejectsMalformedOrMismatchedBundleFacts(t *testing.T) {
	bundle, _ := testBundle(t)
	modelDocument := bundle.Model()
	contractDocument := bundle.Contract()
	providers := bundle.Providers()

	tests := []struct {
		name   string
		bundle golem.SchemaBundle
		code   ErrorCode
	}{
		{
			name:   "zero generation digest",
			bundle: golem.GeneratedSchemaBundle(golem.SchemaDigest{}, "generator", "abi", modelDocument, contractDocument, providers...),
			code:   CodeBundle,
		},
		{
			name:   "model fingerprint mismatch",
			bundle: golem.GeneratedSchemaBundle(bundle.GenerationDigest(), "generator", "abi", golem.GeneratedSchemaDocument(modelDocument.FormatVersion(), modelDocument.CanonicalVersion(), golem.SchemaDigest{0xff}, modelDocument.Bytes()), contractDocument, providers...),
			code:   CodeFingerprint,
		},
		{
			name:   "noncanonical model JSON",
			bundle: golem.GeneratedSchemaBundle(bundle.GenerationDigest(), "generator", "abi", golem.GeneratedSchemaDocument(modelDocument.FormatVersion(), modelDocument.CanonicalVersion(), modelDocument.Fingerprint(), append(modelDocument.Bytes(), ' ')), contractDocument, providers...),
			code:   CodeDocument,
		},
		{
			name:   "missing provider document",
			bundle: golem.GeneratedSchemaBundle(bundle.GenerationDigest(), "generator", "abi", modelDocument, contractDocument, providers[0]),
			code:   CodeProvider,
		},
		{
			name: "outer provider mismatch",
			bundle: golem.GeneratedSchemaBundle(bundle.GenerationDigest(), "generator", "abi", modelDocument, contractDocument,
				golem.GeneratedProviderSchemaDocument(golem.SQLite, providers[1].SystemFingerprint(), providers[1].Schema()),
				golem.GeneratedProviderSchemaDocument(golem.PostgreSQL, providers[0].SystemFingerprint(), providers[0].Schema())),
			code: CodeProvider,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.bundle)
			var schemaErr *Error
			if !errors.As(err, &schemaErr) || schemaErr.Code != test.code {
				t.Fatalf("error = %v, want code %s", err, test.code)
			}
		})
	}
}

func TestRegistryRejectsForgedLogicalOwnershipEvenWithSelfConsistentFingerprint(t *testing.T) {
	bundle, _ := testBundle(t)
	var model compilerir.ModelIR
	if err := decodeCanonicalJSON(bundle.Model().Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	// Move the Post.Author scalar ID into User as a duplicate. Canonical bytes
	// and their fingerprint are recomputed, so only registry ownership proof can
	// reject the forged document.
	model.Models[0].Fields = append(model.Models[0].Fields, model.Models[1].Fields[1])
	modelBytes, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	forgedModel := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(t, string(modelFingerprint)), modelBytes)
	forged := golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), forgedModel, bundle.Contract(), bundle.Providers()...)
	_, err = New(forged)
	var schemaErr *Error
	if !errors.As(err, &schemaErr) || schemaErr.Code != CodeField {
		t.Fatalf("error = %v, want %s", err, CodeField)
	}
}

func TestRegistryRejectsSelfConsistentPhysicalStorageForgery(t *testing.T) {
	bundle, ids := testBundle(t)
	tests := []struct {
		name     string
		provider golem.Provider
		field    golem.FieldID
		forge    func(*physical.StorageType)
	}{
		{name: "sqlite kind", provider: golem.SQLite, field: ids.userID, forge: func(storage *physical.StorageType) { storage.Kind = physical.StorageSQLiteInteger }},
		{name: "sqlite length", provider: golem.SQLite, field: ids.postID, forge: func(storage *physical.StorageType) { storage.Length = 127 }},
		{name: "postgresql precision", provider: golem.PostgreSQL, field: ids.price, forge: func(storage *physical.StorageType) { storage.Precision = 17 }},
		{name: "postgresql scale", provider: golem.PostgreSQL, field: ids.price, forge: func(storage *physical.StorageType) { storage.Scale = 3 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providers := bundle.Providers()
			for index, document := range providers {
				if document.Provider() != test.provider {
					continue
				}
				decoded, err := physical.CanonicalDecodeVerified(document.Schema().Bytes(), physical.Digest(document.Schema().Fingerprint()), physical.Digest(document.SystemFingerprint()))
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for tableIndex := range decoded.Tables {
					for columnIndex := range decoded.Tables[tableIndex].Columns {
						column := &decoded.Tables[tableIndex].Columns[columnIndex]
						fid, conversionErr := fieldID(column.ID)
						if conversionErr == nil && fid == test.field {
							test.forge(&column.Storage)
							found = true
						}
					}
				}
				if !found {
					t.Fatalf("field %s not found in %s physical schema", test.field, test.provider)
				}
				// Re-encoding and re-fingerprinting proves the forged physical
				// document is internally self-consistent. Registry correlation with
				// ModelIR must still reject it.
				providers[index] = providerDocument(t, test.provider, decoded)
			}
			forged := golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), providers...)
			_, err := New(forged)
			var schemaErr *Error
			if !errors.As(err, &schemaErr) || schemaErr.Code != CodePhysical || !strings.HasSuffix(schemaErr.Path, ".storage") {
				t.Fatalf("error = %v, want %s at storage path", err, CodePhysical)
			}
		})
	}
}

type testIDs struct {
	user, post                              golem.ModelID
	userID, postID, author, price           golem.FieldID
	authorRelationField, postsRelationField golem.FieldID
	relation                                golem.RelationID
}

func testBundle(t *testing.T) (golem.SchemaBundle, testIDs) {
	t.Helper()
	userID, postID := compilerir.ModelID(testID(1)), compilerir.ModelID(testID(2))
	// Keep the subscribed Post's declaration order deliberately different from
	// canonical FieldID order. Event snapshot order is semantic and must survive
	// canonical ModelIR sorting.
	userKey, postKey, author, price := compilerir.FieldID(testID(11)), compilerir.FieldID(testID(24)), compilerir.FieldID(testID(22)), compilerir.FieldID(testID(21))
	authorRelation, postsRelation := compilerir.FieldID(testID(23)), compilerir.FieldID(testID(12))
	relationID := compilerir.RelationID(testID(31))
	maxLength := uint32(128)
	model := compilerir.ModelIR{
		FormatVersion: compilerir.ModelFormatVersion,
		Providers:     []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL},
		Models: []compilerir.ModelDeclIR{
			{ID: userID, LogicalName: "User", Table: compilerir.TableBindingIR{PhysicalName: "users"}, Fields: []compilerir.FieldIR{
				logicalScalar(userKey, "ID", "id", compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, false),
				logicalRelation(postsRelation, "Posts", relationID, compilerir.RelationInverse, compilerir.RelationHasMany),
			}, PrimaryKey: &compilerir.KeyIR{ID: compilerir.KeyID(testID(41)), Kind: compilerir.KeyPrimary, PhysicalName: "pk_users", Fields: []compilerir.FieldID{userKey}}},
			{ID: postID, LogicalName: "Post", Table: compilerir.TableBindingIR{PhysicalName: "posts"}, Fields: []compilerir.FieldIR{
				logicalScalar(postKey, "ID", "id", compilerir.LogicalTypeIR{Kind: compilerir.TypeString, MaxLength: &maxLength}, false),
				logicalScalar(author, "AuthorID", "author_id", compilerir.LogicalTypeIR{Kind: compilerir.TypeUUID}, false),
				logicalScalar(price, "Price", "price", compilerir.LogicalTypeIR{Kind: compilerir.TypeDecimal, Precision: uint16Pointer(18), Scale: uint16Pointer(4)}, false),
				logicalRelation(authorRelation, "Author", relationID, compilerir.RelationSource, compilerir.RelationBelongsTo),
			}, PrimaryKey: &compilerir.KeyIR{ID: compilerir.KeyID(testID(42)), Kind: compilerir.KeyPrimary, PhysicalName: "pk_posts", Fields: []compilerir.FieldID{postKey}}},
		},
		Relations: []compilerir.RelationIR{{ID: relationID, SourceModel: postID, TargetModel: userID, SourceField: authorRelation, InverseField: &postsRelation, Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{author}, RemoteFields: []compilerir.FieldID{userKey}}},
	}
	model.Models[1].Fields[1].Scalar.Default = &compilerir.DefaultIR{Kind: compilerir.DefaultUUID, Producer: compilerir.ProducerApplication}
	priceType := model.Models[1].Fields[2].Scalar.Type
	model.Models[1].Fields[2].Scalar.Updated = true
	model.Models[1].Fields[2].Scalar.DatabaseReadOnly = true
	model.Models[1].Fields[2].Scalar.Generation = &compilerir.GeneratedColumnIR{
		Expr: compilerir.SchemaExprIR{
			Kind: compilerir.SchemaExprLiteral, ResultType: priceType,
			Literal:  &compilerir.TypedLiteralIR{Kind: compilerir.LiteralDecimal, Canonical: "1"},
			Provider: compilerir.ProviderScopePortable, Volatility: compilerir.SchemaVolatilityImmutable, Deterministic: true,
		},
		Storage: compilerir.GeneratedStored, Provider: compilerir.ProviderScopePortable,
	}
	postSnapshot := []compilerir.FieldID{postKey, author, price}
	postEventSchema, err := compilerir.BuildEventSchemaShape(model.Models[1], model.Enums, postSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	postEventFingerprint, err := compilerir.EventSchemaFingerprint(postEventSchema)
	if err != nil {
		t.Fatal(err)
	}
	contract := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{
		{ModelID: userID, Fields: []compilerir.FieldContractIR{{FieldID: userKey}, {FieldID: postsRelation}}},
		{ModelID: postID, Fields: []compilerir.FieldContractIR{{FieldID: postKey}, {FieldID: author}, {FieldID: price}, {FieldID: authorRelation}}, Subscriptions: true, Event: &compilerir.EventContractIR{
			PayloadTypeName: "PostEvent", MetadataFields: []string{"eventID", "type", "id", "entity", "causationID", "transactionOrdinal", "recordedAt"}, DeleteSnapshotFull: true,
			Schema: postEventSchema, SchemaFingerprint: postEventFingerprint,
		}},
	}}

	modelBytes, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	contractBytes, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	modelDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(t, string(modelFingerprint)), modelBytes)
	contractDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(t, string(contractFingerprint)), contractBytes)

	providerDocuments := []golem.ProviderSchemaDocument{
		providerDocument(t, golem.SQLite, physicalSchema(compilerir.SQLite, userID, postID, userKey, postKey, author, price)),
		providerDocument(t, golem.PostgreSQL, physicalSchema(compilerir.PostgreSQL, userID, postID, userKey, postKey, author, price)),
	}
	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{1}, "generator", "abi", modelDocument, contractDocument, providerDocuments...)
	return bundle, testIDs{user: mustModelID(t, userID), post: mustModelID(t, postID), userID: mustFieldID(t, userKey), postID: mustFieldID(t, postKey), author: mustFieldID(t, author), price: mustFieldID(t, price), authorRelationField: mustFieldID(t, authorRelation), postsRelationField: mustFieldID(t, postsRelation), relation: mustRelationID(t, relationID)}
}

func concurrencyTestBundle(t *testing.T) (golem.SchemaBundle, testIDs, golem.FieldID) {
	t.Helper()
	base, ids := testBundle(t)
	field := compilerir.FieldID(testID(25))
	var model compilerir.ModelIR
	if err := decodeCanonicalJSON(base.Model().Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	model.Models[1].Fields = append(model.Models[1].Fields, logicalScalar(field, "Version", "version", compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, false))
	model.Models[1].OptimisticConcurrency = &field
	modelBytes, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	modelDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(t, string(modelFingerprint)), modelBytes)

	var contract compilerir.ContractIR
	if err := decodeCanonicalJSON(base.Contract().Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	contract.Models[1].Fields = append(contract.Models[1].Fields, compilerir.FieldContractIR{FieldID: field, Modes: []compilerir.FieldMode{compilerir.ModeVisible}})
	contract.Models[1].OptimisticConcurrency = &field
	eventSchema, err := compilerir.BuildEventSchemaShape(model.Models[1], model.Enums, []compilerir.FieldID{compilerir.FieldID(testID(24)), compilerir.FieldID(testID(22)), compilerir.FieldID(testID(21)), field})
	if err != nil {
		t.Fatal(err)
	}
	eventFingerprint, err := compilerir.EventSchemaFingerprint(eventSchema)
	if err != nil {
		t.Fatal(err)
	}
	contract.Models[1].Event.Schema = eventSchema
	contract.Models[1].Event.SchemaFingerprint = eventFingerprint
	contractBytes, err := compilerir.CanonicalContract(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(contract)
	if err != nil {
		t.Fatal(err)
	}
	contractDocument := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), digest(t, string(contractFingerprint)), contractBytes)

	providers := base.Providers()
	for index, document := range providers {
		schema, decodeErr := physical.CanonicalDecode(document.Schema().Bytes())
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		for tableIndex := range schema.Tables {
			if schema.Tables[tableIndex].ID != compilerir.ModelID(testID(2)) {
				continue
			}
			storage := physical.StorageType{Kind: physical.StorageSQLiteInteger}
			if schema.Provider.Provider == compilerir.PostgreSQL {
				storage = physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
			}
			schema.Tables[tableIndex].Columns = append(schema.Tables[tableIndex].Columns, physical.PhysicalColumn{ID: field, Name: "version", Ordinal: uint32(len(schema.Tables[tableIndex].Columns)), Storage: storage, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
			value := field
			schema.Tables[tableIndex].OptimisticConcurrency = &value
		}
		providers[index] = providerDocument(t, document.Provider(), schema)
	}
	return golem.GeneratedSchemaBundle(base.GenerationDigest(), base.GeneratorVersion(), base.TemplateABIVersion(), modelDocument, contractDocument, providers...), ids, mustFieldID(t, field)
}

func uint16Pointer(value uint16) *uint16 { return &value }

func logicalScalar(id compilerir.FieldID, name, column string, typ compilerir.LogicalTypeIR, nullable bool) compilerir.FieldIR {
	return compilerir.FieldIR{ID: id, GoName: name, LogicalName: name, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: compilerir.SQLIdentifier(column), Type: typ, Nullable: nullable}}
}

func logicalRelation(id compilerir.FieldID, name string, relation compilerir.RelationID, role compilerir.RelationEndpointRole, kind compilerir.RelationKind) compilerir.FieldIR {
	return compilerir.FieldIR{ID: id, GoName: name, LogicalName: name, Kind: compilerir.FieldRelation, Relation: &compilerir.RelationFieldIR{RelationID: relation, Role: role, Kind: kind}}
}

func physicalSchema(provider compilerir.Provider, user, post compilerir.ModelID, userID, postID, author, price compilerir.FieldID) physical.PhysicalSchema {
	manifest, namespace := physical.PostgreSQLManifest(), physical.PhysicalName("public")
	uuidStorage := physical.StoragePostgreSQLUUID
	stringType := physical.StorageType{Kind: physical.StoragePostgreSQLVarchar, Length: 128}
	priceType := physical.StorageType{Kind: physical.StoragePostgreSQLNumeric, Precision: 18, Scale: 4}
	if provider == compilerir.SQLite {
		manifest, namespace = physical.SQLiteManifest(), "main"
		uuidStorage = physical.StorageSQLiteText
		stringType = physical.StorageType{Kind: physical.StorageSQLiteText, Length: 128}
		priceType = physical.StorageType{Kind: physical.StorageSQLiteInteger}
	}
	return physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: manifest, Namespace: physical.Namespace{Name: namespace},
		Tables: []physical.PhysicalTable{
			{ID: user, Name: "users", Columns: []physical.PhysicalColumn{{ID: userID, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: uuidStorage}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}, PrimaryKey: &physical.PhysicalKey{ID: compilerir.KeyID(testID(41)), Name: "pk_users", Columns: []compilerir.FieldID{userID}}},
			{ID: post, Name: "posts", Columns: []physical.PhysicalColumn{{ID: postID, Name: "id", Ordinal: 0, Storage: stringType, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}, {ID: author, Name: "author_id", Ordinal: 1, Storage: physical.StorageType{Kind: uuidStorage}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}, {ID: price, Name: "price", Ordinal: 2, Storage: priceType, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}, PrimaryKey: &physical.PhysicalKey{ID: compilerir.KeyID(testID(42)), Name: "pk_posts", Columns: []compilerir.FieldID{postID}}},
		},
	}
}

func providerDocument(t *testing.T, provider golem.Provider, value physical.PhysicalSchema) golem.ProviderSchemaDocument {
	t.Helper()
	payload, err := physical.CanonicalEncode(value)
	if err != nil {
		t.Fatal(err)
	}
	physicalFingerprint, err := physical.PhysicalFingerprint(value)
	if err != nil {
		t.Fatal(err)
	}
	systemFingerprint, err := physical.SystemFingerprint(value.Provider, value.System)
	if err != nil {
		t.Fatal(err)
	}
	document := golem.GeneratedSchemaDocument(value.Version, value.CanonicalVersion, golem.SchemaDigest(physicalFingerprint), payload)
	return golem.GeneratedProviderSchemaDocument(provider, golem.SchemaDigest(systemFingerprint), document)
}

func digest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("digest %q: %v", value, err)
	}
	var result golem.SchemaDigest
	copy(result[:], decoded)
	return result
}

func mustModelID(t *testing.T, value compilerir.ModelID) golem.ModelID {
	t.Helper()
	result, err := modelID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func mustFieldID(t *testing.T, value compilerir.FieldID) golem.FieldID {
	t.Helper()
	result, err := fieldID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func mustRelationID(t *testing.T, value compilerir.RelationID) golem.RelationID {
	t.Helper()
	result, err := relationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func testID(value int) string { return fmt.Sprintf("%032x", value) }

func isSchemaFailure(err error, code ErrorCode, pathSuffix, detail string) bool {
	var failure *Error
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Code == code && strings.HasSuffix(failure.Path, pathSuffix) && failure.Detail == detail
}
