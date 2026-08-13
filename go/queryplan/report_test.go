package queryplan_test

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	internalreport "github.com/eleven-am/golem/go/internal/queryplanreport"
	"github.com/eleven-am/golem/go/queryplan"
)

func TestPublicReportAccessorsAreExactImmutableAndBatchTruthful(t *testing.T) {
	model := golem.ModelID{15: 1}
	target := golem.ModelID{15: 2}
	relation := golem.RelationID{15: 3}
	field := golem.FieldID{15: 4}
	index := internalreport.IndexID{15: 5}
	input := internalreport.Input{
		Provider: golem.SQLite, Operation: string(queryplan.OperationFindMany), RootModelID: model,
		MinimumExecutionStatements: 1, MaximumExecutionStatements: 3,
		Statements: []internalreport.StatementInput{{Ordinal: 0, Purpose: string(queryplan.StatementPurposeRoot), Root: internalreport.NodeInput{
			Kind: string(queryplan.NodeKindSort), Access: string(queryplan.AccessKindNone), FieldIDs: []golem.FieldID{field},
			Children: []internalreport.NodeInput{
				{Kind: string(queryplan.NodeKindAccess), Access: string(queryplan.AccessKindPrimaryKey), ModelID: model, HasModelID: true, IndexID: index, HasIndexID: true},
				{Kind: string(queryplan.NodeKindDeferredBatch), Access: string(queryplan.AccessKindNone), ModelID: target, HasModelID: true, RelationID: relation, HasRelationID: true, HasBatch: true, BatchCapacity: 100, BatchMinimum: 0, BatchMaximum: 2},
			},
		}}},
	}
	built, err := internalreport.Build(input)
	if err != nil {
		t.Fatalf("build report: %v", err)
	}
	report := queryplan.Report(built)
	if report.FormatVersion() != 1 || report.Provider() != golem.SQLite || report.Operation() != queryplan.OperationFindMany || report.RootModelID() != model {
		t.Fatalf("header=%d/%q/%q/%x", report.FormatVersion(), report.Provider(), report.Operation(), report.RootModelID())
	}
	if report.MinimumExecutionStatements() != 1 || report.MaximumExecutionStatements() != 3 {
		t.Fatalf("bounds=%d..%d", report.MinimumExecutionStatements(), report.MaximumExecutionStatements())
	}
	if got, want := report.Warnings(), []queryplan.Warning{queryplan.WarningTemporarySort, queryplan.WarningDeferredBatch, queryplan.WarningMultiStatement}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings=%v want=%v", got, want)
	}

	statement := report.Statements()[0]
	if statement.Ordinal() != 0 || statement.Purpose() != queryplan.StatementPurposeRoot {
		t.Fatalf("statement=%d/%q", statement.Ordinal(), statement.Purpose())
	}
	root := statement.Root()
	if root.Kind() != queryplan.NodeKindSort || root.Access() != queryplan.AccessKindNone {
		t.Fatalf("root=%q/%q", root.Kind(), root.Access())
	}
	if _, ok := root.BatchCapacity(); ok {
		t.Fatal("non-deferred node exposed batch facts")
	}
	deferred := root.Children()[1]
	if capacity, ok := deferred.BatchCapacity(); !ok || capacity != 100 {
		t.Fatalf("capacity=(%d,%v)", capacity, ok)
	}
	if minimum, ok := deferred.MinimumExecutionStatements(); !ok || minimum != 0 {
		t.Fatalf("minimum=(%d,%v)", minimum, ok)
	}
	if maximum, ok := deferred.MaximumExecutionStatements(); !ok || maximum != 2 {
		t.Fatalf("maximum=(%d,%v)", maximum, ok)
	}
	if got := deferred.Warnings(); !reflect.DeepEqual(got, []queryplan.Warning{queryplan.WarningDeferredBatch}) {
		t.Fatalf("deferred warnings=%v", got)
	}

	input.Statements[0].Root.FieldIDs[0][15] = 99
	input.Statements[0].Root.Children[0].IndexID[15] = 99
	input.Statements[0].Root.Children[1].BatchCapacity = 1
	statements := report.Statements()
	statements[0] = queryplan.Statement{}
	fields := root.FieldIDs()
	fields[0][15] = 99
	children := root.Children()
	children[0] = queryplan.Node{}
	warnings := report.Warnings()
	warnings[0] = queryplan.WarningFullScan
	if root.FieldIDs()[0] != field {
		t.Fatal("repeated FieldIDs accessor observed caller mutation")
	}
	gotIndex, gotIndexOK := report.Statements()[0].Root().Children()[0].IndexID()
	if report.Statements()[0].Root().FieldIDs()[0] != field || !gotIndexOK || gotIndex != queryplan.IndexID(index) {
		t.Fatal("producer or public accessor exposed mutable storage")
	}
	if capacity, _ := report.Statements()[0].Root().Children()[1].BatchCapacity(); capacity != 100 {
		t.Fatal("producer input mutation changed report")
	}

	second, err := internalreport.Build(validDigestInput(model, target, relation, field, index, 2))
	if err != nil {
		t.Fatal(err)
	}
	third, err := internalreport.Build(validDigestInput(model, target, relation, field, index, 3))
	if err != nil {
		t.Fatal(err)
	}
	if queryplan.Report(second).CanonicalDigest() == queryplan.Report(third).CanonicalDigest() {
		t.Fatal("deferred maximum was omitted from canonical digest")
	}
	if queryplan.Report(second).CanonicalDigest() == ([32]byte{}) {
		t.Fatal("valid report has zero digest")
	}
}

func TestPublicZeroReportAndClosedErrors(t *testing.T) {
	zero := queryplan.Report{}
	if zero.FormatVersion() != 0 || zero.Provider() != "" || zero.Operation() != "" || zero.RootModelID() != (golem.ModelID{}) || len(zero.Statements()) != 0 || zero.MinimumExecutionStatements() != 0 || zero.MaximumExecutionStatements() != 0 || len(zero.Warnings()) != 0 || zero.CanonicalDigest() != ([32]byte{}) {
		t.Fatalf("unsafe zero report: %#v", zero)
	}
	_, err := internalreport.Build(internalreport.Input{})
	wrapped := fmt.Errorf("outer: %w", err)
	if code, ok := queryplan.CodeOf(wrapped); !ok || code != queryplan.ErrorInvalid {
		t.Fatalf("CodeOf=%q,%v err=%v", code, ok, err)
	}
	if queryplan.ErrorUnavailable != queryplan.ErrorCode("PLAN_UNAVAILABLE") || queryplan.ErrorTooComplex != queryplan.ErrorCode("PLAN_TOO_COMPLEX") {
		t.Fatal("error constants changed")
	}
	if _, ok := queryplan.CodeOf(errors.New("PLAN_INVALID: forged")); ok {
		t.Fatal("CodeOf classified public text")
	}
	if got := err.Error(); got != "query plan is invalid" {
		t.Fatalf("error text=%q", got)
	}
}

func validDigestInput(model, target golem.ModelID, relation golem.RelationID, field golem.FieldID, index internalreport.IndexID, maximum uint32) internalreport.Input {
	return internalreport.Input{
		Provider: golem.SQLite, Operation: string(queryplan.OperationFindMany), RootModelID: model,
		MinimumExecutionStatements: 1, MaximumExecutionStatements: 1 + maximum,
		Statements: []internalreport.StatementInput{{Ordinal: 0, Purpose: string(queryplan.StatementPurposeRoot), Root: internalreport.NodeInput{
			Kind: string(queryplan.NodeKindSort), Access: string(queryplan.AccessKindNone), FieldIDs: []golem.FieldID{field}, Children: []internalreport.NodeInput{
				{Kind: string(queryplan.NodeKindAccess), Access: string(queryplan.AccessKindPrimaryKey), ModelID: model, HasModelID: true, IndexID: index, HasIndexID: true},
				{Kind: string(queryplan.NodeKindDeferredBatch), Access: string(queryplan.AccessKindNone), ModelID: target, HasModelID: true, RelationID: relation, HasRelationID: true, HasBatch: true, BatchCapacity: 100, BatchMaximum: maximum},
			},
		}}},
	}
}
