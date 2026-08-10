package golem

import (
	"errors"
	"testing"
)

type readPost struct{}
type readComment struct{}

var (
	readPostModel    = ModelID{0x11}
	readCommentModel = ModelID{0x22}
	readPostID       = FieldID{0x31}
	readPostTitle    = FieldID{0x32}
	readPostBody     = FieldID{0x33}
	readPostPayload  = FieldID{0x34}
	readPostComments = FieldID{0x35}
	readCommentID    = FieldID{0x41}
	readCommentBody  = FieldID{0x42}
	readCommentLive  = FieldID{0x43}
	readRelation     = RelationID{0x51}
	readPostKey      = KeyID{0x61}

	readPostDescriptor = GeneratedModelDescriptor[readPost](readPostModel, GeneratedDescriptorShape(
		[]FieldID{readPostID, readPostTitle, readPostBody, readPostPayload}, nil, nil, nil,
	))
	readCommentDescriptor = GeneratedModelDescriptor[readComment](readCommentModel, GeneratedDescriptorShape(
		[]FieldID{readCommentID, readCommentBody, readCommentLive}, nil, nil, nil,
	))

	readPosts = struct {
		ID       EqualField[readPost, UUID]
		Title    ModeTextField[readPost, string]
		Body     NullableModeTextField[readPost, string]
		Payload  BytesField[readPost]
		Comments ToMany[readPost, readComment]
	}{
		ID:       GeneratedEqualField[readPost, UUID](readPostID),
		Title:    GeneratedModeTextField[readPost, string](readPostTitle),
		Body:     GeneratedNullableModeTextField[readPost, string](readPostBody),
		Payload:  GeneratedBytesField[readPost](readPostPayload),
		Comments: GeneratedToMany[readPost, readComment](readPostComments, readRelation, readCommentModel),
	}
	readComments = struct {
		ID   EqualField[readComment, UUID]
		Body ModeTextField[readComment, string]
		Live EqualField[readComment, bool]
	}{
		ID:   GeneratedEqualField[readComment, UUID](readCommentID),
		Body: GeneratedModeTextField[readComment, string](readCommentBody),
		Live: GeneratedEqualField[readComment, bool](readCommentLive),
	}
)

func TestReadRowPreservesThreeStatesAndCopiesMutableValues(t *testing.T) {
	payload := []byte{1, 2, 3}
	row, err := RuntimeReadRow(readPostDescriptor,
		RuntimePresentReadCell(readPostTitle, "hello", func(value string) string { return value }),
		RuntimeNullReadCell(readPostBody),
		RuntimePresentReadCell(readPostPayload, payload, func(value []byte) []byte { return append([]byte(nil), value...) }),
	)
	if err != nil {
		var failure *Error
		if errors.As(err, &failure) {
			t.Fatalf("%v (model=%x field=%x operation=%s)", err, failure.Model, failure.Field, failure.Operation)
		}
		t.Fatal(err)
	}
	payload[0] = 9

	title := Value(row, readPosts.Title)
	if value, ok := title.Get(); !ok || value != "hello" || title.State() != ReadPresent {
		t.Fatalf("title=%q ok=%t state=%d", value, ok, title.State())
	}
	if body := Value(row, readPosts.Body); body.State() != ReadNull || !body.IsSelected() || !body.IsNull() {
		t.Fatalf("body state=%d selected=%t null=%t", body.State(), body.IsSelected(), body.IsNull())
	}
	if id := Value(row, readPosts.ID); id.State() != ReadUnselected || id.IsSelected() {
		t.Fatalf("id state=%d selected=%t", id.State(), id.IsSelected())
	}
	first, ok := Value(row, readPosts.Payload).Get()
	if !ok || first[0] != 1 {
		t.Fatalf("payload=%v ok=%t", first, ok)
	}
	first[0] = 8
	second, _ := Value(row, readPosts.Payload).Get()
	if second[0] != 1 {
		t.Fatalf("mutable result escaped row ownership: %v", second)
	}
}

func TestReadRowNestedRelationsAreDetached(t *testing.T) {
	comment, err := RuntimeReadRow(readCommentDescriptor,
		RuntimePresentReadCell(readCommentBody, "first", func(value string) string { return value }),
	)
	if err != nil {
		t.Fatal(err)
	}
	comments := []Row[readComment]{comment}
	post, err := RuntimeReadRow(readPostDescriptor,
		RuntimePresentReadCell(readPostComments, comments, func(values []Row[readComment]) []Row[readComment] {
			result := make([]Row[readComment], len(values))
			for index, value := range values {
				result[index] = cloneRow(value)
			}
			return result
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	comments[0] = Row[readComment]{}

	value := Many(post, readPosts.Comments)
	rows, ok := value.Get()
	if !ok || len(rows) != 1 {
		t.Fatalf("comments=%v ok=%t", rows, ok)
	}
	if body, present := Value(rows[0], readComments.Body).Get(); !present || body != "first" {
		t.Fatalf("body=%q present=%t", body, present)
	}
	rows[0] = Row[readComment]{}
	again, _ := value.Get()
	if body, present := Value(again[0], readComments.Body).Get(); !present || body != "first" {
		t.Fatalf("second body=%q present=%t", body, present)
	}
}

func TestRelationCountPreservesSelectedZeroAndCoexistsWithRows(t *testing.T) {
	comment, err := RuntimeModelReadRow(readCommentModel,
		RuntimePresentReadCell(readCommentBody, "visible", func(value string) string { return value }),
	)
	if err != nil {
		t.Fatal(err)
	}
	runtimeRow, err := RuntimeModelReadRowWithCounts(readPostModel,
		[]RuntimeReadCell{RuntimeToManyReadCell(readPostComments, []RuntimeModelRow{comment})},
		[]RuntimeRelationCountCell{RuntimePresentRelationCountCell(readPostComments, readRelation, 0)},
	)
	if err != nil {
		t.Fatal(err)
	}
	row, err := RuntimeTypedReadRow(readPostDescriptor, runtimeRow)
	if err != nil {
		t.Fatal(err)
	}
	if related, ok := Many(row, readPosts.Comments).Get(); !ok || len(related) != 1 {
		t.Fatalf("related=%v present=%t", related, ok)
	}
	count := RelationCount(row, readPosts.Comments)
	if value, ok := count.Get(); !ok || value != 0 || count.State() != ReadPresent {
		t.Fatalf("count=%d present=%t state=%d", value, ok, count.State())
	}
	if count := RelationCount(Row[readPost]{}, readPosts.Comments); count.State() != ReadUnselected {
		t.Fatalf("unselected count state=%d", count.State())
	}
}

func TestRuntimeOccurrenceRelationsAndCountsRemainIndependent(t *testing.T) {
	firstChild, err := RuntimeModelReadRow(readCommentModel, RuntimePresentReadCell(readCommentBody, "newest", nil))
	if err != nil {
		t.Fatal(err)
	}
	secondChild, err := RuntimeModelReadRow(readCommentModel, RuntimePresentReadCell(readCommentBody, "oldest", nil))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := RuntimeModelReadRowWithOccurrences(readPostModel, nil,
		[]RuntimeRelationCountCell{
			RuntimePresentRelationCountOccurrenceCell(readPostComments, readRelation, 3, 1),
			RuntimePresentRelationCountOccurrenceCell(readPostComments, readRelation, 4, 2),
		},
		[]RuntimeOccurrenceCell{
			RuntimeToManyOccurrenceCell(readPostComments, 1, []RuntimeModelRow{firstChild}),
			RuntimeToManyOccurrenceCell(readPostComments, 2, []RuntimeModelRow{secondChild}),
		})
	if err != nil {
		t.Fatal(err)
	}
	row, err := RuntimeTypedReadRow(readPostDescriptor, runtime)
	if err != nil {
		t.Fatal(err)
	}
	newest, ok := RuntimeOccurrenceToMany(row, readPosts.Comments, 1).Get()
	if !ok || len(newest) != 1 {
		t.Fatalf("newest = %#v/%v", newest, ok)
	}
	oldest, ok := RuntimeOccurrenceToMany(row, readPosts.Comments, 2).Get()
	if !ok || len(oldest) != 1 {
		t.Fatalf("oldest = %#v/%v", oldest, ok)
	}
	if value, _ := Value(newest[0], readComments.Body).Get(); value != "newest" {
		t.Fatalf("newest body = %q", value)
	}
	if value, _ := Value(oldest[0], readComments.Body).Get(); value != "oldest" {
		t.Fatalf("oldest body = %q", value)
	}
	if value, ok := RuntimeOccurrenceRelationCount(row, readPosts.Comments, 3).Get(); !ok || value != 1 {
		t.Fatalf("first count = %d/%v", value, ok)
	}
	if value, ok := RuntimeOccurrenceRelationCount(row, readPosts.Comments, 4).Get(); !ok || value != 2 {
		t.Fatalf("second count = %d/%v", value, ok)
	}
	if Many(row, readPosts.Comments).IsSelected() || RelationCount(row, readPosts.Comments).IsSelected() {
		t.Fatal("GraphQL occurrences leaked into the ordinary typed field slot")
	}
	if runtime.ModelID() != readPostModel {
		t.Fatalf("runtime model=%x", runtime.ModelID())
	}
	transportRows, present := RuntimeTransportOccurrence(runtime, readPostComments, 1).Get()
	children, typed := transportRows.([]RuntimeModelRow)
	if !present || !typed || len(children) != 1 || children[0].ModelID() != readCommentModel {
		t.Fatalf("transport occurrence=%#v present=%v typed=%v", transportRows, present, typed)
	}
	transportCount, present := RuntimeTransportRelationCount(runtime, readPostComments, readRelation, 4).Get()
	if !present || transportCount != int64(2) {
		t.Fatalf("transport count=%#v present=%v", transportCount, present)
	}
}

func TestRuntimeFreezeReadRequestRetainsIndependentRelationOccurrences(t *testing.T) {
	one, two := 1, 2
	childOne, err := RuntimeFreezeReadRequest(RuntimeReadRequestInput{
		Operation: ReadFindMany, Model: readCommentModel, Take: &one, Projection: ProjectionSelect,
		Selection: []RuntimeReadSelectionInput{{Kind: RuntimeReadScalar, Field: readCommentBody}},
	})
	if err != nil {
		t.Fatal(err)
	}
	childTwo, err := RuntimeFreezeReadRequest(RuntimeReadRequestInput{
		Operation: ReadFindMany, Model: readCommentModel, Take: &two, Projection: ProjectionSelect,
		Selection: []RuntimeReadSelectionInput{{Kind: RuntimeReadScalar, Field: readCommentBody}},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := RuntimeFreezeReadRequest(RuntimeReadRequestInput{
		Operation: ReadFindMany, Model: readPostModel, Projection: ProjectionSelect,
		Selection: []RuntimeReadSelectionInput{
			{Kind: RuntimeReadRelation, Field: readPostComments, Relation: readRelation, Target: readCommentModel, Occurrence: 1, Request: &childOne},
			{Kind: RuntimeReadRelation, Field: readPostComments, Relation: readRelation, Target: readCommentModel, Occurrence: 2, Request: &childTwo},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := request.Selection()
	if len(selection) != 2 || selection[0].OccurrenceID() != 1 || selection[1].OccurrenceID() != 2 {
		t.Fatalf("selection=%#v", selection)
	}
	first, _ := selection[0].Request()
	second, _ := selection[1].Request()
	firstTake, _ := first.Take()
	secondTake, _ := second.Take()
	if firstTake != 1 || secondTake != 2 {
		t.Fatalf("child takes=%d/%d", firstTake, secondTake)
	}
	if _, err := RuntimeFreezeReadRequest(RuntimeReadRequestInput{
		Operation: ReadFindMany, Model: readPostModel, Projection: ProjectionSelect,
		Selection: []RuntimeReadSelectionInput{{Kind: RuntimeReadScalar, Field: readPostTitle, Occurrence: 1}},
	}); err == nil {
		t.Fatal("scalar occurrence identity was accepted")
	}
}

func TestFreezeFindManyOwnsNestedTypedReadShape(t *testing.T) {
	if readPosts.ID.fieldIdentity() == (FieldID{}) {
		t.Fatalf("readPosts.ID was initialized with zero identity; source id=%x", readPostID)
	}
	if direct := readPosts.ID.readSelection(readPost{}); direct.field == (FieldID{}) {
		t.Fatalf("direct selection lost identity: %#v", direct)
	}
	projection := Select[readPost](
		readPosts.ID,
		readPosts.Title,
		readPosts.Comments.Args(
			Where(readComments.Live.Eq(true)),
			OrderBy(readComments.Body.Asc()),
			Take[readComment](5),
			Select[readComment](readComments.ID, readComments.Body),
		),
	)
	projectionNode := projection.readOption(readPost{})
	for index, selection := range projectionNode.selection {
		if selection.field == (FieldID{}) {
			t.Fatalf("projection selection %d has zero field: %#v", index, selection)
		}
	}
	request, err := FreezeFindMany(readPostDescriptor,
		Where(readPosts.Title.Contains("go")),
		OrderBy(readPosts.Title.Desc(), readPosts.ID.Asc()),
		Take[readPost](20),
		Skip[readPost](2),
		Distinct[readPost](readPosts.ID),
		projection,
	)
	if err != nil {
		var failure *Error
		if errors.As(err, &failure) {
			t.Fatalf("%v (model=%x field=%x operation=%s)", err, failure.Model, failure.Field, failure.Operation)
		}
		t.Fatal(err)
	}
	if request.Operation() != ReadFindMany || request.ModelID() != readPostModel {
		t.Fatalf("request operation/model=%d/%x", request.Operation(), request.ModelID())
	}
	if take, ok := request.Take(); !ok || take != 20 {
		t.Fatalf("take=%d ok=%t", take, ok)
	}
	if skip, ok := request.Skip(); !ok || skip != 2 {
		t.Fatalf("skip=%d ok=%t", skip, ok)
	}
	selection := request.Selection()
	if len(selection) != 3 || !selection[2].IsRelation() || selection[2].TargetModelID() != readCommentModel {
		t.Fatalf("selection=%#v", selection)
	}
	child, ok := selection[2].Request()
	if !ok || child.ModelID() != readCommentModel || len(child.Selection()) != 2 || len(child.OrderBy()) != 1 {
		t.Fatalf("child=%#v ok=%t", child, ok)
	}
	if _, ok := child.Where(); !ok {
		t.Fatal("nested relation where was not frozen")
	}
}

func TestFreezeRelationCountOwnsWhereOnlyChildAndCanCoexistWithRows(t *testing.T) {
	request, err := FreezeFindMany(readPostDescriptor, Select[readPost](
		readPosts.Comments.Select(readComments.ID),
		readPosts.Comments.Count(Where(readComments.Live.Eq(true))),
	))
	if err != nil {
		t.Fatal(err)
	}
	selection := request.Selection()
	if len(selection) != 2 || !selection[0].IsRelation() || !selection[1].IsRelationCount() {
		t.Fatalf("selection=%#v", selection)
	}
	child, ok := selection[1].Request()
	if !ok || child.Operation() != ReadCount || child.ModelID() != readCommentModel {
		t.Fatalf("count child=%#v present=%t", child, ok)
	}
	if _, ok := child.Where(); !ok {
		t.Fatal("count where was not frozen")
	}
	if _, err := FreezeFindMany(readPostDescriptor, Select[readPost](
		readPosts.Comments.Count(Take[readComment](1)),
	)); err == nil {
		t.Fatal("relation count accepted paging")
	}
}

func TestFreezeCursorIncludeAndOmitOwnTheirModes(t *testing.T) {
	cursorValue := GeneratedUniqueSelectorValue[readPost](readPostModel, readPostKey,
		GeneratedSelectorComponent(readPostID, UUID{1}),
	)
	request, err := FreezeFindMany(readPostDescriptor,
		Cursor(cursorValue),
		Include[readPost](readPosts.Comments),
		Omit[readPost](readPosts.Body, readPosts.Payload),
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.ProjectionMode() != ProjectionInclude {
		t.Fatalf("projection mode=%d", request.ProjectionMode())
	}
	if omitted := request.Omitted(); len(omitted) != 2 || omitted[0] != readPostBody || omitted[1] != readPostPayload {
		t.Fatalf("omitted=%x", omitted)
	}
	cursor, ok := request.Cursor()
	if !ok || cursor.Selector().KeyID() != readPostKey || len(cursor.Selector().Fields()) != 1 {
		t.Fatalf("cursor=%#v present=%t", cursor, ok)
	}
	if predicate := cursor.Predicate(); predicate.View().RootModelID() != readPostModel {
		t.Fatalf("cursor predicate model=%x", predicate.View().RootModelID())
	}
	selection := request.Selection()
	if len(selection) != 1 || !selection[0].IsRelation() {
		t.Fatalf("include selection=%#v", selection)
	}

	selected, err := FreezeFindMany(readPostDescriptor, Select[readPost](readPosts.ID))
	if err != nil || selected.ProjectionMode() != ProjectionSelect || len(selected.Omitted()) != 0 {
		t.Fatalf("selected=%#v err=%v", selected, err)
	}
}

func TestFreezeReadRequestRejectsStructuralAmbiguity(t *testing.T) {
	tests := []struct {
		name string
		run  func() error
	}{
		{"negative take without order", func() error {
			_, err := FreezeFindMany(readPostDescriptor, Take[readPost](-2))
			return err
		}},
		{"negative skip", func() error {
			_, err := FreezeFindMany(readPostDescriptor, Skip[readPost](-1))
			return err
		}},
		{"duplicate projection", func() error {
			_, err := FreezeFindMany(readPostDescriptor, Select[readPost](readPosts.ID, readPosts.ID))
			return err
		}},
		{"relation without generated target", func() error {
			relation := GeneratedToMany[readPost, readComment](readPostComments, readRelation)
			_, err := FreezeFindMany(readPostDescriptor, Select[readPost](relation.Select(readComments.ID)))
			return err
		}},
		{"count projection", func() error {
			_, err := FreezeCount(readPostDescriptor, Select[readPost](readPosts.ID))
			return err
		}},
		{"select and include", func() error {
			_, err := FreezeFindMany(readPostDescriptor, Select[readPost](readPosts.ID), Include[readPost](readPosts.Comments.Select(readComments.ID)))
			return err
		}},
		{"select and omit", func() error {
			_, err := FreezeFindMany(readPostDescriptor, Select[readPost](readPosts.ID), Omit[readPost](readPosts.Body))
			return err
		}},
		{"empty include", func() error {
			_, err := FreezeFindMany(readPostDescriptor, Include[readPost]())
			return err
		}},
		{"cursor on count", func() error {
			selector := GeneratedUniqueSelectorValue[readPost](readPostModel, readPostKey, GeneratedSelectorComponent(readPostID, UUID{1}))
			_, err := FreezeCount(readPostDescriptor, Cursor(selector))
			return err
		}},
		{"empty where", func() error {
			_, err := FreezeFindMany(readPostDescriptor, Where(Predicate[readPost]{}))
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var failure *Error
			if err := test.run(); !errors.As(err, &failure) || failure.Code != CodeBadUserInput {
				t.Fatalf("error=%#v", err)
			}
		})
	}
}

func TestRuntimeReadRowRejectsForgedCells(t *testing.T) {
	if _, err := RuntimeReadRow(readPostDescriptor, RuntimeNullReadCell(FieldID{})); err == nil {
		t.Fatal("zero field cell was accepted")
	}
	if _, err := RuntimeReadRow(readPostDescriptor, RuntimeNullReadCell(readPostID), RuntimeNullReadCell(readPostID)); err == nil {
		t.Fatal("duplicate field cell was accepted")
	}
	if _, err := RuntimeReadRow(ModelDescriptor[readPost]{}, RuntimeNullReadCell(readPostID)); err == nil {
		t.Fatal("zero model descriptor was accepted")
	}
}
