package fact

import (
	"bytes"
	"strings"
	"testing"
	"time"

	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
)

func TestRenderInsertsProviderPlaceholdersChunkingAndExactBytes(t *testing.T) {
	rows := []OutboxRow{testOutboxRow(1), testOutboxRow(2), testOutboxRow(3)}
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		statements, err := RenderInserts(provider, rows, OutboxColumnCount*2)
		if err != nil {
			t.Fatalf("provider %d render: %v", provider, err)
		}
		if len(statements) != 2 || len(statements[0].Args()) != 26 || len(statements[1].Args()) != 13 {
			t.Fatalf("provider %d chunks=%d args=(%d,%d)", provider, len(statements), len(statements[0].Args()), len(statements[1].Args()))
		}
		for index, statement := range statements {
			ordinal, positional := strings.Count(statement.SQL(), "$"), strings.Count(statement.SQL(), "?")
			bound, foreign := ordinal, positional
			if provider == policyir.ProviderSQLite {
				bound, foreign = positional, ordinal
			}
			if bound != len(statement.Args()) || foreign != 0 {
				t.Fatalf("provider %d chunk %d binds %d of %d args and emits %d foreign placeholders", provider, index, bound, len(statement.Args()), foreign)
			}
		}
		if provider == policyir.ProviderSQLite {
			if _, ok := statements[0].Args()[12].(int64); !ok {
				t.Fatalf("SQLite recorded_at type=%T", statements[0].Args()[12])
			}
		} else {
			if !strings.HasPrefix(statements[0].SQL(), `INSERT INTO "_golem"."_golem_outbox"`) {
				t.Fatalf("PostgreSQL system namespace=%q", statements[0].SQL())
			}
			if _, ok := statements[0].Args()[12].(time.Time); !ok {
				t.Fatalf("PostgreSQL recorded_at type=%T", statements[0].Args()[12])
			}
		}
		arguments := statements[0].Args()
		if got := arguments[10].([]byte); !bytes.Equal(got, rows[0].Metadata) {
			t.Fatalf("provider %d metadata=%x want=%x", provider, got, rows[0].Metadata)
		}
		arguments[10].([]byte)[0] ^= 0xff
		if bytes.Equal(arguments[10].([]byte), statements[0].Args()[10].([]byte)) == true {
			t.Fatal("Args returned aliased binary storage")
		}
	}
}

func TestRenderInsertsRejectsBelowOneRow(t *testing.T) {
	if _, err := RenderInserts(policyir.ProviderSQLite, []OutboxRow{testOutboxRow(1)}, OutboxColumnCount-1); err == nil {
		t.Fatal("parameter limit below one row was accepted")
	}
}

func TestRenderDeliveryInsertIsOneIdempotentCausalRowOnBothProviders(t *testing.T) {
	rows := []OutboxRow{testOutboxRow(1), testOutboxRow(2)}
	rows[0].RecordedAt = time.Unix(1_700_000_001, 987_654_321)
	rows[1].RecordedAt = time.Unix(1_700_000_000, 123_456_789)
	rows[1].EventID = "00000000-0000-0000-0000-000000000003"
	for _, provider := range []policyir.Provider{policyir.ProviderSQLite, policyir.ProviderPostgreSQL} {
		statement, err := RenderDeliveryInsertAt(provider, "closed_system", rows)
		if err != nil {
			t.Fatalf("provider %d: %v", provider, err)
		}
		if !strings.Contains(statement.SQL(), `"closed_system"."_golem_outbox_delivery"`) || len(statement.Args()) != 4 {
			t.Fatalf("provider %d statement=%q args=%#v", provider, statement.SQL(), statement.Args())
		}
		if !strings.Contains(statement.SQL(), `ON CONFLICT ("causation_id") DO NOTHING`) {
			t.Fatalf("provider %d delivery insert is not idempotent: %q", provider, statement.SQL())
		}
		ordinal, positional := strings.Count(statement.SQL(), "$"), strings.Count(statement.SQL(), "?")
		bound, foreign := ordinal, positional
		if provider == policyir.ProviderSQLite {
			bound, foreign = positional, ordinal
		}
		if bound != len(statement.Args()) || foreign != 0 {
			t.Fatalf("provider %d binds %d of %d args and emits %d foreign placeholders", provider, bound, len(statement.Args()), foreign)
		}
		if provider == policyir.ProviderSQLite {
			if got := statement.Args()[1]; got != rows[1].RecordedAt.UTC().Truncate(time.Microsecond).UnixMicro() {
				t.Fatalf("SQLite first_recorded_at=%v", got)
			}
		} else if got := statement.Args()[1].(time.Time); !got.Equal(rows[1].RecordedAt.UTC().Truncate(time.Microsecond)) {
			t.Fatalf("PostgreSQL first_recorded_at=%v", got)
		}
	}
	foreign := append([]OutboxRow(nil), rows...)
	foreign[1].CausationID = "00000000-0000-0000-0000-000000000004"
	if _, err := RenderDeliveryInsertAt(policyir.ProviderSQLite, "main", foreign); err == nil {
		t.Fatal("mixed causations produced one delivery row")
	}
}

func testOutboxRow(ordinal int64) OutboxRow {
	return OutboxRow{
		EventID:               "00000000-0000-0000-0000-000000000001",
		FactVersion:           int64(FormatVersion),
		CodecIdentity:         CodecIdentity,
		GenerationFingerprint: strings.Repeat("1", 64),
		ModelID:               strings.Repeat("2", 32),
		Action:                "created",
		AfterIdentity:         []byte{0x00, 0xff, byte(ordinal)},
		CausationID:           "00000000-0000-0000-0000-000000000002",
		TransactionOrdinal:    ordinal,
		Metadata:              []byte{0x47, 0x00, 0xff, byte(ordinal)},
		RecordedAt:            time.Unix(1_700_000_000, 123_456_789),
	}
}
