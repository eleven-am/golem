package observation

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/evaluate"
	"github.com/eleven-am/golem/go/internal/policy/history"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	"github.com/eleven-am/golem/go/internal/policy/scopecontract"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type historicalIdentityFixture struct {
	historicalFixture
	base                                     historicalFixture
	mode                                     string
	slug, region, watchRegion, commentRegion golem.FieldID
}

// This exercises the actual provider renderer and SQLite joins. Current fixture
// writes deliberately use SQL: this is acceptance of the pure candidate's
// stable-identity predicate, not an outbox/runtime publication test.
func TestHistoricalIdentityPrefixesUseStablePKAfterUniqueValueTransferSQLite(t *testing.T) {
	for _, mode := range []string{"named", "composite_unique", "composite_pk"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newHistoricalIdentityFixture(t, mode)
			graph, changed := historicalIdentityGraph(t, fixture)
			identity := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, changed))
			ctx := context.Background()
			provider := sqliteprovider.New()
			database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "historical-identity.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := provider.ApplyInitial(ctx, database, fixture.graph.SQLite); err != nil {
				t.Fatal(err)
			}
			uuid := func(id byte) string { return (golem.UUID{15: id}).String() }
			for _, user := range []struct {
				id   byte
				name string
			}{{1, "owner"}, {2, "other"}} {
				if _, err := database.ExecContext(ctx, `INSERT INTO users(id,name) VALUES(?,?)`, uuid(user.id), user.name); err != nil {
					t.Fatal(err)
				}
			}
			insertPost := func(id byte, region, slug string) {
				t.Helper()
				if _, err := database.ExecContext(ctx, `INSERT INTO posts(id,author_id,title,identity_region,identity_slug) VALUES(?,?,?,?,?)`, uuid(id), uuid(2), "video", region, slug); err != nil {
					t.Fatal(err)
				}
			}
			insertPost(11, "eu", "old")
			insertPost(99, "eu", "replacement")
			if mode == "composite_pk" {
				insertPost(11, "us", "other-region")
			} else if mode == "composite_unique" {
				insertPost(98, "us", "new")
			}
			// The current original cut already belongs to owner M2, while A's
			// historical owner is M1. Only the recorded PK may connect the prefix.
			insertWatch := func(id, profile byte, reference any, region any) {
				t.Helper()
				if _, err := database.ExecContext(ctx, `INSERT INTO watches(id,profile_id,video_id,video_region) VALUES(?,?,?,?)`, uuid(id), uuid(profile), reference, region); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "composite_pk" {
				insertWatch(1, 1, uuid(11), "eu")
				insertWatch(2, 1, uuid(11), "us")
				insertWatch(3, 1, uuid(11), nil)
				insertWatch(4, 1, nil, "eu")
				insertWatch(5, 2, uuid(11), "eu")
			} else {
				insertWatch(1, 1, "old", "eu")
				// Detach the FK, transfer the old unique value to another PK,
				// then reconnect the source. These are real constrained joins.
				if _, err := database.ExecContext(ctx, `UPDATE watches SET video_id=NULL WHERE id=?`, uuid(1)); err != nil {
					t.Fatal(err)
				}
				if _, err := database.ExecContext(ctx, `UPDATE posts SET identity_slug='new' WHERE id=?`, uuid(11)); err != nil {
					t.Fatal(err)
				}
				if _, err := database.ExecContext(ctx, `UPDATE posts SET identity_slug='old' WHERE id=?`, uuid(99)); err != nil {
					t.Fatal(err)
				}
				if _, err := database.ExecContext(ctx, `UPDATE watches SET video_id='old' WHERE id=?`, uuid(1)); err != nil {
					t.Fatal(err)
				}
				insertWatch(2, 1, "new", "eu")
				insertWatch(3, 2, "new", "eu")
				insertWatch(4, 1, nil, "eu")
				if mode == "composite_unique" {
					insertWatch(5, 1, "new", nil)
					insertWatch(6, 1, "new", "us")
				}
			}
			proof := must(provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.graph.Registry.ModelFingerprint())))
			resolver := policysql.SchemaResolver(fixture.graph.Registry)
			for _, negative := range []bool{false, true} {
				plan := historicalIdentityPlan(t, fixture, negative)
				candidates := must(plan.HistoricalCandidates(ctx, fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.Comment), Before: &identity}))
				var chosen *HistoricalCandidate
				for index := range candidates {
					cell, _ := candidates[index].CutIdentity().Cell(ir.FieldID(fixture.graph.PostID))
					value, _ := cell.PolicyValue()
					id, _ := value.UUID()
					if id[15] == 11 {
						chosen = &candidates[index]
						break
					}
				}
				if chosen == nil || chosen.CutOffset() != 1 {
					t.Fatal("original recorded Video cut was omitted")
				}
				fragment := must(policysql.Compile(policysql.Request{Condition: chosen.Prefix(), Provider: ir.ProviderSQLite, Resolver: resolver, Dialect: sqliteprovider.NewPolicyDialect(), Capabilities: proof, BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "source"}))
				if strings.Contains(fragment.SQL(), `"users"`) || strings.Contains(fragment.SQL(), `"author_id"`) {
					t.Fatalf("prefix rejoined current owner beyond the cut: %s", fragment.SQL())
				}
				selectIDs := func() []string {
					t.Helper()
					var ids []string
					if err := database.SelectContext(ctx, &ids, `SELECT id FROM watches AS source WHERE `+fragment.SQL()+` ORDER BY id`, fragment.Args()...); err != nil {
						t.Fatal(err)
					}
					return ids
				}
				want := []string{uuid(2)}
				if mode == "composite_pk" {
					want = []string{uuid(1)}
				}
				if got := selectIDs(); !reflect.DeepEqual(got, want) {
					t.Fatalf("negative=%v current prefix=%v want=%v", negative, got, want)
				}
				if _, err := database.ExecContext(ctx, `UPDATE posts SET title='revoked' WHERE id=? AND identity_region='eu'`, uuid(11)); err != nil {
					t.Fatal(err)
				}
				if got := selectIDs(); len(got) != 0 {
					t.Fatalf("revoked current cut guard matched: %v", got)
				}
				if _, err := database.ExecContext(ctx, `UPDATE posts SET title='video' WHERE id=? AND identity_region='eu'`, uuid(11)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestHistoricalCompositeIdentityMissingAndNULLStayUnknownOrInvalid(t *testing.T) {
	fixture := newHistoricalIdentityFixture(t, "composite_pk")
	graph, changed := historicalIdentityGraph(t, fixture)
	plan := historicalIdentityPlan(t, fixture, false)
	identity := must(mutationdecode.PrimaryIdentity(fixture.graph.Registry, changed))
	candidates := must(plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.Comment), Before: &identity}))
	if len(candidates) == 0 {
		t.Fatal("composite identity fixture has no candidate")
	}
	profile := must(evaluate.ValueField(ir.FieldID(fixture.profile), ir.UUIDValue([16]byte{15: 1})))
	unknown := must(evaluate.NewRecord(ir.ModelID(fixture.watch), profile))
	if _, err := evaluate.Condition(candidates[0].Prefix(), unknown, ir.PortableProviders()); err == nil {
		t.Fatal("an unloaded current cut relationship became a known empty relation")
	}
	empty := must(evaluate.ToOneField(ir.FieldID(fixture.relationField), ir.ModelID(fixture.graph.Post)))
	if got := must(evaluate.Condition(candidates[0].Prefix(), must(evaluate.NewRecord(ir.ModelID(fixture.watch), profile, empty)), ir.PortableProviders())); got {
		t.Fatal("an explicitly empty current cut relationship matched")
	}
	cutID, _ := candidates[0].CutIdentity().Cell(ir.FieldID(fixture.graph.PostID))
	cutValue, _ := cutID.PolicyValue()
	loadedID := must(evaluate.ValueField(cutID.FieldID(), cutValue))
	loadedTitle := must(evaluate.ValueField(ir.FieldID(fixture.graph.PostTitle), must(ir.StringValue("video"))))
	partialCut := must(evaluate.NewRecord(ir.ModelID(fixture.graph.Post), loadedID, loadedTitle))
	partialRelation := must(evaluate.ToOneField(ir.FieldID(fixture.relationField), ir.ModelID(fixture.graph.Post), partialCut))
	if _, err := evaluate.Condition(candidates[0].Prefix(), must(evaluate.NewRecord(ir.ModelID(fixture.watch), profile, partialRelation)), ir.PortableProviders()); err == nil {
		t.Fatal("missing composite cut PK component became a complete current relation")
	}
	for _, null := range []bool{false, true} {
		components := []mutationdecode.IdentityComponent{must(mutationdecode.IdentityValue(ir.FieldID(fixture.graph.PostID), ir.UUIDValue([16]byte{15: 11})))}
		if null {
			components = append(components, must(mutationdecode.IdentityNull(ir.FieldID(fixture.region))))
		}
		bad := must(mutationdecode.NewIdentity(fixture.graph.PostKey, components))
		if candidates, err := plan.HistoricalCandidates(context.Background(), fixture.graph.Registry, graph, HistoricalChange{Model: ir.ModelID(fixture.graph.Post), Before: &bad}); err == nil || candidates != nil {
			t.Fatalf("missing/NULL composite primary identity was accepted: null=%v err=%v", null, err)
		}
	}
	// The graph itself refuses an incomplete composite correlation; it may
	// not turn the absent component into a complete empty historical extent.
	input := graph.Input()
	for index, entry := range input.Rows {
		if entry.Row.ModelID() != ir.ModelID(fixture.graph.Comment) {
			continue
		}
		var cells []mutationdecode.Cell
		for _, cell := range entry.Row.Cells() {
			if cell.FieldID() != ir.FieldID(fixture.commentRegion) {
				cells = append(cells, cell)
			}
		}
		input.Rows[index].Row = historicalRow(t, fixture.graph.Registry, fixture.graph.Comment, cells...)
	}
	if _, err := history.New(fixture.graph.Registry, ir.ProviderSQLite, input); err == nil {
		t.Fatal("incomplete historical composite correlation was accepted")
	}
}

func historicalIdentityPlan(t *testing.T, fixture historicalIdentityFixture, negative bool) Plan {
	t.Helper()
	f := fixture.graph
	owner := historicalOwnerCondition(t, fixture.historicalFixture, negative)
	video := historicalRelation(t, f.Registry, ir.ModelID(f.Post), f.PostAuthor, f.Authorship, ir.ModelID(f.User), ir.RelationToOne, owner)
	video = must(logical(video.ModelID(), ir.LogicalAnd, []ir.Condition{historicalScalar(t, f.Registry, f.Post, f.PostTitle, must(ir.StringValue("video"))), video}))
	outer := historicalRelation(t, f.Registry, ir.ModelID(fixture.watch), fixture.relationField, fixture.relation, ir.ModelID(f.Post), ir.RelationToOne, video)
	outer = must(logical(outer.ModelID(), ir.LogicalAnd, []ir.Condition{historicalScalar(t, f.Registry, fixture.watch, fixture.profile, ir.UUIDValue([16]byte{15: 1})), outer}))
	return must(New(outer, nil))
}

func historicalIdentityGraph(t *testing.T, fixture historicalIdentityFixture) (history.Graph, mutationdecode.Row) {
	t.Helper()
	base, changed := historicalGraph(t, fixture.base, false)
	input := base.Input()
	rebuild := func(row mutationdecode.Row) mutationdecode.Row {
		cells := row.Cells()
		if fixture.mode == "composite_pk" {
			field := fixture.region
			if row.ModelID() == ir.ModelID(fixture.graph.Comment) {
				field = fixture.commentRegion
			}
			if row.ModelID() == ir.ModelID(fixture.graph.Post) || row.ModelID() == ir.ModelID(fixture.graph.Comment) {
				cells = append(cells, mutationdecode.Value(ir.FieldID(field), must(ir.StringValue("eu"))))
			}
		}
		return must(mutationdecode.NewRow(fixture.graph.Registry, row.ModelID(), cells))
	}
	for index := range input.Rows {
		input.Rows[index].Row = rebuild(input.Rows[index].Row)
	}
	for index := range input.Roots {
		input.Roots[index].Identity = rebuild(input.Roots[index].Identity)
	}
	return must(history.New(fixture.graph.Registry, ir.ProviderSQLite, input)), rebuild(changed)
}

func newHistoricalIdentityFixture(t *testing.T, mode string) historicalIdentityFixture {
	t.Helper()
	base := newHistoricalFixture(t)
	fixture := historicalIdentityFixture{historicalFixture: base, base: base, mode: mode, slug: golem.FieldID{14: 5, 15: 1}, region: golem.FieldID{14: 5, 15: 2}, watchRegion: golem.FieldID{14: 5, 15: 3}, commentRegion: golem.FieldID{14: 5, 15: 4}}
	compilation := must(schema.DecodeActive(base.graph.Bundle))
	modelID := func(id golem.ModelID) compilerir.ModelID { return compilerir.ModelID(hex.EncodeToString(id[:])) }
	fieldID := func(id golem.FieldID) compilerir.FieldID { return compilerir.FieldID(hex.EncodeToString(id[:])) }
	stringField := func(id golem.FieldID, name, column string, nullable bool) compilerir.FieldIR {
		return compilerir.FieldIR{ID: fieldID(id), GoName: name, LogicalName: name, Kind: compilerir.FieldScalar, Scalar: &compilerir.ScalarFieldIR{Column: compilerir.SQLIdentifier(column), Type: compilerir.LogicalTypeIR{Kind: compilerir.TypeString}, Nullable: nullable}}
	}
	for index := range compilation.Model.Models {
		model := &compilation.Model.Models[index]
		switch model.ID {
		case modelID(fixture.graph.Post):
			model.Fields = append(model.Fields, stringField(fixture.slug, "IdentitySlug", "identity_slug", false), stringField(fixture.region, "IdentityRegion", "identity_region", false))
			fields := []compilerir.FieldID{fieldID(fixture.slug)}
			if mode == "composite_unique" {
				fields = append(fields, fieldID(fixture.region))
			}
			model.Uniques = append(model.Uniques, compilerir.KeyIR{ID: compilerir.KeyID("00000000000000000000000000000505"), Kind: compilerir.KeyUnique, PhysicalName: "uq_posts_identity_ref", Fields: fields})
			if mode == "composite_pk" {
				model.PrimaryKey.Fields = append(model.PrimaryKey.Fields, fieldID(fixture.region))
			}
		case modelID(fixture.watch):
			for field := range model.Fields {
				if model.Fields[field].ID == fieldID(fixture.video) {
					model.Fields[field].Scalar.Nullable = true
					if mode != "composite_pk" {
						model.Fields[field].Scalar.Type = compilerir.LogicalTypeIR{Kind: compilerir.TypeString}
					}
				}
			}
			model.Fields = append(model.Fields, stringField(fixture.watchRegion, "VideoRegion", "video_region", true))
		case modelID(fixture.graph.Comment):
			if mode == "composite_pk" {
				model.Fields = append(model.Fields, stringField(fixture.commentRegion, "PostRegion", "post_region", false))
			}
		}
	}
	for index := range compilation.Model.Relations {
		relation := &compilation.Model.Relations[index]
		if relation.ID == compilerir.RelationID(hex.EncodeToString(fixture.relation[:])) {
			relation.LocalFields = []compilerir.FieldID{fieldID(fixture.video)}
			relation.RemoteFields = []compilerir.FieldID{fieldID(fixture.slug)}
			if mode == "composite_pk" {
				relation.RemoteFields = []compilerir.FieldID{fieldID(fixture.graph.PostID)}
			}
			if mode != "named" {
				relation.LocalFields = append(relation.LocalFields, fieldID(fixture.watchRegion))
				relation.RemoteFields = append(relation.RemoteFields, fieldID(fixture.region))
			}
		}
		if mode == "composite_pk" && relation.ID == compilerir.RelationID(hex.EncodeToString(fixture.graph.Commenting[:])) {
			relation.LocalFields = append(relation.LocalFields, fieldID(fixture.commentRegion))
			relation.RemoteFields = append(relation.RemoteFields, fieldID(fixture.region))
		}
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
		if owner.ModelID == modelID(fixture.graph.Post) {
			owner.Fields = append(owner.Fields, compilerir.FieldContractIR{FieldID: fieldID(fixture.slug)}, compilerir.FieldContractIR{FieldID: fieldID(fixture.region)})
		}
		if owner.ModelID == modelID(fixture.watch) {
			owner.Fields = append(owner.Fields, compilerir.FieldContractIR{FieldID: fieldID(fixture.watchRegion)})
		}
		if mode == "composite_pk" && owner.ModelID == modelID(fixture.graph.Comment) {
			owner.Fields = append(owner.Fields, compilerir.FieldContractIR{FieldID: fieldID(fixture.commentRegion)})
		}
		if !owner.Subscriptions {
			continue
		}
		for _, model := range compilation.Model.Models {
			if model.ID != owner.ModelID {
				continue
			}
			fields := must(compilerir.DeleteSnapshotFields(model, compilation.Model.Relations, nil))
			shape := must(compilerir.BuildEventSchemaShape(model, compilation.Model.Enums, fields))
			owner.Event.Schema = shape
			owner.Event.SchemaFingerprint = must(compilerir.EventSchemaFingerprint(shape))
			owner.Event.DeleteSnapshotFull = compilerir.DeleteSnapshotIsFull(model, fields)
			if len(shape.IdentityFields) > 1 {
				owner.Event.IdentityTypeName = model.LogicalName + "EventIdentity"
			}
		}
	}
	if diagnostics := scopecontract.Normalize(&compilation, scopes); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	if diagnostics := scopecontract.NormalizeFacts(&compilation, facts); len(diagnostics) != 0 {
		t.Fatal(diagnostics)
	}
	f := fixture.graph
	f.SQLite = must(sqliteprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{}))
	f.PostgreSQL = must(postgresprovider.New().Lower(context.Background(), compilation.Model, physical.LowerOptions{}))
	model := golem.GeneratedSchemaDocument(uint32(compilerir.ModelFormatVersion), uint32(compilerir.CanonicalFormatVersion), historicalDigest(t, string(must(compilerir.ModelFingerprint(compilation.Model)))), must(compilerir.CanonicalModel(compilation.Model)))
	contract := golem.GeneratedSchemaDocument(uint32(compilerir.ContractFormatVersion), uint32(compilerir.CanonicalFormatVersion), historicalDigest(t, string(must(compilerir.ContractFingerprint(compilation.Contract)))), must(compilerir.CanonicalContract(compilation.Contract)))
	f.Bundle = golem.GeneratedSchemaBundle(f.Bundle.GenerationDigest(), f.Bundle.GeneratorVersion(), f.Bundle.TemplateABIVersion(), model, contract, schematest.ProviderDocument(t, golem.SQLite, f.SQLite), schematest.ProviderDocument(t, golem.PostgreSQL, f.PostgreSQL))
	f.Registry = must(schema.New(f.Bundle))
	fixture.graph = f
	return fixture
}
