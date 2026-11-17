package runtime

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestMutationHistoryRuntimeBatchCapturesBeforeReentrantAfterHook(t *testing.T) {
	var fixture policyFactRuntimeFixture
	var retained *mutationState
	var calls int
	fixture = newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true,
		schemaTransform:    func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
		postMutationGrants: true,
		hooks: func(graph schematest.GraphFixture) []golem.HookBinding[policyFactRuntimeActor] {
			return []golem.HookBinding[policyFactRuntimeActor]{
				golem.GeneratedAfterHookBinding[policyFactRuntimeActor, policyFactRuntimePost, golem.UpdateManyHookResult[policyFactRuntimePost]](graph.Post, golem.HookUpdateMany, func(ctx context.Context, result golem.UpdateManyHookResult[policyFactRuntimePost]) error {
					calls++
					if retained == nil || result.Count() != 2 {
						t.Fatalf("batch hook state=%p count=%d", retained, result.Count())
					}
					images := retained.historyImages()
					if len(images) != 2 {
						t.Fatalf("batch was not journaled before After hook: images=%d", len(images))
					}
					for _, id := range []byte{61, 62} {
						image := mutationHistoryRuntimeImage(t, images, graph.PostID, id)
						mutationHistoryText(t, image.before, graph.PostTitle, "before")
						mutationHistoryText(t, image.after, graph.PostTitle, "batch")
					}
					_, err := golem.HookUpdateRow(ctx, result.Executor(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 61), mutationHistoryRuntimeUpdate(fixture, "hook"))
					return err
				}),
			}
		},
	})
	for _, id := range []byte{61, 62} {
		fixture.mutate(t, golem.EventCreated, id, 1, "before")
	}
	err := CallerTransaction(fixture.ctx, fixture.caller(t), func(transaction *CallerTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
		var err error
		retained, err = transaction.caller.executor.mutationState()
		if err != nil {
			return err
		}
		count, err := CallerTxUpdateMany(fixture.ctx, transaction, fixture.posts, fixture.postID.In(golem.UUID{15: 61}, golem.UUID{15: 62}), mutationHistoryRuntimeUpdateMany(fixture, "batch"))
		if err != nil || count != 2 {
			t.Fatalf("batch count=%d err=%v", count, err)
		}
		images := retained.historyImages()
		if len(images) != 2 {
			t.Fatalf("outer fact construction changed actual row inventory: %d", len(images))
		}
		for _, expected := range []struct {
			id    byte
			title string
		}{{61, "hook"}, {62, "batch"}} {
			image := mutationHistoryRuntimeImage(t, images, fixture.schema.PostID, expected.id)
			mutationHistoryText(t, image.before, fixture.schema.PostTitle, "before")
			mutationHistoryText(t, image.after, fixture.schema.PostTitle, expected.title)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("batch transaction err=%v hooks=%d", err, calls)
	}
	mutationHistoryRuntimeTitle(t, fixture, 61, "hook")
	mutationHistoryRuntimeTitle(t, fixture, 62, "batch")
	mutationHistoryRuntimeCleared(t, retained)
}

func TestMutationHistoryRuntimeBatchAndNestedHookWritesRollbackTogether(t *testing.T) {
	refusal := errors.New("refuse actual batch hook writes")
	var fixture policyFactRuntimeFixture
	var retained *mutationState
	var refuseHook bool
	fixture = newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true,
		schemaTransform:    func(schematest.GraphFixture) schematest.GraphFixture { return mutationHistoryFixture(t) },
		postMutationGrants: true,
		hooks: func(graph schematest.GraphFixture) []golem.HookBinding[policyFactRuntimeActor] {
			return []golem.HookBinding[policyFactRuntimeActor]{
				golem.GeneratedAfterHookBinding[policyFactRuntimeActor, policyFactRuntimePost, golem.UpdateManyHookResult[policyFactRuntimePost]](graph.Post, golem.HookUpdateMany, func(ctx context.Context, result golem.UpdateManyHookResult[policyFactRuntimePost]) error {
					_, err := golem.HookUpdateRow(ctx, result.Executor(), fixture.posts, mutationHistoryRuntimeTarget(fixture, 63), mutationHistoryRuntimeUpdate(fixture, "nested"))
					if err != nil {
						return err
					}
					image := mutationHistoryRuntimeImage(t, retained.historyImages(), graph.PostID, 63)
					mutationHistoryText(t, image.before, graph.PostTitle, "before")
					mutationHistoryText(t, image.after, graph.PostTitle, "nested")
					if refuseHook {
						return refusal
					}
					return nil
				}),
			}
		},
	})
	for _, id := range []byte{63, 64} {
		fixture.mutate(t, golem.EventCreated, id, 1, "before")
	}
	for _, refuse := range []bool{true, false} {
		refuseHook = refuse
		err := CallerTransaction(fixture.ctx, fixture.caller(t), func(transaction *CallerTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
			var err error
			retained, err = transaction.caller.executor.mutationState()
			if err != nil {
				return err
			}
			// A prior successful sibling application belongs to the enclosing
			// transaction and must survive a failed batch's savepoint rollback.
			if _, err := CallerTxUpdate(fixture.ctx, transaction, fixture.posts, mutationHistoryRuntimeTarget(fixture, 64), mutationHistoryRuntimeUpdate(fixture, "sibling")); err != nil {
				return err
			}
			bytes := retained.historyBytes
			_, err = CallerTxUpdateMany(fixture.ctx, transaction, fixture.posts, fixture.postID.Eq(golem.UUID{15: 63}), mutationHistoryRuntimeUpdateMany(fixture, "batch"))
			if refuse {
				if !errors.Is(err, refusal) {
					t.Fatalf("hook refusal returned %v", err)
				}
				images := retained.historyImages()
				if len(images) != 1 || retained.historyBytes != bytes {
					t.Fatalf("failed batch retained descendant history: images=%d bytes=%d want=%d", len(images), retained.historyBytes, bytes)
				}
				image := mutationHistoryRuntimeImage(t, images, fixture.schema.PostID, 64)
				mutationHistoryText(t, image.after, fixture.schema.PostTitle, "sibling")
				rows, readErr := SystemTxFindMany(fixture.ctx, CallerTxSystem(transaction), fixture.posts, golem.Where(fixture.postID.Eq(golem.UUID{15: 63})), golem.Select[policyFactRuntimePost](fixture.title))
				if readErr != nil || len(rows) != 1 {
					t.Fatalf("rolled-back batch read count=%d err=%v", len(rows), readErr)
				}
				if title, ok := golem.Value(rows[0], fixture.title).Get(); !ok || title != "before" {
					t.Fatalf("batch savepoint left nested write title=%q", title)
				}
			} else if err != nil {
				return err
			}
			return refusal // Also exercise rollback of a released successful batch.
		})
		if !errors.Is(err, refusal) {
			t.Fatalf("outer rollback returned %v", err)
		}
		mutationHistoryRuntimeTitle(t, fixture, 63, "before")
		mutationHistoryRuntimeTitle(t, fixture, 64, "before")
		mutationHistoryRuntimeCleared(t, retained)
	}
}

func TestMutationHistoryRuntimeCapturesCascadeAndSetNullAtSQLApplication(t *testing.T) {
	for _, action := range []compilerir.ReferentialAction{compilerir.ActionCascade, compilerir.ActionSetNull} {
		t.Run(string(action), func(t *testing.T) {
			fixture := newPolicyFactRuntimeFixture(t, false, policyFactRuntimeOptions{rootNoFacts: true, schemaTransform: func(schematest.GraphFixture) schematest.GraphFixture {
				return mutationHistoryRuntimeDeleteFixture(t, action)
			}})
			fixture.mutate(t, golem.EventCreated, 71, 1, "dependent")
			fixture.mutate(t, golem.EventCreated, 72, 2, "unrelated")
			refusal := errors.New("rollback modeled referential action")
			for _, rollback := range []bool{true, false} {
				var retained *mutationState
				err := SystemTransaction(fixture.ctx, fixture.app.System(), func(transaction *SystemTx[policyFactRuntimePrincipal, policyFactRuntimeActor]) error {
					target := golem.GeneratedUniqueSelectorValue[policyFactRuntimeUser](fixture.schema.User, fixture.schema.UserKey, golem.GeneratedSelectorComponent(fixture.schema.UserID, golem.UUID{15: 1}))
					if _, err := SystemTxDelete(fixture.ctx, transaction, fixture.users, target); err != nil {
						return err
					}
					var err error
					retained, err = transaction.system.executor.mutationState()
					if err != nil {
						return err
					}
					images := retained.historyImages()
					if len(images) != 2 {
						t.Fatalf("referential action history rows=%d want parent and dependent", len(images))
					}
					parent := mutationHistoryRuntimeImage(t, images, fixture.schema.UserID, 1)
					mutationHistoryText(t, parent.before, fixture.schema.UserName, "source")
					if parent.after != nil {
						t.Fatal("deleted parent retained a final image")
					}
					dependent := mutationHistoryRuntimeImage(t, images, fixture.schema.PostID, 71)
					mutationHistoryText(t, dependent.before, fixture.schema.PostTitle, "dependent")
					cell, ok := dependent.before.Cell(policyir.FieldID(fixture.schema.AuthorID))
					if !ok || cell.IsNull() {
						t.Fatal("dependent lost exact before-correlation")
					}
					if action == compilerir.ActionCascade {
						if dependent.after != nil {
							t.Fatal("cascaded deletion retained a final row")
						}
					} else {
						mutationHistoryText(t, dependent.after, fixture.schema.PostTitle, "dependent")
						cell, ok = dependent.after.Cell(policyir.FieldID(fixture.schema.AuthorID))
						if !ok || !cell.IsNull() {
							t.Fatal("set-null final correlation was not persisted NULL")
						}
					}
					if rollback {
						return refusal
					}
					return nil
				})
				if (rollback && !errors.Is(err, refusal)) || (!rollback && err != nil) {
					t.Fatalf("referential action rollback=%v err=%v", rollback, err)
				}
				mutationHistoryRuntimeCleared(t, retained)
				var count int
				var author *string
				if err := fixture.app.database.GetContext(fixture.ctx, &count, `SELECT count(*) FROM users WHERE id = ?`, (golem.UUID{15: 1}).String()); err != nil || count != map[bool]int{true: 1, false: 0}[rollback] {
					t.Fatalf("parent persisted count=%d err=%v", count, err)
				}
				if rollback || action == compilerir.ActionSetNull {
					if err := fixture.app.database.QueryRowxContext(fixture.ctx, `SELECT author_id FROM posts WHERE id = ?`, (golem.UUID{15: 71}).String()).Scan(&author); err != nil {
						t.Fatal(err)
					}
					if (rollback && (author == nil || *author != (golem.UUID{15: 1}).String())) || (!rollback && author != nil) {
						t.Fatalf("persisted dependent correlation rollback=%v author=%v", rollback, author)
					}
				} else if err := fixture.app.database.GetContext(fixture.ctx, &count, `SELECT count(*) FROM posts WHERE id = ?`, (golem.UUID{15: 71}).String()); err != nil || count != 0 {
					t.Fatalf("cascade persisted dependent count=%d err=%v", count, err)
				}
				mutationHistoryRuntimeTitle(t, fixture, 72, "unrelated")
			}
		})
	}
}

func mutationHistoryRuntimeTarget(fixture policyFactRuntimeFixture, id byte) golem.UniqueSelectorValue[policyFactRuntimePost] {
	return golem.GeneratedUniqueSelectorValue[policyFactRuntimePost](fixture.schema.Post, fixture.schema.PostKey, golem.GeneratedSelectorComponent(fixture.schema.PostID, golem.UUID{15: id}))
}

func mutationHistoryRuntimeUpdate(fixture policyFactRuntimeFixture, title string) golem.UpdateInput[policyFactRuntimePost] {
	return golem.GeneratedUpdateInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title))
}

func mutationHistoryRuntimeUpdateMany(fixture policyFactRuntimeFixture, title string) golem.UpdateManyInput[policyFactRuntimePost] {
	return golem.GeneratedUpdateManyInput[policyFactRuntimePost](fixture.schema.Post, golem.GeneratedSetFieldValue(fixture.schema.Post, fixture.title, title))
}

func mutationHistoryRuntimeImage(t *testing.T, images []mutationHistoryImage, key golem.FieldID, id byte) mutationHistoryImage {
	t.Helper()
	for _, image := range images {
		row := image.after
		if row == nil {
			row = image.before
		}
		if row == nil {
			continue
		}
		cell, ok := row.Cell(policyir.FieldID(key))
		if !ok {
			continue
		}
		value, ok := cell.PolicyValue()
		if !ok {
			continue
		}
		actual, ok := value.UUID()
		if ok && actual == [16]byte(golem.UUID{15: id}) {
			return image
		}
	}
	t.Fatalf("history did not contain key=%x id=%d", key, id)
	return mutationHistoryImage{}
}

func mutationHistoryRuntimeTitle(t *testing.T, fixture policyFactRuntimeFixture, id byte, expected string) {
	t.Helper()
	rows, err := SystemFindMany(fixture.ctx, fixture.app.System(), fixture.posts, golem.Where(fixture.postID.Eq(golem.UUID{15: id})), golem.Select[policyFactRuntimePost](fixture.title))
	if err != nil || len(rows) != 1 {
		t.Fatalf("persisted Post %d count=%d err=%v", id, len(rows), err)
	}
	if value, ok := golem.Value(rows[0], fixture.title).Get(); !ok || value != expected {
		t.Fatalf("persisted Post %d title=%q want=%q present=%v", id, value, expected, ok)
	}
}

func mutationHistoryRuntimeCleared(t *testing.T, state *mutationState) {
	t.Helper()
	if state == nil || len(state.historyImages()) != 0 || state.historyBytes != 0 {
		t.Fatal("transaction completion retained historical images")
	}
}

// The actual Graph model is re-lowered with an explicit referential action;
// the same ordinary templates still define both policy and capture inventory.
// The shared fixture applies a real reviewed baseline for this exact bundle.
func mutationHistoryRuntimeDeleteFixture(t *testing.T, action compilerir.ReferentialAction) schematest.GraphFixture {
	t.Helper()
	fixture := mutationHistoryFixture(t)
	compilation, err := schema.DecodeActive(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	var scopes []scopecontract.Declaration
	var facts []scopecontract.FactDeclaration
	for index := range compilation.Contract.Models {
		owner := &compilation.Contract.Models[index]
		for _, template := range owner.ScopeTemplates {
			scopes = append(scopes, scopecontract.Declaration{ModelID: owner.ModelID, Template: template})
		}
		for _, template := range owner.FactTemplates {
			facts = append(facts, scopecontract.FactDeclaration{ModelID: owner.ModelID, ScopeName: "named", Template: template})
		}
		owner.ScopeTemplates, owner.FactTemplates = nil, nil
	}
	for index := range compilation.Model.Models {
		for field := range compilation.Model.Models[index].Fields {
			value := &compilation.Model.Models[index].Fields[field]
			if value.ID == compilerir.FieldID(hex.EncodeToString(fixture.AuthorID[:])) {
				value.Scalar.Nullable = true
			}
		}
	}
	for index := range compilation.Model.Relations {
		relation := &compilation.Model.Relations[index]
		if relation.ID == compilerir.RelationID(hex.EncodeToString(fixture.Authorship[:])) {
			relation.ForeignKey = &compilerir.ForeignKeyIR{ID: compilerir.ForeignKeyID("000000000000000000000000000000d1"), PhysicalName: "fk_posts_author", OnUpdate: compilerir.ActionRestrict, OnDelete: action, Match: compilerir.MatchSimple, Deferrable: compilerir.NotDeferrable}
		}
	}
	// Making the physical author nullable also changes the exact durable
	// snapshot scalar shape. Rebuild that existing V3 declaration normally;
	// no historical payload or event format is introduced by this harness.
	for index := range compilation.Contract.Models {
		owner := &compilation.Contract.Models[index]
		for _, logical := range compilation.Model.Models {
			if owner.ModelID != logical.ID || !owner.Subscriptions {
				continue
			}
			snapshot, err := compilerir.DeleteSnapshotFields(logical, compilation.Model.Relations, nil)
			if err != nil {
				t.Fatal(err)
			}
			shape, err := compilerir.BuildEventSchemaShape(logical, compilation.Model.Enums, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint, err := compilerir.EventSchemaFingerprint(shape)
			if err != nil {
				t.Fatal(err)
			}
			owner.Event.Schema = shape
			owner.Event.SchemaFingerprint = fingerprint
			owner.Event.DeleteSnapshotFull = compilerir.DeleteSnapshotIsFull(logical, snapshot)
		}
	}
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, facts); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	fixture.SQLite, err = sqliteprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fixture.PostgreSQL, err = postgresprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	modelPayload, err := compilerir.CanonicalModel(compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	modelFingerprint, err := compilerir.ModelFingerprint(compilation.Model)
	if err != nil {
		t.Fatal(err)
	}
	contractPayload, err := compilerir.CanonicalContract(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	contractFingerprint, err := compilerir.ContractFingerprint(compilation.Contract)
	if err != nil {
		t.Fatal(err)
	}
	model := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), mutationHistoryRuntimeDigest(t, string(modelFingerprint)), modelPayload)
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), mutationHistoryRuntimeDigest(t, string(contractFingerprint)), contractPayload)
	var providers []golem.ProviderSchemaDocument
	for _, entry := range []struct {
		provider golem.Provider
		physical physical.PhysicalSchema
	}{{golem.SQLite, fixture.SQLite}, {golem.PostgreSQL, fixture.PostgreSQL}} {
		payload, err := physical.CanonicalEncode(entry.physical)
		if err != nil {
			t.Fatal(err)
		}
		fingerprint, err := physical.PhysicalFingerprint(entry.physical)
		if err != nil {
			t.Fatal(err)
		}
		system, err := physical.SystemFingerprint(entry.physical.Provider, entry.physical.System)
		if err != nil {
			t.Fatal(err)
		}
		providers = append(providers, golem.GeneratedProviderSchemaDocument(entry.provider, golem.SchemaDigest(system), golem.GeneratedSchemaDocument(entry.physical.Version, entry.physical.CanonicalVersion, golem.SchemaDigest(fingerprint), payload)))
	}
	fixture.Bundle = golem.GeneratedSchemaBundle(fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(), model, contract, providers...)
	fixture.Registry, err = schema.New(fixture.Bundle)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func mutationHistoryRuntimeDigest(t *testing.T, value string) golem.SchemaDigest {
	t.Helper()
	data, err := hex.DecodeString(value)
	if err != nil || len(data) != 32 {
		t.Fatalf("invalid schema fingerprint %q: %v", value, err)
	}
	var result golem.SchemaDigest
	copy(result[:], data)
	return result
}
