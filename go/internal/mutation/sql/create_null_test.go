package sql

import (
	"strings"
	"testing"

	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func TestCreateExplicitNullRendersSQLNullWithoutAPlaceholder(t *testing.T) {
	fixture := schematest.NewMutationVocabulary(t)
	id, author := uuidValue(1), uuidValue(2)
	title, _ := policyir.StringValue("nullable-create")
	idOperation, _ := mutationir.NewSet(policyir.FieldID(fixture.PostID), testType(t, fixture, fixture.Post, fixture.PostID, policyir.ProviderSQLite), id)
	authorOperation, _ := mutationir.NewSet(policyir.FieldID(fixture.AuthorID), testType(t, fixture, fixture.Post, fixture.AuthorID, policyir.ProviderSQLite), author)
	titleOperation, _ := mutationir.NewSet(policyir.FieldID(fixture.PostTitle), testType(t, fixture, fixture.Post, fixture.PostTitle, policyir.ProviderSQLite), title)
	nullOperation, err := mutationir.NewNull(policyir.FieldID(fixture.PostOptionalInt), testType(t, fixture, fixture.Post, fixture.PostOptionalInt, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	program, err := Render(createPlan(t, fixture, []mutationir.ScalarOperation{idOperation, authorOperation, titleOperation, nullOperation}), fixture.Registry, policyir.ProviderSQLite, testProof(t, fixture, policyir.ProviderSQLite))
	if err != nil {
		t.Fatal(err)
	}
	statement := program.Statements()[0]
	if !strings.Contains(statement.SQL(), "NULL") || len(statement.Bindings()) != 3 {
		t.Fatalf("explicit-null create SQL=%q bindings=%d", statement.SQL(), len(statement.Bindings()))
	}
}
