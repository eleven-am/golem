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
		if provider == policyir.ProviderSQLite {
			if !strings.HasPrefix(statements[0].SQL(), `INSERT INTO "main"."_golem_outbox"`) {
				t.Fatalf("SQLite system namespace=%q", statements[0].SQL())
			}
			if strings.Contains(statements[0].SQL(), "$1") || strings.Count(statements[0].SQL(), "?") != 26 {
				t.Fatalf("SQLite placeholders=%q", statements[0].SQL())
			}
			if _, ok := statements[0].Args()[12].(int64); !ok {
				t.Fatalf("SQLite recorded_at type=%T", statements[0].Args()[12])
			}
		} else {
			if !strings.HasPrefix(statements[0].SQL(), `INSERT INTO "_golem"."_golem_outbox"`) {
				t.Fatalf("PostgreSQL system namespace=%q", statements[0].SQL())
			}
			if !strings.Contains(statements[0].SQL(), "$1") || !strings.Contains(statements[0].SQL(), "$26") || strings.Contains(statements[0].SQL(), "?") {
				t.Fatalf("PostgreSQL placeholders=%q", statements[0].SQL())
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
