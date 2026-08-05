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
