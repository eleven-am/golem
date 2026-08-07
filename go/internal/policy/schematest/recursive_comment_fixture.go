package schematest

import (
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// RecursiveCommentFixture is the smallest two-provider, subscribed self-
// relation schema. It exercises real recursive nested writes rather than using
// a chain of distinct model types as a proxy for recursion.
type RecursiveCommentFixture struct {
	Bundle     golem.SchemaBundle
	Registry   *schema.Registry
	SQLite     physical.PhysicalSchema
	PostgreSQL physical.PhysicalSchema

	Comment                   golem.ModelID
	CommentID, ParentID, Body golem.FieldID
	Parent, Replies           golem.FieldID
	Threading                 golem.RelationID
	CommentKey                golem.KeyID
}

func NewSubscribedRecursiveComment(t testing.TB) RecursiveCommentFixture {
	return newSubscribedRecursiveComment(t, "public", "_golem")
}

func NewSubscribedRecursiveCommentPostgreSQLNamespaces(t testing.TB, namespace, systemNamespace physical.PhysicalName) RecursiveCommentFixture {
	return newSubscribedRecursiveComment(t, namespace, systemNamespace)
}

func newSubscribedRecursiveComment(t testing.TB, postgresNamespace, postgresSystemNamespace physical.PhysicalName) RecursiveCommentFixture {
	t.Helper()
	comment := compilerir.ModelID(id(211))
	commentID, parentID, body := compilerir.FieldID(id(212)), compilerir.FieldID(id(213)), compilerir.FieldID(id(214))
	parent, replies := compilerir.FieldID(id(215)), compilerir.FieldID(id(216))
	threading, commentKey := compilerir.RelationID(id(217)), compilerir.KeyID(id(218))
	model := compilerir.ModelIR{
		FormatVersion: compilerir.ModelFormatVersion,
		Providers:     []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL},
		Models: []compilerir.ModelDeclIR{{
			ID: comment, LogicalName: "Comment", Table: compilerir.TableBindingIR{PhysicalName: "comments"},
			Fields: []compilerir.FieldIR{
				scalar(commentID, "ID", "id", compilerir.TypeUUID, false),
				scalar(parentID, "ParentID", "parent_id", compilerir.TypeUUID, true),
				scalar(body, "Body", "body", compilerir.TypeString, false),
				relation(parent, "Parent", threading, compilerir.RelationSource, compilerir.RelationBelongsTo),
				relation(replies, "Replies", threading, compilerir.RelationInverse, compilerir.RelationHasMany),
			},
			PrimaryKey: &compilerir.KeyIR{ID: commentKey, Kind: compilerir.KeyPrimary, PhysicalName: "pk_comments", Fields: []compilerir.FieldID{commentID}},
		}},
		Relations: []compilerir.RelationIR{{
			ID: threading, SourceModel: comment, TargetModel: comment, SourceField: parent, InverseField: &replies,
			Cardinality: compilerir.RelationMany, LocalFields: []compilerir.FieldID{parentID}, RemoteFields: []compilerir.FieldID{commentID},
		}},
	}
	contract := compilerir.ContractIR{FormatVersion: compilerir.ContractFormatVersion, Models: []compilerir.ModelContractIR{{
		ModelID: comment, Fields: fieldContracts(commentID, parentID, body, parent, replies), Subscriptions: true,
	}}}
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
	sqliteSchema := recursiveCommentPhysical(compilerir.SQLite, "main", "main", comment, commentID, parentID, body, commentKey)
	postgresSchema := recursiveCommentPhysical(compilerir.PostgreSQL, postgresNamespace, postgresSystemNamespace, comment, commentID, parentID, body, commentKey)
	bundle := golem.GeneratedSchemaBundle(golem.SchemaDigest{4}, "schematest", "p4-recursive-comment", modelDocument, contractDocument,
		providerDocument(t, golem.SQLite, sqliteSchema), providerDocument(t, golem.PostgreSQL, postgresSchema))
	registry, err := schema.New(bundle)
	if err != nil {
		t.Fatalf("bootstrap recursive comment schema: %v", err)
	}
	return RecursiveCommentFixture{
		Bundle: bundle, Registry: registry, SQLite: sqliteSchema, PostgreSQL: postgresSchema,
		Comment:   golem.ModelID(mustFixed(t, string(comment))),
		CommentID: golem.FieldID(mustFixed(t, string(commentID))), ParentID: golem.FieldID(mustFixed(t, string(parentID))), Body: golem.FieldID(mustFixed(t, string(body))),
		Parent: golem.FieldID(mustFixed(t, string(parent))), Replies: golem.FieldID(mustFixed(t, string(replies))),
		Threading: golem.RelationID(mustFixed(t, string(threading))), CommentKey: golem.KeyID(mustFixed(t, string(commentKey))),
	}
}

func recursiveCommentPhysical(provider compilerir.Provider, namespace, systemNamespace physical.PhysicalName, comment compilerir.ModelID, commentID, parentID, body compilerir.FieldID, commentKey compilerir.KeyID) physical.PhysicalSchema {
	manifest, uuid, text := postgresprovider.New().Manifest(), physical.StoragePostgreSQLUUID, physical.StoragePostgreSQLText
	if provider == compilerir.SQLite {
		manifest, uuid, text = sqliteprovider.New().Manifest(), physical.StorageSQLiteText, physical.StorageSQLiteText
	}
	column := func(field compilerir.FieldID, name string, ordinal uint32, storage physical.StorageKind, nullable bool) physical.PhysicalColumn {
		return physical.PhysicalColumn{ID: field, Name: physical.PhysicalName(name), Ordinal: ordinal, Storage: physical.StorageType{Kind: storage}, Nullable: nullable, Default: physical.PhysicalDefault{Kind: physical.DefaultNone}}
	}
	return physical.PhysicalSchema{
		Version: physical.SchemaFormatVersion, CanonicalVersion: physical.CanonicalFormatVersion, Provider: manifest,
		Namespace: physical.Namespace{Name: namespace},
		Tables: []physical.PhysicalTable{{
			ID: comment, Name: "comments", Columns: []physical.PhysicalColumn{
				column(commentID, "id", 0, uuid, false), column(parentID, "parent_id", 1, uuid, true), column(body, "body", 2, text, false),
			},
			PrimaryKey: &physical.PhysicalKey{ID: commentKey, Name: "pk_comments", Columns: []compilerir.FieldID{commentID}},
		}},
		System: graphSystemSchema(systemNamespace),
	}
}
