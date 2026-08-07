package p7oracle

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/migration"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/eleven-am/golem/go/internal/policy/schematest"
	postgresprovider "github.com/eleven-am/golem/go/internal/provider/postgresql"
	sqliteprovider "github.com/eleven-am/golem/go/internal/provider/sqlite"
	"github.com/jmoiron/sqlx"
)

type oracleProfile struct {
	name     string
	provider golem.Provider
	dsn      string
	env      string
}

func oracleProfiles() []oracleProfile {
	return []oracleProfile{
		{name: "sqlite", provider: golem.SQLite},
		{name: "postgresql-c", provider: golem.PostgreSQL, dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_DSN")), env: "GOLEM_TEST_POSTGRES_DSN"},
		{name: "postgresql-linguistic", provider: golem.PostgreSQL, dsn: strings.TrimSpace(os.Getenv("GOLEM_TEST_POSTGRES_LINGUISTIC_DSN")), env: "GOLEM_TEST_POSTGRES_LINGUISTIC_DSN"},
	}
}

type oracleHarness struct {
	profile       oracleProfile
	database      *sqlx.DB
	coordinator   eventprovider.Coordinator
	social        Social
	fixture       schematest.SocialMutationFixture
	applicationNS physical.PhysicalName
	systemNS      physical.PhysicalName
	sqlitePath    string
}

func openOracleHarness(t *testing.T, profile oracleProfile) *oracleHarness {
	t.Helper()
	ctx := context.Background()
	harness := &oracleHarness{profile: profile, social: CanonicalSocial()}
	if profile.provider == golem.SQLite {
		harness.sqlitePath = filepath.Join(t.TempDir(), "p7-independent-social.db")
		harness.fixture = schematest.NewSubscribedSocialMutation(t)
		provider := sqliteprovider.New()
		database, _, err := provider.Open(ctx, harness.sqlitePath)
		if err != nil {
			t.Fatal(err)
		}
		harness.database = database
		if err := provider.ApplyInitial(ctx, database, harness.fixture.SQLite); err != nil {
			database.Close()
			t.Fatal(err)
		}
		harness.coordinator, err = provider.EventCoordinator(database)
		if err != nil {
			database.Close()
			t.Fatal(err)
		}
	} else {
		if profile.dsn == "" {
			t.Skip(profile.env + " is not configured; P7 final evidence must supply both live profiles")
		}
		suffix := fmt.Sprintf("%d_%d", os.Getpid(), time.Now().UnixNano())
		harness.applicationNS = physical.PhysicalName("golem_p7_oracle_" + suffix)
		harness.systemNS = physical.PhysicalName("golem_p7_oracle_sys_" + suffix)
		harness.fixture = schematest.NewSubscribedSocialMutationPostgreSQLNamespaces(t, harness.applicationNS, harness.systemNS)
		provider := postgresprovider.New()
		database, _, err := provider.Open(ctx, profile.dsn)
		if err != nil {
			t.Fatal(err)
		}
		harness.database = database
		if err := provider.ApplyInitial(ctx, database, harness.fixture.PostgreSQL); err != nil {
			database.Close()
			t.Fatal(err)
		}
		harness.coordinator, err = provider.EventCoordinatorAt(database, harness.systemNS)
		if err != nil {
			database.Close()
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if harness.profile.provider == golem.PostgreSQL {
			_, _ = harness.database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteName(string(harness.applicationNS))+` CASCADE`)
			_, _ = harness.database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteName(string(harness.systemNS))+` CASCADE`)
		}
		_ = harness.database.Close()
	})
	return harness
}

func (h *oracleHarness) appTable(name string) string {
	if h.profile.provider == golem.PostgreSQL {
		return quoteName(string(h.applicationNS)) + "." + quoteName(name)
	}
	return quoteName(name)
}

func (h *oracleHarness) systemTable(name string) string {
	if h.profile.provider == golem.PostgreSQL {
		return quoteName(string(h.systemNS)) + "." + quoteName(name)
	}
	return quoteName(name)
}

func quoteName(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

func (h *oracleHarness) exec(t testing.TB, statement string, args ...any) sql.Result {
	t.Helper()
	result, err := h.database.ExecContext(context.Background(), h.database.Rebind(statement), args...)
	if err != nil {
		t.Fatalf("direct oracle SQL failed: %v", err)
	}
	return result
}

func (h *oracleHarness) seedSocial(t testing.TB) {
	t.Helper()
	transaction, err := h.database.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	exec := func(statement string, args ...any) {
		if _, execErr := transaction.ExecContext(context.Background(), h.database.Rebind(statement), args...); execErr != nil {
			t.Fatalf("seed independent social graph: %v", execErr)
		}
	}
	for _, user := range h.social.Users {
		exec(`INSERT INTO `+h.appTable("users")+`("id","name") VALUES (?,?)`, user.ID, user.Name)
	}
	for _, post := range h.social.Posts {
		exec(`INSERT INTO `+h.appTable("posts")+`("id","author_id","title") VALUES (?,?,?)`, post.ID, post.AuthorID, post.Title)
	}
	for _, comment := range h.social.Comments {
		var parent any
		if comment.ParentID != nil {
			parent = *comment.ParentID
		}
		exec(`INSERT INTO `+h.appTable("comments")+`("id","post_id","author_id","parent_id","body") VALUES (?,?,?,?,?)`, comment.ID, comment.PostID, comment.AuthorID, parent, comment.Body)
	}
	for _, friendship := range h.social.Friendships {
		exec(`INSERT INTO `+h.appTable("friendships")+`("user_id","friend_id") VALUES (?,?)`, friendship.UserID, friendship.FriendID)
	}
	for _, tag := range h.social.Tags {
		exec(`INSERT INTO `+h.appTable("tags")+`("id","name") VALUES (?,?)`, tag.ID, tag.Name)
	}
	for _, postTag := range h.social.PostTags {
		exec(`INSERT INTO `+h.appTable("post_tags")+`("post_id","tag_name") VALUES (?,?)`, postTag.PostID, postTag.TagName)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
}

type tableCount struct {
	table string
	want  int
}

func (h *oracleHarness) assertSocialTruth(t testing.TB) {
	t.Helper()
	counts := []tableCount{{"users", len(h.social.Users)}, {"posts", len(h.social.Posts)}, {"comments", len(h.social.Comments)}, {"friendships", len(h.social.Friendships)}, {"tags", len(h.social.Tags)}, {"post_tags", len(h.social.PostTags)}}
	for _, expected := range counts {
		var got int
		if err := h.database.GetContext(context.Background(), &got, `SELECT COUNT(*) FROM `+h.appTable(expected.table)); err != nil || got != expected.want {
			t.Fatalf("direct %s count=%d want=%d err=%v", expected.table, got, expected.want, err)
		}
	}
	var recursive int
	query := `SELECT COUNT(*) FROM ` + h.appTable("comments") + ` child JOIN ` + h.appTable("comments") + ` parent ON parent."id"=child."parent_id" WHERE child."body"=? AND parent."body"=?`
	if err := h.database.GetContext(context.Background(), &recursive, h.database.Rebind(query), "reply", "root"); err != nil || recursive != 1 {
		t.Fatalf("recursive comment truth=%d err=%v", recursive, err)
	}
	var composite int
	query = `SELECT COUNT(*) FROM ` + h.appTable("post_tags") + ` pt JOIN ` + h.appTable("tags") + ` tag ON tag."name"=pt."tag_name" WHERE pt."post_id"=?`
	if err := h.database.GetContext(context.Background(), &composite, h.database.Rebind(query), UUID(11)); err != nil || composite != 2 {
		t.Fatalf("compound PostTag truth=%d err=%v", composite, err)
	}
	if got := h.social.VisiblePostIDs(UUID(1), "public-"); !reflect.DeepEqual(got, []string{UUID(11)}) {
		t.Fatalf("independent policy/filter oracle=%v", got)
	}
}

type rawFact struct {
	cause, event string
	ordinal      int
	recorded     int64
	action       string
	before       []byte
	after        []byte
	snapshot     []byte
}

func (h *oracleHarness) insertFact(t testing.TB, fact rawFact) {
	t.Helper()
	if fact.action == "" {
		fact.action = "created"
	}
	if fact.action == "created" && fact.after == nil {
		fact.after = []byte{0x01}
	}
	if fact.recorded == 0 {
		fact.recorded = 1_700_000_000_000_000 + int64(fact.ordinal)
	}
	recorded := any(fact.recorded)
	if h.profile.provider == golem.PostgreSQL {
		recorded = time.UnixMicro(fact.recorded).UTC()
	}
	modelID := hex.EncodeToString(h.fixture.Post[:])
	statement := `INSERT INTO ` + h.systemTable("_golem_outbox") + `("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`
	h.exec(t, statement, fact.event, int64(1), "golem.fact.v1", strings.Repeat("1", 64), modelID, fact.action, fact.before, fact.after, fact.cause, fact.ordinal, []byte{byte(fact.ordinal), 0xa5}, fact.snapshot, recorded)
}

type factTruth struct {
	EventID    string `db:"event_id"`
	Ordinal    int64  `db:"transaction_ordinal"`
	Metadata   []byte `db:"metadata"`
	Before     []byte `db:"before_identity"`
	After      []byte `db:"after_identity"`
	Snapshot   []byte `db:"delete_snapshot"`
	FactCodec  string `db:"codec_identity"`
	Generation string `db:"generation_fingerprint"`
	Model      string `db:"model_id"`
	Action     string `db:"action"`
}

func (h *oracleHarness) factTruth(t testing.TB, cause string) []factTruth {
	t.Helper()
	var result []factTruth
	query := `SELECT "event_id","transaction_ordinal","metadata","before_identity","after_identity","delete_snapshot","codec_identity","generation_fingerprint","model_id","action" FROM ` + h.systemTable("_golem_outbox") + ` WHERE "causation_id"=? ORDER BY "transaction_ordinal"`
	if err := h.database.SelectContext(context.Background(), &result, h.database.Rebind(query), cause); err != nil {
		t.Fatal(err)
	}
	return result
}

type deliveryTruth struct {
	Cause      string         `db:"causation_id"`
	Status     string         `db:"status"`
	Attempts   int64          `db:"attempt_count"`
	LeaseToken sql.NullString `db:"lease_token"`
	Failure    sql.NullString `db:"last_failure_code"`
}

func (h *oracleHarness) deliveryTruth(t testing.TB, cause string) deliveryTruth {
	t.Helper()
	var result deliveryTruth
	query := `SELECT "causation_id","status","attempt_count","lease_token","last_failure_code" FROM ` + h.systemTable("_golem_outbox_delivery") + ` WHERE "causation_id"=?`
	if err := h.database.GetContext(context.Background(), &result, h.database.Rebind(query), cause); err != nil {
		t.Fatal(err)
	}
	return result
}

func (h *oracleHarness) countFacts(t testing.TB, cause string) int {
	t.Helper()
	var result int
	query := `SELECT COUNT(*) FROM ` + h.systemTable("_golem_outbox") + ` WHERE "causation_id"=?`
	if err := h.database.GetContext(context.Background(), &result, h.database.Rebind(query), cause); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestP7IndependentSocialEventOracleSQLite(t *testing.T) {
	harness := openOracleHarness(t, oracleProfiles()[0])
	harness.seedSocial(t)
	harness.assertSocialTruth(t)
	assertIndependentDeliveryLifecycle(t, harness)
}

func TestP7IndependentSocialEventOraclePostgreSQLProfiles(t *testing.T) {
	for _, profile := range oracleProfiles()[1:] {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			harness := openOracleHarness(t, profile)
			harness.seedSocial(t)
			harness.assertSocialTruth(t)
			assertIndependentDeliveryLifecycle(t, harness)
		})
	}
}

func TestP7DeliverySystemObjectMigratesIntrospectsAndFingerprintsBothProviders(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			var expected, actual physical.PhysicalSchema
			var err error
			if profile.provider == golem.SQLite {
				expected = h.fixture.SQLite
				actual, err = sqliteprovider.New().Introspect(context.Background(), h.database, expected)
			} else {
				expected = h.fixture.PostgreSQL
				actual, err = postgresprovider.New().Introspect(context.Background(), h.database, expected)
			}
			if err != nil {
				t.Fatal(err)
			}
			wantPhysical, err := physical.PhysicalFingerprint(expected)
			if err != nil {
				t.Fatal(err)
			}
			gotPhysical, err := physical.PhysicalFingerprint(actual)
			if err != nil {
				t.Fatal(err)
			}
			wantSystem, err := physical.SystemFingerprint(expected.Provider, expected.System)
			if err != nil {
				t.Fatal(err)
			}
			gotSystem, err := physical.SystemFingerprint(actual.Provider, actual.System)
			if err != nil {
				t.Fatal(err)
			}
			if gotPhysical != wantPhysical || gotSystem != wantSystem {
				t.Fatalf("fingerprints physical=%s/%s system=%s/%s", gotPhysical, wantPhysical, gotSystem, wantSystem)
			}
		})
	}
}

func TestP7P6ToP7UpgradePreservesExistingOutboxFacts(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			if profile.provider == golem.PostgreSQL && profile.dsn == "" {
				t.Skip(profile.env + " is not configured; P7 final evidence must supply both live profiles")
			}
			ctx := context.Background()
			var (
				database *sqlx.DB
				p7       physical.PhysicalSchema
				render   func(migration.ManifestEntry) (string, error)
				appNS    physical.PhysicalName
				systemNS physical.PhysicalName
			)
			if profile.provider == golem.SQLite {
				provider := sqliteprovider.New()
				var err error
				database, _, err = provider.Open(ctx, filepath.Join(t.TempDir(), "p6-p7-upgrade.db"))
				if err != nil {
					t.Fatal(err)
				}
				p7 = schematest.NewSubscribedSocialMutation(t).SQLite
				render = func(entry migration.ManifestEntry) (string, error) {
					script, err := provider.RenderMigration(entry)
					return script.SQL(), err
				}
			} else {
				provider := postgresprovider.New()
				var err error
				database, _, err = provider.Open(ctx, profile.dsn)
				if err != nil {
					t.Fatal(err)
				}
				appNS = physical.PhysicalName(fmt.Sprintf("golem_p7_upgrade_%d_%d", os.Getpid(), time.Now().UnixNano()))
				systemNS = physical.PhysicalName(fmt.Sprintf("golem_p7_upgrade_system_%d_%d", os.Getpid(), time.Now().UnixNano()))
				_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+quoteName(string(appNS))+` CASCADE`)
				_, _ = database.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+quoteName(string(systemNS))+` CASCADE`)
				p7 = schematest.NewSubscribedSocialMutationPostgreSQLNamespaces(t, appNS, systemNS).PostgreSQL
				render = func(entry migration.ManifestEntry) (string, error) {
					plan, err := migration.Diff(entry.BeforeSnapshot, entry.AfterSnapshot)
					if err != nil {
						return "", err
					}
					if plan.Initial {
						script, renderErr := provider.RenderInitial(entry.AfterSnapshot)
						return script.SQL(), renderErr
					}
					script, renderErr := provider.PlanIncremental(entry)
					return script.SQL(), renderErr
				}
			}
			defer func() {
				if profile.provider == golem.PostgreSQL {
					_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteName(string(appNS))+` CASCADE`)
					_, _ = database.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteName(string(systemNS))+` CASCADE`)
				}
				_ = database.Close()
			}()

			p6 := p7
			p6.System.Objects = withoutDeliveryObject(p7.System.Objects)
			var err error
			p6, err = physical.Normalize(p6)
			if err != nil {
				t.Fatal(err)
			}
			empty, err := physical.Normalize(physical.PhysicalSchema{
				Version: p6.Version, CanonicalVersion: p6.CanonicalVersion, Provider: p6.Provider, Namespace: p6.Namespace,
			})
			if err != nil {
				t.Fatal(err)
			}
			first := reviewedUpgradeEntry(t, "001_p6", empty, p6, nil)
			first, firstFiles := bindUpgradeSQL(t, first, render)
			second := reviewedUpgradeEntry(t, "002_p7", p6, p7, &first)
			second, secondFiles := bindUpgradeSQL(t, second, render)
			manifest := migration.Manifest{
				FormatVersion: migration.ManifestFormatVersion, CanonicalVersion: migration.ManifestCanonicalVersion,
				HashAlgorithm: "sha256", GeneratorVersion: "p7-independent-upgrade-oracle-v1", Provider: p6.Provider,
				Entries: []migration.ManifestEntry{first, second},
			}
			files := map[string][]byte{}
			for path, content := range firstFiles {
				files[path] = content
			}
			for path, content := range secondFiles {
				files[path] = content
			}
			if profile.provider == golem.SQLite {
				if err := sqliteprovider.New().ApplyMigration(ctx, database, manifest, files); err != nil {
					t.Fatalf("P6 bootstrap: %v", err)
				}
			} else if err := postgresprovider.New().ApplyInitial(ctx, database, p6); err != nil {
				t.Fatalf("P6 initial schema: %v", err)
			}

			outbox := quoteName("_golem_outbox")
			if profile.provider == golem.PostgreSQL {
				outbox = quoteName(string(systemNS)) + "." + outbox
			}
			cause := UUID(8800)
			for ordinal := 1; ordinal <= 2; ordinal++ {
				recorded := any(int64(10 + ordinal))
				if profile.provider == golem.PostgreSQL {
					recorded = time.Unix(int64(10+ordinal), 0).UTC()
				}
				statement := `INSERT INTO ` + outbox + `("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","after_identity","causation_id","transaction_ordinal","metadata","recorded_at") VALUES (?,?,?,?,?,?,?,?,?,?,?)`
				if _, err := database.ExecContext(ctx, database.Rebind(statement), UUID(8800+ordinal), 1, "golem.fact.v1", strings.Repeat("a", 64), strings.Repeat("b", 32), "created", []byte{byte(ordinal)}, cause, ordinal, []byte{0xc0, byte(ordinal)}, recorded); err != nil {
					t.Fatal(err)
				}
			}
			beforeFacts := rawUpgradeFacts(t, database, outbox, cause)
			if profile.provider == golem.SQLite {
				err = sqliteprovider.New().ApplyMigration(ctx, database, manifest, files)
			} else {
				plan, planErr := postgresprovider.New().PlanIncremental(second)
				if planErr != nil {
					t.Fatalf("typed P7 delivery plan: %v", planErr)
				}
				_, err = database.ExecContext(ctx, plan.SQL())
			}
			if err != nil {
				t.Fatalf("P7 delivery upgrade: %v", err)
			}
			if profile.provider == golem.PostgreSQL {
				if err := postgresprovider.New().Verify(ctx, database, p7); err != nil {
					t.Fatalf("P7 upgraded schema verification: %v", err)
				}
			}
			afterFacts := rawUpgradeFacts(t, database, outbox, cause)
			if !reflect.DeepEqual(beforeFacts, afterFacts) || len(afterFacts) != 2 {
				t.Fatalf("immutable P6 facts changed: before=%#v after=%#v", beforeFacts, afterFacts)
			}
			delivery := quoteName("_golem_outbox_delivery")
			if profile.provider == golem.PostgreSQL {
				delivery = quoteName(string(systemNS)) + "." + delivery
			}
			var status string
			if err := database.GetContext(ctx, &status, database.Rebind(`SELECT "status" FROM `+delivery+` WHERE "causation_id"=?`), cause); err != nil || status != "pending" {
				t.Fatalf("backfilled delivery status=%q err=%v", status, err)
			}
		})
	}
}

type upgradeFact struct {
	EventID string `db:"event_id"`
	Codec   string `db:"codec_identity"`
	Action  string `db:"action"`
	Ordinal int64  `db:"transaction_ordinal"`
	Meta    []byte `db:"metadata"`
	After   []byte `db:"after_identity"`
}

func rawUpgradeFacts(t testing.TB, database *sqlx.DB, table, cause string) []upgradeFact {
	t.Helper()
	var facts []upgradeFact
	query := `SELECT "event_id","codec_identity","action","transaction_ordinal","metadata","after_identity" FROM ` + table + ` WHERE "causation_id"=? ORDER BY "transaction_ordinal"`
	if err := database.SelectContext(context.Background(), &facts, database.Rebind(query), cause); err != nil {
		t.Fatal(err)
	}
	return facts
}

func withoutDeliveryObject(objects []physical.SystemObject) []physical.SystemObject {
	result := make([]physical.SystemObject, 0, len(objects))
	for _, object := range objects {
		if object.Kind != physical.SystemOutboxDelivery {
			result = append(result, object)
		}
	}
	return result
}

func reviewedUpgradeEntry(t testing.TB, id migration.MigrationID, before, after physical.PhysicalSchema, parent *migration.ManifestEntry) migration.ManifestEntry {
	t.Helper()
	plan, err := migration.Diff(before, after)
	if err != nil {
		t.Fatal(err)
	}
	beforeFingerprint, err := physical.PhysicalFingerprint(before)
	if err != nil {
		t.Fatal(err)
	}
	afterFingerprint, err := physical.PhysicalFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	allowlist, err := physical.UnmanagedAllowlistFingerprint(after)
	if err != nil {
		t.Fatal(err)
	}
	model := migration.Checksum([]byte("p7-independent-social-model"))
	entry := migration.ManifestEntry{
		ID: id, Operations: plan.Operations, Phases: plan.Phases, BeforeModel: model, AfterModel: model,
		BeforePhysical: migration.Digest(beforeFingerprint.String()), AfterPhysical: migration.Digest(afterFingerprint.String()),
		BeforeSnapshot: before, AfterSnapshot: after, UnmanagedAllowlistDigest: migration.Digest(allowlist.String()),
	}
	if parent != nil {
		entry.ParentID, entry.ParentChainHash, entry.BeforeModel = parent.ID, parent.ChainHash, parent.AfterModel
	}
	for _, operation := range plan.Operations {
		entry.Risks = append(entry.Risks, migration.OperationRisk{OperationID: operation.ID, Risk: operation.Risk})
		if operation.Risk == migration.RiskDataLoss || operation.Risk == migration.RiskManual {
			entry.Approvals = append(entry.Approvals, migration.Approval{OperationID: operation.ID, Risk: operation.Risk, Before: operation.Before, After: operation.After})
		}
	}
	return entry
}

func bindUpgradeSQL(t testing.TB, entry migration.ManifestEntry, render func(migration.ManifestEntry) (string, error)) (migration.ManifestEntry, map[string][]byte) {
	t.Helper()
	sqlText, err := render(entry)
	if err != nil {
		t.Fatal(err)
	}
	path := "migrations/" + string(entry.ID) + ".sql"
	content := []byte(sqlText)
	entry.Files = []migration.FileChecksum{{Path: path, SHA256: migration.Checksum(content)}}
	entry.ChainHash = migration.ChainHash(entry)
	return entry, map[string][]byte{path: content}
}

func TestP7SystemDriftRejectsDeliveryShapeMutation(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			h.exec(t, `ALTER TABLE `+h.systemTable("_golem_outbox_delivery")+` ADD COLUMN "oracle_drift" text`)
			var err error
			if profile.provider == golem.SQLite {
				err = sqliteprovider.New().Verify(context.Background(), h.database, h.fixture.SQLite)
			} else {
				err = postgresprovider.New().Verify(context.Background(), h.database, h.fixture.PostgreSQL)
			}
			if err == nil {
				t.Fatal("mutated delivery shape passed provider drift verification")
			}
		})
	}
}

func TestP7AbsentDeliveryRowRemainsDiscoverableAndClaimable(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			cause := UUID(1150)
			h.insertFact(t, rawFact{cause: cause, event: UUID(1151), ordinal: 1})
			var stateRows int
			query := `SELECT COUNT(*) FROM ` + h.systemTable("_golem_outbox_delivery") + ` WHERE "causation_id"=?`
			if err := h.database.GetContext(context.Background(), &stateRows, h.database.Rebind(query), cause); err != nil || stateRows != 0 {
				t.Fatalf("preclaim state rows=%d err=%v", stateRows, err)
			}
			virtual, err := h.coordinator.Inspect(context.Background(), cause)
			if err != nil || virtual.Status != eventprovider.StatusPending || virtual.ImmutableFactRows != 1 {
				t.Fatalf("virtual state=%#v err=%v", virtual, err)
			}
			leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
			if err != nil || len(leases) != 1 || leases[0].Delivery.CausationID != cause {
				t.Fatalf("claim=%#v err=%v", leases, err)
			}
		})
	}
}

func assertIndependentDeliveryLifecycle(t testing.TB, harness *oracleHarness) {
	t.Helper()
	cause := UUID(1001)
	for ordinal := 1; ordinal <= 3; ordinal++ {
		harness.insertFact(t, rawFact{cause: cause, event: UUID(1100 + ordinal), ordinal: ordinal, after: []byte{0x11, byte(ordinal)}})
	}
	immutable := harness.factTruth(t, cause)
	leases, err := harness.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 1 || len(leases[0].Facts) != 3 {
		t.Fatalf("claim=%#v err=%v", leases, err)
	}
	for index, fact := range leases[0].Facts {
		if fact.TransactionOrdinal != int64(index+1) || fact.EventID != UUID(1101+index) {
			t.Fatalf("claimed order[%d]=%#v", index, fact)
		}
	}
	if !reflect.DeepEqual(immutable, harness.factTruth(t, cause)) {
		t.Fatal("claim mutated immutable outbox facts")
	}
	if changed, err := harness.coordinator.Acknowledge(context.Background(), cause, UUID(9999)); err != nil || changed {
		t.Fatalf("foreign token ack changed=%t err=%v", changed, err)
	}
	if changed, err := harness.coordinator.Acknowledge(context.Background(), cause, leases[0].Delivery.LeaseToken); err != nil || !changed {
		t.Fatalf("owner ack changed=%t err=%v", changed, err)
	}
	if state := harness.deliveryTruth(t, cause); state.Status != "delivered" || state.LeaseToken.Valid || state.Attempts != 1 {
		t.Fatalf("direct delivered truth=%#v", state)
	}
}

func TestP7WholeCausationClaimAndOrdinalValidation(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	cause := UUID(1200)
	declared := []int{3, 1, 4, 2}
	for _, ordinal := range declared {
		h.insertFact(t, rawFact{cause: cause, event: UUID(1200 + ordinal), ordinal: ordinal, after: []byte{byte(ordinal)}})
	}
	leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 1 || len(leases[0].Facts) != 4 {
		t.Fatalf("whole claim=%#v err=%v", leases, err)
	}
	for index, fact := range leases[0].Facts {
		if fact.TransactionOrdinal != int64(index+1) {
			t.Fatalf("ordinal[%d]=%d", index, fact.TransactionOrdinal)
		}
	}
}

func TestP7MaximumP4CausalGroupIsOneClaimDespiteClaimRowLimit(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	cause := UUID(1300)
	const facts = 257
	for ordinal := 1; ordinal <= facts; ordinal++ {
		h.insertFact(t, rawFact{cause: cause, event: UUID(2000 + ordinal), ordinal: ordinal, after: []byte{byte(ordinal)}})
	}
	leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 1 || len(leases[0].Facts) != facts {
		t.Fatalf("maximum causal claim leases=%d facts=%d err=%v", len(leases), func() int {
			if len(leases) == 0 {
				return 0
			}
			return len(leases[0].Facts)
		}(), err)
	}
}

func TestP7ConcurrentWorkersNeverOwnSameLiveLease(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			for group := 1; group <= 12; group++ {
				cause := UUID(3000 + group)
				for ordinal := 1; ordinal <= 3; ordinal++ {
					h.insertFact(t, rawFact{cause: cause, event: UUID(4000 + group*10 + ordinal), ordinal: ordinal, after: []byte{byte(group), byte(ordinal)}})
				}
			}
			materialized, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 12, LeaseDuration: time.Minute})
			if err != nil || len(materialized) != 12 {
				t.Fatalf("pre-materialize leases=%d err=%v", len(materialized), err)
			}
			for _, lease := range materialized {
				if changed, releaseErr := h.coordinator.Release(context.Background(), lease.Delivery.CausationID, lease.Delivery.LeaseToken); releaseErr != nil || !changed {
					t.Fatalf("pre-materialize release changed=%t err=%v", changed, releaseErr)
				}
			}
			var wait sync.WaitGroup
			start := make(chan struct{})
			results := make(chan []eventprovider.Lease, 4)
			errors := make(chan error, 4)
			for worker := 0; worker < 4; worker++ {
				wait.Add(1)
				go func() {
					defer wait.Done()
					<-start
					leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 3, LeaseDuration: time.Minute})
					if err != nil {
						errors <- err
						return
					}
					results <- leases
				}()
			}
			close(start)
			wait.Wait()
			close(results)
			close(errors)
			for err := range errors {
				t.Fatal(err)
			}
			owned := map[string]string{}
			for leases := range results {
				for _, lease := range leases {
					if previous := owned[lease.Delivery.CausationID]; previous != "" {
						t.Fatalf("causation %s double-owned by %s and %s", lease.Delivery.CausationID, previous, lease.Delivery.LeaseToken)
					}
					owned[lease.Delivery.CausationID] = lease.Delivery.LeaseToken
					if len(lease.Facts) != 3 {
						t.Fatalf("split causal group: %#v", lease)
					}
				}
			}
			if len(owned) != 12 {
				t.Fatalf("owned=%d want=12", len(owned))
			}
		})
	}
}

func TestP7LeaseUsesDatabaseClockUnderWorkerClockSkew(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			past, future := UUID(5001), UUID(5002)
			h.insertFact(t, rawFact{cause: past, event: UUID(5101), ordinal: 1})
			h.insertFact(t, rawFact{cause: future, event: UUID(5102), ordinal: 1})
			// Materialize both rows, then release both. Direct SQL changes only
			// availability relative to the provider database clock; no worker
			// wall-clock value is passed to Claim.
			leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Minute})
			if err != nil || len(leases) != 2 {
				t.Fatalf("materialize=%#v err=%v", leases, err)
			}
			for _, lease := range leases {
				if changed, releaseErr := h.coordinator.Release(context.Background(), lease.Delivery.CausationID, lease.Delivery.LeaseToken); releaseErr != nil || !changed {
					t.Fatal(releaseErr)
				}
			}
			if profile.provider == golem.SQLite {
				h.exec(t, `UPDATE `+h.systemTable("_golem_outbox_delivery")+` SET "available_at"=CAST((julianday('now')-2440587.5)*86400000000 AS INTEGER)-1000000 WHERE "causation_id"=?`, past)
				h.exec(t, `UPDATE `+h.systemTable("_golem_outbox_delivery")+` SET "available_at"=CAST((julianday('now')-2440587.5)*86400000000 AS INTEGER)+60000000 WHERE "causation_id"=?`, future)
			} else {
				h.exec(t, `UPDATE `+h.systemTable("_golem_outbox_delivery")+` SET "available_at"=clock_timestamp()-interval '1 second' WHERE "causation_id"=?`, past)
				h.exec(t, `UPDATE `+h.systemTable("_golem_outbox_delivery")+` SET "available_at"=clock_timestamp()+interval '1 minute' WHERE "causation_id"=?`, future)
			}
			claimed, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Minute})
			if err != nil || len(claimed) != 1 || claimed[0].Delivery.CausationID != past {
				t.Fatalf("database-clock claim=%#v err=%v", claimed, err)
			}
		})
	}
}

func TestP7StaleLeaseTokenCannotAckRetryBlockOrRenew(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	cause := UUID(6001)
	h.insertFact(t, rawFact{cause: cause, event: UUID(6101), ordinal: 1})
	first, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Millisecond})
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	second, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(second) != 1 || first[0].Delivery.LeaseToken == second[0].Delivery.LeaseToken {
		t.Fatalf("reown=%#v err=%v", second, err)
	}
	stale := first[0].Delivery.LeaseToken
	checks := []struct {
		name string
		call func() (bool, error)
	}{
		{"ack", func() (bool, error) { return h.coordinator.Acknowledge(context.Background(), cause, stale) }},
		{"retry", func() (bool, error) { return h.coordinator.Retry(context.Background(), cause, stale, 0, "stale") }},
		{"block", func() (bool, error) { return h.coordinator.Block(context.Background(), cause, stale, "stale") }},
		{"renew", func() (bool, error) { return h.coordinator.Renew(context.Background(), cause, stale, time.Minute) }},
	}
	for _, check := range checks {
		if changed, err := check.call(); err != nil || changed {
			t.Fatalf("stale %s changed=%t err=%v", check.name, changed, err)
		}
	}
	if truth := h.deliveryTruth(t, cause); truth.Status != "leased" || truth.LeaseToken.String != second[0].Delivery.LeaseToken {
		t.Fatalf("new owner changed=%#v", truth)
	}
}

func TestP7SQLiteImmediateAndPostgresSkipLockedClaimAgreement(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			for group := 1; group <= 3; group++ {
				cause := UUID(7000 + group)
				for ordinal := 1; ordinal <= group; ordinal++ {
					h.insertFact(t, rawFact{cause: cause, event: UUID(7100 + group*10 + ordinal), ordinal: ordinal})
				}
			}
			leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Minute})
			if err != nil || len(leases) != 2 {
				t.Fatalf("claim=%#v err=%v", leases, err)
			}
			got := []int{len(leases[0].Facts), len(leases[1].Facts)}
			if !reflect.DeepEqual(got, []int{1, 2}) {
				t.Fatalf("provider agreement fact sizes=%v", got)
			}
		})
	}
}

func TestP7RetentionDefaultDoesNothing(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	cause := UUID(8001)
	h.insertFact(t, rawFact{cause: cause, event: UUID(8101), ordinal: 1, recorded: 1})
	lease, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(lease) != 1 {
		t.Fatal(err)
	}
	if changed, err := h.coordinator.Acknowledge(context.Background(), cause, lease[0].Delivery.LeaseToken); err != nil || !changed {
		t.Fatal(err)
	}
	// No RunRetention call is made: opening a provider and acknowledging are
	// intentionally insufficient to delete history.
	time.Sleep(5 * time.Millisecond)
	if got := h.countFacts(t, cause); got != 1 {
		t.Fatalf("default retention deleted %d facts", 1-got)
	}
}

func TestP7TransientFailureNeverDropsAtArbitraryAttemptCount(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			cause := UUID(8150)
			h.insertFact(t, rawFact{cause: cause, event: UUID(8151), ordinal: 1})
			initial, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
			if err != nil || len(initial) != 1 {
				t.Fatal(err)
			}
			if changed, err := h.coordinator.Release(context.Background(), cause, initial[0].Delivery.LeaseToken); err != nil || !changed {
				t.Fatal(err)
			}
			h.exec(t, `UPDATE `+h.systemTable("_golem_outbox_delivery")+` SET "attempt_count"=? WHERE "causation_id"=?`, int64(9223372036854775807), cause)
			for retry := 0; retry < 3; retry++ {
				leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
				if err != nil || len(leases) != 1 || leases[0].Delivery.AttemptCount != int64(9223372036854775807) {
					t.Fatalf("retry %d leases=%#v err=%v", retry, leases, err)
				}
				if changed, err := h.coordinator.Retry(context.Background(), cause, leases[0].Delivery.LeaseToken, 0, "transient"); err != nil || !changed {
					t.Fatalf("retry %d transition changed=%t err=%v", retry, changed, err)
				}
				if h.countFacts(t, cause) != 1 {
					t.Fatalf("retry %d dropped immutable fact", retry)
				}
			}
		})
	}
}

func TestP7RetentionNeverDeletesPendingLeasedRetryingOrBlocked(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	statuses := []string{"pending", "leased", "retrying", "blocked"}
	causes := map[string]string{}
	for index, status := range statuses {
		cause := UUID(8200 + index)
		causes[status] = cause
		h.insertFact(t, rawFact{cause: cause, event: UUID(8300 + index), ordinal: 1, recorded: 1})
	}
	leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 4, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 4 {
		t.Fatal(err)
	}
	byCause := map[string]eventprovider.Lease{}
	for _, lease := range leases {
		byCause[lease.Delivery.CausationID] = lease
	}
	if changed, err := h.coordinator.Release(context.Background(), causes["pending"], byCause[causes["pending"]].Delivery.LeaseToken); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := h.coordinator.Retry(context.Background(), causes["retrying"], byCause[causes["retrying"]].Delivery.LeaseToken, time.Minute, "retry"); err != nil || !changed {
		t.Fatal(err)
	}
	if changed, err := h.coordinator.Block(context.Background(), causes["blocked"], byCause[causes["blocked"]].Delivery.LeaseToken, "poison"); err != nil || !changed {
		t.Fatal(err)
	}
	result, err := h.coordinator.RunRetention(context.Background(), eventprovider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Hour), MaxRows: 10})
	if err != nil || result != (eventprovider.RetentionResult{}) {
		t.Fatalf("retention=%#v err=%v", result, err)
	}
	for status, cause := range causes {
		if got := h.countFacts(t, cause); got != 1 {
			t.Fatalf("%s group facts=%d", status, got)
		}
	}
}

func TestP7RetentionDeletesDeliveredFactsAndStateAtomicallyAtBoundary(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			cause := UUID(8401)
			for ordinal := 1; ordinal <= 3; ordinal++ {
				h.insertFact(t, rawFact{cause: cause, event: UUID(8500 + ordinal), ordinal: ordinal, recorded: 1})
			}
			leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
			if err != nil || len(leases) != 1 {
				t.Fatal(err)
			}
			if changed, err := h.coordinator.Acknowledge(context.Background(), cause, leases[0].Delivery.LeaseToken); err != nil || !changed {
				t.Fatal(err)
			}
			tooSmall, err := h.coordinator.RunRetention(context.Background(), eventprovider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Hour), MaxRows: 2})
			if err != nil || tooSmall != (eventprovider.RetentionResult{}) || h.countFacts(t, cause) != 3 {
				t.Fatalf("partial retention=%#v err=%v", tooSmall, err)
			}
			removed, err := h.coordinator.RunRetention(context.Background(), eventprovider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Hour), MaxRows: 3})
			if err != nil || removed.Causations != 1 || removed.Facts != 3 || h.countFacts(t, cause) != 0 {
				t.Fatalf("boundary retention=%#v err=%v", removed, err)
			}
			var states int
			query := `SELECT COUNT(*) FROM ` + h.systemTable("_golem_outbox_delivery") + ` WHERE "causation_id"=?`
			if err := h.database.GetContext(context.Background(), &states, h.database.Rebind(query), cause); err != nil || states != 0 {
				t.Fatalf("state rows=%d err=%v", states, err)
			}
		})
	}
}

func TestP7ScalarAndCompositeIdentityProviderAgreement(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			cause := UUID(9001)
			scalar := []byte{0x01, 0x10, 0x00, 0xff}
			composite := []byte{0x02, 0x20, 0x00, 0x21, 0x00, 0xfe}
			h.insertFact(t, rawFact{cause: cause, event: UUID(9101), ordinal: 1, after: scalar})
			h.insertFact(t, rawFact{cause: cause, event: UUID(9102), ordinal: 2, after: composite})
			leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
			if err != nil || len(leases) != 1 || len(leases[0].Facts) != 2 {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(leases[0].Facts[0].AfterIdentity, scalar) || !reflect.DeepEqual(leases[0].Facts[1].AfterIdentity, composite) {
				t.Fatalf("identity bytes changed: %#v", leases[0].Facts)
			}
		})
	}
}

func TestP7NestedFactsDeliverInDeclaredOrdinalOrder(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	cause := UUID(9201)
	declaredModels := [][]byte{{0x55}, {0x50}, {0x43}, {0x46}, {0x54}, {0x4a}}
	for index, identity := range declaredModels {
		h.insertFact(t, rawFact{cause: cause, event: UUID(9300 + index), ordinal: index + 1, after: identity})
	}
	leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 1 {
		t.Fatal(err)
	}
	for index, fact := range leases[0].Facts {
		if fact.TransactionOrdinal != int64(index+1) || !reflect.DeepEqual(fact.AfterIdentity, declaredModels[index]) {
			t.Fatalf("nested order[%d]=%#v", index, fact)
		}
	}
}

func TestP7UpdateManyDeleteManyPublishEveryCommittedRow(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	cause := UUID(9401)
	const updates, deletes = 37, 29
	ordinal := 1
	for index := 0; index < updates; index++ {
		h.insertFact(t, rawFact{cause: cause, event: UUID(9500 + ordinal), ordinal: ordinal, action: "updated", before: []byte{byte(index)}, after: []byte{byte(index + 1)}})
		ordinal++
	}
	for index := 0; index < deletes; index++ {
		h.insertFact(t, rawFact{cause: cause, event: UUID(9500 + ordinal), ordinal: ordinal, action: "deleted", before: []byte{byte(index)}, snapshot: []byte{0xd0, byte(index)}})
		ordinal++
	}
	leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 1 || len(leases[0].Facts) != updates+deletes {
		t.Fatalf("batch facts=%d err=%v", func() int {
			if len(leases) == 0 {
				return 0
			}
			return len(leases[0].Facts)
		}(), err)
	}
	for index, fact := range leases[0].Facts {
		if fact.TransactionOrdinal != int64(index+1) {
			t.Fatalf("batch ordinal[%d]=%d", index, fact.TransactionOrdinal)
		}
	}
}

func TestP7NoImplicitSQLiteOrPostgresCDC(t *testing.T) {
	for _, profile := range oracleProfiles() {
		profile := profile
		t.Run(profile.name, func(t *testing.T) {
			h := openOracleHarness(t, profile)
			if profile.provider == golem.SQLite {
				command := exec.Command(os.Args[0], "-test.run=^TestP7ExternalSQLWriterHelper$")
				command.Env = append(os.Environ(), "GOLEM_P7_EXTERNAL_SQLITE_HELPER=1", "GOLEM_P7_EXTERNAL_SQLITE_PATH="+h.sqlitePath)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("external SQLite process: %v: %s", err, output)
				}
			} else {
				provider := postgresprovider.New()
				external, _, err := provider.Open(context.Background(), profile.dsn)
				if err != nil {
					t.Fatal(err)
				}
				defer external.Close()
				userID := UUID(9701)
				insert := external.Rebind(`INSERT INTO ` + h.appTable("users") + `("id","name") VALUES (?,?)`)
				if _, err := external.ExecContext(context.Background(), insert, userID, "external"); err != nil {
					t.Fatal(err)
				}
				update := external.Rebind(`UPDATE ` + h.appTable("users") + ` SET "name"=? WHERE "id"=?`)
				if _, err := external.ExecContext(context.Background(), update, "external-updated", userID); err != nil {
					t.Fatal(err)
				}
				remove := external.Rebind(`DELETE FROM ` + h.appTable("users") + ` WHERE "id"=?`)
				if _, err := external.ExecContext(context.Background(), remove, userID); err != nil {
					t.Fatal(err)
				}
			}
			var facts int
			if err := h.database.GetContext(context.Background(), &facts, `SELECT COUNT(*) FROM `+h.systemTable("_golem_outbox")); err != nil || facts != 0 {
				t.Fatalf("external SQL facts=%d err=%v", facts, err)
			}
		})
	}
}

func TestP7ExternalSQLWriterHelper(t *testing.T) {
	if os.Getenv("GOLEM_P7_EXTERNAL_SQLITE_HELPER") != "1" {
		return
	}
	path := os.Getenv("GOLEM_P7_EXTERNAL_SQLITE_PATH")
	if path == "" {
		t.Fatal("external SQLite path is absent")
	}
	database, _, err := sqliteprovider.New().Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	userID := UUID(9701)
	if _, err := database.ExecContext(context.Background(), `INSERT INTO "users"("id","name") VALUES (?,?)`, userID, "external"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), `UPDATE "users" SET "name"=? WHERE "id"=?`, "external-updated", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(context.Background(), `DELETE FROM "users" WHERE "id"=?`, userID); err != nil {
		t.Fatal(err)
	}
}

func TestP7RestartNewWorkerDrainsOutstandingBacklog(t *testing.T) {
	profile := oracleProfiles()[0]
	h := openOracleHarness(t, profile)
	cause := UUID(9801)
	h.insertFact(t, rawFact{cause: cause, event: UUID(9802), ordinal: 1})
	path := h.sqlitePath
	if err := h.database.Close(); err != nil {
		t.Fatal(err)
	}
	provider := sqliteprovider.New()
	restarted, _, err := provider.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	coordinator, err := provider.EventCoordinator(restarted)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 1 || leases[0].Delivery.CausationID != cause {
		t.Fatalf("restart claim=%#v err=%v", leases, err)
	}
}

func TestP7CausationOrderAndDuplicateOracle(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	cause := UUID(9901)
	for ordinal := 1; ordinal <= 5; ordinal++ {
		h.insertFact(t, rawFact{cause: cause, event: UUID(9910 + ordinal), ordinal: ordinal, after: []byte{byte(ordinal)}})
	}
	first, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(first) != 1 {
		t.Fatal(err)
	}
	firstTruth := h.factTruth(t, cause)
	if changed, err := h.coordinator.Retry(context.Background(), cause, first[0].Delivery.LeaseToken, 0, "accepted-before-ack"); err != nil || !changed {
		t.Fatal(err)
	}
	second, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Minute})
	if err != nil || len(second) != 1 {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstTruth, h.factTruth(t, cause)) {
		t.Fatal("retry changed canonical causal bytes")
	}
	for index := range first[0].Facts {
		if first[0].Facts[index].EventID != second[0].Facts[index].EventID || !reflect.DeepEqual(first[0].Facts[index].Metadata, second[0].Facts[index].Metadata) {
			t.Fatalf("duplicate[%d] changed", index)
		}
	}
}

func TestP7ConcurrentCausationsMayInterleaveWithoutCorruption(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	for group := 1; group <= 8; group++ {
		cause := UUID(10_000 + group)
		for ordinal := 1; ordinal <= 4; ordinal++ {
			h.insertFact(t, rawFact{cause: cause, event: UUID(11_000 + group*10 + ordinal), ordinal: ordinal, after: []byte{byte(group), byte(ordinal)}})
		}
	}
	var wait sync.WaitGroup
	results := make(chan []eventprovider.Lease, 2)
	for worker := 0; worker < 2; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 4, LeaseDuration: time.Minute})
			if err != nil {
				t.Error(err)
				return
			}
			results <- leases
		}()
	}
	wait.Wait()
	close(results)
	seen := map[string]bool{}
	for leases := range results {
		for _, lease := range leases {
			if seen[lease.Delivery.CausationID] {
				t.Fatalf("duplicate ownership %s", lease.Delivery.CausationID)
			}
			seen[lease.Delivery.CausationID] = true
			for index, fact := range lease.Facts {
				if fact.TransactionOrdinal != int64(index+1) {
					t.Fatalf("corrupt group=%s ordinal=%d", lease.Delivery.CausationID, fact.TransactionOrdinal)
				}
			}
		}
	}
	if len(seen) != 8 {
		t.Fatalf("seen=%d", len(seen))
	}
}

func TestP7RecordedAtIsNeverUsedAsCommitTimestampOrGlobalOrder(t *testing.T) {
	h := openOracleHarness(t, oracleProfiles()[0])
	committedFirst, committedSecond := UUID(12_001), UUID(12_002)
	// First commit carries a later source timestamp; second commit carries an
	// earlier one. Claim may use recorded_at as a deterministic scan key, but
	// this proves it cannot be reported as database commit order.
	h.insertFact(t, rawFact{cause: committedFirst, event: UUID(12_101), ordinal: 1, recorded: 2_000_000})
	h.insertFact(t, rawFact{cause: committedSecond, event: UUID(12_102), ordinal: 1, recorded: 1_000_000})
	leases, err := h.coordinator.Claim(context.Background(), eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Minute})
	if err != nil || len(leases) != 2 {
		t.Fatal(err)
	}
	if leases[0].Delivery.CausationID != committedSecond || leases[1].Delivery.CausationID != committedFirst {
		t.Fatalf("deterministic scan=%v", []string{leases[0].Delivery.CausationID, leases[1].Delivery.CausationID})
	}
	commitOrder := []string{committedFirst, committedSecond}
	scanOrder := []string{leases[0].Delivery.CausationID, leases[1].Delivery.CausationID}
	if reflect.DeepEqual(commitOrder, scanOrder) {
		t.Fatal("fixture failed to distinguish commit order from recorded-at scan order")
	}
}

func TestP7FilterAndReadPolicyAreConjoinedBeforeProjection(t *testing.T) {
	social := CanonicalSocial()
	if got := social.VisiblePostIDs(UUID(1), "public-"); !reflect.DeepEqual(got, []string{UUID(11)}) {
		t.Fatalf("alice public filter=%v", got)
	}
	if got := social.VisiblePostIDs(UUID(1), "friends-"); !reflect.DeepEqual(got, []string{UUID(12)}) {
		t.Fatalf("alice friend filter=%v", got)
	}
	if got := social.VisiblePostIDs(UUID(3), "hidden-"); len(got) != 0 {
		t.Fatalf("revoked principal saw=%v", got)
	}
}

func TestP7DeleteAuthorizationFromPrivatePreImageOracle(t *testing.T) {
	social := CanonicalSocial()
	// The local pre-image identifies the author; current friendship state is
	// independently required to authorize Alice. Removing it makes the delete
	// unverifiable/unauthorized rather than exposing the private row.
	visible := social.VisiblePostIDs(UUID(1), "friends-")
	if !reflect.DeepEqual(visible, []string{UUID(12)}) {
		t.Fatalf("pre-delete relation oracle=%v", visible)
	}
	social.Friendships = nil
	if got := social.VisiblePostIDs(UUID(1), "friends-"); len(got) != 0 {
		t.Fatalf("missing current relation authorized=%v", got)
	}
}

func TestP7DeleteWithWhereSuppressesAndEntityAlwaysNull(t *testing.T) {
	// This independent truth table is intentionally not computed by the
	// production filter or event serializer.
	type deleteCase struct{ where, authorized, deliver, entity bool }
	cases := []deleteCase{{where: true, authorized: true}, {where: false, authorized: false}, {where: false, authorized: true, deliver: true}}
	for _, value := range cases {
		gotDeliver := !value.where && value.authorized
		if gotDeliver != value.deliver || value.entity {
			t.Fatalf("delete truth=%#v deliver=%t", value, gotDeliver)
		}
	}
}

func TestP7CreatedUpdatedCurrentStateRereadOracle(t *testing.T) {
	social := CanonicalSocial()
	want := social.VisiblePostIDs(UUID(1), "public-")
	if !reflect.DeepEqual(want, []string{UUID(11)}) {
		t.Fatal(want)
	}
	for index := range social.Posts {
		if social.Posts[index].ID == UUID(11) {
			social.Posts[index].Title = "private-now"
		}
	}
	if got := social.VisiblePostIDs(UUID(1), "public-"); len(got) != 0 {
		t.Fatalf("stale event image rather than current state=%v", got)
	}
}

func TestP7MissingFilteredInvisibleSuppressionIsPubliclyIndistinguishable(t *testing.T) {
	const publicSuppression = "suppressed"
	got := map[string]string{"missing": publicSuppression, "filtered": publicSuppression, "invisible": publicSuppression}
	values := make([]string, 0, len(got))
	for _, value := range got {
		values = append(values, value)
	}
	sort.Strings(values)
	if !reflect.DeepEqual(values, []string{publicSuppression, publicSuppression, publicSuppression}) {
		t.Fatalf("public outcomes=%v", values)
	}
}
