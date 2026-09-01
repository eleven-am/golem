package operation

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/compiler/compile"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	graphqlbind "github.com/eleven-am/golem/go/internal/graphql/bind"
	graphqlextension "github.com/eleven-am/golem/go/internal/graphql/extension"
	graphqlschema "github.com/eleven-am/golem/go/internal/graphql/schema"
	selectset "github.com/eleven-am/golem/go/internal/graphql/select"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readir "github.com/eleven-am/golem/go/internal/read/ir"
	semanticcontract "github.com/eleven-am/golem/go/internal/semantic/contract"
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

func TestSemanticCustomSearchBindsRuntimeLimitAndReloadsSelectedRelationsInRankOrder(t *testing.T) {
	compilation := semanticSocial(t)
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	operation := compilation.Contract.CustomOperations[0]
	postContract := contractNamed(t, compilation.Contract, "Post")
	postModel := modelByIDForTest(t, compilation.Model, postContract.ModelID)
	relationField, relation, targetContract := firstRelationForTest(t, compilation, postModel)
	postID := fieldContractByIDForTest(t, postContract, postModel.PrimaryKey.Fields[0])
	targetModel := modelByIDForTest(t, compilation.Model, targetContract.ModelID)
	targetID := fieldContractByIDForTest(t, targetContract, targetModel.PrimaryKey.Fields[0])
	limitQuery, limitErrors := gqlparser.LoadQuery(schema, `query Limit { `+operation.Name+`(query: "rank me") { `+postID.GraphQLName+` } }`)
	if len(limitErrors) != 0 {
		t.Fatal(limitErrors)
	}
	limitCompiler, err := New(compilation, Limits{Bind: graphqlbind.Limits{MaxPageSize: 2}})
	if err != nil {
		t.Fatal(err)
	}
	limited, err := limitCompiler.Compile(limitQuery, limitQuery.Operations.ForName("Limit"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited.Custom) != 1 || limited.Custom[0].Arguments["take"] != int32(2) {
		t.Fatalf("runtime-limited semantic take = %#v", limited.Custom)
	}
	for _, semanticOperation := range compilation.Contract.CustomOperations {
		arguments := map[string]any{}
		bound, bindErr := limitCompiler.bindSemanticSearchTake(semanticOperation, arguments, false)
		if bindErr != nil || !bound || arguments["take"] != int32(2) {
			t.Fatalf("omitted %s take bound=%t arguments=%#v error=%v", semanticOperation.Name, bound, arguments, bindErr)
		}
	}
	overflow, overflowErrors := gqlparser.LoadQuery(schema, `query { `+operation.Name+`(query: "rank me", take: 3) { `+postID.GraphQLName+` } }`)
	if len(overflowErrors) != 0 {
		t.Fatal(overflowErrors)
	}
	if _, err := limitCompiler.Compile(overflow, overflow.Operations[0], nil); err == nil {
		t.Fatal("semantic take above the runtime GraphQL maximum was accepted")
	}

	relationArguments := ""
	if definition := schema.Types[postContract.GraphQLName].Fields.ForName(relationField.GraphQLName); definition != nil && definition.Arguments.ForName("take") != nil {
		relationArguments = "(take: 1)"
	}
	query, queryErrors := gqlparser.LoadQuery(schema, `query Search { `+operation.Name+`(query: "rank me") { `+postID.GraphQLName+` `+relationField.GraphQLName+relationArguments+` { `+targetID.GraphQLName+` } } }`)
	if len(queryErrors) != 0 {
		t.Fatal(queryErrors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile(query, query.Operations.ForName("Search"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled.Custom) != 1 || compiled.Custom[0].semantic == nil {
		t.Fatalf("semantic root/take = %#v", compiled.Custom)
	}

	postModelID := golemModelID(postModel.ID)
	postFieldID, _ := publicFieldID(postModel.PrimaryKey.Fields[0])
	firstIdentity := semanticIdentityValue(t, generatedFieldByIDForTest(t, postModel, postModel.PrimaryKey.Fields[0]), 1)
	secondIdentity := semanticIdentityValue(t, generatedFieldByIDForTest(t, postModel, postModel.PrimaryKey.Fields[0]), 2)
	firstRanked := runtimeRowForTest(t, postModelID, postFieldID, firstIdentity)
	secondRanked := runtimeRowForTest(t, postModelID, postFieldID, secondIdentity)
	if _, _, active, err := limitCompiler.PrepareSemanticCustomHydration(limited.Custom[0], []any{firstRanked}); err != nil || active {
		t.Fatalf("scalar-only semantic search scheduled hydration: active=%t err=%v", active, err)
	}
	request, order, active, err := compiler.PrepareSemanticCustomHydration(compiled.Custom[0], []any{secondRanked, firstRanked})
	if err != nil || !active {
		t.Fatalf("prepare semantic hydration active=%t err=%v", active, err)
	}
	if take, ok := request.Take(); !ok || take != 2 || len(request.Selection()) < 2 {
		t.Fatalf("semantic hydration request take/selection = %d/%t/%d", take, ok, len(request.Selection()))
	}
	selectedRelation := false
	for _, selection := range request.Selection() {
		selectedRelation = selectedRelation || selection.IsRelation()
	}
	if !selectedRelation {
		t.Fatal("semantic hydration request discarded the selected relation")
	}

	relationSlot := slotByFieldForTest(t, compiled.Custom[0].Slots, relationField.FieldID)
	targetModelID := golemModelID(targetModel.ID)
	targetFieldID, _ := publicFieldID(targetModel.PrimaryKey.Fields[0])
	child := runtimeRowForTest(t, targetModelID, targetFieldID, semanticIdentityValue(t, generatedFieldByIDForTest(t, targetModel, targetModel.PrimaryKey.Fields[0]), 9))
	firstHydrated := runtimeRelationRowForTest(t, postModelID, postFieldID, firstIdentity, relationField.FieldID, relationSlot, relation, child)
	secondHydrated := runtimeRelationRowForTest(t, postModelID, postFieldID, secondIdentity, relationField.FieldID, relationSlot, relation, child)
	hydrated, err := compiler.FinishSemanticCustomHydration(compiled.Custom[0], order, []golem.RuntimeModelRow{firstHydrated, secondHydrated})
	if err != nil {
		t.Fatal(err)
	}
	encoded, failures, err := compiler.EncodeCustomWithComputedPartial(context.Background(), compiled.Custom[0], hydrated, func(context.Context, compilerir.ModelID, []golem.RuntimeModelRow, selectset.Slot) ([]any, error) {
		return nil, fmt.Errorf("unexpected computed resolver")
	})
	if err != nil || len(failures) != 0 {
		t.Fatalf("semantic relation encode failures=%#v err=%v", failures, err)
	}
	items := encoded.([]any)
	if len(items) != 2 || items[0].(map[string]any)[relationField.GraphQLName] == nil || items[1].(map[string]any)[relationField.GraphQLName] == nil {
		t.Fatalf("semantic relation result = %#v", encoded)
	}
	if got := items[0].(map[string]any)[postID.GraphQLName]; got == items[1].(map[string]any)[postID.GraphQLName] || got != graphqlIdentityValue(secondIdentity) {
		t.Fatalf("semantic rank order = %#v", items)
	}
}

func TestCompilerLowersExactlyOneSubscriptionRootToFullFrozenRead(t *testing.T) {
	compilation := social(t)
	var postModel *compilerir.ModelDeclIR
	for index := range compilation.Model.Models {
		if compilation.Model.Models[index].Go.Name == "Post" {
			postModel = &compilation.Model.Models[index]
			break
		}
	}
	if postModel == nil || postModel.PrimaryKey == nil {
		t.Fatal("Post model or primary key is absent")
	}
	for index := range compilation.Contract.Models {
		contract := &compilation.Contract.Models[index]
		if contract.ModelID != postModel.ID {
			continue
		}
		shape, err := compilerir.BuildEventSchemaShape(*postModel, compilation.Model.Enums, postModel.PrimaryKey.Fields)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := compilerir.EventSchemaFingerprint(shape)
		if err != nil {
			t.Fatal(err)
		}
		contract.Subscriptions = true
		contract.Roots.Events = "postEvents"
		contract.Event = &compilerir.EventContractIR{PayloadTypeName: "PostEvent", Schema: shape, SchemaFingerprint: fingerprint}
	}
	document, err := graphqlschema.Build(compilation)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "generated.graphql", Input: document.SDL})
	if err != nil {
		t.Fatal(err)
	}
	query, errors := gqlparser.LoadQuery(schema, `subscription Feed($visible: Boolean!) {
  feed: postEvents(where: { title: { contains: "go" } }) {
    event: eventID
    type
    id
	    entity {
	      id
	      author @include(if: $visible) { id }
	      _count { comments }
	    }
	    summary: entity { value: title }
  }
}`)
	if len(errors) != 0 {
		t.Fatal(errors)
	}
	compiler, err := New(compilation, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := compiler.Compile(query, query.Operations.ForName("Feed"), map[string]any{"visible": true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Event == nil || result.Event.ResponseName != "feed" || result.Event.Model != postModel.ID || !result.Event.EntitySelected {
		t.Fatalf("event root = %#v", result.Event)
	}
	if result.Event.FrozenRead.Operation() != golem.ReadFindMany || result.Event.FrozenRead.ModelID() != golemModelID(postModel.ID) {
		t.Fatalf("frozen event read = %v / %x", result.Event.FrozenRead.Operation(), result.Event.FrozenRead.ModelID())
	}
	if _, ok := result.Event.FrozenRead.Where(); !ok || len(result.Event.FrozenRead.Selection()) < 3 {
		t.Fatalf("frozen event filter/selection = %#v", result.Event.FrozenRead.Selection())
	}
	if len(result.Event.Slots) != 5 || result.Event.Slots[0].ResponseName != "event" || len(result.Event.EntitySlots) != 4 || len(result.Event.Slots[3].EntitySlots) != 3 || len(result.Event.Slots[4].EntitySlots) != 1 || result.Event.Slots[4].EntitySlots[0].ResponseName != "value" {
		t.Fatalf("event slots = %#v / entity %#v", result.Event.Slots, result.Event.EntitySlots)
	}
	second, errors := gqlparser.LoadQuery(schema, `subscription Invalid { first: postEvents { eventID } second: postEvents { eventID } }`)
	if len(errors) == 0 {
		if _, err := compiler.Compile(second, second.Operations.ForName("Invalid"), nil); err == nil {
			t.Fatal("multi-root subscription was accepted")
		}
	}
}

func TestGraphQLSelectionDrivesMeasuresAndRejectsUngroupedKeys(t *testing.T) {
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

func semanticSocial(t *testing.T) compilerir.CompilationIR {
	t.Helper()
	compilation := social(t)
	post := contractNamed(t, compilation.Contract, "Post")
	model := modelByIDForTest(t, compilation.Model, post.ModelID)
	title := generatedFieldNamedForTest(t, model, "Title")
	payload, err := semanticcontract.Encode(semanticcontract.Index{Name: "content", Space: "content", Dimensions: 3, Fields: []string{string(title.ID)}, Metric: "cosine"})
	if err != nil {
		t.Fatal(err)
	}
	for index, provider := range []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL} {
		compilation.Model.Extensions = append(compilation.Model.Extensions, compilerir.ProviderExtensionIR{
			ID: compilerir.ExtensionID(fmt.Sprintf("%032x", index+1)), Provider: provider, Version: semanticcontract.Version,
			Owner: compilerir.ObjectID(model.ID), Kind: semanticcontract.IndexKind, Payload: payload,
		})
	}
	if diagnostics := graphqlextension.AddSemanticSearchOperations(&compilation); len(diagnostics) != 0 {
		t.Fatalf("semantic diagnostics = %#v", diagnostics)
	}
	if len(compilation.Contract.CustomOperations) != 2 {
		t.Fatalf("semantic operations = %#v", compilation.Contract.CustomOperations)
	}
	return compilation
}

func modelByIDForTest(t *testing.T, model compilerir.ModelIR, id compilerir.ModelID) compilerir.ModelDeclIR {
	t.Helper()
	for _, candidate := range model.Models {
		if candidate.ID == id {
			return candidate
		}
	}
	t.Fatalf("missing model %s", id)
	return compilerir.ModelDeclIR{}
}

func generatedFieldNamedForTest(t *testing.T, model compilerir.ModelDeclIR, name string) compilerir.FieldIR {
	t.Helper()
	for _, field := range model.Fields {
		if field.GoName == name {
			return field
		}
	}
	t.Fatalf("missing field %s.%s", model.Go.Name, name)
	return compilerir.FieldIR{}
}

func generatedFieldByIDForTest(t *testing.T, model compilerir.ModelDeclIR, id compilerir.FieldID) compilerir.FieldIR {
	t.Helper()
	for _, field := range model.Fields {
		if field.ID == id {
			return field
		}
	}
	t.Fatalf("missing field %s.%s", model.Go.Name, id)
	return compilerir.FieldIR{}
}

func fieldContractByIDForTest(t *testing.T, contract compilerir.ModelContractIR, id compilerir.FieldID) compilerir.FieldContractIR {
	t.Helper()
	for _, field := range contract.Fields {
		if field.FieldID == id {
			return field
		}
	}
	t.Fatalf("missing contract field %s.%s", contract.GraphQLName, id)
	return compilerir.FieldContractIR{}
}

func firstRelationForTest(t *testing.T, compilation compilerir.CompilationIR, source compilerir.ModelDeclIR) (compilerir.FieldContractIR, compilerir.RelationIR, compilerir.ModelContractIR) {
	t.Helper()
	sourceContract := contractNamed(t, compilation.Contract, source.Go.Name)
	for _, relation := range compilation.Model.Relations {
		if relation.SourceModel != source.ID {
			continue
		}
		field := fieldContractByIDForTest(t, sourceContract, relation.SourceField)
		for _, target := range compilation.Contract.Models {
			if target.ModelID == relation.TargetModel {
				return field, relation, target
			}
		}
	}
	t.Fatalf("model %s has no exposed relation", source.Go.Name)
	return compilerir.FieldContractIR{}, compilerir.RelationIR{}, compilerir.ModelContractIR{}
}

func semanticIdentityValue(t *testing.T, field compilerir.FieldIR, value byte) any {
	t.Helper()
	if field.Scalar == nil {
		t.Fatalf("identity field %s is not scalar", field.ID)
	}
	switch field.Scalar.Type.Kind {
	case compilerir.TypeUUID:
		var result golem.UUID
		result[len(result)-1] = value
		return result
	case compilerir.TypeString:
		return fmt.Sprintf("id-%d", value)
	case compilerir.TypeInt16:
		return int16(value)
	case compilerir.TypeInt32:
		return int32(value)
	case compilerir.TypeInt64:
		return int64(value)
	default:
		t.Fatalf("unsupported semantic identity type %s", field.Scalar.Type.Kind)
		return nil
	}
}

func runtimeRowForTest(t *testing.T, model golem.ModelID, field golem.FieldID, value any) golem.RuntimeModelRow {
	t.Helper()
	row, err := golem.RuntimeModelReadRow(model, golem.RuntimePresentReadCell(field, value, nil))
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func slotByFieldForTest(t *testing.T, slots []selectset.Slot, field compilerir.FieldID) selectset.Slot {
	t.Helper()
	for _, slot := range slots {
		if slot.FieldID == field {
			return slot
		}
	}
	t.Fatalf("missing slot for field %s", field)
	return selectset.Slot{}
}

func runtimeRelationRowForTest(t *testing.T, model golem.ModelID, identityField golem.FieldID, identity any, relationField compilerir.FieldID, slot selectset.Slot, relation compilerir.RelationIR, child golem.RuntimeModelRow) golem.RuntimeModelRow {
	t.Helper()
	field, err := publicFieldID(relationField)
	if err != nil {
		t.Fatal(err)
	}
	occurrence := golem.RuntimeOccurrenceID(slot.Occurrence)
	var cell golem.RuntimeOccurrenceCell
	if relation.Cardinality == compilerir.RelationMany {
		cell = golem.RuntimeToManyOccurrenceCell(field, occurrence, []golem.RuntimeModelRow{child})
	} else {
		cell = golem.RuntimeToOneOccurrenceCell(field, occurrence, child)
	}
	row, err := golem.RuntimeModelReadRowWithOccurrences(model, []golem.RuntimeReadCell{golem.RuntimePresentReadCell(identityField, identity, nil)}, nil, []golem.RuntimeOccurrenceCell{cell})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func graphqlIdentityValue(value any) any {
	if uuid, ok := value.(golem.UUID); ok {
		return uuid.String()
	}
	return value
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
