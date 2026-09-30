package schematest

import (
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

// NewBytesKeyed is the indexed fixture keyed by Bytes. User's primary key is
// (ID Bytes, Name), Post's is (ID Bytes, Title), and Post.Author correlates
// (AuthorID Bytes, AuthorName) with (User.ID, User.Name), so an empty Bytes key
// component is always followed by another key column.
func NewBytesKeyed(t testing.TB) Fixture {
	t.Helper()
	return bytesKeyed(t, NewIndexed(t))
}

// NewSubscribedBytesKeyed is NewBytesKeyed with durable Post change facts, so
// subscribers receive events whose identity carries the Bytes key.
func NewSubscribedBytesKeyed(t testing.TB) Fixture {
	t.Helper()
	return bytesKeyed(t, NewSubscribedIndexed(t))
}

func bytesKeyed(t testing.TB, fixture Fixture) Fixture {
	t.Helper()
	modelDocument := fixture.Bundle.Model()
	var model compilerir.ModelIR
	if err := json.Unmarshal(modelDocument.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	field := func(value golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(value[:])) }
	userID, userName, postID, authorID, postTitle := field(fixture.UserID), field(fixture.UserName), field(fixture.PostID), field(fixture.AuthorID), field(fixture.PostTitle)
	authorName := compilerir.FieldID(id(34))
	for modelIndex := range model.Models {
		declaration := &model.Models[modelIndex]
		for fieldIndex := range declaration.Fields {
			switch declaration.Fields[fieldIndex].ID {
			case userID, postID, authorID:
				declaration.Fields[fieldIndex].Scalar.Type = compilerir.LogicalTypeIR{Kind: compilerir.TypeBytes}
			}
		}
		switch declaration.ID {
		case compilerir.ModelID(hex.EncodeToString(fixture.User[:])):
			declaration.PrimaryKey.Fields = []compilerir.FieldID{userID, userName}
		case compilerir.ModelID(hex.EncodeToString(fixture.Post[:])):
			declaration.PrimaryKey.Fields = []compilerir.FieldID{postID, postTitle}
			declaration.Fields = append(declaration.Fields, scalar(authorName, "AuthorName", "author_name", compilerir.TypeString, false))
		}
	}
	for index := range model.Relations {
		model.Relations[index].LocalFields = []compilerir.FieldID{authorID, authorName}
		model.Relations[index].RemoteFields = []compilerir.FieldID{userID, userName}
	}
	var contract compilerir.ContractIR
	if err := json.Unmarshal(fixture.Bundle.Contract().Bytes(), &contract); err != nil {
		t.Fatal(err)
	}
	for index := range contract.Models {
		if contract.Models[index].ModelID == compilerir.ModelID(hex.EncodeToString(fixture.Post[:])) {
			contract.Models[index].Fields = append(contract.Models[index].Fields, compilerir.FieldContractIR{FieldID: authorName})
		}
	}
	normalizeSubscribedEvents(t, model, &contract)
	contractDocument := document(t, uint32(compilerir.ContractFormatVersion), func() ([]byte, compilerir.Fingerprint, error) {
		payload, err := compilerir.CanonicalContract(contract)
		if err != nil {
			return nil, "", err
		}
		fingerprint, err := compilerir.ContractFingerprint(contract)
		return payload, fingerprint, err
	})
	fixture.SQLite = lowerSemantic(t, sqliteprovider.New(), model, fixture.SQLite)
	fixture.PostgreSQL = lowerSemantic(t, postgresprovider.New(), model, fixture.PostgreSQL)
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		canonicalModelDocument(t, modelDocument, model), contractDocument,
		providerDocument(t, golem.SQLite, fixture.SQLite), providerDocument(t, golem.PostgreSQL, fixture.PostgreSQL),
	)
	registry, err := schema.New(fixture.Bundle)
	if err != nil {
		t.Fatalf("bootstrap bytes-keyed schema fixture: %v", err)
	}
	fixture.Registry = registry
	fixture.PostAuthorName = golem.FieldID(mustFixed(t, string(authorName)))
	return fixture
}
