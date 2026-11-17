package observation

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	policybind "github.com/eleven-am/golem/go/internal/policy/bind"
	"github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
)

type sourceUser struct{}
type sourcePost struct{}
type sourceComment struct{}

func TestSQLiteFactObservationScopesDeletedAndMovedHiddenRows(t *testing.T) {
	ctx := context.Background()
	fixture := schematest.NewGraph(t)
	provider := sqliteprovider.New()
	database, _, err := provider.Open(ctx, filepath.Join(t.TempDir(), "fact-observation.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := provider.ApplyInitial(ctx, database, fixture.SQLite); err != nil {
		t.Fatal(err)
	}
	const actorName = "owner' OR 1=1 --"
	uid := func(last byte) golem.UUID { return golem.UUID{15: last} }
	for index, name := range []string{actorName, "other", actorName} {
		if _, err := database.Exec(`INSERT INTO users(id,name) VALUES(?,?)`, uid(byte(index+1)).String(), name); err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(`INSERT INTO posts(id,author_id,title) VALUES(?,?,?)`, uid(byte(index+11)).String(), uid(byte(index+1)).String(), "branch"); err != nil {
			t.Fatal(err)
		}
	}
	// There are no Comment rows. Observation must use the captured old parent
	// key rather than try to find a deleted target in the current database.
	descriptor := golem.GeneratedModelDescriptor[sourceUser](fixture.User, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	name := golem.GeneratedTextField[sourceUser, string](fixture.UserName)
	id := golem.GeneratedEqualField[sourceUser, golem.UUID](fixture.UserID)
	posts := golem.GeneratedToMany[sourceUser, sourcePost](fixture.UserPosts, fixture.Authorship, fixture.Post)
	title := golem.GeneratedTextField[sourcePost, string](fixture.PostTitle)
	comments := golem.GeneratedToMany[sourcePost, sourceComment](fixture.PostComments, fixture.Commenting, fixture.Comment)
	body := golem.GeneratedTextField[sourceComment, string](fixture.CommentBody)
	frozen := must(name.Eq(actorName).And(posts.Some(title.Eq("branch").And(comments.Some(body.Eq("allowed"))))).Freeze(descriptor))
	physical := must(policybind.Predicate(frozen, fixture.Registry, ir.PortableProviders()))
	factID := must(ir.FactIdentity(ir.ModelID(fixture.User), "has_allowed_comment"))
	fact := must(ir.NewFact(ir.ModelID(fixture.User), factID, physical, unrestrictedFactScope(t, ir.ModelID(fixture.User))))
	filter := must(policybind.Predicate(must(id.Eq(uid(1)).Freeze(descriptor)), fixture.Registry, ir.PortableProviders()))
	proof := must(provider.PolicyCapabilityProof(ctx, database, [32]byte(fixture.Registry.ModelFingerprint())))
	resolver := policysql.SchemaResolver(fixture.Registry)
	selectSources := func(condition ir.Condition) []string {
		t.Helper()
		if ir.HasFacts(condition) {
			t.Fatal("source-impact condition retained executable policy facts")
		}
		fragment := must(policysql.Compile(policysql.Request{Condition: condition, Provider: ir.ProviderSQLite,
			Resolver: resolver, Dialect: sqliteprovider.NewPolicyDialect(), Capabilities: proof,
			BoundFingerprint: resolver.SchemaFingerprint(), RootAlias: "source"}))
		if strings.Contains(fragment.SQL(), actorName) {
			t.Fatal("actor value was interpolated into observation SQL")
		}
		var selected []string
		if err := database.Select(&selected, `SELECT id FROM users AS source WHERE `+fragment.SQL()+` ORDER BY id`, fragment.Args()...); err != nil {
			t.Fatal(err)
		}
		return selected
	}
	commentImage := func(parent byte, value string) mutationdecode.Row {
		return must(mutationdecode.NewRow(fixture.Registry, ir.ModelID(fixture.Comment), []mutationdecode.Cell{
			mutationdecode.Value(ir.FieldID(fixture.CommentID), ir.UUIDValue(uid(99).Bytes())),
			mutationdecode.Value(ir.FieldID(fixture.CommentPostID), ir.UUIDValue(uid(parent).Bytes())),
			mutationdecode.Value(ir.FieldID(fixture.CommentBody), must(ir.StringValue(value))),
		}))
	}
	postImage := func(parent byte, value string) mutationdecode.Row {
		return must(mutationdecode.NewRow(fixture.Registry, ir.ModelID(fixture.Post), []mutationdecode.Cell{
			mutationdecode.Value(ir.FieldID(fixture.PostID), ir.UUIDValue(uid(11).Bytes())),
			mutationdecode.Value(ir.FieldID(fixture.AuthorID), ir.UUIDValue(uid(parent).Bytes())),
			mutationdecode.Value(ir.FieldID(fixture.PostTitle), must(ir.StringValue(value))),
		}))
	}
	for _, negative := range []bool{false, true} {
		question := fact
		if negative {
			question = must(negate(question))
		}
		plan := must(New(question, &filter))
		if len(plan.Paths()) != 2 {
			t.Fatalf("fact paths=%d, want Post and Comment", len(plan.Paths()))
		}
		unfiltered := must(New(question, nil))
		if selected := selectSources(must(unfiltered.BindChange(commentImage(12, "allowed"), fixture.Registry, ir.ProviderSQLite))); len(selected) != 0 {
			t.Fatal("fact observation escaped the actor's source scope")
		}
		if selected := selectSources(must(unfiltered.BindChange(commentImage(13, "allowed"), fixture.Registry, ir.ProviderSQLite))); !reflect.DeepEqual(selected, []string{uid(3).String()}) {
			t.Fatal("source actor scope was confused with the optional caller filter")
		}
		for _, path := range plan.Paths() {
			watch := must(path.WatchFields(fixture.Registry))
			required := []golem.FieldID{fixture.CommentPostID, fixture.CommentBody}
			if path.TargetModel() == ir.ModelID(fixture.Post) {
				required = []golem.FieldID{fixture.AuthorID, fixture.PostTitle, fixture.PostID}
			}
			for _, field := range required {
				found := false
				for _, actual := range watch {
					found = found || actual == field
				}
				if !found {
					t.Fatalf("watch omitted witness or correlation field %x", field)
				}
			}
		}
		for _, test := range []struct {
			name  string
			image mutationdecode.Row
			want  []string
		}{
			{"deleted-child", commentImage(11, "allowed"), []string{uid(1).String()}},
			{"outside-actor", commentImage(12, "allowed"), nil},
			{"outside-filter", commentImage(13, "allowed"), nil},
			{"outside-witness", commentImage(11, "private"), nil},
			// The other dependency is absent now. Its current false truth must
			// not hide this old Post image from a concurrent revocation.
			{"concurrent-parent", postImage(1, "branch"), []string{uid(1).String()}},
			{"outside-parent-witness", postImage(1, "private"), nil},
		} {
			scope := must(plan.BindChange(test.image, fixture.Registry, ir.ProviderSQLite))
			if selected := selectSources(scope); !reflect.DeepEqual(selected, test.want) {
				t.Fatalf("negative=%v %s: sources=%v want=%v", negative, test.name, selected, test.want)
			}
		}
		before := must(plan.BindChange(commentImage(11, "allowed"), fixture.Registry, ir.ProviderSQLite))
		after := must(plan.BindChange(commentImage(12, "allowed"), fixture.Registry, ir.ProviderSQLite))
		if len(selectSources(before)) != 1 || len(selectSources(after)) != 0 {
			t.Fatal("a moved relationship lost its old source scope")
		}
		missingWitness := must(mutationdecode.NewRow(fixture.Registry, ir.ModelID(fixture.Comment), []mutationdecode.Cell{
			mutationdecode.Value(ir.FieldID(fixture.CommentPostID), ir.UUIDValue(uid(11).Bytes())),
		}))
		missingKey := must(mutationdecode.NewRow(fixture.Registry, ir.ModelID(fixture.Comment), []mutationdecode.Cell{
			mutationdecode.Value(ir.FieldID(fixture.CommentBody), must(ir.StringValue("allowed"))),
		}))
		if selected := selectSources(must(plan.BindChange(missingWitness, fixture.Registry, ir.ProviderSQLite))); !reflect.DeepEqual(selected, []string{uid(1).String()}) {
			t.Fatal("unknown relevance did not conservatively recover its authorized route")
		}
		if _, err := plan.BindChange(missingKey, fixture.Registry, ir.ProviderSQLite); err == nil {
			t.Fatal("missing historical routing became a negative observation")
		}
	}
	// A fact used only by a selected field still needs observation.
	truth := must(ir.NewConstant(ir.ModelID(fixture.User), true))
	masked := must(New(truth, &filter, fact))
	if len(masked.Paths()) != 2 {
		t.Fatal("field-only facts lost their observation paths")
	}
	if selected := selectSources(must(masked.BindChange(commentImage(11, "allowed"), fixture.Registry, ir.ProviderSQLite))); len(selected) != 1 {
		t.Fatal("field-only fact was scoped by a different source visibility rule")
	}
	// This actor's authority is carried by the related User's primary key,
	// rather than by a local source-field guard. Abstracting the Author hop as
	// simply unknown must not make another actor's Post a relevant change.
	author := golem.GeneratedToOne[sourcePost, sourceUser](fixture.PostAuthor, fixture.Authorship, fixture.User)
	keyScopedBody := must(policybind.Predicate(must(posts.Some(author.Is(id.Eq(uid(1)))).Freeze(descriptor)), fixture.Registry, ir.PortableProviders()))
	keyScoped := must(New(must(ir.NewFact(ir.ModelID(fixture.User), factID, keyScopedBody, unrestrictedFactScope(t, ir.ModelID(fixture.User)))), nil))
	if selected := selectSources(must(keyScoped.BindChange(postImage(2, "branch"), fixture.Registry, ir.ProviderSQLite))); len(selected) != 0 {
		t.Fatal("nested actor-key constraint was lost and another actor's hidden activity became observable")
	}
	if selected := selectSources(must(keyScoped.BindChange(postImage(1, "branch"), fixture.Registry, ir.ProviderSQLite))); !reflect.DeepEqual(selected, []string{uid(1).String()}) {
		t.Fatal("nested actor-key constraint suppressed its authorized source")
	}
}
