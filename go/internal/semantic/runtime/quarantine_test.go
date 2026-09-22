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
	semantickey "github.com/eleven-am/golem/go/internal/semantic/key"
	"github.com/eleven-am/golem/go/internal/semantic/sqlitevec"
	"github.com/jmoiron/sqlx"
)

type refusingProvider struct {
	specification embedding.Specification
	mu            sync.Mutex
	refuse        []string
	code          embedding.Code
	calls         int
	inputs        []string
}

func (p *refusingProvider) Specification() embedding.Specification { return p.specification }

func (p *refusingProvider) Embed(_ context.Context, inputs []embedding.Input) ([]embedding.Vector, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	for _, input := range inputs {
		p.inputs = append(p.inputs, input.Text())
	}
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
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		ids = append(ids, fmt.Sprintf("p%02d", index))
	}
	return openKeyedQuarantineFixture(t, ids, poison, code)
}

func openKeyedQuarantineFixture(t *testing.T, ids []string, poison string, code embedding.Code) quarantineFixture {
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
	for _, id := range ids {
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

func (fixture quarantineFixture) markStale(t *testing.T, ids ...string) {
	t.Helper()
	records := make([]MarkRecord, 0, len(ids))
	for _, id := range ids {
		key, err := semantickey.Encode([]any{id})
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, MarkRecord{Key: key, Identity: []any{id}})
	}
	if err := fixture.manager.MarkStale(context.Background(), fixture.database, "post", semanticMarkBinds, records); err != nil {
		t.Fatal(err)
	}
}

func (fixture quarantineFixture) drain(t *testing.T) {
	t.Helper()
	if _, err := fixture.manager.Drain(context.Background(), "post", "related"); err != nil && !ProviderDeferred(err) {
		t.Fatal(err)
	}
}

// TestDrainAppliesStrikesAndQuarantine covers the entry point production uses:
// the mutation worker calls Drain, and SemanticReconcileInterval defaults to
// zero, so a bound that only works under Refresh is inert where it matters.
func TestDrainAppliesStrikesAndQuarantine(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"beta"}, embedding.CodeProvider)
	fixture.markStale(t, "a", "b", "c")
	fixture.drain(t)
	if strikes := fixture.rows(t)["b"].Strikes; strikes == 0 {
		t.Fatal("Drain charged no strike while the provider was demonstrably alive")
	}
	for _, id := range []string{"a", "c"} {
		if status := fixture.rows(t)[id].Status; status != "ready" {
			t.Fatalf("Drain left healthy record %s at %q", id, status)
		}
	}
	// No re-marking: a pending record stays in the drain's own probe, and a new
	// mark would deliberately reset the strike count.
	for attempt := 0; attempt < semanticAmbiguousStrikeBound; attempt++ {
		fixture.drain(t)
	}
	row := fixture.rows(t)["b"]
	if row.Status != "failed" || row.Code == nil || *row.Code != semanticUnclassifiedRefusalCode {
		t.Fatalf("Drain never quarantined the culprit: %+v", row)
	}
}

func TestDrainNeverQuarantinesDuringAnOutage(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"golem-semantic-document"}, embedding.CodeProvider)
	for attempt := 0; attempt < semanticAmbiguousStrikeBound+3; attempt++ {
		fixture.markStale(t, "a", "b", "c")
		fixture.drain(t)
	}
	for key, row := range fixture.rows(t) {
		if row.Status == "failed" || row.Strikes != 0 {
			t.Fatalf("Drain struck or quarantined %s during a total outage: %+v", key, row)
		}
	}
}

func TestDrainNeverStarvesHealthyRecordsBehindACulprit(t *testing.T) {
	const records = 20
	fixture := openPagedQuarantineFixture(t, records, "p00", embedding.CodeProvider)
	ids := make([]string, 0, records)
	for index := 0; index < records; index++ {
		ids = append(ids, fmt.Sprintf("p%02d", index))
	}
	fixture.markStale(t, ids...)
	fixture.drain(t)
	rows := fixture.rows(t)
	for index := 1; index < records; index++ {
		id := fmt.Sprintf("p%02d", index)
		if row := rows[id]; row.Status != "ready" {
			t.Fatalf("Drain left %s behind the poisoned first record: %+v", id, row)
		}
	}
	if culprit := rows["p00"]; culprit.Strikes == 0 {
		t.Fatalf("Drain did not strike the poisoned first record: %+v", culprit)
	}
}

func TestDrainResetsStrikesWhenTheRecordFinallyEmbeds(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"beta"}, embedding.CodeProvider)
	fixture.markStale(t, "a", "b", "c")
	fixture.drain(t)
	if strikes := fixture.rows(t)["b"].Strikes; strikes == 0 {
		t.Fatal("Drain charged no strike to begin with")
	}
	fixture.provider.stopRefusing()
	fixture.drain(t)
	row := fixture.rows(t)["b"]
	if row.Status != "ready" || row.Strikes != 0 {
		t.Fatalf("a successful Drain embed did not clear the strike count: %+v", row)
	}
}

func TestDrainSeesAStaleMarkClearTheStrikeCount(t *testing.T) {
	fixture := openQuarantineFixture(t, []string{"beta"}, embedding.CodeProvider)
	fixture.markStale(t, "a", "b", "c")
	fixture.drain(t)
	if strikes := fixture.rows(t)["b"].Strikes; strikes == 0 {
		t.Fatal("Drain charged no strike to begin with")
	}
	fixture.markStale(t, "b")
	if strikes := fixture.rows(t)["b"].Strikes; strikes != 0 {
		t.Fatalf("a new stale mark left %d strikes behind", strikes)
	}
}

func (fixture quarantineFixture) refuseOnly(texts ...string) {
	fixture.provider.mu.Lock()
	defer fixture.provider.mu.Unlock()
	fixture.provider.refuse = texts
}

// settleAll embeds every record so the shadow table holds a population of
// ready rows for the liveness probe to draw on.
func (fixture quarantineFixture) settleAll(t *testing.T, count int) {
	t.Helper()
	ids := make([]string, 0, count)
	for index := 0; index < count; index++ {
		ids = append(ids, fmt.Sprintf("p%02d", index))
	}
	fixture.settleAllKeyed(t, ids)
}

func (fixture quarantineFixture) settleAllKeyed(t *testing.T, ids []string) {
	t.Helper()
	fixture.refuseOnly()
	fixture.markStale(t, ids...)
	fixture.drain(t)
	for _, id := range ids {
		if row := fixture.rows(t)[id]; row.Status != "ready" {
			t.Fatalf("setup left %s at %q", id, row.Status)
		}
	}
}

// TestLivenessIsNotPinnedToOneFailingCandidate covers a provider whose
// acceptance policy changed after a document was stored: the first ready record
// can no longer embed, so a probe that always chose it would never prove
// liveness and the culprit would stay pending for ever.
func TestLivenessIsNotPinnedToOneFailingCandidate(t *testing.T) {
	const records = 12
	fixture := openPagedQuarantineFixture(t, records, "p11", embedding.CodeProvider)
	fixture.settleAll(t, records)
	fixture.refuseOnly("poisondoc", "healthy p00")

	// A mark alone is not enough: an unchanged document is flipped back to
	// ready without reaching the provider, so the culprit's text must change.
	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='poisondoc p11 revised' WHERE "id"='p11'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "p11")
	fixture.drain(t)
	if strikes := fixture.rows(t)["p11"].Strikes; strikes == 0 {
		t.Fatal("liveness stayed pinned to the first ready record, so no strike was charged")
	}
	for attempt := 0; attempt < semanticAmbiguousStrikeBound; attempt++ {
		fixture.drain(t)
	}
	row := fixture.rows(t)["p11"]
	if row.Status != "failed" || row.Code == nil || *row.Code != semanticUnclassifiedRefusalCode {
		t.Fatalf("the culprit never reached quarantine behind a failing probe candidate: %+v", row)
	}
	if probe := fixture.rows(t)["p00"]; probe.Status != "ready" {
		t.Fatalf("probing must not disturb the candidate it borrows: %+v", probe)
	}
}

func TestEveryProbeCandidateFailingChargesNothing(t *testing.T) {
	const records = 12
	fixture := openPagedQuarantineFixture(t, records, "p11", embedding.CodeProvider)
	fixture.settleAll(t, records)
	fixture.refuseOnly("golem-semantic-document")

	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='poisondoc p11 revised' WHERE "id"='p11'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "p11")
	for attempt := 0; attempt < semanticAmbiguousStrikeBound+3; attempt++ {
		fixture.drain(t)
	}
	for key, row := range fixture.rows(t) {
		if row.Status == "failed" {
			t.Fatalf("a total outage quarantined %s: %+v", key, row)
		}
		if row.Strikes != 0 {
			t.Fatalf("a total outage charged %d strikes to %s", row.Strikes, key)
		}
	}
}

func TestLivenessProbesAreBoundedPerPass(t *testing.T) {
	const records = 12
	fixture := openPagedQuarantineFixture(t, records, "p11", embedding.CodeProvider)
	fixture.settleAll(t, records)
	fixture.refuseOnly("golem-semantic-document")
	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='poisondoc p11 revised' WHERE "id"='p11'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "p11")
	fixture.provider.mu.Lock()
	fixture.provider.calls = 0
	fixture.provider.mu.Unlock()
	fixture.drain(t)
	fixture.provider.mu.Lock()
	calls := fixture.provider.calls
	fixture.provider.mu.Unlock()
	if want := 1 + semanticOutageBatches + semanticLivenessProbes; calls > want {
		t.Fatalf("a pass spent %d provider calls, more than the bound of %d", calls, want)
	}
}

// TestReconcileLivenessIsNotPinnedToItsFirstCandidates covers the same changed
// acceptance policy on the reconcile path, which collects candidates from its
// own scan rather than through livenessCandidates.
func TestReconcileLivenessIsNotPinnedToItsFirstCandidates(t *testing.T) {
	const records = 12
	fixture := openPagedQuarantineFixture(t, records, "p11", embedding.CodeProvider)
	fixture.settleAll(t, records)
	fixture.refuseOnly("poisondoc", "healthy p00", "healthy p01", "healthy p02")

	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='poisondoc p11 revised' WHERE "id"='p11'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "p11")
	ctx := context.Background()
	// The probe budget is three, so the pass that meets three unacceptable
	// candidates proves nothing; the anchor must move it past them by the next.
	_ = fixture.manager.Refresh(ctx, "post", "related")
	_ = fixture.manager.Refresh(ctx, "post", "related")
	if strikes := fixture.rows(t)["p11"].Strikes; strikes == 0 {
		t.Fatal("reconcile liveness stayed pinned to its first candidates, so no strike was charged")
	}
	for attempt := 0; attempt < semanticAmbiguousStrikeBound; attempt++ {
		_ = fixture.manager.Refresh(ctx, "post", "related")
	}
	row := fixture.rows(t)["p11"]
	if row.Status != "failed" || row.Code == nil || *row.Code != semanticUnclassifiedRefusalCode {
		t.Fatalf("the culprit never reached quarantine under reconcile: %+v", row)
	}
}

// TestLivenessRotatesWhenKeyLengthReordersTheEncoding covers identities whose
// record keys sort differently from the source scan: the key encoding is
// length prefixed, so "b" encodes below "aaa" while the scan reaches it after.
func TestLivenessRotatesWhenKeyLengthReordersTheEncoding(t *testing.T) {
	ids := []string{"a", "aa", "aaa", "b", "poison"}
	fixture := openKeyedQuarantineFixture(t, ids, "poison", embedding.CodeProvider)
	fixture.settleAllKeyed(t, ids)
	fixture.refuseOnly("poisondoc", "healthy a", "healthy aa", "healthy aaa")

	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='poisondoc poison revised' WHERE "id"='poison'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "poison")
	ctx := context.Background()
	for attempt := 0; attempt < 4; attempt++ {
		_ = fixture.manager.Refresh(ctx, "post", "related")
	}
	if strikes := fixture.rows(t)["poison"].Strikes; strikes == 0 {
		t.Fatal("liveness never reached the viable candidate the key encoding sorts below the failing ones")
	}
}

// TestLivenessSkipsCandidatesWhoseOwnerIsGone covers ready shadow rows whose
// source row was deleted outside golem: they yield no probe at all, so a
// rotation that only remembers candidates the provider answered for would
// choose them for ever.
func TestLivenessSkipsCandidatesWhoseOwnerIsGone(t *testing.T) {
	const records = 12
	fixture := openPagedQuarantineFixture(t, records, "p11", embedding.CodeProvider)
	fixture.settleAll(t, records)
	if _, err := fixture.database.Exec(`DELETE FROM "posts" WHERE "id" IN ('p00','p01','p02')`); err != nil {
		t.Fatal(err)
	}
	fixture.refuseOnly("poisondoc")

	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='poisondoc p11 revised' WHERE "id"='p11'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "p11")
	for attempt := 0; attempt < 3; attempt++ {
		fixture.drain(t)
	}
	if strikes := fixture.rows(t)["p11"].Strikes; strikes == 0 {
		t.Fatal("liveness never looked past the ready rows whose owner is gone")
	}
}

// TestLivenessProgressesPastManyOwnerlessCandidates covers more ownerless ready
// rows than any bounded exclusion set can hold: progress has to be monotonic
// rather than remembered.
func TestLivenessProgressesPastManyOwnerlessCandidates(t *testing.T) {
	const records = 30
	fixture := openPagedQuarantineFixture(t, records, "p29", embedding.CodeProvider)
	fixture.settleAll(t, records)
	if _, err := fixture.database.Exec(`DELETE FROM "posts" WHERE "id" < 'p22'`); err != nil {
		t.Fatal(err)
	}
	fixture.refuseOnly("poisondoc")

	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='poisondoc p29 revised' WHERE "id"='p29'`); err != nil {
		t.Fatal(err)
	}
	fixture.markStale(t, "p29")
	for attempt := 0; attempt < 6; attempt++ {
		fixture.drain(t)
	}
	if strikes := fixture.rows(t)["p29"].Strikes; strikes == 0 {
		t.Fatal("liveness never reached a usable candidate past the ownerless ones")
	}
}

// TestTwoLeadingCulpritsDoNotStallAFreshIndex covers a fresh index whose first
// two documents the provider refuses: isolating them must not consume the
// outage budget for the healthy documents behind them, which are the only
// thing that can ever prove the provider is answering.
func TestTwoLeadingCulpritsDoNotStallAFreshIndex(t *testing.T) {
	const records = 12
	fixture := openPagedQuarantineFixture(t, records, "p00", embedding.CodeProvider)
	if _, err := fixture.database.Exec(`UPDATE "posts" SET "title"='poisondoc p01' WHERE "id"='p01'`); err != nil {
		t.Fatal(err)
	}
	fixture.refuseOnly("poisondoc")
	ids := make([]string, 0, records)
	for index := 0; index < records; index++ {
		ids = append(ids, fmt.Sprintf("p%02d", index))
	}
	fixture.markStale(t, ids...)
	for attempt := 0; attempt < 4; attempt++ {
		fixture.drain(t)
	}
	rows := fixture.rows(t)
	for index := 2; index < records; index++ {
		id := fmt.Sprintf("p%02d", index)
		if row := rows[id]; row.Status != "ready" {
			t.Fatalf("two refused documents at the head left %s at %q", id, row.Status)
		}
	}
}
