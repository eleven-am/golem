package runtime

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	readplan "github.com/eleven-am/golem/go/internal/read/plan"
	"github.com/eleven-am/golem/go/internal/subscription"
)

func dependencyCommentPolicies() deleteEvalPolicies {
	return deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postTitle.Eq("published")), true
		},
	}
}

func dependencyPostPolicies() deleteEvalPolicies {
	return deleteEvalPolicies{
		post: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalPost], bool) {
			return fields.postComments.Some(fields.commentBody.Eq("hello")), true
		},
	}
}

func expectStreamEndsWithReason(t *testing.T, stream golem.EventStream[p7EventOracleValue], code events.ErrorCode, reason events.Reason) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	value, err := stream.Recv(ctx)
	if err == nil {
		t.Fatalf("stream delivered %x instead of ending with %s", value.validated.Metadata().EventID(), code)
	}
	gotCode, _ := events.CodeOf(err)
	gotReason, _ := events.ReasonOf(err)
	if gotCode != code || gotReason != reason {
		t.Fatalf("stream ended with %v (code %q reason %q), want %s %q", err, gotCode, gotReason, code, reason)
	}
}

func dependencyCaller(t *testing.T, fixture deleteEvalFixture, actor string) *Caller[deleteEvalPrincipal, deleteEvalActor] {
	t.Helper()
	caller, err := fixture.app.ForPrincipal(context.Background(), deleteEvalPrincipal{Name: actor})
	if err != nil {
		t.Fatal(err)
	}
	return caller
}

func TestDependencyDerivationNamesModelsFieldsAndMembership(t *testing.T) {
	cases := map[string]struct {
		policies deleteEvalPolicies
		model    func(deleteEvalFixture) golem.ModelID
		want     func(deleteEvalFixture) map[golem.ModelID]struct {
			fields     []golem.FieldID
			membership bool
		}
	}{
		"to-one hop": {
			policies: dependencyCommentPolicies(),
			model:    func(f deleteEvalFixture) golem.ModelID { return f.schema.Comment },
			want: func(f deleteEvalFixture) map[golem.ModelID]struct {
				fields     []golem.FieldID
				membership bool
			} {
				return map[golem.ModelID]struct {
					fields     []golem.FieldID
					membership bool
				}{f.schema.Post: {fields: []golem.FieldID{f.schema.PostID, f.schema.PostTitle}, membership: false}}
			},
		},
		"to-one chain": {
			policies: deleteEvalPolicies{comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
				return fields.commentPost.Is(fields.postAuthor.Is(fields.userName.Eq("alice"))), true
			}},
			model: func(f deleteEvalFixture) golem.ModelID { return f.schema.Comment },
			want: func(f deleteEvalFixture) map[golem.ModelID]struct {
				fields     []golem.FieldID
				membership bool
			} {
				return map[golem.ModelID]struct {
					fields     []golem.FieldID
					membership bool
				}{
					f.schema.Post: {fields: []golem.FieldID{f.schema.PostID, f.schema.AuthorID}, membership: false},
					f.schema.User: {fields: []golem.FieldID{f.schema.UserID, f.schema.UserName}, membership: false},
				}
			},
		},
		"to-many hop": {
			policies: dependencyPostPolicies(),
			model:    func(f deleteEvalFixture) golem.ModelID { return f.schema.Post },
			want: func(f deleteEvalFixture) map[golem.ModelID]struct {
				fields     []golem.FieldID
				membership bool
			} {
				return map[golem.ModelID]struct {
					fields     []golem.FieldID
					membership bool
				}{f.schema.Comment: {fields: []golem.FieldID{f.schema.CommentBody, f.schema.CommentPostID}, membership: true}}
			},
		},
		"no hop": {
			policies: deleteEvalPolicies{},
			model:    func(f deleteEvalFixture) golem.ModelID { return f.schema.Comment },
			want: func(deleteEvalFixture) map[golem.ModelID]struct {
				fields     []golem.FieldID
				membership bool
			} {
				return nil
			},
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			runDeleteEvalProfiles(t, test.policies, func(t *testing.T, fixture deleteEvalFixture) {
				caller := dependencyCaller(t, fixture, "alice")
				model := test.model(fixture)
				row, filter, err := readplan.CallerReach(caller.policies, policyir.ModelID(model), nil, fixture.app.readLimits.plan.MaxRelationDepth)
				if err != nil {
					t.Fatal(err)
				}
				conditions := []policyir.Condition{row}
				if filter != nil {
					conditions = append(conditions, *filter)
				}
				found, err := fixture.app.eventDependencies(model, conditions...)
				if err != nil {
					t.Fatal(err)
				}
				want := test.want(fixture)
				if len(found) != len(want) {
					t.Fatalf("dependencies = %+v, want %d models", found, len(want))
				}
				for _, item := range found {
					expected, present := want[item.model]
					if !present {
						t.Fatalf("unexpected dependency on %x", item.model)
					}
					if item.membership != expected.membership {
						t.Fatalf("model %x membership = %t, want %t", item.model, item.membership, expected.membership)
					}
					have := make(map[golem.FieldID]bool)
					for _, field := range item.fields {
						have[field] = true
					}
					for _, field := range expected.fields {
						if !have[field] {
							t.Fatalf("model %x does not watch field %x: %v", item.model, field, item.fields)
						}
					}
					if len(item.fields) != len(expected.fields) {
						t.Fatalf("model %x watches %d fields, want %d: %v", item.model, len(item.fields), len(expected.fields), item.fields)
					}
				}
			})
		})
	}
}

func TestDependencyDerivationIncludesTheFilterAReadHookAdds(t *testing.T) {
	policies := deleteEvalPolicies{
		hook: func(fields deleteEvalFields) golem.Predicate[deleteEvalComment] {
			return fields.commentPost.Is(fields.postTitle.Eq("published"))
		},
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		caller := dependencyCaller(t, fixture, "alice")
		state := &callerEventSubscription[deleteEvalPrincipal]{model: fixture.schema.Comment, readRequest: mustFrozenCommentRead(t, fixture)}
		conditions, err := fixture.app.eventDependencyConditions(context.Background(), caller, state)
		if err != nil {
			t.Fatal(err)
		}
		found, err := fixture.app.eventDependencies(fixture.schema.Comment, conditions...)
		if err != nil {
			t.Fatal(err)
		}
		if len(found) != 1 || found[0].model != fixture.schema.Post {
			t.Fatalf("dependencies = %+v, want only the post the hook's filter reaches", found)
		}
	})
}

func TestDependencyRelationEndpointRefusesFieldsThatAreNotRelations(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		for name, field := range map[string]golem.FieldID{"absent field": {0xee}, "scalar field": fixture.schema.PostTitle} {
			_, err := fixture.app.eventRelationEndpoint(fixture.schema.Post, field)
			if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionInvalid {
				t.Fatalf("%s: error = %v, want %s", name, err, events.CodeSubscriptionInvalid)
			}
		}
		if _, err := fixture.app.eventRelationEndpoint(fixture.schema.Post, fixture.schema.PostComments); err != nil {
			t.Fatalf("a relation field was refused: %v", err)
		}
		if _, err := fixture.app.eventRelationEndpoint(fixture.schema.Comment, fixture.schema.PostComments); err == nil {
			t.Fatal("a relation field of another model was accepted")
		}
	})
}

func TestDependencyTransportLossEndsTheDependentStream(t *testing.T) {
	for name, test := range map[string]struct {
		failure error
		code    events.ErrorCode
	}{
		"transient": {events.Failure(events.CodeEventTransport), events.CodeSubscriptionResync},
		"terminal":  {errors.New("transport closed"), events.CodeSubscriptionSourceClosed},
	} {
		t.Run(name, func(t *testing.T) {
			runDeleteEvalProfiles(t, dependencyCommentPolicies(), func(t *testing.T, fixture deleteEvalFixture) {
				fixture.transport.fail(fixture.schema.Post, test.failure)
				stream := fixture.subscribeComments(t, "alice")
				expectStreamEndsWithReason(t, stream, test.code, "")
			})
		})
	}
}

func TestDependencySnapshotReadFailureEndsTheDependentStreamWithRevalidation(t *testing.T) {
	runTracedEventProfiles(t, dependencyCommentPolicies(), func(t *testing.T, fixture deleteEvalFixture, trace *p5SQLTrace) {
		stream := fixture.subscribeComments(t, "alice")
		var consumed atomic.Bool
		trace.setFault(func(statement string) error {
			if strings.Contains(statement, `"posts"`) && consumed.CompareAndSwap(false, true) {
				return errors.New("injected read fault")
			}
			return nil
		})
		defer trace.setFault(nil)
		fixture.publishPostEvent(t, golem.EventUpdated, 1, deleteEvalUUID(deleteEvalPublished), deleteEvalUUID(deleteEvalAlice), "published")
		expectStreamEndsWithReason(t, stream, events.CodeSubscriptionRevalidation, "")
	})
}

func TestDependencyThroughToManyEndsOnCreateAndDeleteAndWhenTheVerdictIsUnknown(t *testing.T) {
	hello := func(fixture deleteEvalFixture) []mutationdecode.Cell {
		body, err := policyir.StringValue("hello")
		if err != nil {
			t.Fatal(err)
		}
		return []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(fixture.schema.CommentID), policyir.UUIDValue(deleteEvalUUID(deleteEvalOnPub))),
			mutationdecode.Value(policyir.FieldID(fixture.schema.CommentPostID), policyir.UUIDValue(deleteEvalUUID(deleteEvalPublished))),
			mutationdecode.Value(policyir.FieldID(fixture.schema.CommentBody), body),
		}
	}
	unreadable := func() deleteEvalPolicies {
		policies := dependencyPostPolicies()
		policies.comment = func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPID.Eq(deleteEvalUUID(deleteEvalDraft)), true
		}
		return policies
	}
	for name, test := range map[string]struct {
		policies func() deleteEvalPolicies
		limit    int
		publish  func(deleteEvalFixture)
		ends     bool
	}{
		"created": {policies: dependencyPostPolicies, ends: true, publish: func(f deleteEvalFixture) {
			f.publishCommentEvent(t, golem.EventCreated, 1, deleteEvalUUID(deleteEvalOnPub), deleteEvalUUID(deleteEvalPublished), "hello")
		}},
		"deleted": {policies: dependencyPostPolicies, ends: true, publish: func(f deleteEvalFixture) {
			f.publishDelete(t, f.schema.Comment, f.schema.CommentID, 2, hello(f))
		}},
		"deleted row the subscriber cannot read": {policies: unreadable, publish: func(f deleteEvalFixture) {
			f.publishDelete(t, f.schema.Comment, f.schema.CommentID, 3, hello(f))
		}},
		"created row the subscriber cannot read": {policies: unreadable, publish: func(f deleteEvalFixture) {
			f.publishCommentEvent(t, golem.EventCreated, 4, deleteEvalUUID(deleteEvalOnPub), deleteEvalUUID(deleteEvalPublished), "hello")
		}},
		"deleted with a dependency the judge cannot plan": {policies: unreadable, limit: 2, ends: true, publish: func(f deleteEvalFixture) {
			f.publishDelete(t, f.schema.Comment, f.schema.CommentID, 5, hello(f))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			runDeleteEvalProfiles(t, test.policies(), func(t *testing.T, fixture deleteEvalFixture) {
				if test.limit != 0 {
					fixture.app.readLimits.plan.MaxSelected = test.limit
				}
				stream := fixture.subscribeActor(t, fixture.schema.Post, "u0")
				test.publish(fixture)
				if test.ends {
					expectStreamEndsWithReason(t, stream, events.CodeSubscriptionResync, events.ReasonDependencyChanged)
					return
				}
				expectDeleteSilent(t, stream)
			})
		})
	}
}

func TestDependencyThroughToOneIgnoresCreatesAndDeletes(t *testing.T) {
	runDeleteEvalProfiles(t, dependencyCommentPolicies(), func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		title, err := policyir.StringValue("published")
		if err != nil {
			t.Fatal(err)
		}
		cells := []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue(deleteEvalUUID(deleteEvalPublished))),
			mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue(deleteEvalUUID(deleteEvalAlice))),
			mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), title),
		}
		fixture.publishRowEvent(t, golem.EventCreated, 1, fixture.schema.Post, cells, fixture.schema.PostID)
		fixture.publishDelete(t, fixture.schema.Post, fixture.schema.PostID, 2, cells)
		expectDeleteSilent(t, stream)
	})
}

func TestDependencyUpdateOutsideTheWatchedFieldsIsIgnored(t *testing.T) {
	policies := deleteEvalPolicies{
		comment: func(fields deleteEvalFields, _ deleteEvalActor) (golem.Predicate[deleteEvalComment], bool) {
			return fields.commentPost.Is(fields.postAuthorID.Eq(deleteEvalUUID(deleteEvalAlice))), true
		},
	}
	runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
		stream := fixture.subscribeComments(t, "alice")
		fixture.publishPostEvent(t, golem.EventUpdated, 1, deleteEvalUUID(deleteEvalPublished), deleteEvalUUID(deleteEvalAlice), "published")
		expectDeleteSilent(t, stream)
	})
}

func TestDependencyOpenRefusals(t *testing.T) {
	t.Run("a read hook that fails at open", func(t *testing.T) {
		var failing atomic.Bool
		policies := dependencyCommentPolicies()
		policies.hook = func(deleteEvalFields) golem.Predicate[deleteEvalComment] { return golem.All[deleteEvalComment]() }
		policies.hookFails = &failing
		runDeleteEvalProfiles(t, policies, func(t *testing.T, fixture deleteEvalFixture) {
			failing.Store(true)
			caller := dependencyCaller(t, fixture, "alice")
			stream, err := CallerEvents[deleteEvalPrincipal, deleteEvalActor, deleteEvalComment, p7EventOracleValue](context.Background(), caller, fixture.comments)
			if stream != nil {
				_ = stream.Close()
			}
			if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionRevalidation {
				t.Fatalf("open error = %v, want %s", err, events.CodeSubscriptionRevalidation)
			}
		})
	})
	t.Run("a reach that cannot be expanded", func(t *testing.T) {
		runDeleteEvalProfiles(t, dependencyCommentPolicies(), func(t *testing.T, fixture deleteEvalFixture) {
			caller := dependencyCaller(t, fixture, "alice")
			state := &callerEventSubscription[deleteEvalPrincipal]{model: fixture.schema.Comment, readRequest: mustFrozenCommentRead(t, fixture)}
			fixture.app.readLimits.plan.MaxRelationDepth = -1
			_, err := fixture.app.eventDependencyConditions(context.Background(), caller, state)
			if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionRevalidation {
				t.Fatalf("conditions error = %v, want %s", err, events.CodeSubscriptionRevalidation)
			}
		})
	})
	t.Run("a dependency that cannot subscribe", func(t *testing.T) {
		runDeleteEvalProfiles(t, dependencyCommentPolicies(), func(t *testing.T, fixture deleteEvalFixture) {
			ctx, cancel := context.WithCancel(context.Background())
			caller := dependencyCaller(t, fixture, "alice")
			cancel()
			stream, err := CallerEvents[deleteEvalPrincipal, deleteEvalActor, deleteEvalComment, p7EventOracleValue](ctx, caller, fixture.comments)
			if stream != nil {
				_ = stream.Close()
			}
			if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionInvalid {
				t.Fatalf("open error = %v, want %s", err, events.CodeSubscriptionInvalid)
			}
		})
	})
}

func mustFrozenCommentRead(t *testing.T, fixture deleteEvalFixture) golem.FrozenReadRequest {
	t.Helper()
	metadata, present := fixture.app.eventRegistry.Lookup(fixture.schema.Comment)
	if !present {
		t.Fatal("comment event metadata is absent")
	}
	request, _, err := eventReadRequest(fixture.app.registry, mustFrozenEventRequest(t, fixture), metadata)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func mustFrozenEventRequest(t *testing.T, fixture deleteEvalFixture) golem.FrozenEventRequest {
	t.Helper()
	request, err := golem.RuntimeFreezeEventOptions(fixture.comments)
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func TestDependencyChangedRefusesAnUnknownAction(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		metadata, _ := fixture.app.eventRegistry.Lookup(fixture.schema.Post)
		member := &eventDependencyMember{metadata: metadata, membership: true}
		changed, err := fixture.app.dependencyChanged(member, &eventNoticeShared{})
		if code, ok := events.CodeOf(err); changed || !ok || code != events.CodeSubscriptionSourceClosed {
			t.Fatalf("changed=%t error=%v, want %s", changed, err, events.CodeSubscriptionSourceClosed)
		}
	})
}

func TestEvaluateDependencyRefusesNoticesItCannotTrust(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		metadata, _ := fixture.app.eventRegistry.Lookup(fixture.schema.Post)
		member := &eventDependencyMember{metadata: metadata}
		for name, shared := range map[string]any{
			"absent":       nil,
			"another type": "shared",
			"failed":       &eventNoticeShared{failed: true},
			"other schema": &eventNoticeShared{},
			"typed nil":    (*eventNoticeShared)(nil),
		} {
			_, err := fixture.app.evaluateDependency(member, shared)
			if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionSourceClosed {
				t.Fatalf("%s: error = %v, want %s", name, err, events.CodeSubscriptionSourceClosed)
			}
		}
	})
}

func TestDependencyBeforeImageAndVerdictAreUnknownWithoutAnIdentityOrAJudge(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		metadata, _ := fixture.app.eventRegistry.Lookup(fixture.schema.Post)
		row, err := policyir.NewConstant(policyir.ModelID(fixture.schema.Post), true)
		if err != nil {
			t.Fatal(err)
		}
		member := &eventDependencyMember{metadata: metadata, judge: &eventJudgeSpec{row: row}}
		evaluator, imaged := fixture.app.dependencyBeforeImage(member, mutationfact.Envelope{})
		if evaluator != nil || imaged {
			t.Fatalf("a fact with no identity produced an evaluator %v (imaged %t)", evaluator, imaged)
		}
		if verdict := dependencyVerdict(member, evaluator); verdict != deleteUnknown {
			t.Fatalf("verdict without an evaluator = %v, want unknown", verdict)
		}
		if verdict := dependencyVerdict(&eventDependencyMember{metadata: metadata}, &deleteEvaluator{}); verdict != deleteUnknown {
			t.Fatalf("verdict without a judge = %v, want unknown", verdict)
		}
		if verdict := dependencyVerdict(member, &deleteEvaluator{model: policyir.ModelID(fixture.schema.Post)}); verdict != deleteTrue {
			t.Fatalf("verdict of a constant-true rule = %v, want true", verdict)
		}
	})
}

func TestDependencyFieldsDifferComparesTheBeforeImageWithTheCurrentRow(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		registry := fixture.schema.Registry
		title := func(value string) policyir.Value {
			result, err := policyir.StringValue(value)
			if err != nil {
				t.Fatal(err)
			}
			return result
		}
		cells := func(value string) []mutationdecode.Cell {
			return withTestRowVersion(registry, fixture.schema.Post, []mutationdecode.Cell{
				mutationdecode.Value(policyir.FieldID(fixture.schema.PostID), policyir.UUIDValue(deleteEvalUUID(deleteEvalPublished))),
				mutationdecode.Value(policyir.FieldID(fixture.schema.AuthorID), policyir.UUIDValue(deleteEvalUUID(deleteEvalAlice))),
				mutationdecode.Value(policyir.FieldID(fixture.schema.PostTitle), title(value)),
			})
		}
		before := make(map[policyir.FieldID]mutationdecode.Cell)
		for _, cell := range cells("published") {
			before[cell.FieldID()] = cell
		}
		snapshot := func(value string, drop ...golem.FieldID) *eventSnapshot {
			byField := make(map[policyir.FieldID]mutationdecode.Cell)
			for _, cell := range cells(value) {
				byField[cell.FieldID()] = cell
			}
			for _, field := range drop {
				delete(byField, policyir.FieldID(field))
			}
			return &eventSnapshot{found: true, evaluator: &deleteEvaluator{cells: byField}}
		}
		watch := []golem.FieldID{fixture.schema.PostTitle}
		for name, test := range map[string]struct {
			watch    []golem.FieldID
			snapshot *eventSnapshot
			want     bool
		}{
			"no snapshot":                {watch, nil, true},
			"row no longer present":      {watch, &eventSnapshot{}, true},
			"watched field unchanged":    {watch, snapshot("published"), false},
			"unwatched field changed":    {[]golem.FieldID{fixture.schema.AuthorID}, snapshot("edited"), false},
			"watched field changed":      {watch, snapshot("edited"), true},
			"before image lacks field":   {[]golem.FieldID{{0xee}}, snapshot("published"), true},
			"current row lacks field":    {watch, snapshot("published", fixture.schema.PostTitle), true},
			"no watched fields at all":   {nil, snapshot("edited"), false},
			"identity and title changed": {[]golem.FieldID{fixture.schema.PostID, fixture.schema.PostTitle}, snapshot("edited"), true},
		} {
			if got := dependencyFieldsDiffer(test.watch, before, test.snapshot); got != test.want {
				t.Fatalf("%s: differ = %t, want %t", name, got, test.want)
			}
		}
	})
}

func TestDependentsHoldTheFirstFailureUntilTheParentAttaches(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		first := events.FailureWithReason(events.CodeSubscriptionResync, events.ReasonDependencyChanged, "")
		dependents := &eventDependents{}
		dependents.end(first)
		dependents.end(events.Failure(events.CodeSubscriptionRevalidation))
		parent := fixture.dependencyStream(t, "parent")
		dependents.attach(parent)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := parent.Recv(ctx); !errors.Is(err, first) {
			t.Fatalf("parent ended with %v, want the first failure", err)
		}
		late := fixture.dependencyStream(t, "late")
		attached := &eventDependents{}
		attached.attach(late)
		attached.end(first)
		if _, err := late.Recv(ctx); !errors.Is(err, first) {
			t.Fatalf("an attached parent ended with %v, want the failure", err)
		}
	})
}

func TestDependentsCloseEveryDependencyStreamOnce(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		dependents := &eventDependents{}
		one, two := fixture.dependencyStream(t, "one"), fixture.dependencyStream(t, "two")
		dependents.streams = []*subscription.Stream[any]{one, two}
		dependents.closeAll()
		dependents.closeAll()
		for name, stream := range map[string]*subscription.Stream[any]{"one": one, "two": two} {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			_, err := stream.Recv(ctx)
			cancel()
			if code, ok := events.CodeOf(err); !ok || code != events.CodeSubscriptionCancelled || errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("%s ended with %v, want %s from the close", name, err, events.CodeSubscriptionCancelled)
			}
		}
	})
}

func TestDroppedDependencyEndsItsParentWithTheDependencysFailure(t *testing.T) {
	runDeleteEvalProfiles(t, deleteEvalPolicies{}, func(t *testing.T, fixture deleteEvalFixture) {
		parent := fixture.dependencyStream(t, "parent")
		owner := &eventDependents{}
		owner.attach(parent)
		failure := events.Failure(events.CodeSubscriptionRevalidation)
		(&eventDependencyMember{owner: owner}).dropped(nil, failure)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, err := parent.Recv(ctx); !errors.Is(err, failure) {
			t.Fatalf("parent ended with %v, want the dependency's failure", err)
		}
	})
}

func (fixture deleteEvalFixture) dependencyStream(t *testing.T, name string) *subscription.Stream[any] {
	t.Helper()
	metadata, present := fixture.app.eventRegistry.Lookup(fixture.schema.Post)
	if !present {
		t.Fatal("post event metadata is absent")
	}
	hub, err := fixture.app.eventHub(fixture.schema.Post)
	if err != nil {
		t.Fatal(err)
	}
	key, err := fixture.app.eventSubscriberKey(fixture.schema.Post, metadata, nil, readplan.Plan{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stream, err := hub.SubscribeWithState(ctx, key, &eventDependencyMember{metadata: metadata}, nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return stream
}

func TestInvalidatePoliciesEndsEveryEventStreamWithResyncAndWorksWithoutACache(t *testing.T) {
	runDeleteEvalProfiles(t, dependencyCommentPolicies(), func(t *testing.T, fixture deleteEvalFixture) {
		dependent := fixture.subscribeComments(t, "alice")
		posts := fixture.subscribeActor(t, fixture.schema.Post, "alice")
		fixture.app.InvalidatePolicies()
		expectStreamEndsWithReason(t, dependent, events.CodeSubscriptionResync, "")
		expectStreamEndsWithReason(t, posts, events.CodeSubscriptionResync, "")
		fixture.app.policyCache = nil
		uncached := fixture.subscribeComments(t, "alice")
		fixture.app.InvalidatePolicies()
		expectStreamEndsWithReason(t, uncached, events.CodeSubscriptionResync, "")
		var absent *App[deleteEvalPrincipal, deleteEvalActor]
		absent.InvalidatePolicies()
	})
}
