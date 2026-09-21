package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/eleven-am/golem/go/embedding"
	"github.com/eleven-am/golem/go/internal/compiler/ir"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	"github.com/jmoiron/sqlx"
)

type refusingProvider struct {
	specification embedding.Specification
	mu            sync.Mutex
	refuse        []string
	code          embedding.Code
	calls         int
}

func (p *refusingProvider) Specification() embedding.Specification { return p.specification }

func (p *refusingProvider) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	for _, input := range inputs {
		for _, fragment := range p.refuse {
			if strings.Contains(input.Text(), fragment) {
				return nil, embedding.NewError(p.code, fmt.Errorf("refused"))
			}
		}
	}
	vectors := make([]embedding.Vector, len(inputs))
	for index := range inputs {
		values := []float32{1, 0, 0}
		vector, err := embedding.NewVector(values)
		if err != nil {
			return nil, err
		}
		vectors[index] = vector
	}
	return vectors, nil
}

func (p *refusingProvider) stopRefusing() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refuse = nil
}

type quarantineFixture struct {
	manager  *Manager
	database *sqlx.DB
	provider *refusingProvider
}

func openQuarantineFixture(t *testing.T, refuse []string, code embedding.Code) quarantineFixture {
	t.Helper()
	handle, err := sqlitevec.Open("file:" + t.TempDir() + "/quarantine.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	database := sqlx.NewDb(handle, "sqlite3")
	if _, err := database.Exec(`CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT);
CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL,"ambiguous_strikes" INTEGER NOT NULL DEFAULT 0 CHECK ("ambiguous_strikes" >= 0)) STRICT;
CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO "posts" (id,title) VALUES ('a','alpha'),('b','beta'),('c','gamma')`); err != nil {
		t.Fatal(err)
	}
	schema := semanticSchema(t, 3)
	schema.Namespace = physical.Namespace{Name: "main"}
	schema.Tables = []physical.PhysicalTable{{
		ID: "post", Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "post-primary", Name: "pk_posts", Columns: []ir.FieldID{"id"}},
	}}
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	embedder := &refusingProvider{specification: specification, refuse: refuse, code: code}
	registry, _ := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	inventory, err := NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(database, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	return quarantineFixture{manager: manager, database: database, provider: embedder}
}

type shadowRow struct {
	Key     string `db:"record_key"`
	Status  string `db:"status"`
	Strikes int64  `db:"ambiguous_strikes"`
	Code    *string
}

func (fixture quarantineFixture) rows(t *testing.T) map[string]shadowRow {
	t.Helper()
	var rows []struct {
		Key     string  `db:"record_key"`
		Status  string  `db:"status"`
		Strikes int64   `db:"ambiguous_strikes"`
		Code    *string `db:"error_code"`
	}
	if err := fixture.database.Select(&rows, `SELECT "record_key","status","ambiguous_strikes","error_code" FROM "_golem_semantic_semantic-post-related_state" ORDER BY "record_key"`); err != nil {
		t.Fatal(err)
	}
	result := make(map[string]shadowRow, len(rows))
	for _, row := range rows {
		suffix := row.Key[strings.LastIndex(row.Key, ":")+1:]
		result[suffix] = shadowRow{Key: row.Key, Status: row.Status, Strikes: row.Strikes, Code: row.Code}
	}
	return result
}

func TestProviderOutageNeverQuarantinesAndNeverStrikes(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"golem-semantic-document"}, embedding.CodeUnavailable)
	ctx := context.Background()
	for attempt := 0; attempt < semanticAmbiguousStrikeBound+3; attempt++ {
		if err := fixture.manager.Refresh(ctx, "post", "related"); err == nil {
			t.Fatal("a total provider outage reported success")
		}
	}
	for key, row := range fixture.rows(t) {
		if row.Status == "failed" {
			t.Fatalf("record %s was quarantined during a provider outage: %+v", key, row)
		}
		if row.Strikes != 0 {
			t.Fatalf("record %s accrued %d strikes during a provider outage", key, row.Strikes)
		}
	}
}

func TestUnclassifiedRefusalQuarantinesOnlyTheCulpritAfterTheStrikeBound(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"beta"}, embedding.CodeProvider)
	ctx := context.Background()
	for attempt := 0; attempt < semanticAmbiguousStrikeBound; attempt++ {
		_ = fixture.manager.Refresh(ctx, "post", "related")
	}
	rows := fixture.rows(t)
	culprit, ok := rows["b"]
	if !ok {
		t.Fatalf("culprit row is absent: %+v", rows)
	}
	if culprit.Status != "failed" {
		t.Fatalf("culprit was not quarantined after %d strikes: %+v", semanticAmbiguousStrikeBound, culprit)
	}
	if culprit.Code == nil || *culprit.Code != semanticUnclassifiedRefusalCode {
		t.Fatalf("culprit code=%v want %s", culprit.Code, semanticUnclassifiedRefusalCode)
	}
	for key, row := range rows {
		if key == "b" {
			continue
		}
		if row.Status != "ready" {
			t.Fatalf("healthy record %s was disturbed: %+v", key, row)
		}
	}
}

func TestStrikesResetWhenTheRecordFinallyEmbeds(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"beta"}, embedding.CodeProvider)
	ctx := context.Background()
	_ = fixture.manager.Refresh(ctx, "post", "related")
	if strikes := fixture.rows(t)["b"].Strikes; strikes == 0 {
		t.Fatal("an isolated unclassified refusal recorded no strike while the provider was alive")
	}
	fixture.provider.stopRefusing()
	if err := fixture.manager.Refresh(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	row := fixture.rows(t)["b"]
	if row.Status != "ready" || row.Strikes != 0 {
		t.Fatalf("a successful embed did not clear the strike count: %+v", row)
	}
}

func TestANewStaleMarkClearsTheStrikeCount(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"beta"}, embedding.CodeProvider)
	ctx := context.Background()
	_ = fixture.manager.Refresh(ctx, "post", "related")
	if strikes := fixture.rows(t)["b"].Strikes; strikes == 0 {
		t.Fatal("no strike was recorded before the mark")
	}
	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='beta rewritten' WHERE "id"='b'`); err != nil {
		t.Fatal(err)
	}
	fixture.provider.stopRefusing()
	if err := fixture.manager.Refresh(ctx, "post", "related"); err != nil {
		t.Fatal(err)
	}
	if strikes := fixture.rows(t)["b"].Strikes; strikes != 0 {
		t.Fatalf("a new stale mark left %d strikes behind", strikes)
	}
}

func TestUnclassifiedTotalOutageNeverQuarantines(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"golem-semantic-document"}, embedding.CodeProvider)
	ctx := context.Background()
	for attempt := 0; attempt < semanticAmbiguousStrikeBound+3; attempt++ {
		_ = fixture.manager.Refresh(ctx, "post", "related")
	}
	rows := fixture.rows(t)
	for key, row := range rows {
		if row.Status == "failed" {
			t.Fatalf("record %s was quarantined while nothing ever embedded: %+v", key, row)
		}
		if row.Strikes != 0 {
			t.Fatalf("record %s accrued %d strikes while nothing ever embedded", key, row.Strikes)
		}
	}
}

func TestStrikeBoundLeavesRoomToProveLiveness(t *testing.T) {
	if semanticAmbiguousStrikeBound < 2 {
		t.Fatalf("strike bound %d quarantines on the first refusal", semanticAmbiguousStrikeBound)
	}
}

func TestUnupgradedShadowStateNeverNamesTheStrikeColumn(t *testing.T) {
	handle, err := sqlitevec.Open("file:" + t.TempDir() + "/legacy.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	database := sqlx.NewDb(handle, "sqlite3")
	if _, err := database.Exec(`CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT);
CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL) STRICT;
CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO "posts" (id,title) VALUES ('a','alpha'),('b','beta')`); err != nil {
		t.Fatal(err)
	}
	schema := semanticSchema(t, 3)
	schema.Namespace = physical.Namespace{Name: "main"}
	attributes := make([]physical.Attribute, 0, len(schema.Extensions[0].Attributes))
	for _, attribute := range schema.Extensions[0].Attributes {
		if attribute.Name != "state_version" {
			attributes = append(attributes, attribute)
		}
	}
	schema.Extensions[0].Attributes = attributes
	schema.Tables = []physical.PhysicalTable{{
		ID: "post", Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "post-primary", Name: "pk_posts", Columns: []ir.FieldID{"id"}},
	}}
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	embedder := &refusingProvider{specification: specification, refuse: []string{"beta"}, code: embedding.CodeProvider}
	registry, _ := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	inventory, err := NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(database, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < semanticAmbiguousStrikeBound+1; attempt++ {
		_ = manager.Refresh(context.Background(), "post", "related")
	}
	var statuses []string
	if err := database.Select(&statuses, `SELECT "status" FROM "_golem_semantic_semantic-post-related_state" ORDER BY "record_key"`); err != nil {
		t.Fatalf("an un-upgraded shadow state table was written with the strike column: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("the un-upgraded drain wrote no state at all")
	}
}

// TestAnOutageAfterAHealthyPeriodNeverQuarantines pins the rule that matters
// most: strikes earned earlier must never be enough on their own. Every strike,
// including the one that reaches the bound, requires a provider call that
// succeeded in the very same pass.
func TestAnOutageAfterAHealthyPeriodNeverQuarantines(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"beta"}, embedding.CodeProvider)
	ctx := context.Background()
	if err := fixture.manager.Refresh(ctx, "post", "related"); err == nil {
		t.Fatal("the poisoned record settled")
	}
	earned := fixture.rows(t)["b"].Strikes
	if earned == 0 {
		t.Fatal("no strike was earned during the healthy period")
	}
	fixture.provider.mu.Lock()
	fixture.provider.refuse = []string{"golem-semantic-document"}
	fixture.provider.mu.Unlock()
	for attempt := 0; attempt < semanticAmbiguousStrikeBound+5; attempt++ {
		_ = fixture.manager.Refresh(ctx, "post", "related")
	}
	row := fixture.rows(t)["b"]
	if row.Status == "failed" {
		t.Fatalf("a total outage after a healthy period quarantined the record: %+v", row)
	}
	if row.Strikes != earned {
		t.Fatalf("a total outage charged strikes without any successful call: %d then %d", earned, row.Strikes)
	}
	for key, other := range fixture.rows(t) {
		if key != "b" && other.Status == "failed" {
			t.Fatalf("the outage quarantined healthy record %s: %+v", key, other)
		}
	}
}

func openPagedQuarantineFixture(t *testing.T, count int, poison string, code embedding.Code) quarantineFixture {
	t.Helper()
	handle, err := sqlitevec.Open("file:" + t.TempDir() + "/paged.db?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	database := sqlx.NewDb(handle, "sqlite3")
	if _, err := database.Exec(`CREATE TABLE "posts" ("id" TEXT NOT NULL PRIMARY KEY,"title" TEXT);
CREATE TABLE "_golem_semantic_semantic-post-related_state" (record_key TEXT NOT NULL PRIMARY KEY,source_hash BLOB NOT NULL,space_fingerprint TEXT NOT NULL,status TEXT NOT NULL,attempt_count INTEGER NOT NULL DEFAULT 0,error_code TEXT,updated_at INTEGER NOT NULL,"id" TEXT NOT NULL,"ambiguous_strikes" INTEGER NOT NULL DEFAULT 0 CHECK ("ambiguous_strikes" >= 0)) STRICT;
CREATE VIRTUAL TABLE "_golem_semantic_semantic-post-related_vec" USING vec0(record_key TEXT PRIMARY KEY,embedding float[3] distance_metric=cosine)`); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("p%02d", index)
		title := "healthy " + id
		if id == poison {
			title = "poisondoc " + id
		}
		if _, err := database.Exec(`INSERT INTO "posts" (id,title) VALUES (?,?)`, id, title); err != nil {
			t.Fatal(err)
		}
	}
	schema := semanticSchema(t, 3)
	schema.Namespace = physical.Namespace{Name: "main"}
	schema.Tables = []physical.PhysicalTable{{
		ID: "post", Name: "posts",
		Columns: []physical.PhysicalColumn{
			{ID: "id", Name: "id", Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
			{ID: "title", Name: "title", Ordinal: 1, Nullable: true, Storage: physical.StorageType{Kind: physical.StorageSQLiteText}},
		},
		PrimaryKey: &physical.PhysicalKey{ID: "post-primary", Name: "pk_posts", Columns: []ir.FieldID{"id"}},
	}}
	specification, _ := embedding.NewSpecification("test", "model", "v1", 3, 8)
	embedder := &refusingProvider{specification: specification, refuse: []string{"poisondoc"}, code: code}
	registry, _ := embedding.NewRegistry(map[string]embedding.Provider{"content": embedder})
	inventory, err := NewInventory(schema, registry)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(database, ir.SQLite, schema, inventory)
	if err != nil {
		t.Fatal(err)
	}
	return quarantineFixture{manager: manager, database: database, provider: embedder}
}

// TestAPoisonedFirstRecordNeverStarvesThePage pins the property the isolation
// path exists to protect: a document the provider refuses must not stop the
// records after it being embedded, whether they share its batch or fall in a
// later one, and however many passes run.
func TestAPoisonedFirstRecordNeverStarvesThePage(t *testing.T) {
	const records = 20
	fixture := openPagedQuarantineFixture(t, records, "p00", embedding.CodeProvider)
	ctx := context.Background()
	_ = fixture.manager.Refresh(ctx, "post", "related")
	rows := fixture.rows(t)
	for index := 1; index < records; index++ {
		id := fmt.Sprintf("p%02d", index)
		row, present := rows[id]
		if !present || row.Status != "ready" {
			t.Fatalf("one pass left %s behind the poisoned first record: present=%v row=%+v", id, present, row)
		}
	}
	if culprit := rows["p00"]; culprit.Status == "ready" || culprit.Strikes == 0 {
		t.Fatalf("the poisoned first record was not isolated and struck: %+v", culprit)
	}
	for attempt := 0; attempt < semanticAmbiguousStrikeBound; attempt++ {
		_ = fixture.manager.Refresh(ctx, "post", "related")
	}
	rows = fixture.rows(t)
	if culprit := rows["p00"]; culprit.Status != "failed" || culprit.Code == nil || *culprit.Code != semanticUnclassifiedRefusalCode {
		t.Fatalf("the poisoned first record never reached the bound: %+v", culprit)
	}
	for index := 1; index < records; index++ {
		if row := rows[fmt.Sprintf("p%02d", index)]; row.Status != "ready" {
			t.Fatalf("later passes disturbed healthy record p%02d: %+v", index, row)
		}
	}
}
