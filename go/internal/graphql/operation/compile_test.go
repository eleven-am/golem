package operation

import (
	"context"
	"encoding/hex"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"
)

func TestCompilerLowersAliasedRootAndNestedArgumentsToP3(t *testing.T) {
	compilation := social(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	query, errors := gqlparser.LoadQuery(schema, `query Feed($take: Int!) {
  feed: `+post.Roots.FindMany+`(where: { title: { contains: "go" } }, take: $take) {
    id
    recent: comments(take: 2) { id body }
    _count { comments(where: { body: { not: "spam" } }) }
  }
}`)
	if len(errors) != 0 {
		t.Fatalf("query errors = %v", errors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := compiler.Compile(query, query.Operations.ForName("Feed"), map[string]any{"take": 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Reads) != 1 || result.Reads[0].ResponseName != "feed" || result.Reads[0].Operation != readir.FindMany {
		t.Fatalf("roots = %#v", result.Reads)
	}
	request := result.Reads[0].Request
	if take, ok := request.Take(); !ok || take != 5 || len(request.Selection()) != 3 {
		t.Fatalf("take/selections = %d/%v/%d", take, ok, len(request.Selection()))
	}
	if len(result.Reads[0].Slots) != 3 || result.Reads[0].Slots[1].ResponseName != "recent" {
		t.Fatalf("slots = %#v", result.Reads[0].Slots)
	}
	frozen := result.Reads[0].Frozen
	if frozen.ModelID() != golemModelID(post.ModelID) || frozen.Operation() != golem.ReadFindMany {
		t.Fatalf("frozen root = %x/%d", frozen.ModelID(), frozen.Operation())
	}
	frozenSelection := frozen.Selection()
	if len(frozenSelection) != 3 || frozenSelection[1].OccurrenceID() == 0 || frozenSelection[2].OccurrenceID() == 0 || frozenSelection[1].OccurrenceID() == frozenSelection[2].OccurrenceID() {
		t.Fatalf("frozen occurrences = %#v", frozenSelection)
	}
	child, ok := frozenSelection[1].Request()
	if !ok {
		t.Fatal("frozen relation child is absent")
	}
	if childTake, present := child.Take(); !present || childTake != 2 {
		t.Fatalf("frozen child take = %d/%v", childTake, present)
	}
}

func TestP6GraphQLSelectionDrivesMeasuresAndRejectsUngroupedKeys(t *testing.T) {
	compilation := p6AnalyticsSocial(t, social(t))
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, errors := gqlparser.LoadQuery(schema, `query Analytics {
  grouped: groupByPosts(
    by: [title]
    having: { min: { title: { not: "hidden" } } }
    orderBy: [{ min: { title: desc } }]
  ) {
    key { title }
    total: count
    counted: countFields { title }
  }
}`)
	if len(errors) != 0 {
		t.Fatal(errors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := compiler.Compile(query, query.Operations.ForName("Analytics"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Analytics) != 1 || len(result.Reads) != 0 || result.Order[0].Kind != RootAnalytics {
		t.Fatalf("analytics roots = %#v", result)
	}
	root := result.Analytics[0]
	if root.ResponseName != "grouped" || root.Request.Operation() != golem.AnalyticsGroupBy || len(root.Request.Dimensions()) != 1 {
		t.Fatalf("analytics root = %#v", root)
	}
	measures := root.Request.Measures()
	if len(measures) != 2 {
		t.Fatalf("selected measures = %#v", measures)
	}
	operators := map[golem.AggregateOperator]bool{}
	for _, measure := range measures {
		operators[measure.Operator] = true
	}
	if !operators[golem.AggregateCountAll] || !operators[golem.AggregateCountField] || operators[golem.AggregateMinimum] {
		t.Fatalf("selection-driven operators = %#v", operators)
	}
	if _, present := root.Request.Having(); !present || len(root.Request.OrderBy()) != 1 || root.Request.OrderBy()[0].Term.Operator != golem.AggregateMinimum {
		t.Fatalf("private having/order terms were not retained: %#v", root.Request)
	}
	dimension := root.Request.Dimensions()[0]
	cells := []golem.RuntimeAnalyticsCell{golem.RuntimePresentAnalyticsCell(golem.RuntimeAnalyticsTermKey(dimension), "visible")}
	for _, measure := range measures {
		cells = append(cells, golem.RuntimePresentAnalyticsCell(golem.RuntimeAnalyticsTermKey(measure), int64(2)))
	}
	encoded, err := compiler.EncodeAnalytics(root, [][]golem.RuntimeAnalyticsCell{cells})
	if err != nil {
		t.Fatal(err)
	}
	row := encoded.([]any)[0].(map[string]any)
	if row["total"] != "2" || row["counted"].(map[string]any)["title"] != "2" || row["key"].(map[string]any)["title"] != "visible" {
		t.Fatalf("exact/aliased analytics encoding = %#v", row)
	}

	invalid, validationErrors := gqlparser.LoadQuery(schema, `query { groupByPosts(by: [title]) { key { authorID } count } }`)
	if len(validationErrors) != 0 {
		t.Fatalf("query should be schema-valid before semantic by validation: %v", validationErrors)
	}
	if _, err := compiler.Compile(invalid, invalid.Operations[0], nil); err == nil {
		t.Fatal("selected ungrouped key was accepted")
	}
}

func p6AnalyticsSocial(t *testing.T, compilation compilerir.CompilationIR) compilerir.CompilationIR {
	t.Helper()
	compilation.Contract.Models = append([]compilerir.ModelContractIR(nil), compilation.Contract.Models...)
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		if contract.GraphQLName != "Post" {
			continue
		}
		var authorID, title compilerir.FieldID
		for _, field := range contract.Fields {
			switch field.GraphQLName {
			case "authorID":
				authorID = field.FieldID
			case "title":
				title = field.FieldID
			}
		}
		if authorID == "" || title == "" {
			t.Fatal("Post analytics fields are absent")
		}
		contract.Operations = append(contract.Operations, compilerir.OperationAggregate, compilerir.OperationGroupBy)
		contract.Aggregation = &compilerir.AggregationContractIR{
			Enabled: true, Dimensions: []compilerir.FieldID{authorID, title}, DimensionsExplicit: true,
			Measures: []compilerir.FieldID{title}, MeasuresExplicit: true,
			GraphQLMaxGroups: 100, RelationMaxIntermediateGroups: 10_000,
		}
		return compilation
	}
	t.Fatal("Post contract is absent")
	return compilerir.CompilationIR{}
}

func TestCompilerRejectsIncompatibleMergedRoot(t *testing.T) {
	compilation := social(t)
	document, _ := graphqlschema.Build(compilation)
	schema, _ := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	post := contractNamed(t, compilation.Contract, "Post")
	query, errors := gqlparser.LoadQuery(schema, `query { same: `+post.Roots.FindMany+`(take: 1) { id } same: `+post.Roots.FindMany+`(take: 2) { id } }`)
	if len(errors) == 0 {
		compiler, _ := New(compilation, Limits{})
		if _, err := compiler.Compile(query, query.Operations[0], nil); err == nil {
			t.Fatal("incompatible merged root was accepted")
		}
	}
}

func TestConditionalScalarRelationListAndCountMasksStayLocalToSelectedField(t *testing.T) {
	compilation := social(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	query, errors := gqlparser.LoadQuery(schema, `query { `+post.Roots.FindMany+`(take: 1) { body title author { handle } comments(take: 1) { body } _count { comments } } }`)
	if len(errors) != 0 {
		t.Fatal(errors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	root := compiled.Reads[0]
	var body, title, author, comments selectset.Slot
	var count selectset.Slot
	for _, slot := range root.Slots {
		switch slot.ResponseName {
		case "body":
			body = slot
		case "title":
			title = slot
		case "author":
			author = slot
		case "comments":
			comments = slot
		case "_count":
			count = slot.Children[0]
		}
	}
	if body.FieldID == "" || title.FieldID == "" || author.Occurrence == 0 || comments.Occurrence == 0 || count.Occurrence == 0 {
		t.Fatalf("slots=%#v", root.Slots)
	}
	postID, _ := publicModelID(root.Model)
	bodyID, _ := publicFieldID(body.FieldID)
	titleID, _ := publicFieldID(title.FieldID)
	authorFieldID, _ := publicFieldID(author.FieldID)
	commentsFieldID, _ := publicFieldID(comments.FieldID)
	countFieldID, _ := publicFieldID(count.FieldID)
	countRelationID, _ := publicRelationID(count.RelationID)
	row, err := golem.RuntimeModelReadRowWithOccurrences(postID,
		[]golem.RuntimeReadCell{golem.RuntimePresentReadCell(bodyID, "visible", nil), golem.RuntimeNullReadCell(titleID)},
		[]golem.RuntimeRelationCountCell{golem.RuntimeNullRelationCountOccurrenceCell(countFieldID, countRelationID, golem.RuntimeOccurrenceID(count.Occurrence))},
		[]golem.RuntimeOccurrenceCell{
			golem.RuntimeNullOccurrenceCell(authorFieldID, golem.RuntimeOccurrenceID(author.Occurrence)),
			golem.RuntimeNullOccurrenceCell(commentsFieldID, golem.RuntimeOccurrenceID(comments.Occurrence)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := compiler.EncodeRead(root, []golem.RuntimeModelRow{row})
	if err != nil {
		t.Fatal(err)
	}
	items := encoded.([]any)
	value := items[0].(map[string]any)
	if value["body"] != "visible" {
		t.Fatalf("unmasked sibling body=%#v", value["body"])
	}
	if value["title"] != nil {
		t.Fatalf("masked title=%#v", value["title"])
	}
	if value["author"] != nil {
		t.Fatalf("masked to-one relation=%#v", value["author"])
	}
	if value["comments"] != nil {
		t.Fatalf("masked to-many relation=%#v", value["comments"])
	}
	if value["_count"].(map[string]any)["comments"] != nil {
		t.Fatalf("counts=%#v", value["_count"])
	}
}

func TestGraphQLAliasedRelationAndCountOccurrencesKeepIndependentArgumentsAndResults(t *testing.T) {
	compilation := social(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	post := contractNamed(t, compilation.Contract, "Post")
	query, errors := gqlparser.LoadQuery(schema, `query { `+post.Roots.FindMany+`(take: 1) {
  recent: comments(take: 1) { body }
  archive: comments(take: 2) { body }
  _count { visible: comments(where: { body: { contains: "ok" } }) total: comments }
} }`)
	if len(errors) != 0 {
		t.Fatal(errors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	root := compiled.Reads[0]
	var recent, archive, visible, total selectset.Slot
	for _, slot := range root.Slots {
		switch slot.ResponseName {
		case "recent":
			recent = slot
		case "archive":
			archive = slot
		case "_count":
			for _, child := range slot.Children {
				switch child.ResponseName {
				case "visible":
					visible = child
				case "total":
					total = child
				}
			}
		}
	}
	if recent.Occurrence == 0 || archive.Occurrence == 0 || recent.Occurrence == archive.Occurrence || visible.Occurrence == 0 || total.Occurrence == 0 || visible.Occurrence == total.Occurrence {
		t.Fatalf("aliases collapsed to shared occurrences: %#v", root.Slots)
	}
	recentRequest, recentOK := root.Request.Selection()[0].Request()
	archiveRequest, archiveOK := root.Request.Selection()[1].Request()
	if !recentOK || !archiveOK {
		t.Fatalf("aliased relation requests are absent: %#v", root.Request.Selection())
	}
	if take, ok := recentRequest.Take(); !ok || take != 1 {
		t.Fatalf("recent take = %d/%v", take, ok)
	}
	if take, ok := archiveRequest.Take(); !ok || take != 2 {
		t.Fatalf("archive take = %d/%v", take, ok)
	}

	commentModel := contractNamed(t, compilation.Contract, "Comment").ModelID
	commentID, _ := publicModelID(commentModel)
	bodyID, _ := publicFieldID(recent.Children[0].FieldID)
	commentRow := func(value string) golem.RuntimeModelRow {
		row, rowErr := golem.RuntimeModelReadRow(commentID, golem.RuntimePresentReadCell(bodyID, value, nil))
		if rowErr != nil {
			t.Fatal(rowErr)
		}
		return row
	}
	postID, _ := publicModelID(root.Model)
	recentFieldID, _ := publicFieldID(recent.FieldID)
	archiveFieldID, _ := publicFieldID(archive.FieldID)
	visibleFieldID, _ := publicFieldID(visible.FieldID)
	visibleRelationID, _ := publicRelationID(visible.RelationID)
	totalFieldID, _ := publicFieldID(total.FieldID)
	totalRelationID, _ := publicRelationID(total.RelationID)
	row, err := golem.RuntimeModelReadRowWithOccurrences(postID, nil,
		[]golem.RuntimeRelationCountCell{
			golem.RuntimePresentRelationCountOccurrenceCell(visibleFieldID, visibleRelationID, golem.RuntimeOccurrenceID(visible.Occurrence), 1),
			golem.RuntimePresentRelationCountOccurrenceCell(totalFieldID, totalRelationID, golem.RuntimeOccurrenceID(total.Occurrence), 3),
		},
		[]golem.RuntimeOccurrenceCell{
			golem.RuntimeToManyOccurrenceCell(recentFieldID, golem.RuntimeOccurrenceID(recent.Occurrence), []golem.RuntimeModelRow{commentRow("recent")}),
			golem.RuntimeToManyOccurrenceCell(archiveFieldID, golem.RuntimeOccurrenceID(archive.Occurrence), []golem.RuntimeModelRow{commentRow("old-a"), commentRow("old-b")}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := compiler.EncodeRead(root, []golem.RuntimeModelRow{row})
	if err != nil {
		t.Fatal(err)
	}
	value := encoded.([]any)[0].(map[string]any)
	gotRecent := value["recent"].([]any)
	gotArchive := value["archive"].([]any)
	if len(gotRecent) != 1 || gotRecent[0].(map[string]any)["body"] != "recent" {
		t.Fatalf("recent alias result = %#v", gotRecent)
	}
	if len(gotArchive) != 2 || gotArchive[0].(map[string]any)["body"] != "old-a" || gotArchive[1].(map[string]any)["body"] != "old-b" {
		t.Fatalf("archive alias result = %#v", gotArchive)
	}
	counts := value["_count"].(map[string]any)
	if counts["visible"] != int32(1) || counts["total"] != int32(3) {
		t.Fatalf("count alias results = %#v", counts)
	}
}

func TestCompilerCarriesComputedSlotsAndWithholdsDependenciesFromResponse(t *testing.T) {
	result := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", result.Diagnostics)
	}
	compilation := *result.Compilation
	user := contractNamed(t, compilation.Contract, "User")
	document, parseErr := parser.ParseQuery(&ast.Source{Name: "computed.graphql", Input: `{ ` + user.Roots.FindMany + ` { batchGreeting(prefix: "hello") } }`})
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(document, document.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Reads) != 1 || len(compiled.Reads[0].Slots) != 1 || len(compiled.Reads[0].Request.Selection()) != 2 {
		t.Fatalf("compiled computed read = %#v", compiled)
	}
	slot := compiled.Reads[0].Slots[0]
	if slot.Kind != selectset.SlotComputed || slot.Computed == nil || slot.Computed.Batch == nil {
		t.Fatalf("computed slot = %#v", slot)
	}
	if slot.Computed.CanonicalArguments != `{"prefix":"hello"}` || len(slot.Computed.Dependencies) != 2 {
		t.Fatalf("computed metadata = %#v", slot.Computed)
	}
	for _, dependency := range slot.Computed.Dependencies {
		if dependency.Kind != graphqlextension.DependencyMaskedScalar {
			t.Fatalf("dependency is not masked public access: %#v", dependency)
		}
	}
}

func TestGraphQLEnumNamesMapExactlyToDeclaredWireValues(t *testing.T) {
	result := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/graphql_extensions", Pattern: "."})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", result.Diagnostics)
	}
	compilation := *result.Compilation
	user := contractNamed(t, compilation.Contract, "User")
	var status compilerir.FieldContractIR
	for _, field := range user.Fields {
		if field.GraphQLName == "status" {
			status = field
		}
	}
	if status.FieldID == "" || len(compilation.Contract.Enums) != 1 {
		t.Fatalf("status/enum contract = %#v / %#v", status, compilation.Contract.Enums)
	}
	enum := compilation.Contract.Enums[0]
	activeName := "UserStatusActive"
	var activeMember compilerir.EnumValueID
	for _, value := range enum.Values {
		if value.GraphQLName == activeName {
			activeMember = value.ValueID
		}
	}
	if activeMember == "" {
		t.Fatalf("active enum mapping is absent: %#v", enum.Values)
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, queryErrors := gqlparser.LoadQuery(schema, `{ `+user.Roots.FindMany+`(where: { status: { equals: `+activeName+` } }) { status } }`)
	if len(queryErrors) != 0 {
		t.Fatalf("query errors = %v", queryErrors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	where, ok := compiled.Reads[0].Request.Where()
	if !ok {
		t.Fatal("enum predicate was not bound")
	}
	operand, ok := where.Operand()
	if !ok {
		t.Fatalf("enum condition has no operand: %#v", where)
	}
	value, ok := operand.One()
	if !ok {
		t.Fatalf("enum operand is not singular: %#v", operand)
	}
	_, member, ok := value.Enum()
	decoded, _ := hex.DecodeString(string(activeMember))
	var expected policyir.EnumValueID
	copy(expected[:], decoded)
	if !ok || member != expected {
		t.Fatalf("bound enum member = %x/%v, want %x", member, ok, expected)
	}

	modelID, _ := publicModelID(user.ModelID)
	fieldID, _ := publicFieldID(status.FieldID)
	row, err := golem.RuntimeModelReadRow(modelID, golem.RuntimePresentReadCell(fieldID, "ACTIVE", nil))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := compiler.EncodeRead(compiled.Reads[0], []golem.RuntimeModelRow{row})
	if err != nil {
		t.Fatal(err)
	}
	if got := encoded.([]any)[0].(map[string]any)["status"]; got != activeName {
		t.Fatalf("serialized enum = %#v, want %q", got, activeName)
	}
	invalid, err := golem.RuntimeModelReadRow(modelID, golem.RuntimePresentReadCell(fieldID, "UNDECLARED", nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compiler.EncodeRead(compiled.Reads[0], []golem.RuntimeModelRow{invalid}); err == nil {
		t.Fatal("undeclared enum wire value was serialized")
	}
}

func social(t *testing.T) compilerir.CompilationIR {
	t.Helper()
	result := compile.Compile(context.Background(), compile.Config{Dir: "../../compiler/compile/testdata/social", Pattern: "."})
	if len(result.Diagnostics) != 0 || result.Compilation == nil {
		t.Fatalf("compile diagnostics = %#v", result.Diagnostics)
	}
	return *result.Compilation
}

func contractNamed(t *testing.T, contract compilerir.ContractIR, name string) compilerir.ModelContractIR {
	t.Helper()
	for _, model := range contract.Models {
		if model.GraphQLName == name {
			return model
		}
	}
	t.Fatalf("missing contract model %s", name)
	return compilerir.ModelContractIR{}
}

func golemModelID(id compilerir.ModelID) (result golem.ModelID) {
	decoded, _ := hex.DecodeString(string(id))
	copy(result[:], decoded)
	return result
}
