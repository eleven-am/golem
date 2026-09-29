package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/eleven-am/golem/go/golem"
	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
	fulltextcontract "github.com/eleven-am/golem/go/internal/fulltext/contract"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresqlprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/eleven-am/golem/go/internal/testenv"
)

func fullTextSchemaWithIndexes(t *testing.T, fixture schematest.Fixture, indexes []fulltextcontract.Index) schematest.Fixture {
	t.Helper()
	modelDocument := fixture.Bundle.Model()
	var model compilerir.ModelIR
	if err := json.Unmarshal(modelDocument.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	for position, index := range indexes {
		index.Fields = []fulltextcontract.Field{{ID: hex.EncodeToString(fixture.PostTitle[:]), Weight: 1}}
		payload, err := fulltextcontract.Encode(index)
		if err != nil {
			t.Fatal(err)
		}
		for _, provider := range []compilerir.Provider{compilerir.SQLite, compilerir.PostgreSQL} {
			model.Extensions = append(model.Extensions, compilerir.ProviderExtensionIR{
				ID: compilerir.ExtensionID(fmt.Sprintf("75%030x", position)), Provider: provider, Version: fulltextcontract.Version,
				Owner: compilerir.ObjectID(hex.EncodeToString(fixture.Post[:])), Kind: fulltextcontract.IndexKind, Payload: payload,
			})
		}
	}
	fixture.SQLite = lowerFullTextFixture(t, sqliteprovider.New(), model, fixture.SQLite)
	fixture.PostgreSQL = lowerFullTextFixture(t, postgresqlprovider.New(), model, fixture.PostgreSQL)
	canonical, err := compilerir.CanonicalModel(model)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := compilerir.ModelFingerprint(model)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(string(fingerprint))
	if err != nil || len(raw) != len(golem.SchemaDigest{}) {
		t.Fatalf("model fingerprint=%q error=%v", fingerprint, err)
	}
	var digest golem.SchemaDigest
	copy(digest[:], raw)
	modelDocument = golem.GeneratedSchemaDocument(modelDocument.FormatVersion(), modelDocument.CanonicalVersion(), digest, canonical)
	fixture.Bundle = golem.GeneratedSchemaBundle(
		fixture.Bundle.GenerationDigest(), fixture.Bundle.GeneratorVersion(), fixture.Bundle.TemplateABIVersion(),
		modelDocument, fixture.Bundle.Contract(),
		schematest.ProviderDocument(t, golem.SQLite, fixture.SQLite), schematest.ProviderDocument(t, golem.PostgreSQL, fixture.PostgreSQL),
	)
	return fixture
}

type fullTextProfile struct {
	name    string
	fixture mutationResultFixture
	cType   bool
}

func runFullTextNormalizationProfiles(t *testing.T, indexes []fulltextcontract.Index, operation func(*testing.T, fullTextProfile)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) {
		schema := fullTextSchemaWithIndexes(t, schematest.NewSubscribedIndexed(t), indexes)
		operation(t, fullTextProfile{name: "sqlite", fixture: openMutationResultFixture(t, schema, MutationLimits{}, nil, nil, nil, true)})
	})
	for _, profile := range []struct{ name, env string }{{"postgresql-c", testenv.PostgreSQLDSNVariable}, {"postgresql-linguistic", testenv.LinguisticDSNVariable}} {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			dsn := testenv.DisposablePostgreSQL(t, profile.env)
			sequence := p4OracleNamespaceSequence.Add(1)
			applicationNamespace := fmt.Sprintf("golem_fts_norm_%d_%d", os.Getpid(), sequence)
			systemNamespace := fmt.Sprintf("golem_fts_norm_system_%d_%d", os.Getpid(), sequence)
			schema := fullTextSchemaWithIndexes(t, schematest.NewSubscribedIndexedPostgreSQLNamespaces(t, physical.PhysicalName(applicationNamespace), physical.PhysicalName(systemNamespace)), indexes)
			base := newMutationResultFixture(t)
			provider := postgresqlprovider.New()
			database, _, err := provider.Open(context.Background(), dsn)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			if err := provider.ApplyInitial(context.Background(), database, schema.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			if err := provider.Verify(context.Background(), database, schema.PostgreSQL); err != nil {
				t.Fatal(err)
			}
			var cType string
			if err := database.Get(&cType, `SELECT datctype FROM pg_catalog.pg_database WHERE datname=current_database()`); err != nil {
				t.Fatal(err)
			}
			for _, user := range [][2]string{{mutationResultUUIDText(1), "alice"}, {mutationResultUUIDText(2), "bob"}} {
				if _, err := database.Exec(`INSERT INTO `+oracleQualified(applicationNamespace, "users")+` ("id","name") VALUES ($1,$2)`, user[0], user[1]); err != nil {
					t.Fatal(err)
				}
			}
			app, err := Open(context.Background(), withRuntimeTestEvents(t, Config[mutationResultPrincipal, mutationResultActor]{
				Database: p8RuntimeTestDatabase(database, golem.PostgreSQL), Bundle: schema.Bundle,
				Bindings: base.app.bindings, Descriptors: base.app.descriptors,
				ResolvePrincipal: base.app.resolvePrincipal, SnapshotActor: base.app.snapshotActor,
			}))
			if err != nil {
				t.Fatal(err)
			}
			base.app, base.schema = app, schema
			operation(t, fullTextProfile{name: profile.name, fixture: base, cType: cType == "C" || cType == "POSIX"})
		})
	}
}

func fullTextMatches(t *testing.T, fixture mutationResultFixture, index, query string) string {
	t.Helper()
	rows, err := SystemTextSearch(context.Background(), fixture.app.System(), fixture.postDescriptor, index, query, 10)
	if err != nil {
		t.Fatalf("index %s query %q: %v", index, query, err)
	}
	ids := make([]string, len(rows))
	for position, result := range rows {
		id, _ := golem.Value(result.Row(), fixture.postID).Get()
		ids[position] = fmt.Sprint(id[15])
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

func TestFullTextNormalizationIsPerIndexAndCtypeIndependent(t *testing.T) {
	indexes := []fulltextcontract.Index{
		{Name: "legacy", Folding: fulltextcontract.FoldingNone},
		{Name: "modern", Folding: fulltextcontract.FoldingNone, Normalization: fulltextcontract.NormalizationNFCLower},
		{Name: "folded", Folding: fulltextcontract.FoldingDiacritics, Normalization: fulltextcontract.NormalizationNFCLower},
	}
	runFullTextNormalizationProfiles(t, indexes, func(t *testing.T, profile fullTextProfile) {
		fixture := profile.fixture
		for _, post := range []struct {
			id    byte
			title string
		}{{61, "Καφές morning"}, {62, "café noir"}, {63, "quick brown fox"}} {
			if _, err := SystemCreate(context.Background(), fixture.app.System(), fixture.postDescriptor, fixture.createPost(post.id, golem.UUID{15: 1}, post.title)); err != nil {
				t.Fatal(err)
			}
		}
		legacyLower := "61"
		if profile.cType {
			legacyLower = ""
		}
		for _, test := range []struct{ index, query, want string }{
			{"modern", "καφές", "61"},
			{"modern", "ΚΑΦΈΣ", "61"},
			{"modern", "caf\u00e9", "62"},
			{"modern", "cafe\u0301", "62"},
			{"modern", `"quick bro"*`, "63"},
			{"folded", "καφες", "61"},
			{"folded", "CAFE", "62"},
			{"legacy", "Καφές", "61"},
			{"legacy", "καφές", legacyLower},
			{"legacy", "café", ""},
			{"legacy", `"quick bro"*`, "63"},
		} {
			if got := fullTextMatches(t, fixture, test.index, test.query); got != test.want {
				t.Errorf("%s index %s query %q matched [%s], want [%s]", profile.name, test.index, test.query, got, test.want)
			}
		}
	})
}
