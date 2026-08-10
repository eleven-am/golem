package nested

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	mutationbind "github.com/eleven-am/golem/go/internal/mutation/bind"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	policysql "github.com/eleven-am/golem/go/internal/policy/sql"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

func TestRelationExpansionSQLIsDeterministicForSQLiteAndPostgreSQL(t *testing.T) {
	fixture := schematest.NewGraph(t)
	anchor := graphUserRow(t, fixture, 7)
	position := relationPosition(t, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, mutationir.PositionEntireMembership, nil)
	node := relationNode(t, fixture.User, fixture.UserKey, fixture.UserID, fixture.Post, fixture.Authorship, mutationir.Disconnect, position)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		program, err := RenderRelationExpansion(RelationExpansionSQLRequest{Node: node, Anchor: anchor, Registry: fixture.Registry, Provider: provider, Capabilities: relationProof(t, fixture.Registry, provider), MaxRows: 5, MaxParameters: 20})
		if err != nil {
			t.Fatal(err)
		}
		statements := program.Statements()
		if len(statements) != 1 || statements[0].Role() != ExpandCurrentMembership || statements[0].ModelID() != policyir.ModelID(fixture.Post) || len(statements[0].Args()) != 1 {
			t.Fatalf("provider %d expansion=%#v", provider, statements)
		}
		sqlText := statements[0].SQL()
		placeholder := "?1"
		if provider == policyir.ProviderPostgreSQL {
			placeholder = "$1"
			if !strings.HasSuffix(sqlText, " FOR UPDATE") {
				t.Fatalf("PostgreSQL expansion lacks target lock: %s", sqlText)
			}
		}
		for _, fragment := range []string{"author_id", "= " + placeholder, "ORDER BY", "id", "LIMIT 6"} {
			if !strings.Contains(sqlText, fragment) {
				t.Fatalf("provider %d SQL lacks %q: %s", provider, fragment, sqlText)
			}
		}
		again, err := RenderRelationExpansion(RelationExpansionSQLRequest{Node: node, Anchor: anchor, Registry: fixture.Registry, Provider: provider, Capabilities: relationProof(t, fixture.Registry, provider), MaxRows: 5, MaxParameters: 20})
		if err != nil || again.Statements()[0].SQL() != sqlText {
			t.Fatal("relation expansion rendering is not deterministic")
		}
	}
}

func TestSetDifferenceRendersCurrentAndCanonicalDesiredTargets(t *testing.T) {
	fixture := schematest.NewGraph(t)
	anchor := graphUserRow(t, fixture, 7)
	first := targetFor(t, fixture.Post, fixture.PostKey, fixture.PostID, 9)
	second := targetFor(t, fixture.Post, fixture.PostKey, fixture.PostID, 8)
	expansion, _ := mutationir.NewExpansionRequirement(mutationir.ExpandSetDifference, 5)
	position, err := mutationir.NewRelationPosition(mutationir.RelationPositionInput{
		ParentModel: policyir.ModelID(fixture.User), Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship),
		TargetModel: policyir.ModelID(fixture.Post), Kind: mutationir.PositionSetDifference, Desired: []mutationir.Target{first, second}, Expansion: &expansion,
	})
	if err != nil {
		t.Fatal(err)
	}
	node := relationNode(t, fixture.User, fixture.UserKey, fixture.UserID, fixture.Post, fixture.Authorship, mutationir.SetRelation, position)
	program, err := RenderRelationExpansion(RelationExpansionSQLRequest{Node: node, Anchor: anchor, Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Capabilities: relationProof(t, fixture.Registry, policyir.ProviderSQLite), MaxRows: 5, MaxParameters: 20})
	if err != nil {
		t.Fatal(err)
	}
	statements := program.Statements()
	if len(statements) != 3 || statements[0].Role() != ExpandCurrentMembership || statements[1].Role() != ExpandDesiredTarget || statements[2].Role() != ExpandDesiredTarget {
		t.Fatalf("set-difference statements=%#v", statements)
	}
	firstWhere := strings.SplitN(statements[1].SQL(), " WHERE ", 2)[1]
	secondWhere := strings.SplitN(statements[2].SQL(), " WHERE ", 2)[1]
	if strings.Contains(firstWhere, "author_id") || strings.Contains(secondWhere, "author_id") {
		t.Fatal("desired target resolution was incorrectly restricted to current membership")
	}
}

type relationSQLPost struct{}

func TestRelatedPredicateIsCorrelatedAndParametersAreRebased(t *testing.T) {
	fixture := schematest.NewGraph(t)
	descriptor := golem.GeneratedModelDescriptor[relationSQLPost](fixture.Post, golem.GeneratedDescriptorShape(nil, nil, nil, nil))
	frozen, err := golem.GeneratedTextField[relationSQLPost, string](fixture.PostTitle).Eq("open").Freeze(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	predicate, err := mutationbind.BatchPredicate(frozen, fixture.Post, fixture.Registry)
	if err != nil {
		t.Fatal(err)
	}
	expansion, _ := mutationir.NewExpansionRequirement(mutationir.ExpandRelatedPredicate, 5)
	position, err := mutationir.NewRelationPosition(mutationir.RelationPositionInput{
		ParentModel: policyir.ModelID(fixture.User), Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship),
		TargetModel: policyir.ModelID(fixture.Post), Kind: mutationir.PositionRelatedPredicate, Predicate: &predicate, Expansion: &expansion,
	})
	if err != nil {
		t.Fatal(err)
	}
	node := relationNode(t, fixture.User, fixture.UserKey, fixture.UserID, fixture.Post, fixture.Authorship, mutationir.DeleteMany, position)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		program, err := RenderRelationExpansion(RelationExpansionSQLRequest{
			Node: node, Anchor: graphUserRow(t, fixture, 7), Registry: fixture.Registry, Provider: provider,
			Capabilities: relationProof(t, fixture.Registry, provider), MaxRows: 5, MaxParameters: 2,
		})
		if err != nil {
			t.Fatal(err)
		}
		statement := program.Statements()[0]
		first, second := "?1", "?2"
		if provider == policyir.ProviderPostgreSQL {
			first, second = "$1", "$2"
		}
		if !strings.Contains(statement.SQL(), "author_id\" = "+first) || !strings.Contains(statement.SQL(), "title\"") || !strings.Contains(statement.SQL(), second) || len(statement.Args()) != 2 {
			t.Fatalf("provider %d correlated predicate SQL=%s args=%#v", provider, statement.SQL(), statement.Args())
		}
		if _, err := RenderRelationExpansion(RelationExpansionSQLRequest{
			Node: node, Anchor: graphUserRow(t, fixture, 7), Registry: fixture.Registry, Provider: provider,
			Capabilities: relationProof(t, fixture.Registry, provider), MaxRows: 5, MaxParameters: 1,
		}); err == nil || !strings.Contains(err.Error(), "P4_NESTED_SQL_LIMIT") {
			t.Fatalf("provider %d predicate parameter bound was not enforced: %v", provider, err)
		}
	}
}

func TestMembershipWriteUsesCorrelationOrderAndRejectsRequiredDisconnect(t *testing.T) {
	fixture := schematest.NewGraph(t)
	post := graphPostRow(t, fixture, 4, 1)
	user := graphUserRow(t, fixture, 7)
	position := relationPosition(t, fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, mutationir.PositionBranchResult, nil)
	node := relationNode(t, fixture.Post, fixture.PostKey, fixture.PostID, fixture.Post, fixture.Authorship, mutationir.Connect, position)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		statement, err := RenderMembershipWrite(MembershipSQLRequest{Node: node, Anchor: post, Related: user, Effect: MembershipConnect, Registry: fixture.Registry, Provider: provider, MaxParameters: 10})
		if err != nil {
			t.Fatal(err)
		}
		if statement.Role() != ApplyMembershipConnect || statement.ModelID() != policyir.ModelID(fixture.Post) || len(statement.Args()) != 2 {
			t.Fatalf("provider %d membership statement invalid", provider)
		}
		first, second := "?1", "?2"
		if provider == policyir.ProviderPostgreSQL {
			first, second = "$1", "$2"
		}
		if !strings.Contains(statement.SQL(), "author_id\" = "+first) || !strings.Contains(statement.SQL(), "id\" = "+second) {
			t.Fatalf("correlation/identity bind order changed: %s", statement.SQL())
		}
		if _, err := RenderMembershipWrite(MembershipSQLRequest{Node: node, Anchor: post, Related: user, Effect: MembershipDisconnect, Registry: fixture.Registry, Provider: provider, MaxParameters: 10}); err == nil {
			t.Fatal("required FK disconnect rendered SQL")
		}
	}
}

func TestSQLiteRenderedExpansionAndMembershipWriteExecuteLive(t *testing.T) {
	fixture := schematest.NewGraph(t)
	database, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE users(id TEXT PRIMARY KEY, name TEXT NOT NULL); CREATE TABLE posts(id TEXT PRIMARY KEY, author_id TEXT NOT NULL, title TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	user7, user8, post := uuidText(7), uuidText(8), uuidText(4)
	if _, err := database.Exec("INSERT INTO users VALUES (?, ?), (?, ?)", user7, "seven", user8, "eight"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO posts VALUES (?, ?, ?)", post, user8, "post"); err != nil {
		t.Fatal(err)
	}
	userAnchor := graphUserRow(t, fixture, 8)
	expansionPosition := relationPosition(t, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, mutationir.PositionEntireMembership, nil)
	expansionNode := relationNode(t, fixture.User, fixture.UserKey, fixture.UserID, fixture.Post, fixture.Authorship, mutationir.Disconnect, expansionPosition)
	program, err := RenderRelationExpansion(RelationExpansionSQLRequest{Node: expansionNode, Anchor: userAnchor, Registry: fixture.Registry, Provider: policyir.ProviderSQLite, Capabilities: relationProof(t, fixture.Registry, policyir.ProviderSQLite), MaxRows: 5, MaxParameters: 20})
	if err != nil {
		t.Fatal(err)
	}
	statement := program.Statements()[0]
	rows, err := database.QueryContext(context.Background(), statement.SQL(), statement.Args()...)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		raw := make([]any, len(statement.Columns()))
		dest := make([]any, len(raw))
		for index := range raw {
			dest[index] = &raw[index]
		}
		if err := rows.Scan(dest...); err != nil {
			t.Fatal(err)
		}
		count++
	}
	_ = rows.Close()
	if count != 1 {
		t.Fatalf("related expansion rows=%d want 1; sql=%s args=%#v inserted_author=%s", count, statement.SQL(), statement.Args(), user8)
	}
	postAnchor := graphPostRow(t, fixture, 4, 8)
	userRelated := graphUserRow(t, fixture, 7)
	writePosition := relationPosition(t, fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, mutationir.PositionBranchResult, nil)
	writeNode := relationNode(t, fixture.Post, fixture.PostKey, fixture.PostID, fixture.Post, fixture.Authorship, mutationir.Connect, writePosition)
	write, err := RenderMembershipWrite(MembershipSQLRequest{Node: writeNode, Anchor: postAnchor, Related: userRelated, Effect: MembershipConnect, Registry: fixture.Registry, Provider: policyir.ProviderSQLite, MaxParameters: 10})
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]any, len(write.Columns()))
	dest := make([]any, len(raw))
	for index := range raw {
		dest[index] = &raw[index]
	}
	if err := database.QueryRowContext(context.Background(), write.SQL(), write.Args()...).Scan(dest...); err != nil {
		t.Fatal(err)
	}
	var author string
	if err := database.QueryRow("SELECT author_id FROM posts WHERE id = ?", post).Scan(&author); err != nil {
		t.Fatal(err)
	}
	if author != user7 {
		t.Fatalf("membership write author=%s want %s", author, user7)
	}
}

func TestCompositeCorrelationAndPrimaryKeysPreserveDeclaredOrder(t *testing.T) {
	fixture := schematest.NewCompositeRelation(t)
	tenant := compositeTenantRow(t, fixture, 1, 2)
	item := compositeItemRow(t, fixture, 3, 4, 8, 9)
	position := relationPosition(t, fixture.Tenant, fixture.TenantItems, fixture.Ownership, fixture.Item, mutationir.PositionEntireMembership, nil)
	node := compositeRelationNode(t, fixture.Tenant, fixture.TenantKey, []golem.FieldID{fixture.TenantRegion, fixture.TenantID}, fixture.Item, fixture.Ownership, mutationir.Disconnect, position)
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		program, err := RenderRelationExpansion(RelationExpansionSQLRequest{
			Node: node, Anchor: tenant, Registry: fixture.Registry, Provider: provider,
			Capabilities: relationProof(t, fixture.Registry, provider), MaxRows: 5, MaxParameters: 20,
		})
		if err != nil {
			t.Fatal(err)
		}
		statement := program.Statements()[0]
		first, second := "?1", "?2"
		if provider == policyir.ProviderPostgreSQL {
			first, second = "$1", "$2"
		}
		where := strings.SplitN(statement.SQL(), " WHERE ", 2)[1]
		ownerRegion := strings.Index(where, "owner_region\" = "+first)
		ownerID := strings.Index(where, "owner_id\" = "+second)
		itemRegion := strings.Index(where, "region\" ASC")
		itemID := strings.Index(where, "id\" ASC")
		if ownerRegion < 0 || ownerID <= ownerRegion || itemRegion <= ownerID || itemID <= itemRegion {
			t.Fatalf("provider %d composite relation order changed: %s", provider, statement.SQL())
		}
		if len(statement.Args()) != 2 {
			t.Fatalf("provider %d args=%#v", provider, statement.Args())
		}
	}

	sourcePosition := relationPosition(t, fixture.Item, fixture.ItemOwner, fixture.Ownership, fixture.Tenant, mutationir.PositionBranchResult, nil)
	sourceNode := compositeRelationNode(t, fixture.Item, fixture.ItemKey, []golem.FieldID{fixture.ItemRegion, fixture.ItemID}, fixture.Item, fixture.Ownership, mutationir.Connect, sourcePosition)
	write, err := RenderMembershipWrite(MembershipSQLRequest{
		Node: sourceNode, Anchor: item, Related: tenant, Effect: MembershipConnect,
		Registry: fixture.Registry, Provider: policyir.ProviderPostgreSQL, MaxParameters: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"owner_region\" = $1", "owner_id\" = $2", "region\" = $3", "id\" = $4"} {
		if !strings.Contains(write.SQL(), fragment) {
			t.Fatalf("composite membership SQL lacks %q: %s", fragment, write.SQL())
		}
	}
	if len(write.Args()) != 4 {
		t.Fatalf("composite membership args=%#v", write.Args())
	}
	if _, err := RenderMembershipWrite(MembershipSQLRequest{
		Node: sourceNode, Anchor: item, Related: tenant, Effect: MembershipConnect,
		Registry: fixture.Registry, Provider: policyir.ProviderPostgreSQL, MaxParameters: 3,
	}); err == nil || !strings.Contains(err.Error(), "P4_NESTED_SQL_LIMIT") {
		t.Fatalf("parameter bound was not enforced: %v", err)
	}
}

func TestExecuteRelationSQLDecodesCompleteRowsAndRefusesSentinel(t *testing.T) {
	fixture := schematest.NewGraph(t)
	database, err := sqlx.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE users(id TEXT PRIMARY KEY, name TEXT NOT NULL); CREATE TABLE posts(id TEXT PRIMARY KEY, author_id TEXT NOT NULL, title TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	user := uuidText(7)
	if _, err := database.Exec("INSERT INTO users VALUES (?, ?)", user, "seven"); err != nil {
		t.Fatal(err)
	}
	for index := byte(1); index <= 6; index++ {
		if _, err := database.Exec("INSERT INTO posts VALUES (?, ?, ?)", uuidText(index), user, fmt.Sprintf("post-%d", index)); err != nil {
			t.Fatal(err)
		}
	}
	position := relationPosition(t, fixture.User, fixture.UserPosts, fixture.Authorship, fixture.Post, mutationir.PositionEntireMembership, nil)
	node := relationNode(t, fixture.User, fixture.UserKey, fixture.UserID, fixture.Post, fixture.Authorship, mutationir.Disconnect, position)
	program, err := RenderRelationExpansion(RelationExpansionSQLRequest{
		Node: node, Anchor: graphUserRow(t, fixture, 7), Registry: fixture.Registry,
		Provider: policyir.ProviderSQLite, Capabilities: relationProof(t, fixture.Registry, policyir.ProviderSQLite), MaxRows: 5, MaxParameters: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteRelationSQL(context.Background(), database, fixture.Registry, policyir.ProviderSQLite, program.Statements()[0]); err == nil || !strings.Contains(err.Error(), "P4_NESTED_SQL_EXEC_LIMIT") {
		t.Fatalf("sentinel expansion was not refused: %v", err)
	}
	if _, err := database.Exec("DELETE FROM posts WHERE id = ?", uuidText(1)); err != nil {
		t.Fatal(err)
	}
	missing := graphPostRow(t, fixture, 1, 7)
	writePosition := relationPosition(t, fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, mutationir.PositionBranchResult, nil)
	writeNode := relationNode(t, fixture.Post, fixture.PostKey, fixture.PostID, fixture.Post, fixture.Authorship, mutationir.Connect, writePosition)
	write, err := RenderMembershipWrite(MembershipSQLRequest{
		Node: writeNode, Anchor: missing, Related: graphUserRow(t, fixture, 7), Effect: MembershipConnect,
		Registry: fixture.Registry, Provider: policyir.ProviderSQLite, MaxParameters: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExecuteRelationSQL(context.Background(), database, fixture.Registry, policyir.ProviderSQLite, write); err == nil || !strings.Contains(err.Error(), "P4_NESTED_SQL_EXEC_CARDINALITY") {
		t.Fatalf("zero-row membership update was not refused: %v", err)
	}
}

func TestRelationSetDifferenceUsesCanonicalPrimaryIdentities(t *testing.T) {
	fixture := schematest.NewGraph(t)
	current := []mutationdecode.Row{graphPostRow(t, fixture, 3, 7), graphPostRow(t, fixture, 1, 7)}
	desired := []mutationdecode.Row{graphPostRow(t, fixture, 3, 8), graphPostRow(t, fixture, 2, 8)}
	connect, disconnect, err := RelationSetDifference(fixture.Registry, current, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(connect) != 1 || rowUUIDSuffix(t, connect[0], policyir.FieldID(fixture.PostID)) != 2 {
		t.Fatalf("connect delta=%#v", connect)
	}
	if len(disconnect) != 1 || rowUUIDSuffix(t, disconnect[0], policyir.FieldID(fixture.PostID)) != 1 {
		t.Fatalf("disconnect delta=%#v", disconnect)
	}
	if _, _, err := RelationSetDifference(fixture.Registry, append(current, current[0]), desired); err == nil || !strings.Contains(err.Error(), "P4_NESTED_ROWS_SET") {
		t.Fatalf("duplicate current membership was not refused: %v", err)
	}
	composite := schematest.NewCompositeRelation(t)
	work, err := ExistingRowWork(composite.Registry, compositeItemRow(t, composite, 3, 4, 8, 9))
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := work.Identity()
	if !ok || len(identity.Components()) != 2 || identity.Components()[0].FieldID() != policyir.FieldID(composite.ItemRegion) || identity.Components()[1].FieldID() != policyir.FieldID(composite.ItemID) || len(work.OrderKey()) == 0 {
		t.Fatalf("composite work identity/order is invalid")
	}
	resolved, err := OwnerRelationWork(composite.Registry, compositeItemRow(t, composite, 3, 4, 8, 9), compositeTenantRow(t, composite, 8, 9), MembershipConnect)
	if err != nil {
		t.Fatal(err)
	}
	row, ok := resolved.ResolvedRelationRow()
	if !ok || row.ModelID() != policyir.ModelID(composite.Tenant) || resolved.MembershipEffect() != MembershipConnect {
		t.Fatalf("resolved source-side relation payload was not retained")
	}
}

func TestMembershipNoOpUsesExactSourceAndInverseCorrelationTuple(t *testing.T) {
	fixture := schematest.NewGraph(t)
	post := graphPostRow(t, fixture, 1, 7)
	user7 := graphUserRow(t, fixture, 7)
	user8 := graphUserRow(t, fixture, 8)
	source, ok := fixture.Registry.RelationEndpoint(fixture.Post, fixture.PostAuthor, fixture.Authorship)
	if !ok {
		t.Fatal("source endpoint missing")
	}
	for _, probe := range []struct {
		name    string
		related mutationdecode.Row
		effect  MembershipEffect
		want    bool
	}{
		{"source-connect-same", user7, MembershipConnect, false},
		{"source-connect-different", user8, MembershipConnect, true},
		{"source-disconnect-same", user7, MembershipDisconnect, true},
		{"source-disconnect-different", user8, MembershipDisconnect, false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			got, err := membershipWouldChange(source, post, probe.related, probe.effect)
			if err != nil || got != probe.want {
				t.Fatalf("change=%t want=%t err=%v", got, probe.want, err)
			}
		})
	}
	inverse, ok := fixture.Registry.RelationEndpoint(fixture.User, fixture.UserPosts, fixture.Authorship)
	if !ok {
		t.Fatal("inverse endpoint missing")
	}
	for _, probe := range []struct {
		name   string
		anchor mutationdecode.Row
		effect MembershipEffect
		want   bool
	}{
		{"inverse-connect-same", user7, MembershipConnect, false},
		{"inverse-connect-different", user8, MembershipConnect, true},
		{"inverse-disconnect-same", user7, MembershipDisconnect, true},
		{"inverse-disconnect-different", user8, MembershipDisconnect, false},
	} {
		t.Run(probe.name, func(t *testing.T) {
			got, err := membershipWouldChange(inverse, probe.anchor, post, probe.effect)
			if err != nil || got != probe.want {
				t.Fatalf("change=%t want=%t err=%v", got, probe.want, err)
			}
		})
	}
}

func TestExpandRelationSQLProducesSourceWorkAndExactSetEffects(t *testing.T) {
	fixture := schematest.NewGraph(t)
	database, err := sqlx.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE users(id TEXT PRIMARY KEY, name TEXT NOT NULL); CREATE TABLE posts(id TEXT PRIMARY KEY, author_id TEXT NOT NULL, title TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []byte{7, 8} {
		if _, err := database.Exec("INSERT INTO users VALUES (?, ?)", uuidText(id), fmt.Sprintf("user-%d", id)); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, author byte }{{1, 7}, {2, 8}, {3, 7}} {
		if _, err := database.Exec("INSERT INTO posts VALUES (?, ?, ?)", uuidText(row.id), uuidText(row.author), fmt.Sprintf("post-%d", row.id)); err != nil {
			t.Fatal(err)
		}
	}
	anchorRow := graphPostRow(t, fixture, 1, 7)
	anchor := appliedWithAfter(anchorRow)
	target := targetFor(t, fixture.User, fixture.UserKey, fixture.UserID, 8)
	position := relationPosition(t, fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, mutationir.PositionRelatedTarget, &target)
	node := relationNode(t, fixture.Post, fixture.PostKey, fixture.PostID, fixture.Post, fixture.Authorship, mutationir.Connect, position)
	expansion, err := ExpandRelationSQL(context.Background(), SQLExpansionRequest{
		Expansion: ExpansionRequest{node: node, anchor: &anchor}, Queryer: database, Registry: fixture.Registry,
		Provider: policyir.ProviderSQLite, Capabilities: relationProof(t, fixture.Registry, policyir.ProviderSQLite), MaxRows: 5, MaxParameters: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	works := expansion.Works()
	if len(works) != 1 || works[0].ModelID() != policyir.ModelID(fixture.Post) || works[0].MembershipEffect() != MembershipConnect {
		t.Fatalf("source work=%#v", works)
	}
	resolved, ok := works[0].ResolvedRelationRow()
	if !ok || resolved.ModelID() != policyir.ModelID(fixture.User) || rowUUIDSuffix(t, resolved, policyir.FieldID(fixture.UserID)) != 8 {
		t.Fatalf("source related row was not retained")
	}
	missingTarget := targetFor(t, fixture.User, fixture.UserKey, fixture.UserID, 99)
	missingPosition := relationPosition(t, fixture.Post, fixture.PostAuthor, fixture.Authorship, fixture.User, mutationir.PositionRelatedTarget, &missingTarget)
	missingNode := relationNode(t, fixture.Post, fixture.PostKey, fixture.PostID, fixture.Post, fixture.Authorship, mutationir.Connect, missingPosition)
	_, err = ExpandRelationSQL(context.Background(), SQLExpansionRequest{
		Expansion: ExpansionRequest{node: missingNode, anchor: &anchor}, Queryer: database, Registry: fixture.Registry,
		Provider: policyir.ProviderSQLite, Capabilities: relationProof(t, fixture.Registry, policyir.ProviderSQLite), MaxRows: 5, MaxParameters: 20,
	})
	var notFound *NotFoundError
	if !errors.As(err, &notFound) || notFound.Model != policyir.ModelID(fixture.User) || notFound.Field != policyir.FieldID(fixture.PostAuthor) {
		t.Fatalf("missing exact relation target err=%#v raw=%v", notFound, err)
	}

	userAnchorRow := graphUserRow(t, fixture, 7)
	userAnchor := appliedWithAfter(userAnchorRow)
	desired := []mutationir.Target{
		targetFor(t, fixture.Post, fixture.PostKey, fixture.PostID, 2),
		targetFor(t, fixture.Post, fixture.PostKey, fixture.PostID, 3),
	}
	setExpansion, _ := mutationir.NewExpansionRequirement(mutationir.ExpandSetDifference, 5)
	setPosition, err := mutationir.NewRelationPosition(mutationir.RelationPositionInput{
		ParentModel: policyir.ModelID(fixture.User), Field: policyir.FieldID(fixture.UserPosts), Relation: policyir.RelationID(fixture.Authorship),
		TargetModel: policyir.ModelID(fixture.Post), Kind: mutationir.PositionSetDifference, Desired: desired, Expansion: &setExpansion,
	})
	if err != nil {
		t.Fatal(err)
	}
	setNode := relationNode(t, fixture.User, fixture.UserKey, fixture.UserID, fixture.Post, fixture.Authorship, mutationir.SetRelation, setPosition)
	setResult, err := ExpandRelationSQL(context.Background(), SQLExpansionRequest{
		Expansion: ExpansionRequest{node: setNode, anchor: &userAnchor}, Queryer: database, Registry: fixture.Registry,
		Provider: policyir.ProviderSQLite, Capabilities: relationProof(t, fixture.Registry, policyir.ProviderSQLite), MaxRows: 5, MaxParameters: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	var connectID, disconnectID byte
	for _, work := range setResult.Works() {
		row, _ := work.ResolvedRelationRow()
		switch work.MembershipEffect() {
		case MembershipConnect:
			connectID = rowUUIDSuffix(t, row, policyir.FieldID(fixture.PostID))
		case MembershipDisconnect:
			disconnectID = rowUUIDSuffix(t, row, policyir.FieldID(fixture.PostID))
		}
	}
	if connectID != 2 || disconnectID != 1 {
		t.Fatalf("set effects connect=%d disconnect=%d", connectID, disconnectID)
	}
}

func relationProof(t *testing.T, registry interface {
	ModelFingerprint() golem.SchemaDigest
}, provider policyir.Provider) policysql.CapabilityProof {
	t.Helper()
	proof, err := policysql.NewCapabilityProof(provider, [32]byte(registry.ModelFingerprint()), policyir.CapabilityBinaryText, policyir.CapabilityASCIIInsensitiveText, policyir.CapabilityExactJSON, policyir.CapabilityScalarListJSON, policyir.CapabilityRelationCorrelation)
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func relationPosition(t *testing.T, parent golem.ModelID, field golem.FieldID, relation golem.RelationID, target golem.ModelID, kind mutationir.RelationPositionKind, targetValue *mutationir.Target) mutationir.RelationPosition {
	t.Helper()
	input := mutationir.RelationPositionInput{ParentModel: policyir.ModelID(parent), Field: policyir.FieldID(field), Relation: policyir.RelationID(relation), TargetModel: policyir.ModelID(target), Kind: kind, Target: targetValue}
	if kind == mutationir.PositionEntireMembership {
		expansion, _ := mutationir.NewExpansionRequirement(mutationir.ExpandEntireMembership, 5)
		input.Expansion = &expansion
	}
	value, err := mutationir.NewRelationPosition(input)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func relationNode(t *testing.T, parent golem.ModelID, parentKey golem.KeyID, parentField golem.FieldID, nodeModel golem.ModelID, relation golem.RelationID, operation mutationir.Operation, position mutationir.RelationPosition) mutationir.Node {
	t.Helper()
	rootTarget := targetFor(t, parent, parentKey, parentField, 7)
	identity := mutationir.IdentityUnchanged
	if operation == mutationir.UpdateMany || operation == mutationir.DeleteMany {
		identity = mutationir.IdentityBatchChangeRefused
	}
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Update, Model: policyir.ModelID(parent), Target: &rootTarget, Identity: mutationir.IdentityUnchanged, Children: []mutationir.NodeInput{{Operation: operation, Model: policyir.ModelID(nodeModel), Relation: policyir.RelationID(relation), RelationPosition: &position, Identity: identity}}})
	if err != nil {
		t.Fatal(err)
	}
	return graph.Nodes()[1]
}

func compositeRelationNode(t *testing.T, parent golem.ModelID, parentKey golem.KeyID, parentFields []golem.FieldID, nodeModel golem.ModelID, relation golem.RelationID, operation mutationir.Operation, position mutationir.RelationPosition) mutationir.Node {
	t.Helper()
	selectors := make([]mutationir.SelectorValue, len(parentFields))
	for index, field := range parentFields {
		selectors[index], _ = mutationir.NewSelectorValue(policyir.FieldID(field), policyir.UUIDValue([16]byte{15: byte(index + 1)}))
	}
	rootTarget, err := mutationir.NewTarget(policyir.ModelID(parent), parentKey, selectors, nil)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := mutationir.NewGraph(mutationir.NodeInput{Operation: mutationir.Update, Model: policyir.ModelID(parent), Target: &rootTarget, Identity: mutationir.IdentityUnchanged, Children: []mutationir.NodeInput{{Operation: operation, Model: policyir.ModelID(nodeModel), Relation: policyir.RelationID(relation), RelationPosition: &position, Identity: mutationir.IdentityUnchanged}}})
	if err != nil {
		t.Fatal(err)
	}
	return graph.Nodes()[1]
}

func targetFor(t *testing.T, model golem.ModelID, key golem.KeyID, field golem.FieldID, last byte) mutationir.Target {
	t.Helper()
	selector, _ := mutationir.NewSelectorValue(policyir.FieldID(field), policyir.UUIDValue([16]byte{15: last}))
	target, err := mutationir.NewTarget(policyir.ModelID(model), key, []mutationir.SelectorValue{selector}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func graphUserRow(t *testing.T, fixture schematest.GraphFixture, last byte) mutationdecode.Row {
	t.Helper()
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.User), []mutationdecode.Cell{mutationdecode.Value(policyir.FieldID(fixture.UserID), policyir.UUIDValue([16]byte{15: last}))})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func graphPostRow(t *testing.T, fixture schematest.GraphFixture, post, author byte) mutationdecode.Row {
	t.Helper()
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Post), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.PostID), policyir.UUIDValue([16]byte{15: post})),
		mutationdecode.Value(policyir.FieldID(fixture.AuthorID), policyir.UUIDValue([16]byte{15: author})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func compositeTenantRow(t *testing.T, fixture schematest.CompositeRelationFixture, region, id byte) mutationdecode.Row {
	t.Helper()
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Tenant), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.TenantRegion), policyir.UUIDValue([16]byte{15: region})),
		mutationdecode.Value(policyir.FieldID(fixture.TenantID), policyir.UUIDValue([16]byte{15: id})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func compositeItemRow(t *testing.T, fixture schematest.CompositeRelationFixture, region, id, ownerRegion, ownerID byte) mutationdecode.Row {
	t.Helper()
	row, err := mutationdecode.NewRow(fixture.Registry, policyir.ModelID(fixture.Item), []mutationdecode.Cell{
		mutationdecode.Value(policyir.FieldID(fixture.ItemRegion), policyir.UUIDValue([16]byte{15: region})),
		mutationdecode.Value(policyir.FieldID(fixture.ItemID), policyir.UUIDValue([16]byte{15: id})),
		mutationdecode.Value(policyir.FieldID(fixture.OwnerRegion), policyir.UUIDValue([16]byte{15: ownerRegion})),
		mutationdecode.Value(policyir.FieldID(fixture.OwnerID), policyir.UUIDValue([16]byte{15: ownerID})),
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func uuidText(last byte) string {
	return fmt.Sprintf("00000000-0000-0000-0000-%012x", last)
}

func rowUUIDSuffix(t *testing.T, row mutationdecode.Row, field policyir.FieldID) byte {
	t.Helper()
	cell, ok := row.Cell(field)
	if !ok {
		t.Fatalf("row field %x absent", field)
	}
	value, ok := cell.PolicyValue()
	if !ok {
		t.Fatalf("row field %x is NULL", field)
	}
	id, ok := value.UUID()
	if !ok {
		t.Fatalf("row field %x is not UUID", field)
	}
	return id[15]
}

func appliedWithAfter(row mutationdecode.Row) AppliedNode {
	return AppliedNode{result: NewApplyResult(nil, &row)}
}
