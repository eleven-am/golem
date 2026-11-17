package golem

import (
	"bytes"
	"context"
	"fmt"
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

func TestRuntimeReadHookBridgePreservesIdentityHydration(t *testing.T) {
	base, err := FreezeFindMany(readPostDescriptor, Select[readPost](readPosts.Title), Take[readPost](1))
	if err != nil {
		t.Fatal(err)
	}
	identity, err := RuntimeNewRowIdentity([]FieldID{readPostID}, []RuntimeReadCell{RuntimePresentReadCell(readPostID, UUID{15: 7}, nil)})
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := RuntimeIdentityHydrationRequest(base, []RuntimeRowIdentity{identity})
	if err != nil {
		t.Fatal(err)
	}
	correlation, _, _ := lookup.RuntimeIdentityCorrelation()
	expectedWhere, err := readPosts.Title.Eq("hook-visible").Freeze(readPostDescriptor)
	if err != nil {
		t.Fatal(err)
	}
	for _, rewrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("rewrite=%v", rewrite), func(t *testing.T) {
			calls, validated := 0, 0
			first := GeneratedBeforeHookBinding[bindingActor, readPost, FindManyHookRequest[readPost]](readPostModel, HookFindMany, func(_ context.Context, request *FindManyHookRequest[readPost]) error {
				calls++
				public, err := FreezeFindMany(readPostDescriptor, request.Options()...)
				if err != nil {
					return err
				}
				if _, _, present := public.RuntimeIdentityCorrelation(); present {
					return fmt.Errorf("private identities became public hook options")
				}
				if _, present := public.Where(); present {
					return fmt.Errorf("private identities became a hook-visible filter")
				}
				if rewrite {
					request.ReplaceOptions(Select[readPost](readPosts.Title), Take[readPost](1), Where(readPosts.Title.Eq("hook-visible")))
				}
				return nil
			})
			second := GeneratedBeforeHookBinding[bindingActor, readPost, FindManyHookRequest[readPost]](readPostModel, HookFindMany, func(_ context.Context, request *FindManyHookRequest[readPost]) error {
				calls++
				public, err := FreezeFindMany(readPostDescriptor, request.Options()...)
				if err != nil {
					return err
				}
				if _, _, present := public.RuntimeIdentityCorrelation(); present {
					return fmt.Errorf("private identities escaped into second hook")
				}
				return nil
			})
			digest := SchemaDigest{31}
			bindings, err := GeneratedApplicationBindings(digest, GeneratedStampedPackageBindings[bindingActor](digest, nil, []HookBinding[bindingActor]{first, second}))
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := RuntimeReadHookRequestFromFrozen(lookup)
			if err != nil {
				t.Fatal(err)
			}
			result, err := RuntimeInvokeReadBeforeHooks(context.Background(), bindings, envelope, func(value RuntimeReadHookRequest) error {
				validated++
				current := value.Request()
				got, fields, present := current.RuntimeIdentityCorrelation()
				if !present || !bytes.Equal(got.CanonicalBytes(), correlation.CanonicalBytes()) || len(fields) != 1 || fields[0] != readPostID {
					return fmt.Errorf("typed before hook dropped or changed authoritative identity hydration")
				}
				if take, present := current.Take(); !present || take != 1 {
					return fmt.Errorf("identity lookup window changed")
				}
				where, present := current.Where()
				if present != rewrite || present && !bytes.Equal(where.CanonicalBytes(), expectedWhere.CanonicalBytes()) {
					return fmt.Errorf("hook filter did not remain separate caller authority")
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 2 || validated != 2 {
				t.Fatalf("calls=%d validations=%d", calls, validated)
			}
			if _, _, present := result.Request().RuntimeIdentityCorrelation(); !present {
				t.Fatal("final handoff lost identity correlation")
			}
		})
	}
}

func TestRuntimeReadHookBridgePreservesNestedIdentityOccurrences(t *testing.T) {
	first := readPosts.Comments.Args(Select[readComment](readComments.Body), Take[readComment](1)).readSelection(readPost{})
	second := first
	first.occurrence = 9
	second.occurrence = 10
	root, err := FreezeFindMany(readPostDescriptor, readOptionValue[readPost]{node: readOptionNode{kind: readOptionSelect, selection: []readSelectionNode{first, second}}})
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[RuntimeOccurrenceID][]byte{}
	for index := range root.selection {
		child, _ := root.selection[index].Request()
		identity, err := RuntimeNewRowIdentity([]FieldID{readCommentID}, []RuntimeReadCell{RuntimePresentReadCell(readCommentID, UUID{15: byte(index + 1)}, nil)})
		if err != nil {
			t.Fatal(err)
		}
		child, err = RuntimeIdentityHydrationRequest(child, []RuntimeRowIdentity{identity})
		if err != nil {
			t.Fatal(err)
		}
		root.selection[index].request = &child
		correlation, _, _ := child.RuntimeIdentityCorrelation()
		wanted[root.selection[index].occurrence] = correlation.CanonicalBytes()
	}
	bridge := generatedReadBeforeBridge[readPost, FindManyHookRequest[readPost]](HookFindMany, func(_ context.Context, request *FindManyHookRequest[readPost]) error {
		// Preserve each selected occurrence while adding an ordinary filter to
		// the containing query. The two child identity sets must not merge.
		request.AppendOptions(Where(readPosts.Title.Eq("hook-visible")))
		return nil
	})
	envelope, err := RuntimeReadHookRequestFromFrozen(root)
	if err != nil {
		t.Fatal(err)
	}
	result, err := bridge(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range result.Request().Selection() {
		child, present := selection.Request()
		if !present {
			t.Fatal("nested request lost")
		}
		correlation, fields, present := child.RuntimeIdentityCorrelation()
		if !present || len(fields) != 1 || fields[0] != readCommentID || !bytes.Equal(correlation.CanonicalBytes(), wanted[selection.OccurrenceID()]) {
			t.Fatalf("private hydration lost or crossed occurrence %d", selection.OccurrenceID())
		}
		if _, present := child.Where(); present {
			t.Fatal("child identity became caller where")
		}
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
