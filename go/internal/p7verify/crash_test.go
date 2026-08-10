package p7verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	mutationir "github.com/eleven-am/golem/go/internal/mutation/ir"
	"github.com/eleven-am/golem/go/internal/mutationverify"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

var (
	sqliteCrashOnce     sync.Once
	sqliteCrashEvidence []CrashEvidence
	sqliteCrashError    error
)

func TestP7CrashBoundariesUseKilledAndRestartedSQLiteProcesses(t *testing.T) {
	evidence := cachedSQLiteCrashEvidence(t)
	if len(evidence) != len(crashBoundaries) {
		t.Fatalf("evidence=%d want=%d", len(evidence), len(crashBoundaries))
	}
	for index, record := range evidence {
		if record.Boundary != crashBoundaries[index] || record.Status != "PASS" || record.KilledPID <= 0 || record.RestartedPID <= 0 || record.KilledPID == record.RestartedPID {
			t.Fatalf("boundary evidence[%d]=%#v", index, record)
		}
	}
}

func cachedSQLiteCrashEvidence(t testing.TB) []CrashEvidence {
	t.Helper()
	sqliteCrashOnce.Do(func() {
		root, err := os.MkdirTemp("", "golem-p7-sqlite-crash-test-")
		if err != nil {
			sqliteCrashError = err
			return
		}
		defer os.RemoveAll(root)
		executable := filepath.Join(root, "p7crash")
		build := exec.Command("go", "build", "-o", executable, "./internal/cmd/p7crash")
		build.Dir = filepath.Clean(filepath.Join("..", ".."))
		if output, err := build.CombinedOutput(); err != nil {
			sqliteCrashError = &crashTestError{message: "build crash child", err: err, output: string(output)}
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		profile := crashProfile{Name: "sqlite-file", Provider: "sqlite", Endpoint: "file-backed", DBPath: filepath.Join(root, "crash.sqlite")}
		sqliteCrashError = runProfile(ctx, executable, root, profile, nil, func(record CrashEvidence) {
			sqliteCrashEvidence = append(sqliteCrashEvidence, record)
		})
	})
	if sqliteCrashError != nil {
		t.Fatal(sqliteCrashError)
	}
	return append([]CrashEvidence(nil), sqliteCrashEvidence...)
}

type crashTestError struct {
	message string
	err     error
	output  string
}

func (failure *crashTestError) Error() string {
	return failure.message + ": " + failure.err.Error() + "\n" + failure.output
}

func crashBoundaryEvidence(t testing.TB, boundary string) CrashEvidence {
	t.Helper()
	for _, record := range cachedSQLiteCrashEvidence(t) {
		if record.Boundary == boundary {
			return record
		}
	}
	t.Fatalf("missing crash boundary %q", boundary)
	return CrashEvidence{}
}

func TestP7CrashBeforeClaimCommit(t *testing.T) {
	record := crashBoundaryEvidence(t, "before-claim-commit")
	if record.Status != "PASS" || record.AcceptedCount != 2 || record.DuplicateIDs != 0 {
		t.Fatalf("before-claim-commit=%#v", record)
	}
}

func TestP7CrashAfterClaimBeforePublish(t *testing.T) {
	record := crashBoundaryEvidence(t, "after-claim")
	if record.Status != "PASS" || record.AcceptedCount != 2 || record.DuplicateIDs != 0 {
		t.Fatalf("after-claim=%#v", record)
	}
}

func TestP7CrashAfterTransportBeforeAckDuplicatesSameIDs(t *testing.T) {
	record := crashBoundaryEvidence(t, "accepted-before-ack")
	if record.Status != "PASS" || record.AcceptedCount != 4 || record.DuplicateIDs != 2 {
		t.Fatalf("accepted-before-ack=%#v", record)
	}
}

func TestP7AckCommitPreventsRepublish(t *testing.T) {
	record := crashBoundaryEvidence(t, "after-ack-cleanup")
	if record.Status != "PASS" || record.AcceptedCount != 2 || record.DuplicateIDs != 0 {
		t.Fatalf("after-ack-cleanup=%#v", record)
	}
}

func TestP7LegacyFactBackfillCrashRestartIsIdempotent(t *testing.T) {
	rows, err := canonicalCrashFacts("00000000-0000-4000-8000-000000000161", []string{"10000000-0000-4000-8000-000000000161", "10000000-0000-4000-8000-000000000162"})
	if err != nil {
		t.Fatal(err)
	}
	assertMissingDeliveryBackfillCrashRestart(t, rows)
}

func TestP7PendingV1FactUsesHistoricalBundleAfterRestart(t *testing.T) {
	rows, err := canonicalHistoricalCrashV1Facts("00000000-0000-4000-8000-000000000171", []string{"10000000-0000-4000-8000-000000000171", "10000000-0000-4000-8000-000000000172"})
	if err != nil {
		t.Fatal(err)
	}
	assertMissingDeliveryBackfillCrashRestart(t, rows)
}

func TestP7PublicationRetryNeverRunsMutationHooksOrClosure(t *testing.T) {
	var mutation mutationverify.Mutation
	for _, candidate := range MutationCatalog() {
		if candidate.Label == "RUN_HOOK_ON_PUBLISH_RETRY" {
			mutation = candidate
			break
		}
	}
	if !mutation.Covered() {
		t.Fatal("RUN_HOOK_ON_PUBLISH_RETRY mutation is not executable")
	}
	root := filepath.Clean(filepath.Join("..", ".."))
	result, err := (mutationverify.Runner{ModuleDir: root, Timeout: 2 * time.Minute}).Run(context.Background(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != mutationverify.StatusKilled || result.Test != "TestP7MutationPublisherRetryPathContainsNoApplicationCallback" {
		t.Fatalf("retry hook mutation=%#v", result)
	}
}

func assertMissingDeliveryBackfillCrashRestart(t testing.TB, rows []mutationfact.OutboxRow) {
	t.Helper()
	root := t.TempDir()
	executable := filepath.Join(root, "p7crash")
	build := exec.Command("go", "build", "-o", executable, "./internal/cmd/p7crash")
	build.Dir = filepath.Clean(filepath.Join("..", ".."))
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build crash child: %v\n%s", err, output)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	profile := crashProfile{Name: "sqlite-backfill", Provider: "sqlite", Endpoint: "file-backed", DBPath: filepath.Join(root, "backfill.sqlite")}
	database, coordinator, cleanup, err := prepareProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	defer database.Close()
	for _, row := range rows {
		if _, err := database.ExecContext(ctx, `INSERT INTO "_golem_outbox" ("event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, row.EventID, row.FactVersion, row.CodecIdentity, row.GenerationFingerprint, row.ModelID, row.Action, row.BeforeIdentity, row.AfterIdentity, row.CausationID, row.TransactionOrdinal, row.Metadata, row.DeleteSnapshot, row.RecordedAt.UnixMicro()); err != nil {
			t.Fatal(err)
		}
	}
	ready := filepath.Join(root, "backfill.ready")
	accepted := filepath.Join(root, "backfill.accepted")
	resultPath := filepath.Join(root, "backfill.result")
	initial := CrashChildConfig{Provider: "sqlite", DBPath: profile.DBPath, Mode: "after-claim", Causation: rows[0].CausationID, ReadyPath: ready, LogPath: accepted}
	killed, err := startAndKill(ctx, executable, "", initial, ready, nil)
	if err != nil || killed <= 0 {
		t.Fatalf("kill after backfill claim pid=%d err=%v", killed, err)
	}
	if err := waitLeaseExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	recovery := CrashChildConfig{Provider: "sqlite", DBPath: profile.DBPath, Mode: "recover-after-claim", Causation: rows[0].CausationID, LogPath: accepted, Result: resultPath}
	restarted, err := runChild(ctx, executable, "", recovery, nil)
	if err != nil || restarted <= 0 || restarted == killed {
		t.Fatalf("backfill restart pid=%d killed=%d err=%v", restarted, killed, err)
	}
	var states int
	if err := database.GetContext(ctx, &states, `SELECT COUNT(*) FROM "_golem_outbox_delivery" WHERE "causation_id"=?`, rows[0].CausationID); err != nil || states != 1 {
		t.Fatalf("backfill delivery states=%d err=%v", states, err)
	}
	state, err := coordinator.Inspect(ctx, rows[0].CausationID)
	if err != nil || state.Status != "delivered" {
		t.Fatalf("backfill state=%#v err=%v", state, err)
	}
	journal, err := readAccepted(accepted)
	if err != nil || len(journal) != len(rows) {
		t.Fatalf("backfill journal=%d err=%v", len(journal), err)
	}
}

func canonicalHistoricalCrashV1Facts(causation string, ids []string) ([]mutationfact.OutboxRow, error) {
	resolver, err := canonicalCrashResolver()
	if err != nil {
		return nil, err
	}
	historical := resolver.history[golem.SchemaDigest{0x78}]
	model, ok := historical.Model(resolver.model)
	if !ok || len(model.PrimaryKey()) != 1 {
		return nil, &crashTestError{message: "historical Post identity is unavailable", err: os.ErrInvalid}
	}
	requirement, err := mutationir.NewFactRequirement(mutationir.FactCreated, nil, []policyir.FieldID{policyir.FieldID(model.PrimaryKey()[0])}, nil)
	if err != nil {
		return nil, err
	}
	causationID, err := parseUUID16(causation)
	if err != nil {
		return nil, err
	}
	result := make([]mutationfact.OutboxRow, len(ids))
	for index, text := range ids {
		eventID, parseErr := parseUUID16(text)
		if parseErr != nil {
			return nil, parseErr
		}
		row, rowErr := mutationdecode.NewRow(historical, policyir.ModelID(resolver.model), []mutationdecode.Cell{
			mutationdecode.Value(policyir.FieldID(model.PrimaryKey()[0]), policyir.UUIDValue([16]byte{0x71, byte(index + 1)})),
		})
		if rowErr != nil {
			return nil, rowErr
		}
		fact, factErr := mutationfact.New(historical, mutationfact.EventID(eventID), requirement, mutationfact.CausationID(causationID), uint32(index+1), nil, &row)
		if factErr != nil {
			return nil, factErr
		}
		result[index], factErr = fact.OutboxRow(time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond).Add(time.Duration(index) * time.Microsecond))
		if factErr != nil {
			return nil, factErr
		}
	}
	return result, nil
}
