package golem

import (
	"context"
	"testing"
)

func TestRuntimeReadHookBridgePreservesOccurrencesAndTypedHooks(t *testing.T) {
	relation := readPosts.Comments.Args(Select[readComment](readComments.Body)).readSelection(readPost{})
	relation.occurrence = 9
	request, err := FreezeFindMany(readPostDescriptor, readOptionValue[readPost]{node: readOptionNode{
		kind:      readOptionSelect,
		selection: []readSelectionNode{readPosts.ID.readSelection(readPost{}), relation},
	}})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := RuntimeReadHookRequestFromFrozen(request)
	if err != nil {
		t.Fatal(err)
	}

	beforeCalls, afterCalls := 0, 0
	digest := SchemaDigest{1}
	hooks := []HookBinding[bindingActor]{
		GeneratedBeforeHookBinding[bindingActor, readPost, FindManyHookRequest[readPost]](readPostModel, HookFindMany, func(_ context.Context, request *FindManyHookRequest[readPost]) error {
			beforeCalls++
			request.AppendOptions(Take[readPost](3))
			return nil
		}),
		GeneratedAfterHookBinding[bindingActor, readPost, FindManyHookResult[readPost]](readPostModel, HookFindMany, func(_ context.Context, result FindManyHookResult[readPost]) error {
			afterCalls++
			rows := result.Rows()
			if len(rows) != 1 {
				t.Fatalf("hook rows=%d", len(rows))
			}
			children, ok := RuntimeOccurrenceToMany(rows[0], readPosts.Comments, 9).Get()
			if !ok || len(children) != 1 {
				t.Fatalf("hook occurrence rows=%d present=%v", len(children), ok)
			}
			if body, present := Value(children[0], readComments.Body).Get(); !present || body != "visible" {
				t.Fatalf("hook child body=%q present=%v", body, present)
			}
			return nil
		}),
	}
	bindings, err := GeneratedApplicationBindings(digest, GeneratedStampedPackageBindings[bindingActor](digest, nil, hooks))
	if err != nil {
		t.Fatal(err)
	}
	validated := 0
	transformed, err := RuntimeInvokeReadBeforeHooks(context.Background(), bindings, envelope, func(value RuntimeReadHookRequest) error {
		validated++
		if value.ModelID() != readPostModel || value.Operation() != HookFindMany {
			t.Fatalf("transformed envelope=%x/%s", value.ModelID(), value.Operation())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if beforeCalls != 1 || validated != 1 {
		t.Fatalf("before calls=%d validations=%d", beforeCalls, validated)
	}
	frozen := transformed.Request()
	if take, ok := frozen.Take(); !ok || take != 3 {
		t.Fatalf("transformed take=%d present=%v", take, ok)
	}
	selection := frozen.Selection()
	if len(selection) != 2 || selection[1].OccurrenceID() != 9 {
		t.Fatalf("transformed selection=%#v", selection)
	}

	child, err := RuntimeModelReadRow(readCommentModel, RuntimePresentReadCell(readCommentBody, "visible", nil))
	if err != nil {
		t.Fatal(err)
	}
	row, err := RuntimeModelReadRowWithOccurrences(readPostModel, nil, nil, []RuntimeOccurrenceCell{
		RuntimeToManyOccurrenceCell(readPostComments, 9, []RuntimeModelRow{child}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := RuntimeInvokeReadResultHooks(context.Background(), bindings, RuntimeReadHookRows(transformed, []RuntimeModelRow{row}, true)); err != nil {
		t.Fatal(err)
	}
	if afterCalls != 1 {
		t.Fatalf("after calls=%d", afterCalls)
	}
}

func TestRuntimeReadHookBridgeRefusesWrongTypedPayload(t *testing.T) {
	request, err := FreezeFindMany(readPostDescriptor, Select[readPost](readPosts.ID))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := RuntimeReadHookRequestFromFrozen(request)
	if err != nil {
		t.Fatal(err)
	}
	digest := SchemaDigest{2}
	hook := GeneratedBeforeHookBinding[bindingActor, readPost, CreateHookRequest[readPost]](readPostModel, HookFindMany, func(context.Context, *CreateHookRequest[readPost]) error { return nil })
	bindings, err := GeneratedApplicationBindings(digest, GeneratedStampedPackageBindings[bindingActor](digest, nil, []HookBinding[bindingActor]{hook}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RuntimeInvokeReadBeforeHooks(context.Background(), bindings, envelope, func(RuntimeReadHookRequest) error { return nil }); err == nil {
		t.Fatal("wrong generated read hook payload was accepted")
	}
}
