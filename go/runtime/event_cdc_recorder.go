package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	eventcdc "github.com/eleven-am/golem/go/internal/event/cdc"
	mutationdecode "github.com/eleven-am/golem/go/internal/mutation/decode"
	mutationfact "github.com/eleven-am/golem/go/internal/mutation/fact"
	policyir "github.com/eleven-am/golem/go/internal/policy/ir"
	"github.com/eleven-am/golem/go/internal/policy/schema"
	"github.com/jmoiron/sqlx"
)

type cdcOutboxExecer interface {
	sqlx.QueryerContext
	sqlx.ExecerContext
}

type cdcOutboxRecorder struct {
	registry  *schema.Registry
	history   mutationfact.HistoricalSchemaResolver
	database  *sqlx.DB
	provider  policyir.Provider
	namespace string
	limits    normalizedMutationLimits
	scan      func(interface{ Scan(...any) error }) (int, mutationfact.OutboxRow, error)
}

func (app *App[P, A]) cdcOutboxRecorder() (*cdcOutboxRecorder, error) {
	namespace, ok := app.registry.PhysicalSystemNamespace(app.eventProvider)
	if !ok || namespace == "" || strings.ContainsAny(string(namespace), "\x00\"") || app.database == nil {
		return nil, fmt.Errorf("P7_CDC_RECORD_CONFIG: system namespace or database is absent")
	}
	if app.provider != policyir.ProviderSQLite && app.provider != policyir.ProviderPostgreSQL {
		return nil, fmt.Errorf("P7_CDC_RECORD_CONFIG: provider is unsupported")
	}
	return &cdcOutboxRecorder{registry: app.registry, history: app.eventSchemas, database: app.database, provider: app.provider, namespace: string(namespace), limits: app.mutationLimits, scan: app.scanDeleteFact}, nil
}

func (recorder *cdcOutboxRecorder) RecordCDC(ctx context.Context, causation golem.CausationID, changes []events.CDCChangeInput, build func([]int64) ([]mutationfact.OutboxRow, error)) error {
	if recorder == nil || recorder.database == nil || ctx == nil {
		return fmt.Errorf("P7_CDC_RECORD: recorder is incomplete")
	}
	if len(changes) == 0 || len(changes) > recorder.limits.facts {
		return events.Failure(events.CodeCDCInvalid)
	}
	return recorder.within(ctx, func(execer cdcOutboxExecer) error {
		stored, err := recorder.stored(ctx, execer, golem.UUID(causation).String())
		if err != nil {
			return err
		}
		var versions []int64
		if len(stored) != 0 {
			versions, err = recorder.storedVersions(stored, len(changes))
		} else {
			versions, err = recorder.stampVersions(ctx, execer, changes)
		}
		if err != nil {
			return err
		}
		rows, err := build(versions)
		if err != nil {
			return err
		}
		return recorder.insert(ctx, execer, rows)
	})
}

func (recorder *cdcOutboxRecorder) storedVersions(stored []mutationfact.OutboxRow, changes int) ([]int64, error) {
	if len(stored) != changes {
		return nil, events.Failure(events.CodeCDCInvalid)
	}
	versions := make([]int64, len(stored))
	for index, row := range stored {
		envelope, err := mutationfact.ValidateStoredRow(row, recorder.history)
		if err != nil {
			return nil, events.Failure(events.CodeCDCInvalid)
		}
		versions[index] = envelope.RowVersion()
	}
	return versions, nil
}

func (recorder *cdcOutboxRecorder) stampVersions(ctx context.Context, queryer sqlx.QueryerContext, changes []events.CDCChangeInput) ([]int64, error) {
	versions := make([]int64, len(changes))
	for index, change := range changes {
		model := policyir.ModelID(change.Model)
		if change.Action == golem.EventDeleted {
			before, err := decodeCDCExactImage(recorder.registry, model, change.Before)
			if err != nil || before == nil {
				return nil, events.Failure(events.CodeCDCInvalid)
			}
			version, err := mutationfact.RowVersionOf(recorder.registry, *before)
			if err != nil {
				return nil, events.Failure(events.CodeCDCInvalid)
			}
			versions[index] = version
			continue
		}
		after, err := decodeCDCExactImage(recorder.registry, model, change.After)
		if err != nil || after == nil {
			return nil, events.Failure(events.CodeCDCInvalid)
		}
		stamped, err := stampCascadeRows(ctx, queryer, recorder.registry, recorder.provider, recorder.limits, model, []mutationdecode.Row{*after})
		if err != nil {
			return nil, fmt.Errorf("P7_CDC_RECORD: stamp: %w", err)
		}
		if len(stamped) != 1 {
			return nil, fmt.Errorf("P7_CDC_RECORD: stamp: %d rows stamped for one change", len(stamped))
		}
		version, err := mutationfact.RowVersionOf(recorder.registry, stamped[0])
		if err != nil {
			return nil, fmt.Errorf("P7_CDC_RECORD: stamp: %w", err)
		}
		versions[index] = version
	}
	return versions, nil
}

func (recorder *cdcOutboxRecorder) insert(ctx context.Context, execer cdcOutboxExecer, rows []mutationfact.OutboxRow) error {
	if len(rows) == 0 || len(rows) > recorder.limits.facts {
		return events.Failure(events.CodeCDCInvalid)
	}
	causation := rows[0].CausationID
	total := 0
	for _, row := range rows {
		if row.CausationID != causation {
			return events.Failure(events.CodeCDCInvalid)
		}
		if row.EncodedBytes() > recorder.limits.outboxBytes-total {
			return events.Failure(events.CodeCDCInvalid)
		}
		total += row.EncodedBytes()
	}
	inserts, err := mutationfact.RenderIdempotentInsertsAt(recorder.provider, recorder.namespace, rows, recorder.limits.statementParameters)
	if err != nil {
		return events.Failure(events.CodeCDCInvalid)
	}
	delivered, err := mutationfact.RenderDeliveredInsertAt(recorder.provider, recorder.namespace, rows)
	if err != nil {
		return events.Failure(events.CodeCDCInvalid)
	}
	for _, statement := range append(inserts, delivered) {
		if _, err := execer.ExecContext(ctx, statement.SQL(), statement.Args()...); err != nil {
			return fmt.Errorf("P7_CDC_RECORD: insert: %w", err)
		}
	}
	stored, err := recorder.stored(ctx, execer, causation)
	if err != nil {
		return err
	}
	if !sameCDCOutboxRows(stored, rows) {
		return events.Failure(events.CodeCDCInvalid)
	}
	return nil
}

func (recorder *cdcOutboxRecorder) stored(ctx context.Context, queryer sqlx.QueryerContext, causation string) ([]mutationfact.OutboxRow, error) {
	mark := "?"
	if recorder.provider == policyir.ProviderPostgreSQL {
		mark = "$1"
	}
	query := `SELECT 0 AS "golem_check","event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at" FROM "` + recorder.namespace + `"."_golem_outbox" WHERE "causation_id" = ` + mark + ` ORDER BY "transaction_ordinal"`
	result, err := queryer.QueryxContext(ctx, query, causation)
	if err != nil {
		return nil, fmt.Errorf("P7_CDC_RECORD: read back: %w", err)
	}
	defer result.Close()
	var stored []mutationfact.OutboxRow
	for result.Next() {
		_, row, err := recorder.scan(result)
		if err != nil {
			return nil, fmt.Errorf("P7_CDC_RECORD: scan: %w", err)
		}
		stored = append(stored, row)
	}
	if err := result.Err(); err != nil {
		return nil, fmt.Errorf("P7_CDC_RECORD: read back: %w", err)
	}
	return stored, nil
}

func (recorder *cdcOutboxRecorder) within(ctx context.Context, run func(cdcOutboxExecer) error) error {
	if recorder.provider == policyir.ProviderPostgreSQL {
		transaction, err := recorder.database.BeginTxx(ctx, nil)
		if err != nil {
			return fmt.Errorf("P7_CDC_RECORD: begin: %w", err)
		}
		if err := run(transaction); err != nil {
			return errors.Join(err, ignoreTransactionDone(transaction.Rollback()))
		}
		return transaction.Commit()
	}
	connection, err := recorder.database.Connx(ctx)
	if err != nil {
		return fmt.Errorf("P7_CDC_RECORD: reserve connection: %w", err)
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = connection.Close()
		return fmt.Errorf("P7_CDC_RECORD: begin immediate: %w", err)
	}
	if err := run(connection); err != nil {
		rollbackErr := execMutationCleanup(ctx, connection, "ROLLBACK")
		return errors.Join(err, rollbackErr, connection.Close())
	}
	if err := commitSQLiteImmediate(ctx, connection, nil); err != nil {
		return errors.Join(err, execMutationCleanup(ctx, connection, "ROLLBACK"), connection.Close())
	}
	return nil
}

func sameCDCOutboxRows(stored, expected []mutationfact.OutboxRow) bool {
	if len(stored) != len(expected) {
		return false
	}
	for index, row := range expected {
		have := stored[index]
		if have.EventID != row.EventID || have.FactVersion != row.FactVersion || have.CodecIdentity != row.CodecIdentity ||
			have.GenerationFingerprint != row.GenerationFingerprint || have.ModelID != row.ModelID || have.Action != row.Action ||
			have.CausationID != row.CausationID || have.TransactionOrdinal != row.TransactionOrdinal ||
			!bytes.Equal(have.BeforeIdentity, row.BeforeIdentity) || !bytes.Equal(have.AfterIdentity, row.AfterIdentity) ||
			!bytes.Equal(have.Metadata, row.Metadata) || !bytes.Equal(have.DeleteSnapshot, row.DeleteSnapshot) ||
			!have.RecordedAt.Equal(row.RecordedAt) {
			return false
		}
	}
	return true
}

var _ eventcdc.Recorder = (*cdcOutboxRecorder)(nil)
