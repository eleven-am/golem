package observation

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

func TestComposedFactsDoNotEraseAnotherFactsActorDomain(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewGraph(t)
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "composed-facts.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	id := func(last byte) golem.UUID { return golem.UUID{15: last} }
	for _, actor := range []byte{1, 2} {
		if _, err := database.Exec(`INSERT INTO users(id,name) VALUES(?,?)`, id(actor).String(), "actor"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO posts(id,author_id,title) VALUES(?,?,?)`, id(actor+10).String(), id(actor).String(), "ready"); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO comments(id,post_id,body) VALUES(?,?,?)`, id(actor+20).String(), id(actor+10).String(), "source"); err != nil {
			t.Fatal(err)
		}
	}
	descriptor := golem.GeneratedModelDescriptor[sourceComment](fixture.Comment, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	post := golem.GeneratedToOne[sourceComment, sourcePost](fixture.CommentPost, fixture.Commenting, fixture.Post)
	author := golem.GeneratedToOne[sourcePost, sourceUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	userID := golem.GeneratedEqualField[sourceUser, golem.UUID](fixture.UserID)
	title := golem.GeneratedTextField[sourcePost, string](fixture.PostTitle)
	bind := func(predicate golem.Predicate[sourceComment]) ir.Condition {
		return must(policybind.Predicate(must(predicate.Freeze(descriptor)), fixture.Registry, ir.PortableProviders()))
	}
	root := ir.ModelID(fixture.Comment)
	postID := golem.GeneratedEqualField[sourceComment, golem.UUID](fixture.CommentPostID)
	// The actor resolver supplies authoritative source-domain grant keys.
	// A membership data fact is not an observation-authority declaration.
	audience := must(ir.NewFactScope(bind(postID.Eq(id(11)))))
	memberID := must(ir.FactIdentity(root, "in_actor_domain"))
	readyID := must(ir.FactIdentity(root, "ready"))
	member := must(ir.NewFact(root, memberID, bind(post.Is(author.Is(userID.Eq(id(1))))), audience))
	ready := must(ir.NewFact(root, readyID, bind(post.Is(title.Eq("ready"))), audience))
	plan := must(New(must(logical(root, ir.LogicalAnd, []ir.Condition{member, ready})), nil))
	otherActor := must(mutationdecode.NewRow(fixture.Registry, ir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(ir.FieldID(fixture.PostID), ir.UUIDValue(id(12).Bytes())),
		mutationdecode.Value(ir.FieldID(fixture.AuthorID), ir.UUIDValue(id(2).Bytes())),
		mutationdecode.Value(ir.FieldID(fixture.PostTitle), must(ir.StringValue("ready"))),
	}))
	scope := must(plan.BindChange(otherActor, fixture.Registry, ir.ProviderSQLite))
	resolver := policysql.SchemaResolver(fixture.Registry)
	proof := must(provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint())))
	selectSources := func(table string, condition ir.Condition) []string {
		t.Helper()
		fragment := must(policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite,
			Resolver: resolver, Dialect: sqliteprovider.NewPolicyDialect(), Capabilities: proof,
			BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "source"}))
		var selected []string
		if err := database.Select(&selected, `SELECT id FROM `+table+` AS source WHERE `+fragment.SQL(), fragment.Args()...); err != nil {
			t.Fatal(err)
		}
		return selected
	}
	selected := selectSources("comments", scope)
	if len(selected) != 0 {
		t.Fatalf("readiness observation escaped another fact's actor domain: sources=%v", selected)
	}
	ownActor := must(mutationdecode.NewRow(fixture.Registry, ir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(ir.FieldID(fixture.PostID), ir.UUIDValue(id(11).Bytes())),
		mutationdecode.Value(ir.FieldID(fixture.AuthorID), ir.UUIDValue(id(1).Bytes())),
		mutationdecode.Value(ir.FieldID(fixture.PostTitle), must(ir.StringValue("ready"))),
	}))
	if selected := selectSources("comments", must(plan.BindChange(ownActor, fixture.Registry, ir.ProviderSQLite))); !reflect.DeepEqual(selected, []string{id(21).String()}) {
		t.Fatalf("authorized composition lost source recovery: %v", selected)
	}
	for _, actor := range []byte{1, 2} {
		// Unknown data relevance may trigger recovery only after the exact
		// source domain and captured route have constrained the audience.
		partial := must(mutationdecode.NewRow(fixture.Registry, ir.ModelID(fixture.Post), []mutationdecode.Cell{
			mutationdecode.Value(ir.FieldID(fixture.PostID), ir.UUIDValue(id(actor+10).Bytes())),
			mutationdecode.Value(ir.FieldID(fixture.AuthorID), ir.UUIDValue(id(actor).Bytes())),
		}))
		route, err := plan.BindChange(partial, fixture.Registry, ir.ProviderSQLite)
		if err != nil {
			t.Fatalf("missing relevance became an unscoped error for actor %d: %v", actor, err)
		}
		selected := selectSources("comments", route)
		if actor == 2 && len(selected) != 0 || actor == 1 && !reflect.DeepEqual(selected, []string{id(21).String()}) {
			t.Fatalf("unknown relevance escaped or lost its exact actor route: actor=%d sources=%v", actor, selected)
		}
	}
	postDescriptor := golem.GeneratedModelDescriptor[sourcePost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	bindPost := func(predicate golem.Predicate[sourcePost]) ir.Condition {
		return must(policybind.Predicate(must(predicate.Freeze(postDescriptor)), fixture.Registry, ir.PortableProviders()))
	}
	postRoot := ir.ModelID(fixture.Post)
	authorID := golem.GeneratedEqualField[sourcePost, golem.UUID](fixture.AuthorID)
	postScope := must(ir.NewFactScope(bindPost(authorID.Eq(id(1)))))
	localReady := must(ir.NewFact(postRoot, must(ir.FactIdentity(postRoot, "local_ready")), bindPost(title.Eq("ready")), postScope))
	related := must(ir.NewRelation(root, ir.FieldID(fixture.CommentPost), ir.RelationID(fixture.Commenting), postRoot, ir.RelationToOne, ir.OperatorRelationIs, &localReady, nil))
	relatedPlan := must(New(related, nil))
	for _, actor := range []byte{1, 2} {
		partial := must(mutationdecode.NewRow(fixture.Registry, postRoot, []mutationdecode.Cell{
			mutationdecode.Value(ir.FieldID(fixture.PostID), ir.UUIDValue(id(actor+10).Bytes())),
			mutationdecode.Value(ir.FieldID(fixture.AuthorID), ir.UUIDValue(id(actor).Bytes())),
		}))
		selected := selectSources("comments", must(relatedPlan.BindChange(partial, fixture.Registry, ir.ProviderSQLite)))
		if actor == 2 && len(selected) != 0 || actor == 1 && !reflect.DeepEqual(selected, []string{id(21).String()}) {
			t.Fatalf("related owner lost its exact historical authority: actor=%d sources=%v", actor, selected)
		}
	}
	unknownAuthority := must(mutationdecode.NewRow(fixture.Registry, postRoot, []mutationdecode.Cell{
		mutationdecode.Value(ir.FieldID(fixture.PostID), ir.UUIDValue(id(11).Bytes())),
	}))
	if _, err := relatedPlan.BindChange(unknownAuthority, fixture.Registry, ir.ProviderSQLite); err == nil {
		t.Fatal("missing historical authority became an unrestricted relevance route")
	}
	// Separate children may witness the member and readiness facts. Their
	// Boolean composition must not borrow a different source's audience.
	userDescriptor := golem.GeneratedModelDescriptor[sourceUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	bindUser := func(predicate golem.Predicate[sourceUser]) ir.Condition {
		return must(policybind.Predicate(must(predicate.Freeze(userDescriptor)), fixture.Registry, ir.PortableProviders()))
	}
	posts := golem.GeneratedToMany[sourceUser, sourcePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	userRoot := ir.ModelID(fixture.User)
	userScope := must(ir.NewFactScope(bindUser(userID.Eq(id(1)))))
	memberMany := must(ir.NewFact(userRoot, must(ir.FactIdentity(userRoot, "member")), bindUser(posts.Some(author.Is(userID.Eq(id(1))))), userScope))
	readyMany := must(ir.NewFact(userRoot, must(ir.FactIdentity(userRoot, "ready")), bindUser(posts.Some(title.Eq("ready"))), userScope))
	for _, negative := range []bool{false, true} {
		question := must(logical(userRoot, ir.LogicalAnd, []ir.Condition{memberMany, readyMany}))
		if negative {
			question = must(negate(question))
		}
		manyPlan := must(New(question, nil))
		if selected := selectSources("users", must(manyPlan.BindChange(otherActor, fixture.Registry, ir.ProviderSQLite))); len(selected) != 0 {
			t.Fatalf("negative=%v collection observation escaped its source scope: %v", negative, selected)
		}
		if selected := selectSources("users", must(manyPlan.BindChange(ownActor, fixture.Registry, ir.ProviderSQLite))); !reflect.DeepEqual(selected, []string{id(1).String()}) {
			t.Fatalf("negative=%v collection observation lost its authorized route: %v", negative, selected)
		}
	}
}
