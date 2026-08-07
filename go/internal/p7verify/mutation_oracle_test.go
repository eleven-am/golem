package p7verify

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/eleven-am/golem/go/golem"
	eventcodec "github.com/eleven-am/golem/go/internal/event/codec"
	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	typedvalue "github.com/eleven-am/golem/go/internal/event/typedvalue"
	"github.com/jmoiron/sqlx"
)

func sqliteMutationFixture(t *testing.T) (context.Context, crashProfile, *sqlx.DB, eventprovider.Coordinator, func()) {
	t.Helper()
	ctx := context.Background()
	profile := crashProfile{Name: "mutation-sqlite", Provider: "sqlite", Endpoint: "file-backed", DBPath: t.TempDir() + "/mutation.sqlite"}
	database, coordinator, cleanup, err := prepareProfile(ctx, profile)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, profile, database, coordinator, func() { cleanup(); _ = database.Close() }
}

func TestP7MutationLeaseTokensArePerClaimCapabilities(t *testing.T) {
	ctx, profile, database, coordinator, cleanup := sqliteMutationFixture(t)
	defer cleanup()
	for index, causation := range []string{"00000000-0000-4000-8000-000000000101", "00000000-0000-4000-8000-000000000102"} {
		ids := []string{formatUUID16([16]byte{0x11, byte(index + 1)})}
		if err := seedCausation(ctx, database, profile, causation, ids); err != nil {
			t.Fatal(err)
		}
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Second})
	if err != nil || len(leases) != 2 {
		t.Fatalf("leases=%d err=%v", len(leases), err)
	}
	if leases[0].Delivery.LeaseToken == "" || leases[0].Delivery.LeaseToken == leases[1].Delivery.LeaseToken {
		t.Fatal("independent claims reused one worker identity instead of unique fencing tokens")
	}
}

func TestP7MutationLeaseExpiryUsesDatabaseClock(t *testing.T) {
	ctx, profile, database, coordinator, cleanup := sqliteMutationFixture(t)
	defer cleanup()
	causation := "00000000-0000-4000-8000-000000000111"
	if err := seedCausation(ctx, database, profile, causation, []string{"10000000-0000-4000-8000-000000000111"}); err != nil {
		t.Fatal(err)
	}
	_, err := database.ExecContext(ctx, `UPDATE "_golem_outbox_delivery" SET "status"='leased',"lease_token"='00000000-0000-4000-8000-000000000222',"lease_until"=CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)+3600000000 WHERE "causation_id"=?`, causation)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 0 {
		t.Fatal("worker clock treated a database-live lease as expired")
	}
}

func TestP7MutationCausalFactsUseTransactionOrdinalNotEventID(t *testing.T) {
	ctx, profile, database, coordinator, cleanup := sqliteMutationFixture(t)
	defer cleanup()
	causation := "00000000-0000-4000-8000-000000000121"
	ids := []string{"f0000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"}
	if err := seedCausation(ctx, database, profile, causation, ids); err != nil {
		t.Fatal(err)
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 1, LeaseDuration: time.Second})
	if err != nil || len(leases) != 1 || len(leases[0].Facts) != 2 {
		t.Fatalf("claim=%d err=%v", len(leases), err)
	}
	if leases[0].Facts[0].EventID != ids[0] || leases[0].Facts[1].EventID != ids[1] {
		t.Fatalf("fact order=%s,%s", leases[0].Facts[0].EventID, leases[0].Facts[1].EventID)
	}
}

func TestP7MutationClaimTieHasCausationIdentityOrder(t *testing.T) {
	ctx, profile, database, coordinator, cleanup := sqliteMutationFixture(t)
	defer cleanup()
	causations := []string{"00000000-0000-4000-8000-000000000132", "00000000-0000-4000-8000-000000000131"}
	for index, causation := range causations {
		if err := seedCausation(ctx, database, profile, causation, []string{formatUUID16([16]byte{0x31, byte(index + 1)})}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := database.ExecContext(ctx, `UPDATE "_golem_outbox_delivery" SET "first_recorded_at"=1,"available_at"=1,"updated_at"=1`)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := coordinator.Claim(ctx, eventprovider.ClaimOptions{Groups: 2, LeaseDuration: time.Second})
	if err != nil || len(leases) != 2 {
		t.Fatalf("claim=%d err=%v", len(leases), err)
	}
	if leases[0].Delivery.CausationID != causations[1] || leases[1].Delivery.CausationID != causations[0] {
		t.Fatalf("causation tie order=%s,%s", leases[0].Delivery.CausationID, leases[1].Delivery.CausationID)
	}
}

func TestP7MutationRetentionCannotTouchPendingFacts(t *testing.T) {
	ctx, profile, database, coordinator, cleanup := sqliteMutationFixture(t)
	defer cleanup()
	causation := "00000000-0000-4000-8000-000000000141"
	if err := seedCausation(ctx, database, profile, causation, []string{"10000000-0000-4000-8000-000000000141"}); err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.RunRetention(ctx, eventprovider.RetentionPolicy{OlderThan: time.Now().UTC().Add(time.Hour), MaxRows: 10})
	if err != nil || result.Causations != 0 || result.Facts != 0 {
		t.Fatalf("retention=%#v err=%v", result, err)
	}
	var count int
	if err := database.GetContext(ctx, &count, `SELECT COUNT(*) FROM "_golem_outbox" WHERE "causation_id"=?`, causation); err != nil || count != 1 {
		t.Fatalf("pending facts=%d err=%v", count, err)
	}
}

func TestP7MutationUnknownCodecIdentityIsRejected(t *testing.T) {
	resolver, err := canonicalCrashResolver()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := canonicalCrashFacts("00000000-0000-4000-8000-000000000151", []string{"10000000-0000-4000-8000-000000000151"})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := eventcodec.EncodeStoredRow(rows[0], resolver, eventcodec.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	encoded := envelope.Encoded()
	identityLengthOffset := len("GOLEMEVENT") + 2
	identityLength := int(binary.BigEndian.Uint16(encoded[identityLengthOffset:]))
	identityOffset := identityLengthOffset + 2
	if identityLength != len(eventcodec.CodecIdentity) {
		t.Fatalf("codec identity length=%d", identityLength)
	}
	encoded[identityOffset+identityLength-1] ^= 1
	if _, err := eventcodec.Decode(encoded, resolver, eventcodec.Limits{}); err == nil {
		t.Fatal("unknown codec identity was guessed as the active codec")
	}
}

func TestP7MutationDeletedEntityCannotEnterPublicTypedValue(t *testing.T) {
	model := golem.ModelID{15: 5}
	row, err := golem.RuntimeModelReadRow(model)
	if err != nil {
		t.Fatal(err)
	}
	schema := golem.EventSchemaDigest{31: 4}
	_, err = typedvalue.New(typedvalue.Metadata{
		EventID: golem.EventID{15: 1}, Action: golem.EventDeleted,
		CausationID: golem.CausationID{15: 2}, Ordinal: 1,
		RecordedAt: time.Unix(1, 0).UTC(), Generation: golem.SchemaDigest{31: 3},
		EventSchema: schema, HasEventSchema: true, ResolvedEventSchema: schema, ModelID: model,
	}, []any{"identity"}, &row)
	if err == nil {
		t.Fatal("deleted event accepted a public entity")
	}
}

func TestP7MutationPublisherRetryPathContainsNoApplicationCallback(t *testing.T) {
	path := filepath.Join("..", "event", "outbox", "publisher.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := "if publishErr != nil { if publisher.hooks.AfterPublishBeforeAck != nil"
	if strings.Contains(string(content), forbidden) {
		t.Fatal("publisher retry path invokes an application-owned callback")
	}
}

func TestP7MutationPostgresClaimsUseSkipLocked(t *testing.T) {
	path := filepath.Join("..", "provider", "postgresql", "event_delivery.go")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), " FOR UPDATE SKIP LOCKED LIMIT $1`") {
		t.Fatal("PostgreSQL concurrent claim does not fence candidates with SKIP LOCKED")
	}
}
