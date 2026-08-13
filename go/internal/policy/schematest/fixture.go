// Package schematest provides the smallest complete, fingerprinted two-model
// schema used by policy and read integration tests. It deliberately exercises
// both relation directions and both supported providers.
package schematest

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type Fixture struct {
	Bundle     golem.SchemaBundle
	Registry   *schema.Registry
	SQLite     physical.PhysicalSchema
	PostgreSQL physical.PhysicalSchema

	User, Post       golem.ModelID
	UserID, UserName golem.FieldID
	PostID, AuthorID golem.FieldID
	PostTitle        golem.FieldID
	PostBigInt       golem.FieldID
	PostDecimal      golem.FieldID
	PostOptionalInt  golem.FieldID
	PostNullableText golem.FieldID
	PostBytes        golem.FieldID
	PostJSON         golem.FieldID
	PostList         golem.FieldID
	PostDateTime     golem.FieldID
	UserPosts        golem.FieldID
	PostAuthor       golem.FieldID
	Authorship       golem.RelationID
	UserKey, PostKey golem.KeyID
}

// ContractModes customizes field exposure for tests that need to exercise the
// public-schema boundary. A nil or empty slice keeps the field publicly
// visible, matching the generated contract's source-compatible default.
type ContractModes struct {
	UserID, UserName, UserPosts             []compilerir.FieldMode
	PostID, AuthorID, PostTitle, PostAuthor []compilerir.FieldMode
}

func New(t testing.TB) Fixture {
	return NewWithMaxTake(t, 0, 0)
}

// NewIndexed is the same logical fixture with an equality-indexed Post.AuthorID
// relation key, used to exercise the correlated read strategy.
func NewIndexed(t testing.TB) Fixture {
	return newFixture(t, 0, 0, ContractModes{}, true, false)
}

// NewSubscribedIndexed is the indexed fixture with durable Post subscription
// facts enabled in ContractIR. User remains unsubscribed.
func NewSubscribedIndexed(t testing.TB) Fixture {
	return newFixtureWithSubscriptions(t, 0, 0, ContractModes{}, true, false, true)
}

func NewSubscribedIndexedPostgreSQLNamespace(t testing.TB, namespace physical.PhysicalName) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, false, false, true, false, namespace, "_golem", false, false, false)
}

func NewSubscribedIndexedPostgreSQLNamespaces(t testing.TB, namespace, systemNamespace physical.PhysicalName) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, false, false, true, false, namespace, systemNamespace, false, false, false)
}

// NewSubscribedIndexedInverseRequiredHasOne is the ordinary subscribed
// authorship fixture with the inverse endpoint narrowed to has-one. AuthorID
// remains non-null, so User.Post is a required inverse one endpoint whose
// optional-only Disconnect surface must be rejected before SQL.
func NewSubscribedIndexedInverseRequiredHasOne(t testing.TB) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, false, false, true, false, "public", "_golem", true, false, false)
}

func NewSubscribedIndexedInverseRequiredHasOnePostgreSQLNamespaces(t testing.TB, namespace, systemNamespace physical.PhysicalName) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, false, false, true, false, namespace, systemNamespace, true, false, false)
}

func NewSubscribedIndexedOptionalSource(t testing.TB) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, false, false, true, false, "public", "_golem", false, true, false)
}

func NewSubscribedIndexedOptionalSourcePostgreSQLNamespaces(t testing.TB, namespace, systemNamespace physical.PhysicalName) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, false, false, true, false, namespace, systemNamespace, false, true, false)
}

// NewIndexedExact adds portable Int64 and Decimal(18,13) child fields to the
// indexed fixture. It exists for live correlated JSON exactness acceptance.
func NewIndexedExact(t testing.TB) Fixture {
	return newFixture(t, 0, 0, ContractModes{}, true, true)
}

func NewIndexedExactScoped(t testing.TB) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, true, false, false, false, "public", "_golem", false, false, true)
}

// NewMutationVocabulary exposes exact numeric fields plus one nullable numeric
// field for live public Set/Null/Increment/Decrement mutation acceptance.
func NewMutationVocabulary(t testing.TB) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, true, true, true, false, "public", "_golem", false, false, false)
}

func NewMutationVocabularyPostgreSQLNamespaces(t testing.TB, namespace, systemNamespace physical.PhysicalName) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, true, true, true, false, namespace, systemNamespace, false, false, false)
}

// NewOptimisticConcurrency is the complete portable Post fixture with its
// existing logical int64 field explicitly owned as the version token. It is a
// real bootstrap bundle for runtime CAS tests, not a mutable registry fake.
func NewOptimisticConcurrency(t testing.TB) Fixture {
	return newFixtureConfiguredWithConcurrency(t, 0, 0, ContractModes{}, true, true, true, true, false, "public", "_golem", false, false, false, true)
}

func NewOptimisticConcurrencyPostgreSQLNamespaces(t testing.TB, namespace, systemNamespace physical.PhysicalName) Fixture {
	return newFixtureConfiguredWithConcurrency(t, 0, 0, ContractModes{}, true, true, true, true, false, namespace, systemNamespace, false, false, false, true)
}

// NewMutationExactValues adds the complete live cross-provider mutation value
// vocabulary required by P4: nullable scalar, bytes, JSON, scalar-list JSON,
// Decimal, BigInt, and microsecond DateTime.
func NewMutationExactValues(t testing.TB) Fixture {
	return newFixtureConfigured(t, 0, 0, ContractModes{}, true, true, false, false, true, "public", "_golem", false, false, false)
}

func NewWithMaxTake(t testing.TB, userMaxTake, postMaxTake uint32) Fixture {
	return newFixture(t, userMaxTake, postMaxTake, ContractModes{}, false, false)
}

func NewWithContractModes(t testing.TB, modes ContractModes) Fixture {
	return newFixture(t, 0, 0, modes, false, false)
}

func newFixture(t testing.TB, userMaxTake, postMaxTake uint32, modes ContractModes, indexedAuthor, exactValues bool) Fixture {
	return newFixtureWithSubscriptions(t, userMaxTake, postMaxTake, modes, indexedAuthor, exactValues, false)
}

func newFixtureWithSubscriptions(t testing.TB, userMaxTake, postMaxTake uint32, modes ContractModes, indexedAuthor, exactValues, postSubscriptions bool) Fixture {
	return newFixtureConfigured(t, userMaxTake, postMaxTake, modes, indexedAuthor, exactValues, false, postSubscriptions, false, "public", "_golem", false, false, false)
}

func newFixtureConfigured(t testing.TB, userMaxTake, postMaxTake uint32, modes ContractModes, indexedAuthor, exactValues, mutationVocabulary, postSubscriptions, fullExactValues bool, postgresNamespace, postgresSystemNamespace physical.PhysicalName, inverseHasOne, nullableAuthor, scopedReads bool) Fixture {
	return newFixtureConfiguredWithConcurrency(t, userMaxTake, postMaxTake, modes, indexedAuthor, exactValues, mutationVocabulary, postSubscriptions, fullExactValues, postgresNamespace, postgresSystemNamespace, inverseHasOne, nullableAuthor, scopedReads, false)
}

func newFixtureConfiguredWithConcurrency(t testing.TB, userMaxTake, postMaxTake uint32, modes ContractModes, indexedAuthor, exactValues, mutationVocabulary, postSubscriptions, fullExactValues bool, postgresNamespace, postgresSystemNamespace physical.PhysicalName, inverseHasOne, nullableAuthor, scopedReads, optimisticConcurrency bool) Fixture {
	t.Helper()
	user, post := compilerir.ModelID(id(1)), compilerir.ModelID(id(2))
	userID, userName := compilerir.FieldID(id(11)), compilerir.FieldID(id(12))
	userPosts := compilerir.FieldID(id(13))
	postID, authorID, postTitle := compilerir.FieldID(id(21)), compilerir.FieldID(id(22)), compilerir.FieldID(id(23))
	postAuthor := compilerir.FieldID(id(24))
	postBigInt, postDecimal, postOptionalInt := compilerir.FieldID(id(25)), compilerir.FieldID(id(26)), compilerir.FieldID(id(27))
	postNullableText, postBytes, postJSON, postList, postDateTime := compilerir.FieldID(id(28)), compilerir.FieldID(id(29)), compilerir.FieldID(id(30)), compilerir.FieldID(id(32)), compilerir.FieldID(id(33))
	authorship := compilerir.RelationID(id(31))
	userKey, postKey := compilerir.KeyID(id(41)), compilerir.KeyID(id(42))

	inverseKind, cardinality := compilerir.RelationHasMany, compilerir.RelationMany
	if inverseHasOne {
		inverseKind, cardinality = compilerir.RelationHasOne, compilerir.RelationOne
	}
	model := compilerir.ModelIR{
		FormatVersion: compilerir.ModelFormatVersion,
		Providers:     []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL},
		Models: []compilerir.ModelDeclIR{
			{ID: user, LogicalName: "User", Table: compilerir.TableBindingIR{PhysicalName: "users"}, Fields: []compilerir.FieldIR{
				scalar(userID, "ID", "id", compilerir.TypeUUID, false),
				scalar(userName, "Name", "name", compilerir.TypeString, false),
				relation(userPosts, "Posts", authorship, compilerir.RelationInverse, inverseKind),
			}, PrimaryKey: &compilerir.KeyIR{ID: userKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_users", Fields: []compilerir.FieldID{userID}}},
			{ID: post, LogicalName: "Post", Table: compilerir.TableBindingIR{PhysicalName: "posts"}, Fields: []compilerir.FieldIR{
				scalar(postID, "ID", "id", compilerir.TypeUUID, false),
				scalar(authorID, "AuthorID", "author_id", compilerir.TypeUUID, false),
				scalar(postTitle, "Title", "title", compilerir.TypeString, false),
				relation(postAuthor, "Author", authorship, compilerir.RelationSource, compilerir.RelationBelongsTo),
			}, PrimaryKey: &compilerir.KeyIR{ID: postKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_posts", Fields: []compilerir.FieldID{postID}}},
		},
		Relations: []compilerir.RelationIR{{ID: authorship, SourceModel: post, TargetModel: user, SourceField: postAuthor, InverseField: &userPosts, Cardinality: cardinality, LocalFields: []compilerir.FieldID{authorID}, RemoteFields: []compilerir.FieldID{userID}}},
	}
	if nullableAuthor {
		model.Models[1].Fields[1].Scalar.Nullable = true
	}
	if nullableAuthor || inverseHasOne {
		model.Relations[0].ForeignKey = &compilerir.ForeignKeyIR{ID: compilerir.ForeignKeyID(id(44)), PhysicalName: "fk_posts_author", OnUpdate: compilerir.ActionRestrict, OnDelete: compilerir.ActionRestrict, Match: compilerir.MatchSimple, Deferrable: compilerir.NotDeferrable}
	}
	if exactValues {
		precision, scale := uint16(18), uint16(13)
		model.Models[1].Fields = append(model.Models[1].Fields,
			compilerir.FieldIR{ID: postBigInt, GoName: "BigInt", LogicalName: "BigInt", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "big_int", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}}},
			compilerir.FieldIR{ID: postDecimal, GoName: "Decimal", LogicalName: "Decimal", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "decimal_value", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeDecimal, Precision: &precision, Scale: &scale}}},
		)
	}
	if optimisticConcurrency {
		field := postBigInt
		model.Models[1].OptimisticConcurrency = &field
		// OC runtime retry acceptance also carries one application-owned Updated
		// value so the end-to-end kernel can prove preparation happens once.
		updated := scalar(postDateTime, "DateTime", "datetime_value", compilerir.TypeDateTime, true)
		precision := uint16(6)
		updated.Scalar.Type.Precision, updated.Scalar.Updated = &precision, true
		model.Models[1].Fields = append(model.Models[1].Fields, updated)
	}
	if mutationVocabulary {
		model.Models[1].Fields = append(model.Models[1].Fields, compilerir.FieldIR{ID: postOptionalInt, GoName: "OptionalInt", LogicalName: "OptionalInt", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "optional_int", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeInt64}, Nullable: true}})
	}
	if fullExactValues {
		listElement := compilerir.LogicalTypeIR{Kind: compilerir.TypeString}
		listCapability := compilerir.CapabilityID("scalar-list:json-array:v1")
		precision := uint16(6)
		model.Models[1].Fields = append(model.Models[1].Fields,
			compilerir.FieldIR{ID: postNullableText, GoName: "NullableText", LogicalName: "NullableText", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "nullable_text", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, Nullable: true}},
			compilerir.FieldIR{ID: postBytes, GoName: "Bytes", LogicalName: "Bytes", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "bytes_value", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeBytes}, Nullable: true}},
			compilerir.FieldIR{ID: postJSON, GoName: "JSON", LogicalName: "JSON", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "json_value", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeJSON}, Nullable: true}},
			compilerir.FieldIR{ID: postList, GoName: "List", LogicalName: "List", Kind: compilerir.FieldScalarList, Scalar: &compilerir.ScalarFieldIR{Column: "list_value", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeScalarList, Element: &listElement, Capability: &listCapability}, Nullable: true}},
			compilerir.FieldIR{ID: postDateTime, GoName: "DateTime", LogicalName: "DateTime", Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: "datetime_value", Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: &precision}, Nullable: true}},
		)
	}
	if indexedAuthor {
		indexID := compilerir.IndexID(id(43))
		fieldID := authorID
		model.Models[1].Indexes = []compilerir.IndexIR{{ID: indexID, ModelID: post, PhysicalName: "idx_posts_author", Method: compilerir.IndexBTree, Keys: []compilerir.IndexKeyIR{{Column: &fieldID, Direction: compilerir.SortAsc, Nulls: compilerir.NullsDefault}}}}
		model.Models[1].EqualityIndexes = []compilerir.EqualityIndexIR{{FieldID: authorID, Kind: compilerir.EqualityViaIndex, IndexID: &indexID}}
	}
	contract := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{
		{ModelID: user, Fields: []compilerir.FieldContractIR{{FieldID: userID, Modes: modes.UserID}, {FieldID: userName, Modes: modes.UserName}, {FieldID: userPosts, Modes: modes.UserPosts}}, Limits: compilerir.LimitContractIR{MaxTake: userMaxTake}},
		{ModelID: post, Fields: []compilerir.FieldContractIR{{FieldID: postID, Modes: modes.PostID}, {FieldID: authorID, Modes: modes.AuthorID}, {FieldID: postTitle, Modes: modes.PostTitle}, {FieldID: postAuthor, Modes: modes.PostAuthor}}, Limits: compilerir.LimitContractIR{MaxTake: postMaxTake}, Subscriptions: postSubscriptions},
	}}
	contract.Models[1].ScopedReads = scopedReads
	contract.Models[0].ScopedReads = scopedReads
	if exactValues {
		contract.Models[1].Fields = append(contract.Models[1].Fields, compilerir.FieldContractIR{FieldID: postBigInt}, compilerir.FieldContractIR{FieldID: postDecimal})
	}
	if optimisticConcurrency {
		field := postBigInt
		contract.Models[1].OptimisticConcurrency = &field
		contract.Models[1].Fields = append(contract.Models[1].Fields, compilerir.FieldContractIR{FieldID: postDateTime})
		for index := range contract.Models[1].Fields {
			if contract.Models[1].Fields[index].FieldID == field {
				contract.Models[1].Fields[index].Modes = []compilerir.FieldMode{compilerir.ModeVisible}
			}
		}
	}
	if mutationVocabulary {
		contract.Models[1].Fields = append(contract.Models[1].Fields, compilerir.FieldContractIR{FieldID: postOptionalInt})
	}
	if fullExactValues {
		contract.Models[1].Fields = append(contract.Models[1].Fields,
			compilerir.FieldContractIR{FieldID: postNullableText}, compilerir.FieldContractIR{FieldID: postBytes},
			compilerir.FieldContractIR{FieldID: postJSON}, compilerir.FieldContractIR{FieldID: postList}, compilerir.FieldContractIR{FieldID: postDateTime})
	}
	normalizeSubscribedEvents(t, model, &contract)

	modelDocument := document(t, uint32(compilerir.ModelFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalModel(model)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ModelFingerprint(model)
		return payload, fingerprint, err
	})
	contractDocument := document(t, uint32(compilerir.ContractFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalContract(contract)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ContractFingerprint(contract)
		return payload, fingerprint, err
	})
	sqliteSchema := physicalSchema(compilerir.SQLite, user, post, userID, userName, postID, authorID, postTitle, postBigInt, postDecimal, postOptionalInt, postNullableText, postBytes, postJSON, postList, postDateTime, userKey, postKey, indexedAuthor, exactValues, mutationVocabulary, fullExactValues)
	postgresSchema := physicalSchema(compilerir.PostgreSQL, user, post, userID, userName, postID, authorID, postTitle, postBigInt, postDecimal, postOptionalInt, postNullableText, postBytes, postJSON, postList, postDateTime, userKey, postKey, indexedAuthor, exactValues, mutationVocabulary, fullExactValues)
	if optimisticConcurrency {
		field := postBigInt
		sqliteSchema.Tables[1].OptimisticConcurrency = &field
		postgresSchema.Tables[1].OptimisticConcurrency = &field
		logical := compilerir.LogicalTypeIR{Kind: compilerir.TypeDateTime, Precision: func() *uint16 { value := uint16(6); return &value }()}
		for _, target := range []*physical.PhysicalSchema{&sqliteSchema, &postgresSchema} {
			storage, storageErr := physical.ExpectedStorage(target.Provider.Provider, logical)
			if storageErr != nil {
				t.Fatal(storageErr)
			}
			target.Tables[1].Columns = append(target.Tables[1].Columns, physical.PhysicalColumn{ID: postDateTime, Name: "datetime_value", Ordinal: uint32(len(target.Tables[1].Columns)), Storage: storage, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
		}
	}
	if nullableAuthor {
		sqliteSchema.Tables[1].Columns[1].Nullable = true
		postgresSchema.Tables[1].Columns[1].Nullable = true
	}
	if nullableAuthor || inverseHasOne {
		foreign := physical.PhysicalForeignKey{ID: compilerir.ForeignKeyID(id(44)), Name: "fk_posts_author", Columns: []compilerir.FieldID{authorID}, ReferencedTable: user, ReferencedColumns: []compilerir.FieldID{userID}, OnUpdate: compilerir.ActionRestrict, OnDelete: compilerir.ActionRestrict, Deferrable: compilerir.NotDeferrable}
		sqliteSchema.Tables[1].ForeignKeys = []physical.PhysicalForeignKey{foreign}
		postgresSchema.Tables[1].ForeignKeys = []physical.PhysicalForeignKey{foreign}
	}
	// The complete exact-value fixture includes provider-owned JSON/list checks
	// and temporal/storage details. Lower it through the real providers so live
	// post-apply introspection must reproduce precisely the same physical IR.
	if fullExactValues {
		var err error
		sqliteSchema, err = sqliteprovider.New().Lower(context.Background(), model, physical.LowerOptions{})
		if err != nil {
			t.Fatal(err)
		}
		postgresSchema, err = postgresprovider.New().Lower(context.Background(), model, physical.LowerOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	postgresSchema.Namespace.Name = postgresNamespace
	postgresSchema.System.Namespace.Name = postgresSystemNamespace
	providers := []golem.ProviderSchemaDocument{providerDocument(t, golem.SQLite, sqliteSchema), providerDocument(t, golem.PostgreSQL, postgresSchema)}
	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{1}, "schematest", "p3", modelDocument, contractDocument, providers...)
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatalf("bootstrap schema fixture: %v", err)
	}
	fixture := Fixture{
		Bundle: bundle, Registry: registry, SQLite: sqliteSchema, PostgreSQL: postgresSchema,
		User: golem.ModelID(mustFixed(t, string(user))), Post: golem.ModelID(mustFixed(t, string(post))),
		UserID: golem.FieldID(mustFixed(t, string(userID))), UserName: golem.FieldID(mustFixed(t, string(userName))), UserPosts: golem.FieldID(mustFixed(t, string(userPosts))),
		PostID: golem.FieldID(mustFixed(t, string(postID))), AuthorID: golem.FieldID(mustFixed(t, string(authorID))), PostTitle: golem.FieldID(mustFixed(t, string(postTitle))), PostAuthor: golem.FieldID(mustFixed(t, string(postAuthor))),
		Authorship: golem.RelationID(mustFixed(t, string(authorship))),
		UserKey:    golem.KeyID(mustFixed(t, string(userKey))), PostKey: golem.KeyID(mustFixed(t, string(postKey))),
	}
	if exactValues {
		fixture.PostBigInt = golem.FieldID(mustFixed(t, string(postBigInt)))
		fixture.PostDecimal = golem.FieldID(mustFixed(t, string(postDecimal)))
	}
	if mutationVocabulary {
		fixture.PostOptionalInt = golem.FieldID(mustFixed(t, string(postOptionalInt)))
	}
	if optimisticConcurrency {
		fixture.PostDateTime = golem.FieldID(mustFixed(t, string(postDateTime)))
	}
	if fullExactValues {
		fixture.PostNullableText = golem.FieldID(mustFixed(t, string(postNullableText)))
		fixture.PostBytes = golem.FieldID(mustFixed(t, string(postBytes)))
		fixture.PostJSON = golem.FieldID(mustFixed(t, string(postJSON)))
		fixture.PostList = golem.FieldID(mustFixed(t, string(postList)))
		fixture.PostDateTime = golem.FieldID(mustFixed(t, string(postDateTime)))
	}
	return fixture
}

func scalar(field compilerir.FieldID, name, column string, kind compilerir.LogicalTypeKind, nullable bool) compilerir.FieldIR {
	return compilerir.FieldIR{ID: field, GoName: name, LogicalName: name, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: compilerir.SQLIdentifier(column), Type: compilerir.LogicalTypeIR{Kind: kind}, Nullable: nullable}}
}

func relation(field compilerir.FieldID, name string, relationID compilerir.RelationID, role compilerir.RelationEndpointRole, kind compilerir.RelationKind) compilerir.FieldIR {
	return compilerir.FieldIR{ID: field, GoName: name, LogicalName: name, Kind: compilerir.FieldRelation, Relation: &compilerir.RelationFieldIR{RelationID: relationID, Role: role, Kind: kind}}
}

func physicalSchema(provider compilerir.Provider, user, post compilerir.ModelID, userID, userName, postID, authorID, postTitle, postBigInt, postDecimal, postOptionalInt, postNullableText, postBytes, postJSON, postList, postDateTime compilerir.FieldID, userKey, postKey compilerir.KeyID, indexedAuthor, exactValues, mutationVocabulary, fullExactValues bool) physical.PhysicalSchema {
	manifest, namespace, uuid := postgresprovider.New().Manifest(), physical.PhysicalName("public"), physical.StoragePostgreSQLUUID
	if provider == compilerir.SQLite {
		manifest, namespace, uuid = sqliteprovider.New().Manifest(), "main", physical.StorageSQLiteText
	}
	stringStorage := physical.StorageType{Kind: physical.StoragePostgreSQLText}
	if provider == compilerir.SQLite {
		stringStorage.Kind = physical.StorageSQLiteText
	}
	systemNamespace := namespace
	if provider == compilerir.PostgreSQL {
		systemNamespace = "_golem"
	}
	result := physical.PhysicalSchema{Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: manifest, Namespace: physical.Namespace{Name: namespace}, System: physical.SystemSchema{Version: 1, Namespace: physical.Namespace{Name: systemNamespace}, Objects: []physical.SystemObject{
		{ID: physical.MigrationLedgerObjectIDV1, Kind: physical.SystemMigrationLedger, Version: 1, Name: "_golem_migrations"},
		{ID: physical.MigrationLockObjectIDV1, Kind: physical.SystemMigrationLock, Version: 1, Name: "_golem_migration_lock"},
		physical.OutboxSystemObjectV1(),
		physical.OutboxDeliverySystemObjectV1(),
		physical.UpsertGuardSystemObjectV1(),
	}}, Tables: []physical.PhysicalTable{
		{ID: user, Name: "users", Columns: []physical.PhysicalColumn{{ID: userID, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: uuid}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}, {ID: userName, Name: "name", Ordinal: 1, Storage: stringStorage, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}, PrimaryKey: &physical.PhysicalKey{ID: userKey, Name: "pk_users", Columns: []compilerir.FieldID{userID}}},
		{ID: post, Name: "posts", Columns: []physical.PhysicalColumn{{ID: postID, Name: "id", Ordinal: 0, Storage: physical.StorageType{Kind: uuid}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}, {ID: authorID, Name: "author_id", Ordinal: 1, Storage: physical.StorageType{Kind: uuid}, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}, {ID: postTitle, Name: "title", Ordinal: 2, Storage: stringStorage, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}}, PrimaryKey: &physical.PhysicalKey{ID: postKey, Name: "pk_posts", Columns: []compilerir.FieldID{postID}}},
	}}
	if exactValues {
		bigIntStorage := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
		decimalStorage := physical.StorageType{Kind: physical.StoragePostgreSQLNumeric, Precision: 18, Scale: 13}
		if provider == compilerir.SQLite {
			bigIntStorage = physical.StorageType{Kind: physical.StorageSQLiteInteger}
			decimalStorage = physical.StorageType{Kind: physical.StorageSQLiteInteger}
		}
		result.Tables[1].Columns = append(result.Tables[1].Columns,
			physical.PhysicalColumn{ID: postBigInt, Name: "big_int", Ordinal: 3, Storage: bigIntStorage, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
			physical.PhysicalColumn{ID: postDecimal, Name: "decimal_value", Ordinal: 4, Storage: decimalStorage, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}},
		)
	}
	if mutationVocabulary {
		optionalStorage := physical.StorageType{Kind: physical.StoragePostgreSQLBigInt}
		if provider == compilerir.SQLite {
			optionalStorage = physical.StorageType{Kind: physical.StorageSQLiteInteger}
		}
		result.Tables[1].Columns = append(result.Tables[1].Columns, physical.PhysicalColumn{ID: postOptionalInt, Name: "optional_int", Ordinal: uint32(len(result.Tables[1].Columns)), Storage: optionalStorage, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
	}
	if fullExactValues {
		logicalTypes := []compilerir.LogicalTypeIR{
			{Kind: compilerir.TypeString}, {Kind: compilerir.TypeBytes}, {Kind: compilerir.TypeJSON},
			{Kind: compilerir.TypeScalarList}, {Kind: compilerir.TypeDateTime},
		}
		ids := []compilerir.FieldID{postNullableText, postBytes, postJSON, postList, postDateTime}
		names := []physical.PhysicalName{"nullable_text", "bytes_value", "json_value", "list_value", "datetime_value"}
		for index, logical := range logicalTypes {
			storage, _ := physical.ExpectedStorage(provider, logical)
			result.Tables[1].Columns = append(result.Tables[1].Columns, physical.PhysicalColumn{ID: ids[index], Name: names[index], Ordinal: uint32(len(result.Tables[1].Columns)), Storage: storage, Nullable: true, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}})
		}
	}
	if indexedAuthor {
		column := authorID
		result.Tables[1].Indexes = []physical.PhysicalIndex{{ID: compilerir.IndexID(id(43)), Name: "idx_posts_author", Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &column, Direction: compilerir.SortAsc, Nulls: compilerir.NullsDefault}}, CreationMode: physical.IndexTransactional}}
	}
	return result
}

func providerDocument(t testing.TB, provider golem.Provider, value physical.PhysicalSchema) golem.ProviderSchemaDocument {
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

func document(t testing.TB, version uint32, build func() ([]byte, compilerir.Fingerprint, error)) golem.SchemaDocument {
	t.Helper()
	payload, fingerprint, err := build()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(decoded) != 32 {
		t.Fatalf("fingerprint: %v", err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], decoded)
	return golem.GeneratedSchemaDocument(version, uint32(compilerir.CanonicalFormatVersion), digest, payload)
}

func mustFixed(t testing.TB, value string) [16]byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 16 {
		t.Fatalf("fixed ID %q: %v", value, err)
	}
	var result [16]byte
	copy(result[:], decoded)
	return result
}

func id(value int) string { return fmt.Sprintf("%032x", value) }
