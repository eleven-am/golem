package runtime

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
)

func requireIdentity(t *testing.T, label string, identity golem.RuntimeRowIdentity, fields []golem.FieldID, values []any) {
	t.Helper()
	if !identity.Present() {
		t.Fatalf("%s: row has no identity", label)
	}
	if got := identity.Fields(); !reflect.DeepEqual(got, fields) {
		t.Fatalf("%s: identity fields=%v want %v", label, got, fields)
	}
	if got := identity.Values(); !reflect.DeepEqual(got, values) {
		t.Fatalf("%s: identity values=%v want %v", label, got, values)
	}
}

func TestReadRowsCarryAHiddenIdentityForEveryProjection(t *testing.T) {
	runIdentityGuard(t, func(t *testing.T, h *identityGuardHarness) {
		ctx := context.Background()
		keyFields := []golem.FieldID{h.schema.PostID}
		wantIDs := []golem.UUID{{15: 11}, {15: 12}, {15: 13}, {15: 14}, {15: 15}, {15: 16}}
		tokens := map[string][]golem.RuntimeSemanticIdentityToken{}
		for _, reader := range h.readers(t) {
			for _, projection := range []struct {
				name   string
				option golem.ReadOption[identityGuardPost]
			}{
				{"title only", golem.Select[identityGuardPost](h.postTitle)},
				{"key selected", golem.Select[identityGuardPost](h.postID, h.postTitle)},
			} {
				rows, err := reader.posts(ctx, golem.OrderBy(h.postID.Asc()), projection.option)
				if err != nil {
					t.Fatalf("%s/%s: %v", reader.name, projection.name, err)
				}
				if len(rows) != len(wantIDs) {
					t.Fatalf("%s/%s: rows=%d", reader.name, projection.name, len(rows))
				}
				for index, row := range rows {
					label := reader.name + "/" + projection.name
					requireIdentity(t, label, golem.RuntimeTypedRowIdentity(row), keyFields, []any{wantIDs[index]})
					if selected := golem.Value(row, h.postID).IsSelected(); selected != (projection.name == "key selected") {
						t.Fatalf("%s: key cell selected=%t", label, selected)
					}
					tokens[label] = append(tokens[label], golem.RuntimeTypedRowIdentity(row).Token())
				}
			}
		}
		for label, got := range tokens {
			if !reflect.DeepEqual(got, tokens["system/key selected"]) {
				t.Fatalf("%s: tokens differ from the key-selected system read", label)
			}
		}

		strategies := []struct {
			name string
			ctx  context.Context
		}{
			{"production_correlated", ctx},
			{"forced_batched", context.WithValue(ctx, relationLoadStrategyContextKey{}, relationLoadBatched)},
		}
		for _, strategy := range strategies {
			for _, reader := range h.readers(t) {
				label := strategy.name + "/" + reader.name
				users, err := reader.users(strategy.ctx, golem.OrderBy(h.userID.Asc()), golem.Select[identityGuardUser](h.userName,
					h.userPosts.Args(golem.OrderBy(h.postID.Asc()), golem.Select[identityGuardPost](h.postTitle))))
				if err != nil {
					t.Fatalf("%s: %v", label, err)
				}
				if len(users) != 5 {
					t.Fatalf("%s: users=%d", label, len(users))
				}
				for index, user := range users {
					requireIdentity(t, label+" user", golem.RuntimeTypedRowIdentity(user), []golem.FieldID{h.schema.UserID}, []any{golem.UUID{15: byte(index + 1)}})
				}
				children, present := golem.Many(users[0], h.userPosts).Get()
				if !present || len(children) != 4 {
					t.Fatalf("%s: alice children=%d present=%t", label, len(children), present)
				}
				for index, id := range []byte{11, 12, 14, 16} {
					requireIdentity(t, label+" child", golem.RuntimeTypedRowIdentity(children[index]), keyFields, []any{golem.UUID{15: id}})
					if golem.Value(children[index], h.postID).IsSelected() {
						t.Fatalf("%s: child key became a cell", label)
					}
				}
			}
		}
	})
}

func TestUniqueReadRowCarriesIdentityWithoutTheKeyCell(t *testing.T) {
	runIdentityGuard(t, func(t *testing.T, h *identityGuardHarness) {
		ctx := context.Background()
		caller, err := h.app.ForPrincipal(ctx, identityGuardPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		selector := golem.GeneratedUniqueSelectorValue[identityGuardUser](h.schema.User, h.schema.UserKey, golem.GeneratedSelectorComponent(h.schema.UserID, golem.UUID{15: 2}))
		projection := golem.Select[identityGuardUser](h.userName)
		systemRow, err := SystemFindUnique(ctx, h.app.System(), h.users, selector, projection)
		if err != nil {
			t.Fatal(err)
		}
		callerRow, err := CallerFindUnique(ctx, caller, h.users, selector, projection)
		if err != nil {
			t.Fatal(err)
		}
		fields := []golem.FieldID{h.schema.UserID}
		requireIdentity(t, "system unique", golem.RuntimeTypedRowIdentity(systemRow), fields, []any{golem.UUID{15: 2}})
		requireIdentity(t, "caller unique", golem.RuntimeTypedRowIdentity(callerRow), fields, []any{golem.UUID{15: 2}})
		if golem.RuntimeTypedRowIdentity(systemRow).Token() != golem.RuntimeTypedRowIdentity(callerRow).Token() {
			t.Fatal("system and caller identities of one row differ")
		}
		if golem.Value(systemRow, h.userID).IsSelected() || golem.Value(callerRow, h.userID).IsSelected() {
			t.Fatal("an unselected key became a visible cell")
		}
	})
}

func TestMaskedKeyRowsCarryIdentityOnlyWhereTheMaskIsDischarged(t *testing.T) {
	runIdentityGuardWithKeyMask(t, true, func(t *testing.T, h *identityGuardHarness) {
		ctx := context.Background()
		caller, err := h.app.ForPrincipal(ctx, identityGuardPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		rows, err := CallerFindMany(ctx, caller, h.posts, golem.Where(h.postTitle.StartsWith("p-a")), golem.OrderBy(h.postTitle.Asc()), golem.Select[identityGuardPost](h.postTitle))
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 3 {
			t.Fatalf("rows=%d", len(rows))
		}
		for index, id := range []byte{11, 15, 16} {
			requireIdentity(t, "discharged", golem.RuntimeTypedRowIdentity(rows[index]), []golem.FieldID{h.schema.PostID}, []any{golem.UUID{15: id}})
			if golem.Value(rows[index], h.postID).IsSelected() {
				t.Fatal("the masked key became a visible cell")
			}
		}
		refused, err := CallerFindMany(ctx, caller, h.posts, golem.Select[identityGuardPost](h.postTitle))
		if err == nil || len(refused) != 0 {
			t.Fatalf("undischarged masked key read rows=%d err=%v", len(refused), err)
		}
	})
}

func TestCompositeKeyIdentityFollowsDeclaredKeyOrder(t *testing.T) {
	check := func(t *testing.T, fixture reversedCompositeFixture) {
		ctx := context.Background()
		system := fixture.app.System()
		if _, err := SystemCreate(ctx, system, fixture.tenantDescriptor, golem.GeneratedCreateInput[compositeMutationTenant](fixture.schema.Tenant,
			golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantRegion, golem.UUID{15: 21}),
			golem.GeneratedCreateFieldValue(fixture.schema.Tenant, fixture.tenantID, golem.UUID{15: 22}),
		)); err != nil {
			t.Fatal(err)
		}
		created, err := SystemCreate(ctx, system, fixture.itemDescriptor, golem.GeneratedCreateInput[compositeMutationItem](fixture.schema.Item,
			golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemRegion, golem.UUID{15: 11}),
			golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.itemID, golem.UUID{15: 12}),
			golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.ownerRegion, golem.UUID{15: 21}),
			golem.GeneratedCreateFieldValue(fixture.schema.Item, fixture.ownerID, golem.UUID{15: 22}),
		), golem.Select[compositeMutationItem](fixture.ownerID))
		if err != nil {
			t.Fatal(err)
		}
		fields := []golem.FieldID{fixture.schema.ItemID, fixture.schema.ItemRegion}
		values := []any{golem.UUID{15: 12}, golem.UUID{15: 11}}
		requireIdentity(t, "created item", golem.RuntimeTypedRowIdentity(created), fields, values)
		read, err := SystemFindMany(ctx, system, fixture.itemDescriptor, golem.Select[compositeMutationItem](fixture.ownerRegion))
		if err != nil || len(read) != 1 {
			t.Fatalf("rows=%d err=%v", len(read), err)
		}
		requireIdentity(t, "read item", golem.RuntimeTypedRowIdentity(read[0]), fields, values)
		if golem.RuntimeTypedRowIdentity(read[0]).Token() != golem.RuntimeTypedRowIdentity(created).Token() {
			t.Fatal("the created and re-read item have different identities")
		}
		tenants, err := SystemFindMany(ctx, system, fixture.tenantDescriptor, golem.Select[compositeMutationTenant](fixture.itemsOf.Args(golem.Select[compositeMutationItem](fixture.ownerID))))
		if err != nil || len(tenants) != 1 {
			t.Fatalf("tenants=%d err=%v", len(tenants), err)
		}
		children, present := golem.Many(tenants[0], fixture.itemsOf).Get()
		if !present || len(children) != 1 {
			t.Fatalf("children=%d present=%t", len(children), present)
		}
		requireIdentity(t, "relation child", golem.RuntimeTypedRowIdentity(children[0]), fields, values)
	}
	t.Run("sqlite", func(t *testing.T) {
		check(t, openReversedComposite(t, postgresAcceptanceProfile{}, golem.SQLite))
	})
	for _, profile := range postgresAcceptanceProfiles() {
		profile := profile
		t.Run("postgresql-"+profile.name, func(t *testing.T) {
			check(t, openReversedComposite(t, profile, golem.PostgreSQL))
		})
	}
}

func TestBytesKeyIdentityDistinguishesEmptyAndNonEmptyKeys(t *testing.T) {
	runExactValueProfilesFor(t, schematest.NewBytesKeyed, func(t *testing.T, profile exactValueProfile) {
		seedBytesKeyedRows(t, profile.database, profile.provider, profile.prefix)
		reader := newBytesKeyedReader(t, profile.database, guardProvider(profile), profile.fixture, nil)
		rows, err := SystemFindMany(context.Background(), reader.fixture.app.System(), reader.posts, golem.OrderBy(reader.title.Asc()), golem.Select[mutationResultPost](reader.title))
		if err != nil || len(rows) != 2 {
			t.Fatalf("rows=%d err=%v", len(rows), err)
		}
		fields := []golem.FieldID{profile.fixture.PostID, profile.fixture.PostTitle}
		tokens := map[golem.RuntimeSemanticIdentityToken]bool{}
		for index, want := range [][]byte{{}, {1}} {
			identity := golem.RuntimeTypedRowIdentity(rows[index])
			requireIdentity(t, "bytes key", identity, fields, []any{want, "alice"})
			tokens[identity.Token()] = true
		}
		if len(tokens) != 2 {
			t.Fatal("empty and non-empty bytes keys share an identity")
		}
	})
}

func TestDateTimeKeyIdentityAndMutationResultIdentity(t *testing.T) {
	runExactValueProfilesFor(t, schematest.NewDateTimeKeyed, func(t *testing.T, profile exactValueProfile) {
		ctx := context.Background()
		fixture := newDateTimeGuardFixture(t, profile)
		system := fixture.app.System()
		schema := fixture.schema
		if _, err := SystemCreate(ctx, system, fixture.users, golem.GeneratedCreateInput[mutationResultUser](schema.User,
			golem.GeneratedCreateFieldValue(schema.User, golem.GeneratedEqualField[mutationResultUser, golem.UUID](schema.UserID), golem.UUID{15: 201}),
			golem.GeneratedCreateFieldValue(schema.User, golem.GeneratedTextField[mutationResultUser, string](schema.UserName), "identity-author"),
		)); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2026, 10, 25, 12, 0, 0, 123456000, time.UTC)
		created, err := SystemCreate(ctx, system, fixture.posts, fixture.createInput(31, at, "first"), golem.Select[mutationResultPost](fixture.title))
		if err != nil {
			t.Fatal(err)
		}
		fields := []golem.FieldID{schema.PostID, schema.PostAt}
		identity := golem.RuntimeTypedRowIdentity(created)
		requireIdentity(t, "created post", identity, fields, []any{golem.UUID{15: 31}, at})
		if golem.Value(created, fixture.postID).IsSelected() {
			t.Fatal("the key became a cell of the mutation result")
		}
		read, err := SystemFindUnique(ctx, system, fixture.posts, fixture.selector(31, at), golem.Select[mutationResultPost](fixture.title))
		if err != nil {
			t.Fatal(err)
		}
		if golem.RuntimeTypedRowIdentity(read).Token() != identity.Token() {
			t.Fatal("the created and re-read post have different identities")
		}
		updated, err := SystemUpdate(ctx, system, fixture.posts, fixture.selector(31, at), fixture.retitle("second"), golem.Select[mutationResultPost](fixture.title))
		if err != nil {
			t.Fatal(err)
		}
		if golem.RuntimeTypedRowIdentity(updated).Token() != identity.Token() {
			t.Fatal("the updated post has a different identity")
		}
	})
}

func TestMaskedKeyForAReadRowIsAnExplicitIdentityError(t *testing.T) {
	runIdentityGuardWithKeyMask(t, true, func(t *testing.T, h *identityGuardHarness) {
		ctx := context.Background()
		caller, err := h.app.ForPrincipal(ctx, identityGuardPrincipal{})
		if err != nil {
			t.Fatal(err)
		}
		callerRequest, err := golem.FreezeFindMany(h.posts, golem.Where(h.postTitle.StartsWith("p-a")), golem.OrderBy(h.postTitle.Asc()), golem.Select[identityGuardPost](h.postID, h.postTitle))
		if err != nil {
			t.Fatal(err)
		}
		callerPrepared, err := caller.Prepare(callerRequest)
		if err != nil {
			t.Fatal(err)
		}
		callerPlan, err := preparePlan(callerPrepared, h.app.registry, h.app.readLimits.plan)
		if err != nil {
			t.Fatal(err)
		}
		callerRows, err := executePlan(ctx, h.app, callerPrepared.executor, callerPrepared.Operation(), callerPlan)
		if err != nil || len(callerRows) == 0 {
			t.Fatalf("caller rows=%d err=%v", len(callerRows), err)
		}
		systemRequest, err := golem.FreezeFindMany(h.posts, golem.Where(h.postTitle.Eq("p-b")), golem.Select[identityGuardPost](h.postTitle))
		if err != nil {
			t.Fatal(err)
		}
		systemPrepared, err := h.app.System().Prepare(systemRequest)
		if err != nil {
			t.Fatal(err)
		}
		systemPlan, err := preparePlan(systemPrepared, h.app.registry, h.app.readLimits.plan)
		if err != nil {
			t.Fatal(err)
		}
		systemRows, err := executePlan(ctx, h.app, systemPrepared.executor, systemPrepared.Operation(), systemPlan)
		if err != nil || len(systemRows) != 1 {
			t.Fatalf("system rows=%d err=%v", len(systemRows), err)
		}

		identity, err := plannedRowIdentity(h.app, callerPlan, callerPlan.Fields(), callerRows[0].values, callerRows[0].record)
		if err != nil || !identity.Present() {
			t.Fatalf("visible key: present=%t err=%v", identity.Present(), err)
		}
		masked, err := plannedRowIdentity(h.app, callerPlan, callerPlan.Fields(), callerRows[0].values, systemRows[0].record)
		want := fmt.Sprintf("P3_RUNTIME_IDENTITY: model=%x field=%x: primary-key field is masked for a row that was read", callerPlan.ModelID(), h.schema.PostID)
		if err == nil || err.Error() != want || masked.Present() {
			t.Fatalf("masked key: present=%t err=%v want %q", masked.Present(), err, want)
		}
	})
}
