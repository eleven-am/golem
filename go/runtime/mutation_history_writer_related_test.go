package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventvalue "github.com/eleven-am/golem/go/internal/event/value"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
)

func mutationHistoryRelatedFixture(t *testing.T, unused bool) policyFactRuntimeFixture {
	t.Helper()
	requireMutationHistoryWriter(t)
	var scope compilerir.ScopeTemplateIR
	var digest golem.SchemaDigest
	return newHistoricalPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{
		unusedFact: unused,
		schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
			graph := schematest.NewSubscribedGraph(t)
			compilation, err := schema.DecodeActive(graph.Bundle)
			if err != nil {
				t.Fatal(err)
			}
			model := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
			field := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
			user, post := model(graph.User), model(graph.Post)
			idType, _ := graph.Registry.Field(graph.User, graph.UserID)
			titleType, _ := graph.Registry.Field(graph.Post, graph.PostTitle)
			args := compilerir.GoNamedTypeIR{PackagePath: "example.test/related", Name: "DomainArgs"}
			declaration := compilerir.ScopeTemplateIR{Name: "domain", Arguments: args,
				Parameters: []compilerir.ScopeParameterIR{{Name: "Member", Type: titleType.LogicalType()}, {Name: "Owner", Type: idType.LogicalType()}},
				Expression: compilerir.ScopeExpressionIR{ModelID: user, Operator: "and", Children: []compilerir.ScopeExpressionIR{
					{ModelID: user, Operator: "eq", FieldID: field(graph.UserID), Parameter: "Owner"},
					{ModelID: user, Operator: "some", FieldID: field(graph.UserPosts), RelationID: compilerir.RelationID(hex.EncodeToString(graph.Authorship[:])), TargetModelID: post, Children: []compilerir.ScopeExpressionIR{{ModelID: post, Operator: "eq", FieldID: field(graph.PostTitle), Parameter: "Member"}}},
				}},
			}
			if diagnostics := scopecontract.Normalize(&compilation, []scopecontract.Declaration{{ModelID: user, Template: declaration}}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			if diagnostics := scopecontract.NormalizeFacts(&compilation, []scopecontract.FactDeclaration{{ModelID: user, ScopeName: "domain", Template: compilerir.FactTemplateIR{
				Name: "ready", Arguments: args, Parameters: []compilerir.ScopeParameterIR{{Name: "State", Type: titleType.LogicalType()}},
				Expression: compilerir.ScopeExpressionIR{ModelID: user, Operator: "some", FieldID: field(graph.UserPosts), RelationID: compilerir.RelationID(hex.EncodeToString(graph.Authorship[:])), TargetModelID: post, Children: []compilerir.ScopeExpressionIR{{ModelID: post, Operator: "eq", FieldID: field(graph.PostTitle), Parameter: "State"}}},
			}}}); len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			payload, err := compilerir.CanonicalContract(compilation.Contract)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := hex.DecodeString(string(fingerprint))
			if err != nil || len(encoded) != 32 {
				t.Fatal("invalid contract fingerprint")
			}
			var contractDigest golem.SchemaDigest
			copy(contractDigest[:], encoded)
			bundle := graph.Bundle
			contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), contractDigest, payload)
			graph.Bundle = golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), contract, bundle.Providers()...)
			graph.Registry, err = schema.New(graph.Bundle)
			if err != nil {
				t.Fatal(err)
			}
			for _, owner := range compilation.Contract.Models {
				if owner.ModelID == user {
					scope = owner.ScopeTemplates[0]
				}
			}
			encoded, err = hex.DecodeString(string(scope.Fingerprint))
			if err != nil || len(encoded) != 32 {
				t.Fatal("invalid scope fingerprint")
			}
			copy(digest[:], encoded)
			return graph
		},
		boundUserScope: func(graph schematest.GraphFixture, actor policyFactRuntimeActor) golem.BoundFactScope[policyFactRuntimeUser] {
			id := golem.GeneratedEqualField[policyFactRuntimeUser, golem.UUID](graph.UserID)
			posts := golem.GeneratedToMany[policyFactRuntimeUser, policyFactRuntimePost](graph.UserPosts, graph.Authorship, graph.Post)
			title := golem.GeneratedTextField[policyFactRuntimePost, string](graph.PostTitle)
			return golem.GeneratedBoundFactScope(graph.User, string(scope.ID), digest, id.Eq(actor.ID).And(posts.Some(title.Eq("member"))), golem.GeneratedTemplateArgument("Member", "member"), golem.GeneratedTemplateArgument("Owner", actor.ID))
		},
	})
}

func mutationHistoryRelatedStreams(t *testing.T, fixture policyFactRuntimeFixture) [2]golem.EventStream[golem.EventMetadata] {
	t.Helper()
	var streams [2]golem.EventStream[golem.EventMetadata]
	for index := range streams {
		stream, err := CallerEvents[policyFactRuntimePrincipal, policyFactRuntimeActor, policyFactRuntimeUser, golem.EventMetadata](fixture.ctx, eventHistoryScopeCaller(t, fixture, byte(index+1)), fixture.users)
		if err != nil {
			t.Fatal(err)
		}
		streams[index] = stream
		t.Cleanup(func() { _ = stream.Close() })
	}
	return streams
}

func mutationHistoryRelatedDelivery(t *testing.T, fixture policyFactRuntimeFixture, stream golem.EventStream[golem.EventMetadata], reason events.Reason) {
	t.Helper()
	ctx, cancel := context.WithTimeout(fixture.ctx, 3*time.Second)
	defer cancel()
	_, err := stream.Recv(ctx)
	var failure *events.Error
	if !errors.As(err, &failure) || failure.Code() != events.CodeSubscriptionResync || failure.Reason() != reason || failure.Detail() != "" {
		t.Fatalf("related scope delivery reason=%q: %v", reason, err)
	}
}

func mutationHistoryRelatedSilence(t *testing.T, fixture policyFactRuntimeFixture, stream golem.EventStream[golem.EventMetadata]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(fixture.ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := stream.Recv(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("another actor observed the historical scope: %v", err)
	}
}

func TestMutationHistoryWriterRelatedScopePublishesIndependentAuthorityChanges(t *testing.T) {
	for _, unused := range []bool{false, true} {
		for _, action := range []golem.EventAction{golem.EventCreated, golem.EventUpdated, golem.EventDeleted} {
			t.Run(string(action)+map[bool]string{false: "/false-data", true: "/unused-data"}[unused], func(t *testing.T) {
				fixture := mutationHistoryRelatedFixture(t, unused)
				fixture.mutate(t, golem.EventCreated, 184, 2, "member")
				seedCount := 1
				if action != golem.EventCreated {
					fixture.mutate(t, golem.EventCreated, 181, 1, "member")
					seedCount++
				}
				fixture.startPublisher(t)
				for range seedCount {
					fixture.awaitPublication(t, golem.EventCreated)
				}
				streams := mutationHistoryRelatedStreams(t, fixture)
				title := "revoked"
				if action == golem.EventCreated {
					title = "member"
				}
				fixture.mutate(t, action, 181, 1, title)
				// This fixture's barrier counts successful suppression records.
				// A scope-loss cleanup terminates that member with an error;
				// only the unaffected actor emits a suppression in that case.
				evaluations := 1
				if action == golem.EventCreated {
					evaluations = 2
				}
				fixture.awaitPublicationAndEvaluation(t, action, evaluations)
				reason := events.Reason("")
				if action == golem.EventCreated {
					reason = events.ReasonDependencyChanged
				}
				mutationHistoryRelatedDelivery(t, fixture, streams[0], reason)
				mutationHistoryRelatedSilence(t, fixture, streams[1])
				count, err := CallerCount(fixture.ctx, eventHistoryScopeCaller(t, fixture, 1), fixture.users)
				want := int64(0)
				if unused && action == golem.EventCreated {
					want = 1
				}
				if err != nil || count != want {
					t.Fatalf("scope/data distinction: count=%d want=%d err=%v", count, want, err)
				}
			})
		}
	}
}

func TestMutationHistoryWriterRelatedScopeKeepsCommittedOwnerAfterDataReparent(t *testing.T) {
	fixture := mutationHistoryRelatedFixture(t, false)
	for _, actor := range []byte{1, 2} {
		fixture.mutate(t, golem.EventCreated, 180+actor, actor, "member")
	}
	fixture.mutate(t, golem.EventCreated, 183, 1, "ready")
	streams := mutationHistoryRelatedStreams(t, fixture)
	before := mutationHistoryWriterCausations(t, fixture)
	fixture.mutate(t, golem.EventUpdated, 183, 1, "changed")
	var causal string
	for candidate := range mutationHistoryWriterCausations(t, fixture) {
		if !before[candidate] {
			if causal != "" {
				t.Fatal("A produced multiple causations")
			}
			causal = candidate
		}
	}
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) != 1 || rows[0].FactVersion != 4 {
		t.Fatal("A was not an automatically sealed V4 mutation")
	}
	if _, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 183), golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post,
		golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 2}))); err != nil {
		t.Fatal(err)
	}
	// Publish only A through the real transport/factory, after B commits. The
	// native outbox retains both groups; no graph or event bytes are rewritten.
	encoded, err := eventcodec.EncodeStoredRow(rows[0], fixture.app.eventSchemas, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	notice, err := eventvalue.NewRoutedNotice(encoded.EventID(), encoded.GenerationDigest(), encoded.ResolvedEventSchemaDigest(), encoded.ModelID(), encoded.Action(), encoded.CausationID(), encoded.TransactionOrdinal(), encoded.Encoded())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := eventvalue.NewEventBatch(encoded.CausationID(), []events.Notice{notice})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.transport.Publish(fixture.ctx, batch); err != nil {
		t.Fatal(err)
	}
	mutationHistoryRelatedDelivery(t, fixture, streams[0], events.ReasonDependencyChanged)
	mutationHistoryRelatedSilence(t, fixture, streams[1])
}
