package queryplanreport

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
)

func TestNewErrorConstructsOnlyClosedSanitizedFailures(t *testing.T) {
	for _, code := range []Code{CodeUnavailable, CodeTooComplex, CodeInvalid} {
		wrapped := fmt.Errorf("outer: %w", NewError(code))
		if got, ok := CodeOf(wrapped); !ok || got != code {
			t.Fatalf("CodeOf(NewError(%q))=(%q,%t)", code, got, ok)
		}
		if errors.Unwrap(NewError(code)) != nil {
			t.Fatalf("NewError(%q) retained a cause", code)
		}
	}
	invalid := NewError(Code("RAW_PROVIDER_DIAGNOSTIC"))
	if got, ok := CodeOf(invalid); !ok || got != CodeInvalid {
		t.Fatalf("unknown code canonicalized to (%q,%t)", got, ok)
	}
	if invalid.Error() != "query plan is invalid" {
		t.Fatalf("unknown code leaked through message %q", invalid.Error())
	}
}

func TestBuildRejectsEveryNonCanonicalReportShape(t *testing.T) {
	tests := map[string]func(*Input){
		"provider":                               func(value *Input) { value.Provider = "mysql" },
		"operation":                              func(value *Input) { value.Operation = "delete" },
		"zero root model":                        func(value *Input) { value.RootModelID = golem.ModelID{} },
		"empty statements":                       func(value *Input) { value.Statements = nil },
		"ordinal gap":                            func(value *Input) { value.Statements[0].Ordinal = 1 },
		"ordinal sentinel":                       func(value *Input) { value.Statements[0].Ordinal = math.MaxUint32 },
		"find first is not root":                 func(value *Input) { value.Statements[0].Purpose = purposeAnalytics },
		"unknown purpose":                        func(value *Input) { value.Statements[0].Purpose = "rawSQL" },
		"relation purpose without deferred root": func(value *Input) { value.Statements[0].Purpose = purposeRelationBatch },
		"inverted bounds":                        func(value *Input) { value.MinimumExecutionStatements = 2 },
		"untruthful bounds":                      func(value *Input) { value.MaximumExecutionStatements = 2 },
		"unknown kind":                           func(value *Input) { value.Statements[0].Root.Kind = "providerScan" },
		"kind access contradiction":              func(value *Input) { value.Statements[0].Root.Access = accessConstant },
		"missing model flag":                     func(value *Input) { value.Statements[0].Root.HasModelID = false },
		"zero model behind flag":                 func(value *Input) { value.Statements[0].Root.ModelID = golem.ModelID{} },
		"missing index": func(value *Input) {
			value.Statements[0].Root.HasIndexID = false
			value.Statements[0].Root.IndexID = IndexID{}
		},
		"zero index behind flag": func(value *Input) { value.Statements[0].Root.IndexID = IndexID{} },
		"relation value without flag": func(value *Input) {
			value.Statements[0].Root.RelationID = golem.RelationID{15: 7}
		},
		"zero relation behind flag": func(value *Input) { value.Statements[0].Root.HasRelationID = true },
		"zero field":                func(value *Input) { value.Statements[0].Root.FieldIDs = []golem.FieldID{{}} },
		"duplicate field": func(value *Input) {
			field := golem.FieldID{15: 8}
			value.Statements[0].Root.FieldIDs = []golem.FieldID{field, field}
		},
		"batch facts on access": func(value *Input) {
			value.Statements[0].Root.HasBatch = true
			value.Statements[0].Root.BatchCapacity = 1
		},
		"hidden batch facts on access": func(value *Input) { value.Statements[0].Root.BatchCapacity = 1 },
		"index on full scan":           func(value *Input) { value.Statements[0].Root.Access = accessFullScan },
		"unknown access":               func(value *Input) { value.Statements[0].Root.Access = "heap" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := validInput()
			mutate(&input)
			report, err := Build(input)
			assertCode(t, err, CodeInvalid)
			if !reflect.DeepEqual(report, Report{}) {
				t.Fatal("failure returned a partial report")
			}
		})
	}

	tooMany := validInput()
	tooMany.Statements = make([]StatementInput, maxStatements+1)
	assertBuildCode(t, tooMany, CodeTooComplex)
	unbounded := validInput()
	unbounded.MaximumExecutionStatements = math.MaxUint32
	assertBuildCode(t, unbounded, CodeUnavailable)
	duplicateRoot := validInput()
	duplicateRoot.Statements = append(duplicateRoot.Statements, StatementInput{Ordinal: 1, Purpose: purposeRoot, Root: NodeInput{Kind: kindConstant, Access: accessConstant}})
	duplicateRoot.MaximumExecutionStatements = 2
	assertBuildCode(t, duplicateRoot, CodeInvalid)
}

func TestBuildRequiresTheExactPrimaryPurposeForEachOperation(t *testing.T) {
	tests := []struct {
		operation string
		purpose   string
	}{
		{operationFindUnique, purposeRoot},
		{operationFindFirst, purposeRoot},
		{operationFindMany, purposeRoot},
		{operationCount, purposeRoot},
		{operationAggregate, purposeAnalytics},
		{operationGroupBy, purposeAnalytics},
		{operationRelationGroupBy, purposeAnalytics},
		{operationScoped, purposeScoped},
	}
	for _, test := range tests {
		t.Run(test.operation, func(t *testing.T) {
			input := validInput()
			input.Operation = test.operation
			input.Statements[0].Purpose = test.purpose
			if _, err := Build(input); err != nil {
				t.Fatalf("exact primary purpose rejected: %v", err)
			}
			for _, wrong := range []string{purposeRoot, purposeAnalytics, purposeScoped, purposePolicyHydration, purposeRelationBatch} {
				if wrong == test.purpose {
					continue
				}
				mutant := input
				mutant.Statements = append([]StatementInput(nil), input.Statements...)
				mutant.Statements[0].Purpose = wrong
				assertBuildCode(t, mutant, CodeInvalid)
			}
		})
	}

	input := validInput()
	input.Statements = append(input.Statements, StatementInput{Ordinal: 1, Purpose: purposeAnalytics, Root: NodeInput{Kind: kindConstant, Access: accessConstant}})
	input.MinimumExecutionStatements, input.MaximumExecutionStatements = 2, 2
	assertBuildCode(t, input, CodeInvalid)
}

func TestBuildEnforcesClosedKindAccessMatrix(t *testing.T) {
	kinds := []string{kindAccess, kindJoin, kindSort, kindAggregate, kindMaterialize, kindCorrelatedRelation, kindDeferredBatch, kindConstant, kindUnknown}
	accesses := []string{accessNone, accessPrimaryKey, accessUniqueIndex, accessIndex, accessBitmapIndex, accessFullScan, accessConstant, accessUnknown}
	want := map[[2]string]bool{
		{kindAccess, accessPrimaryKey}: true, {kindAccess, accessUniqueIndex}: true, {kindAccess, accessIndex}: true,
		{kindAccess, accessBitmapIndex}: true, {kindAccess, accessFullScan}: true, {kindAccess, accessUnknown}: true,
		{kindJoin, accessNone}: true, {kindSort, accessNone}: true, {kindAggregate, accessNone}: true,
		{kindMaterialize, accessNone}: true, {kindCorrelatedRelation, accessNone}: true, {kindDeferredBatch, accessNone}: true,
		{kindConstant, accessConstant}: true, {kindUnknown, accessUnknown}: true,
	}
	for _, kind := range kinds {
		for _, access := range accesses {
			if got := validNodeAccess(kind, access); got != want[[2]string{kind, access}] {
				t.Fatalf("validNodeAccess(%q,%q)=%v want=%v", kind, access, got, want[[2]string{kind, access}])
			}
		}
	}
}

func TestBuildDeferredBatchFactsBoundsAndNoProviderClaim(t *testing.T) {
	base := validInput()
	deferred := validDeferredNode()
	base.Statements[0].Root = NodeInput{Kind: kindJoin, Access: accessNone, Children: []NodeInput{base.Statements[0].Root, deferred}}
	base.MinimumExecutionStatements, base.MaximumExecutionStatements = 1, 3
	report, err := Build(base)
	if err != nil {
		t.Fatal(err)
	}
	batch := report.Statements()[0].Root().Children()[1]
	if capacity, ok := batch.BatchCapacity(); !ok || capacity != 64 {
		t.Fatalf("capacity=(%d,%v)", capacity, ok)
	}
	if minimum, ok := batch.MinimumExecutionStatements(); !ok || minimum != 0 {
		t.Fatalf("minimum=(%d,%v)", minimum, ok)
	}
	if maximum, ok := batch.MaximumExecutionStatements(); !ok || maximum != 2 {
		t.Fatalf("maximum=(%d,%v)", maximum, ok)
	}
	if batch.Access() != accessNone || len(batch.Children()) != 0 {
		t.Fatal("deferred batch claimed a provider access path")
	}

	tests := map[string]func(*NodeInput){
		"no facts":         func(value *NodeInput) { value.HasBatch = false; value.BatchCapacity = 0; value.BatchMaximum = 0 },
		"zero capacity":    func(value *NodeInput) { value.BatchCapacity = 0 },
		"inverted":         func(value *NodeInput) { value.BatchMinimum = 3 },
		"unbounded":        func(value *NodeInput) { value.BatchMaximum = math.MaxUint32 },
		"missing model":    func(value *NodeInput) { value.ModelID = golem.ModelID{}; value.HasModelID = false },
		"missing relation": func(value *NodeInput) { value.RelationID = golem.RelationID{}; value.HasRelationID = false },
		"provider access":  func(value *NodeInput) { value.Access = accessFullScan },
		"provider child":   func(value *NodeInput) { value.Children = []NodeInput{{Kind: kindConstant, Access: accessConstant}} },
		"invented index":   func(value *NodeInput) { value.IndexID = IndexID{15: 9}; value.HasIndexID = true },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			input := validInput()
			node := validDeferredNode()
			mutate(&node)
			input.Statements[0].Root = NodeInput{Kind: kindJoin, Access: accessNone, Children: []NodeInput{input.Statements[0].Root, node}}
			input.MaximumExecutionStatements = 3
			_, err := Build(input)
			if name == "unbounded" {
				assertCode(t, err, CodeUnavailable)
			} else {
				assertCode(t, err, CodeInvalid)
			}
		})
	}
}

func TestBuildAcceptsDeferredAndProviderPlannedPolicyHydration(t *testing.T) {
	input := validInput()
	input.Statements = append(input.Statements, StatementInput{Ordinal: 1, Purpose: purposePolicyHydration, Root: validDeferredNode()})
	input.MaximumExecutionStatements = 3
	if _, err := Build(input); err != nil {
		t.Fatalf("typed deferred policy hydration rejected: %v", err)
	}
	input.Statements[1].Root = NodeInput{Kind: kindConstant, Access: accessConstant}
	input.MinimumExecutionStatements, input.MaximumExecutionStatements = 2, 2
	if _, err := Build(input); err != nil {
		t.Fatalf("provider-planned policy hydration rejected: %v", err)
	}
}

func TestBuildDerivesCanonicalWarningsAndDigest(t *testing.T) {
	input := validInput()
	input.Statements[0].Root = NodeInput{Kind: kindJoin, Access: accessNone, Children: []NodeInput{
		{Kind: kindAccess, Access: accessFullScan, ModelID: golem.ModelID{15: 2}, HasModelID: true},
		{Kind: kindSort, Access: accessNone},
		{Kind: kindMaterialize, Access: accessNone},
		validDeferredNode(),
		{Kind: kindUnknown, Access: accessUnknown},
	}}
	input.MaximumExecutionStatements = 3
	report, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{warningFullScan, warningTemporarySort, warningMaterialization, warningDeferredBatch, warningMultiStatement, warningUnknownProviderNode}
	if got := report.Warnings(); !reflect.DeepEqual(got, want) {
		t.Fatalf("warnings=%v want=%v", got, want)
	}
	if report.CanonicalDigest() == ([32]byte{}) {
		t.Fatal("valid report has zero digest")
	}
	again, err := Build(input)
	if err != nil || again.CanonicalDigest() != report.CanonicalDigest() {
		t.Fatal("canonical digest is nondeterministic")
	}
}

func TestBuildDerivesUnknownWarningFromUnknownAccessWithoutGuessingIdentity(t *testing.T) {
	input := validInput()
	input.Statements[0].Root = NodeInput{Kind: kindAccess, Access: accessUnknown, ModelID: input.RootModelID, HasModelID: true}
	report, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	root := report.Statements()[0].Root()
	if got := root.Warnings(); !reflect.DeepEqual(got, []string{warningUnknownProviderNode}) {
		t.Fatalf("node warnings=%v", got)
	}
	if got := report.Warnings(); !reflect.DeepEqual(got, []string{warningUnknownProviderNode}) {
		t.Fatalf("report warnings=%v", got)
	}
	if _, ok := root.IndexID(); ok {
		t.Fatal("unknown access acquired a guessed index identity")
	}
}

func TestBuildEnforcesExactComplexityLimits(t *testing.T) {
	exactStatements := validInput()
	for ordinal := 1; ordinal < maxStatements; ordinal++ {
		exactStatements.Statements = append(exactStatements.Statements, StatementInput{Ordinal: uint32(ordinal), Purpose: purposePolicyHydration, Root: NodeInput{Kind: kindConstant, Access: accessConstant}})
	}
	exactStatements.MinimumExecutionStatements, exactStatements.MaximumExecutionStatements = maxStatements, maxStatements
	if _, err := Build(exactStatements); err != nil {
		t.Fatalf("exact statement limit: %v", err)
	}

	fields := make([]golem.FieldID, maxFields)
	for index := range fields {
		fields[index][14], fields[index][15] = byte(index>>8), byte(index+1)
	}
	input := validInput()
	input.Statements[0].Root.FieldIDs = fields
	if _, err := Build(input); err != nil {
		t.Fatalf("exact field limit: %v", err)
	}
	input.Statements[0].Root.FieldIDs = append(fields, golem.FieldID{13: 1})
	assertBuildCode(t, input, CodeTooComplex)

	exactDepth := validInput()
	exactDepth.Statements[0].Root = nestedNode(maxNodeDepth)
	if _, err := Build(exactDepth); err != nil {
		t.Fatalf("exact depth: %v", err)
	}
	tooDeep := validInput()
	tooDeep.Statements[0].Root = nestedNode(maxNodeDepth + 1)
	assertBuildCode(t, tooDeep, CodeTooComplex)

	exactNodes := validInput()
	exactNodes.Statements[0].Root = broadNode(maxNodes - 1)
	if _, err := Build(exactNodes); err != nil {
		t.Fatalf("exact node limit: %v", err)
	}
	tooManyNodes := validInput()
	tooManyNodes.Statements[0].Root = broadNode(maxNodes)
	assertBuildCode(t, tooManyNodes, CodeTooComplex)

	exactWarnings := validInput()
	exactWarnings.Statements[0].Root = warningNode(maxWarnings - 1)
	if _, err := Build(exactWarnings); err != nil {
		t.Fatalf("exact warning limit: %v", err)
	}
	tooManyWarnings := validInput()
	tooManyWarnings.Statements[0].Root = warningNode(maxWarnings)
	assertBuildCode(t, tooManyWarnings, CodeTooComplex)
}

func TestBuildDeepCopiesEveryProducerSlice(t *testing.T) {
	input := validInput()
	child := NodeInput{Kind: kindConstant, Access: accessConstant}
	input.Statements[0].Root = NodeInput{Kind: kindJoin, Access: accessNone, FieldIDs: []golem.FieldID{{15: 7}}, Children: []NodeInput{input.Statements[0].Root, child}}
	report, err := Build(input)
	if err != nil {
		t.Fatal(err)
	}
	digest := report.CanonicalDigest()
	input.Statements[0].Purpose = purposeScoped
	input.Statements[0].Root.FieldIDs[0][15] = 99
	input.Statements[0].Root.Children[0].Kind = kindUnknown
	if report.CanonicalDigest() != digest || report.Statements()[0].Purpose() != purposeRoot || report.Statements()[0].Root().FieldIDs()[0][15] != 7 || report.Statements()[0].Root().Children()[0].Kind() != kindAccess {
		t.Fatal("producer slice mutation changed immutable report")
	}
}

func TestCanonicalDigestCoversEveryClosedFactClass(t *testing.T) {
	baseline, err := Build(validInput())
	if err != nil {
		t.Fatal(err)
	}
	baselineDigest := baseline.CanonicalDigest()
	mutants := map[string]func(*Input){
		"provider":   func(value *Input) { value.Provider = golem.PostgreSQL },
		"operation":  func(value *Input) { value.Operation = operationFindFirst },
		"root model": func(value *Input) { value.RootModelID = golem.ModelID{15: 9} },
		"access":     func(value *Input) { value.Statements[0].Root.Access = accessUniqueIndex },
		"model":      func(value *Input) { value.Statements[0].Root.ModelID = golem.ModelID{15: 9} },
		"field":      func(value *Input) { value.Statements[0].Root.FieldIDs = []golem.FieldID{{15: 8}} },
		"relation": func(value *Input) {
			value.Statements[0].Root.RelationID = golem.RelationID{15: 7}
			value.Statements[0].Root.HasRelationID = true
		},
		"index": func(value *Input) { value.Statements[0].Root.IndexID = IndexID{15: 6} },
		"child": func(value *Input) {
			value.Statements[0].Root.Children = []NodeInput{{Kind: kindConstant, Access: accessConstant}}
		},
		"warning fact": func(value *Input) {
			value.Statements[0].Root.Access = accessFullScan
			value.Statements[0].Root.IndexID = IndexID{}
			value.Statements[0].Root.HasIndexID = false
		},
	}
	for name, mutate := range mutants {
		t.Run(name, func(t *testing.T) {
			input := validInput()
			mutate(&input)
			report, buildErr := Build(input)
			if buildErr != nil {
				t.Fatalf("mutant does not build: %v", buildErr)
			}
			if report.CanonicalDigest() == baselineDigest {
				t.Fatal("closed fact was omitted from canonical digest")
			}
		})
	}

	batchInput := validInput()
	batchInput.Statements[0].Root = NodeInput{Kind: kindJoin, Access: accessNone, Children: []NodeInput{batchInput.Statements[0].Root, validDeferredNode()}}
	batchInput.MaximumExecutionStatements = 3
	batchBaseline, err := Build(batchInput)
	if err != nil {
		t.Fatal(err)
	}
	batchMutants := map[string]func(*Input){
		"batch capacity": func(value *Input) { value.Statements[0].Root.Children[1].BatchCapacity++ },
		"batch minimum": func(value *Input) {
			value.Statements[0].Root.Children[1].BatchMinimum = 1
			value.MinimumExecutionStatements = 2
		},
		"batch maximum": func(value *Input) {
			value.Statements[0].Root.Children[1].BatchMaximum = 3
			value.MaximumExecutionStatements = 4
		},
	}
	for name, mutate := range batchMutants {
		t.Run(name, func(t *testing.T) {
			input := batchInput
			input.Statements = append([]StatementInput(nil), batchInput.Statements...)
			input.Statements[0].Root.Children = append([]NodeInput(nil), batchInput.Statements[0].Root.Children...)
			mutate(&input)
			report, buildErr := Build(input)
			if buildErr != nil {
				t.Fatal(buildErr)
			}
			if report.CanonicalDigest() == batchBaseline.CanonicalDigest() {
				t.Fatal("batch fact was omitted from canonical digest")
			}
		})
	}

	statementInput := validInput()
	statementInput.Statements = append(statementInput.Statements, StatementInput{Ordinal: 1, Purpose: purposePolicyHydration, Root: NodeInput{Kind: kindConstant, Access: accessConstant}})
	statementInput.MinimumExecutionStatements, statementInput.MaximumExecutionStatements = 2, 2
	statementBaseline, err := Build(statementInput)
	if err != nil {
		t.Fatal(err)
	}
	statementInput.Statements[1].Purpose = purposeRelationBatch
	statementInput.Statements[1].Root = validDeferredNode()
	statementInput.MinimumExecutionStatements, statementInput.MaximumExecutionStatements = 1, 3
	statementChanged, err := Build(statementInput)
	if err != nil {
		t.Fatal(err)
	}
	if statementChanged.CanonicalDigest() == statementBaseline.CanonicalDigest() {
		t.Fatal("statement purpose was omitted from canonical digest")
	}
}

func TestCodeOfRejectsUnknownPrivateCodes(t *testing.T) {
	if code, ok := CodeOf(&planError{code: Code("PLAN_FORGED")}); ok || code != "" {
		t.Fatalf("CodeOf forged=(%q,%v)", code, ok)
	}
}

func TestProducerInputVocabularyContainsOnlyClosedSanitizedFacts(t *testing.T) {
	assertFieldInventory(t, reflect.TypeOf(Input{}), "Provider", "Operation", "RootModelID", "Statements", "MinimumExecutionStatements", "MaximumExecutionStatements")
	assertFieldInventory(t, reflect.TypeOf(StatementInput{}), "Ordinal", "Purpose", "Root")
	assertFieldInventory(t, reflect.TypeOf(NodeInput{}), "Kind", "Access", "ModelID", "HasModelID", "FieldIDs", "RelationID", "HasRelationID", "IndexID", "HasIndexID", "BatchCapacity", "BatchMinimum", "BatchMaximum", "HasBatch", "Children")
}

func assertFieldInventory(t *testing.T, value reflect.Type, want ...string) {
	t.Helper()
	got := make([]string, value.NumField())
	for index := range got {
		got[index] = value.Field(index).Name
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s fields=%v want=%v", value, got, want)
	}
}

func validInput() Input {
	model := golem.ModelID{15: 1}
	return Input{
		Provider: golem.SQLite, Operation: operationFindMany, RootModelID: model,
		MinimumExecutionStatements: 1, MaximumExecutionStatements: 1,
		Statements: []StatementInput{{Ordinal: 0, Purpose: purposeRoot, Root: NodeInput{
			Kind: kindAccess, Access: accessPrimaryKey, ModelID: model, HasModelID: true,
			IndexID: IndexID{15: 2}, HasIndexID: true,
		}}},
	}
}

func validDeferredNode() NodeInput {
	return NodeInput{
		Kind: kindDeferredBatch, Access: accessNone,
		ModelID: golem.ModelID{15: 3}, HasModelID: true,
		RelationID: golem.RelationID{15: 4}, HasRelationID: true,
		HasBatch: true, BatchCapacity: 64, BatchMinimum: 0, BatchMaximum: 2,
	}
}

func nestedNode(depth int) NodeInput {
	value := NodeInput{Kind: kindConstant, Access: accessConstant}
	for level := 1; level < depth; level++ {
		value = NodeInput{Kind: kindJoin, Access: accessNone, Children: []NodeInput{value}}
	}
	return value
}

func broadNode(children int) NodeInput {
	values := make([]NodeInput, children)
	for index := range values {
		values[index] = NodeInput{Kind: kindConstant, Access: accessConstant}
	}
	return NodeInput{Kind: kindJoin, Access: accessNone, Children: values}
}

func warningNode(children int) NodeInput {
	values := make([]NodeInput, children)
	for index := range values {
		values[index] = NodeInput{Kind: kindAccess, Access: accessFullScan, ModelID: golem.ModelID{14: 1, 15: byte(index + 1)}, HasModelID: true}
	}
	return NodeInput{Kind: kindJoin, Access: accessNone, Children: values}
}

func assertBuildCode(t *testing.T, input Input, want Code) {
	t.Helper()
	_, err := Build(input)
	assertCode(t, err, want)
}

func assertCode(t *testing.T, err error, want Code) {
	t.Helper()
	got, ok := CodeOf(err)
	if !ok || got != want {
		t.Fatalf("error=%v code=%q ok=%v want=%q", err, got, ok, want)
	}
}
