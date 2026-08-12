package schema

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
)

func TestRegistryMapsPhysicalPlanObjectsToStableTypedIdentities(t *testing.T) {
	bundle, ids := testBundle(t)
	bundle = bundleWithPlanAccessObjects(t, bundle, "uq_posts_author", "idx_posts_author", "uidx_posts_price")
	registry, err := New(bundle)
	if err != nil {
		t.Fatal(err)
	}

	for _, provider := range []golem.Provider{golem.SQLite, golem.PostgreSQL} {
		t.Run(string(provider), func(t *testing.T) {
			model, ok := registry.PhysicalModelIDByName(provider, "posts")
			if !ok || model != ids.post {
				t.Fatalf("posts lookup = %x, %v", model, ok)
			}

			primary, ok := registry.PhysicalAccessObjectByName(provider, "pk_posts")
			wantPrimary, keyErr := keyID(compilerir.KeyID(testID(42)))
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			if !ok || primary.Kind() != PhysicalAccessPrimaryKey || primary.ModelID() != ids.post {
				t.Fatalf("primary lookup = %#v, %v", primary, ok)
			}
			if got, present := primary.KeyID(); !present || got != wantPrimary {
				t.Fatalf("primary key ID = %x, %v", got, present)
			}
			if _, present := primary.IndexID(); present {
				t.Fatal("primary access object also claimed an index identity")
			}

			unique, ok := registry.PhysicalAccessObjectByName(provider, "uq_posts_author")
			wantUnique, keyErr := keyID(compilerir.KeyID(testID(51)))
			if keyErr != nil {
				t.Fatal(keyErr)
			}
			if !ok || unique.Kind() != PhysicalAccessUniqueIndex || unique.ModelID() != ids.post {
				t.Fatalf("unique lookup = %#v, %v", unique, ok)
			}
			if got, present := unique.KeyID(); !present || got != wantUnique {
				t.Fatalf("unique key ID = %x, %v", got, present)
			}

			ordinary, ok := registry.PhysicalAccessObjectByName(provider, "idx_posts_author")
			wantOrdinary := mustPhysicalIndexID(t, compilerir.IndexID(testID(52)))
			if !ok || ordinary.Kind() != PhysicalAccessIndex || ordinary.ModelID() != ids.post {
				t.Fatalf("ordinary lookup = %#v, %v", ordinary, ok)
			}
			if got, present := ordinary.IndexID(); !present || got != wantOrdinary {
				t.Fatalf("ordinary index ID = %x, %v", got, present)
			}
			if _, present := ordinary.KeyID(); present {
				t.Fatal("ordinary index access object also claimed a key identity")
			}

			uniqueIndex, ok := registry.PhysicalAccessObjectByName(provider, "uidx_posts_price")
			wantUniqueIndex := mustPhysicalIndexID(t, compilerir.IndexID(testID(53)))
			if !ok || uniqueIndex.Kind() != PhysicalAccessUniqueIndex || uniqueIndex.ModelID() != ids.post {
				t.Fatalf("unique physical index lookup = %#v, %v", uniqueIndex, ok)
			}
			if got, present := uniqueIndex.IndexID(); !present || got != wantUniqueIndex {
				t.Fatalf("unique physical index ID = %x, %v", got, present)
			}

			// Returned facts are values with fixed-width IDs. Corrupting a caller's
			// copy cannot alter the registry's privately owned lookup entry.
			ordinary.indexID[0] ^= 0xff
			fresh, freshOK := registry.PhysicalAccessObjectByName(provider, "idx_posts_author")
			freshID, freshIDOK := fresh.IndexID()
			if !freshOK || !freshIDOK || freshID != wantOrdinary {
				t.Fatal("physical access lookup leaked mutable registry storage")
			}
		})
	}
}

func TestRegistryMapsPhysicalKeysByModelAndExactStableFieldSequence(t *testing.T) {
	bundle, ids := testBundle(t)
	bundle = bundleWithPlanAccessObjects(t, bundle, "uq_posts_author_price", "idx_posts_author", "uidx_posts_price")
	registry, err := New(bundle)
	if err != nil {
		t.Fatal(err)
	}

	wantPrimary, keyErr := keyID(compilerir.KeyID(testID(42)))
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	wantUnique, keyErr := keyID(compilerir.KeyID(testID(51)))
	if keyErr != nil {
		t.Fatal(keyErr)
	}

	for _, provider := range []golem.Provider{golem.SQLite, golem.PostgreSQL} {
		t.Run(string(provider), func(t *testing.T) {
			facts := registry.PhysicalKeyAccessObjects(provider, ids.post)
			if len(facts) != 2 {
				t.Fatalf("key access inventory = %#v", facts)
			}
			if facts[0].Kind() != PhysicalAccessPrimaryKey || facts[0].ModelID() != ids.post {
				t.Fatalf("primary fact = %#v", facts[0])
			}
			if got, ok := facts[0].KeyID(); !ok || got != wantPrimary {
				t.Fatalf("primary key = %x, %v", got, ok)
			}
			if got := facts[0].FieldIDs(); !reflect.DeepEqual(got, []golem.FieldID{ids.postID}) {
				t.Fatalf("primary fields = %x", got)
			}
			if facts[1].Kind() != PhysicalAccessUniqueIndex {
				t.Fatalf("unique fact = %#v", facts[1])
			}
			if got, ok := facts[1].KeyID(); !ok || got != wantUnique {
				t.Fatalf("unique key = %x, %v", got, ok)
			}
			wantUniqueFields := []golem.FieldID{ids.author, ids.price}
			if got := facts[1].FieldIDs(); !reflect.DeepEqual(got, wantUniqueFields) {
				t.Fatalf("unique fields = %x", got)
			}
			copiedFields := facts[1].FieldIDs()
			copiedFields[0] = golem.FieldID{0xee}
			if !reflect.DeepEqual(facts[1].FieldIDs(), wantUniqueFields) {
				t.Fatal("physical key FieldIDs accessor leaked mutable fact storage")
			}

			matched, ok := registry.PhysicalKeyAccessByFields(provider, ids.post, PhysicalAccessUniqueIndex, wantUniqueFields)
			if !ok || matched.Kind() != PhysicalAccessUniqueIndex {
				t.Fatalf("exact unique lookup = %#v, %v", matched, ok)
			}
			if got, present := matched.KeyID(); !present || got != wantUnique {
				t.Fatalf("exact unique key = %x, %v", got, present)
			}
			if _, ok := registry.PhysicalKeyAccessByFields(provider, ids.post, PhysicalAccessUniqueIndex, []golem.FieldID{ids.price, ids.author}); ok {
				t.Fatal("reordered unique fields resolved")
			}
			if _, ok := registry.PhysicalKeyAccessByFields(provider, ids.post, PhysicalAccessIndex, wantUniqueFields); ok {
				t.Fatal("ordinary-index kind resolved from the key inventory")
			}

			// Both the inventory and each fact's field sequence are caller-owned.
			facts[0].fields[0] = golem.FieldID{0xff}
			facts[1] = PhysicalAccessObject{}
			fresh := registry.PhysicalKeyAccessObjects(provider, ids.post)
			if len(fresh) != 2 || !reflect.DeepEqual(fresh[0].FieldIDs(), []golem.FieldID{ids.postID}) || !reflect.DeepEqual(fresh[1].FieldIDs(), wantUniqueFields) {
				t.Fatal("physical key inventory leaked mutable registry storage")
			}
		})
	}
}

func TestRegistryPhysicalKeyFieldLookupRefusesAmbiguousReviewedSequence(t *testing.T) {
	bundle, ids := testBundle(t)
	bundle = rewriteProviderSchemas(t, bundle, func(schema *physical.PhysicalSchema) {
		post := tableByPhysicalID(t, schema, compilerir.ModelID(testID(2)))
		author, price := compilerir.FieldID(testID(22)), compilerir.FieldID(testID(21))
		post.Uniques = append(post.Uniques,
			physical.PhysicalKey{ID: compilerir.KeyID(testID(71)), Name: "uq_posts_author_price_first", Columns: []compilerir.FieldID{author, price}},
			physical.PhysicalKey{ID: compilerir.KeyID(testID(72)), Name: "uq_posts_author_price_second", Columns: []compilerir.FieldID{author, price}},
		)
	})
	registry, err := New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []golem.Provider{golem.SQLite, golem.PostgreSQL} {
		if facts := registry.PhysicalKeyAccessObjects(provider, ids.post); len(facts) != 3 {
			t.Fatalf("%s key inventory = %#v", provider, facts)
		}
		if _, ok := registry.PhysicalKeyAccessByFields(provider, ids.post, PhysicalAccessUniqueIndex, []golem.FieldID{ids.author, ids.price}); ok {
			t.Fatalf("%s guessed between duplicate reviewed key sequences", provider)
		}
	}
}

func TestRegistryPhysicalPlanObjectLookupFailsClosedForUnknownInputs(t *testing.T) {
	bundle, _ := testBundle(t)
	registry, err := New(bundle)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := registry.PhysicalModelIDByName(golem.SQLite, "unknown_plan_table_canary"); ok {
		t.Fatal("unknown table name resolved")
	}
	if _, ok := registry.PhysicalAccessObjectByName(golem.SQLite, "unknown_plan_access_canary"); ok {
		t.Fatal("unknown access-object name resolved")
	}
	if _, ok := registry.PhysicalModelIDByName(golem.Provider("unknown-provider"), "posts"); ok {
		t.Fatal("unknown provider resolved a table")
	}
	if _, ok := registry.PhysicalAccessObjectByName(golem.Provider("unknown-provider"), "pk_posts"); ok {
		t.Fatal("unknown provider resolved an access object")
	}

	var absent *Registry
	if _, ok := absent.PhysicalModelIDByName(golem.SQLite, "posts"); ok {
		t.Fatal("nil registry resolved a table")
	}
	if _, ok := absent.PhysicalAccessObjectByName(golem.SQLite, "pk_posts"); ok {
		t.Fatal("nil registry resolved an access object")
	}
}

func TestRegistryPhysicalPlanObjectLookupIsProviderScoped(t *testing.T) {
	bundle, ids := testBundle(t)
	bundle = rewriteProviderSchemas(t, bundle, func(schema *physical.PhysicalSchema) {
		if schema.Provider.Provider != compilerir.PostgreSQL {
			return
		}
		post := tableByPhysicalID(t, schema, compilerir.ModelID(testID(2)))
		author := compilerir.FieldID(testID(22))
		post.Indexes = append(post.Indexes, physical.PhysicalIndex{ID: compilerir.IndexID(testID(54)), Name: "idx_posts_pg_only", Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &author, Direction: compilerir.SortAsc, Nulls: compilerir.NullsDefault}}, CreationMode: physical.IndexTransactional})
	})
	registry, err := New(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.PhysicalAccessObjectByName(golem.SQLite, "idx_posts_pg_only"); ok {
		t.Fatal("SQLite resolved a PostgreSQL-only access object")
	}
	value, ok := registry.PhysicalAccessObjectByName(golem.PostgreSQL, "idx_posts_pg_only")
	if !ok || value.Kind() != PhysicalAccessIndex || value.ModelID() != ids.post {
		t.Fatalf("PostgreSQL-only access object = %#v, %v", value, ok)
	}
}

func TestPhysicalPlanAccessFactContainsNoPhysicalNameSurface(t *testing.T) {
	typ := reflect.TypeOf(PhysicalAccessObject{})
	for index := 0; index < typ.NumField(); index++ {
		field := typ.Field(index)
		if field.PkgPath == "" || strings.Contains(strings.ToLower(field.Name), "name") || field.Type == reflect.TypeOf(physical.PhysicalName("")) {
			t.Fatalf("physical access fact exposes field %s (%s)", field.Name, field.Type)
		}
	}
	if _, ok := typ.MethodByName("Name"); ok {
		t.Fatal("physical access fact exposes a Name method")
	}
}

func TestRegistryRejectsCrossTableAmbiguousPhysicalPlanAccessNameWithoutEcho(t *testing.T) {
	const canary = "query_plan_private_collision_canary"
	bundle, _ := testBundle(t)
	bundle = bundleWithCrossTableAccessCollision(t, bundle, canary)

	_, err := New(bundle)
	var schemaErr *Error
	if !errors.As(err, &schemaErr) || schemaErr.Code != CodePhysical {
		t.Fatalf("error = %v, want %s", err, CodePhysical)
	}
	if strings.Contains(err.Error(), canary) {
		t.Fatalf("bootstrap refusal echoed untrusted physical name: %v", err)
	}
}

func bundleWithPlanAccessObjects(t *testing.T, bundle golem.SchemaBundle, uniqueName, indexName, uniqueIndexName physical.PhysicalName) golem.SchemaBundle {
	t.Helper()
	return rewriteProviderSchemas(t, bundle, func(schema *physical.PhysicalSchema) {
		post := tableByPhysicalID(t, schema, compilerir.ModelID(testID(2)))
		author, price := compilerir.FieldID(testID(22)), compilerir.FieldID(testID(21))
		post.Uniques = append(post.Uniques, physical.PhysicalKey{ID: compilerir.KeyID(testID(51)), Name: uniqueName, Columns: []compilerir.FieldID{author, price}})
		post.Indexes = append(post.Indexes,
			physical.PhysicalIndex{ID: compilerir.IndexID(testID(52)), Name: indexName, Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &author, Direction: compilerir.SortAsc, Nulls: compilerir.NullsDefault}}, CreationMode: physical.IndexTransactional},
			physical.PhysicalIndex{ID: compilerir.IndexID(testID(53)), Name: uniqueIndexName, Unique: true, Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &price, Direction: compilerir.SortAsc, Nulls: compilerir.NullsDefault}}, CreationMode: physical.IndexTransactional},
		)
	})
}

func bundleWithCrossTableAccessCollision(t *testing.T, bundle golem.SchemaBundle, canary physical.PhysicalName) golem.SchemaBundle {
	t.Helper()
	return rewriteProviderSchemas(t, bundle, func(schema *physical.PhysicalSchema) {
		user := tableByPhysicalID(t, schema, compilerir.ModelID(testID(1)))
		post := tableByPhysicalID(t, schema, compilerir.ModelID(testID(2)))
		userID, author := compilerir.FieldID(testID(11)), compilerir.FieldID(testID(22))
		user.Uniques = append(user.Uniques, physical.PhysicalKey{ID: compilerir.KeyID(testID(61)), Name: canary, Columns: []compilerir.FieldID{userID}})
		post.Indexes = append(post.Indexes, physical.PhysicalIndex{ID: compilerir.IndexID(testID(62)), Name: canary, Method: physical.IndexBTree, Keys: []physical.IndexKey{{Column: &author, Direction: compilerir.SortAsc, Nulls: compilerir.NullsDefault}}, CreationMode: physical.IndexTransactional})
	})
}

func rewriteProviderSchemas(t *testing.T, bundle golem.SchemaBundle, rewrite func(*physical.PhysicalSchema)) golem.SchemaBundle {
	t.Helper()
	documents := bundle.Providers()
	for index, document := range documents {
		decoded, err := physical.CanonicalDecodeVerified(document.Schema().Bytes(), physical.Digest(document.Schema().Fingerprint()), physical.Digest(document.SystemFingerprint()))
		if err != nil {
			t.Fatal(err)
		}
		rewrite(&decoded)
		documents[index] = providerDocument(t, document.Provider(), decoded)
	}
	return golem.GeneratedSchemaBundle(bundle.GenerationDigest(), bundle.GeneratorVersion(), bundle.TemplateABIVersion(), bundle.Model(), bundle.Contract(), documents...)
}

func tableByPhysicalID(t *testing.T, schema *physical.PhysicalSchema, id compilerir.ModelID) *physical.PhysicalTable {
	t.Helper()
	for index := range schema.Tables {
		if schema.Tables[index].ID == id {
			return &schema.Tables[index]
		}
	}
	t.Fatalf("physical table %s not found", id)
	return nil
}

func mustPhysicalIndexID(t *testing.T, value compilerir.IndexID) PhysicalIndexID {
	t.Helper()
	result, err := physicalIndexID(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
