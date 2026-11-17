package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyhistory "github.com/eleven-am/golem/go/internal/policy/history"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func requireMutationHistoryWriter(t *testing.T) {
	t.Helper()
	if mutationHistoryWriterVersion != mutationfact.HistoryFactVersion {
		t.Skip("automatic V4 writer release rehearsal; reader-first binary writes V3")
	}
}

func TestMutationHistoryWriterAutomaticSQLiteMutationFamilies(t *testing.T) {
	requireMutationHistoryWriter(t)
	fixture := eventHistoryScopeFixture(t, false)
	create := func(id byte) golem.CreateInput[policyFactRuntimePost] {
		return golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: id}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}),
			golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "created"))
	}
	for _, step := range []struct {
		name  string
		facts int
		run   func() error
	}{
		{"standalone_create", 1, func() error {
			_, err := SystemCreate(fixture.ctx, fixture.app.System(), fixture.posts, create(221))
			return err
		}},
		{"standalone_update", 1, func() error {
			_, err := SystemUpdate(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 221), mutationHistoryRuntimeUpdate(fixture, "updated"))
			return err
		}},
		{"upsert_update", 1, func() error {
			_, err := SystemUpsert(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 221), create(221), mutationHistoryRuntimeUpdate(fixture, "upserted"))
			return err
		}},
		{"upsert_create", 1, func() error {
			_, err := SystemUpsert(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 222), create(222), mutationHistoryRuntimeUpdate(fixture, "not-applied"))
			return err
		}},
		{"batch_update", 2, func() error {
			count, err := SystemUpdateMany(fixture.ctx, fixture.app.System(), fixture.posts, fixture.postID.In(golem.UUID{15: 221}, golem.UUID{15: 222}), mutationHistoryRuntimeUpdateMany(fixture, "batch"))
			if err == nil && count != 2 {
				t.Fatalf("batch updated %d rows", count)
			}
			return err
		}},
		{"batch_delete", 1, func() error {
			count, err := SystemDeleteMany(fixture.ctx, fixture.app.System(), fixture.posts, fixture.postID.Eq(golem.UUID{15: 222}))
			if err == nil && count != 1 {
				t.Fatalf("batch deleted %d rows", count)
			}
			return err
		}},
		{"standalone_delete", 1, func() error {
			_, err := SystemDelete(fixture.ctx, fixture.app.System(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 221))
			return err
		}},
		{"system_transaction", 2, func() error {
			return SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
				for _, id := range []byte{225, 226} {
					if _, err := SystemTxCreate(fixture.ctx, transaction, fixture.posts, create(id)); err != nil {
						return err
					}
				}
				return nil // The normal flush owns all finalization.
			})
		}},
		{"nested_create", 3, func() error {
			comment := golem.GeneratedCreateInput[policyFactRuntimeComment](fixture.schema.Comment,
				golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedEqualField[policyFactRuntimeComment, golem.UUID](fixture.schema.CommentID), golem.UUID{15: 224}),
				golem.GeneratedCreateFieldValue(fixture.schema.Comment, golem.GeneratedTextField[policyFactRuntimeComment, string](fixture.schema.CommentBody), "nested-comment"))
			post := golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 223}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "nested-post"),
				golem.GeneratedNestedCreate[policyFactRuntimePost, policyFactRuntimeComment](fixture.schema.Post, fixture.schema.PostComments, fixture.schema.Commenting, fixture.schema.Comment, comment))
			user := golem.GeneratedCreateInput[policyFactRuntimeUser](fixture.schema.User,
				golem.GeneratedCreateFieldValue(fixture.schema.User, golem.GeneratedEqualField[policyFactRuntimeUser, golem.UUID](fixture.schema.UserID), golem.UUID{15: 3}),
				golem.GeneratedCreateFieldValue(fixture.schema.User, golem.GeneratedTextField[policyFactRuntimeUser, string](fixture.schema.UserName), "nested-owner"),
				golem.GeneratedNestedCreate[policyFactRuntimeUser, policyFactRuntimePost](fixture.schema.User, fixture.schema.UserPosts, fixture.schema.Authorship, fixture.schema.Post, post))
			_, err := SystemCreate(fixture.ctx, fixture.app.System(), fixture.users, user)
			return err
		}},
	} {
		t.Run(step.name, func(t *testing.T) {
			before := mutationHistoryWriterCausations(t, fixture)
			if err := step.run(); err != nil {
				t.Fatal(err)
			}
			graph := mutationHistoryWriterGroup(t, fixture, before, step.facts)
			if len(graph.Roots()) != 1 {
				t.Fatal("automatic writer lost the exact affected User root")
			}
			wantPosts := 1
			switch step.name {
			case "upsert_create", "batch_update", "system_transaction":
				wantPosts = 2
			case "standalone_delete":
				wantPosts = 0
			}
			children, complete := graph.Children(policyhistory.After, graph.RootKeys()[0], policyir.FieldID(fixture.schema.UserPosts), policyir.RelationID(fixture.schema.Authorship))
			if !complete || len(children) != wantPosts {
				t.Fatalf("automatic final extent complete=%v rows=%d want%d", complete, len(children), wantPosts)
			}
		})
	}
	mutationHistorySealAbsent(t, fixture, 221)
	mutationHistorySealAbsent(t, fixture, 222)
	mutationHistoryRuntimeTitle(t, fixture, 223, "nested-post")
}

func TestMutationHistoryWriterAutomaticHookAndSavepointComposition(t *testing.T) {
	requireMutationHistoryWriter(t)
	var fixture policyFactRuntimeFixture
	var target byte
	refuse := false
	refusal := errors.New("automatic writer child rollback")
	fixture = newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true,
		schemaTransform:    func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
		postMutationGrants: true,
		hooks: func(graph schematest.GraphFixture) []golem.HookBinding[policyFactRuntimeActor] {
			return []golem.HookBinding[policyFactRuntimeActor]{
				golem.GeneratedAfterHookBinding[policyFactRuntimeActor, policyFactRuntimePost, golem.UpdateManyHookResult[policyFactRuntimePost]](graph.Post, golem.HookUpdateMany, func(ctx context.Context, result golem.UpdateManyHookResult[policyFactRuntimePost]) error {
					if _, err := golem.HookUpdateRow(ctx, result.Executor(), fixture.posts, mutationHistoryRuntimeTarget(fixture, target), mutationHistoryRuntimeUpdate(fixture, "hook-final")); err != nil {
						return err
					}
					if refuse {
						return refusal
					}
					return nil
				}),
			}
		},
	})
	for _, id := range []byte{231, 232} {
		fixture.mutate(t, golem.EventCreated, id, 1, "before")
	}
	before := mutationHistoryWriterCausations(t, fixture)
	err := CallerTransaction(fixture.ctx, fixture.caller(t), func(transaction *CallerTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		if _, err := CallerTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 232), mutationHistoryRuntimeUpdate(fixture, "sibling")); err != nil {
			return err
		}
		target, refuse = 231, true
		if _, err := CallerTxUpdateMany(fixture.ctx, transaction, fixture.posts, fixture.postID.Eq(golem.UUID{15: target}), mutationHistoryRuntimeUpdateMany(fixture, "batch")); !errors.Is(err, refusal) {
			t.Fatalf("child savepoint did not return hook refusal: %v", err)
		}
		target, refuse = 232, false
		_, err := CallerTxUpdateMany(fixture.ctx, transaction, fixture.posts, fixture.postID.Eq(golem.UUID{15: target}), mutationHistoryRuntimeUpdateMany(fixture, "batch"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	graph := mutationHistoryWriterGroup(t, fixture, before, 3)
	seen := map[byte]bool{}
	for _, entry := range graph.Rows() {
		if entry.Phase != policyhistory.After || golem.ModelID(entry.Row.ModelID()) != fixture.schema.Post {
			continue
		}
		id := mutationHistoryGraphRowID(t, entry.Row, fixture.schema.PostID)
		want := "before"
		if id == 232 {
			want = "hook-final"
		}
		mutationHistoryText(t, &entry.Row, fixture.schema.PostTitle, want)
		seen[id] = true
	}
	if !seen[231] || !seen[232] {
		t.Fatal("automatic graph lost the unchanged or hook-written sibling")
	}
	mutationHistoryRuntimeTitle(t, fixture, 231, "before")
	mutationHistoryRuntimeTitle(t, fixture, 232, "hook-final")
}

func TestMutationHistoryWriterAutomaticGraphLimitRollsBack(t *testing.T) {
	requireMutationHistoryWriter(t)
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "system_transaction"}[explicit], func(t *testing.T) {
			fixture := eventHistoryScopeFixture(t, false)
			for _, id := range []byte{241, 242} {
				fixture.mutate(t, golem.EventCreated, id, 1, "seed")
			}
			// Prepare oversized unrelated witness rows, not oversized mutation
			// facts. The public mutation below is small; automatic capture must
			// refuse the complete historical inventory instead of truncating it.
			large := strings.Repeat("w", 600<<10)
			if _, err := fixture.app.database.ExecContext(fixture.ctx, `UPDATE posts SET title=? WHERE id IN (?,?)`, large, (golem.UUID{15: 241}).String(), (golem.UUID{15: 242}).String()); err != nil {
				t.Fatal(err)
			}
			before := mutationHistoryWriterCausations(t, fixture)
			var factsBefore, deliveryBefore int
			if err := fixture.app.database.GetContext(fixture.ctx, &factsBefore, `SELECT COUNT(*) FROM _golem_outbox`); err != nil {
				t.Fatal(err)
			}
			if err := fixture.app.database.GetContext(fixture.ctx, &deliveryBefore, `SELECT COUNT(*) FROM _golem_outbox_delivery`); err != nil {
				t.Fatal(err)
			}
			input := golem.GeneratedCreateInput[policyFactRuntimePost](fixture.schema.Post,
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.postID, golem.UUID{15: 243}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.authorID, golem.UUID{15: 1}),
				golem.GeneratedCreateFieldValue(fixture.schema.Post, fixture.title, "small-write"))
			var err error
			if explicit {
				callbackFinished := false
				err = SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
					if _, err := SystemTxCreate(fixture.ctx, transaction, fixture.posts, input); err != nil {
						return err
					}
					callbackFinished = true
					return nil
				})
				if !callbackFinished {
					t.Fatal("mutation failed before automatic outer finalization")
				}
			} else {
				_, err = SystemCreate(fixture.ctx, fixture.app.System(), fixture.posts, input)
			}
			mutationHistoryLimit(t, err, golem.LimitPolicyHistoryBytes)
			mutationHistorySealAbsent(t, fixture, 243)
			if after := mutationHistoryWriterCausations(t, fixture); len(after) != len(before) {
				t.Fatal("failed automatic history committed a causation")
			}
			var factsAfter, deliveryAfter int
			if err := fixture.app.database.GetContext(fixture.ctx, &factsAfter, `SELECT COUNT(*) FROM _golem_outbox`); err != nil {
				t.Fatal(err)
			}
			if err := fixture.app.database.GetContext(fixture.ctx, &deliveryAfter, `SELECT COUNT(*) FROM _golem_outbox_delivery`); err != nil {
				t.Fatal(err)
			}
			if factsAfter != factsBefore || deliveryAfter != deliveryBefore {
				t.Fatalf("rollback left outbox/delivery rows: facts=%d/%d delivery=%d/%d", factsAfter, factsBefore, deliveryAfter, deliveryBefore)
			}
		})
	}
}

func TestMutationHistoryWriterWithoutTemplatesKeepsV3(t *testing.T) {
	requireMutationHistoryWriter(t)
	fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true})
	fixture.mutate(t, golem.EventCreated, 251, 1, "ordinary")
	causations := mutationHistoryWriterCausations(t, fixture)
	if len(causations) != 1 {
		t.Fatal("ordinary write did not persist exactly one causation")
	}
	for causal := range causations {
		rows := mutationHistorySealStoredRows(t, fixture, causal)
		if len(rows) != 1 {
			t.Fatal("ordinary create did not persist one fact")
		}
		for _, row := range rows {
			if row.FactVersion != int64(mutationfact.FormatVersion) || row.CodecIdentity != mutationfact.CodecIdentity {
				t.Fatal("writer candidate changed an app with no capture templates")
			}
		}
	}
}

func mutationHistoryWriterCausations(t *testing.T, fixture policyFactRuntimeFixture) map[string]bool {
	t.Helper()
	var values []string
	if err := fixture.app.database.SelectContext(fixture.ctx, &values, `SELECT DISTINCT causation_id FROM _golem_outbox`); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func mutationHistoryWriterGroup(t *testing.T, fixture policyFactRuntimeFixture, before map[string]bool, count int) policyhistory.Graph {
	t.Helper()
	var causal string
	for value := range mutationHistoryWriterCausations(t, fixture) {
		if !before[value] {
			if causal != "" {
				t.Fatal("one API boundary produced multiple causations")
			}
			causal = value
		}
	}
	if causal == "" {
		t.Fatal("successful API emitted no causation")
	}
	rows := mutationHistorySealStoredRows(t, fixture, causal)
	if len(rows) != count {
		t.Fatalf("automatic fact count=%d want%d", len(rows), count)
	}
	for index, row := range rows {
		if row.FactVersion != int64(mutationfact.HistoryFactVersion) || row.TransactionOrdinal != int64(index+1) {
			t.Fatal("automatic flush retained V3 or lost ordinal continuity")
		}
		envelope, err := mutationfact.ValidateStoredRow(row, fixture.app.eventSchemas)
		if err != nil {
			t.Fatal(err)
		}
		_, anchor := envelope.PolicyHistoryCompanion()
		if anchor != (index == 0) {
			t.Fatal("automatic flush did not store exactly one first-ordinal anchor")
		}
	}
	value, err := mutationfact.DecodePolicyHistoryCausation(rows, fixture.app.eventSchemas)
	if err != nil {
		t.Fatal(err)
	}
	var delivery int
	if err := fixture.app.database.GetContext(fixture.ctx, &delivery, `SELECT COUNT(*) FROM _golem_outbox_delivery WHERE causation_id=?`, causal); err != nil || delivery != 1 {
		t.Fatalf("automatic causal delivery count=%d err=%v", delivery, err)
	}
	return value.Graph()
}
